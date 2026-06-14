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
	"syscall"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/orchestrate"
	"github.com/alyamovsky/redrill/internal/sandbox/docker"
	"github.com/alyamovsky/redrill/internal/scheduler"
	"github.com/alyamovsky/redrill/internal/store"
)

// runServe loads and validates the config, then hands off to serve under a
// context canceled by SIGINT/SIGTERM. No HTTP yet (Phase 2).
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
		err = checkSchedules(cfg)
	}
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", *path, err)
		return 3
	}

	// Cancel the daemon context on the first interrupt/terminate signal; a second
	// signal falls through to the default handler for a hard exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg, newLogger(stderr))
}

// serve runs the scheduler loop until ctx is canceled. It is the testable core
// of runServe (no signal wiring), returning a process exit code.
func serve(ctx context.Context, cfg *config.Config, log *slog.Logger) int {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("cannot create data_dir", "dir", cfg.DataDir, "error", err.Error())
		return 2
	}
	// Startup (store, container runtime) runs under a background context: a
	// shutdown signal arriving mid-boot must not abort a half-built daemon. Only
	// the scheduler loop below — and the runs it drives — honor ctx cancellation.
	startCtx := context.Background()
	st, err := store.Open(startCtx, filepath.Join(cfg.DataDir, "redrill.db"))
	if err != nil {
		log.Error("cannot open store", "error", err.Error())
		return 2
	}
	defer func() { _ = st.Close() }()

	executor := buildExecutor(startCtx, log)
	if executor.sandbox != nil {
		defer func() { _ = executor.sandbox.Close() }()
	}

	o := orchestrate.New(st, executor.local, func() time.Time { return time.Now().UTC() })
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
		return nil
	}

	sched, err := scheduler.New(cfg.Drills, runFunc, scheduler.Options{Concurrency: cfg.Concurrency, Logger: log})
	if err != nil {
		log.Error("invalid schedule", "error", err.Error())
		return 3
	}

	log.Info("redrill serving", "drills", len(cfg.Drills), "concurrency", cfg.Concurrency)
	if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("scheduler stopped with error", "error", err.Error())
		return 2
	}
	log.Info("redrill stopped")
	return 0
}

// executorBundle keeps the sandbox runtime handle alongside the executor so
// serve can close it on shutdown.
type executorBundle struct {
	local   *exec.LocalExecutor
	sandbox *docker.Runtime
}

// buildExecutor wires a LocalExecutor with the Docker sandbox runtime when a
// container engine is reachable; without one, L3 degrades to skipped. The
// startup janitor reaps containers left by crashed runs (DESIGN §9.5).
func buildExecutor(ctx context.Context, log *slog.Logger) executorBundle {
	host, _ := os.Hostname()
	local := exec.NewLocal(host)
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

// newLogger builds the slog logger: human text on a TTY, JSON otherwise
// (DEVELOPMENT.md logging convention).
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
