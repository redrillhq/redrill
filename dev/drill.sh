#!/usr/bin/env bash
#
# dev/drill.sh — manual end-to-end drill runner (dev toolset, kept for the
# life of the project; see dev/README.md).
#
# The loop redrill will productize, runnable by hand against real engines:
#   fetch/restore from a backup source -> load the contained DB dump into a
#   postgres sandbox (network=none) -> run SQL asserts -> report timings,
#   bytes, and verdicts.
#
# Source access is strictly read-only by construction: the only borg commands
# here are `borg info`, `borg list`, `borg extract`; dumpdir files are only
# read. No create/prune/delete/compact exists in this script — ever.
#
# Modes (pick one):
#   borg:     BORG_REPO=...  [BORG_PASSPHRASE_FILE=...]
#             default if the borg fixture exists at $DEV_DATA/borg-fixture
#   dumpdir:  DUMP_DIR=...   [PATTERN='*.sql.gz']   (pick: newest by mtime)
#
# Exit codes (product taxonomy, DESIGN §9.8): 0 asserts pass · 1 a check
# failed — the backup is bad · 2 couldn't check — infra/spike error.
#
# Run inside the dev env: dev/shell.sh dev/drill.sh
set -euo pipefail
. "$(dirname "$0")/lib.sh"

DEV_DATA=${DEV_DATA:-/var/tmp/redrill-dev}

# ---------- mode resolution ----------
MODE=""
if [[ -n "${BORG_REPO:-}" && -n "${DUMP_DIR:-}" ]]; then
  die "set either BORG_REPO or DUMP_DIR, not both"
elif [[ -n "${DUMP_DIR:-}" ]]; then
  MODE=dumpdir
elif [[ -n "${BORG_REPO:-}" ]]; then
  MODE=borg
elif [[ -d "$DEV_DATA/borg-fixture/repo" ]]; then
  MODE=borg
  BORG_REPO="$DEV_DATA/borg-fixture/repo"
  BORG_PASSPHRASE_FILE=${BORG_PASSPHRASE_FILE:-$DEV_DATA/borg-fixture/secrets/passphrase}
else
  die "no source: set BORG_REPO=... or DUMP_DIR=..., or build a fixture first (dev/shell.sh dev/make-borg-fixture.sh)"
fi

# ---------- inputs (env) ----------
SCRATCH_DIR=${SCRATCH_DIR:-$DEV_DATA/scratch}
SEED=${SEED:-42}                                     # sampling determinism
ARCHIVE=${ARCHIVE:-}                                 # borg: default newest
SAMPLE_FILES=${SAMPLE_FILES:-200}                    # borg: random files for the L2-style sample
PATTERN=${PATTERN:-*.sql.gz}                         # dumpdir: glob for dump files
PG_IMAGE=${PG_IMAGE:-postgres:16}
PG_CONTAINER=${PG_CONTAINER:-redrill-dev-pg}
CONFIG_PATH=${CONFIG_PATH:-}                         # borg: override path discovery
DB_DUMP_PATH=${DB_DUMP_PATH:-}                       # borg: override path discovery
ASSERT_DB=${ASSERT_DB:-}                             # override the database asserts run against
ASSERT_SQL_1=${ASSERT_SQL_1:-select count(*) from users}      # expected: scalar > 0
ASSERT_SQL_2=${ASSERT_SQL_2:-select * from events limit 1}    # expected: no error
KEEP=${KEEP:-0}                                      # 1 = keep the pg sandbox afterwards

# Passphrase only via file or env — never echoed, never on a command line.
if [[ -n "${BORG_PASSPHRASE_FILE:-}" ]]; then
  BORG_PASSPHRASE=$(<"$BORG_PASSPHRASE_FILE")
  export BORG_PASSPHRASE
fi

RESTORE_DIR="$SCRATCH_DIR/restore"
OUT_DIR="$SCRATCH_DIR/out"
REPORT="$OUT_DIR/results.md"
SCRIPT_T0=$(date +%s)
EXIT_CODE=0
TIMINGS=()

t_start() { _T0=$(date +%s); }
t_stop()  { _DUR=$(( $(date +%s) - _T0 )); }
timing()  { TIMINGS+=("| $1 | ${2}s | $3 |"); }

