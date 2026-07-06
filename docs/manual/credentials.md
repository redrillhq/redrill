# Credentials: least-privilege access per engine

Redrill is read-only by construction — the drivers have no write, prune, or
delete code paths, and every engine invocation uses an allow-listed
subcommand. Defense in depth says don't take the code's word for it: hand
redrill credentials that *could not* damage the backups even if the host
running it were fully compromised. This page is the per-engine recipe for
that, honest about where each engine's locking makes "strictly read-only"
impossible.

Two rules apply to every engine:

- **Secrets are references, never values.** The config schema only accepts
  `*_file` / `*_env` fields; there is no inline form. Secret files should be
  `0600`, owned by the user running redrill. Captured engine output passes
  through a redaction boundary before becoming evidence or logs.
- **No credentials in repository URLs.** A `rest:https://user:pass@host/` URL
  works, but a URL is not a secret store — prefer the dedicated credential
  fields. (Redrill keeps repo URLs off process argv and registers URL-embedded
  credentials with the redactor as a backstop, but the better setup is not to
  embed them at all.)

## Borg (SSH)

Borg writes lock files inside the repository even for read operations, so a
hard read-only filesystem or SSH account breaks `borg list`. The practical
least privilege is a **dedicated SSH key restricted to an append-only
`borg serve`** on the repository host — reads work, and nothing invoked with
that key can delete or overwrite existing archives:

```
# ~backup/.ssh/authorized_keys on the repository host — one line:
command="borg serve --append-only --restrict-to-repository /srv/borg/app",restrict ssh-ed25519 AAAA... redrill-audit
```

- `--append-only` — the client can add data (locks, at worst garbage) but
  cannot delete or change existing archives; `prune`/`delete` become no-ops
  recorded in the transaction log.
- `--restrict-to-repository` — the key reaches exactly one repository.
- `restrict` — disables forwarding, PTY, and X11 for the key.

Config:

```yaml
sources:
  - name: app-borg
    type: borg
    repo: "ssh://backup@nas.lan/srv/borg/app"
    passphrase_file: /etc/redrill/secrets/borg-pass
    ssh_key_file: /etc/redrill/secrets/borg-audit-key
```

The passphrase travels as `BORG_PASSPHRASE`, the key via `BORG_RSH` — never
on the command line. For a local repo path, filesystem permissions do the same
job: a user with read access and write access limited to the repo's lock
files is not achievable with plain modes, so prefer the SSH form even on the
same host, or accept that the redrill user can write to the repo directory
and rely on borg's own append-only repo flag (`borg config repo append_only 1`).

## restic (S3, B2, REST)

restic reads accept `--no-lock`, and redrill passes it on every read — those
work against strictly read-only credentials. The exception is `restic check`
(the L1 `native_check`): it takes a real repository lock by engine design,
which means **writing and deleting under the `locks/` prefix**. Two honest
options:

1. **Read-only everywhere, plus a `locks/` carve-out** — the standard restic
   pattern. S3 policy sketch:

   ```json
   {
     "Statement": [
       { "Effect": "Allow",
         "Action": ["s3:ListBucket"],
         "Resource": "arn:aws:s3:::backups" },
       { "Effect": "Allow",
         "Action": ["s3:GetObject"],
         "Resource": "arn:aws:s3:::backups/*" },
       { "Effect": "Allow",
         "Action": ["s3:PutObject", "s3:DeleteObject"],
         "Resource": "arn:aws:s3:::backups/locks/*" }
     ]
   }
   ```

   Everything but lock files is immutable to redrill. For Backblaze B2, the
   equivalent is a read-only application key plus a second key scoped to the
   `locks/` prefix — or option 2.

2. **Strictly read-only credentials, no native check** — drop `native_check`
   from L1 and let L2/L3 carry the proof (restoring and booting the data
   verifies far more than `restic check` does anyway). Every redrill read
   passes `--no-lock`, so nothing else needs write access.

Config:

```yaml
sources:
  - name: app-restic
    type: restic
    repo: "s3:s3.amazonaws.com/backups/app"
    password_file: /etc/redrill/secrets/restic-pass
    env_file: /etc/redrill/secrets/restic-s3.env   # AWS_ACCESS_KEY_ID=..., AWS_SECRET_ACCESS_KEY=...
```

The repository and password travel as `RESTIC_REPOSITORY` / `RESTIC_PASSWORD`
in the environment, backend credentials from the dotenv file — never on argv.

## Dump directories

A dumpdir source is plain files; no engine, no locks. Give redrill read-only
access and be done:

- **Compose:** mount the directory `:ro` —
  `- /backups/pg:/backups/pg:ro`. The container cannot write it regardless of
  what runs inside.
- **Host install:** run redrill as a user with `r-x` on the directory, or use
  a read-only bind mount.

## Verifying the setup

`redrill doctor` checks every source's reachability with the configured
credentials and reports what is missing or unreadable — run it after any
credential change. The read-only claim itself is enforced in code and CI: the
drivers construct only allow-listed read subcommands, and tests assert no
write invocation can ever be built.
