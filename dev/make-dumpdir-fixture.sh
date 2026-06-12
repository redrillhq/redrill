#!/usr/bin/env bash
#
# Build a dumpdir fixture for dev e2e drills: a directory of timestamped
# plain-SQL gzipped pg_dump files (the "nightly pg_dump cron" shape),
# three generations with backdated mtimes so `pick: newest` is meaningful.
# The newest dump contains extra rows the older ones lack.
#
# Reproducible: same SEED -> same rows and same dump bytes (gzip -n), except
# timestamps, which stay "now"-relative on purpose.
#
# Run inside the dev env: dev/shell.sh dev/make-dumpdir-fixture.sh
set -euo pipefail
. "$(dirname "$0")/lib.sh"

DEV_DATA=${DEV_DATA:-/var/tmp/drillbit-dev}
FIXTURE_DIR=${FIXTURE_DIR:-$DEV_DATA/dumpdir-fixture}
SEED=${SEED:-42}
USERS=${USERS:-500}
EVENTS=${EVENTS:-2000}
PG_IMAGE=${PG_IMAGE:-postgres:16}
FIXTURE_PG=${FIXTURE_PG:-drillbit-dev-fixture-pg}

command -v docker >/dev/null || die "docker CLI not found — run this via dev/shell.sh"
docker info >/dev/null 2>&1 || die "docker daemon not reachable"

trap 'pg_stop "$FIXTURE_PG"' EXIT

log "Building dumpdir fixture (SEED=$SEED) at $FIXTURE_DIR"
rm -rf "${FIXTURE_DIR:?}"
mkdir -p "$FIXTURE_DIR"

log "Seeding postgres ($PG_IMAGE): $USERS users, $EVENTS events"
pg_start "$FIXTURE_PG" "$PG_IMAGE"
seed_sampledb "$FIXTURE_PG" "$SEED" "$USERS" "$EVENTS"

EPOCH_2D=$(epoch_days_ago 2)
EPOCH_1D=$(epoch_days_ago 1)
F_2D="$FIXTURE_DIR/myapp-$(fmt_epoch "$EPOCH_2D" %Y%m%d).sql.gz"
F_1D="$FIXTURE_DIR/myapp-$(fmt_epoch "$EPOCH_1D" %Y%m%d).sql.gz"
F_0D="$FIXTURE_DIR/myapp-$(date +%Y%m%d).sql.gz"

log "Dumping three generations (mtimes backdated 2d / 1d / now)"
dump_sampledb "$FIXTURE_PG" plain-gz "$F_2D"
backdate "$EPOCH_2D" "$F_2D"
dump_sampledb "$FIXTURE_PG" plain-gz "$F_1D"
backdate "$EPOCH_1D" "$F_1D"
grow_events "$FIXTURE_PG" $(( EVENTS + 1 )) 100   # newest dump differs: 100 fresh events
dump_sampledb "$FIXTURE_PG" plain-gz "$F_0D"
pg_stop "$FIXTURE_PG"

log "Fixture ready"
note "dir: $FIXTURE_DIR"
ls -l "$FIXTURE_DIR" | sed 's/^/    /' >&2
note ""
note "Drill it:"
note "  DUMP_DIR='$FIXTURE_DIR' dev/shell.sh dev/drill.sh"
