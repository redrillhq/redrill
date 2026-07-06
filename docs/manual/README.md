# Redrill manual

Documentation for operating Redrill — scheduled restore drills that prove the
backups are restorable by actually restoring them.

- [Getting started](getting-started.md) — from zero to a proven backup in
  about ten minutes: the sabotage demo, the first real drill, the daemon.
- [Configuration reference](configuration.md) — every option in the YAML config
  file, with types, defaults, allowed values, and validation rules.
- [Credentials](credentials.md) — least-privilege repository access per
  engine, so the auditor cannot harm the backups even in the worst case.
- [Security and threat model](security.md) — what redrill is trusted with,
  what holds by construction, the docker-socket tradeoff, and what it
  deliberately does not protect against.

<!--
Planned pages (not written yet):
  - install.md          Docker / prebuilt binary / from source / systemd
  - concepts.md         sources · drills · checks · levels; fail vs error vs stale; the proof SLA
  - scheduling.md       the daemon (serve) vs one-shot (run) vs external cron
  - cli.md              command + flag + exit-code reference
  - operations.md       metrics/API, healthchecks, troubleshooting
-->
