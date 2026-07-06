# Security and threat model

Redrill's job is to be trusted with read access to every backup a household
or lab has, and to tell the truth about them. That position deserves a plain
statement of what the tool holds, what holds by construction, and what it
deliberately does not protect against.

## What redrill is trusted with

- **Read credentials for every configured repository** — borg passphrases and
  SSH keys, restic passwords and backend keys, filesystem access to dump
  directories. [Credentials](credentials.md) shows how to make each of these
  incapable of writing, so the blast radius of a compromised redrill host is
  "the attacker can read the backups", never "the attacker can destroy them".
- **Optionally, the container runtime socket** for L3 drills — see the
  tradeoff below.
- **The evidence it records**: check results, redacted engine output, run
  history in a local SQLite file. No telemetry; nothing leaves the host except
  the notifications explicitly configured.

## What holds by construction

**Repositories are read-only to redrill.** The drivers have no write, prune,
or delete code paths; every engine invocation is built from an allow-listed
read subcommand (`list`/`info`/`check`/`extract` for borg;
`snapshots`/`check`/`ls`/`restore`/`stats`, all with `--no-lock` where the
engine permits, for restic). Tests exercise every driver method and assert no
other invocation can be constructed; the sabotage CI gate proves each release
still *catches* broken backups rather than blessing them.

**Secrets stay out of the database, logs, and evidence.** The config schema
only accepts `*_file`/`*_env` secret references — an inline secret is a
validation error. Secrets travel to engines via environment variables, never
argv (nothing to read in `ps`). Every captured byte of engine output passes
through a redaction boundary that scrubs registered secret values and
`*_PASSWORD`-style environment values before it becomes evidence, a log line,
or a notification.

**`fail` is never conflated with `error`.** A failed check means the backup
is bad; an error means the auditor could not check. The distinction survives
into exit codes, alerts, metrics, and the UI — so an infrastructure problem
can never quietly count as a passing audit, and a missing container runtime
degrades L3 to `skipped`, never to a pass.

**Sandboxes are disposable and isolated.** L3 databases boot in containers
with `network=none`, a memory limit, and a redrill-owned label; teardown is
guaranteed twice (per-run cleanup plus a startup janitor that reaps orphans
from crashed runs). Restored data is loaded and queried entirely inside that
container.

## The docker-socket tradeoff

L3 needs a container runtime, and mounting `/var/run/docker.sock` into the
redrill container is root-equivalent on the host: anyone who fully controls
redrill controls Docker. The compose example mounts it deliberately and says
so. Options, in decreasing order of caution:

1. Run redrill on the host (systemd unit) so no socket crosses a container
   boundary.
2. Point it at a rootless Docker or podman socket.
3. Mount the socket into the container (the compose default) and treat the
   redrill container as host-privileged.
4. Drop the socket entirely — L1/L2 still run; L3 reports `skipped (no
   sandbox runtime)` and the board shows exactly what remains unproven.

## The HTTP surface

`redrill serve` only listens when `server.listen` is set, and then **auth is
required by default** — serving open needs an explicit
`server.allow_no_auth: true`, so an unauthenticated API is never an accident.
Basic auth accepts bcrypt entries only (weaker htpasswd schemes are rejected
at load); API keys are bearer tokens for programmatic clients. The single
mutating endpoint (`POST .../run`) is rate-limited, auth-gated, and shares the
scheduler's single-flight gate. There is no artifact-download route: redacted
logs stay on the host. `/metrics` and the UI disclose drill names and proof
ages — on an exposed host, set `server.auth_scope: all` or allow-list them at
the proxy. Basic auth over plain HTTP is credential disclosure; front any
exposed instance with TLS (see the [reverse-proxy recipes](../../deploy/README.md)).

## Restored data is untrusted input

A drill processes whatever the backup contains. L2 extraction happens inside
a per-run scratch directory with a size quota and preflight; L3 loads the
restored dump inside the network-less sandbox, and SQL assertions execute in
that container, not on the host. The `exec` check runs operator-authored
commands (on the host at L2, in the sandbox at L3) — the config is the trust
boundary, so an `exec` script deserves the same review care as the config
file that names it. The residual risk is the engines' own
extraction code — restoring is inherently parsing untrusted archives with
borg/restic/psql. Keep engines within the tested version bands (`redrill
doctor` warns outside them) and patched.

## What redrill does not protect against

Honesty about the negative space:

- **It is not a backup tool and cannot repair anything.** It proves or
  disproves restorability; fixing a bad backup is the backup tool's job.
- **A proof is point-in-time.** "Last proven 3 days ago" attests that drill,
  that snapshot, that day — the Proof SLA (`max_proof_age`) exists precisely
  because proofs age.
- **It cannot attest what it cannot read.** A dataset outside the config, or
  a level degraded to `skipped`, is unproven and shown as such — absence of
  proof, not evidence of health.
- **It does not scan for malware or ransomware.** `size_anomaly_pct` flags
  gross size drift as advisory signal; nothing more is claimed.
- **A compromised redrill host holds redrill's credentials.** Least-privilege
  setup ([credentials](credentials.md)) caps that at read access plus, if
  mounted, the docker socket — which is why the socket deserves the paragraph
  above.

## Reporting a vulnerability

Suspected vulnerabilities are best reported privately via GitHub security
advisories on the repository rather than public issues.
