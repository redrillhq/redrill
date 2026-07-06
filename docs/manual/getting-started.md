# Getting started

From zero to a proven backup in about ten minutes. Redrill is an auditor: it
restores backups made by other tools (Borg, restic, SQL dump directories) into
sandboxes and proves they are restorable. It never writes to a repository.

## 60 seconds: watch it catch a dead backup

The demo builds a throwaway backup, proves it restorable, then breaks it the
way real backups die — file present, timestamp fresh, contents dead — and
shows the drill catching it. Nothing outside a temp directory is touched.

With the binary (build: `go build -o bin/redrill ./cmd/redrill`, Go ≥ 1.26):

```bash
redrill demo sabotage              # boots a disposable postgres when Docker is present
redrill demo sabotage --no-sandbox # file-integrity checks only, no Docker needed
```

From the compose deployment (see below):

```bash
docker compose run --rm redrill demo sabotage
```

The healthy drill passes, the sabotaged one fails — and the freshness check
*still passes* on the dead backup. That gap between "the cron job ran" and
"the backup restores" is the product.

## Ten minutes: the first real proof

### 1. Scaffold a config

`redrill init` asks a handful of questions (where the backups live, which
engine made them, how deep to verify) and emits a config that is guaranteed to
pass `validate`:

```bash
redrill init                # interactive on a terminal
redrill init --target local --type dumpdir --path /backups/pg -o config.yaml
```

Or start from the [annotated example](../../deploy/compose/config.example.yaml)
and the [configuration reference](configuration.md). A minimal config auditing
a directory of `pg_dump` files:

```yaml
version: 1
data_dir: /var/lib/redrill
scratch: { dir: /var/lib/redrill/scratch, max_bytes: 40GiB }

sources:
  - name: pg-dumps
    type: dumpdir
    path: /backups/pg
    pattern: "*.sql.gz"

drills:
  - name: app-db
    source: pg-dumps
    schedule: "Sun 05:00"   # omit for a manual-only drill
    max_proof_age: 10d      # the Proof SLA: stale alert past this age
    levels:
      l1: { file_min_bytes: 1MiB, compression_test: true, max_age: 36h }
      l3:
        sandbox: { image: postgres:16 }
        checks:
          - sql: { query: "select count(*) from users", expect: "> 0" }
```

L1 checks the backup's bytes, L2 restores a sample into scratch, L3 boots a
disposable database from the restored dump and runs SQL assertions — the
[configuration reference](configuration.md) has the full check catalog.

### 2. Validate, preflight, drill

```bash
redrill validate -c config.yaml   # strict schema check; exit 3 on any problem
redrill doctor   -c config.yaml   # engines present? runtime reachable? repo readable?
redrill run app-db -c config.yaml # the drill, streaming each check's evidence
```

A passing run prints the evidence and records a proof:

```
[l1] PASS — pass: 3 checks (3 pass, 0 fail, 0 error)
[l3] PASS — pass: 2 checks (2 pass, 0 fail, 0 error)
redrill: app-db → PASS (reached l3, run 1)
```

Exit codes are stable and monitoring-friendly: `0` proven, `1` the backup is
bad, `2` redrill could not check (never a silent pass), `3` config error.

### 3. Read the proof picture

```bash
redrill status -c config.yaml     # per drill: last run, proof age, next run, SLA state
redrill history app-db -c config.yaml
redrill report -c config.yaml --format html --out report.html
```

## Keep it proven: the daemon

One-shot runs prove a backup today; the daemon proves it *stays* restorable.
`redrill serve` runs the schedules, sends notifications (`fail` ≠ `error`,
`stale` when a proof outlives `max_proof_age` — even if the daemon was down),
and serves the dashboard.

The reference deployment is [deploy/compose](../../deploy/compose/):

```bash
git clone https://github.com/redrillhq/redrill
cd redrill/deploy/compose
$EDITOR config.example.yaml   # point the source at the backups
$EDITOR compose.yaml          # mount the backup dir read-only; keep the docker socket for L3
docker compose up -d --build
```

The dashboard is at `http://127.0.0.1:8090/` — a card per dataset with the
headline **"Last proven: N days ago"** and the proof chain. Before exposing it
beyond localhost, set up auth and TLS: see [security](security.md) and the
[reverse-proxy recipes](../../deploy/README.md). A systemd unit for host
installs lives in [deploy/systemd](../../deploy/systemd/).

## Next

- [Credentials](credentials.md) — least-privilege access per engine, so the
  auditor cannot harm the backups even in the worst case.
- [Security](security.md) — the threat model: what redrill is trusted with
  and what holds by construction.
- [Configuration reference](configuration.md) — every key, default, and rule.
