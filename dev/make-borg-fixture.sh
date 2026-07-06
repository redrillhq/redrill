#!/usr/bin/env bash
#
# Copyright (C) 2026 Andrew Alyamovsky
# SPDX-License-Identifier: AGPL-3.0-or-later
#

#
# Build a deterministic borg fixture repo for dev e2e drills:
#   sample file tree + seeded postgres dump (custom format), packaged as a
#   borg repo with two archives (so "newest" selection is meaningful).
#
# Same SEED -> same tree bytes, same archive names, same DB rows. The only
# non-determinism is timestamps (mtimes, dump header), which stay "now" on
# purpose — freshness checks need recent data.
#
# SABOTAGE variants (a drill against the fixture MUST then fail):
#   SABOTAGE=stale-source      archives backdated 30d -> snapshot_max_age catches it
#   SABOTAGE=truncated-segment a repo segment truncated -> borg check / native_check catches it
#   SABOTAGE=missing-data-dir  data/ excluded from the archives -> path_exists catches it
#
# This builder WRITES — but only inside FIXTURE_DIR and a temp pg container.
# The product invariant is untouched: dev/drill.sh and redrill itself never
# write to repositories; building test fixtures is test-setup's job.
#
# Run inside the dev env: dev/shell.sh dev/make-borg-fixture.sh
set -euo pipefail
. "$(dirname "$0")/lib.sh"

DEV_DATA=${DEV_DATA:-/var/tmp/redrill-dev}
FIXTURE_DIR=${FIXTURE_DIR:-$DEV_DATA/borg-fixture}
SEED=${SEED:-42}
NUM_FILES=${NUM_FILES:-300}
USERS=${USERS:-500}
EVENTS=${EVENTS:-2000}
PG_IMAGE=${PG_IMAGE:-postgres:16}
FIXTURE_PG=${FIXTURE_PG:-redrill-dev-fixture-pg}
SABOTAGE=${SABOTAGE:-}

case "$SABOTAGE" in
  ""|stale-source|truncated-segment|missing-data-dir) ;;
  *) die "unknown SABOTAGE '$SABOTAGE' (stale-source | truncated-segment | missing-data-dir)" ;;
esac

command -v borg >/dev/null || die "borg not found — run this via dev/shell.sh (all deps live in the dev image)"
command -v docker >/dev/null || die "docker CLI not found — run this via dev/shell.sh"
docker info >/dev/null 2>&1 || die "docker daemon not reachable"

trap 'pg_stop "$FIXTURE_PG"' EXIT

log "Building borg fixture (SEED=$SEED${SABOTAGE:+, SABOTAGE=$SABOTAGE}) at $FIXTURE_DIR"
rm -rf "${FIXTURE_DIR:?}"
mkdir -p "$FIXTURE_DIR/source/database" "$FIXTURE_DIR/secrets"
SRC="$FIXTURE_DIR/source"

log "Generating deterministic sample tree ($NUM_FILES files)"
gen_tree "$SRC" "$NUM_FILES" "$SEED"

log "Seeding postgres ($PG_IMAGE): $USERS users, $EVENTS events; dumping sampledb (custom format)"
pg_start "$FIXTURE_PG" "$PG_IMAGE"
seed_sampledb "$FIXTURE_PG" "$SEED" "$USERS" "$EVENTS"
dump_sampledb "$FIXTURE_PG" custom "$SRC/database/sampledb.dump"
pg_stop "$FIXTURE_PG"

log "Creating borg repo (repokey) with two archives"
PASSFILE="$FIXTURE_DIR/secrets/passphrase"
printf 'redrill-dev-fixture-%s\n' "$SEED" > "$PASSFILE"   # fixture-only secret, guards nothing real
chmod 600 "$PASSFILE"
export BORG_PASSPHRASE="redrill-dev-fixture-$SEED"
REPO="$FIXTURE_DIR/repo"
borg init --encryption=repokey "$REPO"

# stale-source: both archives carry a 30-day-old timestamp — the "perfect
# cron that silently stopped" shape.
TS_2D=""
if [[ "$SABOTAGE" == stale-source ]]; then
  TS_2D=$(fmt_epoch "$(epoch_days_ago 30)" '%Y-%m-%dT%H:%M:%S')
fi

borg_create() { # archive-name [paths...]
  local name=$1
  shift
  if [[ -n "$TS_2D" ]]; then
    ( cd "$SRC" && borg create --timestamp "$TS_2D" "$REPO::$name" "$@" )
  else
    ( cd "$SRC" && borg create "$REPO::$name" "$@" )
  fi
}

if [[ "$SABOTAGE" == missing-data-dir ]]; then
  # The bad-exclude shape: config and the DB dump made it, data/ never did.
  borg_create "seed$SEED-1" config database
  printf 'added after the first archive (seed=%s)\n' "$SEED" > "$SRC/config/added-in-second-archive.txt"
  borg_create "seed$SEED-2" config database
else
  borg_create "seed$SEED-1" .
  printf 'added after the first archive (seed=%s)\n' "$SEED" > "$SRC/data/docs/added-in-second-archive.txt"
  borg_create "seed$SEED-2" .
fi

if [[ "$SABOTAGE" == truncated-segment ]]; then
  # Pick the largest segment with a read loop — a sort|head pipe could
  # SIGPIPE under pipefail.
  SEG="" SEG_BYTES=0
  while IFS= read -r f; do
    b=$(file_bytes "$f")
    if [[ "$b" -gt "$SEG_BYTES" ]]; then SEG="$f" SEG_BYTES="$b"; fi
  done < <(find "$REPO/data" -type f)
  [[ -n "$SEG" ]] || die "no segment file found to truncate"
  truncate -s 4096 "$SEG"
  log "SABOTAGE: truncated segment $SEG ($(human "$SEG_BYTES") -> 4 KiB) — borg check must now fail"
fi

log "Fixture ready"
note "repo:            $REPO  (archives: seed$SEED-1, seed$SEED-2)"
note "passphrase file: $PASSFILE"
note "source tree:     $SRC ($(human "$(dir_bytes "$SRC")"))"
case "$SABOTAGE" in
  stale-source)      note "SABOTAGE=stale-source: a drill with snapshot_max_age <30d MUST fail at L1" ;;
  truncated-segment) note "SABOTAGE=truncated-segment: a drill MUST fail at L1 (native_check)" ;;
  missing-data-dir)  note "SABOTAGE=missing-data-dir: a drill asserting path_exists data/ MUST fail at L2" ;;
esac
note ""
note "Drill it (drill.sh finds this fixture by default):"
note "  dev/shell.sh dev/drill.sh"
