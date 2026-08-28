package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	localActivityProjectID   = "00000000-0000-4000-8000-000000000001"
	localActivityWorkspaceID = "00000000-0000-4000-8000-000000000011"
	localActivityProfileID   = "10000000-0000-4000-8000-000000000001"
	localActivityFabricID    = "20000000-0000-4000-8000-000000000001"
	localActivityRemoteID    = "30000000-0000-4000-8000-000000000001"
	localActivityStreamID    = "40000000-0000-4000-8000-000000000001"
	localActivityAttachID    = "50000000-0000-4000-8000-000000000001"
	localActivityAgentID     = "60000000-0000-4000-8000-000000000001"
	localActivityHumanID     = "70000000-0000-4000-8000-000000000001"
	localActivityChannelID   = "80000000-0000-4000-8000-000000000001"
	localActivityTaskID      = "90000000-0000-4000-8000-000000000001"
	localActivityIDOne       = "a0000000-0000-4000-8000-000000000001"
	localActivityIDTwo       = "a0000000-0000-4000-8000-000000000002"
)

type localActivityFixture struct {
	path   string
	store  *Store
	repo   *ActivityRepo
	route  types.ActivityRouteKey
	policy projectstate.EffectiveActivityPolicyV1
}

func newLocalActivityFixture(t *testing.T, withPolicy bool) localActivityFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	return newLocalActivityFixtureAt(t, path, withPolicy)
}

func newFreshLocalActivityFixtureAtPolicy(t *testing.T, policy projectstate.EffectiveActivityPolicyV1) localActivityFixture {
	t.Helper()
	fixture := newLocalActivityFixture(t, false)
	raw, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	now := sqliteActivityTimestamp(testUTCNow())
	arguments := activityRouteArgs(fixture.route)
	arguments = append(arguments, policy.PolicyVersion, raw, string(digest), policy.TerminalRetentionSeconds, now)
	if _, err := fixture.store.DB().Exec(`INSERT INTO activity_policy_versions
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,
		 canonical_policy_json,policy_digest,terminal_retention_seconds,received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
		t.Fatal(err)
	}
	arguments = activityRouteArgs(fixture.route)
	arguments = append(arguments, policy.PolicyVersion, string(digest), now)
	if _, err := fixture.store.DB().Exec(`INSERT INTO activity_policy_current
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,policy_digest,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
		t.Fatal(err)
	}
	fixture.policy = policy
	return fixture
}

func newLocalActivityFixtureAt(t *testing.T, path string, withPolicy bool) localActivityFixture {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRepo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, workspaceRepo, localActivityProjectID, localActivityWorkspaceID, "/activity-checkout", 101, 201)
	routes := NewFabricRouteRepo(store.DB())
	profile := types.FabricProfile{
		ProfileID: localActivityProfileID, Alias: "activity", FabricInstanceID: localActivityFabricID,
		BaseURL: "https://fabric.example.test", Mode: types.FabricModePrivate, CredentialRef: "keyring:activity",
	}
	if err := routes.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	remote := types.FabricBinding{
		Workspace: binding, ProfileID: profile.ProfileID, FabricInstanceID: profile.FabricInstanceID,
		RemoteProjectID: localActivityRemoteID, StreamID: localActivityStreamID,
		AttachmentRef: localActivityAttachID, CanonicalRef: binding.AcceptedRef, Writable: true,
	}
	if err := routes.AttachWorkspace(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	fixture := localActivityFixture{
		path:  path,
		store: store,
		repo:  NewActivityRepo(store.DB()),
		route: types.ActivityRouteKey{
			ProjectID: binding.Scope.ProjectID, WorkspaceID: binding.Scope.WorkspaceID,
			FabricInstanceID: remote.FabricInstanceID, RemoteProjectID: remote.RemoteProjectID,
			StreamID: remote.StreamID, CanonicalRef: remote.CanonicalRef,
		},
		policy: localActivityPolicy(1, 2_592_000),
	}
	if withPolicy {
		if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route, 0, "", fixture.policy); err != nil {
			t.Fatalf("install policy: %v", err)
		}
	}
	return fixture
}

func localActivityPolicy(version, retention int64) projectstate.EffectiveActivityPolicyV1 {
	return projectstate.EffectiveActivityPolicyV1{
		SchemaVersion: 1, PolicyVersion: version,
		OrdinaryMaxAgeSeconds: 2_592_000, OrdinaryMaxRows: 10_000,
		TerminalDefaultAgeSeconds: 2_592_000, TerminalMaximumAgeSeconds: 31_536_000,
		TerminalRetentionSeconds: retention,
	}
}

func localActivityActor(at time.Time) types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: localActivityAgentID, AccountableHumanID: localActivityHumanID,
		SessionID: "session-activity", HarnessName: "codex", HarnessVersion: "1.0",
		ModelName: "gpt", ModelVersion: "5.6", Assurance: types.AssurancePrivateAuthenticated, OccurredAt: at,
	}
}

