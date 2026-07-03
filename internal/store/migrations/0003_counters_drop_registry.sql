/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

-- Migration 0003: drop the never-written config-registry tables (config is the
-- source of truth; no code ever inserted a row) and add monotonic per-drill
-- counters. Counters live outside runs so retention pruning can't make the
-- Prometheus counter regress; seeded from surviving run history.

DROP TABLE sources;
DROP TABLE drills;

CREATE TABLE drill_counters (
    drill                TEXT    NOT NULL PRIMARY KEY,
    bytes_restored_total INTEGER NOT NULL DEFAULT 0
) STRICT;

INSERT INTO drill_counters (drill, bytes_restored_total)
SELECT drill, SUM(bytes_restored) FROM runs GROUP BY drill;
