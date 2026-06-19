// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server is the Phase 2 HTTP surface: REST API, metrics, and web UI.
//
// It is a thin, read-only window over the store and config, plus exactly one
// mutating endpoint (the run trigger). Drills, runs, and evidence are served
// from the store; the trigger is an injected capability so this package never
// imports the orchestrator or executor.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/store"
)

// ErrBusy is returned by a TriggerFunc when a run already holds the single-flight
// slot; the API maps it to 409 Conflict.
var ErrBusy = errors.New("a run is already in flight")

// TriggerFunc starts a drill out of band (the UI's "Run now"). It returns once
// the run has been accepted (started in the background) or rejected with ErrBusy;
// it never blocks for the run's duration. nil means triggering is disabled.
type TriggerFunc func(drill string) error

// Options configure a Server. Store and Config are required.
type Options struct {
	Store   *store.Store
	Config  *config.Config
	Now     func() time.Time // injected clock (UTC); defaults to time.Now().UTC
	Trigger TriggerFunc      // nil disables POST .../run (503)
	UI      fs.FS            // built SPA assets (rooted at index.html); nil serves API only
	Logger  *slog.Logger
}

type Server struct {
	store   *store.Store
	cfg     *config.Config
	now     func() time.Time
	trigger TriggerFunc
	auth    *basicAuth    // nil when no basic auth (file or env) is configured
	apiKeys *apiKeys      // nil when no api_keys_env is configured
	limiter *rate.Limiter // gates the mutating trigger endpoint
	ui      fs.FS         // nil when no UI was embedded/injected
	uiFiles http.Handler  // static file server over ui
	uiIndex []byte        // cached index.html for the SPA fallback
	log     *slog.Logger
}

// New builds a Server, loading the htpasswd basic-auth file if one is configured.
// A bad auth file is a configuration error.
func New(opts Options) (*Server, error) {
	if opts.Store == nil || opts.Config == nil {
		return nil, errors.New("server: Store and Config are required")
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var auth *basicAuth
	if path := opts.Config.Server.BasicAuthFile; path != "" {
		a, err := loadHtpasswd(path)
		if err != nil {
			return nil, err
		}
		auth = a
	}
	if name := opts.Config.Server.BasicAuthEnv; name != "" {
		content := os.Getenv(name)
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("basic_auth_env: $%s is empty or unset", name)
		}
		envAuth, err := basicAuthFromEnv(content)
		if err != nil {
			return nil, err
		}
		if auth == nil {
			auth = envAuth
		} else {
			for u, h := range envAuth.users {
				auth.users[u] = h
			}
		}
	}
	var keys *apiKeys
	if name := opts.Config.Server.APIKeysEnv; name != "" {
		content := os.Getenv(name)
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("api_keys_env: $%s is empty or unset", name)
		}
		k, err := apiKeysFromEnv(content)
		if err != nil {
			return nil, err
		}
		keys = k
	}
	var uiIndex []byte
	var uiFiles http.Handler
	if opts.UI != nil {
		b, err := fs.ReadFile(opts.UI, "index.html")
		if err != nil {
			return nil, fmt.Errorf("server: UI assets missing index.html: %w", err)
		}
		uiIndex = b
		uiFiles = http.FileServerFS(opts.UI)
	}
	return &Server{
		store:   opts.Store,
		cfg:     opts.Config,
		now:     now,
		trigger: opts.Trigger,
		auth:    auth,
		apiKeys: keys,
		// ~1 trigger/sec sustained, small burst — a manual "Run now" guard, not a
		// throughput limiter (single-flight already serializes the actual runs).
		limiter: rate.NewLimiter(rate.Every(time.Second), 5),
		ui:      opts.UI,
		uiFiles: uiFiles,
		uiIndex: uiIndex,
		log:     log,
	}, nil
}

// Handler builds the routed, middleware-wrapped HTTP handler. The API is gated by
// basic auth when configured; /healthz and /metrics stay open (liveness probes
// and Prometheus scrapes are infra endpoints on a trusted network).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// /healthz stays open even under auth_scope=all so liveness probes need no
	// credentials; /metrics and the UI are open by default but gated when
	// auth_scope=all extends basic auth to everything but liveness.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /metrics", s.gateExtra(s.handleMetrics))
	mux.HandleFunc("GET /api/v1/drills", s.requireAuth(s.handleDrills))
	mux.HandleFunc("GET /api/v1/drills/{name}/runs", s.requireAuth(s.handleDrillRuns))
	mux.HandleFunc("GET /api/v1/runs/{id}", s.requireAuth(s.handleRun))
	mux.HandleFunc("POST /api/v1/drills/{name}/run", s.requireAuth(s.rateLimit(s.handleTrigger)))
	if s.ui != nil {
		// Catch-all for the SPA; the specific /api, /healthz, /metrics patterns
		// above win by ServeMux's longest-pattern precedence.
		mux.HandleFunc("GET /", s.gateExtra(s.handleUI))
	}
	return s.recoverer(s.logRequests(mux))
}

// gateExtra applies basic auth to /metrics and the UI only when auth_scope=all;
// otherwise they stay open (Prometheus scrapes, anonymous dashboard shell).
func (s *Server) gateExtra(h http.HandlerFunc) http.HandlerFunc {
	if s.cfg.Server.AuthScope == "all" {
		return s.requireAuth(h)
	}
	return h
}

// requireAuth enforces a valid API key or basic-auth credential; a pass-through
// when no auth is configured (the localhost default).
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	if s.auth == nil && s.apiKeys == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticate(r) {
			// Basic realm so the browser UI still gets a native login prompt; bearer
			// clients just read the 401.
			w.Header().Set("WWW-Authenticate", `Basic realm="redrill"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r)
	}
}

// rateLimit guards the mutating trigger endpoint.
func (s *Server) rateLimit(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.Allow() {
			writeError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		h(w, r)
	}
}

// recoverer turns a handler panic into a 500 instead of crashing the daemon.
func (s *Server) recoverer(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in http handler", "path", r.URL.Path, "panic", v)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		h.ServeHTTP(w, r)
	})
}

// logRequests logs each request at debug with its status and duration.
func (s *Server) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(sw, r)
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "ms", s.now().Sub(start).Milliseconds())
	})
}

// statusWriter captures the response status for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // keep expect predicates ("> 0", "age < 8d") readable in evidence
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
