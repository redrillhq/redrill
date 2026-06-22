// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/redrillhq/redrill/internal/config"
)

// TestGenerateConfigValidates is the trust guarantee: across the whole input
// matrix, generated config parses and validates (generateConfig gates on it, so
// a nil error already proves validity; we re-Parse to be explicit) and carries
// the right per-type levels.
func TestGenerateConfigValidates(t *testing.T) {
	for _, typ := range []string{"dumpdir", "borg", "restic"} {
		for _, target := range []string{"docker", "host", "local"} {
			for _, l3 := range []bool{true, false} {
				o := genOptions{Target: target, SourceType: typ, L3: l3}
				switch typ {
				case "dumpdir":
					o.Path = "/backups/pg"
				case "borg":
					o.Repo, o.PassphraseFile = "ssh://b@h/./r", "/etc/redrill/secrets/p"
				case "restic":
					o.Repo, o.PasswordFile = "s3:s3.example.com/b", "/etc/redrill/secrets/p"
				}
				if l3 && typ != "dumpdir" {
					o.ExtractPath = "db/dump.sql"
				}
				resolveDefaults(&o)

				b, err := generateConfig(o)
				if err != nil {
					t.Fatalf("%s/%s/l3=%v: generateConfig: %v", typ, target, l3, err)
				}
				if _, err := config.Parse(b); err != nil {
					t.Fatalf("%s/%s/l3=%v: emitted config does not Parse: %v\n%s", typ, target, l3, err, b)
				}
				s := string(b)
				wantData := "/var/lib/redrill"
				if target == "local" {
					home, _ := os.UserHomeDir()
					wantData = filepath.Join(home, ".local/share/redrill")
				}
				mustContain(t, s, "data_dir: "+wantData)
				wantL1 := "file_min_bytes"
				if typ != "dumpdir" {
					wantL1 = "native_check"
				}
				mustContain(t, s, wantL1)
				if typ != "dumpdir" {
					mustContain(t, s, "l2:") // borg/restic get the L2 restore proof
				}
				if l3 {
					mustContain(t, s, "sql_no_error")
					if typ != "dumpdir" {
						mustContain(t, s, "extract_path")
					}
				} else if strings.Contains(s, "l3:") {
					t.Errorf("%s/%s/l3=false: emitted an l3 block", typ, target)
				}
			}
		}
	}
}

// TestGenerateConfigSecretSafety: secret-bearing fields appear only as *_file
// references, never as an inline key (init never receives a secret value).
func TestGenerateConfigSecretSafety(t *testing.T) {
	o := genOptions{
		Target: "host", SourceType: "borg", Repo: "ssh://b@h/./r",
		PassphraseFile: "/s/pass", SSHKeyFile: "/s/key", L3: true, ExtractPath: "d.sql",
	}
	resolveDefaults(&o)
	b, err := generateConfig(o)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	mustContain(t, s, "passphrase_file:")
	mustContain(t, s, "ssh_key_file:")
	// No inline secret keys (a passphrase:/password: line not suffixed by _file/_env).
	for _, re := range []string{`(?m)^\s*passphrase:`, `(?m)^\s*password:`} {
		if regexp.MustCompile(re).MatchString(s) {
			t.Errorf("inline secret key matched %q in:\n%s", re, s)
		}
	}
}

func TestFromFlags(t *testing.T) {
	cases := []struct {
		name string
		o    genOptions
		want int
	}{
		{"missing target", genOptions{SourceType: "dumpdir", Path: "/x"}, 2},
		{"bad target", genOptions{Target: "k8s", SourceType: "dumpdir", Path: "/x"}, 2},
		{"bad type", genOptions{Target: "docker", SourceType: "mysql"}, 2},
		{"dumpdir ok", genOptions{Target: "docker", SourceType: "dumpdir", Path: "/x"}, 0},
		{"dumpdir missing path", genOptions{Target: "docker", SourceType: "dumpdir"}, 2},
		{"borg missing passphrase", genOptions{Target: "docker", SourceType: "borg", Repo: "r"}, 2},
		{"borg ok no l3", genOptions{Target: "docker", SourceType: "borg", Repo: "r", PassphraseFile: "/p"}, 0},
		{"borg l3 missing extract", genOptions{Target: "docker", SourceType: "borg", Repo: "r", PassphraseFile: "/p", L3: true}, 2},
		{"borg l3 ok", genOptions{Target: "docker", SourceType: "borg", Repo: "r", PassphraseFile: "/p", L3: true, ExtractPath: "d.sql"}, 0},
		{"restic missing password", genOptions{Target: "docker", SourceType: "restic", Repo: "r"}, 2},
		{"restic ok", genOptions{Target: "host", SourceType: "restic", Repo: "r", PasswordFile: "/p"}, 0},
		{"local ok", genOptions{Target: "local", SourceType: "dumpdir", Path: "/x"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := c.o
			if got := fromFlags(io.Discard, &o); got != c.want {
				t.Fatalf("fromFlags = %d, want %d", got, c.want)
			}
		})
	}
}