func localOrdinaryActivity(id, note string, at time.Time) projectstate.ActivityV1 {
	actor := localActivityActor(at)
	payload := json.RawMessage(fmt.Sprintf(`{"from_status":"wip","task_id":%q,"to_status":"done"}`, localActivityTaskID))
	return projectstate.ActivityV1{
		SchemaVersion: 1, ID: id, Class: projectstate.ActivityOrdinaryV1, Actor: actor,
		Event: &projectstate.ActivityEventProjectionV1{
			ChannelID: localActivityChannelID, ActorID: actor.AgentID, EventType: "task.status_changed",
			Payload: payload, Note: &note, CreatedAt: at,
		},
		CreatedAt: at,
	}
}

func localLifecycleActivity(id string, kind projectstate.ActivityLifecycleKindV1, reference string, at time.Time) projectstate.ActivityV1 {
	return projectstate.ActivityV1{
		SchemaVersion: 1, ID: id, Class: projectstate.ActivityLifecycleV1, Actor: localActivityActor(at),
		Lifecycle: &projectstate.ActivityLifecycleProjectionV1{Kind: kind, ReferenceID: reference}, CreatedAt: at,
	}
}

func localReceipt(t *testing.T, record ActivityRecord, sequence int64, acceptedAt time.Time) projectstate.ActivityReceiptV1 {
	t.Helper()
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: record.Key.ActivityID, ActivityDigest: record.ActivityDigest,
		Sequence: sequence, PolicyVersion: record.PolicyVersion, PolicyDigest: record.PolicyDigest,
		AcceptedAt: acceptedAt,
	}
	if _, err := projectstate.CanonicalActivityReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func localPullDelivery(t *testing.T, activity projectstate.ActivityV1, source types.WorkspaceID, sequence int64, policy projectstate.EffectiveActivityPolicyV1) ActivityPullDelivery {
	t.Helper()
	activityJSON, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	activityDigest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: activity.ID, ActivityDigest: activityDigest, Sequence: sequence,
		PolicyVersion: policy.PolicyVersion, PolicyDigest: policyDigest, AcceptedAt: testUTCNow().Add(time.Duration(sequence) * time.Second),
	}
	receiptJSON, err := projectstate.CanonicalActivityReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return ActivityPullDelivery{SourceWorkspaceID: source, ActivityJSON: activityJSON, ActivityDigest: activityDigest, ReceiptJSON: receiptJSON}
}

func localPullBatch(t *testing.T, policy projectstate.EffectiveActivityPolicyV1, expected, next int64, hasMore bool, deliveries ...ActivityPullDelivery) ActivityPullBatch {
	t.Helper()
	policyJSON, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	batch := ActivityPullBatch{PolicyJSON: policyJSON, ExpectedAfter: expected, NextSequence: next, HasMore: hasMore, Deliveries: deliveries}
	if len(deliveries) != 0 {
		digest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		batch.HistoricalPolicies = []ActivityPolicyEvidence{{
			Route: localActivityTestRoute(), PolicyJSON: append([]byte(nil), policyJSON...), PolicyDigest: digest,
		}}
	}
	return batch
}

func localActivityTestRoute() types.ActivityRouteKey {
	return types.ActivityRouteKey{
		ProjectID: localActivityProjectID, WorkspaceID: localActivityWorkspaceID,
		FabricInstanceID: localActivityFabricID, RemoteProjectID: localActivityRemoteID,
		StreamID: localActivityStreamID, CanonicalRef: "refs/heads/main",
	}
}

func activityTableCounts(t *testing.T, store *Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{
		"activity_policy_versions", "activity_policy_current", "activity_ledger", "activity_ingress_receipts",
		"activity_lifecycle", "activity_outbound_queue", "activity_cursors", "activity_promotion_receipts",
	} {
		var count int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func localActivitySemanticSnapshot(t *testing.T, db *sql.DB) map[string][][]string {
	t.Helper()
	tables := []string{
		"activity_policy_versions", "activity_policy_current", "activity_ledger", "activity_ingress_receipts",
		"activity_lifecycle", "activity_outbound_queue", "activity_cursors", "activity_promotion_receipts",
	}
	snapshot := make(map[string][][]string, len(tables))
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("snapshot %s columns: %v", table, err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatalf("snapshot %s scan: %v", table, err)
			}
			semantic := make([]string, len(values))
			for index, value := range values {
				switch typed := value.(type) {
				case nil:
					semantic[index] = "<null>"
				case []byte:
					semantic[index] = string(typed)
				case time.Time:
					semantic[index] = typed.UTC().Format(time.RFC3339Nano)
				default:
					semantic[index] = fmt.Sprint(typed)
				}
			}
			snapshot[table] = append(snapshot[table], semantic)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("snapshot %s iterate: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("snapshot %s close: %v", table, err)
		}
		if snapshot[table] == nil {
			snapshot[table] = [][]string{}
		}
	}
	return snapshot
}
