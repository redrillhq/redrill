// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package redact scrubs known secret values and *_PASSWORD-style environment
// variables from captured output, before anything becomes evidence or logs.
package redact
