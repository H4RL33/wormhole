package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestBeginProjectTxWithOptionsSetsIsolationAndProject(t *testing.T) {
	store := testStore(t)
	projectID := createProject(t, store, "tx-options")
	tx, err := store.BeginProjectTxWithOptions(context.Background(), projectID, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var isolation, readOnly, gotProject string
	if err := tx.QueryRow(`SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only'), current_setting('wormhole.project_id', true)`).Scan(&isolation, &readOnly, &gotProject); err != nil {
		t.Fatal(err)
	}
	if isolation != "repeatable read" || readOnly != "off" || gotProject != projectID {
		t.Fatalf("transaction = (%q,%q,%q), want repeatable read/off/%q", isolation, readOnly, gotProject, projectID)
	}
}

func lockIdentityRLSFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("open RLS fixture lock connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock(867530913)`); err != nil {
		conn.Close()
		t.Fatalf("acquire RLS fixture lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(867530913)`)
		_ = conn.Close()
	})
}

func testEnrolmentRegistrationInput(projectID string) EnrolmentRegistrationInput {
	return EnrolmentRegistrationInput{
		ProjectID:      projectID,
		IdempotencyKey: "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1",
		RequestHash:    strings.Repeat("a", 64),
		Permissions:    []string{"task.create", "kb.write"},
		Owner:          "enrolment-owner",
		Model:          "gpt-5",
		Capabilities:   []string{"code", "review"},
		Repositories:   []string{"https://github.com/H4RL33/wormhole"},
		Roles:          []string{"contributor"},
	}
}

func TestRegisterEnrolment_ReplayReturnsSameIdentityWithoutRawToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "enrolment-replay")
	in := testEnrolmentRegistrationInput(projectID)

	first, err := s.RegisterEnrolment(ctx, in)
	if err != nil {
		t.Fatalf("RegisterEnrolment first: %v", err)
	}
	cleanupAgent(t, s, first.Agent.ID)
	if first.RawToken == "" || first.Replay {
		t.Fatalf("first result fields: token_empty=%v replay=%v", first.RawToken == "", first.Replay)
	}

	replay, err := s.RegisterEnrolment(ctx, in)
	if err != nil {
		t.Fatalf("RegisterEnrolment replay: %v", err)
	}
	if replay.Agent.ID != first.Agent.ID || replay.Passport.ID != first.Passport.ID {
		t.Fatalf("replay identity = agent %q passport %q, want %q/%q", replay.Agent.ID, replay.Passport.ID, first.Agent.ID, first.Passport.ID)
	}
	if replay.RawToken != "" || !replay.Replay {
		t.Fatalf("replay fields: token_empty=%v replay=%v", replay.RawToken == "", replay.Replay)
	}

	var agents, passports, enrolments int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE id = $1`, first.Agent.ID).Scan(&agents); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM passports WHERE id = $1`, first.Passport.ID).Scan(&passports); err != nil {
		t.Fatalf("count passports: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_enrolments WHERE project_id = $1 AND idempotency_key = $2`, projectID, in.IdempotencyKey).Scan(&enrolments); err != nil {
		t.Fatalf("count enrolments: %v", err)
	}
	if agents != 1 || passports != 1 || enrolments != 1 {
		t.Fatalf("row counts agents=%d passports=%d enrolments=%d, want 1/1/1", agents, passports, enrolments)
	}
}

func TestRegisterEnrolment_RejectsDigestConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "enrolment-conflict")
	in := testEnrolmentRegistrationInput(projectID)

	first, err := s.RegisterEnrolment(ctx, in)
	if err != nil {
		t.Fatalf("RegisterEnrolment first: %v", err)
	}
	cleanupAgent(t, s, first.Agent.ID)

	conflict := in
	conflict.RequestHash = strings.Repeat("b", 64)
	if _, err := s.RegisterEnrolment(ctx, conflict); !errors.Is(err, ErrEnrolmentConflict) {
		t.Fatalf("digest conflict error = %v, want ErrEnrolmentConflict", err)
	}
}

