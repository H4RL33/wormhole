# Day 4: Passport Object Model + Audit Trail Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every registered agent a Passport (portable, project-scoped identity record: repositories + roles) issued automatically at registration, and an append-only Audit Trail recording every identity-service action, per RFC-0001 §8.4.

**Architecture:** `passports` table already exists (migration 000001) with `agent_id, project_id, repositories jsonb, roles jsonb, issued_at`, unique on `(agent_id, project_id)`. Add a Go `Passport` struct and `Store.IssuePassport` method, then extend `Store.Register` to issue a passport in the same transaction as the agent + token insert (Day 4 roadmap: "Passport issuance on registration"). Add a new `audit_log` table (migration 000003) and `Store.RecordAction` method; call it from `Register`, `IssueToken`, and `IssuePassport` so every identity-service action leaves an append-only trail scoped by project, matching the RLS convention set in migration 000001.

**Tech Stack:** Go, `database/sql`, `github.com/lib/pq`, Postgres (golang-migrate migrations), existing `internal/core/identity` package.

## Global Constraints

- RLS convention (migration 000001 comment, lines 46-51): every project-scoped table gets `ALTER TABLE x ENABLE ROW LEVEL SECURITY` + a policy named `<table>_project_isolation` comparing `project_id = current_setting('wormhole.project_id', true)::uuid`. Apply this to `audit_log`.
- Migration numbering continues sequentially: next pair is `000003_audit_trail.up.sql` / `000003_audit_trail.down.sql`.
- All new `Store` methods follow the existing package's error style: sentinel `var Err... = errors.New(...)` for expected/validation failures, `fmt.Errorf("identity: <verb>: %w", err)` for wrapped unexpected failures.
- Tests are DB-backed integration tests in `internal/core/identity/identity_test.go`, using the existing `testStore(t)`, `createProject(t, s, name)`, `cleanupAgent(t, s, agentID)` helpers already defined in that file — do not duplicate them.
- `go build ./...` and `go vet ./...` must stay clean after every task.
- No DB is reachable in the execution sandbox; DB-backed tests will `t.Skip` locally (expected) and run for real in CI (`WORMHOLE_INTEGRATION_REQUIRED=1`). Do not treat a local skip as a failure.
- Passports and audit rows are append-only / immutable once written — no update methods, only insert + select.

---

### Task 1: Passport object model + issuance migration

**Files:**
- Modify: `internal/core/identity/identity.go`
- Test: `internal/core/identity/identity_test.go`

**Interfaces:**
- Consumes: existing `Store`, `Store.db *sql.DB`, existing `Register(ctx, projectID, permissions, owner, model, capabilities) (Agent, string, error)` at `internal/core/identity/identity.go:54`, existing `passports` table (migration `000001_init_schema.up.sql`: columns `id, agent_id, project_id, repositories jsonb, roles jsonb, issued_at`, `UNIQUE (agent_id, project_id)`).
- Produces:
  - `type Passport struct { ID string; AgentID string; ProjectID string; Repositories []string; Roles []string; IssuedAt time.Time }`
  - `var ErrPassportExists = errors.New("identity: passport already issued for this agent and project")`
  - `func (s *Store) IssuePassport(ctx context.Context, agentID, projectID string, repositories, roles []string) (Passport, error)` — inserts a row into `passports`, returns the created `Passport`. `repositories`/`roles` nil is treated as empty (matches `capabilities` handling in `Register`), never as an error.
  - `Register`'s signature changes to `func (s *Store) Register(ctx context.Context, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (Agent, Passport, string, error)` — issues the passport in the same transaction as the agent + token insert. Return order: `Agent, Passport, string (raw token), error`.

Task 2 (audit trail) and Task 3 (tests) both depend on this exact `Register` signature and `Passport` struct — do not deviate from the field names/order above.

- [ ] **Step 1: Write the failing tests**

Add to `internal/core/identity/identity_test.go` (append at end of file, after `TestRegister_TokenHashNotReversible`):

