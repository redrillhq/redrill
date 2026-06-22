// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/redrillhq/redrill/internal/config"
)

const (
	defaultSchedule    = "Sun 04:00"
	defaultMaxProofAge = "10d"
	defaultPattern     = "*.sql.gz"
	defaultPGImage     = "postgres:16"
)

// genOptions is the fully-resolved set of answers the generator needs.
type genOptions struct {
	Target      string // docker | host
	SourceType  string // dumpdir | borg | restic
	SourceName  string
	DrillName   string
	Schedule    string
	MaxProofAge string

	// dumpdir
	Path    string
	Pattern string

	// borg / restic
	Repo           string
	PassphraseFile string // borg
	SSHKeyFile     string // borg (optional)
	PasswordFile   string // restic
	EnvFile        string // restic (optional)

	// L2 (borg/restic file-restore proof) / L3 (Postgres sandbox)
	L2          bool
	L3          bool
	ExtractPath string // borg/restic L3 only
	PGImage     string
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "deployment target: docker|host (required when non-interactive)")
	typ := fs.String("type", "dumpdir", "source type: dumpdir|borg|restic")
	name := fs.String("name", "", "drill name (source name is derived from it)")
	schedule := fs.String("schedule", "", "drill schedule (cron or 'Sun 04:00'); empty = manual-only")
	maxProofAge := fs.String("max-proof-age", defaultMaxProofAge, "Proof SLA: stale alert past this age")
	path := fs.String("path", "", "dumpdir: directory holding the dumps")
	pattern := fs.String("pattern", defaultPattern, "dumpdir: filename glob")
	repo := fs.String("repo", "", "borg/restic: repository URL")
	passFile := fs.String("passphrase-file", "", "borg: file holding the repo passphrase")
	sshKeyFile := fs.String("ssh-key-file", "", "borg: read-only SSH key file (optional)")
	pwFile := fs.String("password-file", "", "restic: file holding the repo password")
	envFile := fs.String("env-file", "", "restic: dotenv with backend (S3/B2) credentials (optional)")
	l3 := fs.Bool("l3", true, "include an L3 database sandbox drill")
	extractPath := fs.String("extract-path", "", "borg/restic L3: path of the dump inside the snapshot")
	pgImage := fs.String("pg-image", defaultPGImage, "L3 postgres sandbox image")
	outFile := fs.String("o", "", "write the config to this file instead of stdout")
	write := fs.Bool("write", false, "write to ./config.yaml (shorthand for -o ./config.yaml)")
	force := fs.Bool("force", false, "overwrite an existing output file")
	jsonOut := fs.Bool("json", false, "machine-readable output (implies non-interactive)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// Track whether --l3 was given; if not, default it by source type below.
	l3Set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "l3" {
			l3Set = true
		}
	})

	o := genOptions{
		Target: *target, SourceType: *typ, DrillName: *name,
		Schedule: *schedule, MaxProofAge: *maxProofAge,
		Path: *path, Pattern: *pattern,
		Repo: *repo, PassphraseFile: *passFile, SSHKeyFile: *sshKeyFile,
		PasswordFile: *pwFile, EnvFile: *envFile,
		L3: *l3, ExtractPath: *extractPath, PGImage: *pgImage,
	}
	// L3 boots a database, so it only fits a backup that holds a DB dump:
	// default on for a SQL dumpdir, off for a borg/restic file tree.
	if !l3Set {
		o.L3 = o.SourceType == "dumpdir"
	}

	dest := *outFile
	if *write && dest == "" {
		dest = "config.yaml"
	}
	overwrite := *force

	// Interactive only on a real terminal and not when emitting JSON; otherwise
	// the run is fully flag-driven (cron/scripts), which requires --target.
	interactive := !*jsonOut && isTTY(os.Stdin)
	if interactive {
		a, code := wizard(os.Stdin, stderr, &o)
		if code != 0 {
			return code
		}
		// Final question, unless a flag already chose: where to write the config.
		// Blank prints to stdout; an existing path is overwritten in place (y/n),
		// not via a forced re-run.
		if dest == "" {
			var ov bool
			dest, ov = a.askWriteDest()
			overwrite = overwrite || ov
		}
	} else if code := fromFlags(stderr, &o); code != 0 {
		return code
	}
	resolveDefaults(&o)

	yamlBytes, err := generateConfig(o)
	if err != nil {
		// generateConfig validates its own output; a failure here is a redrill bug.
		fmt.Fprintf(stderr, "redrill: internal error: generated config did not validate: %v\n", err)
		return 2
	}

	if *jsonOut {
		if dest != "" {
			if code := writeConfigFile(stderr, dest, yamlBytes, overwrite); code != 0 {
				return code
			}
		}
		writeJSON(stdout, initPlan(o, yamlBytes, dest))
		return 0
	}

	if code := emit(stdout, stderr, yamlBytes, dest, interactive, overwrite); code != 0 {
		return code
	}
	printGuidance(stderr, o, dest)
	return 0
}