func TestRegisterEnrolment_ConcurrentSameKeyWaitsAndReplaysWinner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "enrolment-concurrent-replay")
	in := testEnrolmentRegistrationInput(projectID)

	winnerTx, err := s.BeginProjectTx(ctx, projectID)
	if err != nil {
		t.Fatalf("begin winner tx: %v", err)
	}
	defer winnerTx.Rollback()
	winner, err := s.RegisterEnrolmentInTx(ctx, winnerTx, in)
	if err != nil {
		t.Fatalf("register winner in transaction: %v", err)
	}

	type registrationResponse struct {
		result EnrolmentRegistrationResult
		err    error
	}
	response := make(chan registrationResponse, 1)
	go func() {
		result, err := s.RegisterEnrolment(ctx, in)
		response <- registrationResponse{result: result, err: err}
	}()
	select {
	case early := <-response:
		t.Fatalf("concurrent request returned before winner committed: err=%v replay=%v", early.err, early.result.Replay)
	case <-time.After(100 * time.Millisecond):
	}
	if err := winnerTx.Commit(); err != nil {
		t.Fatalf("commit winner: %v", err)
	}
	cleanupAgent(t, s, winner.Agent.ID)
	select {
	case replay := <-response:
		if replay.err != nil {
			t.Fatalf("concurrent replay: %v", replay.err)
		}
		if !replay.result.Replay || replay.result.RawToken != "" || replay.result.Agent.ID != winner.Agent.ID || replay.result.Passport.ID != winner.Passport.ID {
			t.Fatalf("concurrent replay fields: replay=%v token_empty=%v agent_match=%v passport_match=%v",
				replay.result.Replay, replay.result.RawToken == "", replay.result.Agent.ID == winner.Agent.ID, replay.result.Passport.ID == winner.Passport.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent replay did not unblock after winner commit")
	}
}

func TestRegisterEnrolment_IsProjectScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "enrolment-project-a")
	projectB := createProject(t, s, "enrolment-project-b")
	inA := testEnrolmentRegistrationInput(projectA)
	inB := testEnrolmentRegistrationInput(projectB)

	gotA, err := s.RegisterEnrolment(ctx, inA)
	if err != nil {
		t.Fatalf("RegisterEnrolment A: %v", err)
	}
	cleanupAgent(t, s, gotA.Agent.ID)
	gotB, err := s.RegisterEnrolment(ctx, inB)
	if err != nil {
		t.Fatalf("RegisterEnrolment B: %v", err)
	}
	cleanupAgent(t, s, gotB.Agent.ID)
	if gotA.Agent.ID == gotB.Agent.ID || gotA.Passport.ID == gotB.Passport.ID {
		t.Fatalf("same idempotency key crossed projects: A=%s/%s B=%s/%s", gotA.Agent.ID, gotA.Passport.ID, gotB.Agent.ID, gotB.Passport.ID)
	}

	tx, err := s.BeginProjectTx(ctx, projectA)
	if err != nil {
		t.Fatalf("BeginProjectTx A: %v", err)
	}
	defer tx.Rollback()
	var visible int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_enrolments WHERE project_id = $1 AND idempotency_key = $2`, projectA, inA.IdempotencyKey).Scan(&visible); err != nil {
		t.Fatalf("query project-scoped enrolments: %v", err)
	}
	if visible != 1 {
		t.Fatalf("project A visible enrolments = %d, want 1", visible)
	}
}

func TestAgentEnrolmentsRLSHidesProjectBWhileScopedToProjectA(t *testing.T) {
	ownerStore := testStore(t)
	lockIdentityRLSFixture(t, ownerStore.db)
	ctx := context.Background()
	projectA := createProject(t, ownerStore, "enrolment-rls-a")
	projectB := createProject(t, ownerStore, "enrolment-rls-b")
	inputA := testEnrolmentRegistrationInput(projectA)
	inputB := testEnrolmentRegistrationInput(projectB)
	resultA, err := ownerStore.RegisterEnrolment(ctx, inputA)
	if err != nil {
		t.Fatalf("register project A enrolment: %v", err)
	}
	cleanupAgent(t, ownerStore, resultA.Agent.ID)
	resultB, err := ownerStore.RegisterEnrolment(ctx, inputB)
	if err != nil {
		t.Fatalf("register project B enrolment: %v", err)
	}
	cleanupAgent(t, ownerStore, resultB.Agent.ID)

	const roleName = "identity_enrolment_rls_test_user"
	const rolePassword = "identity_enrolment_rls_test_password"
	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(fmt.Sprintf("REVOKE ALL PRIVILEGES ON TABLE agent_enrolments FROM %s", roleName))
		_, _ = ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName))
	})
	if _, err := ownerStore.db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %s", roleName)); err != nil {
		t.Fatalf("drop pre-existing RLS role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("create RLS role: %v", err)
	}
	if _, err := ownerStore.db.Exec(fmt.Sprintf("GRANT SELECT ON TABLE agent_enrolments TO %s", roleName)); err != nil {
		t.Fatalf("grant enrolment select: %v", err)
	}
	cfg := types.LoadConfig()
	databaseURL, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	databaseURL.User = url.UserPassword(roleName, rolePassword)
	restrictedDB, err := sql.Open("postgres", databaseURL.String())
	if err != nil {
		t.Fatalf("open restricted database: %v", err)
	}
	t.Cleanup(func() { _ = restrictedDB.Close() })
	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("ping restricted database: %v", err)
	}
	tx, err := restrictedDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin restricted transaction: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectA); err != nil {
		t.Fatalf("scope restricted transaction to project A: %v", err)
	}
	var found string
	err = tx.QueryRowContext(ctx, `SELECT project_id FROM agent_enrolments WHERE project_id = $1 AND idempotency_key = $2`, projectB, inputB.IdempotencyKey).Scan(&found)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project B enrolment visible while scoped A: err=%v found=%q", err, found)
	}
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM agent_enrolments WHERE project_id = $1 AND idempotency_key = $2`, projectA, inputA.IdempotencyKey).Scan(&found); err != nil {
		t.Fatalf("project A enrolment hidden under own scope: %v", err)
	}
	if found != projectA {
		t.Fatalf("visible project = %q, want project A", found)
	}
}