cleanup() {
  rc=$?
  if [[ "$KEEP" == 1 ]]; then
    note "KEEP=1: postgres sandbox '$PG_CONTAINER' left running. Inspect with:"
    note "  docker exec -it $PG_CONTAINER psql -U postgres"
  else
    docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
  fi
  note "Scratch (restored data, listings, logs, report) kept at: $SCRATCH_DIR"
  exit "$rc"
}
trap cleanup EXIT

# ---------- preflight ----------
command -v docker >/dev/null || die "docker CLI not found — run this via dev/shell.sh"
docker info >/dev/null 2>&1 || die "docker daemon not reachable"
if [[ "$MODE" == borg ]]; then
  command -v borg >/dev/null || die "borg not found — run this via dev/shell.sh (all deps live in the dev image)"
fi

mkdir -p "$OUT_DIR"
rm -rf "${RESTORE_DIR:?}"
mkdir -p "$RESTORE_DIR"

log "Mode: $MODE · scratch: $SCRATCH_DIR (free: $(df -h "$SCRATCH_DIR" | awk 'NR==2{print $4}'))"

# Vars the report consumes in both modes.
ARCHIVE_COUNT="-"; TOTAL_ENTRIES="-"; TOTAL_FILES="-"
CONFIG_CANDS=""; DUMP_CANDS=""; CONFIG_RESTORED="n/a"
SAMPLE_BYTES=0; DUMP_AGE_H="-"

