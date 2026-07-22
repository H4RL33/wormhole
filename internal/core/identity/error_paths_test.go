package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestIdentityOperationsPropagateCanceledContext(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	operations := map[string]func() error{
		"register": func() error {
			_, _, _, err := s.Register(ctx, uuid.NewString(), []string{}, "owner", "model", nil, nil, nil)
			return err
		},
		"issue passport": func() error {
			_, err := s.IssuePassport(ctx, uuid.NewString(), uuid.NewString(), nil, nil)
			return err
		},
		"issue token": func() error {
			_, err := s.IssueToken(ctx, uuid.NewString(), uuid.NewString(), []string{})
			return err
		},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestRegisterInTxPropagatesCanceledContext(t *testing.T) {
	s := testStore(t)
	projectID := createProject(t, s, "register-in-tx-canceled")
	tx, err := s.BeginProjectTx(context.Background(), projectID)
	if err != nil {
		t.Fatalf("BeginProjectTx: %v", err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err = s.RegisterInTx(ctx, tx, projectID, []string{}, "owner", "model", nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RegisterInTx error = %v, want context.Canceled", err)
	}
}

func TestIdentityReadsPropagateCanceledContext(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.ListAuditTrail(ctx, uuid.NewString(), uuid.NewString()); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListAuditTrail error = %v, want context.Canceled", err)
	}
	if _, err := s.WhoAmI(ctx, uuid.NewString(), "token"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WhoAmI error = %v, want context.Canceled", err)
	}
}

func TestIssuePassportUnknownAgentRollsBackAudit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "passport-unknown-agent")
	missingAgentID := uuid.NewString()

	_, err := s.IssuePassport(ctx, missingAgentID, projectID, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "insert passport") {
		t.Fatalf("IssuePassport error = %v, want wrapped insert error", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE agent_id = $1`, missingAgentID).Scan(&count); err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit entry count = %d, want 0", count)
	}
}

func TestIssueTokenUnknownAgentRollsBackAudit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "token-unknown-agent")
	missingAgentID := uuid.NewString()

	_, err := s.IssueToken(ctx, missingAgentID, projectID, []string{"kb.read"})
	if err == nil || !strings.Contains(err.Error(), "insert token") {
		t.Fatalf("IssueToken error = %v, want wrapped insert error", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE agent_id = $1`, missingAgentID).Scan(&count); err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit entry count = %d, want 0", count)
	}
}

func TestRecordActionPersistsAndPropagatesConstraintErrors(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "record-action")
	agent, _, _, err := s.Register(ctx, projectID, []string{}, "owner", "model", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	entry, err := s.RecordAction(ctx, agent.ID, projectID, "coverage.checked")
	if err != nil {
		t.Fatalf("RecordAction: %v", err)
	}
	if entry.AgentID != agent.ID || entry.ProjectID != projectID || entry.Action != "coverage.checked" || entry.Seq == 0 {
		t.Fatalf("RecordAction entry = %+v", entry)
	}

	if _, err := s.RecordAction(ctx, uuid.NewString(), projectID, "coverage.rejected"); err == nil || !strings.Contains(err.Error(), "insert audit entry") {
		t.Fatalf("RecordAction unknown agent error = %v, want wrapped constraint error", err)
	}
}

func TestWhoAmIRejectsCorruptStoredScopeJSON(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantErr    string
		updateStmt string
	}{
		{name: "capabilities", wantErr: "unmarshal capabilities", updateStmt: `UPDATE agents SET capabilities = '"broken"'::jsonb WHERE id = $1`},
		{name: "roles", wantErr: "unmarshal roles", updateStmt: `UPDATE passports SET roles = '"broken"'::jsonb WHERE agent_id = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			projectID := createProject(t, s, "corrupt-whoami-"+tc.name)
			agent, _, token, err := s.Register(ctx, projectID, []string{"kb.read"}, "owner", "model", nil, nil, []string{"reviewer"})
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			cleanupAgent(t, s, agent.ID)
			if _, err := s.db.ExecContext(ctx, tc.updateStmt, agent.ID); err != nil {
				t.Fatalf("corrupt %s: %v", tc.name, err)
			}

			_, err = s.WhoAmI(ctx, projectID, token)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("WhoAmI error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