func TestRegisterEnrolment_ControlledReissueSupersedesTokenOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "enrolment-reissue")
	in := testEnrolmentRegistrationInput(projectID)

	first, err := s.RegisterEnrolment(ctx, in)
	if err != nil {
		t.Fatalf("RegisterEnrolment first: %v", err)
	}
	cleanupAgent(t, s, first.Agent.ID)

	in.Reissue = true
	reissued, err := s.RegisterEnrolment(ctx, in)
	if err != nil {
		t.Fatalf("RegisterEnrolment reissue: %v", err)
	}
	if reissued.Agent.ID != first.Agent.ID || reissued.Passport.ID != first.Passport.ID || reissued.RawToken == "" || reissued.RawToken == first.RawToken || !reissued.Reissued {
		t.Fatalf("reissue fields: agent_match=%v passport_match=%v token_empty=%v token_changed=%v reissued=%v",
			reissued.Agent.ID == first.Agent.ID, reissued.Passport.ID == first.Passport.ID,
			reissued.RawToken == "", reissued.RawToken != first.RawToken, reissued.Reissued)
	}
	if _, err := s.WhoAmI(ctx, projectID, first.RawToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("superseded token WhoAmI error = %v, want ErrInvalidToken", err)
	}
	if _, err := s.WhoAmI(ctx, projectID, reissued.RawToken); err != nil {
		t.Fatalf("reissued token WhoAmI: %v", err)
	}
	if _, err := s.RegisterEnrolment(ctx, in); !errors.Is(err, ErrEnrolmentReissueExhausted) {
		t.Fatalf("second reissue error = %v, want ErrEnrolmentReissueExhausted", err)
	}

	var requestHash, tokenHash string
	if err := s.db.QueryRowContext(ctx, `SELECT request_hash FROM agent_enrolments WHERE project_id = $1 AND idempotency_key = $2`, projectID, in.IdempotencyKey).Scan(&requestHash); err != nil {
		t.Fatalf("read stored request hash: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM agent_tokens WHERE agent_id = $1 AND expires_at > now()`, first.Agent.ID).Scan(&tokenHash); err != nil {
		t.Fatalf("read active token hash: %v", err)
	}
	if requestHash == first.RawToken || requestHash == reissued.RawToken || tokenHash == first.RawToken || tokenHash == reissued.RawToken {
		t.Fatal("Fabric persisted a raw token in enrolment or agent_tokens storage")
	}
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

	agent, _, token, err := s.Register(ctx, projectID, []string{"event.publish", "kb.write"}, "harley", "claude", []string{"code_review", "write_kb"}, nil, nil)
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

	agent, _, token, err := s.Register(ctx, projectID, []string{}, "harley", "codex", nil, nil, nil)
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

	if _, _, _, err := s.Register(ctx, "", []string{"kb.read"}, "harley", "codex", nil, nil, nil); !errors.Is(err, ErrInvalidScope) {
		t.Errorf("Register(empty project) error = %v, want ErrInvalidScope", err)
	}
	if _, _, _, err := s.Register(ctx, projectID, nil, "harley", "codex", nil, nil, nil); !errors.Is(err, ErrInvalidScope) {
		t.Errorf("Register(nil permissions) error = %v, want ErrInvalidScope", err)
	}
}

