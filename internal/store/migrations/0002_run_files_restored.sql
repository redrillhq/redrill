/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

-- Migration 0002: record how many files a run restored, so file_count_tolerance
-- (L2) can compare against the previous proven run. Forward-only.

ALTER TABLE runs ADD COLUMN files_restored INTEGER NOT NULL DEFAULT 0;
