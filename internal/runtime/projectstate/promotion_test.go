package projectstate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	promotionSourceActorID         = "61000000-0000-4000-8000-000000000001"
	promotionSourceHumanID         = "71000000-0000-4000-8000-000000000001"
	promotionSourceChannelID       = "81000000-0000-4000-8000-000000000001"
	promotionSourceTaskID          = "91000000-0000-4000-8000-000000000001"
	promotionSourceActivityID      = "a1000000-0000-4000-8000-000000000001"
	promotionSourceWorkspaceID     = types.WorkspaceID("b1000000-0000-4000-8000-000000000001")
	promotionGeneratedEventID      = "c1000000-0000-4000-8000-000000000001"
	promotionGeneratedOperationID  = "d1000000-0000-4000-8000-000000000001"
	promotionProfileID             = "11000000-0000-4000-8000-000000000001"
	promotionFabricID              = "21000000-0000-4000-8000-000000000001"
	promotionRemoteProjectID       = "31000000-0000-4000-8000-000000000001"
	promotionStreamID              = "41000000-0000-4000-8000-000000000001"
	promotionAttachmentID          = "51000000-0000-4000-8000-000000000001"
	promotionGoldenSourceDigest    = state.Digest("sha256:2213c6591a66ce78d0627c8656b8704f610f230203d6f9dff01223c97e11cf82")
	promotionGoldenViewDigest      = state.Digest("sha256:3313645a05fd84d5e1509beb33b12722b26d7c601f315d30a927a2b045f301c4")
	promotionGoldenOperationDigest = state.Digest("sha256:0031f492983b30d3d1f00e2054aa69ad6fa31d6cb1f9092f713c3d0281d86673")
	promotionGoldenExtensionJSON   = `{"source_activity_digest":"sha256:2213c6591a66ce78d0627c8656b8704f610f230203d6f9dff01223c97e11cf82","source_activity_id":"a1000000-0000-4000-8000-000000000001"}`
	promotionGoldenOperationJSON   = `{"schema_version":1,"id":"d1000000-0000-4000-8000-000000000001","kind":"put_record","expected_view_digest":"sha256:3313645a05fd84d5e1509beb33b12722b26d7c601f315d30a927a2b045f301c4","actor":{"actor_kind":"human","human_principal_id":"72000000-0000-4000-8000-000000000001","assurance":"local","occurred_at":"2026-08-28T14:00:00.987654321Z"},"put_record":{"record":{"event":{"schema_version":1,"kind":"event","id":"c1000000-0000-4000-8000-000000000001","channel_id":"81000000-0000-4000-8000-000000000001","actor_id":"61000000-0000-4000-8000-000000000001","event_type":"task.status_changed","payload":{"from_status":"wip","task_id":"91000000-0000-4000-8000-000000000001","to_status":"done"},"note":"source note","created_at":"2026-08-28T13:00:00.123456789Z","extensions":{"dev.wormhole.promotion":{"schema_version":1,"data":{"source_activity_digest":"sha256:2213c6591a66ce78d0627c8656b8704f610f230203d6f9dff01223c97e11cf82","source_activity_id":"a1000000-0000-4000-8000-000000000001"}}}}}}}` + "\n"
)

type promotionFixture struct {
	store    *localstore.Store
	service  *Service
	binding  types.WorkspaceBinding
	route    types.ActivityRouteKey
	policy   state.EffectiveActivityPolicyV1
	activity state.ActivityV1
	digest   state.Digest
	request  PromoteActivityRequest
}

