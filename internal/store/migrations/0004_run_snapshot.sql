/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

-- Migration 0004: record which snapshot/archive/dump a run audited, so a
-- run's evidence still names the backup it tested after the source rotates.
-- Forward-only.

ALTER TABLE runs ADD COLUMN snapshot TEXT NOT NULL DEFAULT '';
