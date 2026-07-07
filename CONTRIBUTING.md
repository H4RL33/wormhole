# Contributing

Wormhole is pre-alpha, moving fast against a 24-day roadmap (see [ROADMAP.md](ROADMAP.md)). External contributions aren't being actively solicited yet, but issues/discussion are welcome.

## Ground rules

- RFC-0001 and RFC-0002 (`docs/rfcs/`) are the source of truth for scope. Changes that expand MVP scope (see RFC-0001 §12) need discussion first, not a surprise PR.
- Git remains the source of truth for code — Wormhole itself never stores or mirrors code, only references (commit SHAs, PR URLs).
- Everything user-facing goes through MCP (RFC-0001 §9). If a capability isn't exposed as an MCP tool, it isn't part of the platform surface.

## Workflow

1. Open an issue before a non-trivial PR.
2. Keep PRs scoped to one roadmap item where possible.
3. Tests required for anything touching identity, permissions, or multi-tenancy.
