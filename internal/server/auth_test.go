// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func writeHtpasswd(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".htpasswd")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bcryptHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func TestLoadHtpasswd(t *testing.T) {
	t.Parallel()
	path := writeHtpasswd(t, "# a comment\n\nadmin:"+bcryptHash(t, "pw")+"\n")
	a, err := loadHtpasswd(path)
	if err != nil {
		t.Fatalf("loadHtpasswd: %v", err)
	}
	if !a.check("admin", "pw") {
		t.Error("valid credentials rejected")
	}
	if a.check("admin", "nope") {
		t.Error("wrong password accepted")
	}
	if a.check("ghost", "pw") {
		t.Error("unknown user accepted")
	}
}

func TestLoadHtpasswdErrors(t *testing.T) {
	t.Parallel()
	hash := bcryptHash(t, "pw")
	cases := map[string]string{
		"unsupported apr1 hash": "admin:$apr1$abc$def\n",
		"plaintext":             "admin:plaintext\n",
		"no colon":              "adminhash\n",
		"empty user":            ":" + hash + "\n",
		"empty hash":            "admin:\n",
		"no entries":            "# only a comment\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := loadHtpasswd(writeHtpasswd(t, content)); err == nil {
				t.Errorf("want error for %q", name)
			}
		})
	}
	if _, err := loadHtpasswd(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("want error for a missing file")
	}
}
