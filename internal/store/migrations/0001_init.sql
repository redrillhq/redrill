/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

-- Migration 0001: initial schema. Forward-only — never edit a
-- shipped migration; add a new NNNN_*.sql file instead.
--
-- All timestamps are INTEGER unix nanoseconds in UTC; max_proof_age is INTEGER
-- nanoseconds. runs.result is CHECK-constrained to the pass|fail|error taxonomy
-- so a bad verdict can never reach the store.

CREATE TABLE sources (
    name        TEXT    NOT NULL PRIMARY KEY,
    type        TEXT    NOT NULL,
    config_hash TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE drills (
    name          TEXT    NOT NULL PRIMARY KEY,
    source        TEXT    NOT NULL,
    config_hash   TEXT    NOT NULL,
    max_proof_age INTEGER NOT NULL,
    levels_json   TEXT    NOT NULL
) STRICT;

CREATE TABLE runs (
    id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    drill          TEXT    NOT NULL,
    "trigger"      TEXT    NOT NULL CHECK ("trigger" IN ('schedule', 'manual', 'api')),
    started_at     INTEGER NOT NULL,
    finished_at    INTEGER,
    result         TEXT    CHECK (result IS NULL OR result IN ('pass', 'fail', 'error')),
    level_reached  TEXT    NOT NULL DEFAULT '',
    bytes_restored INTEGER NOT NULL DEFAULT 0,
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    executor       TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_runs_drill_id ON runs (drill, id);

CREATE TABLE run_steps (
    run_id      INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    idx         INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    status      TEXT    NOT NULL,
    summary     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, idx)
) STRICT;

CREATE TABLE evidence (
    run_id     INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    idx        INTEGER NOT NULL,
    check_kind TEXT    NOT NULL,
    target     TEXT    NOT NULL DEFAULT '',
    expected   TEXT    NOT NULL DEFAULT '',
    actual     TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL,
    weak       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, idx)
) STRICT;

CREATE TABLE artifacts (
    run_id INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    idx    INTEGER NOT NULL,
    name   TEXT    NOT NULL,
    path   TEXT    NOT NULL,
    bytes  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, idx)
) STRICT;

CREATE TABLE drill_state (
    drill          TEXT    NOT NULL,
    level          TEXT    NOT NULL,
    last_proven_at INTEGER NOT NULL,
    PRIMARY KEY (drill, level)
) STRICT;
