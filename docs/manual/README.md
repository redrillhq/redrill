# Redrill manual

Documentation for operating Redrill — scheduled restore drills that prove your
backups are restorable by actually restoring them.

- [Configuration reference](configuration.md) — every option in the YAML config
  file, with types, defaults, allowed values, and validation rules.

<!--
Planned pages (not written yet — this landing intentionally links only the
config reference for now):
  - getting-started.md  first proof in ~10 minutes (one happy path)
  - install.md          Docker / prebuilt binary / from source / systemd
  - concepts.md         sources · drills · checks · levels; fail vs error vs stale; the proof SLA
  - scheduling.md       the daemon (serve) vs one-shot (run) vs external cron
  - cli.md              command + flag + exit-code reference
  - security.md         read-only credentials, secret handling, the docker-socket tradeoff
  - operations.md       metrics/API, healthchecks, troubleshooting
-->
