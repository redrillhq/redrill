package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/notify"
	"github.com/alyamovsky/redrill/internal/orchestrate"
	"github.com/alyamovsky/redrill/internal/sandbox/docker"
	"github.com/alyamovsky/redrill/internal/scheduler"
	"github.com/alyamovsky/redrill/internal/store"
)

func runServe(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", defaultConfigPath, "config file path")
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
		fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", *path, err)
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
	runFunc := func(rctx context.Context, d config.Drill) error {
		src, ok := findSource(cfg, d.Source)
		if !ok {
			return fmt.Errorf("drill %s: no such source %q", d.Name, d.Source)
		}
		res, err := o.Run(rctx, d, *src, orchestrate.RunOptions{Trigger: store.TriggerSchedule, Scratch: cfg.Scratch})
		if err != nil {
			return err
		}
		log.Info("drill finished", "drill", d.Name, "result", string(res.Status),
			"level", res.LevelReached, "run_id", res.RunID)
		al.afterRun(rctx, d, res)
		return nil
	}

	sched, err := scheduler.New(cfg.Drills, runFunc, scheduler.Options{Concurrency: cfg.Concurrency, Logger: log})
	if err != nil {
		log.Error("invalid schedule", "error", err.Error())
		return 3
	}

	// The staleness sweeper runs alongside the scheduler; both stop on ctx cancel.
	var wg sync.WaitGroup
	if al.active() {
		wg.Add(1)
		go func() { defer wg.Done(); al.runSweeps(ctx) }()
	}

	log.Info("redrill serving", "drills", len(cfg.Drills), "concurrency", cfg.Concurrency)
	runErr := sched.Run(ctx)
	wg.Wait()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Error("scheduler stopped with error", "error", runErr.Error())
		return 2
	}
	log.Info("redrill stopped")
	return 0
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
