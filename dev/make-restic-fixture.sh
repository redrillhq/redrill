#!/usr/bin/env bash
#
# Copyright (C) 2026 Andrew Alyamovsky
# SPDX-License-Identifier: AGPL-3.0-or-later
#

#
# Build a deterministic restic fixture repo for dev e2e runs:
#   sample file tree + seeded postgres dump (custom format), packaged as a
#   restic repo with two snapshots (so "newest" selection is meaningful).
#
# Same SEED -> same tree bytes, same DB rows. Encryption makes the repo bytes
# themselves non-reproducible (like borg's repokey); the contents reproduce
# row-for-row. Timestamps stay "now" on purpose — freshness checks need
# recent data.
#
# SABOTAGE variants (a drill against the fixture MUST then fail):
#   SABOTAGE=missing-pack      delete a pack file  -> restic check / native_check catches it
#   SABOTAGE=stale-source      snapshots backdated 30d -> snapshot_max_age catches it
#   SABOTAGE=missing-data-dir  data/ excluded from the backup -> path_exists catches it
#
# This builder WRITES — but only inside FIXTURE_DIR and a temp pg container.
# The product invariant is untouched: redrill itself never writes to
# repositories; building test fixtures is test-setup's job.
#
# Run inside the dev env: dev/shell.sh dev/make-restic-fixture.sh
set -euo pipefail
. "$(dirname "$0")/lib.sh"

DEV_DATA=${DEV_DATA:-/var/tmp/redrill-dev}
FIXTURE_DIR=${FIXTURE_DIR:-$DEV_DATA/restic-fixture}
SEED=${SEED:-42}
NUM_FILES=${NUM_FILES:-300}
USERS=${USERS:-500}
EVENTS=${EVENTS:-2000}
PG_IMAGE=${PG_IMAGE:-postgres:16}
FIXTURE_PG=${FIXTURE_PG:-redrill-dev-fixture-pg}
SABOTAGE=${SABOTAGE:-}

case "$SABOTAGE" in
  ""|missing-pack|stale-source|missing-data-dir) ;;
  *) die "unknown SABOTAGE '$SABOTAGE' (missing-pack | stale-source | missing-data-dir)" ;;
esac

command -v restic >/dev/null || die "restic not found — the dev image predates it; refresh with: docker rmi redrill-dev"
command -v docker >/dev/null || die "docker CLI not found — run this via dev/shell.sh"
docker info >/dev/null 2>&1 || die "docker daemon not reachable"

trap 'pg_stop "$FIXTURE_PG"' EXIT

log "Building restic fixture (SEED=$SEED${SABOTAGE:+, SABOTAGE=$SABOTAGE}) at $FIXTURE_DIR"
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

log "Creating restic repo with two snapshots"
PASSFILE="$FIXTURE_DIR/secrets/password"
printf 'redrill-dev-fixture-%s\n' "$SEED" > "$PASSFILE"   # fixture-only secret, guards nothing real
chmod 600 "$PASSFILE"
export RESTIC_PASSWORD="redrill-dev-fixture-$SEED"
export RESTIC_REPOSITORY="$FIXTURE_DIR/repo"
restic init >/dev/null

BACKUP_TS=""
if [[ "$SABOTAGE" == stale-source ]]; then
  BACKUP_TS=$(fmt_epoch "$(epoch_days_ago 30)" '%Y-%m-%d %H:%M:%S')
fi

restic_backup() { # extra restic-backup args...
  if [[ -n "$BACKUP_TS" ]]; then
    ( cd "$SRC" && restic backup --quiet --time "$BACKUP_TS" "$@" . )
  else
    ( cd "$SRC" && restic backup --quiet "$@" . )
  fi
}

case "$SABOTAGE" in
  missing-data-dir)
    restic_backup --exclude data
    printf 'added after the first snapshot (seed=%s)\n' "$SEED" > "$SRC/config/added-in-second-snapshot.txt"
    restic_backup --exclude data
    ;;
  *)
    restic_backup
    printf 'added after the first snapshot (seed=%s)\n' "$SEED" > "$SRC/data/docs/added-in-second-snapshot.txt"
    restic_backup
    ;;
esac

if [[ "$SABOTAGE" == missing-pack ]]; then
  # Glob, not find|head: head's early exit would SIGPIPE find under pipefail.
  PACK=""
  for f in "$RESTIC_REPOSITORY"/data/*/*; do
    if [[ -f "$f" ]]; then PACK="$f"; break; fi
  done
  [[ -n "$PACK" ]] || die "no pack file found to delete"
  rm -f "$PACK"   # -f: restic packs are 0400; rm must not stall on the prompt
  log "SABOTAGE: deleted pack $(basename "$PACK") — restic check must now fail"
fi

log "Fixture ready"
note "repo:          $RESTIC_REPOSITORY"
note "password file: $PASSFILE"
note "source tree:   $SRC ($(human "$(dir_bytes "$SRC")"))"
restic snapshots --no-lock --compact 2>/dev/null | sed 's/^/    /' >&2 || true
case "$SABOTAGE" in
  missing-pack)     note "SABOTAGE=missing-pack: a drill MUST fail at L1 (native_check)" ;;
  stale-source)     note "SABOTAGE=stale-source: a drill with snapshot_max_age <30d MUST fail at L1" ;;
  missing-data-dir) note "SABOTAGE=missing-data-dir: a drill asserting path_exists data/ MUST fail at L2" ;;
esac
note ""
note "Drill it with redrill itself (drill.sh is borg/dumpdir-only) — source snippet:"
note "  sources:"
note "    - name: dev-restic"
note "      type: restic"
note "      repo: \"$RESTIC_REPOSITORY\""
note "      password_file: \"$PASSFILE\""