// emit writes the config to dest (or stdout when dest is empty). In interactive
// mode it also echoes the config to stdout when writing, so the user sees it.
func emit(stdout, stderr io.Writer, yamlBytes []byte, dest string, echo, force bool) int {
	if dest == "" {
		_, _ = stdout.Write(yamlBytes)
		return 0
	}
	if code := writeConfigFile(stderr, dest, yamlBytes, force); code != 0 {
		return code
	}
	if echo {
		_, _ = stdout.Write(yamlBytes)
	}
	fmt.Fprintf(stderr, "redrill: wrote %s\n", dest)
	return 0
}

// fromFlags validates and completes a non-interactive invocation, reporting the
// missing required flags. --target is always required (no TTY to default it).
func fromFlags(stderr io.Writer, o *genOptions) int {
	if o.Target != "" && !validTarget(o.Target) {
		fmt.Fprintf(stderr, "redrill: --target must be docker, host, or local, got %q\n", o.Target)
		return 2
	}
	var missing []string
	if o.Target == "" {
		missing = append(missing, "--target")
	}
	switch o.SourceType {
	case "dumpdir":
		if o.Path == "" {
			missing = append(missing, "--path")
		}
	case "borg":
		if o.Repo == "" {
			missing = append(missing, "--repo")
		}
		if o.PassphraseFile == "" {
			missing = append(missing, "--passphrase-file")
		}
	case "restic":
		if o.Repo == "" {
			missing = append(missing, "--repo")
		}
		if o.PasswordFile == "" {
			missing = append(missing, "--password-file")
		}
	default:
		fmt.Fprintf(stderr, "redrill: --type must be dumpdir, borg, or restic, got %q\n", o.SourceType)
		return 2
	}
	if o.L3 && (o.SourceType == "borg" || o.SourceType == "restic") && o.ExtractPath == "" {
		missing = append(missing, "--extract-path (the dump inside the snapshot, required for a borg/restic L3)")
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "redrill: init is non-interactive (no TTY or --json); missing: %s\n", strings.Join(missing, ", "))
		return 2
	}
	return 0
}

// wizard collects the answers interactively. It returns its asker (so the caller
// can keep reading from the same input) and a non-zero exit code if input ended
// before a required answer was given.
func wizard(in io.Reader, out io.Writer, o *genOptions) (*asker, int) {
	a := &asker{in: bufio.NewReader(in), out: out}
	fmt.Fprintln(out, "redrill init — a few questions to scaffold a starter config.")
	fmt.Fprintln(out)

	o.Target = a.selectChoice("Where will redrill run?", targetChoices, -1) // no default — an explicit pick
	o.SourceType = a.selectChoice("How is the backup stored?", sourceChoices, sourceDefaultIndex(o.SourceType))

	switch o.SourceType {
	case "dumpdir":
		o.Path = a.askPath("Directory holding the dumps", o.Path, o.Target, validateDumpdir)
		o.Pattern = a.ask("Filename pattern", orDefault(o.Pattern, defaultPattern))
	case "borg":
		o.Repo = a.askPath("Borg repository (local path or ssh:// URL)", o.Repo, o.Target, validateBorgRepo)
		o.PassphraseFile = a.secretAnswer("borg", o.Target)
		o.SSHKeyFile = a.ask("Read-only SSH key file (blank for a local repo)", o.SSHKeyFile)
	case "restic":
		o.Repo = a.askRequired("Restic repository URL", o.Repo)
		o.PasswordFile = a.secretAnswer("restic", o.Target)
		o.EnvFile = a.ask("Backend env_file (S3/B2 creds; blank if none)", o.EnvFile)
	}

	// A SQL dumpdir's proof is L3 (boot it in Postgres and query) — offered by
	// default. A borg/restic file tree's proof is L2 (sample restore + checks),
	// added automatically in resolveDefaults; its L3 (a Postgres dump inside the
	// archive) stays a flag-only opt-in, so the wizard never asks a file backup
	// about Postgres.
	if o.SourceType == "dumpdir" {
		o.L3 = a.yesno("Boot the restored Postgres dump and query it (L3)?", true)
		if o.L3 {
			o.PGImage = a.ask("Postgres sandbox image", orDefault(o.PGImage, defaultPGImage))
		}
	} else {
		o.L3 = false
	}

	o.Schedule = a.scheduleAnswer()
	o.MaxProofAge = a.ask("Proof SLA (stale alert past this age)", orDefault(o.MaxProofAge, defaultMaxProofAge))

	// The name is just a label; default it from the repo/path so it can be
	// accepted with Enter instead of invented up front.
	o.DrillName = a.ask("Name for this drill (alias)", orDefault(o.DrillName, deriveBase(*o)))
	o.SourceName = o.DrillName + "-backups"

	if a.aborted {
		fmt.Fprintln(out, "redrill: init aborted (incomplete input)")
		return a, 2
	}
	return a, 0
}

