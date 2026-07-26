package identity

import (
	"context"
	"testing"
)

func TestIdentityStoreOperationsFailClosedWhenDatabaseIsUnavailable(t *testing.T) {
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "00000000-0000-0000-0000-000000000001"
	checks := []struct {
		name string
		call func() error
	}{
		{"begin project", func() error { _, err := s.BeginProjectTx(ctx, id); return err }},
		{"begin bootstrap", func() error { _, err := s.BeginBootstrapSnapshotTx(ctx, id); return err }},
		{"register", func() error {
			_, _, _, err := s.Register(ctx, id, []string{}, "owner", "model", []string{}, []string{}, []string{})
			return err
		}},
		{"register enrolment", func() error { _, err := s.RegisterEnrolment(ctx, testEnrolmentRegistrationInput(id)); return err }},
		{"issue passport", func() error { _, err := s.IssuePassport(ctx, id, id, []string{}, []string{}); return err }},
		{"record action", func() error { _, err := s.RecordAction(ctx, id, id, "read"); return err }},
		{"list audit", func() error { _, err := s.ListAuditTrail(ctx, id, id); return err }},
		{"issue token", func() error { _, err := s.IssueToken(ctx, id, id, []string{}); return err }},
		{"who am i", func() error { _, err := s.WhoAmI(ctx, id, "token"); return err }},
		{"create viewer key", func() error { _, _, err := s.CreateViewerKey(ctx, id, "viewer"); return err }},
		{"resolve viewer key", func() error {
			_, err := s.ResolveViewerKey(ctx, id, "whv_00000000000000000000000000000000")
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("operation succeeded with a closed database")
			}
		})
	}
}
