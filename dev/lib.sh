# dev/lib.sh — shared helpers for the redrill dev toolset. Sourced, never executed.
# Portability target: bash 3.2+ (macOS default) and Linux. Functions only, no side effects.

log()  { printf '\n==> %s\n' "$*" >&2; }
note() { printf '    %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 2; }

human() { # bytes -> human-readable
  awk -v b="$1" 'BEGIN{ if (b>=1073741824) printf "%.1f GiB", b/1073741824;
    else if (b>=1048576) printf "%.1f MiB", b/1048576;
    else if (b>=1024) printf "%.1f KiB", b/1024;
    else printf "%d B", b }'
}
rate() { # bytes seconds -> MiB/s
  awk -v b="$1" -v s="$2" 'BEGIN{ if (s<1) s=1; printf "%.1f MiB/s", b/s/1048576 }'
}
dir_bytes()  { du -sk "$1" | awk '{print $1*1024}'; }
file_bytes() { wc -c <"$1" | tr -d ' '; }
magic_hex()  { od -An -tx1 -N5 "$1" | tr -d ' \n'; }

epoch_days_ago() { echo $(( $(date +%s) - $1 * 86400 )); }
fmt_epoch() { # epoch strftime-fmt — works on GNU (-d @) and BSD (-r) date
  date -d "@$1" "+$2" 2>/dev/null || date -r "$1" "+$2"
}
mtime_epoch() { # file -> mtime as epoch (GNU stat, BSD stat fallback)
  stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"
}
backdate() { # epoch file — set mtime
  touch -t "$(fmt_epoch "$1" '%Y%m%d%H%M.%S')" "$2"
}

# Deterministic sample tree: same SEED -> byte-identical content and layout.
# (mtimes are creation time by design — freshness checks need "now"-relative data.)
gen_tree() { # dir num_files seed
  local dir=$1 n=$2 seed=$3 i sub kb
  mkdir -p "$dir/config" "$dir/data/docs" "$dir/data/media" "$dir/data/projects"
  {
    printf '<?php // redrill dev fixture (seed=%s) — stands in for an app config\n' "$seed"
    printf '$CONFIG = array("instanceid" => "devfixture%s", "version" => "1.0.0");\n' "$seed"
  } > "$dir/config/config.php"
  i=1
  while [ "$i" -le "$n" ]; do
    case $(( i % 3 )) in
      0) sub=docs ;;
      1) sub=media ;;
      *) sub=projects ;;
    esac
    kb=$(( (i * 37 + seed) % 197 + 3 ))   # 3..199 KiB, deterministic per file
    if [ "$i" -le 2 ]; then kb=2048; fi   # two ~2 MiB files for throughput feel
    awk -v f="$i" -v seed="$seed" -v kb="$kb" 'BEGIN{
      bytes = kb * 1024; printed = 0; k = 0;
      while (printed < bytes) {
        s = sprintf("redrill dev fixture seed=%s file=%d line=%d\n", seed, f, ++k);
        if (printed + length(s) > bytes) s = substr(s, 1, bytes - printed);
        printf "%s", s; printed += length(s);
      }
    }' > "$dir/data/$sub/file-$(printf '%04d' "$i").txt"
    i=$(( i + 1 ))
  done
}

# --- postgres helpers (fixture seeding; the drill runner has its own sandbox) ---

pg_start() { # name image [extra docker-run args...]
  local name=$1 image=$2 i=0
  shift 2
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" -e POSTGRES_PASSWORD=drill "$@" "$image" >/dev/null
  while [ "$i" -lt 60 ]; do
    if docker exec "$name" pg_isready -U postgres -q 2>/dev/null; then return 0; fi
    sleep 1; i=$(( i + 1 ))
  done
  die "postgres container '$name' not ready after 60s (docker logs $name)"
}
pg_stop() { docker rm -f "$1" >/dev/null 2>&1 || true; }

# sampledb: users + events. setseed() makes the random timestamp offsets
# deterministic for a given SEED; they stay relative to now() on purpose.
seed_sampledb() { # container seed users events
  docker exec -i "$1" psql -q -v ON_ERROR_STOP=1 -U postgres \
    -c 'create database sampledb'
  docker exec -i "$1" psql -q -v ON_ERROR_STOP=1 -U postgres -d sampledb <<SQL
select setseed(0.$2);
create table users(id int primary key, name text not null, email text not null);
insert into users select g, 'user'||g, 'user'||g||'@example.test'
  from generate_series(1, $3) g;
create table events(id int primary key, kind text not null, created_at timestamptz not null);
insert into events select g, 'event-'||(g % 7), now() - (random() * interval '6 days')
  from generate_series(1, $4) g;
SQL
}

grow_events() { # container from-id count — deterministic recent rows (makes "newest" meaningful)
  docker exec -i "$1" psql -q -v ON_ERROR_STOP=1 -U postgres -d sampledb <<SQL
insert into events select g, 'event-late-'||(g % 3), now() - (random() * interval '12 hours')
  from generate_series($2, $2 + $3 - 1) g;
SQL
}

dump_sampledb() { # container custom|plain-gz outfile
  local c=$1 format=$2 out=$3
  case "$format" in
    custom)
      docker exec "$c" pg_dump -U postgres -d sampledb -Fc -f /tmp/fixture.dump
      docker cp "$c:/tmp/fixture.dump" "$out" >/dev/null
      ;;
    plain-gz)
      docker exec "$c" pg_dump -U postgres -d sampledb -f /tmp/fixture.sql
      docker cp "$c:/tmp/fixture.sql" "$out.tmp" >/dev/null
      gzip -n -c "$out.tmp" > "$out"   # -n: no name/timestamp -> reproducible bytes
      rm -f "$out.tmp"
      ;;
    *) die "dump_sampledb: unknown format '$format'" ;;
  esac
}
