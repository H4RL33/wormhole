# Contributing to Wormhole

Thank you for your interest in contributing to Wormhole! We are currently in **v0.2.3-alpha** and moving quickly. To keep the codebase robust, secure, and maintainable, all contributions must strictly adhere to the guidelines below.

---

## Architectural Constraints

Every modification must respect the codebase boundaries and layering principles:

1. **R1: Flow direction**: Core packages (`internal/core/*`) must never import the MCP layer (`internal/mcp`). The flow is strictly one-way: `mcp -> core`.
2. **R2: Core isolation**: Core packages (`internal/core/*`) must not import each other. The only exception is `tasks -> events` to emit status transition events.
3. **R3: Types at the bottom**: `internal/types` contains shared, plain data structures and must not import anything outside the Go standard library.
4. **R4: Dependencies**: Do not introduce new top-level packages or add external third-party Go dependencies without explicit human approval.
5. **R5: Single datastore**: Wormhole runs entirely on Go and PostgreSQL + pgvector. No caching layers (Redis), message brokers (NATS), or other datastores may be added.

For detailed rules on the codebase structure, review [docs/architecture.md](docs/architecture.md).

---

## Database Schema Rules

1. **Migrations**: Schema changes must only be introduced via `golang-migrate` SQL files in the `migrations/` directory (`NNNNNN_name.up.sql` and `NNNNNN_name.down.sql`).
2. **Reversibility**: Down migrations must fully and cleanly revert all changes from their corresponding up migrations.
3. **Multi-Tenancy Isolation**: Every project-scoped table must carry a `project_id` column, have Row-Level Security (RLS) enabled, and apply the standard isolation policy:
   ```sql
   ALTER TABLE <table_name> ENABLE ROW LEVEL SECURITY;
   CREATE POLICY <table_name>_project_isolation ON <table_name>
       USING (project_id = current_setting('wormhole.project_id', true)::uuid);
   ```

---

## Testing Requirements

All contributions must include test coverage matching these requirements:

1. **Real Postgres**: Use DB-backed tests against a real Postgres container (configured via Docker Compose). Mocking `*sql.DB` or `database/sql` is banned.
2. **Sentinel Errors**: Test both the happy path and failure paths, verifying that appropriate package sentinel variables (e.g. `ErrInvalidToken`) are returned.
3. **Isolation Tests**: Any new table or query under project-scoped RLS must include explicit cross-project rejection tests to verify that tenant boundaries cannot be bypassed.
4. **Validation**: Before proposing changes, run and verify:
   ```bash
   go build ./...
   go vet ./...
   go test ./...
   ```

---

## Workflow

1. **Open an Issue**: For non-trivial changes, open an issue to discuss design and alignment before writing code.
2. **Single-Scoped PRs**: Keep pull requests tightly scoped to a single feature or bug fix.
3. **Clean Diffs**: Avoid modifying unrelated code formatting or adding speculative code.
