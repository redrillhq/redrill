// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const kindEntropyAnomaly = "entropy_anomaly"

// Advisory tuning: a text-class file at or above the threshold is
// "near-random" (plain text sits around 4–5 bits/byte, base64 near 6;
// encrypted or compressed bytes exceed 7.9). The flag fires only when at
// least minFlagged files AND at least a tenth of the text-class population
// look random — one odd file stays quiet.
const (
	entropyThreshold  = 7.5
	entropySampleSize = 64 * 1024
	entropyMinBytes   = 64
	entropyMinFlagged = 2
)

// EntropyAnomaly is the advisory encryption canary, sibling to size_anomaly:
// text-class files that restore as near-random bytes are flagged, never
// failed — a signal, not a scanner. Media, archives, and unknown extensions
// are not judged at all: high entropy is their normal state, and judging
// them would make every photo library scream.
type EntropyAnomaly struct{}

func (c EntropyAnomaly) Kind() string { return kindEntropyAnomaly }

func (c EntropyAnomaly) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{
		Kind: kindEntropyAnomaly, Target: "restored text-class files",
		Expected: "structured content, not near-random bytes", Weak: true, Status: Pass,
	}
	var total, flagged int
	var example string
	err := filepath.WalkDir(env.RestoreDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || !textClass(d.Name()) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() < entropyMinBytes {
			return nil // too small for a stable estimate
		}
		total++
		bits, eerr := headEntropy(p)
		if eerr != nil {
			return eerr
		}
		if bits >= entropyThreshold {
			flagged++
			if example == "" {
				rel, rerr := filepath.Rel(env.RestoreDir, p)
				if rerr != nil {
					rel = d.Name()
				}
				example = fmt.Sprintf("%s (%.2f bits/byte)", rel, bits)
			}
		}
		return nil
	})
	if err != nil {
		ev.Status, ev.Actual = Error, "walk: "+err.Error()
		return ev, nil
	}

	switch {
	case total == 0:
		ev.Actual = "no signal — no text-class files restored"
	case flagged >= entropyMinFlagged && flagged*10 >= total:
		ev.Actual = fmt.Sprintf("ANOMALY: %d of %d text-class files near-random (>=%.1f bits/byte), e.g. %s — content may be encrypted",
			flagged, total, entropyThreshold, example)
	default:
		ev.Actual = fmt.Sprintf("%d of %d text-class files near-random", flagged, total)
	}
	return ev, nil
}

// textClass reports whether a file is expected to hold structured text —
// the only class where near-random bytes are a meaningful signal.
func textClass(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".sql", ".csv", ".json", ".xml", ".yaml", ".yml",
		".ini", ".conf", ".cfg", ".toml", ".log", ".html", ".htm", ".css",
		".php", ".py", ".js", ".ts", ".sh", ".go", ".java", ".rb", ".pl":
		return true
	}
	return false
}

// headEntropy is the Shannon entropy (bits/byte) of the file's first sample.
func headEntropy(path string) (float64, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path comes from walking the run's own restore dir
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, entropySampleSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	var freq [256]int
	for _, b := range buf[:n] {
		freq[b]++
	}
	var bits float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / float64(n)
		bits -= p * math.Log2(p)
	}
	return bits, nil
}