func TestRegister_SetsProjectContextBeforeRestrictedRoleWrites(t *testing.T) {
	ownerStore := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, ownerStore, "restricted-register")
	lockConn, err := ownerStore.db.Conn(ctx)
	if err != nil {
		t.Fatalf("open fixture lock: %v", err)
	}
	if _, err := lockConn.ExecContext(ctx, `SELECT pg_advisory_lock(867530913)`); err != nil {
		t.Fatalf("lock fixture: %v", err)
	}
	const role = "wormhole_identity_register_test"
	t.Cleanup(func() {
		_, _ = ownerStore.db.Exec(`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM ` + role)
		_, _ = ownerStore.db.Exec(`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM ` + role)
		_, _ = ownerStore.db.Exec(`DROP ROLE IF EXISTS ` + role)
		_, _ = lockConn.ExecContext(ctx, `SELECT pg_advisory_unlock(867530913)`)
		lockConn.Close()
	})
	_, _ = ownerStore.db.Exec(`DROP ROLE IF EXISTS ` + role)
	if _, err := ownerStore.db.Exec(`CREATE ROLE ` + role + ` LOGIN PASSWORD 'wormhole_identity_register_test'`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := ownerStore.db.Exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + role); err != nil {
		t.Fatalf("grant tables: %v", err)
	}
	if _, err := ownerStore.db.Exec(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + role); err != nil {
		t.Fatalf("grant sequences: %v", err)
	}

	cfg := types.LoadConfig()
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	u.User = url.UserPassword(role, "wormhole_identity_register_test")
	restrictedDB, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatalf("open restricted db: %v", err)
	}
	defer restrictedDB.Close()
	if err := restrictedDB.PingContext(ctx); err != nil {
		t.Fatalf("ping restricted db: %v", err)
	}
	agent, _, _, err := NewStore(restrictedDB).Register(ctx, projectID, []string{"task.create"}, "restricted", "test", nil, nil, nil)
	if err != nil {
		t.Fatalf("restricted Register: %v", err)
	}
	cleanupAgent(t, ownerStore, agent.ID)
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

	agent, _, token, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
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

	agentA, _, tokenA, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", []string{"a"}, nil, nil)
	if err != nil {
		t.Fatalf("Register A: %v", err)
	}
	cleanupAgent(t, s, agentA.ID)

	agentB, _, tokenB, err := s.Register(ctx, projectID, []string{"kb.write"}, "harley", "codex", []string{"b"}, nil, nil)
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

	agent, _, tokenA, err := s.Register(ctx, projectA, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
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

// TestWhoAmI_EmptyProjectIDResolvesFromToken proves whoami is reachable
// from a spec-compliant MCP client that omits project_id, per RFC-0001 §9
// (whoami's tool schema exempts project_id from "required" — see
// internal/mcp/jsonrpc.go's buildInputSchema). An empty projectID must
// resolve the token's own project from agent_tokens instead of being
// rejected outright (github.com/H4RL33/wormhole/issues/11).
func TestWhoAmI_EmptyProjectIDResolvesFromToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "whoami-empty-project-id")

	agent, _, token, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	scope, err := s.WhoAmI(ctx, "", token)
	if err != nil {
		t.Fatalf("WhoAmI(\"\", token) error = %v, want nil", err)
	}
	if scope.Agent.ID != agent.ID {
		t.Errorf("WhoAmI(\"\", token).Agent.ID = %q, want %q", scope.Agent.ID, agent.ID)
	}
	if scope.ProjectID != projectID {
		t.Errorf("WhoAmI(\"\", token).ProjectID = %q, want %q (resolved from agent_tokens, not caller-supplied)", scope.ProjectID, projectID)
	}
}

