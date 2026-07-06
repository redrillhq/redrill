/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

-- Migration 0005: record the audited snapshot's own timestamp, so the proven
-- recovery point (RPO — how old the data was when it was proven restorable)
-- is a first-class output. NULL = unknown (e.g. a pinned level that never
-- listed the repo resolved no timestamp). Forward-only.

ALTER TABLE runs ADD COLUMN snapshot_time INTEGER;