func TestWizard(t *testing.T) {
	// dumpdir happy path: docker target (so the path check is skipped), answer
	// the required path, accept every default. Name is asked last and derived.
	in := strings.NewReader(strings.Join([]string{
		"docker",      // target
		"dumpdir",     // type
		"/backups/pg", // path (required)
		"",            // pattern → default
		"y",           // L3 (dumpdir default is yes)
		"",            // pg image → default
		"",            // schedule → default
		"",            // max proof age → default
		"",            // name → derived from the path
	}, "\n") + "\n")

	var o genOptions
	if _, code := wizard(in, io.Discard, &o); code != 0 {
		t.Fatalf("wizard code = %d, want 0", code)
	}
	if o.Target != "docker" || o.SourceType != "dumpdir" || o.Path != "/backups/pg" {
		t.Fatalf("unexpected answers: %+v", o)
	}
	if o.DrillName != "pg" || o.SourceName != "pg-backups" {
		t.Errorf("derived names wrong: drill=%q source=%q (want pg / pg-backups)", o.DrillName, o.SourceName)
	}
	if !o.L3 {
		t.Error("L3 should default on for a dumpdir")
	}
	resolveDefaults(&o)
	if _, err := generateConfig(o); err != nil {
		t.Fatalf("wizard answers did not generate a valid config: %v", err)
	}
}

// A borg file backup scaffolds L1 + L2 (sample restore) and the wizard never
// mentions Postgres — L3 stays a flag-only opt-in.
func TestWizardBorgScaffoldsL2(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"host",           // target
		"borg",           // source
		"ssh://h/./repo", // repo (ssh:// skips the local check)
		"",               // passphrase: path → default (host is path-only)
		"",               // ssh key → blank
		"",               // schedule
		"",               // max proof age
		"",               // name → derived "repo"
	}, "\n") + "\n")

	var out bytes.Buffer
	var o genOptions
	if _, code := wizard(in, &out, &o); code != 0 {
		t.Fatalf("wizard code = %d, want 0", code)
	}
	if strings.Contains(out.String(), "Postgres") {
		t.Errorf("the borg wizard flow must not mention Postgres:\n%s", out.String())
	}
	if o.L3 {
		t.Error("L3 should be off for a borg file backup")
	}
	if o.DrillName != "repo" {
		t.Errorf("derived name = %q, want repo", o.DrillName)
	}
	resolveDefaults(&o)
	if !o.L2 {
		t.Error("borg should scaffold L2")
	}
	b, err := generateConfig(o)
	if err != nil {
		t.Fatalf("did not generate a valid L1+L2 borg config: %v", err)
	}
	s := string(b)
	mustContain(t, s, "l2:")
	mustContain(t, s, "min_total_bytes")
	if strings.Contains(s, "l3:") {
		t.Error("borg default should not include an l3 block")
	}
}

// A clearly-bad path is flagged, then accepted on override.
func TestWizardPathOverride(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"host", "dumpdir",
		"/no/such/dir/xyz", // fails validation
		"y",                // use it anyway
		"",                 // pattern
		"n",                // L3 off (keep the script short)
		"", "", "",         // schedule, max proof age, name
	}, "\n") + "\n")
	var o genOptions
	if _, code := wizard(in, io.Discard, &o); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if o.Path != "/no/such/dir/xyz" {
		t.Errorf("path = %q, want the overridden value", o.Path)
	}
}

