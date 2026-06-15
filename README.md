# Redrill

**Your backups run every night. Have you ever actually restored one?**

Redrill is a self-hosted daemon that proves your backups are restorable by actually restoring them. On a schedule it pulls data out of the backups you already make (Borg repos and Postgres dumps, for now), restores them into a throwaway sandbox, and for databases boots a real instance to check the loaded data. For each dataset you get a line like:

> **last proven restore: 3 days ago** ✅

![access: read--only](https://img.shields.io/badge/repo%20access-read--only-brightgreen)
![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8)
![license: AGPL--3.0](https://img.shields.io/badge/license-AGPL--3.0-blue)

## The problem

Every backup tool checks its own integrity, while none of them check whether the data is actually usable. The real-world failures lie elsewhere:

- A `pg_dump` cron writes an empty file after a password rotation.
- A newly introduced exclude pattern drops directories from the archive.
- Expired API tokens result in empty dumps.
- A volume quietly unmounts.
- A version misconfiguration between `pg_dump` and Postgres.
- etc.

## Redrill in essence

Redrill is read-only and never modifies your backups. Each drill is configurable: use the backup tool's own integrity check, or fully restore the data and run your own checks. Runs are kept with history, and failures raise an alert.

## Verification layers

| Layer | What it does | IO |
|------:|--------------|----|
| **L1 — Integrity** | **Borg:** native `borg check`, snapshot freshness, size-anomaly detection.<br>**pg_dump:** minimum file size, `gzip -t`/`zstd -t` compression test, file freshness (mtime). | Low |
| **L2 — Restorability** | **Borg:** restores a sample of files into scratch, asserts `path_exists`, newest-file freshness, file-count tolerance vs. the last good run.<br>**pg_dump:** copies the dump into scratch, but for a single dump L1+L3 do the real work. | Moderate (scales with sample size) |
| **L3 — Usability** | **Borg:** extracts the dump at `extract_path` from the archive, then boots it the same way.<br>**pg_dump:** boots an ephemeral, network-isolated Postgres, loads the dump, runs your `sql` assertions (`select count(*) from users` → `> 0`, `age < 8d`, …). | High (full restore + DB boot) |

Layers always run sequentially, so if L3 is selected in the config, a failing L2 stops the job from executing.

## Drill results

- `fail` — a check returned false. The backup is the problem, and data is at risk.
- `error` — the check couldn't be completed (repo unreachable, scratch full, no container runtime), reported with the reason. Redrill is the problem, not the backup, and never a silent pass.
- `stale` — a dataset hasn't been proven within its `max_proof_age`, for any reason, including the daemon having been down.

## Quickstart

### Installation

L3 boots database sandboxes, so it needs a container runtime (Docker or podman). Without one, L1/L2 still run and L3 reports `skipped` rather than passing.

```bash
git clone https://github.com/alyamovsky/redrill
cd redrill/deploy/compose

# 1. Point the config at your backups and tune the checks.
$EDITOR config.example.yaml

# 2. In compose.yaml, mount your backup dir read-only and (for L3) the docker socket.
$EDITOR compose.yaml

# 3. Go.
docker compose up -d
docker compose logs -f redrill
```

> Prefer not to use Docker? Build the single static binary with `go build ./cmd/redrill` (Go 1.26). You'll need the `borg` binary on the host for Borg sources and a container runtime for L3. Run `redrill doctor` and it tells you exactly what's missing.

### Config example

Auditing a directory of `pg_dump` files:

```yaml
version: 1
data_dir: /var/lib/redrill
scratch: { dir: /var/lib/redrill/scratch, max_bytes: 40GiB }

notify:
  urls: ["ntfy://ntfy.example.com/redrill"]   # any shoutrrr URL: ntfy/telegram/discord/email/webhook
  events: [fail, error, recover, stale]

sources:
  - name: pg-dumps
    type: dumpdir
    path: /backups/pg            # mount this read-only
    pattern: "*.sql.gz"
    pick: newest

drills:
  - name: app-db
    source: pg-dumps
    schedule: "Sun 05:00"        # cron or human shorthand ("Sun 05:00", "04:10")
    max_proof_age: 10d           # stale alert if no proof newer than this
    retention: { max_count: 50 } # keep the newest 50 runs of history
    levels:
      l1: { file_min_bytes: 1MiB, compression_test: true, max_age: 36h }
      l3:
        sandbox: { image: postgres:16, network: none, memory: 1GiB }
        load: auto
        checks:
          - sql: { query: "select count(*) from users", expect: "> 0" }
          - sql: { query: "select max(created_at) from events", expect: "age < 8d" }
```

Auditing a Borg repo:

```yaml
sources:
  - name: nextcloud-borg
    type: borg
    repo: "ssh://backup@nas.lan/./borg/nextcloud-aio"
    passphrase_file: /etc/redrill/secrets/borg-pass
    ssh_key_file: /etc/redrill/secrets/borg-readonly-key

drills:
  - name: nextcloud-files
    source: nextcloud-borg
    schedule: "Sun 04:10"
    max_proof_age: 10d
    levels:
      l1: { native_check: true, snapshot_max_age: 36h, size_anomaly_pct: 40 }
      l2:
        restore: { scope: sample, sample: { files: 200, newest: 50 }, include_paths: ["config/", "data/"] }
        checks:
          - path_exists: "config/config.php"
          - newest_file_max_age: 8d
          - file_count_tolerance_pct: 15
```

## Available CLI commands

```
redrill validate          # strictly check your config (exit 3 on any problem)
redrill doctor            # preflight: engines, container runtime, scratch space, repo reachability
redrill run NAME          # run one drill now and stream the step log  (--level l1|l2|l3)
redrill status            # table: each drill's last run, proof age, next run, SLA state
redrill history NAME      # past runs with verdicts and durations      (-n 20)
redrill serve             # the daemon: scheduler + notifications
redrill version
```

Every command takes `--json`. Exit codes are stable: 0 ok, 1 a drill failed, 2 infra error, 3 bad config. Drop `redrill status` in a terminal and you get the whole picture:

```
DRILL             LAST RUN      PROVEN     NEXT RUN     SLA
app-db            pass 2h ago   2h ago     Sun 05:00    ok
nextcloud-files   fail 1d ago   6d ago     Sun 04:10    STALE

1 of 2 drills proven within SLA
```
(`PROVEN` shows the proof age of the drill's highest configured level.)

## Configuration glossary

- **Sources** — where backups live and how to read them. Today: `borg` and `dumpdir` (a directory of dump files).
- **Drills** — a scheduled audit of one source: `schedule`, `max_proof_age` (the proof SLA), optional `jitter`/`timeout`/`retention`, and one or more `levels`.
- **Checks** — typed assertions producing evidence (expected vs. actual): `path_exists`, `file_count_tolerance_pct`, `newest_file_max_age`, `sql`, `sql_no_error`. The `sql` `expect` grammar covers `> N`, `>= N`, `== N`, `!= N`, `between A B`, `age < 8d` / `age > 8d`, `matches REGEX`, `nonempty`.
- **Notifications** — via [shoutrrr](https://github.com/nicholas-fedor/shoutrrr): ntfy, Telegram, Discord, email, webhooks, and more. Messages lead with the diagnosis and the last-good date, not a stack trace.
- **Retention** — prune each drill's run history by `max_age` and/or `max_count`. The proof timeline (`last_proven_at`) is kept forever.

The full annotated schema lives in [`deploy/compose/config.example.yaml`](deploy/compose/config.example.yaml).

## Safety measures

- Read-only by construction: the drivers have no write, prune, or delete code paths.
- Secrets are referenced by `*_file`/`*_env` only and redacted from stored output.
- L3 sandboxes run with `network=none`, memory limits, and guaranteed cleanup.
- The Docker socket is needed only for L3; drop it to keep L1/L2.

## Trusting the verifier

A verifier you can't trust is worse than none. On every change the test suite runs, including real-engine drills in throwaway containers. Some of those tests feed Redrill deliberately broken backups that are byte-perfect but semantically dead (for example, an empty-but-valid gzip, a dump of the wrong database, or a stale snapshot), and the build fails unless it flags each one.

## Feedback

Redrill is under active development. Bug reports and ideas are welcome through issues, especially which backup tools you'd want supported next.

Built and maintained by [Andrew Alyamovsky](https://github.com/alyamovsky)
