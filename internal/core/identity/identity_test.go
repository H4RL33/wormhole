package identity

import (
	"context"
	"database/sql"
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
		t.Skipf("postgres not reachable (%v) — run `docker compose up -d db` and apply migrations before running this test", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
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

	agent, token, err := s.Register(ctx, "harley", "claude", []string{"code_review", "write_kb"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if token == "" {
		t.Fatal("Register returned empty token")
	}

	got, err := s.WhoAmI(ctx, token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}

	if got.ID != agent.ID {
		t.Errorf("WhoAmI ID = %q, want %q", got.ID, agent.ID)
	}
	if got.Owner != "harley" {
		t.Errorf("WhoAmI Owner = %q, want %q", got.Owner, "harley")
	}
	if got.Model != "claude" {
		t.Errorf("WhoAmI Model = %q, want %q", got.Model, "claude")
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "code_review" || got.Capabilities[1] != "write_kb" {
		t.Errorf("WhoAmI Capabilities = %v, want [code_review write_kb]", got.Capabilities)
	}
}

// TestRegister_CapabilitiesEmpty covers the nil-capabilities edge case:
// Register must not error, and WhoAmI must return an empty (not nil-panic)
// slice.
func TestRegister_CapabilitiesEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	agent, token, err := s.Register(ctx, "harley", "codex", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	got, err := s.WhoAmI(ctx, token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if len(got.Capabilities) != 0 {
		t.Errorf("WhoAmI Capabilities = %v, want empty", got.Capabilities)
	}
}

// TestWhoAmI_ForgedTokenRejected is the RFC-0001 §13 unforgeability claim:
// a token that was never issued must not resolve to any agent.
func TestWhoAmI_ForgedTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.WhoAmI(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != ErrInvalidToken {
		t.Errorf("WhoAmI(forged) error = %v, want ErrInvalidToken", err)
	}
}

// TestWhoAmI_EmptyTokenRejected covers the trivial forgery attempt.
func TestWhoAmI_EmptyTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.WhoAmI(ctx, "")
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

	agent, token, err := s.Register(ctx, "harley", "claude", nil)
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

	_, err = s.WhoAmI(ctx, string(tampered))
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

	agentA, tokenA, err := s.Register(ctx, "harley", "claude", []string{"a"})
	if err != nil {
		t.Fatalf("Register A: %v", err)
	}
	cleanupAgent(t, s, agentA.ID)

	agentB, tokenB, err := s.Register(ctx, "harley", "codex", []string{"b"})
	if err != nil {
		t.Fatalf("Register B: %v", err)
	}
	cleanupAgent(t, s, agentB.ID)

	gotA, err := s.WhoAmI(ctx, tokenA)
	if err != nil {
		t.Fatalf("WhoAmI(tokenA): %v", err)
	}
	if gotA.ID != agentA.ID {
		t.Errorf("WhoAmI(tokenA).ID = %q, want %q", gotA.ID, agentA.ID)
	}
	if gotA.ID == agentB.ID {
		t.Error("WhoAmI(tokenA) resolved to agent B — cross-identity leakage")
	}

	gotB, err := s.WhoAmI(ctx, tokenB)
	if err != nil {
		t.Fatalf("WhoAmI(tokenB): %v", err)
	}
	if gotB.ID != agentB.ID {
		t.Errorf("WhoAmI(tokenB).ID = %q, want %q", gotB.ID, agentB.ID)
	}
	if gotB.ID == agentA.ID {
		t.Error("WhoAmI(tokenB) resolved to agent A — cross-identity leakage")
	}
}

// TestRegister_TokenHashNotReversible: the raw token must never be
// recoverable from storage — only its hash is persisted, and the hash
// must differ from the raw value (RFC-0001 §13).
func TestRegister_TokenHashNotReversible(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	agent, token, err := s.Register(ctx, "harley", "claude", nil)
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
