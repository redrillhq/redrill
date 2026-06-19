// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/notify"
	"github.com/redrillhq/redrill/internal/orchestrate"
	"github.com/redrillhq/redrill/internal/sandbox/docker"
	"github.com/redrillhq/redrill/internal/scheduler"
	"github.com/redrillhq/redrill/internal/server"
	"github.com/redrillhq/redrill/internal/store"
	"github.com/redrillhq/redrill/web"
)

func runServe(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", configFileDefault(), "config file path (or set $REDRILL_CONFIG)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(*path)
	if err == nil {
		err = extraValidate(cfg)
	}
	if err != nil {
		printConfigError(stderr, *path, err)
		return 3
	}

	// First signal cancels ctx; a second falls through to the default handler for a hard exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg, newLogger(stderr))
}

// The testable core of runServe; runs the scheduler loop until ctx is canceled.
func serve(ctx context.Context, cfg *config.Config, log *slog.Logger) int {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("cannot create data_dir", "dir", cfg.DataDir, "error", err.Error())
		return 2
	}
	// Startup uses a background context so a shutdown mid-boot can't abort a
	// half-built daemon; only the scheduler loop below honors ctx cancellation.
	startCtx := context.Background()
	st, err := store.Open(startCtx, filepath.Join(cfg.DataDir, "redrill.db"))
	if err != nil {
		log.Error("cannot open store", "error", err.Error())
		return 2
	}
	defer func() { _ = st.Close() }()

	notifier, err := notify.New(cfg.Notify.URLs, cfg.Notify.Events, log)
	if err != nil {
		log.Error("invalid notify config", "error", err.Error())
		return 3
	}

	clock := func() time.Time { return time.Now().UTC() }
	executor := buildExecutor(startCtx, cfg, log)
	if executor.sandbox != nil {
		defer func() { _ = executor.sandbox.Close() }()
	}

	o := orchestrate.New(st, executor.local, clock).WithLogger(log)
	al := newAlerter(notifier, st, cfg.Drills, clock)
	runOnce := func(rctx context.Context, d config.Drill, trigger store.Trigger) error {
		src, ok := findSource(cfg, d.Source)
		if !ok {
			return fmt.Errorf("drill %s: no such source %q", d.Name, d.Source)
		}
		res, err := o.Run(rctx, d, *src, orchestrate.RunOptions{Trigger: trigger, Scratch: cfg.Scratch})
		if err != nil {
			return err
		}
		log.Info("drill finished", "drill", d.Name, "result", string(res.Status),
			"level", res.LevelReached, "run_id", res.RunID, "trigger", string(trigger))
		al.afterRun(rctx, d, res)
		return nil
	}
	runFunc := func(rctx context.Context, d config.Drill) error { return runOnce(rctx, d, store.TriggerSchedule) }

	// One single-flight gate, shared between the scheduler and API-triggered runs
	// so a manual "Run now" never overlaps a scheduled run (DESIGN §9.6).
	gate := make(chan struct{}, cfg.Concurrency)
	var apiWG sync.WaitGroup
	apiTrigger := func(name string) error {
		d, _, ok := findDrill(cfg, name)
		if !ok {
			return fmt.Errorf("no such drill %q", name)
		}
		select {
		case gate <- struct{}{}:
		default:
			return server.ErrBusy
		}
		apiWG.Add(1)
		go func() {
			defer apiWG.Done()
			defer func() { <-gate }()
			rctx := ctx
			if to := d.Timeout.Duration(); to > 0 {
				var cancel context.CancelFunc
				rctx, cancel = context.WithTimeout(ctx, to)
				defer cancel()
			}
			if err := runOnce(rctx, *d, store.TriggerAPI); err != nil {
				log.Error("api-triggered drill failed", "drill", name, "error", err.Error())
			}
		}()
		return nil
	}

	hcURL := cfg.Notify.HealthchecksURL
	onCycle := func() {
		if hcURL != "" {
			go pingHealthchecks(ctx, hcURL, log)
		}
	}
	sched, err := scheduler.New(cfg.Drills, runFunc, scheduler.Options{
		Concurrency: cfg.Concurrency, Logger: log, Gate: gate, OnCycle: onCycle,
	})
	if err != nil {
		log.Error("invalid schedule", "error", err.Error())
		return 3
	}

	httpSrv, code := startHTTP(ctx, cfg, st, clock, apiTrigger, log)
	if code != 0 {
		return code
	}

	// The staleness sweeper runs alongside the scheduler; both stop on ctx cancel.
	var wg sync.WaitGroup
	if al.active() {
		wg.Add(1)
		go func() { defer wg.Done(); al.runSweeps(ctx) }()
	}

	log.Info("redrill serving", "drills", len(cfg.Drills), "concurrency", cfg.Concurrency)
	runErr := sched.Run(ctx)

	// Stop accepting new HTTP triggers, then drain in-flight API runs before the
	// deferred store close so no background run writes to a closed store.
	if httpSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
	}
	apiWG.Wait()
	wg.Wait()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Error("scheduler stopped with error", "error", runErr.Error())
		return 2
	}
	log.Info("redrill stopped")
	return 0
}