func TestPromotionCopiesExactActivityProjectionAndUsesDistinctPromoter(t *testing.T) {
	fixture := newPromotionFixture(t, true)
	result, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	canonicalOperation, err := state.CanonicalOperation(result.Operation)
	if err != nil {
		t.Fatal(err)
	}
	operationDigest, err := state.DigestCanonicalJSON(result.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.digest != promotionGoldenSourceDigest || fixture.request.ExpectedViewDigest != promotionGoldenViewDigest ||
		operationDigest != promotionGoldenOperationDigest || string(canonicalOperation) != promotionGoldenOperationJSON {
		t.Fatalf("promotion golden changed: source=%s view=%s operation=%s\n%s", fixture.digest,
			fixture.request.ExpectedViewDigest, operationDigest, canonicalOperation)
	}
	projection := fixture.activity.Event
	if result.Event.ChannelID != projection.ChannelID || result.Event.ActorID != projection.ActorID ||
		result.Event.EventType != projection.EventType || !bytes.Equal(result.Event.Payload, projection.Payload) ||
		!equalPromotionNote(result.Event.Note, projection.Note) || !result.Event.CreatedAt.Equal(projection.CreatedAt) {
		t.Fatalf("promoted event=%+v, want exact projection %+v", result.Event, *projection)
	}
	if result.Event.ActorID == fixture.request.Promoter.PrincipalID() || result.Operation.Actor != fixture.request.Promoter {
		t.Fatalf("promotion attribution event=%q operation=%+v promoter=%+v", result.Event.ActorID, result.Operation.Actor, fixture.request.Promoter)
	}
	if result.Event.ID != promotionGeneratedEventID || result.Operation.ID != promotionGeneratedOperationID ||
		result.Operation.ExpectedViewDigest != fixture.request.ExpectedViewDigest || result.Operation.PutRecord == nil ||
		result.Operation.PutRecord.Record.Event == nil || !reflect.DeepEqual(*result.Operation.PutRecord.Record.Event, result.Event) {
		t.Fatalf("promotion result=%+v", result)
	}
	if len(result.Event.Extensions) != 1 {
		t.Fatalf("promotion extensions=%+v, want sole extension", result.Event.Extensions)
	}
	extension, ok := result.Event.Extensions["dev.wormhole.promotion"]
	if !ok || extension.SchemaVersion != 1 {
		t.Fatalf("promotion extension=%+v", result.Event.Extensions)
	}
	if string(extension.Data) != promotionGoldenExtensionJSON {
		t.Fatalf("promotion extension bytes=%q, want golden %q", extension.Data, promotionGoldenExtensionJSON)
	}
	var data map[string]string
	if err := json.Unmarshal(extension.Data, &data); err != nil {
		t.Fatal(err)
	}
	wantData := map[string]string{"source_activity_id": fixture.activity.ID, "source_activity_digest": string(fixture.digest)}
	if !reflect.DeepEqual(data, wantData) || len(data) != 2 {
		t.Fatalf("promotion extension data=%v, want %v", data, wantData)
	}
	projection.Payload[0] = '['
	*projection.Note = "mutated source"
	if bytes.Equal(result.Event.Payload, projection.Payload) || *result.Event.Note == *projection.Note {
		t.Fatal("promotion result aliases source projection")
	}

	view, err := fixture.service.View(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.Snapshot.Events[result.Event.ID], result.Event) {
		t.Fatalf("composed portable event=%+v, want result %+v", view.Snapshot.Events[result.Event.ID], result.Event)
	}
}

func TestPromotionAtomicallyMarksSourceAndLeavesFabricWithoutPromotionAuthority(t *testing.T) {
	fixture := newPromotionFixture(t, true)
	result, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	var operationRows, receiptRows, lifecycleRows int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=? AND operation_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID, result.Operation.ID).Scan(&operationRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_promotion_receipts WHERE local_project_id=? AND local_workspace_id=? AND source_activity_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID, fixture.activity.ID).Scan(&receiptRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='receipt' AND reference_id=? AND state='confirmed'`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID, fixture.activity.ID, fixture.activity.ID).Scan(&lifecycleRows); err != nil {
		t.Fatal(err)
	}
	if operationRows != 1 || receiptRows != 1 || lifecycleRows != 1 {
		t.Fatalf("atomic promotion rows operation/receipt/lifecycle=%d/%d/%d", operationRows, receiptRows, lifecycleRows)
	}
	for _, table := range []string{"fabric_stream_requests", "fabric_stream_versions", "fabric_activity"} {
		var present int
		err := fixture.store.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present)
		if err != nil || present != 0 {
			t.Fatalf("Gateway database contains Fabric promotion-shaped table %q: present=%d err=%v", table, present, err)
		}
	}
}

func TestPromotionExactRetryReturnsStoredEventAndOperation(t *testing.T) {
	fixture := newPromotionFixture(t, true)
	first, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	var revisionBefore int64
	if err := fixture.store.DB().QueryRow(`SELECT workspace_revision FROM workspace_bindings WHERE project_id=? AND workspace_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID).Scan(&revisionBefore); err != nil {
		t.Fatal(err)
	}
	allocations := 0
	fixture.service.workspace.newEventID = func() (string, error) {
		allocations++
		return "e1000000-0000-4000-8000-000000000001", nil
	}
	fixture.service.workspace.newOperationID = func() (string, error) {
		allocations++
		return "e1000000-0000-4000-8000-000000000002", nil
	}
	fixture.service.workspace.now = func() time.Time {
		t.Fatal("exact replay read a new promotion time")
		return time.Time{}
	}
	replay, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if allocations != 0 || !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay=%+v allocations=%d, want stored %+v and zero", replay, allocations, first)
	}
	var revisionAfter int64
	if err := fixture.store.DB().QueryRow(`SELECT workspace_revision FROM workspace_bindings WHERE project_id=? AND workspace_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID).Scan(&revisionAfter); err != nil {
		t.Fatal(err)
	}
	if revisionAfter != revisionBefore {
		t.Fatalf("exact replay changed workspace revision from %d to %d", revisionBefore, revisionAfter)
	}
	for name, mutate := range map[string]func(*PromoteActivityRequest){
		"source digest": func(req *PromoteActivityRequest) { req.ExpectedSourceDigest = promotionOtherDigest() },
		"view digest":   func(req *PromoteActivityRequest) { req.ExpectedViewDigest = promotionOtherDigest() },
		"promoter": func(req *PromoteActivityRequest) {
			req.Promoter.HumanPrincipalID = "72000000-0000-4000-8000-000000000002"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := fixture.request
			mutate(&changed)
			if _, err := fixture.service.PromoteActivity(context.Background(), changed); !errors.Is(err, ErrActivityPromotionConflict) {
				t.Fatalf("changed replay error=%v, want ErrActivityPromotionConflict", err)
			}
		})
	}
	if allocations != 0 {
		t.Fatalf("conflicting replay allocated %d IDs", allocations)
	}
}

func TestPromotionRejectsCanonicalSourceDivergenceOnRetryAndUnknownCommit(t *testing.T) {
	for _, corruption := range promotionCanonicalEventCorruptions(t) {
		t.Run(corruption.name+"/retry", func(t *testing.T) {
			fixture := newPromotionFixture(t, true)
			if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); err != nil {
				t.Fatal(err)
			}
			corruptPromotionOperation(t, fixture, corruption.mutate)

			got, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
			if !errors.Is(err, ErrActivityPromotionConflict) || !reflect.DeepEqual(got, PromoteActivityResult{}) {
				t.Fatalf("source-divergent retry=(%+v,%v), want zero and ErrActivityPromotionConflict", got, err)
			}
		})

		t.Run(corruption.name+"/unknown commit", func(t *testing.T) {
			fixture := newPromotionFixture(t, true)
			real := fixture.service.workspace.withImmediateWorkspace
			fixture.service.workspace.withImmediateWorkspace = func(ctx context.Context, scope types.WorkspaceScope, fn func(*localstore.WorkspaceMutationTx) error) error {
				if err := real(ctx, scope, fn); err != nil {
					return err
				}
				corruptPromotionOperation(t, fixture, corruption.mutate)
				return fmt.Errorf("synthetic source-divergent ambiguity: %w", localstore.ErrCommitOutcomeUnknown)
			}

			got, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
			if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, PromoteActivityResult{}) {
				t.Fatalf("source-divergent unknown commit=(%+v,%v), want zero and ErrCommitOutcomeUnknown", got, err)
			}
		})
	}
}

func TestPromotionUsesRetainedPolicyWhenCurrentPolicyIsUnavailable(t *testing.T) {
	fixture := newPromotionFixture(t, true)
	if _, err := fixture.store.DB().Exec(`DELETE FROM activity_policy_current WHERE project_id=? AND workspace_id=?`,
		fixture.route.ProjectID, fixture.route.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
	if err != nil || result.Operation.ID != promotionGeneratedOperationID {
		t.Fatalf("retained-policy promotion=(%+v,%v)", result, err)
	}
}

func TestPromotionRejectsInvalidPromoterBeforeOpeningWriter(t *testing.T) {
	fixture := newPromotionFixture(t, true)
	fixture.request.Promoter.Assurance = types.AssurancePrivateAuthenticated
	writes := 0
	fixture.service.workspace.withImmediateWorkspace = func(context.Context, types.WorkspaceScope, func(*localstore.WorkspaceMutationTx) error) error {
		writes++
		return nil
	}
	if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, types.ErrInvalidActorEnvelope) {
		t.Fatalf("invalid promoter error=%v, want ErrInvalidActorEnvelope", err)
	}
	if writes != 0 {
		t.Fatalf("invalid promoter opened %d writer transactions", writes)
	}
}

func TestPromotionRejectsMissingProjectionChangedDigestAndAmbiguousSource(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		fixture := newPromotionFixture(t, false)
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, ErrActivityNotPromotable) {
			t.Fatalf("missing source error=%v, want ErrActivityNotPromotable", err)
		}
		assertPromotionAbsent(t, fixture)
	})
	t.Run("changed digest", func(t *testing.T) {
		fixture := newPromotionFixture(t, true)
		fixture.request.ExpectedSourceDigest = promotionOtherDigest()
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, ErrActivityPromotionConflict) {
			t.Fatalf("changed digest error=%v, want ErrActivityPromotionConflict", err)
		}
		assertPromotionAbsent(t, fixture)
	})
	t.Run("missing event projection", func(t *testing.T) {
		fixture := newPromotionFixture(t, false)
		fixture.activity = promotionLifecycleActivity()
		fixture.digest = acceptPromotionActivity(t, fixture, fixture.activity, promotionSourceWorkspaceID, 1, 0)
		fixture.request.SourceActivityID = fixture.activity.ID
		fixture.request.ExpectedSourceDigest = fixture.digest
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, ErrActivityNotPromotable) {
			t.Fatalf("missing projection error=%v, want ErrActivityNotPromotable", err)
		}
		assertPromotionAbsent(t, fixture)
	})
	t.Run("missing portable references", func(t *testing.T) {
		fixture := newPromotionFixture(t, false)
		fixture.activity = promotionOrdinaryActivity()
		fixture.activity.Event.ChannelID = "82000000-0000-4000-8000-000000000002"
		fixture.digest = acceptPromotionActivity(t, fixture, fixture.activity, promotionSourceWorkspaceID, 1, 0)
		fixture.request.ExpectedSourceDigest = fixture.digest
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, ErrActivityNotPromotable) {
			t.Fatalf("missing reference error=%v, want ErrActivityNotPromotable", err)
		}
		assertPromotionAbsent(t, fixture)
	})
	t.Run("ambiguous source", func(t *testing.T) {
		fixture := newPromotionFixture(t, true)
		acceptPromotionActivity(t, fixture, fixture.activity, types.WorkspaceID("b2000000-0000-4000-8000-000000000002"), 2, 1)
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, ErrActivityPromotionConflict) {
			t.Fatalf("ambiguous source error=%v, want ErrActivityPromotionConflict", err)
		}
		assertPromotionAbsent(t, fixture)
	})
}

func TestPromotionRejectsOpenConflictAndStaleViewWithoutWrites(t *testing.T) {
	t.Run("open conflict", func(t *testing.T) {
		fixture := newPromotionFixture(t, true)
		insertServiceConflict(t, fixture.store, fixture.binding.Scope, "promotion conflict", state.RecordKey{Kind: "task", ID: promotionSourceTaskID}, "open")
		setServiceWorkspaceState(t, fixture.store, fixture.binding.Scope, "conflicted")
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
			t.Fatalf("open conflict error=%v, want ErrWorkspaceConflicted", err)
		}
		assertPromotionAbsent(t, fixture)
	})
	t.Run("stale view", func(t *testing.T) {
		fixture := newPromotionFixture(t, true)
		fixture.request.ExpectedViewDigest = promotionOtherDigest()
		if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); !errors.Is(err, state.ErrOperationPrecondition) {
			t.Fatalf("stale view error=%v, want ErrOperationPrecondition", err)
		}
		assertPromotionAbsent(t, fixture)
	})
}

func TestPromotionRollsBackAtEveryActivityReceiptAndOperationWrite(t *testing.T) {
	tests := []struct {
		name, trigger string
	}{
		{"operation", `CREATE TRIGGER promotion_fault BEFORE INSERT ON workspace_overlay_operations BEGIN SELECT RAISE(ABORT,'promotion operation fault'); END`},
		{"status", `CREATE TRIGGER promotion_fault BEFORE UPDATE OF status ON workspace_bindings WHEN NEW.status='pending' BEGIN SELECT RAISE(ABORT,'promotion status fault'); END`},
		{"receipt", `CREATE TRIGGER promotion_fault BEFORE INSERT ON activity_promotion_receipts BEGIN SELECT RAISE(ABORT,'promotion receipt fault'); END`},
		{"lifecycle", `CREATE TRIGGER promotion_fault BEFORE INSERT ON activity_lifecycle WHEN NEW.lifecycle_kind='receipt' BEGIN SELECT RAISE(ABORT,'promotion lifecycle fault'); END`},
		{"revision", `CREATE TRIGGER promotion_fault BEFORE UPDATE OF workspace_revision ON workspace_bindings BEGIN SELECT RAISE(ABORT,'promotion revision fault'); END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPromotionFixture(t, true)
			persistedBefore := readPromotionPersistedState(t, fixture)
			before, err := fixture.service.View(context.Background(), fixture.binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.service.PromoteActivity(context.Background(), fixture.request); err == nil {
				t.Fatal("injected promotion fault unexpectedly succeeded")
			} else if !strings.Contains(err.Error(), "promotion "+test.name+" fault") {
				t.Fatalf("promotion fault error=%v, want %q marker", err, "promotion "+test.name+" fault")
			}
			after, err := fixture.service.View(context.Background(), fixture.binding.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if before.Snapshot.Digest != after.Snapshot.Digest || before.ThroughGeneration != after.ThroughGeneration {
				t.Fatalf("failed promotion changed view from %+v to %+v", before, after)
			}
			persistedAfter := readPromotionPersistedState(t, fixture)
			if !reflect.DeepEqual(persistedAfter, persistedBefore) {
				t.Fatalf("failed promotion changed status/revision or durable rows\nbefore=%+v\nafter=%+v", persistedBefore, persistedAfter)
			}
			assertPromotionAbsent(t, fixture)
		})
	}
}