if [[ "$MODE" == borg ]]; then
  # ---------- borg: reachability, archive pick, listing ----------
  log "borg info — repo reachability ($BORG_REPO)"
  t_start
  borg info "$BORG_REPO" > "$OUT_DIR/repo-info.txt"
  t_stop
  timing "borg info (repo)" "$_DUR" "-"

  log "borg list — archives"
  t_start
  borg list --short "$BORG_REPO" > "$OUT_DIR/archives.txt"
  t_stop
  ARCHIVE_COUNT=$(wc -l <"$OUT_DIR/archives.txt" | tr -d ' ')
  [[ "$ARCHIVE_COUNT" -gt 0 ]] || die "repo contains no archives"
  if [[ -z "$ARCHIVE" ]]; then
    ARCHIVE=$(tail -n1 "$OUT_DIR/archives.txt")   # borg lists oldest -> newest
  fi
  timing "borg list (archives)" "$_DUR" "$ARCHIVE_COUNT archives"
  log "Using archive: $ARCHIVE"

  t_start
  borg info "$BORG_REPO::$ARCHIVE" > "$OUT_DIR/archive-info.txt"
  t_stop
  timing "borg info (archive)" "$_DUR" "-"

  log "borg list — full file listing of the archive (can take a while on real repos)"
  t_start
  borg list --format '{mode} {path}{NL}' "$BORG_REPO::$ARCHIVE" > "$OUT_DIR/files-with-mode.txt"
  t_stop
  grep '^-' "$OUT_DIR/files-with-mode.txt" | cut -d' ' -f2- > "$OUT_DIR/files.txt" || true
  TOTAL_ENTRIES=$(wc -l <"$OUT_DIR/files-with-mode.txt" | tr -d ' ')
  TOTAL_FILES=$(wc -l <"$OUT_DIR/files.txt" | tr -d ' ')
  timing "borg list (archive contents)" "$_DUR" "$TOTAL_ENTRIES entries, $TOTAL_FILES regular files"

  # ---------- borg: discover in-archive paths ----------
  log "Discovering config and DB dump paths in the archive"
  CONFIG_CANDS=$(grep -E '(^|/)config/config\.php$' "$OUT_DIR/files.txt" | head -n10 || true)
  if [[ -z "$CONFIG_PATH" ]]; then
    CONFIG_PATH=$(head -n1 <<<"$CONFIG_CANDS")
  fi
  if [[ -n "$CONFIG_PATH" ]]; then
    log "Using config path: $CONFIG_PATH"
  else
    log "WARNING: no config/config.php found — inspect $OUT_DIR/files.txt, rerun with CONFIG_PATH=<path>"
  fi

  DUMP_CANDS=$(grep -iE '(^|/)[^/]*(database|db)[^/]*(dump|backup)[^/]*$|\.dump$' "$OUT_DIR/files.txt" | head -n10 || true)
  if [[ -z "$DUMP_CANDS" ]]; then
    DUMP_CANDS=$(grep -iE '\.sql(\.(gz|zst))?$' "$OUT_DIR/files.txt" | head -n10 || true)
  fi
  if [[ -z "$DB_DUMP_PATH" ]]; then
    DB_DUMP_PATH=$(head -n1 <<<"$DUMP_CANDS")
  fi
  [[ -n "$DB_DUMP_PATH" ]] || die "no DB dump candidate found — inspect $OUT_DIR/files.txt, rerun with DB_DUMP_PATH=<path>"
  log "Using DB dump path: $DB_DUMP_PATH"

  # ---------- borg: sample restore (L2-style) ----------
  log "borg extract — sample restore ($SAMPLE_FILES seeded-random files + config)"
  SAMPLE_LIST="$OUT_DIR/sample.txt"
  grep -vxF -- "$DB_DUMP_PATH" "$OUT_DIR/files.txt" > "$OUT_DIR/sample-pool.txt" || true
  awk -v n="$SAMPLE_FILES" -v seed="$SEED" \
    'BEGIN{srand(seed)} {if (NR<=n) r[NR]=$0; else {i=int(rand()*NR)+1; if (i<=n) r[i]=$0}} END{for (k in r) print r[k]}' \
    "$OUT_DIR/sample-pool.txt" > "$SAMPLE_LIST"
  if [[ -n "$CONFIG_PATH" ]]; then printf '%s\n' "$CONFIG_PATH" >> "$SAMPLE_LIST"; fi
  sort -u "$SAMPLE_LIST" -o "$SAMPLE_LIST"
  SAMPLE_PATHS=()
  while IFS= read -r p; do
    if [[ -n "$p" ]]; then SAMPLE_PATHS+=("$p"); fi
  done < "$SAMPLE_LIST"

  if [[ ${#SAMPLE_PATHS[@]} -gt 0 ]]; then
    t_start
    ( cd "$RESTORE_DIR" && borg extract "$BORG_REPO::$ARCHIVE" -- "${SAMPLE_PATHS[@]}" )
    t_stop
    SAMPLE_BYTES=$(dir_bytes "$RESTORE_DIR")
    timing "borg extract (sample, ${#SAMPLE_PATHS[@]} files)" "$_DUR" "$(human "$SAMPLE_BYTES"), $(rate "$SAMPLE_BYTES" "$_DUR")"
  else
    log "WARNING: empty sample — skipping sample restore"
  fi

  CONFIG_RESTORED="not found"
  if [[ -n "$CONFIG_PATH" && -f "$RESTORE_DIR/$CONFIG_PATH" ]]; then
    CONFIG_RESTORED="restored, $(human "$(file_bytes "$RESTORE_DIR/$CONFIG_PATH")")"
  fi

  # ---------- borg: extract the DB dump ----------
  log "borg extract — DB dump"
  t_start
  ( cd "$RESTORE_DIR" && borg extract "$BORG_REPO::$ARCHIVE" -- "$DB_DUMP_PATH" )
  t_stop
  DUMP_FILE="$RESTORE_DIR/$DB_DUMP_PATH"
  [[ -f "$DUMP_FILE" ]] || die "dump missing after extract: $DUMP_FILE"
  DUMP_EXTRACT_BYTES=$(file_bytes "$DUMP_FILE")
  timing "borg extract (db dump)" "$_DUR" "$(human "$DUMP_EXTRACT_BYTES"), $(rate "$DUMP_EXTRACT_BYTES" "$_DUR")"

else
  # ---------- dumpdir: pick newest, integrity, fetch ----------
  [[ -d "$DUMP_DIR" ]] || die "DUMP_DIR does not exist: $DUMP_DIR"
  log "Picking newest dump in $DUMP_DIR (pattern: $PATTERN, pick: newest)"
  # shellcheck disable=SC2086  # PATTERN is an intentional glob
  NEWEST=$(cd "$DUMP_DIR" && ls -t -- $PATTERN 2>/dev/null | head -n1 || true)
  [[ -n "$NEWEST" ]] || die "no files matching '$PATTERN' in $DUMP_DIR"
  SRC_FILE="$DUMP_DIR/$NEWEST"
  DUMP_AGE_H=$(( ( $(date +%s) - $(mtime_epoch "$SRC_FILE") ) / 3600 ))
  DB_DUMP_PATH=$NEWEST
  log "Newest: $NEWEST (mtime age: ${DUMP_AGE_H}h)"

  case "$NEWEST" in
    *.gz)
      t_start
      if gzip -t "$SRC_FILE" 2>"$OUT_DIR/compression-test.err"; then
        t_stop; timing "compression_test (gzip -t)" "$_DUR" "ok"
      else
        log "compression_test FAILED — the dump file is corrupt. Backup is bad (fail, not error)."
        exit 1
      fi
      ;;
    *.zst)
      command -v zstd >/dev/null || die "dump is .zst but zstd is not installed"
      t_start
      if zstd -t -q "$SRC_FILE" 2>"$OUT_DIR/compression-test.err"; then
        t_stop; timing "compression_test (zstd -t)" "$_DUR" "ok"
      else
        log "compression_test FAILED — the dump file is corrupt. Backup is bad (fail, not error)."
        exit 1
      fi
      ;;
  esac

  t_start
  mkdir -p "$RESTORE_DIR/dump"
  cp "$SRC_FILE" "$RESTORE_DIR/dump/"
  t_stop
  DUMP_FILE="$RESTORE_DIR/dump/$NEWEST"
  DUMP_EXTRACT_BYTES=$(file_bytes "$DUMP_FILE")
  timing "fetch dump (cp)" "$_DUR" "$(human "$DUMP_EXTRACT_BYTES")"
fi

# ---------- decompress + dump format detection (shared from here on) ----------
MAGIC=$(magic_hex "$DUMP_FILE")
case "$MAGIC" in
  1f8b*)
    log "Dump is gzip-compressed — decompressing"
    if [[ "$DUMP_FILE" != *.gz ]]; then mv "$DUMP_FILE" "$DUMP_FILE.gz"; DUMP_FILE="$DUMP_FILE.gz"; fi
    t_start; gunzip "$DUMP_FILE"; t_stop
    DUMP_FILE=${DUMP_FILE%.gz}
    timing "gunzip dump" "$_DUR" "$(human "$(file_bytes "$DUMP_FILE")") uncompressed"
    MAGIC=$(magic_hex "$DUMP_FILE")
    ;;
  28b52ffd*)
    command -v zstd >/dev/null || die "dump is zstd-compressed but zstd is not installed"
    log "Dump is zstd-compressed — decompressing"
    t_start; zstd -dq --rm "$DUMP_FILE" -o "$DUMP_FILE.raw"; t_stop
    DUMP_FILE="$DUMP_FILE.raw"
    timing "zstd -d dump" "$_DUR" "$(human "$(file_bytes "$DUMP_FILE")") uncompressed"
    MAGIC=$(magic_hex "$DUMP_FILE")
    ;;