// startHTTP builds and starts the HTTP API server when server.listen is set,
// returning the server (nil if disabled) and a non-zero exit code on failure.
func startHTTP(ctx context.Context, cfg *config.Config, st *store.Store, clock func() time.Time, trigger server.TriggerFunc, log *slog.Logger) (*http.Server, int) {
	if cfg.Server.Listen == "" {
		log.Info("http api disabled (no server.listen configured)")
		return nil, 0
	}
	ui, err := web.Assets()
	if err != nil {
		log.Warn("ui assets unavailable; serving API only", "error", err.Error())
		ui = nil
	}
	srv, err := server.New(server.Options{Store: st, Config: cfg, Now: clock, Trigger: trigger, UI: ui, Logger: log})
	if err != nil {
		log.Error("invalid server config", "error", err.Error())
		return nil, 3
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Server.Listen)
	if err != nil {
		log.Error("cannot listen", "addr", cfg.Server.Listen, "error", err.Error())
		return nil, 2
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("http api listening", "addr", cfg.Server.Listen)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped with error", "error", err.Error())
		}
	}()
	return httpSrv, 0
}

// pingHealthchecks sends a bounded dead-man heartbeat GET; failures are logged,
// never fatal — a flaky monitor must not affect drills.
func pingHealthchecks(ctx context.Context, url string, log *slog.Logger) {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pingCtx, http.MethodGet, url, nil)
	if err != nil {
		log.Warn("healthcheck ping: bad url", "url", url, "error", err.Error())
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("healthcheck ping failed", "error", err.Error())
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// executorBundle keeps the sandbox handle alongside the executor so serve can close it.
type executorBundle struct {
	local   *exec.LocalExecutor
	sandbox *docker.Runtime
}

// buildExecutor wires the Docker sandbox when a container engine is reachable,
// else L3 skips.
func buildExecutor(ctx context.Context, cfg *config.Config, log *slog.Logger) executorBundle {
	host, _ := os.Hostname()
	local := exec.NewLocal(host).WithIOPolicy(ioPolicy(cfg))
	rt, err := docker.NewRuntime(ctx)
	if err != nil {
		log.Info("no container runtime reachable; L3 drills will be skipped", "error", err.Error())
		return executorBundle{local: local}
	}
	if n, jerr := rt.Janitor(ctx); jerr != nil {
		log.Warn("sandbox janitor failed", "error", jerr.Error())
	} else if n > 0 {
		log.Info("reaped orphaned sandbox containers", "count", n)
	}
	local.WithSandbox(rt)
	return executorBundle{local: local, sandbox: rt}
}

// Human text on a TTY, JSON otherwise.
func newLogger(w io.Writer) *slog.Logger {
	if f, ok := w.(*os.File); ok && isTTY(f) {
		return slog.New(slog.NewTextHandler(w, nil))
	}
	return slog.New(slog.NewJSONHandler(w, nil))
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