func TestPromotionUnknownCommitConfirmsOnlyStoredReceipt(t *testing.T) {
	t.Run("committed", func(t *testing.T) {
		fixture := newPromotionFixture(t, true)
		fixture.store.DB().SetMaxOpenConns(1)
		real := fixture.service.workspace.withImmediateWorkspace
		fixture.service.workspace.withImmediateWorkspace = func(ctx context.Context, scope types.WorkspaceScope, fn func(*localstore.WorkspaceMutationTx) error) error {
			if err := real(ctx, scope, fn); err != nil {
				return err
			}
			if _, err := fixture.store.DB().ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
				t.Fatalf("make confirmation connection read-only: %v", err)
			}
			return fmt.Errorf("synthetic promotion ambiguity: %w", localstore.ErrCommitOutcomeUnknown)
		}
		defer func() {
			if _, err := fixture.store.DB().Exec(`PRAGMA query_only=OFF`); err != nil {
				t.Fatalf("restore confirmation connection: %v", err)
			}
		}()
		got, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
		if err != nil || got.Operation.ID != promotionGeneratedOperationID {
			t.Fatalf("confirmed unknown commit=(%+v,%v)", got, err)
		}
	})
	t.Run("not attempted", func(t *testing.T) {
		fixture := newPromotionFixture(t, true)
		fixture.service.workspace.withImmediateWorkspace = func(context.Context, types.WorkspaceScope, func(*localstore.WorkspaceMutationTx) error) error {
			return fmt.Errorf("synthetic promotion ambiguity: %w", localstore.ErrCommitOutcomeUnknown)
		}
		got, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
		if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || !reflect.DeepEqual(got, PromoteActivityResult{}) {
			t.Fatalf("unconfirmed unknown commit=(%+v,%v), want zero and sentinel", got, err)
		}
		assertPromotionAbsent(t, fixture)
	})
}