// TestRegister_TokenHashNotReversible: the raw token must never be
// recoverable from storage — only its hash is persisted, and the hash
// must differ from the raw value (RFC-0001 §13).
func TestRegister_TokenHashNotReversible(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "hash")

	agent, _, token, err := s.Register(ctx, projectID, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
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

// TestIssuePassport_StandaloneRoundTrip covers the public, non-registration
// passport path: it must retain the declared repository and role scope and
// record the issuance in the append-only audit trail.
func TestIssuePassport_StandaloneRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "standalone-passport-a")
	projectB := createProject(t, s, "standalone-passport-b")

	agent, _, _, err := s.Register(ctx, projectA, []string{"kb.read"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	got, err := s.IssuePassport(ctx, agent.ID, projectB, []string{"github.com/H4RL33/wormhole"}, []string{"reviewer"})
	if err != nil {
		t.Fatalf("IssuePassport: %v", err)
	}
	if got.AgentID != agent.ID || got.ProjectID != projectB {
		t.Fatalf("IssuePassport scope = agent %q project %q, want agent %q project %q", got.AgentID, got.ProjectID, agent.ID, projectB)
	}
	if !reflect.DeepEqual(got.Repositories, []string{"github.com/H4RL33/wormhole"}) || !reflect.DeepEqual(got.Roles, []string{"reviewer"}) {
		t.Fatalf("IssuePassport scope = repositories %v roles %v, want declared values", got.Repositories, got.Roles)
	}

	entries, err := s.ListAuditTrail(ctx, agent.ID, projectB)
	if err != nil {
		t.Fatalf("ListAuditTrail: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != ActionPassportIssued {
		t.Fatalf("project-B audit entries = %+v, want one %q entry", entries, ActionPassportIssued)
	}
}

func TestIssuePassport_RequiresAgentAndProjectScope(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "passport-scope")

	for _, tt := range []struct {
		name, agentID, projectID string
	}{
		{name: "empty agent", projectID: projectID},
		{name: "empty project", agentID: "00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.IssuePassport(ctx, tt.agentID, tt.projectID, nil, nil); !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("IssuePassport(%q, %q) error = %v, want ErrInvalidScope", tt.agentID, tt.projectID, err)
			}
		})
	}
}

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

