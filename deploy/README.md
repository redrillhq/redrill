# Deploying redrill

Two supported shapes. Both run the same daemon (`redrill serve`).

## Docker / compose (recommended)

The image bundles the engine tools redrill shells out to (`borg`, `restic`) and
the postgres client used inside L3 sandboxes — the host needs only a container
runtime.

```sh
cd deploy/compose
# edit config.example.yaml and the volume mounts in compose.yaml
docker compose up -d
docker compose exec redrill redrill doctor   # preflight: engines, runtime, scratch, repos
```

- **Expose backups read-only.** Mount the repositories/dump directories you audit
  with `:ro`. redrill never writes to them by construction, and the read-only
  mount makes that belt-and-suspenders.
- **Secrets are files.** Mount borg passphrases / read-only SSH keys and reference
  them via `*_file` in the config. There is no inline secret form.
- **L3 needs the docker socket.** Database sandboxes are spawned on the host
  runtime via the mounted `/var/run/docker.sock` — the documented trust tradeoff.
  Omit the mount for L1/L2-only setups; L3 then reports
  `skipped (no sandbox runtime)`, never a silent pass. A rootless-podman socket
  works too.

## systemd (host binary)

For running the static binary directly. Unlike the container, the **host must
provide the engine tools** the configured sources need (`borg`/`restic`, and
`pg_isready`/`pg_restore`/`psql` are inside the sandbox image, not the host).
Verify with `redrill doctor`.

```sh
install -m0755 redrill /usr/local/bin/redrill
useradd --system --home /var/lib/redrill redrill
install -d -o redrill -g redrill /etc/redrill /etc/redrill/secrets
# write /etc/redrill/config.yaml (chmod 0600 the secrets)
cp deploy/systemd/redrill.service /etc/systemd/system/
systemctl enable --now redrill
```

For L3, add `redrill` to the `docker` group (uncomment `SupplementaryGroups=docker`
in the unit) or use rootless podman.

## HTTP API & metrics

Set `server.listen` (e.g. `":8090"`) to expose the read-only REST API and
Prometheus metrics; omit it to run headless. The API is read-only by design —
the only mutating endpoint is the run trigger.

```
GET  /healthz                          # liveness
GET  /metrics                          # Prometheus (redrill_proof_sla_ok, …)
GET  /api/v1/drills                    # per-drill status: proof age, next run, SLA
GET  /api/v1/drills/{name}/runs        # run history (?n=20)
GET  /api/v1/runs/{id}                 # steps, per-check evidence, artifacts
POST /api/v1/drills/{name}/run         # "Run now" (rate-limited; 409 if busy)
```

- **Auth.** Optional `server.basic_auth_file` (htpasswd, **bcrypt only** — `htpasswd -B`)
  gates `/api/*`; `/healthz` and `/metrics` stay open for probes/scrapes. Basic auth
  is a convenience — front the daemon with a reverse proxy for TLS. The compose example
  binds the port to `127.0.0.1` for that reason.
- **Dead-man ping.** Set `notify.healthchecks_url` to have redrill ping a monitor
  (e.g. healthchecks.io) each scheduler cycle, so you're alerted if the daemon itself
  goes down. Set the check's expected period to your drill cadence.

## After deploying

```sh
redrill doctor          # environment preflight
redrill validate        # strict config check (also checks schedules + notify URLs)
redrill status          # per-drill proof age and SLA state
```