esac
DUMP_FINAL_BYTES=$(file_bytes "$DUMP_FILE")

DUMP_FORMAT=plain
if [[ "$MAGIC" == 5047444d50* ]]; then DUMP_FORMAT=custom; fi   # "PGDMP"
DUMP_PG_VERSION="unknown"
if [[ "$DUMP_FORMAT" == plain ]]; then
  DUMP_PG_VERSION=$(grep -m1 -a '^-- Dumped from database version' "$DUMP_FILE" || echo "unknown")
fi
log "Dump format: $DUMP_FORMAT ($(human "$DUMP_FINAL_BYTES") uncompressed)"

# ---------- postgres sandbox + load ----------
log "Postgres sandbox ($PG_IMAGE, network=none)"
docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
if ! docker image inspect "$PG_IMAGE" >/dev/null 2>&1; then
  t_start; docker pull "$PG_IMAGE" >/dev/null; t_stop
  timing "docker pull $PG_IMAGE (one-time)" "$_DUR" "-"
fi
t_start
pg_start "$PG_CONTAINER" "$PG_IMAGE" --network none --label io.redrill.dev=1
t_stop
timing "postgres start -> ready" "$_DUR" "$PG_IMAGE"

docker cp "$DUMP_FILE" "$PG_CONTAINER:/tmp/db.dump" >/dev/null
if [[ "$DUMP_FORMAT" == custom ]]; then
  DUMP_PG_VERSION=$(docker exec "$PG_CONTAINER" pg_restore -l /tmp/db.dump 2>/dev/null \
    | grep -m1 -i 'dumped from database version' || echo "unknown")