func TestActivityExpiryLeavesPortableTreeGitAcceptanceAndPromotionBytesIdentical(t *testing.T) {
	fixture := newPromotionFixture(t, true)
	activityRepo := localstore.NewActivityRepo(fixture.store.DB())
	if pruned, err := activityRepo.Prune(context.Background(), fixture.route, promotionSourceWorkspaceID, 10); err != nil || pruned != 0 {
		t.Fatalf("pre-promotion prune=(%d,%v), want no change", pruned, err)
	}
	acceptedBefore := mustServiceStatus(t, fixture.service, fixture.binding.Scope).AcceptedSnapshot
	result, err := fixture.service.PromoteActivity(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	wantOperation, err := state.CanonicalOperation(result.Operation)
	if err != nil {
		t.Fatal(err)
	}
	terminal := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := terminal.Add(time.Duration(fixture.policy.TerminalRetentionSeconds) * time.Second)
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_at=?,expires_at=?,updated_at=?
		WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='receipt'`,
		terminal.Format("2006-01-02T15:04:05.000000000Z"), expires.Format("2006-01-02T15:04:05.000000000Z"),
		terminal.Format("2006-01-02T15:04:05.000000000Z"), fixture.route.ProjectID, fixture.route.WorkspaceID, fixture.activity.ID); err != nil {
		t.Fatal(err)
	}
	if pruned, err := activityRepo.Prune(context.Background(), fixture.route, promotionSourceWorkspaceID, 10); err != nil || pruned != 1 {
		t.Fatalf("post-promotion expiry prune=(%d,%v), want one", pruned, err)
	}
	status := mustServiceStatus(t, fixture.service, fixture.binding.Scope)
	if status.AcceptedSnapshot.Digest != acceptedBefore.Digest || status.Binding.AcceptedCommitSHA != fixture.binding.AcceptedCommitSHA {
		t.Fatalf("Activity expiry changed Git authority: before=%+v after=%+v", acceptedBefore, status.AcceptedSnapshot)
	}
	var stored []byte
	if err := fixture.store.DB().QueryRow(`SELECT operation_json FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=? AND operation_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID, result.Operation.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, wantOperation) {
		t.Fatalf("Activity expiry changed portable operation bytes\ngot  %s\nwant %s", stored, wantOperation)
	}
	view, err := fixture.service.View(context.Background(), fixture.binding.Scope)
	if err != nil || !reflect.DeepEqual(view.Snapshot.Events[result.Event.ID], result.Event) {
		t.Fatalf("portable event after Activity expiry=(%+v,%v)", view.Snapshot.Events[result.Event.ID], err)
	}
}