```go
// TestRegister_IssuesPassport is the roadmap Day 4 "passport issuance on
// registration" requirement: registering an agent must also create its
// passport for that project, carrying the declared repositories and roles
// (RFC-0001 §8.4).
func TestRegister_IssuesPassport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "passport-issuance")

	agent, passport, _, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude",
		[]string{"code_review"}, []string{"github.com/acme/backend"}, []string{"contributor"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if passport.ID == "" {
		t.Fatal("Register returned passport with empty ID")
	}
	if passport.AgentID != agent.ID {
		t.Errorf("Passport.AgentID = %q, want %q", passport.AgentID, agent.ID)
	}
	if passport.ProjectID != projectID {
		t.Errorf("Passport.ProjectID = %q, want %q", passport.ProjectID, projectID)
	}
	if !reflect.DeepEqual(passport.Repositories, []string{"github.com/acme/backend"}) {
		t.Errorf("Passport.Repositories = %v, want [github.com/acme/backend]", passport.Repositories)
	}
	if !reflect.DeepEqual(passport.Roles, []string{"contributor"}) {
		t.Errorf("Passport.Roles = %v, want [contributor]", passport.Roles)
	}
	if passport.IssuedAt.IsZero() {
		t.Error("Passport.IssuedAt is zero")
	}
}

// TestRegister_PassportRepositoriesRolesNilBecomeEmpty covers the
// nil-repositories/nil-roles edge case, mirroring the existing
// nil-capabilities handling.
func TestRegister_PassportRepositoriesRolesNilBecomeEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "passport-nil-fields")

	agent, passport, _, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "codex", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if len(passport.Repositories) != 0 {
		t.Errorf("Passport.Repositories = %v, want empty", passport.Repositories)
	}
	if len(passport.Roles) != 0 {
		t.Errorf("Passport.Roles = %v, want empty", passport.Roles)
	}
}

// TestIssuePassport_DuplicateRejected: a second passport for the same
// agent+project pair must be rejected — the passports table's
// UNIQUE(agent_id, project_id) constraint is the source of truth, and
// IssuePassport must surface it as ErrPassportExists, not a raw SQL error.
func TestIssuePassport_DuplicateRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "passport-duplicate")

	agent, _, _, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil, nil, []string{"contributor"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	_, err = s.IssuePassport(ctx, agent.ID, projectID, nil, []string{"reviewer"})
	if !errors.Is(err, ErrPassportExists) {
		t.Errorf("IssuePassport(duplicate) error = %v, want ErrPassportExists", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail to compile (Register signature doesn't match yet)**

Run: `go build ./... && go test ./internal/core/identity/... -run TestRegister_IssuesPassport -v`
Expected: compile error, e.g. `not enough arguments in call to s.Register` / `undefined: Passport`

- [ ] **Step 3: Write the migration**

Create `migrations/000003_audit_trail.up.sql` — wait, this file is Task 2's migration. Task 1 needs no new migration; the `passports` table already exists from migration 000001. Skip straight to the Go implementation.

- [ ] **Step 4: Implement `Passport` struct, `ErrPassportExists`, `IssuePassport`, and update `Register`**

Edit `internal/core/identity/identity.go`. Add after the `ErrInvalidScope` var block (after line 24):

```go
// ErrPassportExists is returned when a passport is issued for an
// agent+project pair that already has one — passports are append-only
// and unique per (agent, project) (migration 000001, UNIQUE(agent_id,
// project_id)).
var ErrPassportExists = errors.New("identity: passport already issued for this agent and project")
```

Add after the `AuthenticatedScope` struct (after line 41):

```go
// Passport is the portable, project-scoped identity record an agent
// presents when joining a project: its declared repository scope and
// resolved roles (RFC-0001 §8.4, §8.5).
type Passport struct {
	ID           string
	AgentID      string
	ProjectID    string
	Repositories []string
	Roles        []string
	IssuedAt     time.Time
}
```

Add a new method after `Register` (after line 108, before `IssueToken`):

```go
// IssuePassport creates the portable identity record an agent presents
// when joining projectID. Nil repositories/roles are treated as empty,
// never as an error. A second passport for the same agent+project pair
// is rejected — passports are append-only.
func (s *Store) IssuePassport(ctx context.Context, agentID, projectID string, repositories, roles []string) (Passport, error) {
	return issuePassport(ctx, s.db, agentID, projectID, repositories, roles)
}