fi
DUMP_MAJOR=$(grep -oE '[0-9]+' <<<"$DUMP_PG_VERSION" | head -n1 || true)
IMAGE_MAJOR=$(grep -oE '[0-9]+' <<<"$PG_IMAGE" | head -n1 || true)
if [[ -n "$DUMP_MAJOR" && -n "$IMAGE_MAJOR" && "$DUMP_MAJOR" -gt "$IMAGE_MAJOR" ]]; then
  log "WARNING: dump is from postgres $DUMP_MAJOR, sandbox is $PG_IMAGE — the version trap. If the load fails, rerun with PG_IMAGE=postgres:$DUMP_MAJOR"
fi

LOAD_LOG="$OUT_DIR/load.log"
log "Loading dump ($DUMP_FORMAT) — errors are tolerated and counted; asserts give the verdict"
t_start
if [[ "$DUMP_FORMAT" == custom ]]; then
  docker exec "$PG_CONTAINER" pg_restore --create --no-owner --no-privileges \
    -U postgres -d postgres /tmp/db.dump >"$LOAD_LOG" 2>&1 || true
else
  docker exec "$PG_CONTAINER" psql -q -U postgres -d postgres -f /tmp/db.dump >"$LOAD_LOG" 2>&1 || true
fi
t_stop
LOAD_DUR=$_DUR
LOAD_ERRORS=$(grep -ci 'error' "$LOAD_LOG" || true)
timing "load dump ($DUMP_FORMAT)" "$LOAD_DUR" "$(rate "$DUMP_FINAL_BYTES" "$LOAD_DUR"), $LOAD_ERRORS error lines"

# ---------- SQL asserts ----------
log "SQL asserts"
if [[ -z "$ASSERT_DB" ]]; then
  NEW_DBS=$(docker exec "$PG_CONTAINER" psql -U postgres -tAc \
    "select datname from pg_database where not datistemplate and datname <> 'postgres'" || true)
  if [[ -z "$NEW_DBS" ]]; then
    ASSERT_DB=postgres
  else
    ASSERT_DB=$(head -n1 <<<"$NEW_DBS")
  fi
fi
log "Asserting against database: $ASSERT_DB"

psql_assert() { docker exec "$PG_CONTAINER" psql -U postgres -d "$ASSERT_DB" -tA -v ON_ERROR_STOP=1 -c "$1" 2>&1; }

