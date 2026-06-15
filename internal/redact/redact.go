// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

const Placeholder = "[REDACTED]"

// secretEnvName matches env-var names whose values are secrets. Distinctive
// tokens match as substrings (catching PGPASSWORD); KEY only matches at a
// separator boundary so MONKEY_MODE / KEYBOARD are left alone.
var secretEnvName = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASSPHRASE|SECRET|TOKEN|CREDENTIALS?|API_?KEY|(^|[_-])KEY([_-]|$))`)

// Safe for concurrent use; the zero value is usable and redacts nothing until
// secrets are registered.
type Redactor struct {
	mu       sync.Mutex
	secrets  map[string]struct{}
	replacer *strings.Replacer // nil when stale or empty
}

func New(secrets ...string) *Redactor {
	r := &Redactor{}
	for _, s := range secrets {
		r.AddSecret(s)
	}
	return r
}

// AddSecret registers a literal secret value to scrub. Empty or whitespace-only
// values are ignored so redaction can never blank out all output.
func (r *Redactor) AddSecret(value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secrets == nil {
		r.secrets = make(map[string]struct{})
	}
	if _, ok := r.secrets[value]; ok {
		return
	}
	r.secrets[value] = struct{}{}
	r.replacer = nil
}

// AddEnv registers value as a secret iff name looks secret-bearing. Non-secret
// vars are left alone so useful diagnostics (PATH, PGHOST, …) survive.
func (r *Redactor) AddEnv(name, value string) {
	if secretEnvName.MatchString(name) {
		r.AddSecret(value)
	}
}

func (r *Redactor) Redact(s string) string {
	r.mu.Lock()
	rep := r.replacerLocked()
	r.mu.Unlock()
	if rep == nil {
		return s
	}
	return rep.Replace(s)
}

func (r *Redactor) RedactBytes(b []byte) []byte {
	return []byte(r.Redact(string(b)))
}

// replacerLocked builds (if stale) and returns the Replacer. Secrets are
// ordered longest-first so a shorter secret that is a substring of a longer one
// can't pre-empt it. Caller holds r.mu.
func (r *Redactor) replacerLocked() *strings.Replacer {
	if r.replacer != nil || len(r.secrets) == 0 {
		return r.replacer
	}
	vals := make([]string, 0, len(r.secrets))
	for v := range r.secrets {
		vals = append(vals, v)
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	pairs := make([]string, 0, len(vals)*2)
	for _, v := range vals {
		pairs = append(pairs, v, Placeholder)
	}
	r.replacer = strings.NewReplacer(pairs...)
	return r.replacer
}
