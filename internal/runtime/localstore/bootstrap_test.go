package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func localBootstrapSnapshot(namespace, channelID string) types.BootstrapOrgConfigV1 {
	now := time.Date(2026, 7, 25, 12, 0, 0, 123, time.UTC)
	return types.BootstrapOrgConfigV1{
		SchemaVersion: types.BootstrapSchemaVersionV1,
		Project:       types.BootstrapProjectV1{ID: namespace, Name: "project", Owner: "owner", CreatedAt: now},
		Identity: types.BootstrapIdentityV1{
			Agent:       types.BootstrapAgentV1{ID: "agent-1", Owner: "owner", Model: "model", Capabilities: []string{"code"}, CreatedAt: now},
			Passport:    types.BootstrapPassportV1{ID: "passport-1", AgentID: "agent-1", ProjectID: namespace, Repositories: []string{"repo"}, Roles: []string{"builder"}, IssuedAt: now},
			Permissions: []string{"task.list"},
		},
		Channels:                    []types.BootstrapChannelV1{{ID: channelID, ProjectID: namespace, Name: "general", CreatedAt: now}},
		Events:                      []types.BootstrapEventV1{{ID: "event-1", ProjectID: namespace, ChannelID: channelID, AgentID: "agent-1", EventType: "message.posted", Payload: json.RawMessage(`{}`), CreatedAt: now}},
		Tasks:                       []types.BootstrapTaskV1{{ID: "task-1", ProjectID: namespace, Title: "task", Status: "todo", CreatedAt: now, UpdatedAt: now}},
		KB:                          types.BootstrapKBV1{Articles: []types.BootstrapArticleV1{{ID: "kb-1", ProjectID: namespace, Title: "article", Body: "body", Frontmatter: json.RawMessage(`{}`), AuthorAgentID: "agent-1", CreatedAt: now, UpdatedAt: now}}},
		IntegrationManifestMetadata: json.RawMessage(`null`),
	}
}

func TestApplyBootstrapAtomicallyCommitsCompleteSnapshotAndReady(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	attempt, _, err := store.ResolveEnrolmentAttempt(ctx, EnrolmentAttemptRecord{
		ProjectID: "ns-1", IdempotencyKey: "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1", RequestHash: strings.Repeat("a", 64),
		State: "credentials_persisted", CredentialProfile: "profile", AgentID: "agent-1", PassportID: "passport-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateEnrolmentAttempt(ctx, attempt, "credentials_persisted", "agent-1", "passport-1", false); err != nil {
		t.Fatal(err)
	}
	attempt.AgentID = "agent-1"
	attempt.PassportID = "passport-1"
	snapshot := localBootstrapSnapshot("ns-1", "channel-1")
	timestamp := time.Date(2026, 7, 25, 12, 1, 0, 456, time.UTC)
	if err := store.ApplyBootstrap(ctx, "ns-1", snapshot, timestamp, &attempt); err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	for table, want := range map[string]int{"projects": 1, "agents": 1, "passports": 1, "auth_scopes": 1, "channels": 1, "events": 1, "tasks": 1, "kb_articles": 1, "bootstrap_metadata": 1} {
		var got int
		if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE namespace_id = ?`, "ns-1").Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
	var state string
	var terminal int
	if err := store.DB().QueryRowContext(ctx, `SELECT state, terminal FROM enrolment_attempts WHERE project_id = ? AND idempotency_key = ?`, "ns-1", attempt.IdempotencyKey).Scan(&state, &terminal); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || terminal != 1 {
		t.Fatalf("attempt state=%q terminal=%d, want ready/1", state, terminal)
	}
	if err := store.ValidateReadyCheckpoint(ctx, "ns-1", "agent-1", "passport-1", "profile"); err != nil {
		t.Fatalf("ValidateReadyCheckpoint after atomic bootstrap: %v", err)
	}
	if err := store.ValidateReadyCheckpoint(ctx, "ns-1", "agent-1", "passport-1", "other-profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong profile checkpoint error = %v, want ErrNotFound", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE enrolment_attempts SET state = 'recovery_required', terminal = 0 WHERE project_id = ?`, "ns-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateReadyCheckpoint(ctx, "ns-1", "agent-1", "passport-1", "profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-ready attempt checkpoint error = %v, want ErrNotFound", err)
	}
}

func TestValidateReadyCheckpointRejectsMissingOrMismatchedIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.ValidateReadyCheckpoint(ctx, "ns-1", "agent-1", "passport-1", "profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty checkpoint error = %v, want ErrNotFound", err)
	}
	if err := store.ApplyBootstrap(ctx, "ns-1", localBootstrapSnapshot("ns-1", "channel-1"), time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ namespace, agent, passport string }{
		{namespace: "other", agent: "agent-1", passport: "passport-1"},
		{namespace: "ns-1", agent: "other", passport: "passport-1"},
		{namespace: "ns-1", agent: "agent-1", passport: "other"},
	} {
		if err := store.ValidateReadyCheckpoint(ctx, test.namespace, test.agent, test.passport, "profile"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("mismatched checkpoint (%q,%q,%q) error = %v, want ErrNotFound", test.namespace, test.agent, test.passport, err)
		}
	}
	if err := store.ValidateReadyCheckpoint(ctx, "ns-1", "agent-1", "passport-1", "profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot without ready enrolment attempt error = %v, want ErrNotFound", err)
	}
}

func TestApplyBootstrapRejectsCrossNamespaceBeforeMutation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := localBootstrapSnapshot("ns-b", "channel-b")
	if err := store.ApplyBootstrap(context.Background(), "ns-a", snapshot, time.Now().UTC(), nil); err == nil {
		t.Fatal("ApplyBootstrap accepted a cross-namespace snapshot")
	}
	var projects int
	if err := store.DB().QueryRow(`SELECT count(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatalf("cross-namespace bootstrap wrote %d project rows", projects)
	}
}

func TestApplyBootstrapRollsBackAllRowsOnMidTransactionCollision(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().Exec(`INSERT INTO channels (id, namespace_id, name, created_at) VALUES (?, ?, ?, ?)`, "shared-channel", "ns-b", "other", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	snapshot := localBootstrapSnapshot("ns-a", "shared-channel")
	err = store.ApplyBootstrap(context.Background(), "ns-a", snapshot, time.Now().UTC(), nil)
	if !errors.Is(err, ErrNamespaceCollision) {
		t.Fatalf("ApplyBootstrap error = %v, want ErrNamespaceCollision", err)
	}
	for _, table := range []string{"projects", "agents", "passports", "auth_scopes", "tasks", "events", "kb_articles", "bootstrap_metadata"} {
		var got int
		if err := store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE namespace_id = ?`, "ns-a").Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != 0 {
			t.Fatalf("rollback left %d %s rows in ns-a", got, table)
		}
	}
}