# Assert 1: scalar > 0. Query error -> ERROR (couldn't check), 0/garbage -> FAIL.
A1_OUT=$(psql_assert "$ASSERT_SQL_1") && A1_RC=0 || A1_RC=$?
A1_OUT=$(tr '\n|' ' /' <<<"$A1_OUT" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
if [[ $A1_RC -ne 0 ]]; then
  A1_RES="ERROR — query failed: $A1_OUT"; EXIT_CODE=2
elif [[ "$A1_OUT" =~ ^[0-9]+$ && "$A1_OUT" -gt 0 ]]; then
  A1_RES="PASS — returned $A1_OUT"
else
  A1_RES="FAIL — returned '$A1_OUT', expected > 0"
  if [[ $EXIT_CODE -eq 0 ]]; then EXIT_CODE=1; fi
fi
log "assert 1 [$ASSERT_SQL_1]: $A1_RES"

# Assert 2: sql_no_error — an erroring query means the restored data is bad.
A2_OUT=$(psql_assert "$ASSERT_SQL_2") && A2_RC=0 || A2_RC=$?
A2_OUT=$(tr '\n|' ' /' <<<"$A2_OUT" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
if [[ $A2_RC -eq 0 ]]; then
  A2_RES="PASS — no error"
else
  A2_RES="FAIL — $A2_OUT"
  if [[ $EXIT_CODE -eq 0 ]]; then EXIT_CODE=1; fi
fi
log "assert 2 [$ASSERT_SQL_2]: $A2_RES"

DB_SIZE=$(docker exec "$PG_CONTAINER" psql -U postgres -tAc \
  "select pg_size_pretty(pg_database_size('$ASSERT_DB'))" 2>/dev/null || echo "n/a")
PG_MEM=$(docker stats --no-stream --format '{{.MemUsage}}' "$PG_CONTAINER" 2>/dev/null || echo "n/a")

# ---------- report ----------
TOTAL_DUR=$(( $(date +%s) - SCRIPT_T0 ))
{
  echo "# redrill dev drill — results"
  echo
  echo "- date (UTC): $(date -u '+%Y-%m-%d %H:%M:%S')"
  echo "- host: $(uname -srm)"
  echo "- mode: $MODE"
  if [[ "$MODE" == borg ]]; then
    echo "- versions: $(borg --version), $(docker --version)"
    echo "- repo: \`$BORG_REPO\` ($ARCHIVE_COUNT archives)"
    echo "- archive: \`$ARCHIVE\` ($TOTAL_ENTRIES entries, $TOTAL_FILES regular files)"
  else
    echo "- versions: $(docker --version)"
    echo "- dump dir: \`$DUMP_DIR\` (pattern \`$PATTERN\`, picked newest)"
    echo "- picked: \`$DB_DUMP_PATH\` (mtime age: ${DUMP_AGE_H}h)"
  fi
  echo
  if [[ "$MODE" == borg ]]; then
    echo "## In-archive paths (the M11-gate payload when run against the real AIO repo)"
    echo
    echo "| What | Path | Status |"
    echo "|---|---|---|"
    echo "| config.php | \`${CONFIG_PATH:-NOT FOUND}\` | $CONFIG_RESTORED |"
    echo "| DB dump | \`$DB_DUMP_PATH\` | restored; format: $DUMP_FORMAT |"
    echo
    echo "- other candidates seen (verify the right one was picked):"
    echo '```'
    echo "config.php: ${CONFIG_CANDS:-none}"
    echo "db dump:    ${DUMP_CANDS:-none}"
    echo '```'
    echo
  fi
  echo "- dump version header: $DUMP_PG_VERSION"
  echo "- dump size: $(human "$DUMP_EXTRACT_BYTES") as stored, $(human "$DUMP_FINAL_BYTES") uncompressed"
  echo
  echo "## Timings & IO"
  echo
  echo "| Step | Wall time | Notes |"
  echo "|---|---|---|"
  printf '%s\n' "${TIMINGS[@]}"
  echo
  echo "- total wall time: ${TOTAL_DUR}s"
  echo "- restored to scratch: $(human "$(dir_bytes "$RESTORE_DIR")")"
  echo "- loaded DB size: $DB_SIZE (database: \`$ASSERT_DB\`)"
  echo "- postgres container memory after load: $PG_MEM"
  echo "- load error lines: $LOAD_ERRORS (full log: out/load.log)"
  echo '```'
  grep -im5 'error' "$LOAD_LOG" || echo "(no error lines)"
  echo '```'
  echo
  echo "## Asserts (database: \`$ASSERT_DB\`)"
  echo
  echo "| # | SQL | Expected | Result |"
  echo "|---|---|---|---|"
  echo "| 1 | \`$ASSERT_SQL_1\` | > 0 | $A1_RES |"
  echo "| 2 | \`$ASSERT_SQL_2\` | no error | $A2_RES |"
  echo
  echo "## Follow-ups"
  echo
  echo "- Record timings/anomalies relevant to the current milestone."
  echo "- If this ran against the real Nextcloud AIO repo (M11 dogfood gate): update the"
  echo "  docs/agents/DESIGN.md Appendix A placeholders (config.php path, dump \`extract_path\`) and the"
  echo "  §12 recipe caveat with the verified paths above."
} > "$REPORT"

echo
echo "============================================================"
echo " DRILL SUMMARY ($MODE)"
if [[ "$MODE" == borg ]]; then
  echo "   archive:      $ARCHIVE"
  echo "   config.php:   ${CONFIG_PATH:-NOT FOUND} ($CONFIG_RESTORED)"
fi
echo "   db dump:      $DB_DUMP_PATH (format: $DUMP_FORMAT)"
echo "   load:         ${LOAD_DUR}s, $LOAD_ERRORS error lines"
echo "   assert 1:     $A1_RES"
echo "   assert 2:     $A2_RES"
echo "   total wall:   ${TOTAL_DUR}s"
echo
echo "   full report:  $REPORT"
echo "============================================================"

exit "$EXIT_CODE"
