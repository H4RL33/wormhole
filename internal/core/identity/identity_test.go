package identity

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

// testStore opens a real connection to the configured Postgres instance
// and skips the test if it isn't reachable — these are integration tests
// against real schema/RLS behavior, not mocks (RFC-0001 §13 claims are
// about actual storage guarantees).
func testStore(t *testing.T) *Store {
	t.Helper()
	cfg := types.LoadConfig()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		if os.Getenv("WORMHOLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("postgres required but not reachable: %v", err)
		}
		t.Skipf("postgres not reachable (%v) — run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func createProject(t *testing.T, s *Store, name string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

func cleanupAgent(t *testing.T, s *Store, agentID string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DELETE FROM agents WHERE id = $1`, agentID); err != nil {
			t.Logf("cleanup: delete agent %s: %v", agentID, err)
		}
	})
}

// TestRegister_WhoAmI_RoundTrip covers the base case behind RFC-0001 §8.5
// joining flow: register an agent, then resolve its issued token back to
// the same identity via WhoAmI.
func TestRegister_WhoAmI_RoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "round-trip")

	agent, token, err := s.Register(ctx, projectID, []string{"event.publish", "kb.write"}, "harley", "claude", []string{"code_review", "write_kb"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if token == "" {
		t.Fatal("Register returned empty token")
	}

	got, err := s.WhoAmI(ctx, projectID, token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}

	if got.Agent.ID != agent.ID {
		t.Errorf("WhoAmI ID = %q, want %q", got.Agent.ID, agent.ID)
	}
	if got.Agent.Owner != "harley" {
		t.Errorf("WhoAmI Owner = %q, want %q", got.Agent.Owner, "harley")
	}
	if got.Agent.Model != "claude" {
		t.Errorf("WhoAmI Model = %q, want %q", got.Agent.Model, "claude")
	}
	if got.ProjectID != projectID {
		t.Errorf("WhoAmI ProjectID = %q, want %q", got.ProjectID, projectID)
	}
	if !reflect.DeepEqual(got.Permissions, []string{"event.publish", "kb.write"}) {
		t.Errorf("WhoAmI Permissions = %v, want [event.publish kb.write]", got.Permissions)
	}
	if !reflect.DeepEqual(got.Agent.Capabilities, []string{"code_review", "write_kb"}) {
		t.Errorf("WhoAmI Capabilities = %v, want [code_review write_kb]", got.Agent.Capabilities)
	}
}

// TestRegister_CapabilitiesEmpty covers the nil-capabilities edge case:
// Register must not error, and WhoAmI must return an empty (not nil-panic)
// slice.
func TestRegister_CapabilitiesEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "empty-capabilities")

	agent, token, err := s.Register(ctx, projectID, []string{}, "harley", "codex", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	got, err := s.WhoAmI(ctx, projectID, token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if len(got.Agent.Capabilities) != 0 {
		t.Errorf("WhoAmI Capabilities = %v, want empty", got.Agent.Capabilities)
	}
}

func TestRegister_RequiresProjectAndExplicitPermissions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "required-scope")

	if _, _, err := s.Register(ctx, "", []string{"kb.read"}, "harley", "codex", nil); !errors.Is(err, ErrInvalidScope) {
		t.Errorf("Register(empty project) error = %v, want ErrInvalidScope", err)
	}
	if _, _, err := s.Register(ctx, projectID, nil, "harley", "codex", nil); !errors.Is(err, ErrInvalidScope) {
		t.Errorf("Register(nil permissions) error = %v, want ErrInvalidScope", err)
	}
}

// TestWhoAmI_ForgedTokenRejected is the RFC-0001 §13 unforgeability claim:
// a token that was never issued must not resolve to any agent.
func TestWhoAmI_ForgedTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.WhoAmI(ctx, "00000000-0000-0000-0000-000000000000", "0000000000000000000000000000000000000000000000000000000000000000")
	if err != ErrInvalidToken {
		t.Errorf("WhoAmI(forged) error = %v, want ErrInvalidToken", err)
	}
}

// TestWhoAmI_EmptyTokenRejected covers the trivial forgery attempt.
func TestWhoAmI_EmptyTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.WhoAmI(ctx, "00000000-0000-0000-0000-000000000000", "")
	if err != ErrInvalidToken {
		t.Errorf("WhoAmI(\"\") error = %v, want ErrInvalidToken", err)
	}
}

// TestWhoAmI_TamperedTokenRejected: flipping a single character of a real,
// previously-valid token must not resolve — the hash comparison must be
// exact, not fuzzy/prefix-based.
func TestWhoAmI_TamperedTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "tampered")

	agent, token, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	tampered := []byte(token)
	if tampered[0] == 'a' {
		tampered[0] = 'b'
	} else {
		tampered[0] = 'a'
	}

	_, err = s.WhoAmI(ctx, projectID, string(tampered))
	if err != ErrInvalidToken {
		t.Errorf("WhoAmI(tampered) error = %v, want ErrInvalidToken", err)
	}
}

// TestWhoAmI_ScopedToOwnAgent is the roadmap Day 3 "scoped-token boundaries
// hold" requirement: agent A's token must resolve only to A, never to B,
// and vice versa — no cross-identity leakage.
func TestWhoAmI_ScopedToOwnAgent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "agents")

	agentA, tokenA, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", []string{"a"})
	if err != nil {
		t.Fatalf("Register A: %v", err)
	}
	cleanupAgent(t, s, agentA.ID)

	agentB, tokenB, err := s.Register(ctx, projectID, []string{"kb.write"}, "harley", "codex", []string{"b"})
	if err != nil {
		t.Fatalf("Register B: %v", err)
	}
	cleanupAgent(t, s, agentB.ID)

	gotA, err := s.WhoAmI(ctx, projectID, tokenA)
	if err != nil {
		t.Fatalf("WhoAmI(tokenA): %v", err)
	}
	if gotA.Agent.ID != agentA.ID {
		t.Errorf("WhoAmI(tokenA).ID = %q, want %q", gotA.Agent.ID, agentA.ID)
	}
	if gotA.Agent.ID == agentB.ID {
		t.Error("WhoAmI(tokenA) resolved to agent B — cross-identity leakage")
	}

	gotB, err := s.WhoAmI(ctx, projectID, tokenB)
	if err != nil {
		t.Fatalf("WhoAmI(tokenB): %v", err)
	}
	if gotB.Agent.ID != agentB.ID {
		t.Errorf("WhoAmI(tokenB).ID = %q, want %q", gotB.Agent.ID, agentB.ID)
	}
	if gotB.Agent.ID == agentA.ID {
		t.Error("WhoAmI(tokenB) resolved to agent A — cross-identity leakage")
	}
}

func TestWhoAmI_RejectsSameAgentTokenInDifferentProject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "project-a")
	projectB := createProject(t, s, "project-b")

	agent, tokenA, err := s.Register(ctx, projectA, []string{"kb.read"}, "harley", "claude", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)
	tokenB, err := s.IssueToken(ctx, agent.ID, projectB, []string{"kb.write"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	if _, err := s.WhoAmI(ctx, projectB, tokenA); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("WhoAmI(projectB, tokenA) error = %v, want ErrInvalidToken", err)
	}
	if _, err := s.WhoAmI(ctx, projectA, tokenB); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("WhoAmI(projectA, tokenB) error = %v, want ErrInvalidToken", err)
	}

	scopeA, err := s.WhoAmI(ctx, projectA, tokenA)
	if err != nil {
		t.Fatalf("WhoAmI(projectA, tokenA): %v", err)
	}
	scopeB, err := s.WhoAmI(ctx, projectB, tokenB)
	if err != nil {
		t.Fatalf("WhoAmI(projectB, tokenB): %v", err)
	}
	if scopeA.Agent.ID != scopeB.Agent.ID {
		t.Errorf("agent IDs differ: %q != %q", scopeA.Agent.ID, scopeB.Agent.ID)
	}
	if !reflect.DeepEqual(scopeA.Permissions, []string{"kb.read"}) {
		t.Errorf("project A permissions = %v, want [kb.read]", scopeA.Permissions)
	}
	if !reflect.DeepEqual(scopeB.Permissions, []string{"kb.write"}) {
		t.Errorf("project B permissions = %v, want [kb.write]", scopeB.Permissions)
	}
}

// TestRegister_TokenHashNotReversible: the raw token must never be
// recoverable from storage — only its hash is persisted, and the hash
// must differ from the raw value (RFC-0001 §13).
func TestRegister_TokenHashNotReversible(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "hash")

	agent, token, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	var storedHash string
	err = s.db.QueryRowContext(ctx, `SELECT token_hash FROM agent_tokens WHERE agent_id = $1`, agent.ID).Scan(&storedHash)
	if err != nil {
		t.Fatalf("query token_hash: %v", err)
	}

	if storedHash == token {
		t.Error("stored token_hash equals raw token — raw token is recoverable from storage")
	}
	if storedHash == "" {
		t.Error("stored token_hash is empty")
	}
}
