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

From the repo root, `make docker-up` builds the image and starts this stack in one step (`make docker-down` to stop, `make docker-logs` to tail) — handy for a production-like local run. The web UI + API are then at `http://127.0.0.1:8090/`.

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

## Web UI, HTTP API & metrics

Set `server.listen` (e.g. `":8090"`) to expose the web dashboard, the read-only
REST API, and Prometheus metrics; omit it to run headless. Open
`http://<host>:8090/` for the UI (proof board, run detail, history) — it is the
embedded SPA, served by the same daemon, no extra container or process. The API
is read-only by design — the only mutating endpoint is the run trigger (the UI's
"Run now").

```
GET  /                                 # web UI (embedded SPA)
GET  /healthz                          # liveness
GET  /metrics                          # Prometheus (redrill_proof_sla_ok, …)
GET  /api/v1/drills                    # per-drill status: proof age, next run, SLA
GET  /api/v1/drills/{name}/runs        # run history (?n=20)
GET  /api/v1/runs/{id}                 # steps, per-check evidence, artifacts
POST /api/v1/drills/{name}/run         # "Run now" (rate-limited; 409 if busy)
```

- **Auth (required when `listen` is set).** redrill refuses to start an unauthenticated
  HTTP API — configure a credential (see "Exposing redrill" below) or explicitly set
  `server.allow_no_auth: true` to serve open (private host / authenticating proxy). When
  auth is on it gates `/api/*`; `server.auth_scope: all` also gates the UI and `/metrics`
  (`/healthz` always stays open for liveness probes).
- **Dead-man ping.** Set `notify.healthchecks_url` to have redrill ping a monitor
  (e.g. healthchecks.io) each scheduler cycle, so you're alerted if the daemon itself
  goes down. Set the check's expected period to the drill cadence.

## Exposing redrill on a public URL

The compose example binds to `127.0.0.1` on purpose. To reach redrill from outside
localhost, **put it behind a reverse proxy that terminates TLS** — basic auth over
plain HTTP would send credentials in the clear. Drop the published port from
`compose.yaml` so the daemon is only reachable through the proxy.

**Pick one proxy.** [`compose/Caddyfile.example`](compose/Caddyfile.example) (Caddy,
auto-HTTPS — recommended) and the nginx block below are two ways to do the *same*
thing; deploy whichever you already run, not both. Each terminates TLS and restricts
`/metrics`.

**Where credentials live — pick one place, not both:**

- **In redrill.** Browser login via either `server.basic_auth_file` (a bcrypt htpasswd
  file, `htpasswd -B -c ./htpasswd admin`, mounted at the path you set — the compose
  mount is commented out pointing at `/etc/redrill/htpasswd`) or `server.basic_auth_env`
  (an env var with `user:password` lines — easiest in compose). For scripts, Prometheus,
  and the MCP server, `server.api_keys_env` holds bearer tokens, sent as
  `Authorization: Bearer <key>` or `X-API-Key`. `/api/*` accepts a basic credential or a
  key. Multiple users/keys are independent credentials with the same access (not roles).
  Add `server.auth_scope: all` to also cover the UI and `/metrics`.
- **In the proxy** (Caddy `basic_auth` / nginx `auth_basic`): the credentials live in
  the proxy config and redrill stays open behind it.

**Lock down `/metrics`** regardless — it discloses drill names, proof ages, and SLA
state (fine on a private network, not public): restrict it at the proxy (allow-list
below) or set `server.auth_scope: all`.

```nginx
# nginx alternative to compose/Caddyfile.example (add the `listen 443 ssl` / certs):
location /metrics {
    allow 10.0.0.0/8;   # the monitoring network only
    allow 127.0.0.1;
    deny all;
    proxy_pass http://127.0.0.1:8090;
}
location / {
    proxy_pass http://127.0.0.1:8090;
}
```

## After deploying

```sh
redrill doctor          # environment preflight
redrill validate        # strict config check (also checks schedules + notify URLs)
redrill status          # per-drill proof age and SLA state
```
