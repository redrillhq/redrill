# Configuration reference

Redrill reads a single YAML file. The path is resolved in this order:

1. the `-c <file>` flag,
2. the `$REDRILL_CONFIG` environment variable,
3. the built-in default `/etc/redrill/config.yaml`.

Parsing is **strict** — any unknown key, anywhere, is an error. Run
`redrill validate -c <file>` to check a config; it exits `3` and prints every
problem with a path-qualified message (e.g.
`drills[0].levels.l1.file_min_bytes: not valid for borg source`).

**Secrets are never inline.** Every secret-bearing field exists only as a
`*_file` (path to a file) or `*_env` (name of an environment variable)
reference.

---

## Full configuration tree

Every option, annotated. This is a *union* for reference — in a real config the
keys are constrained by `type` (sources) and by the source type (L1, L3), and
using a key that doesn't apply is a validation error.

```yaml
version: 1                       # int, required — must be 1

data_dir: /var/lib/redrill       # string, required — SQLite DB + run artifacts (created 0700)

scratch:
  dir: /var/lib/redrill/scratch  # string, required — where restores are written
  max_bytes: 50GiB               # size, optional — hard per-run cap; unset = free-disk check only

concurrency: 1                   # int, default 1 — global single-flight; must be >= 1
bandwidth_limit: 40MiB           # size, optional — best-effort, passed to engine flags where supported

nice:
  cpu: 10                        # int, optional — nice level for engine subprocesses
  io_class: idle                 # string, optional — idle | best-effort | none

server:                          # optional — omit the whole block to run headless (no HTTP)
  listen: ":8090"                # string — host:port; enables the API/UI/metrics
  basic_auth_file: /etc/redrill/htpasswd  # string — bcrypt htpasswd path (browser logins)
  basic_auth_env: REDRILL_BASIC_AUTH      # string — env var holding "user:password" lines
  api_keys_env: REDRILL_API_KEYS          # string — env var holding bearer API keys
  auth_scope: api                # string, default api — api | all
  allow_no_auth: false           # bool, default false — opt in to serving with no auth

notify:                          # optional
  urls:                          # list of shoutrrr URLs (ntfy/telegram/discord/email/webhook/…)
    - "ntfy://ntfy.example.com/redrill"
  events: [fail, error, recover, stale, weekly_digest]  # any subset of these five
  healthchecks_url: ""           # string — http(s) dead-man ping per scheduler cycle

sources:                         # list — keys depend on `type`
  - name: nextcloud-borg         # string, required, unique
    type: borg                   # string, required — borg | dumpdir | restic
    repo: "ssh://backup@nas/./borg/nc"   # string, required (borg/restic)
    binary: borg                 # string, optional — path/version override
    passphrase_file: /etc/redrill/secrets/borg-pass  # string — OR passphrase_env (encrypted repos)
    passphrase_env: BORG_PASSPHRASE                  # string — OR passphrase_file
    ssh_key_file: /etc/redrill/secrets/borg-ro-key   # string, optional — read-only key (BORG_RSH)

  - name: app-dumps
    type: dumpdir
    path: /backups/pg            # string, required — directory of dump files
    pattern: "*.sql.gz"          # string, required — glob
    pick: newest                 # string, default newest — newest | all-matching-window

  - name: photos-restic
    type: restic
    repo: "s3:s3.example.com/bucket/photos"  # string, required
    binary: restic               # string, optional
    password_file: /etc/redrill/secrets/restic-pass  # string — OR password_env (one required)
    password_env: RESTIC_PASSWORD                    # string — OR password_file
    env_file: /etc/redrill/secrets/b2.env            # string, optional — dotenv: backend creds

drills:                          # list
  - name: nextcloud-files        # string, required, unique
    source: nextcloud-borg       # string, required — must name a source above
    schedule: "Sun 04:10"        # string, required — shorthand or cron (UTC)
    jitter: 20m                  # duration, optional — random delay [0, jitter)
    max_proof_age: 10d           # duration, optional — proof SLA; older ⇒ stale alert
    timeout: 2h                  # duration, optional — per-run timeout
    retention:                   # optional — unset keeps every run
      max_age: 90d               # duration, optional — drop runs older than this
      max_count: 50              # int, optional, >= 0 — keep at most this many
    levels:                      # mapping — at least one of l1/l2/l3 required
      l1:                        # integrity
        # borg / restic:
        native_check: true       # bool — run the engine's own check
        snapshot_max_age: 36h    # duration — fail if newest snapshot is older
        size_anomaly_pct: 40     # int 0..100 — advisory warn if latest is >pct% below trailing avg
        # dumpdir:
        file_min_bytes: 1MiB     # size — fail if the picked dump is smaller
        compression_test: true   # bool — gzip -t / zstd -t by extension
        max_age: 36h             # duration — fail if the dump's mtime is older
      l2:                        # restorability
        restore:
          scope: sample          # string, default sample — sample | full
          sample: { files: 200, newest: 50 }  # ints — N random + M newest
          include_paths: ["config/", "data/"] # list, optional
        checks:                  # list — see the check catalog
          - path_exists: "config/config.php"
          - newest_file_max_age: 8d
          - file_count_tolerance_pct: 15
      l3:                        # usability
        extract_path: "db/dump.sql"  # string — required for borg/restic; the dump inside the snapshot
        sandbox:
          image: postgres:16     # string, required — pin to your production major
          env: { POSTGRES_PASSWORD: drill }  # map, optional
          network: none          # string, default none — only none in v1
          memory: 1GiB           # size, optional
          timeout: 20m           # duration, optional
        load: auto               # string, default auto — auto | pg_restore | psql
        checks:                  # list — at least one required
          - sql: { query: "select count(*) from users", expect: "> 0" }
          - sql_no_error: "select * from orders limit 1"
```