// A clearly-bad path is flagged and re-prompted until a real directory is given,
// and the rejected value is NOT offered back as the re-prompt default.
func TestWizardPathReprompt(t *testing.T) {
	good := t.TempDir()
	in := strings.NewReader(strings.Join([]string{
		"host", "dumpdir",
		"/no/such/dir/xyz", // fails
		"n",                // don't use it → re-prompt
		good,               // a real directory → accepted
		"",                 // pattern
		"n",                // L3 off
		"", "", "",         // schedule, max proof age, name
	}, "\n") + "\n")
	var out bytes.Buffer
	var o genOptions
	if _, code := wizard(in, &out, &o); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if o.Path != good {
		t.Errorf("path = %q, want %q", o.Path, good)
	}
	if strings.Contains(out.String(), "[/no/such/dir/xyz]") {
		t.Error("re-prompt offered the rejected path as its default")
	}
}

// A relative path is resolved to absolute (init runs in the user's shell).
func TestWizardResolvesRelativePath(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"docker", "dumpdir",
		"./var/dumps", // relative → resolved (docker skips the existence check)
		"",            // pattern
		"n",           // L3 off
		"", "", "",    // schedule, max proof age, name
	}, "\n") + "\n")
	var o genOptions
	if _, code := wizard(in, io.Discard, &o); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !filepath.IsAbs(o.Path) {
		t.Errorf("path = %q, want absolute", o.Path)
	}
	if !strings.HasSuffix(o.Path, "/var/dumps") {
		t.Errorf("path = %q, want it to end with /var/dumps", o.Path)
	}
}

func TestDeriveBase(t *testing.T) {
	cases := []struct {
		o    genOptions
		want string
	}{
		{genOptions{SourceType: "dumpdir", Path: "/backups/pg"}, "pg"},
		{genOptions{SourceType: "borg", Repo: "/var/borg1"}, "borg1"},
		{genOptions{SourceType: "borg", Repo: "ssh://backup@nas.lan/./borg/nextcloud-aio"}, "nextcloud-aio"},
		{genOptions{SourceType: "restic", Repo: "s3:s3.example.com/bucket/photos"}, "photos"},
		{genOptions{SourceType: "dumpdir", Path: "/"}, "backup"},
	}
	for _, c := range cases {
		if got := deriveBase(c.o); got != c.want {
			t.Errorf("deriveBase(%+v) = %q, want %q", c.o, got, c.want)
		}
	}
}

func TestCheckLocalDir(t *testing.T) {
	dir := t.TempDir()
	borg := t.TempDir()
	if err := os.WriteFile(filepath.Join(borg, "config"), []byte("[repository]"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		target, p  string
		expectBorg bool
		wantMatch  string // substring; "" = no problem
	}{
		{"relative", "host", "./var/borg1", false, "absolute"},
		{"docker skips existence", "docker", "/whatever/missing", false, ""},
		{"host missing", "host", "/no/such/xyz", false, "no such directory"},
		{"host dir ok", "host", dir, false, ""},
		{"host borg without config", "host", dir, true, "borg repository"},
		{"host borg ok", "host", borg, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkLocalDir(c.target, c.p, c.expectBorg)
			if c.wantMatch == "" && got != "" {
				t.Errorf("got %q, want no problem", got)
			}
			if c.wantMatch != "" && !strings.Contains(got, c.wantMatch) {
				t.Errorf("got %q, want substring %q", got, c.wantMatch)
			}
		})
	}
}

func TestWizardAborted(t *testing.T) {
	// Input ends after the first answer: a required field is never given.
	var o genOptions
	if _, code := wizard(strings.NewReader("docker\n"), io.Discard, &o); code != 2 {
		t.Fatalf("wizard code = %d, want 2 (aborted)", code)
	}
}

func TestWriteConfigFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "config.yaml")
	if code := writeConfigFile(io.Discard, dest, []byte("a"), false); code != 0 {
		t.Fatalf("first write = %d, want 0", code)
	}
	if code := writeConfigFile(io.Discard, dest, []byte("b"), false); code != 2 {
		t.Fatalf("overwrite without force = %d, want 2", code)
	}
	if code := writeConfigFile(io.Discard, dest, []byte("c"), true); code != 0 {
		t.Fatalf("overwrite with force = %d, want 0", code)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != "c" {
		t.Fatalf("content = %q (err %v), want \"c\"", b, err)
	}
}