// resolveDefaults fills the derived/optional fields left empty by either path.
func resolveDefaults(o *genOptions) {
	// borg/restic are file trees, so their restorability proof is L2.
	o.L2 = o.SourceType == "borg" || o.SourceType == "restic"
	if o.DrillName == "" {
		o.DrillName = deriveBase(*o)
	}
	if o.SourceName == "" {
		o.SourceName = o.DrillName + "-backups"
	}
	// Schedule is intentionally not defaulted — empty means manual-only.
	if o.MaxProofAge == "" {
		o.MaxProofAge = defaultMaxProofAge
	}
	if o.Pattern == "" {
		o.Pattern = defaultPattern
	}
	if o.PGImage == "" {
		o.PGImage = defaultPGImage
	}
}

// validateBorgRepo / validateDumpdir return a human-readable problem with a path
// entered in the wizard, or "" if it looks usable. A remote repo (ssh://) can't
// be checked from here and passes.
func validateBorgRepo(target, repo string) string {
	if strings.Contains(repo, "://") {
		return ""
	}
	return checkLocalDir(target, repo, true)
}

func validateDumpdir(target, path string) string {
	return checkLocalDir(target, path, false)
}

// absPath resolves a path against init's working directory; init runs where the
// user is, so a relative entry becomes a usable absolute path.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// secretWord names the repo secret per engine.
func secretWord(engine string) string {
	if engine == "restic" {
		return "password"
	}
	return "passphrase"
}

// writeSecretFile saves a typed secret to ./secrets/<engine>-pass (0600) and
// drops a .gitignore so the dir's secrets can't be committed by accident. Used
// only for docker, where ./secrets is bind-mounted into the container.
func writeSecretFile(engine, value string) error {
	const dir = "secrets"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(gi, []byte("*\n"), 0o600)
	}
	return os.WriteFile(filepath.Join(dir, engine+"-pass"), []byte(value), 0o600)
}

// checkLocalDir reports why a local path is unusable as a source directory. For
// a Docker target the path is a container path, so only its shape is checked.
func checkLocalDir(target, p string, expectBorgRepo bool) string {
	if !strings.HasPrefix(p, "/") {
		return "not an absolute path — redrill runs as a daemon whose working directory isn't yours; use a full path"
	}
	if target == "docker" {
		return "" // a container path; can't verify it on this host
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "no such directory: " + p
	}
	if !fi.IsDir() {
		return "not a directory: " + p
	}
	if expectBorgRepo {
		if _, err := os.Stat(filepath.Join(p, "config")); err != nil {
			return "doesn't look like a borg repository (no 'config' file) — point this at the repo root, not the backed-up directory"
		}
	}
	return ""
}

// deriveBase makes a default drill/source name from the repo or dump directory:
// its last path segment, sanitized. Falls back to "backup".
func deriveBase(o genOptions) string {
	raw := o.Repo
	if o.SourceType == "dumpdir" {
		raw = o.Path
	}
	if i := strings.Index(raw, "://"); i >= 0 { // strip ssh:// and friends
		raw = raw[i+3:]
	}
	if i := strings.LastIndex(raw, ":"); i >= 0 { // strip s3:/sftp: prefixes and host:port
		raw = raw[i+1:]
	}
	raw = strings.Trim(raw, "/")
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	if base := sanitizeName(raw); base != "" {
		return base
	}
	return "backup"
}

