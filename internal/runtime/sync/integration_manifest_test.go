package sync

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

type recordingIntegrationManifestReceiver struct {
	projectID  string
	passportID string
	roles      []string
	raw        json.RawMessage
	err        error
	rollbacks  int
}

func (r *recordingIntegrationManifestReceiver) RollbackBootstrapIntegrationManifest(_ context.Context, _, _ string, _ []string, raw json.RawMessage) error {
	r.rollbacks++
	r.raw = append(json.RawMessage(nil), raw...)
	return nil
}

func (r *recordingIntegrationManifestReceiver) ReceiveIntegrationManifest(_ context.Context, projectID, passportID string, roles []string, raw json.RawMessage) error {
	r.projectID = projectID
	r.passportID = passportID
	r.roles = append([]string(nil), roles...)
	r.raw = append(json.RawMessage(nil), raw...)
	return r.err
}

func TestBootstrapRoutesManifestThroughAuthenticatedReceiverBeforeReadyCommit(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
	if err := engine.ConfigureBootstrap(store, "agent-1", "passport-1", nil); err != nil {
		t.Fatal(err)
	}
	receiver := &recordingIntegrationManifestReceiver{}
	engine.ConfigureIntegrationManifestReceiver(receiver)
	offer := json.RawMessage(`{"operation":"offered","project_id":"ns-1"}`)
	bootstrap := validBootstrapWire()
	bootstrap.OrgConfig.Identity.Passport.Roles = []string{"contributor"}
	bootstrap.OrgConfig.IntegrationManifestMetadata = offer
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return bootstrap, nil
	}
	if err := engine.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if receiver.projectID != "ns-1" || receiver.passportID != "passport-1" || !reflect.DeepEqual(receiver.roles, []string{"contributor"}) || !jsonEqual(receiver.raw, offer) {
		t.Fatalf("receiver binding/raw = %q %q %#v %s", receiver.projectID, receiver.passportID, receiver.roles, receiver.raw)
	}
	var snapshots int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM bootstrap_metadata WHERE namespace_id = 'ns-1'`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatalf("bootstrap metadata rows = %d, want 1 after verified receipt", snapshots)
	}
}

func TestBootstrapReceiverFailurePreventsReadyCommit(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
	if err := engine.ConfigureBootstrap(store, "agent-1", "passport-1", nil); err != nil {
		t.Fatal(err)
	}
	engine.ConfigureIntegrationManifestReceiver(&recordingIntegrationManifestReceiver{err: errors.New("malformed manifest")})
	bootstrap := validBootstrapWire()
	bootstrap.OrgConfig.Identity.Passport.Roles = []string{"contributor"}
	bootstrap.OrgConfig.IntegrationManifestMetadata = json.RawMessage(`{"operation":"offered"}`)
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
		return bootstrap, nil
	}
	if err := engine.Bootstrap(ctx); err == nil {
		t.Fatal("Bootstrap accepted a rejected manifest")
	}
	var projects int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE namespace_id = 'ns-1'`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatalf("rejected manifest bootstrap committed %d projects", projects)
	}
}

func TestBootstrapSnapshotFailureRollsBackReceivedManifestCandidate(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_manifest_bootstrap BEFORE INSERT ON projects BEGIN SELECT RAISE(ABORT, 'injected bootstrap failure'); END`); err != nil {
		t.Fatal(err)
	}
	qRepo, aRepo := setupTestRepos(t)
	defer qRepo.db.Close()
	engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
	if err := engine.ConfigureBootstrap(store, "agent-1", "passport-1", nil); err != nil {
		t.Fatal(err)
	}
	receiver := &recordingIntegrationManifestReceiver{}
	engine.ConfigureIntegrationManifestReceiver(receiver)
	bootstrap := validBootstrapWire()
	bootstrap.OrgConfig.Identity.Passport.Roles = []string{"contributor"}
	bootstrap.OrgConfig.IntegrationManifestMetadata = json.RawMessage(`{"operation":"offered"}`)
	engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) { return bootstrap, nil }
	if err := engine.Bootstrap(ctx); err == nil {
		t.Fatal("injected snapshot failure unexpectedly committed")
	}
	if receiver.rollbacks != 1 {
		t.Fatalf("bootstrap receiver rollbacks = %d, want 1", receiver.rollbacks)
	}
}

func TestIncrementalManifestReceiverFailureDoesNotAdvanceCursor(t *testing.T) {
	for _, operation := range []string{"offered", "revoked"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			bootstrap := validBootstrapWire()
			bootstrap.OrgConfig.Identity.Passport.Roles = []string{"contributor"}
			if err := store.ApplyBootstrap(ctx, "ns-1", bootstrap.OrgConfig, time.Now().UTC(), nil); err != nil {
				t.Fatal(err)
			}
			qRepo, aRepo := setupTestRepos(t)
			defer qRepo.db.Close()
			engine := mustNewEngine(t, "http://unused.invalid", qRepo, aRepo, nil, nil, DefaultConfig())
			if err := engine.ConfigureBootstrap(store, "agent-1", "passport-1", nil); err != nil {
				t.Fatal(err)
			}
			receiver := &recordingIntegrationManifestReceiver{err: errors.New("malformed " + operation)}
			engine.ConfigureIntegrationManifestReceiver(receiver)
			engine.lastSyncCursor = "2026-07-26T10:00:00Z"
			raw := json.RawMessage(`{"operation":"` + operation + `"}`)
			engine.testCallSyncToolWithResultFn = func(context.Context, string, map[string]interface{}) (interface{}, error) {
				return incrementalPullResult("2026-07-26T11:00:00Z", []syncUpdateEnvelopeWire{{Type: "integration_manifest", Data: raw}}), nil
			}
			if err := engine.PullIncremental(ctx); err == nil {
				t.Fatal("PullIncremental accepted a rejected manifest")
			}
			if engine.lastSyncCursor != "2026-07-26T10:00:00Z" {
				t.Fatalf("cursor advanced to %q", engine.lastSyncCursor)
			}
			if receiver.projectID != "ns-1" || receiver.passportID != "passport-1" || !reflect.DeepEqual(receiver.roles, []string{"contributor"}) || !jsonEqual(receiver.raw, raw) {
				t.Fatalf("receiver binding/raw = %q %q %#v %s", receiver.projectID, receiver.passportID, receiver.roles, receiver.raw)
			}
		})
	}
}

func jsonEqual(left, right []byte) bool {
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}