func TestAskWriteDest(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.yaml")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.yaml")

	cases := []struct {
		name, input, wantDest string
		wantOverwrite         bool
	}{
		{"blank → stdout", "\n", "", false},
		{"fresh path", fresh + "\n", fresh, false},
		{"existing + confirm", existing + "\ny\n", existing, true},
		{"existing + decline → fresh", existing + "\nn\n" + fresh + "\n", fresh, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &asker{in: bufio.NewReader(strings.NewReader(c.input)), out: io.Discard}
			d, ov := a.askWriteDest()
			if d != c.wantDest || ov != c.wantOverwrite {
				t.Errorf("= (%q, %v), want (%q, %v)", d, ov, c.wantDest, c.wantOverwrite)
			}
		})
	}
}

func TestEmit(t *testing.T) {
	yaml := []byte("version: 1\n")
	dir := t.TempDir()

	// no dest → stdout only
	var out, errb bytes.Buffer
	if code := emit(&out, &errb, yaml, "", false, false); code != 0 || out.String() != string(yaml) {
		t.Fatalf("stdout mode: code=%d out=%q", code, out.String())
	}

	// dest + echo (interactive): file written AND echoed to stdout
	dest := filepath.Join(dir, "a.yaml")
	out.Reset()
	if code := emit(&out, &errb, yaml, dest, true, false); code != 0 {
		t.Fatalf("write+echo: code=%d", code)
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != string(yaml) {
		t.Fatalf("file = %q (err %v)", b, err)
	}
	if out.String() != string(yaml) {
		t.Errorf("interactive write should echo to stdout, got %q", out.String())
	}

	// dest without echo (non-interactive): file only, stdout empty
	out.Reset()
	if code := emit(&out, &errb, yaml, filepath.Join(dir, "b.yaml"), false, false); code != 0 || out.Len() != 0 {
		t.Errorf("non-interactive write should not echo: code=%d out=%q", code, out.String())
	}
}

func TestPrintGuidanceTargets(t *testing.T) {
	var docker bytes.Buffer
	printGuidance(&docker, genOptions{Target: "docker", SourceType: "dumpdir", Path: "/backups/pg", L3: true, DrillName: "app-db"}, "config.yaml")
	ds := docker.String()
	mustContain(t, ds, "/backups/pg:/backups/pg:ro") // the matching compose mount line
	mustContain(t, ds, "docker.sock")                // L3 needs the socket
	mustContain(t, ds, "redrill validate -c config.yaml")

	var host bytes.Buffer
	printGuidance(&host, genOptions{Target: "host", SourceType: "borg", Repo: "r", PassphraseFile: "/s/p", L3: false, DrillName: "app-db"}, "config.yaml")
	hs := host.String()
	mustContain(t, hs, "deploy/README.md")
	mustContain(t, hs, "/s/p")                  // secret-file checklist
	mustContain(t, hs, "install -D -o redrill") // the systemd ownership note
	if strings.Contains(hs, "docker.sock") {
		t.Error("host guidance should not mention the docker socket")
	}

	var local bytes.Buffer
	printGuidance(&local, genOptions{Target: "local", SourceType: "dumpdir", Path: "/backups/pg", L3: false, DrillName: "app-db"}, "config.yaml")
	ls := local.String()
	mustContain(t, ls, "Local:")
	if strings.Contains(ls, "docker.sock") || strings.Contains(ls, "install -D -o redrill") {
		t.Error("local guidance should not mention docker socket or systemd install")
	}
}

func TestSecretAnswer(t *testing.T) {
	// host: a path only — no "enter it now" menu (writing a local file doesn't fit)
	a := &asker{in: bufio.NewReader(strings.NewReader("/my/host/pass\n")), out: io.Discard}
	if got := a.secretAnswer("borg", "host"); got != "/my/host/pass" {
		t.Errorf("host path = %q, want /my/host/pass", got)
	}

	// docker, path option
	a = &asker{in: bufio.NewReader(strings.NewReader("1\n/my/pass\n")), out: io.Discard}
	if got := a.secretAnswer("borg", "docker"); got != "/my/pass" {
		t.Errorf("docker path = %q, want /my/pass", got)
	}

	// docker, "enter it now": writes ./secrets/borg-pass (0600), returns the container path
	t.Chdir(t.TempDir())
	a = &asker{in: bufio.NewReader(strings.NewReader("2\nsupersecret\n")), out: io.Discard}
	got := a.secretAnswer("borg", "docker")
	if got != "/etc/redrill/secrets/borg-pass" {
		t.Errorf("docker text = %q, want the container mount path", got)
	}
	if b, err := os.ReadFile(filepath.Join("secrets", "borg-pass")); err != nil || string(b) != "supersecret" {
		t.Fatalf("secret file = %q (err %v), want supersecret", b, err)
	}
	if fi, _ := os.Stat(filepath.Join("secrets", "borg-pass")); fi.Mode().Perm() != 0o600 {
		t.Errorf("perms = %v, want 0600", fi.Mode().Perm())
	}
	// the secrets dir is self-ignoring so a secret can't be committed by accident
	if gi, err := os.ReadFile(filepath.Join("secrets", ".gitignore")); err != nil || !strings.Contains(string(gi), "*") {
		t.Errorf("secrets/.gitignore = %q (err %v), want it to ignore the dir", gi, err)
	}
	// the typed value never reaches the emitted config — only the path does
	o := genOptions{Target: "docker", SourceType: "borg", Repo: "r", PassphraseFile: got}
	resolveDefaults(&o)
	cfg, err := generateConfig(o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "supersecret") {
		t.Error("typed secret value leaked into the config")
	}

	// local, "enter it now": writes ./secrets and references the absolute local path
	a = &asker{in: bufio.NewReader(strings.NewReader("2\nlocalsecret\n")), out: io.Discard}
	lp := a.secretAnswer("borg", "local")
	if !filepath.IsAbs(lp) || !strings.HasSuffix(lp, "secrets/borg-pass") {
		t.Errorf("local text = %q, want an absolute .../secrets/borg-pass", lp)
	}
	if b, err := os.ReadFile(lp); err != nil || string(b) != "localsecret" {
		t.Errorf("local secret file = %q (err %v), want localsecret", b, err)
	}
}

func TestScheduleAnswer(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"weekly", "1\n", "Sun 04:00"},
		{"daily", "2\n", "04:00"},
		{"custom", "3\n0 4 * * 0\n", "0 4 * * 0"},
		{"manual explicit", "4\n", ""},
		{"manual is the default (blank)", "\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &asker{in: bufio.NewReader(strings.NewReader(c.input)), out: io.Discard}
			if got := a.scheduleAnswer(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSelectChoice(t *testing.T) {
	// by number
	a := &asker{in: bufio.NewReader(strings.NewReader("2\n")), out: io.Discard}
	if got := a.selectChoice("?", sourceChoices, 0); got != "borg" {
		t.Errorf("number: got %q, want borg", got)
	}
	// blank → the default index
	a = &asker{in: bufio.NewReader(strings.NewReader("\n")), out: io.Discard}
	if got := a.selectChoice("?", sourceChoices, 2); got != "restic" {
		t.Errorf("default: got %q, want restic", got)
	}
	// raw value still accepted (power users / scripts), though never shown
	a = &asker{in: bufio.NewReader(strings.NewReader("borg\n")), out: io.Discard}
	if got := a.selectChoice("?", sourceChoices, 0); got != "borg" {
		t.Errorf("keyword: got %q, want borg", got)
	}

	// no default (defIdx < 0): a blank re-prompts until a real pick
	a = &asker{in: bufio.NewReader(strings.NewReader("\n\n2\n")), out: io.Discard}
	if got := a.selectChoice("?", targetChoices, -1); got != "host" {
		t.Errorf("no-default: got %q, want host (after blanks)", got)
	}
}

// The internal "dumpdir" name must never appear in the wizard menu.
func TestSelectChoiceHidesJargon(t *testing.T) {
	var out bytes.Buffer
	a := &asker{in: bufio.NewReader(strings.NewReader("1\n")), out: &out}
	a.selectChoice("How is the backup stored?", sourceChoices, 0)
	s := out.String()
	if strings.Contains(s, "dumpdir") {
		t.Errorf("wizard exposed the internal term 'dumpdir':\n%s", s)
	}
	mustContain(t, s, "Plain dump files in a directory")
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("missing %q in:\n%s", sub, s)
	}
}
