// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package redact

import (
	"strings"
	"testing"
)

// No registered secret may survive a capture path. The secret is embedded in
// arbitrary surrounding text and must be gone from the redacted output.
func FuzzRedactNoSecretSurvives(f *testing.F) {
	f.Add("hunter2", "user=admin password=hunter2\nhost=db")
	f.Add("s3cr3t", "prefix s3cr3ts3cr3t suffix")
	f.Add("p@ss w0rd", "json {\"pw\":\"p@ss w0rd\"}")
	f.Add("", "ignored")

	f.Fuzz(func(t *testing.T, secret, surround string) {
		if strings.TrimSpace(secret) == "" {
			return // empty/whitespace secrets are ignored by design
		}
		// Skip secrets sharing a character with the fixed placeholder: then
		// "[REDACTED]" could itself contain the secret as a substring, a
		// meaningless confound. For every other secret the guarantee is total —
		// no placeholder character can recombine into the secret.
		if strings.ContainsAny(secret, "[REDACT]") {
			return
		}
		out := New(secret).Redact(surround + secret + surround)
		if strings.Contains(out, secret) {
			t.Fatalf("registered secret %q survived redaction: %q", secret, out)
		}
	})
}
