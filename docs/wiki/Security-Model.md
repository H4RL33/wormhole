# Security Model

This page is an approachable deployment guide. The canonical vulnerability
reporting policy and detailed security contract live in
[SECURITY.md](https://github.com/H4RL33/wormhole/blob/main/SECURITY.md).

## Human control

Wormhole is designed for capable agents operating inside explicit boundaries.

- Humans control deployment, credentials, destructive actions, and policy.
- Agent access is granted through project-scoped Passports and exact tool
  permissions.
- Governance, when adopted, remains opt-in and human-authorized.
- Git remains the source of truth for code; Wormhole cannot silently replace
  repository contents.

## Trust boundaries

### Harness to local daemon

Harnesses connect to `gatewayd` over a same-user Unix socket. The socket and
its parent directory must remain owner-only. V1 adds no second local bearer
token, so any process running as that OS user is inside the local trust
boundary.

### Local daemon to Coordination Server

Each organization profile carries a project-scoped bearer token. Use HTTPS for
every non-loopback Coordination Server. Plain HTTP sends the bearer token
unencrypted and is suitable only for loopback development.

### Coordination Server to PostgreSQL

Project-scoped PostgreSQL tables use row-level security. Store operations set
the transaction-local `wormhole.project_id`; policy predicates prevent one
project from reading or writing another project's rows.

Production database credentials must not be superusers or hold `BYPASSRLS`.
The broader pre-beta role and RLS audit is tracked in
[#36](https://github.com/H4RL33/wormhole/issues/36).

## Credentials

- Raw Passport bearer tokens are shown once and stored only in local credential
  profiles.
- The Coordination Server stores token hashes, not raw bearer tokens.
- Newly created credential directories request mode `0700`; files request
  `0600`.
- Existing path permissions are not automatically tightened. Verify them
  before use.
- Never commit `~/.wormhole/credentials/` or the local SQLite replica.

## Offline state

Local writes are persisted to SQLite before sync and outbound work remains in
a restart-surviving queue. First-time enrollment and current daemon startup
still require a reachable Coordination Server; true serverless initialization
and offline startup are tracked in
[#37](https://github.com/H4RL33/wormhole/issues/37).

## Reporting vulnerabilities

Do not open a public issue.

- Use GitHub Private Vulnerability Reporting from the repository Security tab.
- Email `security@wormhole.systems`.

See [SECURITY.md](https://github.com/H4RL33/wormhole/blob/main/SECURITY.md) for
the current response policy.