func TestIssueToken_RequiresCompleteScope(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "token-scope")

	for _, tt := range []struct {
		name, agentID, projectID string
		permissions              []string
	}{
		{name: "empty agent", projectID: projectID, permissions: []string{}},
		{name: "empty project", agentID: "00000000-0000-0000-0000-000000000000", permissions: []string{}},
		{name: "nil permissions", agentID: "00000000-0000-0000-0000-000000000000", projectID: projectID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.IssueToken(ctx, tt.agentID, tt.projectID, tt.permissions); !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("IssueToken error = %v, want ErrInvalidScope", err)
			}
		})
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

func TestStandaloneAuditOperationsAreScopedToProject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "standalone-audit-a")
	projectB := createProject(t, s, "standalone-audit-b")

	agent, _, _, err := s.Register(ctx, projectA, []string{}, "owner", "model", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	entry, err := s.RecordAction(ctx, agent.ID, projectA, "coverage.checked")
	if err != nil {
		t.Fatalf("RecordAction: %v", err)
	}
	entriesA, err := s.ListAuditTrail(ctx, agent.ID, projectA)
	if err != nil {
		t.Fatalf("ListAuditTrail(projectA): %v", err)
	}
	entriesB, err := s.ListAuditTrail(ctx, agent.ID, projectB)
	if err != nil {
		t.Fatalf("ListAuditTrail(projectB): %v", err)
	}
	if entry.ProjectID != projectA || len(entriesA) == 0 || len(entriesB) != 0 {
		t.Fatalf("standalone audit scope: entry=%+v projectA=%+v projectB=%+v", entry, entriesA, entriesB)
	}
}

// TestWhoAmI_ExpiredTokenRejected covers the expiry half of RFC-0001 §13's
// unforgeable-identity guarantee: a token past its expires_at must be
// rejected the same way a forged one is (ErrInvalidToken, no distinct
// error), never resolved to a live scope.
func TestWhoAmI_ExpiredTokenRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "expired-token")

	agent, _, token, err := s.Register(ctx, projectID, []string{"event.publish"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	if _, err := s.db.ExecContext(ctx,
		`UPDATE agent_tokens SET expires_at = now() - interval '1 hour' WHERE agent_id = $1`,
		agent.ID,
	); err != nil {
		t.Fatalf("backdate token expiry: %v", err)
	}

	if _, err := s.WhoAmI(ctx, projectID, token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("WhoAmI with expired token: got err %v, want ErrInvalidToken", err)
	}
}

// TestWhoAmI_ReturnsPassportRoles covers Chapter 7 Task 1: WhoAmI must
// surface the calling agent's passport role tags on AuthenticatedScope, so
// downstream consumers (task-list role filtering) can pick a default role
// without a separate passport lookup.
func TestWhoAmI_ReturnsPassportRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "whoami-roles")

	agent, _, rawToken, err := s.Register(ctx, projectID,
		[]string{"task.read"}, "harley", "claude", nil, nil, []string{"backend-engineer"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	scope, err := s.WhoAmI(ctx, projectID, rawToken)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if !reflect.DeepEqual(scope.Roles, []string{"backend-engineer"}) {
		t.Fatalf("scope.Roles = %v, want [backend-engineer]", scope.Roles)
	}
}

// TestWhoAmI_ReturnsEmptyRolesWhenNoneSet covers the no-roles-set edge case:
// scope.Roles must be empty, not nil-panic or error, when the passport was
// issued with no role tags.
func TestWhoAmI_ReturnsEmptyRolesWhenNoneSet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "whoami-no-roles")

	agent, _, rawToken, err := s.Register(ctx, projectID,
		[]string{"task.read"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	scope, err := s.WhoAmI(ctx, projectID, rawToken)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if len(scope.Roles) != 0 {
		t.Fatalf("scope.Roles = %v, want empty", scope.Roles)
	}
}