// sanitizeName lowercases and reduces a string to [a-z0-9-_], collapsing any
// other run to a single dash.
func sanitizeName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// generateConfig renders the starter config and validates it before returning;
// it never returns bytes that would fail `redrill validate`.
func generateConfig(o genOptions) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# redrill starter config, generated by `redrill init`. Review before use.\n")
	b.WriteString("# Strict schema: `redrill validate` is the contract. Secrets are *_file/*_env only.\n")
	b.WriteString("version: 1\n")
	dd := dataDir(o.Target)
	fmt.Fprintf(&b, "data_dir: %s\n", dd)
	b.WriteString("scratch:\n")
	fmt.Fprintf(&b, "  dir: %s\n", filepath.Join(dd, "scratch"))
	b.WriteString("  max_bytes: 40GiB\n")
	b.WriteString("concurrency: 1\n\n")

	b.WriteString("sources:\n")
	fmt.Fprintf(&b, "  - name: %s\n", o.SourceName)
	fmt.Fprintf(&b, "    type: %s\n", o.SourceType)
	switch o.SourceType {
	case "dumpdir":
		fmt.Fprintf(&b, "    path: %q\n", o.Path)
		fmt.Fprintf(&b, "    pattern: %q\n", o.Pattern)
		b.WriteString("    pick: newest\n")
	case "borg":
		fmt.Fprintf(&b, "    repo: %q\n", o.Repo)
		fmt.Fprintf(&b, "    passphrase_file: %q\n", o.PassphraseFile)
		if o.SSHKeyFile != "" {
			fmt.Fprintf(&b, "    ssh_key_file: %q\n", o.SSHKeyFile)
		}
	case "restic":
		fmt.Fprintf(&b, "    repo: %q\n", o.Repo)
		fmt.Fprintf(&b, "    password_file: %q\n", o.PasswordFile)
		if o.EnvFile != "" {
			fmt.Fprintf(&b, "    env_file: %q\n", o.EnvFile)
		}
	}
	b.WriteString("\n")

	b.WriteString("drills:\n")
	fmt.Fprintf(&b, "  - name: %s\n", o.DrillName)
	fmt.Fprintf(&b, "    source: %s\n", o.SourceName)
	if o.Schedule != "" {
		fmt.Fprintf(&b, "    schedule: %q\n", o.Schedule)
	} else {
		b.WriteString("    # schedule: \"Sun 04:00\" # uncomment to auto-run; without it the drill runs only via `redrill run`\n")
	}
	fmt.Fprintf(&b, "    max_proof_age: %s\n", o.MaxProofAge)
	b.WriteString("    levels:\n")
	switch o.SourceType {
	case "dumpdir":
		b.WriteString("      l1: { file_min_bytes: 1MiB, compression_test: true, max_age: 36h }\n")
	case "borg", "restic":
		b.WriteString("      l1: { native_check: true, snapshot_max_age: 36h }\n")
	}
	if o.L2 {
		b.WriteString("      l2:\n")
		b.WriteString("        restore: { scope: sample, sample: { files: 50, newest: 20 } }\n")
		b.WriteString("        checks:\n")
		b.WriteString("          - min_total_bytes: 1MiB\n")
		b.WriteString("          - newest_file_max_age: 8d\n")
		b.WriteString("          # - path_exists: \"path/you/expect\" # assert a known file restored\n")
	}
	if o.L3 {
		b.WriteString("      l3:\n")
		if o.SourceType == "borg" || o.SourceType == "restic" {
			fmt.Fprintf(&b, "        extract_path: %q  # the dump file inside the snapshot\n", o.ExtractPath)
		}
		fmt.Fprintf(&b, "        sandbox: { image: %s, env: { POSTGRES_PASSWORD: drill }, network: none, memory: 1GiB, timeout: 20m }\n", o.PGImage)
		b.WriteString("        load: auto\n")
		b.WriteString("        checks:\n")
		b.WriteString("          - sql_no_error: \"select 1\" # proves the DB boots and answers; replace with a real assertion:\n")
		b.WriteString("          # - sql: { query: \"select count(*) from users\", expect: \"> 0\" }\n")
	}

	out := []byte(b.String())
	if err := validateGenerated(out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateGenerated runs the rendered config through the same gate `validate`
// uses, so init can never emit a config the CLI would reject.
func validateGenerated(b []byte) error {
	cfg, err := config.Parse(b)
	if err != nil {
		return err
	}
	return extraValidate(cfg)
}

func writeConfigFile(stderr io.Writer, dest string, data []byte, force bool) int {
	if !force {
		if _, err := os.Stat(dest); err == nil {
			fmt.Fprintf(stderr, "redrill: %s already exists; pass --force to overwrite\n", dest)
			return 2
		}
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil { //nolint:gosec // a config is not a secret; secrets are referenced by path
		fmt.Fprintf(stderr, "redrill: cannot write %s: %v\n", dest, err)
		return 2
	}
	return 0
}

// printGuidance prints the deployment snippet, secret-file checklist, and next
// steps to stderr — keeping stdout pure YAML when the config is piped to a file.
func printGuidance(w io.Writer, o genOptions, dest string) {
	cpath := dest
	if cpath == "" {
		cpath = "config.yaml"
	}
	fmt.Fprintln(w)
	switch o.Target {
	case "docker":
		fmt.Fprintln(w, "Docker: mount these into the redrill service in deploy/compose/compose.yaml:")
		fmt.Fprintf(w, "    - %s:/etc/redrill/config.yaml:ro\n", cpath)
		if o.SourceType == "dumpdir" {
			fmt.Fprintf(w, "    - %s:%s:ro # the backups to audit\n", o.Path, o.Path)
		}
		if o.SourceType == "borg" || o.SourceType == "restic" {
			fmt.Fprintln(w, "    - ./secrets:/etc/redrill/secrets:ro # the secret files below")
		}
		if o.L3 {
			fmt.Fprintln(w, "    - /var/run/docker.sock:/var/run/docker.sock # L3 database sandboxes")
		}
	case "host":
		fmt.Fprintln(w, "Host (systemd): install per deploy/README.md. The host must provide the engine tools this config needs.")
	case "local":
		fmt.Fprintln(w, "Local: runs as you — data lives under your home; put the engine tools (borg/restic) on your PATH. Nothing to mount or install.")
	}

	if secrets := secretFiles(o); len(secrets) > 0 {
		fmt.Fprintln(w, "\nSecret files referenced by the config (create any that don't exist; chmod 600; never commit them):")
		for _, f := range secrets {
			fmt.Fprintf(w, "    %s\n", f)
		}
		if o.Target == "host" {
			fmt.Fprintln(w, "  systemd runs redrill as its own user, so the secret must be owned/readable by it (a 0600 file you own isn't). Provision it with that owner, e.g.:")
			fmt.Fprintln(w, "    sudo install -D -o redrill -g redrill -m600 <your-secret-file> /etc/redrill/secrets/<name>")
			fmt.Fprintln(w, "  Verify before enabling the timer:  sudo -u redrill redrill doctor -c <config>")
		}
	}

	fmt.Fprintln(w, "\nNext:")
	fmt.Fprintf(w, "    redrill validate -c %s\n", cpath)
	fmt.Fprintf(w, "    redrill doctor   -c %s\n", cpath)
	fmt.Fprintf(w, "    redrill run %s -c %s\n", o.DrillName, cpath)
}

func secretFiles(o genOptions) []string {
	var out []string
	for _, f := range []string{o.PassphraseFile, o.SSHKeyFile, o.PasswordFile, o.EnvFile} {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func initPlan(o genOptions, yamlBytes []byte, dest string) map[string]any {
	return map[string]any{
		"target":       o.Target,
		"source_type":  o.SourceType,
		"drill":        o.DrillName,
		"l3":           o.L3,
		"output":       dest, // "" means stdout
		"secret_files": secretFiles(o),
		"config":       string(yamlBytes),
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// choice pairs a human-readable label with the internal config value it maps to.
type choice struct {
	value string
	label string
}

// targetChoices is asked with no default — the operator picks where redrill runs.
var targetChoices = []choice{
	{value: "docker", label: "Docker / Compose"},
	{value: "host", label: "A host service (systemd)"},
	{value: "local", label: "Run it yourself / local (a data dir you own)"},
}

func validTarget(t string) bool {
	return t == "docker" || t == "host" || t == "local"
}

// dataDir is the default data_dir for a target. Docker (named volume) and a
// systemd host both use /var/lib/redrill — the unit provisions it via
// StateDirectory and can write only there; a "run it yourself" local setup uses
// a dir the invoking user can actually create.
func dataDir(target string) string {
	if target == "local" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "redrill")
		}
	}
	return "/var/lib/redrill"
}

// sourceChoices describes the source types in plain language; the internal
// "dumpdir" name is deliberately never shown to the user.
var sourceChoices = []choice{
	{value: "dumpdir", label: "Plain dump files in a directory (e.g. a pg_dump/mysqldump cron)"},
	{value: "borg", label: "A BorgBackup repository"},
	{value: "restic", label: "A restic repository"},
}

func sourceDefaultIndex(cur string) int {
	for i, c := range sourceChoices {
		if c.value == cur {
			return i
		}
	}
	return 0
}

// scheduleChoices offers common cadences; "manual" emits no schedule (the drill
// runs only on demand). The default is manual — a scaffold shouldn't silently
// commit to background restores; the Proof SLA still nags if it goes unproven.
var scheduleChoices = []choice{
	{value: "weekly", label: "Weekly (Sun 04:00)"},
	{value: "daily", label: "Daily (04:00)"},
	{value: "custom", label: "Custom cron expression"},
	{value: "manual", label: "Manual only — no schedule (run by hand / hook)"},
}

const scheduleManualIndex = 3

// secretChoices: provide a repo secret as a file path, or type it now (init then
// writes it to a local 0600 file — the value reaches a file, never the config).
var secretChoices = []choice{
	{value: "path", label: "Point to a file that already holds it (a path)"},
	{value: "text", label: "Enter it now — saved to a local ./secrets/ file (chmod 600)"},
}

// asker reads line-oriented answers, writing prompts to out (stderr) so stdout
// stays clean for the emitted YAML.
type asker struct {
	in      *bufio.Reader
	out     io.Writer
	aborted bool
}

// readLine returns the trimmed next line; ok is false at EOF with no content,
// which the callers treat as an aborted session rather than looping forever.
func (a *asker) readLine() (string, bool) {
	line, err := a.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" && err != nil {
		return "", false
	}
	return line, true
}

func (a *asker) ask(prompt, def string) string {
	fmt.Fprintf(a.out, "%s [%s]: ", prompt, def)
	s, ok := a.readLine()
	if !ok {
		a.aborted = true
		return def
	}
	if s == "" {
		return def
	}
	return s
}

func (a *asker) askRequired(prompt, def string) string {
	for {
		if def != "" {
			fmt.Fprintf(a.out, "%s [%s]: ", prompt, def)
		} else {
			fmt.Fprintf(a.out, "%s: ", prompt)
		}
		s, ok := a.readLine()
		if !ok {
			a.aborted = true
			return def
		}
		if s == "" {
			if def != "" {
				return def
			}
			fmt.Fprintln(a.out, "  (required)")
			continue
		}
		return s
	}
}

// askPath asks for a path and validates it as entered: a problem is shown and
// the user re-prompted, but they can override (a Docker container path or a
// not-yet-created directory legitimately won't exist on this host).
func (a *asker) askPath(prompt, def, target string, validate func(target, val string) string) string {
	for {
		v := a.askRequired(prompt, def)
		if a.aborted {
			return v
		}
		if !strings.Contains(v, "://") {
			v = absPath(v) // resolve a relative entry against init's working dir
		}
		msg := validate(target, v)
		if msg == "" {
			return v
		}
		fmt.Fprintf(a.out, "  ⚠ %s\n", msg)
		if a.yesno("  use it anyway?", false) {
			return v
		}
		// Rejected: re-prompt with the original default, not the bad value.
	}
}

// selectChoice presents a numbered menu of human-readable labels and returns the
// internal value of the pick — so jargon like "dumpdir" never reaches the user.
// A number or the raw value is accepted; a blank takes the default, or re-prompts
// when defIdx < 0 (no default — the pick is required).
func (a *asker) selectChoice(prompt string, choices []choice, defIdx int) string {
	for {
		fmt.Fprintln(a.out, prompt)
		for i, c := range choices {
			fmt.Fprintf(a.out, "  %d) %s\n", i+1, c.label)
		}
		if defIdx >= 0 {
			fmt.Fprintf(a.out, "choice [%d]: ", defIdx+1)
		} else {
			fmt.Fprint(a.out, "choice: ")
		}
		s, ok := a.readLine()
		if !ok {
			a.aborted = true
			if defIdx >= 0 {
				return choices[defIdx].value
			}
			return ""
		}
		if s == "" {
			if defIdx >= 0 {
				return choices[defIdx].value
			}
			fmt.Fprintf(a.out, "  enter a number 1-%d\n", len(choices))
			continue
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(choices) {
			return choices[n-1].value
		}
		for _, c := range choices {
			if strings.EqualFold(s, c.value) {
				return c.value
			}
		}
		fmt.Fprintf(a.out, "  enter a number 1-%d\n", len(choices))
	}
}

// scheduleAnswer runs the schedule menu, returning the cron/shorthand string or
// "" for a manual-only drill (the default).
func (a *asker) scheduleAnswer() string {
	switch a.selectChoice("Schedule for this drill:", scheduleChoices, scheduleManualIndex) {
	case "weekly":
		return defaultSchedule
	case "daily":
		return "04:00"
	case "custom":
		return a.askRequired("Cron expression (e.g. '0 4 * * 0')", "")
	default: // manual
		return ""
	}
}

// secretAnswer asks how the repo secret is supplied and returns a path for the
// *_file config field. "Enter it now" writes the typed value to a local 0600
// file — that only fits docker (./secrets is bind-mounted into the container);
// on a host the service user can't read a file in your home, so there the secret
// is a path you provision. Either way the value reaches a file, never the config.
func (a *asker) secretAnswer(engine, target string) string {
	word := secretWord(engine)
	// host: a path only — the systemd service user can't read a local file you own.
	if target == "host" {
		return a.ask("Path to the file holding the "+word, "/etc/redrill/secrets/"+engine+"-pass")
	}
	// docker / local: offer "enter it now" → write a local 0600 file you can read.
	if a.selectChoice("Repository "+word+":", secretChoices, 0) == "text" {
		val := a.askRequired("Enter the "+word, "")
		if a.aborted {
			return secretDefaultPath(target, engine)
		}
		if err := writeSecretFile(engine, val); err != nil {
			fmt.Fprintf(a.out, "  couldn't write the secret file (%v); set it by hand\n", err)
			return secretDefaultPath(target, engine)
		}
		if target == "docker" {
			fmt.Fprintf(a.out, "  saved to ./secrets/%s-pass (chmod 600); compose mounts it to /etc/redrill/secrets/%s-pass\n", engine, engine)
			return "/etc/redrill/secrets/" + engine + "-pass"
		}
		p := absPath(filepath.Join("secrets", engine+"-pass")) // local: read the file directly
		fmt.Fprintf(a.out, "  saved to %s (chmod 600)\n", p)
		return p
	}
	return a.ask("Path to the file holding the "+word, secretDefaultPath(target, engine))
}

// secretDefaultPath is the default for the "point at a file" option per target.
func secretDefaultPath(target, engine string) string {
	if target == "local" {
		return absPath(filepath.Join("secrets", engine+"-pass"))
	}
	return "/etc/redrill/secrets/" + engine + "-pass"
}

// askWriteDest asks where to write the config, looping so an existing path can be
// overwritten in place (y/n) — the operator isn't forced to re-run with --force.
// Returns the chosen path ("" = print to stdout) and whether to overwrite.
func (a *asker) askWriteDest() (dest string, overwrite bool) {
	for {
		dest = a.ask("Write the config to a file? (path, or blank to print to stdout)", "")
		if dest == "" || a.aborted {
			return "", false
		}
		if _, err := os.Stat(dest); err != nil {
			return dest, false // not there (or unstat-able) — write fresh
		}
		if a.yesno("  "+dest+" exists — overwrite?", false) {
			return dest, true
		}
		// declined: loop and ask for a different path (or blank for stdout)
	}
}

func (a *asker) yesno(prompt string, def bool) bool {
	d := "y"
	if !def {
		d = "n"
	}
	for {
		fmt.Fprintf(a.out, "%s (y/n) [%s]: ", prompt, d)
		s, ok := a.readLine()
		if !ok {
			a.aborted = true
			return def
		}
		switch strings.ToLower(s) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			fmt.Fprintln(a.out, "  please answer y or n")
		}
	}
}
