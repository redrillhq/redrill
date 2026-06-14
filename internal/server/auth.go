package server

import (
	"bufio"
	"bytes"
	"fmt"
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
