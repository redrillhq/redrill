package server

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// basicAuth verifies credentials against an htpasswd file. Only bcrypt entries
// (htpasswd -B) are supported; weaker schemes are rejected at load so an operator
// is never silently running with an insecure hash. Basic auth is a convenience —
// the threat model recommends a reverse proxy for TLS (DESIGN §8.2/§9.7).
type basicAuth struct {
	users map[string][]byte // username -> bcrypt hash
}

func loadHtpasswd(path string) (*basicAuth, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: basic_auth_file is operator-supplied via config, by design
	if err != nil {
		return nil, fmt.Errorf("read basic_auth_file %s: %w", path, err)
	}
	users := map[string][]byte{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		user, hash, ok := strings.Cut(t, ":")
		if !ok || user == "" || hash == "" {
			return nil, fmt.Errorf("basic_auth_file %s line %d: not a user:hash entry", path, line)
		}
		if !isBcrypt(hash) {
			return nil, fmt.Errorf("basic_auth_file %s line %d: user %q uses an unsupported hash (only bcrypt / htpasswd -B is supported)", path, line, user)
		}
		users[user] = []byte(hash)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read basic_auth_file %s: %w", path, err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("basic_auth_file %s has no usable entries", path)
	}
	return &basicAuth{users: users}, nil
}

func isBcrypt(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")
}

// basicAuthFromEnv builds a basicAuth from plaintext "user:password" lines. Env is
// already the runtime trust boundary (like the engine secret *_env refs), so
// passwords arrive in the clear and are bcrypt-hashed in memory here — the stored
// shape is identical to the file form, so check() is unchanged.
func basicAuthFromEnv(content string) (*basicAuth, error) {
	users := map[string][]byte{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		user, pass, ok := strings.Cut(t, ":")
		if !ok || user == "" || pass == "" {
			return nil, fmt.Errorf("basic_auth_env: entry is not user:password")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("basic_auth_env: hashing %q: %w", user, err)
		}
		users[user] = hash
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("basic_auth_env: no user:password entries")
	}
	return &basicAuth{users: users}, nil
}

// apiKeys is a set of bearer tokens for programmatic clients (scripts, Prometheus,
// MCP). Multiple keys are independent credentials with identical access — not roles.
type apiKeys struct {
	keys [][]byte
}

func apiKeysFromEnv(content string) (*apiKeys, error) {
	var keys [][]byte
	for _, tok := range strings.FieldsFunc(content, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	}) {
		keys = append(keys, []byte(tok))
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("api_keys_env: no keys")
	}
	return &apiKeys{keys: keys}, nil
}

// valid reports whether token matches any configured key, compared in constant time
// (no early return) so a near-miss can't be found by timing.
func (k *apiKeys) valid(token string) bool {
	if token == "" {
		return false
	}
	b := []byte(token)
	matched := 0
	for _, key := range k.keys {
		matched |= subtle.ConstantTimeCompare(key, b)
	}
	return matched == 1
}

// bearerToken reads an API key from the Authorization: Bearer header or X-API-Key.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// authenticate reports whether the request carries a valid API key or basic-auth
// credential.
func (s *Server) authenticate(r *http.Request) bool {
	if s.apiKeys != nil && s.apiKeys.valid(bearerToken(r)) {
		return true
	}
	if s.auth != nil {
		if user, pass, ok := r.BasicAuth(); ok && s.auth.check(user, pass) {
			return true
		}
	}
	return false
}

// check reports whether user/pass match a stored bcrypt entry. bcrypt's compare
// is itself constant-time; an unknown user still pays a compare against a dummy
// hash so presence isn't leaked by response timing.
func (a *basicAuth) check(user, pass string) bool {
	hash, ok := a.users[user]
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pass))
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(pass)) == nil
}

// dummyHash is a valid bcrypt hash of a random value, used to equalize timing for
// unknown usernames. It never matches a real password.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")