func newPromotionFixture(t *testing.T, withSource bool) promotionFixture {
	t.Helper()
	repository := createPromotionGitRepository(t)
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	binding := registered.Binding
	profile := types.FabricProfile{
		ProfileID: promotionProfileID, Alias: "promotion", FabricInstanceID: promotionFabricID,
		BaseURL: "https://fabric.example.test", Mode: types.FabricModePrivate, CredentialRef: "keyring:promotion",
	}
	routes := localstore.NewFabricRouteRepo(store.DB())
	if err := routes.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	remote := types.FabricBinding{
		Workspace: binding, ProfileID: profile.ProfileID, FabricInstanceID: profile.FabricInstanceID,
		RemoteProjectID: promotionRemoteProjectID, StreamID: promotionStreamID, AttachmentRef: promotionAttachmentID,
		CanonicalRef: binding.AcceptedRef, Writable: true,
	}
	if err := routes.AttachWorkspace(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	route := types.ActivityRouteKey{
		ProjectID: binding.Scope.ProjectID, WorkspaceID: binding.Scope.WorkspaceID,
		FabricInstanceID: remote.FabricInstanceID, RemoteProjectID: remote.RemoteProjectID,
		StreamID: remote.StreamID, CanonicalRef: remote.CanonicalRef,
	}
	policy := promotionPolicy()
	if _, err := localstore.NewActivityRepo(store.DB()).ReplacePolicy(context.Background(), route, 0, "", policy); err != nil {
		t.Fatal(err)
	}
	fixture := promotionFixture{store: store, service: service, binding: binding, route: route, policy: policy, activity: promotionOrdinaryActivity()}
	if withSource {
		fixture.digest = acceptPromotionActivity(t, fixture, fixture.activity, promotionSourceWorkspaceID, 1, 0)
	} else {
		fixture.digest, _ = state.DigestActivity(fixture.activity)
	}
	view, err := service.View(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request = PromoteActivityRequest{
		Scope: binding.Scope, SourceActivityID: fixture.activity.ID, ExpectedSourceDigest: fixture.digest,
		ExpectedViewDigest: view.Snapshot.Digest, Promoter: promotionPromoter(),
	}
	service.workspace.newEventID = func() (string, error) { return promotionGeneratedEventID, nil }
	service.workspace.newOperationID = func() (string, error) { return promotionGeneratedOperationID, nil }
	service.workspace.now = func() time.Time { return time.Date(2026, 8, 28, 15, 0, 0, 456, time.UTC) }
	return fixture
}

func createPromotionGitRepository(t *testing.T) gitRepository {
	t.Helper()
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	snapshot, err := state.DecodeTree(testSnapshotTree(t, repository.projectID, repository.identity))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	snapshot.Actors[promotionSourceActorID] = state.Record[state.ActorV1]{Value: &state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: promotionSourceActorID, ActorKind: types.ActorAgent,
		DisplayName: "Remote source", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}}
	snapshot.Channels[promotionSourceChannelID] = state.Record[state.ChannelV1]{Value: &state.ChannelV1{
		SchemaVersion: 1, Kind: "channel", ID: promotionSourceChannelID, Name: "activity", CreatedAt: now, Extensions: state.ExtensionsV1{},
	}}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range tree {
		path := filepath.Join(repository.root, ".wormhole", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repository.root, "add", ".wormhole")
	runGit(t, repository.root, "commit", "-m", "add promotion references")
	repository.commit = string(bytes.TrimSpace([]byte(runGit(t, repository.root, "rev-parse", "HEAD"))))
	return repository
}

func promotionPolicy() state.EffectiveActivityPolicyV1 {
	return state.EffectiveActivityPolicyV1{
		SchemaVersion: 1, PolicyVersion: 1, OrdinaryMaxAgeSeconds: 2_592_000, OrdinaryMaxRows: 10_000,
		TerminalDefaultAgeSeconds: 2_592_000, TerminalMaximumAgeSeconds: 31_536_000, TerminalRetentionSeconds: 2_592_000,
	}
}

func promotionSourceActor() types.ActorEnvelope {
	at := time.Date(2026, 8, 28, 13, 0, 0, 123456789, time.UTC)
	return types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: promotionSourceActorID, AccountableHumanID: promotionSourceHumanID,
		SessionID: "promotion-source", HarnessName: "codex", HarnessVersion: "1.0", ModelName: "gpt", ModelVersion: "5.6",
		Assurance: types.AssurancePrivateAuthenticated, OccurredAt: at,
	}
}

func promotionPromoter() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "72000000-0000-4000-8000-000000000001",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 28, 14, 0, 0, 987654321, time.UTC),
	}
}

