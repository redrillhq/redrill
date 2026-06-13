package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Placeholder replaces every redacted secret in captured output.
const Placeholder = "[REDACTED]"

// secretEnvName matches environment-variable names whose values are secrets and
// must be scrubbed from captured output (DESIGN §9.7, "*_PASSWORD-style env").
// Distinctive tokens match as substrings (catching PGPASSWORD); KEY only matches
// at a separator boundary so MONKEY_MODE / KEYBOARD are left alone.
var secretEnvName = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASSPHRASE|SECRET|TOKEN|CREDENTIALS?|API_?KEY|(^|[_-])KEY([_-]|$))`)

// Redactor scrubs registered secret values from any captured output before it
// becomes evidence or logs — the mandatory boundary of DESIGN §9.7. Safe
// for concurrent use. The zero value is usable and redacts nothing until
// secrets are registered.
type Redactor struct {
	mu       sync.Mutex
	secrets  map[string]struct{}
	replacer *strings.Replacer // cached; nil when stale or empty
}

// New returns a Redactor seeded with the given literal secret values.
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
	r.replacer = nil // rebuilt lazily on next Redact
}

// AddEnv registers value as a secret iff name looks secret-bearing
// (*_PASSWORD-style). Non-secret vars are left alone so useful diagnostics
// (PATH, PGHOST, …) survive in evidence.
func (r *Redactor) AddEnv(name, value string) {
	if secretEnvName.MatchString(name) {
		r.AddSecret(value)
	}
}

// Redact returns s with every registered secret value replaced by Placeholder.
func (r *Redactor) Redact(s string) string {
	r.mu.Lock()
	rep := r.replacerLocked()
	r.mu.Unlock()
	if rep == nil {
		return s
	}
	return rep.Replace(s)
}

// RedactBytes is Redact for byte slices.
func (r *Redactor) RedactBytes(b []byte) []byte {
	return []byte(r.Redact(string(b)))
}

// replacerLocked returns a Replacer for the current secret set, building it if
// stale. Secrets are ordered longest-first so a shorter secret that is a
// substring of a longer one can't pre-empt the longer match. Caller holds r.mu.
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
