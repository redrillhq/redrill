// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/report"
	"github.com/redrillhq/redrill/internal/store"
)

func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", configFileDefault(), "config file path (or set $REDRILL_CONFIG)")
	format := fs.String("format", "md", "output format: md or html")
	out := fs.String("out", "", "write to a file instead of stdout")
	jsonOut := fs.Bool("json", false, "machine-readable output (the assembled report data)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *format != "md" && *format != "html" {
		fmt.Fprintf(stderr, "redrill: report --format must be md or html, got %q\n", *format)
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		printConfigError(stderr, *path, err)
		return 3
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "redrill: cannot create data_dir %s: %v\n", cfg.DataDir, err)
		return 2
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "redrill.db"))
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}
	defer func() { _ = st.Close() }()

	rep, err := report.Build(ctx, st, cfg, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}

	var data []byte
	switch {
	case *jsonOut:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "redrill: %v\n", err)
			return 2
		}
		data = buf.Bytes()
	case *format == "html":
		if data, err = report.HTML(rep); err != nil {
			fmt.Fprintf(stderr, "redrill: %v\n", err)
			return 2
		}
	default:
		data = report.Markdown(rep)
	}

	if *out == "" {
		_, _ = stdout.Write(data)
		return 0
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil { //nolint:gosec // the report is shareable and carries no secrets by construction
		fmt.Fprintf(stderr, "redrill: cannot write report: %v\n", err)
		return 2
	}
	fmt.Fprintf(stderr, "report written to %s\n", *out)
	return 0
}