func promotionOrdinaryActivity() state.ActivityV1 {
	actor := promotionSourceActor()
	note := "source note"
	return state.ActivityV1{
		SchemaVersion: 1, ID: promotionSourceActivityID, Class: state.ActivityOrdinaryV1, Actor: actor,
		Event: &state.ActivityEventProjectionV1{
			ChannelID: promotionSourceChannelID, ActorID: promotionSourceActorID, EventType: "task.status_changed",
			Payload: json.RawMessage(`{"from_status":"wip","task_id":"91000000-0000-4000-8000-000000000001","to_status":"done"}`),
			Note:    &note, CreatedAt: actor.OccurredAt,
		}, CreatedAt: actor.OccurredAt,
	}
}

func promotionLifecycleActivity() state.ActivityV1 {
	actor := promotionSourceActor()
	return state.ActivityV1{
		SchemaVersion: 1, ID: promotionSourceActivityID, Class: state.ActivityLifecycleV1, Actor: actor,
		Lifecycle: &state.ActivityLifecycleProjectionV1{Kind: state.ActivityLifecycleRecoveryV1, ReferenceID: promotionSourceTaskID},
		CreatedAt: actor.OccurredAt,
	}
}

func acceptPromotionActivity(t *testing.T, fixture promotionFixture, activity state.ActivityV1, source types.WorkspaceID, sequence, expectedAfter int64) state.Digest {
	t.Helper()
	activityJSON, err := state.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	policyJSON, err := state.CanonicalActivityPolicy(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := state.DigestActivityPolicy(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := state.CanonicalActivityReceipt(state.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: activity.ID, ActivityDigest: digest, Sequence: sequence,
		PolicyVersion: fixture.policy.PolicyVersion, PolicyDigest: policyDigest,
		AcceptedAt: time.Date(2026, 8, 28, 13, 30, 0, int(sequence), time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := localstore.ActivityPullBatch{
		PolicyJSON: policyJSON, ExpectedAfter: expectedAfter, NextSequence: sequence, HasMore: false,
		Deliveries: []localstore.ActivityPullDelivery{{SourceWorkspaceID: source, ActivityJSON: activityJSON, ActivityDigest: digest, ReceiptJSON: receiptJSON}},
	}
	if err := localstore.NewActivityRepo(fixture.store.DB()).AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertPromotionAbsent(t *testing.T, fixture promotionFixture) {
	t.Helper()
	var operations, receipts, lifecycles int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_promotion_receipts WHERE local_project_id=? AND local_workspace_id=?`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND lifecycle_kind='receipt'`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID).Scan(&lifecycles); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || receipts != 0 || lifecycles != 0 {
		t.Fatalf("rejected promotion left operation/receipt/lifecycle=%d/%d/%d", operations, receipts, lifecycles)
	}
}

func promotionOtherDigest() state.Digest {
	return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

func equalPromotionNote(left, right *string) bool {
	return (left == nil) == (right == nil) && (left == nil || *left == *right)
}

type promotionOperationCorruption struct {
	name   string
	mutate func(*state.OperationV1)
}

func promotionCanonicalEventCorruptions(t *testing.T) []promotionOperationCorruption {
	t.Helper()
	return []promotionOperationCorruption{
		{name: "operation ID", mutate: func(operation *state.OperationV1) {
			operation.ID = "d1000000-0000-4000-8000-000000000002"
		}},
		{name: "promoter", mutate: func(operation *state.OperationV1) {
			operation.Actor.HumanPrincipalID = "72000000-0000-4000-8000-000000000002"
		}},
		{name: "event ID", mutate: func(operation *state.OperationV1) {
			operation.PutRecord.Record.Event.ID = "c1000000-0000-4000-8000-000000000002"
		}},
		{name: "channel ID", mutate: func(operation *state.OperationV1) {
			operation.PutRecord.Record.Event.ChannelID = "81000000-0000-4000-8000-000000000002"
		}},
		{name: "source actor ID", mutate: func(operation *state.OperationV1) {
			operation.PutRecord.Record.Event.ActorID = "61000000-0000-4000-8000-000000000002"
		}},
		{name: "event type", mutate: func(operation *state.OperationV1) {
			operation.PutRecord.Record.Event.EventType = "review.requested"
		}},
		{name: "payload", mutate: func(operation *state.OperationV1) {
			operation.PutRecord.Record.Event.Payload = json.RawMessage(`{"changed":true}`)
		}},
		{name: "note", mutate: func(operation *state.OperationV1) {
			note := "changed canonical note"
			operation.PutRecord.Record.Event.Note = &note
		}},
		{name: "created at", mutate: func(operation *state.OperationV1) {
			operation.PutRecord.Record.Event.CreatedAt = operation.PutRecord.Record.Event.CreatedAt.Add(time.Second)
		}},
		{name: "sole extension", mutate: func(operation *state.OperationV1) {
			extension := operation.PutRecord.Record.Event.Extensions["dev.wormhole.promotion"]
			operation.PutRecord.Record.Event.Extensions["dev.wormhole.other"] = extension
		}},
		{name: "extension source ID", mutate: func(operation *state.OperationV1) {
			setPromotionCorruptionExtension(t, operation, "a1000000-0000-4000-8000-000000000002", promotionGoldenSourceDigest)
		}},
		{name: "extension source digest", mutate: func(operation *state.OperationV1) {
			setPromotionCorruptionExtension(t, operation, promotionSourceActivityID, promotionOtherDigest())
		}},
	}
}

func setPromotionCorruptionExtension(t *testing.T, operation *state.OperationV1, activityID string, digest state.Digest) {
	t.Helper()
	data, err := state.CanonicalJSON(struct {
		SourceActivityID     string       `json:"source_activity_id"`
		SourceActivityDigest state.Digest `json:"source_activity_digest"`
	}{SourceActivityID: activityID, SourceActivityDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	operation.PutRecord.Record.Event.Extensions["dev.wormhole.promotion"] = state.ExtensionV1{SchemaVersion: 1, Data: data}
}

func corruptPromotionOperation(t *testing.T, fixture promotionFixture, mutate func(*state.OperationV1)) {
	t.Helper()
	var raw []byte
	if err := fixture.store.DB().QueryRow(`SELECT operation_json FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND operation_id=?`, fixture.binding.Scope.ProjectID,
		fixture.binding.Scope.WorkspaceID, promotionGeneratedOperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	operation, err := state.DecodeOperation(raw)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&operation)
	canonical, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatalf("corruption is not another canonical valid Operation: %v", err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET operation_json=?
		WHERE project_id=? AND workspace_id=? AND operation_id=?`, canonical, fixture.binding.Scope.ProjectID,
		fixture.binding.Scope.WorkspaceID, promotionGeneratedOperationID); err != nil {
		t.Fatal(err)
	}
}

type promotionPersistedState struct {
	Status     string
	Revision   int64
	Operations [][]string
	Receipts   [][]string
	Lifecycles [][]string
}

func readPromotionPersistedState(t *testing.T, fixture promotionFixture) promotionPersistedState {
	t.Helper()
	var result promotionPersistedState
	if err := fixture.store.DB().QueryRow(`SELECT status,workspace_revision FROM workspace_bindings
		WHERE project_id=? AND workspace_id=?`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID).
		Scan(&result.Status, &result.Revision); err != nil {
		t.Fatal(err)
	}
	result.Operations = readPromotionRows(t, fixture.store.DB(), `SELECT * FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? ORDER BY rowid`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID)
	result.Receipts = readPromotionRows(t, fixture.store.DB(), `SELECT * FROM activity_promotion_receipts
		WHERE local_project_id=? AND local_workspace_id=? ORDER BY rowid`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID)
	result.Lifecycles = readPromotionRows(t, fixture.store.DB(), `SELECT * FROM activity_lifecycle
		WHERE project_id=? AND workspace_id=? AND lifecycle_kind='receipt' ORDER BY rowid`, fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID)
	return result
}

func readPromotionRows(t *testing.T, db *sql.DB, query string, arguments ...any) [][]string {
	t.Helper()
	rows, err := db.Query(query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := make([][]string, 0)
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(values))
		for index, value := range values {
			if value == nil {
				row[index] = "<NULL>"
				continue
			}
			row[index] = fmt.Sprintf("%d:%s", len(value), value)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
