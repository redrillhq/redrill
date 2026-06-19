# `dev/` — redrill dev environment

Reproducible, containerized toolset for **real-engine e2e during development**: build
deterministic backup fixtures (borg repo, dumpdir) and run the same drill loop redrill
automates (restore → postgres sandbox → SQL asserts) by hand, with timings and a report.

This is for hands-on e2e runs during development, not part of CI. The automated gates are the
Go tests (`make test`, `make test-integration`) and the sabotage kit (`make test-sabotage`);
use this when you want to drive the real engines by hand and read a drill report.

**The only host dependency is Docker.** Every engine dep (borg, postgres client tools, zstd,
ssh) lives in the `redrill-dev` image — nothing gets installed on the host. The shipped
redrill image bundles its engines the same way.

## Quickstart

```sh
dev/shell.sh dev/make-borg-fixture.sh   # builds image on first run, then the fixture
dev/shell.sh dev/drill.sh               # e2e drill against it (finds the fixture by default)
```

Dumpdir flavor (the "nightly pg_dump cron" shape):

```sh
dev/shell.sh dev/make-dumpdir-fixture.sh
DUMP_DIR=/work/dumpdir-fixture dev/shell.sh dev/drill.sh
```

`dev/shell.sh` with no arguments drops you into an interactive shell in the dev env.

## How it works

| Piece | What it does |
|---|---|
| `Dockerfile` | The dev image: bash, coreutils, borg, postgres client, gzip/zstd, ssh, docker CLI |
| `shell.sh` | Host entrypoint (needs only Docker). Builds the image on first use; mounts the repo at `/repo`, a persistent data volume at `/work`, and the Docker socket |
| `lib.sh` | Shared helpers: deterministic tree/DB generation, pg container lifecycle, formatting |
| `make-borg-fixture.sh` | Sample file tree + seeded pg dump (custom format) → borg repo (repokey, passphrase file), two archives so "newest" matters |
| `make-dumpdir-fixture.sh` | Three timestamped `myapp-*.sql.gz` generations, mtimes backdated 2d/1d/now, newest contains extra rows — exercises `pick: newest` |
| `drill.sh` | The e2e loop, borg or dumpdir mode: fetch/restore (sample + DB dump) → postgres sandbox (`network=none`, labeled, removed on exit) → two SQL asserts → `results.md` |

Containers started from inside the dev env (fixture pg, sandbox pg) run as **siblings** on
the Docker daemon via the mounted socket — nothing is nested. All file transfer into the
sandbox goes through `docker cp`/`docker exec`, so no host paths leak into containers.

## Reproducibility contract

Same `SEED` (default 42) ⇒ same tree bytes, same archive names, same DB rows (`setseed()` in
postgres), byte-identical plain dumps (`gzip -n`); the custom-format dump embeds a `pg_dump`
timestamp, so it reproduces row-for-row rather than byte-for-byte. The deliberate exception:
**timestamps** (file mtimes, `events.created_at` offsets) are relative to *now*, because
freshness checks are only meaningful against recent data.

## Inputs (env knobs)

All pass through `dev/shell.sh` automatically when set on the host, e.g.
`SEED=7 dev/shell.sh dev/make-borg-fixture.sh`.

| Variable | Default | Used by | Meaning |
|---|---|---|---|
| `SEED` | `42` | builders, drill | Determinism seed (tree, rows, sampling) |
| `NUM_FILES` / `USERS` / `EVENTS` | `300` / `500` / `2000` | builders | Fixture sizing |
| `PG_IMAGE` | `postgres:16` | builders, drill | Postgres image (pin to match the dump's major) |
| `FIXTURE_DIR` | `/work/<kind>-fixture` | builders | Where the fixture lands (inside the data volume) |
| `BORG_REPO` | borg fixture if present | drill | Borg mode source |
| `BORG_PASSPHRASE_FILE` / `BORG_PASSPHRASE` | fixture's | drill | Secret ref — file form preferred, never inline in configs |
| `DUMP_DIR`, `PATTERN` | —, `*.sql.gz` | drill | Dumpdir mode source |
| `ARCHIVE` | newest | drill | Borg archive override |
| `SAMPLE_FILES` | `200` | drill | Sample-restore size (seeded random) |
| `CONFIG_PATH` / `DB_DUMP_PATH` | auto-discovered | drill | In-archive path overrides |
| `ASSERT_DB` | auto | drill | Database the asserts run against |
| `ASSERT_SQL_1` / `ASSERT_SQL_2` | `users` count / `events` probe | drill | The two asserts (scalar>0 / no-error) |
| `KEEP` | `0` | drill | `1` keeps the sandbox container for inspection |
| `SCRATCH_DIR` | `/work/scratch` | drill | Restore + output dir |

## Outputs & data lifecycle

Everything generated lives in the `redrill-dev-data` volume, mounted at `/work` — never in
the repo, never on the host filesystem.

- `/work/scratch/out/results.md` — the auto-filled drill report (timings, sizes, throughput,
  load errors, assert verdicts, discovered paths)
- `/work/scratch/out/` — listings and logs (`files.txt`, `load.log`, borg infos)
- Read them via `dev/shell.sh cat /work/scratch/out/results.md` or an interactive shell

Cleanup: `docker volume rm redrill-dev-data` (data), `docker rmi redrill-dev` (image; it
rebuilds on next use). Exit codes from `drill.sh` match the product: `0` pass · `1` check
failed (the backup is bad) · `2` couldn't check (infra).

## Safety properties

- **`drill.sh` is read-only on backup sources by construction** — only `borg info`/`list`/
  `extract`; dump files are only read. Mirrors the product's hard invariant.
- The builders write only inside their `FIXTURE_DIR` and a temporary, name-pinned pg container.
- The sandbox runs with `network=none`, is labeled `io.redrill.dev=1`, and is removed on exit.
- The fixture passphrase is a fixture-only secret (deterministic, guards synthetic data).

## Using it against a real repo

The same `drill.sh` works against a real Nextcloud AIO repo: run it with a read-only key,
outside the AIO backup window, let path discovery find `config.php` and the DB dump, and
override the asserts for custom data:

```sh
BORG_REPO="ssh://backup@nas.lan/./borg/nextcloud-aio" \
BORG_PASSPHRASE_FILE=/work/secrets/borg-pass \
ASSERT_SQL_1="select count(*) from oc_users" \
ASSERT_SQL_2="select * from oc_filecache limit 1" \
dev/shell.sh dev/drill.sh
```

(SSH key/known_hosts need mounting into the dev env at that point — extend `shell.sh` then,
not before.) Results from real data may contain personal information: they stay in the data
volume; don't commit or share them. Note the verified in-archive paths so you can pin them in
the real config.

## Troubleshooting

- **Discovery picked the wrong file** → inspect `/work/scratch/out/files.txt`, rerun with
  `CONFIG_PATH=…` / `DB_DUMP_PATH=…`.
- **Version trap** (dump from a newer pg major) → the drill warns; rerun with `PG_IMAGE=postgres:17`.
- **Wrong assert DB** → `ASSERT_DB=…`; list databases with
  `KEEP=1`, then `docker exec -it redrill-dev-pg psql -U postgres -c '\l'`.
- **Stale image after editing the Dockerfile** → `docker rmi redrill-dev`, it rebuilds.
- Reruns are always safe: fixtures rebuild from scratch, the sandbox is recreated, scratch is
  wiped per run; source access stays read-only either way.