// dbtx is satisfied by both *sql.DB and *sql.Tx, letting issuePassport run
// standalone (Store.IssuePassport) or inside Register's transaction.
type dbtx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func issuePassport(ctx context.Context, db dbtx, agentID, projectID string, repositories, roles []string) (Passport, error) {
	if repositories == nil {
		repositories = []string{}
	}
	if roles == nil {
		roles = []string{}
	}
	reposJSON, err := json.Marshal(repositories)
	if err != nil {
		return Passport{}, fmt.Errorf("identity: marshal repositories: %w", err)
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return Passport{}, fmt.Errorf("identity: marshal roles: %w", err)
	}

	var passport Passport
	var reposRaw, rolesRaw []byte
	err = db.QueryRowContext(ctx,
		`INSERT INTO passports (agent_id, project_id, repositories, roles) VALUES ($1, $2, $3, $4)
		 RETURNING id, agent_id, project_id, repositories, roles, issued_at`,
		agentID, projectID, reposJSON, rolesJSON,
	).Scan(&passport.ID, &passport.AgentID, &passport.ProjectID, &reposRaw, &rolesRaw, &passport.IssuedAt)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return Passport{}, ErrPassportExists
		}
		return Passport{}, fmt.Errorf("identity: insert passport: %w", err)
	}
	if err := json.Unmarshal(reposRaw, &passport.Repositories); err != nil {
		return Passport{}, fmt.Errorf("identity: unmarshal repositories: %w", err)
	}
	if err := json.Unmarshal(rolesRaw, &passport.Roles); err != nil {
		return Passport{}, fmt.Errorf("identity: unmarshal roles: %w", err)
	}
	return passport, nil
}
```

Add the `pq` import to the import block at the top of the file:

```go
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)
```

Now update `Register` (lines 54-108) to take `repositories, roles []string`, issue the passport inside the existing transaction via `issuePassport(ctx, tx, ...)`, and return `Passport` as the second value:

```go
// Register creates a new agent identity, issues its passport for
// projectID, and issues a bearer token for it. The raw token is returned
// exactly once — only its SHA-256 hash is persisted, so the raw value can
// never be recovered from storage.
func (s *Store) Register(ctx context.Context, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (Agent, Passport, string, error) {
	if projectID == "" || permissions == nil {
		return Agent{}, Passport{}, "", ErrInvalidScope
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	capsJSON, err := json.Marshal(capabilities)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal capabilities: %w", err)
	}

	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Agent{}, Passport{}, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: begin tx: %w", err)
	}
	defer tx.Rollback()

	var agent Agent
	var capsRaw []byte
	err = tx.QueryRowContext(ctx,
		`INSERT INTO agents (owner, model, capabilities) VALUES ($1, $2, $3)
		 RETURNING id, owner, model, capabilities, created_at`,
		owner, model, capsJSON,
	).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert agent: %w", err)
	}

	passport, err := issuePassport(ctx, tx, agent.ID, projectID, repositories, roles)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: issue passport: %w", err)
	}

	permissionsJSON, err := json.Marshal(permissions)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash) VALUES ($1, $2, $3, $4)`,
		agent.ID, projectID, permissionsJSON, tokenHash,
	); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: commit: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}

	return agent, passport, rawToken, nil
}
```

Note `*sql.Tx` satisfies the `dbtx` interface (it has `QueryRowContext`), so `issuePassport(ctx, tx, ...)` works unchanged.