---

## Top level

| Key | Type | Default | Notes |
|---|---|---|---|
| `version` | int | — *(required)* | Must be `1`. |
| `data_dir` | string | — *(required)* | Holds `redrill.db` and run artifacts (redacted logs, reports). Created `0700`. |
| `scratch` | mapping | — *(required)* | Restore workspace — see below. |
| `concurrency` | int | `1` | Global single-flight cap; overlapping fires are dropped, not queued. Must be ≥ 1. |
| `bandwidth_limit` | [size](#size) | unset | Best-effort; mapped to engine-native rate flags where they exist. |
| `nice` | mapping | — | CPU/IO niceness for engine subprocesses — see below. |
| `server` | mapping | — | Omit to run headless (no HTTP). See below. |
| `notify` | mapping | — | Alerting — see below. |
| `sources` | list | — | Where backups live and how to read them — see [Sources](#sources). |
| `drills` | list | — | Scheduled audits — see [Drills](#drills). |

### `scratch`

| Key | Type | Default | Notes |
|---|---|---|---|
| `dir` | string | — *(required)* | Restores are written here; a per-run subdir is cleaned up afterward. |
| `max_bytes` | [size](#size) | unset | Hard cap on a run's predicted restore size; preflight refuses (as `error`) before restoring. **Unset = no cap** (only free-disk is checked). |

### `nice`

| Key | Type | Default | Notes |
|---|---|---|---|
| `cpu` | int | unset | Applied as `nice -n` to spawned engines. |
| `io_class` | string | unset | `idle` \| `best-effort` \| `none` (applied as `ionice -c`). |

### `server`

Omit the block to run headless. When `listen` is set you **must** configure an
auth mechanism *or* explicitly set `allow_no_auth: true`, otherwise validation
fails (secure by default).

| Key | Type | Default | Notes |
|---|---|---|---|
| `listen` | string | unset | `host:port` (e.g. `":8090"`). Enables the REST API, web UI, and `/metrics`. |
| `basic_auth_file` | string | unset | Path to a bcrypt htpasswd file (browser logins). |
| `basic_auth_env` | string | unset | Name of an env var holding `user:password` lines. |
| `api_keys_env` | string | unset | Name of an env var holding bearer API keys. |
| `auth_scope` | string | `api` | `api` gates `/api/*`; `all` also gates the UI and `/metrics` (`/healthz` stays open). `all` requires some auth configured. |
| `allow_no_auth` | bool | `false` | Set `true` to serve with no auth (private host / behind an authenticating proxy). |

### `notify`

| Key | Type | Default | Notes |
|---|---|---|---|
| `urls` | list of string | unset | [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) URLs: ntfy, Telegram, Discord, email, webhooks… |
| `events` | list of string | unset | Any subset of `fail`, `error`, `recover`, `stale`, `weekly_digest`. |
| `healthchecks_url` | string | unset | Must be an `http(s)` URL; pinged once per scheduler cycle as a dead-man heartbeat. |

> Notifications are emitted by the **daemon** (`redrill serve`). A manual
> `redrill run` is notification-free — it reports via exit code.

---

## Sources

A source is one backup repository or dump directory. Every source has:

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — *(required)* | Unique across sources; drills reference it. |
| `type` | string | — *(required)* | `borg` \| `dumpdir` \| `restic`. |

The remaining keys depend on `type`. **A key that doesn't belong to the chosen
type is a validation error** (e.g. `path` on a `borg` source).

### `type: borg`

| Key | Type | Default | Notes |
|---|---|---|---|
| `repo` | string | — *(required)* | Local path or `ssh://user@host/./path`. |
| `binary` | string | unset | Override the `borg` executable (path / version pin). |
| `passphrase_file` | string | unset | File holding the repo passphrase (encrypted repos). |
| `passphrase_env` | string | unset | Env var holding the passphrase. Use one of file/env. |
| `ssh_key_file` | string | unset | Read-only SSH key for `ssh://` repos (becomes `BORG_RSH`). |

### `type: dumpdir`

| Key | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — *(required)* | Directory of dump files. |
| `pattern` | string | — *(required)* | Glob, e.g. `"*.sql.gz"`. |
| `pick` | string | `newest` | `newest` (by mtime) \| `all-matching-window`. |

### `type: restic`

| Key | Type | Default | Notes |
|---|---|---|---|
| `repo` | string | — *(required)* | e.g. `s3:…`, `sftp:…`, local path. |
| `binary` | string | unset | Override the `restic` executable. |
| `password_file` | string | *(one required)* | Repo password file… |
| `password_env` | string | *(one required)* | …or env var. One of file/env is required. |
| `env_file` | string | unset | dotenv file of backend credentials (e.g. S3 keys). |

---

## Drills

A drill is a scheduled audit of one source.

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — *(required)* | Unique across drills. |
| `source` | string | — *(required)* | Must match a source `name`. |
| `schedule` | string | — *(required)* | Shorthand or cron — see [Schedule](#the-schedule-string). |
| `jitter` | [duration](#duration) | unset | Random delay added to each fire, in `[0, jitter)`. |
| `max_proof_age` | [duration](#duration) | unset | The proof SLA. If no proof is newer than this the drill is `stale` — computed from timestamps, so it fires even after daemon downtime. |
| `timeout` | [duration](#duration) | unset | Per-run timeout. |
| `retention` | mapping | unset *(keep all)* | Prune run history — see below. |
| `levels` | mapping | — *(≥1 required)* | One or more of `l1`/`l2`/`l3` — see [Levels](#levels). |

### `retention`

The proof timeline (`last_proven_at`) is kept forever regardless of these.

| Key | Type | Default | Notes |
|---|---|---|---|
| `max_age` | [duration](#duration) | unset | Drop runs older than this. |
| `max_count` | int | unset | Keep at most this many runs (≥ 0). |

---

## Levels

A drill runs its configured levels in order **L1 → L2 → L3**. A failing or
erroring level short-circuits the higher levels to `skipped`.

### L1 — integrity

L1 keys are **source-type specific**; mixing them (e.g. `file_min_bytes` on a
borg source) is a validation error.

**borg / restic:**

| Key | Type | Default | Notes |
|---|---|---|---|
| `native_check` | bool | unset | Run the engine's own check (`borg check` / `restic check`). |
| `snapshot_max_age` | [duration](#duration) | unset | `fail` if the newest snapshot is older than this. |
| `size_anomaly_pct` | int | unset | Advisory (always passes, may warn): flag a latest snapshot more than this percent below the trailing average. `0..100`. |

**dumpdir:**

| Key | Type | Default | Notes |
|---|---|---|---|
| `file_min_bytes` | [size](#size) | unset | `fail` if the picked dump is smaller. |
| `compression_test` | bool | unset | `gzip -t` / `zstd -t` integrity, chosen by file extension. |
| `max_age` | [duration](#duration) | unset | `fail` if the dump file's mtime is older than this. |

### L2 — restorability

Restore a sample (or the full set) into scratch, then assert against it.

| Key | Type | Default | Notes |
|---|---|---|---|
| `restore.scope` | string | `sample` | `sample` \| `full`. |
| `restore.sample.files` | int | unset | Random files to restore (when `scope: sample`). |
| `restore.sample.newest` | int | unset | Plus this many newest files. |
| `restore.include_paths` | list of string | unset | Restrict/seed the restore to these subpaths. |
| `checks` | list | unset | L2 checks — see the [catalog](#check-catalog). |

### L3 — usability

Boot a sandbox from the restored data and assert against it. Requires a
container runtime; without one, L3 reports `skipped` (never a silent pass).

| Key | Type | Default | Notes |
|---|---|---|---|
| `extract_path` | string | *(required for borg/restic)* | Path of the dump inside the snapshot to extract and load. Not used by `dumpdir` (already a single file). |
| `sandbox.image` | string | — *(required)* | Container image. Pin to your production major (image major ≥ dump major, or it's a version-trap `fail`). |
| `sandbox.env` | map | unset | Environment for the sandbox (e.g. `POSTGRES_PASSWORD`). |
| `sandbox.network` | string | `none` | Only `none` is supported in v1. |
| `sandbox.memory` | [size](#size) | unset | Memory limit. |
| `sandbox.timeout` | [duration](#duration) | unset | Sandbox boot/operation timeout. |
| `load` | string | `auto` | `auto` (detect `pg_restore` vs `psql` by dump format) \| `pg_restore` \| `psql`. |
| `checks` | list | — *(≥1 required)* | L3 checks — an L3 with no checks proves nothing and is rejected. |

---

## Check catalog

Each check is a single-key mapping, e.g. `- path_exists: "config/config.php"`.

| Check | Level | Value | Verdict |
|---|---|---|---|
| `path_exists` | L2 | string path | `fail` if the path is absent in the restore. |
| `path_absent` | L2 | string path | `fail` if the path **is** present. |
| `canary_file` | L2 *(weak)* | string path | Like `path_exists` but comfort-only — weak-labeled, never counts as sole proof. |
| `hash_match` | L2 | bool | Verify restored bytes against an engine manifest. borg/restic expose none, so this relies on the engine's extract-time chunk verification (passes, clearly labeled). |
| `newest_file_max_age` | L2 | [duration](#duration) | `fail` if the newest restored file is older than this. |
| `min_total_bytes` | L2 | [size](#size) | `fail` if total restored bytes is below this. |
| `file_count_tolerance_pct` | L2 | int (percent) | `fail` if the restored file count deviates more than this percent from the last proven run. |
| `sql` | L3 | `{query, expect}` | Run `query`, compare the scalar to `expect`. Mismatch ⇒ `fail`; a query or coercion error ⇒ `error`. See [expect](#the-expect-predicate). |
| `sql_no_error` | L3 | string query | `fail` if the query errors (the restored data is bad). |
| `exec` | L2 / L3 | string | ⚠️ **Accepted by validation but not implemented in v1** — currently a no-op that produces no evidence. Don't rely on it yet. |

### The `sql` check

```yaml
- sql:
    query: "select count(*) from users"   # required
    expect: "> 100"                        # required — see below
```

### The `expect` predicate

| Predicate | Meaning |
|---|---|
| `> N`, `>= N`, `== N`, `!= N` | Numeric comparison of the scalar. |
| `between A B` | Inclusive range `A ≤ value ≤ B`. |
| `age < DUR`, `age > DUR` | Parse the scalar as a timestamp; compare `now − value`. `DUR` uses [duration](#duration) syntax. |
| `matches REGEX` | Go-regexp match against the scalar. |
| `nonempty` | True if the scalar is non-whitespace. |

If the scalar can't be coerced to the needed type (not a number for `> N`, not
a timestamp for `age <`), the check is **`error`**, not `fail`.

---

## The `schedule` string

- **Shorthand** — `HH:MM` (daily) or `Ddd HH:MM` (weekday + time). Weekdays are
  the 3-letter `sun mon tue wed thu fri sat` (case-insensitive); hour `0–23`,
  minute `00–59`. Examples: `04:10`, `Sun 04:10`.
- **Cron** — any standard 5-field expression `min hour dom mon dow`, e.g.
  `10 4 * * 0`.
- **Descriptors** — `@daily`, `@weekly`, `@hourly`, etc.

All schedules are interpreted as **UTC** unless the expression carries its own
`CRON_TZ=…` / `TZ=…` prefix.

---

## Value types

### Duration

Go duration syntax (`90s`, `30m`, `36h`, `1h30m`) plus a day suffix where `8d` =
8 × 24h. Must be non-negative.

### Size

One of:

- **IEC** binary units — `KiB MiB GiB TiB PiB` (×1024ⁿ), e.g. `1MiB`, `50GiB`;
- **SI** decimal units — `KB MB GB TB PB` (×1000ⁿ), e.g. `40MB`;
- a **bare integer** of bytes, e.g. `1048576`; or `B` (×1).

Must be non-negative.
