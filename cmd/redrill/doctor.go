package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/sandbox/docker"
)

type doctorStatus string

const (
	statusOK   doctorStatus = "ok"
	statusWarn doctorStatus = "warn"
	statusErr  doctorStatus = "error"
)

type doctorCheck struct {
	Name   string
	Status doctorStatus
	Detail string
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", defaultConfigPath, "config file path")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", *path, err)
		return 3
	}

	results := collectDoctor(context.Background(), cfg)
	if *jsonOut {
		writeJSON(stdout, doctorJSON(results))
	} else {
		printDoctor(stdout, results)
	}
	return doctorExit(results)
}

func collectDoctor(ctx context.Context, cfg *config.Config) []doctorCheck {
	var out []doctorCheck
	out = append(out, engineChecks(ctx, cfg)...)
	out = append(out, runtimeCheck(ctx, cfg))
	out = append(out, scratchCheck(cfg))
	out = append(out, ioToolChecks(cfg)...)
	for i := range cfg.Sources {
		out = append(out, repoCheck(ctx, cfg.Sources[i]))
	}
	return out
}

func engineChecks(ctx context.Context, cfg *config.Config) []doctorCheck {
	var out []doctorCheck
	borgBin, resticBin := "", ""
	hasBorg, hasRestic := false, false
	for i := range cfg.Sources {
		switch cfg.Sources[i].Type {
		case "borg":
			hasBorg = true
			if cfg.Sources[i].Binary != "" {
				borgBin = cfg.Sources[i].Binary
			}
		case "restic":
			hasRestic = true
			if cfg.Sources[i].Binary != "" {
				resticBin = cfg.Sources[i].Binary
			}
		}
	}
	if hasBorg {
		if borgBin == "" {
			borgBin = "borg"
		}
		out = append(out, binaryCheck(ctx, "borg", borgBin, borgVersions, "--version"))
	}
	if hasRestic {
		if resticBin == "" {
			resticBin = "restic"
		}
		out = append(out, binaryCheck(ctx, "restic", resticBin, resticVersions, "version"))
	}
	return out
}

// Supported engine version bands (the versions redrill's parsers are written and
// tested against). borg is 1.x only — borg 2.x changed the CLI/JSON and timestamp
// format the driver relies on. restic is 0.17+ (the ls --json message_type field).
var (
	borgVersions   = versionRange{label: "borg", minMajor: 1, minMinor: 2, unsupportedMajor: 2}
	resticVersions = versionRange{label: "restic", minMajor: 0, minMinor: 17}
)

// versionRange is the engine version band redrill is tested against. A version
// outside it is a warn — the binary ran, but its output may be misparsed — never
// a hard error.
type versionRange struct {
	label              string
	minMajor, minMinor int
	unsupportedMajor   int // a major >= this is unverified (0 = no ceiling)
}

func binaryCheck(ctx context.Context, label, bin string, vr versionRange, versionArgs ...string) doctorCheck {
	name := "engine: " + label
	if _, err := osexec.LookPath(bin); err != nil {
		return doctorCheck{Name: name, Status: statusErr, Detail: bin + " not found on PATH"}
	}
	out, err := osexec.CommandContext(ctx, bin, versionArgs...).Output() //nolint:gosec // G204: binary is operator-configured
	if err != nil {
		return doctorCheck{Name: name, Status: statusErr, Detail: bin + ": " + err.Error()}
	}
	detail := firstLine(string(out))
	if msg, ok := vr.check(detail); !ok {
		return doctorCheck{Name: name, Status: statusWarn, Detail: detail + " — " + msg}
	}
	return doctorCheck{Name: name, Status: statusOK, Detail: detail}
}

var engineVersionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// check reports whether a "name X.Y.Z" version line falls in the tested band, with
// an explanatory message when it does not. An unparseable line is left alone — a
// binary that answered is better trusted than second-guessed.
func (vr versionRange) check(versionLine string) (string, bool) {
	m := engineVersionRe.FindStringSubmatch(versionLine)
	if m == nil {
		return "", true
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	if vr.unsupportedMajor > 0 && maj >= vr.unsupportedMajor {
		return fmt.Sprintf("redrill targets %s %d.x; %d.%d is unverified and may be misread", vr.label, vr.unsupportedMajor-1, maj, min), false
	}
	if maj < vr.minMajor || (maj == vr.minMajor && min < vr.minMinor) {
		return fmt.Sprintf("older than the tested minimum %s %d.%d", vr.label, vr.minMajor, vr.minMinor), false
	}
	return "", true
}

func runtimeCheck(ctx context.Context, cfg *config.Config) doctorCheck {
	name := "container runtime"
	rt, err := docker.NewRuntime(ctx)
	if err == nil {
		_ = rt.Close()
		return doctorCheck{Name: name, Status: statusOK, Detail: "reachable"}
	}
	if anyL3(cfg) {
		// L3 degrades to skipped without a runtime, never to a silent pass.
		return doctorCheck{Name: name, Status: statusWarn, Detail: "unreachable: L3 drills will be skipped, not failed"}
	}
	return doctorCheck{Name: name, Status: statusOK, Detail: "unreachable, but no L3 drills configured"}
}

func scratchCheck(cfg *config.Config) doctorCheck {
	name := "scratch"
	dir := cfg.Scratch.Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return doctorCheck{Name: name, Status: statusErr, Detail: "cannot create " + dir + ": " + err.Error()}
	}
	probe := filepath.Join(dir, ".redrill-doctor")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return doctorCheck{Name: name, Status: statusErr, Detail: dir + " not writable: " + err.Error()}
	}
	_ = os.Remove(probe)

	free, err := exec.FreeBytes(dir)
	if err != nil {
		return doctorCheck{Name: name, Status: statusWarn, Detail: "writable; free space unknown: " + err.Error()}
	}
	if max := cfg.Scratch.MaxBytes.Bytes(); max > 0 && free < uint64(max) {
		return doctorCheck{Name: name, Status: statusWarn,
			Detail: fmt.Sprintf("%s writable; free %s below quota %s", dir, humanBytes(free), humanBytes(uint64(max)))}
	}
	return doctorCheck{Name: name, Status: statusOK, Detail: fmt.Sprintf("%s writable, %s free", dir, humanBytes(free))}
}

// ioToolChecks verifies nice/ionice exist when the IO policy needs them; missing
// tools would otherwise break the engine processes they wrap.
func ioToolChecks(cfg *config.Config) []doctorCheck {
	var out []doctorCheck
	if cfg.Nice.CPU != 0 {
		out = append(out, toolCheck("nice"))
	}
	if cfg.Nice.IOClass == "idle" || cfg.Nice.IOClass == "best-effort" {
		out = append(out, toolCheck("ionice"))
	}
	return out
}

func toolCheck(bin string) doctorCheck {
	name := "io tool: " + bin
	if _, err := osexec.LookPath(bin); err != nil {
		return doctorCheck{Name: name, Status: statusWarn, Detail: bin + " not on PATH; it is configured but won't apply"}
	}
	return doctorCheck{Name: name, Status: statusOK, Detail: "available"}
}

func repoCheck(ctx context.Context, src config.Source) doctorCheck {
	name := "repo: " + src.Name
	err := exec.ValidateSource(ctx, src)
	switch {
	case err == nil:
		return doctorCheck{Name: name, Status: statusOK, Detail: "reachable (" + src.Type + ")"}
	case errors.Is(err, exec.ErrUnsupported):
		return doctorCheck{Name: name, Status: statusWarn, Detail: firstLine(err.Error())}
	default:
		return doctorCheck{Name: name, Status: statusErr, Detail: firstLine(err.Error())}
	}
}

func anyL3(cfg *config.Config) bool {
	for i := range cfg.Drills {
		if cfg.Drills[i].Levels.L3 != nil {
			return true
		}
	}
	return false
}

func printDoctor(w io.Writer, results []doctorCheck) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCHECK\tDETAIL")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", strings.ToUpper(string(r.Status)), r.Name, r.Detail)
	}
	_ = tw.Flush()
	errc, warnc := tally(results)
	fmt.Fprintf(w, "\n%d ok, %d warn, %d error\n", len(results)-errc-warnc, warnc, errc)
}

func doctorJSON(results []doctorCheck) map[string]any {
	checks := make([]map[string]string, 0, len(results))
	for _, r := range results {
		checks = append(checks, map[string]string{"check": r.Name, "status": string(r.Status), "detail": r.Detail})
	}
	errc, warnc := tally(results)
	return map[string]any{
		"ok":     errc == 0,
		"checks": checks,
		"summary": map[string]int{
			"ok": len(results) - errc - warnc, "warn": warnc, "error": errc,
		},
	}
}

func doctorExit(results []doctorCheck) int {
	for _, r := range results {
		if r.Status == statusErr {
			return 2
		}
	}
	return 0
}

func tally(results []doctorCheck) (errc, warnc int) {
	for _, r := range results {
		switch r.Status {
		case statusErr:
			errc++
		case statusWarn:
			warnc++
		}
	}
	return errc, warnc
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