Every other existing caller of `Register` in `identity_test.go` (the 8 pre-existing tests) must be updated to match the new 4-value return and 7 positional args (append `nil, nil` for repositories/roles unless the test cares, and add the `passport` (or `_`) return value). Update each call site:
- `TestRegister_WhoAmI_RoundTrip`: `agent, token, err := s.Register(ctx, projectID, []string{"event.publish", "kb.write"}, "harley", "claude", []string{"code_review", "write_kb"})` becomes `agent, _, token, err := s.Register(ctx, projectID, []string{"event.publish", "kb.write"}, "harley", "claude", []string{"code_review", "write_kb"}, nil, nil)`
- `TestRegister_CapabilitiesEmpty`: same pattern, add `, nil, nil` before the closing paren, `agent, _, token, err := ...`
- `TestRegister_RequiresProjectAndExplicitPermissions`: both `s.Register(...)` calls get `, nil, nil` appended and `_, _, _, err :=` (4 return values now)
- `TestWhoAmI_TamperedTokenRejected`: `agent, token, err := s.Register(...)` becomes `agent, _, token, err := s.Register(..., nil, nil)`
- `TestWhoAmI_ScopedToOwnAgent`: both Register calls, same pattern
- `TestWhoAmI_RejectsSameAgentTokenInDifferentProject`: `agent, tokenA, err := s.Register(...)` becomes `agent, _, tokenA, err := s.Register(..., nil, nil)`
- `TestRegister_TokenHashNotReversible`: same pattern

- [ ] **Step 5: Run tests to verify they pass (locally they will skip — no DB — confirm skip, not a compile or logic failure)**

Run: `go build ./... && go vet ./... && go test ./internal/core/identity/... -v`
Expected: `go build`/`go vet` produce no output; `go test` shows all tests `SKIP: postgres not reachable` (not FAIL, not a compile error).

- [ ] **Step 6: Commit**

```bash
git add internal/core/identity/identity.go internal/core/identity/identity_test.go
git commit -m "Day 4: passport object model + issuance on registration"
```

---

### Task 2: Audit trail table + `RecordAction`, wired into Register/IssueToken/IssuePassport

**Files:**
- Create: `migrations/000003_audit_trail.up.sql`
- Create: `migrations/000003_audit_trail.down.sql`
- Modify: `internal/core/identity/identity.go`
- Test: `internal/core/identity/identity_test.go`

**Interfaces:**
- Consumes: `Store` from Task 1, `Register`/`IssueToken`/`IssuePassport` signatures exactly as Task 1 left them, the `dbtx` interface from Task 1 (`internal/core/identity/identity.go`, has `QueryRowContext`) — extend it or add a sibling interface for `ExecContext` as needed since audit rows don't need `RETURNING`.
- Produces:
  - `type AuditEntry struct { ID string; AgentID string; ProjectID string; Action string; CreatedAt time.Time }`
  - `func (s *Store) RecordAction(ctx context.Context, agentID, projectID, action string) (AuditEntry, error)`
  - `func (s *Store) ListAuditTrail(ctx context.Context, agentID, projectID string) ([]AuditEntry, error)` — returns entries ordered oldest-first (`ORDER BY created_at ASC`).
  - Action string constants: `ActionAgentRegistered = "agent.registered"`, `ActionTokenIssued = "token.issued"`, `ActionPassportIssued = "passport.issued"`.

- [ ] **Step 1: Write the migration**

Create `migrations/000003_audit_trail.up.sql`:

```sql
-- RFC-0001 §8.4: append-only log of actions taken under an identity.
-- Immutable by convention — no UPDATE/DELETE path is exposed by the Go
-- Store; this migration does not add a DB-level trigger to enforce it
-- (matches the rest of the schema, which relies on application-level
-- discipline rather than triggers).

CREATE TABLE audit_log (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    action      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_agent_id ON audit_log(agent_id);
CREATE INDEX idx_audit_log_project_id ON audit_log(project_id);

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
```

Create `migrations/000003_audit_trail.down.sql`:

```sql
DROP TABLE IF EXISTS audit_log;
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/core/identity/identity_test.go`:

```go
// TestRegister_RecordsAuditTrail: registering an agent must leave an
// append-only audit trail entry for the registration itself, the passport
// issuance, and the token issuance (RFC-0001 §8.4 Audit Trail).
func TestRegister_RecordsAuditTrail(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "audit-trail")

	agent, _, _, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil, nil, []string{"contributor"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	entries, err := s.ListAuditTrail(ctx, agent.ID, projectID)
	if err != nil {
		t.Fatalf("ListAuditTrail: %v", err)
	}

	wantActions := []string{ActionAgentRegistered, ActionPassportIssued, ActionTokenIssued}
	if len(entries) != len(wantActions) {
		t.Fatalf("ListAuditTrail returned %d entries, want %d: %+v", len(entries), len(wantActions), entries)
	}
	for i, entry := range entries {
		if entry.Action != wantActions[i] {
			t.Errorf("entries[%d].Action = %q, want %q", i, entry.Action, wantActions[i])
		}
		if entry.AgentID != agent.ID {
			t.Errorf("entries[%d].AgentID = %q, want %q", i, entry.AgentID, agent.ID)
		}
		if entry.ProjectID != projectID {
			t.Errorf("entries[%d].ProjectID = %q, want %q", i, entry.ProjectID, projectID)
		}
	}
}

// TestIssueToken_RecordsAuditTrail: a separately issued token (IssueToken,
// outside Register) must also append to the trail.
func TestIssueToken_RecordsAuditTrail(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "audit-issue-token-a")
	projectB := createProject(t, s, "audit-issue-token-b")

	agent, _, _, err := s.Register(ctx, projectA, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if _, err := s.IssueToken(ctx, agent.ID, projectB, []string{"kb.write"}); err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	entries, err := s.ListAuditTrail(ctx, agent.ID, projectB)
	if err != nil {
		t.Fatalf("ListAuditTrail: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != ActionTokenIssued {
		t.Fatalf("ListAuditTrail(projectB) = %+v, want single %q entry", entries, ActionTokenIssued)
	}
}

// TestListAuditTrail_ScopedToProject: audit entries for one project must
// not leak into another project's trail for the same agent.
func TestListAuditTrail_ScopedToProject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "audit-scope-a")
	projectB := createProject(t, s, "audit-scope-b")

	agent, _, _, err := s.Register(ctx, projectA, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	entriesB, err := s.ListAuditTrail(ctx, agent.ID, projectB)
	if err != nil {
		t.Fatalf("ListAuditTrail(projectB): %v", err)
	}
	if len(entriesB) != 0 {
		t.Errorf("ListAuditTrail(projectB) = %+v, want empty (registration was under projectA)", entriesB)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail to compile**

Run: `go build ./... && go test ./internal/core/identity/... -run TestRegister_RecordsAuditTrail -v`
Expected: compile error, e.g. `undefined: ActionAgentRegistered` / `s.ListAuditTrail undefined`

- [ ] **Step 4: Implement `AuditEntry`, action constants, `RecordAction`, `ListAuditTrail`, and wire into `Register`/`IssueToken`/`IssuePassport`**

Edit `internal/core/identity/identity.go`. Add after the `Passport` struct:

```go
// AuditEntry is one append-only record in an identity's audit trail
// (RFC-0001 §8.4).
type AuditEntry struct {
	ID        string
	AgentID   string
	ProjectID string
	Action    string
	CreatedAt time.Time
}

// Audit action names recorded by the identity service.
const (
	ActionAgentRegistered = "agent.registered"
	ActionTokenIssued     = "token.issued"
	ActionPassportIssued  = "passport.issued"
)
```

Widen the `dbtx` interface (both `*sql.DB` and `*sql.Tx` already satisfy `ExecContext` too) to cover audit inserts:

```go
// dbtx is satisfied by both *sql.DB and *sql.Tx, letting issuePassport and
// recordAction run standalone (Store methods) or inside Register's
// transaction.
type dbtx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
```

Add `RecordAction`, `ListAuditTrail`, and the internal `recordAction` helper (mirrors the `IssuePassport`/`issuePassport` split from Task 1) after `IssuePassport`:

```go
// RecordAction appends one entry to agentID's audit trail for projectID.
func (s *Store) RecordAction(ctx context.Context, agentID, projectID, action string) (AuditEntry, error) {
	return recordAction(ctx, s.db, agentID, projectID, action)
}

func recordAction(ctx context.Context, db dbtx, agentID, projectID, action string) (AuditEntry, error) {
	var entry AuditEntry
	err := db.QueryRowContext(ctx,
		`INSERT INTO audit_log (agent_id, project_id, action) VALUES ($1, $2, $3)
		 RETURNING id, agent_id, project_id, action, created_at`,
		agentID, projectID, action,
	).Scan(&entry.ID, &entry.AgentID, &entry.ProjectID, &entry.Action, &entry.CreatedAt)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("identity: insert audit entry: %w", err)
	}
	return entry, nil
}

// ListAuditTrail returns agentID's audit trail for projectID, oldest
// first.
func (s *Store) ListAuditTrail(ctx context.Context, agentID, projectID string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, project_id, action, created_at
		 FROM audit_log
		 WHERE agent_id = $1 AND project_id = $2
		 ORDER BY created_at ASC`,
		agentID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity: list audit trail: %w", err)
	}
	defer rows.Close()

	entries := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.ProjectID, &entry.Action, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("identity: scan audit entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: iterate audit trail: %w", err)
	}
	return entries, nil
}
```

`QueryRowContext`'s `*sql.Tx`/`*sql.DB` both already satisfy this — since `dbtx` is used with `tx` inside `Register`, `recordAction(ctx, tx, ...)` works.

Wire audit calls into `Register` (order matters — test asserts `ActionAgentRegistered, ActionPassportIssued, ActionTokenIssued`, matching insertion order): inside the transaction, immediately after the agent insert succeeds, call `recordAction(ctx, tx, agent.ID, projectID, ActionAgentRegistered)`; immediately after `issuePassport` succeeds, call `recordAction(ctx, tx, agent.ID, projectID, ActionPassportIssued)`; immediately after the token insert succeeds, call `recordAction(ctx, tx, agent.ID, projectID, ActionTokenIssued)`. Each call's error wraps and returns exactly like the surrounding inserts (`fmt.Errorf("identity: record audit entry: %w", err)`), still inside the same transaction so a failure rolls back the whole registration.

The resulting `Register` body (full replacement, same signature as Task 1):

```go
func (s *Store) Register(ctx context.Context, projectID string, permissions []string, owner, model string, capabilities, repositories, roles []string) (Agent, Passport, string, error) {
	if projectID == "" || permissions == nil {
		return Agent{}, Passport{}, "", ErrInvalidScope
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	capsJSON, err := json.Marshal(capabilities)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal capabilities: %w", err)
	}

	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return Agent{}, Passport{}, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: begin tx: %w", err)
	}
	defer tx.Rollback()

	var agent Agent
	var capsRaw []byte
	err = tx.QueryRowContext(ctx,
		`INSERT INTO agents (owner, model, capabilities) VALUES ($1, $2, $3)
		 RETURNING id, owner, model, capabilities, created_at`,
		owner, model, capsJSON,
	).Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert agent: %w", err)
	}
	if _, err := recordAction(ctx, tx, agent.ID, projectID, ActionAgentRegistered); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: record audit entry: %w", err)
	}

	passport, err := issuePassport(ctx, tx, agent.ID, projectID, repositories, roles)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: issue passport: %w", err)
	}
	if _, err := recordAction(ctx, tx, agent.ID, projectID, ActionPassportIssued); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: record audit entry: %w", err)
	}

	permissionsJSON, err := json.Marshal(permissions)
	if err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: marshal permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash) VALUES ($1, $2, $3, $4)`,
		agent.ID, projectID, permissionsJSON, tokenHash,
	); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: insert token: %w", err)
	}
	if _, err := recordAction(ctx, tx, agent.ID, projectID, ActionTokenIssued); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: record audit entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: commit: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return Agent{}, Passport{}, "", fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}

	return agent, passport, rawToken, nil
}
```

Wire `IssueToken` (full replacement, same signature as before Task 1/2, no signature change):

```go
func (s *Store) IssueToken(ctx context.Context, agentID, projectID string, permissions []string) (string, error) {
	if agentID == "" || projectID == "" || permissions == nil {
		return "", ErrInvalidScope
	}
	permissionsJSON, err := json.Marshal(permissions)
	if err != nil {
		return "", fmt.Errorf("identity: marshal permissions: %w", err)
	}
	rawToken, tokenHash, err := generateToken()
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_tokens (agent_id, project_id, permissions, token_hash) VALUES ($1, $2, $3, $4)`,
		agentID, projectID, permissionsJSON, tokenHash,
	); err != nil {
		return "", fmt.Errorf("identity: insert token: %w", err)
	}
	if _, err := recordAction(ctx, s.db, agentID, projectID, ActionTokenIssued); err != nil {
		return "", fmt.Errorf("identity: record audit entry: %w", err)
	}
	return rawToken, nil
}
```

`Store.IssuePassport` (the standalone, non-`Register` path) also records its own audit entry — update it:

```go
func (s *Store) IssuePassport(ctx context.Context, agentID, projectID string, repositories, roles []string) (Passport, error) {
	passport, err := issuePassport(ctx, s.db, agentID, projectID, repositories, roles)
	if err != nil {
		return Passport{}, err
	}
	if _, err := recordAction(ctx, s.db, agentID, projectID, ActionPassportIssued); err != nil {
		return Passport{}, fmt.Errorf("identity: record audit entry: %w", err)
	}
	return passport, nil
}
```

This means `TestIssuePassport_DuplicateRejected` (Task 1) still passes unchanged: the duplicate call fails inside `issuePassport` before reaching `recordAction`, so no audit entry is written for the rejected attempt.

- [ ] **Step 5: Run tests to verify they pass (local skip expected, no DB)**

Run: `go build ./... && go vet ./... && go test ./internal/core/identity/... -v`
Expected: `go build`/`go vet` clean; all tests `SKIP: postgres not reachable`, zero compile errors, zero FAIL.

- [ ] **Step 6: Commit**

```bash
git add migrations/000003_audit_trail.up.sql migrations/000003_audit_trail.down.sql internal/core/identity/identity.go internal/core/identity/identity_test.go
git commit -m "Day 4: append-only audit trail for identity-service actions"
```

---

### Task 3: Roadmap update

**Files:**
- Modify: `ROADMAP.md`

**Interfaces:**
- Consumes: nothing code-level: this task only marks roadmap checkboxes complete once Tasks 1-2 are reviewed clean.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Mark Day 4 items complete**

In `ROADMAP.md`, find the `### Day 4 — 2026-07-10` section (currently 3 unchecked items: passport object model, passport issuance, audit trail). Replace it with:

```markdown
### Day 4 — 2026-07-10
- [x] Passport object model (RFC §8.4): owner, model, capabilities, repositories, roles — `internal/core/identity/identity.go` (`Passport` struct; owner/model/capabilities already on `Agent`, repositories/roles on `Passport`)
- [x] Passport issuance on registration — `Store.Register` issues agent + passport + token in one transaction; `Store.IssuePassport` for standalone issuance
- [x] Audit trail: append-only action log per identity — `audit_log` table (`migrations/000003_audit_trail.*.sql`), `Store.RecordAction`/`Store.ListAuditTrail`, wired into `Register`/`IssueToken`/`IssuePassport`
```

- [ ] **Step 2: Commit**

```bash
git add ROADMAP.md
git commit -m "Day 4: mark roadmap items complete"
```

---

## Self-Review Notes

- **Spec coverage:** all 3 Day 4 roadmap bullets map 1:1 to Tasks 1 (object model + issuance), 2 (audit trail), 3 (roadmap bookkeeping). RFC-0001 §8.4 fields (`Owner, Model, Capabilities, Repositories, Roles, Audit Trail`) are all represented: Owner/Model/Capabilities already lived on `Agent` (Day 1-3), Repositories/Roles land on `Passport` here, Audit Trail is the new `audit_log` table. `Sessions` and `Permissions` fields from the RFC list are out of scope for Day 4 (Permissions already exists on `AuthenticatedScope`/`agent_tokens` from Day 3; Sessions isn't scheduled until later in the roadmap — not invented here).
- **Placeholder scan:** none — every step has complete code.
- **Type consistency:** `Register` return type `(Agent, Passport, string, error)` is identical across Task 1's definition, Task 2's full replacement, and every call-site update. `dbtx` interface is defined once in Task 1 and widened (not redefined) in Task 2 — the plan text for Task 2 shows the full replacement so the implementer doesn't have two conflicting `dbtx` definitions in different tasks.
