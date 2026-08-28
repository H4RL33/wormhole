package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	activityTestAgentID   = "60000000-0000-4000-8000-000000000001"
	activityTestHumanID   = "70000000-0000-4000-8000-000000000001"
	activityTestChannelID = "80000000-0000-4000-8000-000000000001"
	activityTestTaskID    = "90000000-0000-4000-8000-000000000001"
	activityTestIDOne     = "a0000000-0000-4000-8000-000000000001"
	activityTestIDTwo     = "a0000000-0000-4000-8000-000000000002"
	activityTestIDThree   = "a0000000-0000-4000-8000-000000000003"
)

type activityTransportFixture struct {
	path        string
	store       *localstore.Store
	routes      *localstore.FabricRouteRepo
	workspaces  *localstore.WorkspaceRepo
	activities  *localstore.ActivityRepo
	bindings    []types.FabricBinding
	profiles    []types.FabricProfile
	activityKey []types.ActivityRouteKey
}

func newActivityTransportFixture(t *testing.T, bindings int, installPolicy bool) *activityTransportFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture := &activityTransportFixture{
		path:  path,
		store: store, routes: localstore.NewFabricRouteRepo(store.DB()),
		workspaces: localstore.NewWorkspaceRepo(store.DB()), activities: localstore.NewActivityRepo(store.DB()),
	}
	ids := [][7]string{
		{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000001"},
		{"00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000012", "10000000-0000-4000-8000-000000000002", "20000000-0000-4000-8000-000000000002", "30000000-0000-4000-8000-000000000002", "40000000-0000-4000-8000-000000000002", "50000000-0000-4000-8000-000000000002"},
	}
	for index := 0; index < bindings; index++ {
		set := ids[index]
		binding := activityTestWorkspaceBinding(t, set[0], set[1], fmt.Sprintf("/activity-%d", index), uint64(index+1), uint64(index+11))
		created, ok, err := fixture.workspaces.RegisterWorkspace(context.Background(), binding, activityTestWorkspaceTree(t, binding.Scope.ProjectID))
		if err != nil || !ok {
			t.Fatalf("register workspace: created=%v err=%v", ok, err)
		}
		profile := types.FabricProfile{
			ProfileID: set[2], Alias: fmt.Sprintf("activity-%d", index), FabricInstanceID: set[3],
			BaseURL: fmt.Sprintf("https://fabric-%d.example.test", index), Mode: types.FabricModePrivate,
			CredentialRef: fmt.Sprintf("keyring:activity-%d", index),
		}
		if err := fixture.routes.CreateProfile(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
		remote := types.FabricBinding{
			Workspace: created, ProfileID: profile.ProfileID, FabricInstanceID: profile.FabricInstanceID,
			RemoteProjectID: set[4], StreamID: set[5], AttachmentRef: set[6],
			CanonicalRef: created.AcceptedRef, Writable: true,
		}
		if err := fixture.routes.AttachWorkspace(context.Background(), remote); err != nil {
			t.Fatal(err)
		}
		route := activityRouteForBinding(remote)
		if installPolicy {
			if _, err := fixture.activities.ReplacePolicy(context.Background(), route, 0, "", activityTestPolicy(1)); err != nil {
				t.Fatal(err)
			}
		}
		fixture.bindings = append(fixture.bindings, remote)
		fixture.profiles = append(fixture.profiles, profile)
		fixture.activityKey = append(fixture.activityKey, route)
	}
	return fixture
}

func activityTestWorkspaceBinding(t *testing.T, projectID, workspaceID, path string, device, inode uint64) types.WorkspaceBinding {
	t.Helper()
	tree := activityTestWorkspaceTree(t, projectID)
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return types.WorkspaceBinding{
		Scope:      types.WorkspaceScope{ProjectID: projectID, WorkspaceID: types.WorkspaceID(workspaceID)},
		Checkout:   types.CheckoutIdentity{CanonicalPath: path, Device: device, Inode: inode},
		Repository: types.RepositoryIdentity{}, AcceptedRef: "refs/heads/main",
		AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: string(snapshot.Digest),
	}
}

func activityTestWorkspaceTree(t *testing.T, projectID string) projectstate.Tree {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	snapshot := projectstate.Snapshot{
		Config:  projectstate.ConfigV1{SnapshotVersion: 1, ProjectID: projectID, Handle: types.ProjectHandle{Namespace: "acme", Name: "wormhole"}, Repository: types.RepositoryIdentity{}},
		Project: projectstate.ProjectV1{SchemaVersion: 1, Kind: "project", ID: projectID, Name: "Wormhole", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: projectstate.ExtensionsV1{}},
		Actors:  map[string]projectstate.Record[projectstate.ActorV1]{}, Tasks: map[string]projectstate.Record[projectstate.TaskV1]{},
		TaskLinks: map[string]projectstate.Record[projectstate.TaskLinkV1]{}, Articles: map[string]projectstate.KBRecord{},
		Channels: map[string]projectstate.Record[projectstate.ChannelV1]{}, Events: map[string]projectstate.EventV1{},
		GitLinks: map[string]projectstate.Record[projectstate.GitLinkV1]{},
	}
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func activityRouteForBinding(binding types.FabricBinding) types.ActivityRouteKey {
	return types.ActivityRouteKey{
		ProjectID: binding.Workspace.Scope.ProjectID, WorkspaceID: binding.Workspace.Scope.WorkspaceID,
		FabricInstanceID: binding.FabricInstanceID, RemoteProjectID: binding.RemoteProjectID,
		StreamID: binding.StreamID, CanonicalRef: binding.CanonicalRef,
	}
}

func activityTestPolicy(version int64) projectstate.EffectiveActivityPolicyV1 {
	return projectstate.EffectiveActivityPolicyV1{
		SchemaVersion: 1, PolicyVersion: version, OrdinaryMaxAgeSeconds: 2_592_000,
		OrdinaryMaxRows: 10_000, TerminalDefaultAgeSeconds: 2_592_000,
		TerminalMaximumAgeSeconds: 31_536_000, TerminalRetentionSeconds: 2_592_000,
	}
}

func activityTestPolicyEvidence(t *testing.T, policy projectstate.EffectiveActivityPolicyV1) ([]byte, projectstate.Digest) {
	t.Helper()
	raw, err := projectstate.CanonicalActivityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	return raw, digest
}

func activityTestActor(at time.Time) types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: activityTestAgentID, AccountableHumanID: activityTestHumanID,
		SessionID: "activity-session", HarnessName: "codex", HarnessVersion: "1.0",
		ModelName: "gpt", ModelVersion: "5.6", Assurance: types.AssurancePrivateAuthenticated, OccurredAt: at,
	}
}

func activityTestOrdinary(id string, at time.Time) projectstate.ActivityV1 {
	actor := activityTestActor(at)
	note := "task moved"
	return projectstate.ActivityV1{
		SchemaVersion: 1, ID: id, Class: projectstate.ActivityOrdinaryV1, Actor: actor,
		Event: &projectstate.ActivityEventProjectionV1{
			ChannelID: activityTestChannelID, ActorID: actor.AgentID, EventType: "task.status_changed",
			Payload: json.RawMessage(fmt.Sprintf(`{"from_status":"wip","task_id":%q,"to_status":"done"}`, activityTestTaskID)),
			Note:    &note, CreatedAt: at,
		},
		CreatedAt: at,
	}
}

func activityTestPresence(id string, at time.Time) projectstate.ActivityV1 {
	return projectstate.ActivityV1{SchemaVersion: 1, ID: id, Class: projectstate.ActivityPresenceV1, Actor: activityTestActor(at), CreatedAt: at}
}

type activityTestCredentials struct {
	mu     sync.Mutex
	values map[string]string
	reads  []string
	err    error
}

func (s *activityTestCredentials) Read(_ context.Context, reference string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, reference)
	if s.err != nil {
		return "", s.err
	}
	value, ok := s.values[reference]
	if !ok {
		return "", errors.New("missing credential")
	}
	return value, nil
}

type activityTestConflictGate struct {
	mu   sync.Mutex
	open map[types.WorkspaceScope]bool
	err  error
}

type activityLateConflictGate struct {
	mu         sync.Mutex
	store      *localstore.Store
	workspaces *localstore.WorkspaceRepo
	scope      types.WorkspaceScope
	calls      int
}

func (g *activityLateConflictGate) HasOpenConflicts(ctx context.Context, scope types.WorkspaceScope) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	open, err := g.workspaces.HasOpenConflicts(ctx, scope)
	if err != nil || open || scope != g.scope || g.calls != 2 {
		return open, err
	}
	digest := sha256.Sum256([]byte("late-activity-conflict"))
	conflictID := fmt.Sprintf("sha256:%x", digest)
	_, err = g.store.DB().ExecContext(ctx, `INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state)
		VALUES (?,?,?,?,? ,?,'/title','same_field','{}','{}','{}','open')`,
		scope.ProjectID, scope.WorkspaceID, conflictID, conflictID, "task", activityTestTaskID)
	return open, err
}

func (g *activityTestConflictGate) HasOpenConflicts(_ context.Context, scope types.WorkspaceScope) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open[scope], g.err
}

func (g *activityTestConflictGate) set(scope types.WorkspaceScope, open bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open[scope] = open
}

type activityTestClient struct {
	accept   func(context.Context, ActivityAcceptRequest) (ActivityAcceptResponse, error)
	pull     func(context.Context, ActivityPullRequest) (ActivityPullResponse, error)
	presence func(context.Context, ActivityPresenceRequest) (ActivityPresenceResponse, error)
}

func (c *activityTestClient) Accept(ctx context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
	if c.accept == nil {
		return ActivityAcceptResponse{}, errors.New("unexpected accept")
	}
	return c.accept(ctx, request)
}

func (c *activityTestClient) Pull(ctx context.Context, request ActivityPullRequest) (ActivityPullResponse, error) {
	if c.pull == nil {
		return ActivityPullResponse{}, errors.New("unexpected pull")
	}
	return c.pull(ctx, request)
}

func (c *activityTestClient) SendPresence(ctx context.Context, request ActivityPresenceRequest) (ActivityPresenceResponse, error) {
	if c.presence == nil {
		return ActivityPresenceResponse{}, errors.New("unexpected presence")
	}
	return c.presence(ctx, request)
}

type activityTestClientFactory struct {
	mu       sync.Mutex
	client   ActivityFabricClient
	err      error
	profiles []types.FabricProfile
	tokens   []string
}

func (f *activityTestClientFactory) Client(_ context.Context, profile types.FabricProfile, token string) (ActivityFabricClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles = append(f.profiles, profile)
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

type activityTestRouteSource struct {
	mu     sync.Mutex
	routes map[types.WorkspaceScope]struct {
		binding types.FabricBinding
		profile types.FabricProfile
	}
	reads []types.WorkspaceScope
	err   error
}

func (s *activityTestRouteSource) GetRoute(_ context.Context, scope types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, scope)
	if s.err != nil {
		return types.FabricBinding{}, types.FabricProfile{}, s.err
	}
	value, ok := s.routes[scope]
	if !ok {
		return types.FabricBinding{}, types.FabricProfile{}, errors.New("missing route")
	}
	return value.binding, value.profile, nil
}

func activityRouteSourceForFixture(fixture *activityTransportFixture) *activityTestRouteSource {
	routes := make(map[types.WorkspaceScope]struct {
		binding types.FabricBinding
		profile types.FabricProfile
	})
	for index, binding := range fixture.bindings {
		routes[binding.Workspace.Scope] = struct {
			binding types.FabricBinding
			profile types.FabricProfile
		}{binding, fixture.profiles[index]}
	}
	return &activityTestRouteSource{routes: routes}
}

func activityTestTransport(t *testing.T, fixture *activityTransportFixture, routes FabricRouteSource, credentials CredentialSource, gate localstore.WorkspaceConflictGate, factory ActivityClientFactory) *ActivityTransport {
	t.Helper()
	transport, err := NewActivityTransport(routes, credentials, gate, fixture.activities, factory)
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func activityTestReceipt(t *testing.T, request ActivityAcceptRequest, policy projectstate.EffectiveActivityPolicyV1, sequence int64) projectstate.ActivityReceiptV1 {
	t.Helper()
	_, policyDigest := activityTestPolicyEvidence(t, policy)
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: activityTestDecode(t, request.ActivityJSON).ID,
		ActivityDigest: request.ActivityDigest, Sequence: sequence, PolicyVersion: policy.PolicyVersion,
		PolicyDigest: policyDigest, AcceptedAt: time.Date(2026, 8, 28, 11, 0, int(sequence), 123, time.UTC),
	}
	if _, err := projectstate.CanonicalActivityReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func activityTestDecode(t *testing.T, raw []byte) projectstate.ActivityV1 {
	t.Helper()
	activity, err := projectstate.DecodeActivity(raw)
	if err != nil {
		t.Fatal(err)
	}
	return activity
}

func activityTableCount(t *testing.T, fixture *activityTransportFixture, table string) int {
	t.Helper()
	var count int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestActivityTransportRejectsPolicyBeforeQueueSendAcceptOrExpose(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, false)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "secret"}}
	client := &activityTestClient{}
	factory := &activityTestClientFactory{client: client}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}
	transport := activityTestTransport(t, fixture, routes, credentials, gate, factory)
	scope := fixture.bindings[0].Workspace.Scope
	at := time.Date(2026, 8, 28, 10, 1, 0, 123, time.UTC)

	for name, call := range map[string]func() error{
		"queue": func() error {
			return transport.Queue(context.Background(), scope, activityTestOrdinary(activityTestIDOne, at))
		},
		"send":   func() error { return transport.DeliverPending(context.Background(), scope, 1) },
		"accept": func() error { return transport.Pull(context.Background(), scope, 1) },
		"expose": func() error { _, err := transport.Retained(context.Background(), scope, 1); return err },
		"presence": func() error {
			return transport.SendPresence(context.Background(), scope, activityTestPresence(activityTestIDTwo, at))
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, localstore.ErrActivityPolicyUnavailable) {
				t.Fatalf("error=%v, want ErrActivityPolicyUnavailable", err)
			}
		})
	}
	if got := activityTableCount(t, fixture, "activity_ledger"); got != 0 {
		t.Fatalf("activity ledger rows=%d, want 0", got)
	}
	if len(credentials.reads) != 0 || len(factory.profiles) != 0 {
		t.Fatalf("policy rejection reached credential/client: reads=%v clients=%d", credentials.reads, len(factory.profiles))
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "malformed", raw: []byte("not-json")},
		{name: "unbounded", raw: []byte(`{"schema_version":1,"policy_version":1,"ordinary_max_age_seconds":2592000,"ordinary_max_rows":10000,"terminal_default_age_seconds":2592000,"terminal_maximum_age_seconds":31536000,"terminal_retention_seconds":0}` + "\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			corrupt := newActivityTransportFixture(t, 1, false)
			route := corrupt.activityKey[0]
			digest := "sha256:" + strings.Repeat("a", 64)
			arguments := []any{route.ProjectID, route.WorkspaceID, route.FabricInstanceID, route.RemoteProjectID, route.StreamID, route.CanonicalRef}
			if _, err := corrupt.store.DB().Exec(`INSERT INTO activity_policy_versions
				(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,
				 canonical_policy_json,policy_digest,terminal_retention_seconds,received_at)
				VALUES (?,?,?,?,?,?,1,?,?,2592000,?)`, append(arguments, test.raw, digest, time.Now().UTC())...); err != nil {
				t.Fatal(err)
			}
			if _, err := corrupt.store.DB().Exec(`INSERT INTO activity_policy_current
				(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,policy_version,policy_digest,updated_at)
				VALUES (?,?,?,?,?,?,1,?,?)`, append(arguments, digest, time.Now().UTC())...); err != nil {
				t.Fatal(err)
			}
			corruptCredentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "secret"}}
			corruptFactory := &activityTestClientFactory{client: &activityTestClient{}}
			corruptTransport := activityTestTransport(t, corrupt, activityRouteSourceForFixture(corrupt), corruptCredentials,
				&activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, corruptFactory)
			err := corruptTransport.Queue(context.Background(), corrupt.bindings[0].Workspace.Scope,
				activityTestOrdinary(activityTestIDOne, at))
			if !errors.Is(err, localstore.ErrActivityPolicyUnavailable) {
				t.Fatalf("corrupt policy error=%v, want ErrActivityPolicyUnavailable", err)
			}
			if len(corruptCredentials.reads) != 0 || len(corruptFactory.profiles) != 0 || activityTableCount(t, corrupt, "activity_ledger") != 0 {
				t.Fatal("corrupt policy reached durable queue or network dependency")
			}
		})
	}
}

func TestActivityTransportRetryAndServerReplayAreIdempotent(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}
	policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	var requests []ActivityAcceptRequest
	var receipt projectstate.ActivityReceiptV1
	client := &activityTestClient{accept: func(_ context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
		requests = append(requests, request)
		if receipt.ActivityID == "" {
			receipt = activityTestReceipt(t, request, activityTestPolicy(1), 1)
			gate.set(fixture.bindings[0].Workspace.Scope, true)
		}
		return ActivityAcceptResponse{Receipt: receipt, PolicyJSON: policyRaw, PolicyDigest: policyDigest}, nil
	}}
	transport := activityTestTransport(t, fixture, routes, credentials, gate, &activityTestClientFactory{client: client})
	scope := fixture.bindings[0].Workspace.Scope
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 2, 0, 456, time.UTC))
	if err := transport.Queue(context.Background(), scope, activity); err != nil {
		t.Fatal(err)
	}
	if err := transport.DeliverPending(context.Background(), scope, 1); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
		t.Fatalf("first delivery error=%v, want ErrWorkspaceConflicted", err)
	}
	gate.set(scope, false)
	if err := transport.DeliverPending(context.Background(), scope, 1); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !bytes.Equal(requests[0].ActivityJSON, requests[1].ActivityJSON) || requests[0].ActivityDigest != requests[1].ActivityDigest {
		t.Fatalf("retry changed activity evidence: %+v", requests)
	}
	if pending, err := fixture.activities.PendingOutbound(context.Background(), fixture.activityKey[0], 1); err != nil || len(pending) != 0 {
		t.Fatalf("pending after replay=(%d,%v), want (0,nil)", len(pending), err)
	}
}

func TestActivityTransportPolicyChangeRetriesSameActivityBytes(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}
	policyOneRaw, policyOneDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	policyThreeRaw, policyThreeDigest := activityTestPolicyEvidence(t, activityTestPolicy(3))
	var requests []ActivityAcceptRequest
	client := &activityTestClient{accept: func(_ context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return ActivityAcceptResponse{PolicyJSON: policyThreeRaw, PolicyDigest: policyThreeDigest, PolicyChanged: true}, nil
		}
		return ActivityAcceptResponse{Receipt: activityTestReceipt(t, request, activityTestPolicy(3), 2), PolicyJSON: policyThreeRaw, PolicyDigest: policyThreeDigest}, nil
	}}
	transport := activityTestTransport(t, fixture, routes, credentials, gate, &activityTestClientFactory{client: client})
	scope := fixture.bindings[0].Workspace.Scope
	if err := transport.Queue(context.Background(), scope, activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 3, 0, 789, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if err := transport.DeliverPending(context.Background(), scope, 1); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !bytes.Equal(requests[0].ActivityJSON, requests[1].ActivityJSON) || requests[0].ActivityDigest != requests[1].ActivityDigest {
		t.Fatalf("policy retry changed immutable evidence: %+v", requests)
	}
	if requests[0].PolicyVersion != 1 || requests[0].PolicyDigest != policyOneDigest || requests[1].PolicyVersion != 3 || requests[1].PolicyDigest != policyThreeDigest {
		t.Fatalf("policy request pairs=%+v", requests)
	}
	var createdVersion, expectedVersion int64
	var createdDigest, expectedDigest string
	if err := fixture.store.DB().QueryRow(`SELECT created_policy_version,created_policy_digest,expected_policy_version,expected_policy_digest
		FROM activity_outbound_queue WHERE activity_id=?`, activityTestIDOne).Scan(&createdVersion, &createdDigest, &expectedVersion, &expectedDigest); err != nil {
		t.Fatal(err)
	}
	if createdVersion != 1 || createdDigest != string(policyOneDigest) || expectedVersion != 3 || expectedDigest != string(policyThreeDigest) {
		t.Fatalf("queue policy evidence=(%d,%s,%d,%s)", createdVersion, createdDigest, expectedVersion, expectedDigest)
	}
	var observedVersions string
	if err := fixture.store.DB().QueryRow(`SELECT group_concat(policy_version, ',') FROM
		(SELECT policy_version FROM activity_policy_versions ORDER BY policy_version)`).Scan(&observedVersions); err != nil {
		t.Fatal(err)
	}
	if observedVersions != "1,3" {
		t.Fatalf("observed immutable policy versions=%q, want 1,3", observedVersions)
	}
	_ = policyOneRaw
}

func TestActivityTransportPullGapsAndCursorRollback(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	source := types.WorkspaceID("00000000-0000-4000-8000-000000000099")
	first := activityTestDelivery(t, activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 4, 0, 1, time.UTC)), source, 3, activityTestPolicy(1))
	second := activityTestDelivery(t, activityTestOrdinary(activityTestIDTwo, time.Date(2026, 8, 28, 10, 4, 1, 2, time.UTC)), source, 6, activityTestPolicy(1))
	invalid := activityTestDelivery(t, activityTestOrdinary(activityTestIDThree, time.Date(2026, 8, 28, 10, 4, 2, 3, time.UTC)), source, 7, activityTestPolicy(1))
	invalid.ActivityDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
	responses := []ActivityPullResponse{
		{PolicyJSON: policyRaw, PolicyDigest: policyDigest, Deliveries: []localstore.ActivityPullDelivery{first}, NextSequence: 5},
		{PolicyJSON: policyRaw, PolicyDigest: policyDigest, Deliveries: []localstore.ActivityPullDelivery{second, invalid}, NextSequence: 7},
		{PolicyJSON: policyRaw, PolicyDigest: policyDigest, Deliveries: []localstore.ActivityPullDelivery{second}, NextSequence: 7},
	}
	client := &activityTestClient{pull: func(_ context.Context, request ActivityPullRequest) (ActivityPullResponse, error) {
		response := responses[0]
		responses = responses[1:]
		if request.Limit != 2 {
			t.Fatalf("pull limit=%d, want 2", request.Limit)
		}
		return response, nil
	}}
	transport := activityTestTransport(t, fixture, routes, credentials, &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, &activityTestClientFactory{client: client})
	scope := fixture.bindings[0].Workspace.Scope
	if err := transport.Pull(context.Background(), scope, 2); err != nil {
		t.Fatal(err)
	}
	if cursor, err := fixture.activities.Cursor(context.Background(), fixture.activityKey[0]); err != nil || cursor != 5 {
		t.Fatalf("gap cursor=(%d,%v), want (5,nil)", cursor, err)
	}
	if err := transport.Pull(context.Background(), scope, 2); !errors.Is(err, localstore.ErrActivityReplayConflict) {
		t.Fatalf("invalid batch error=%v, want ErrActivityReplayConflict", err)
	}
	if cursor, _ := fixture.activities.Cursor(context.Background(), fixture.activityKey[0]); cursor != 5 {
		t.Fatalf("invalid batch advanced cursor to %d", cursor)
	}
	if err := transport.Pull(context.Background(), scope, 2); err != nil {
		t.Fatal(err)
	}
	if cursor, _ := fixture.activities.Cursor(context.Background(), fixture.activityKey[0]); cursor != 7 {
		t.Fatalf("valid retry cursor=%d, want 7", cursor)
	}
}

func activityTestDelivery(t *testing.T, activity projectstate.ActivityV1, source types.WorkspaceID, sequence int64, policy projectstate.EffectiveActivityPolicyV1) localstore.ActivityPullDelivery {
	t.Helper()
	raw, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	_, policyDigest := activityTestPolicyEvidence(t, policy)
	receipt := projectstate.ActivityReceiptV1{SchemaVersion: 1, ActivityID: activity.ID, ActivityDigest: digest, Sequence: sequence,
		PolicyVersion: policy.PolicyVersion, PolicyDigest: policyDigest, AcceptedAt: time.Date(2026, 8, 28, 11, 0, int(sequence), 123, time.UTC)}
	receiptRaw, err := projectstate.CanonicalActivityReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return localstore.ActivityPullDelivery{SourceWorkspaceID: source, ActivityJSON: raw, ActivityDigest: digest, ReceiptJSON: receiptRaw}
}

func TestActivityTransportPresenceHasZeroDurableRowsAndVanishesOnRestart(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	var sent int
	client := &activityTestClient{presence: func(_ context.Context, request ActivityPresenceRequest) (ActivityPresenceResponse, error) {
		sent++
		if request.AttachmentRef != fixture.bindings[0].AttachmentRef {
			t.Fatalf("attachment=%q", request.AttachmentRef)
		}
		activity := activityTestDecode(t, request.ActivityJSON)
		computed, _ := projectstate.DigestActivity(activity)
		_, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
		if activity.Class != projectstate.ActivityPresenceV1 || computed != request.ActivityDigest ||
			request.PolicyVersion != 1 || request.PolicyDigest != policyDigest {
			t.Fatalf("presence evidence invalid")
		}
		return ActivityPresenceResponse{}, nil
	}}
	transport := activityTestTransport(t, fixture, routes, credentials, fixture.workspaces, &activityTestClientFactory{client: client})
	before := activitySemanticSnapshot(t, fixture.store.DB())
	if err := transport.SendPresence(context.Background(), fixture.bindings[0].Workspace.Scope,
		activityTestPresence(activityTestIDOne, time.Date(2026, 8, 28, 10, 5, 0, 4, time.UTC))); err != nil {
		t.Fatal(err)
	}
	after := activitySemanticSnapshot(t, fixture.store.DB())
	if sent != 1 || !reflect.DeepEqual(after, before) {
		t.Fatalf("presence persisted: sent=%d before=%v after=%v", sent, before, after)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := localstore.Open(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := activitySemanticSnapshot(t, reopened.DB())
	if !reflect.DeepEqual(restarted, before) {
		t.Fatalf("restart retained presence state: before=%v restarted=%v", before, restarted)
	}
}

var activityTables = []string{
	"activity_policy_versions", "activity_policy_current", "activity_ledger", "activity_ingress_receipts",
	"activity_lifecycle", "activity_outbound_queue", "activity_cursors", "activity_promotion_receipts",
}

func activitySemanticSnapshot(t *testing.T, db *sql.DB) map[string][][]string {
	t.Helper()
	snapshot := make(map[string][][]string, len(activityTables))
	for _, table := range activityTables {
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

func TestActivityTransportPresencePolicyChangeRetriesSameBytes(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	policyOneRaw, policyOneDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	policyThreeRaw, policyThreeDigest := activityTestPolicyEvidence(t, activityTestPolicy(3))
	var requests []ActivityPresenceRequest
	client := &activityTestClient{presence: func(_ context.Context, request ActivityPresenceRequest) (ActivityPresenceResponse, error) {
		request.ActivityJSON = append([]byte(nil), request.ActivityJSON...)
		requests = append(requests, request)
		if len(requests) == 1 {
			return ActivityPresenceResponse{PolicyJSON: policyThreeRaw, PolicyDigest: policyThreeDigest, PolicyChanged: true}, nil
		}
		return ActivityPresenceResponse{}, nil
	}}
	transport := activityTestTransport(t, fixture, activityRouteSourceForFixture(fixture),
		&activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}, fixture.workspaces,
		&activityTestClientFactory{client: client})
	activity := activityTestPresence(activityTestIDOne, time.Date(2026, 8, 28, 10, 5, 30, 5, time.UTC))
	if err := transport.SendPresence(context.Background(), fixture.bindings[0].Workspace.Scope, activity); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !bytes.Equal(requests[0].ActivityJSON, requests[1].ActivityJSON) ||
		requests[0].ActivityDigest != requests[1].ActivityDigest {
		t.Fatalf("presence policy retry changed immutable evidence: %+v", requests)
	}
	if requests[0].PolicyVersion != 1 || requests[0].PolicyDigest != policyOneDigest ||
		requests[1].PolicyVersion != 3 || requests[1].PolicyDigest != policyThreeDigest {
		t.Fatalf("presence policy pairs=%+v", requests)
	}
	current, err := fixture.activities.CurrentPolicy(context.Background(), fixture.activityKey[0])
	if err != nil {
		t.Fatal(err)
	}
	if current.Policy.PolicyVersion != 3 || current.PolicyDigest != policyThreeDigest {
		t.Fatalf("current policy=(%d,%s), want v3", current.Policy.PolicyVersion, current.PolicyDigest)
	}
	for _, table := range []string{"activity_ledger", "activity_ingress_receipts", "activity_lifecycle", "activity_outbound_queue", "activity_promotion_receipts"} {
		if count := activityTableCount(t, fixture, table); count != 0 {
			t.Fatalf("presence policy retry persisted %s rows=%d", table, count)
		}
	}
	_ = policyOneRaw
}

func TestActivityTransportResolvesRouteCredentialAndPolicyEveryCycle(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token-one", "keyring:rotated": "token-two"}}
	policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	var accepted int
	client := &activityTestClient{accept: func(_ context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
		accepted++
		if accepted == 1 {
			routes.mu.Lock()
			value := routes.routes[fixture.bindings[0].Workspace.Scope]
			value.profile.BaseURL = "https://rotated.example.test"
			value.profile.CredentialRef = "keyring:rotated"
			routes.routes[fixture.bindings[0].Workspace.Scope] = value
			routes.mu.Unlock()
		}
		return ActivityAcceptResponse{Receipt: activityTestReceipt(t, request, activityTestPolicy(1), int64(accepted)), PolicyJSON: policyRaw, PolicyDigest: policyDigest}, nil
	}}
	factory := &activityTestClientFactory{client: client}
	transport := activityTestTransport(t, fixture, routes, credentials, &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, factory)
	scope := fixture.bindings[0].Workspace.Scope
	for index, id := range []string{activityTestIDOne, activityTestIDTwo} {
		if err := transport.Queue(context.Background(), scope, activityTestOrdinary(id, time.Date(2026, 8, 28, 10, 6, index, index+1, time.UTC))); err != nil {
			t.Fatal(err)
		}
	}
	if err := transport.DeliverPending(context.Background(), scope, 2); err != nil {
		t.Fatal(err)
	}
	if len(factory.tokens) != 2 || fmt.Sprint(factory.tokens) != "[token-one token-two]" {
		t.Fatalf("per-cycle credentials=%v", factory.tokens)
	}
	if len(routes.reads) < 4 {
		t.Fatalf("route resolutions=%d, want queue and each network cycle", len(routes.reads))
	}
}

func TestActivityTransportMissingCredentialStopsBeforeFactory(t *testing.T) {
	for _, test := range []struct {
		name      string
		reference string
		values    map[string]string
		wantReads int
	}{
		{name: "empty reference", values: map[string]string{}, wantReads: 0},
		{name: "empty resolved token", reference: "keyring:empty", values: map[string]string{"keyring:empty": ""}, wantReads: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newActivityTransportFixture(t, 1, true)
			routes := activityRouteSourceForFixture(fixture)
			value := routes.routes[fixture.bindings[0].Workspace.Scope]
			value.profile.CredentialRef = test.reference
			routes.routes[fixture.bindings[0].Workspace.Scope] = value
			credentials := &activityTestCredentials{values: test.values}
			factory := &activityTestClientFactory{client: &activityTestClient{}}
			transport := activityTestTransport(t, fixture, routes, credentials,
				&activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, factory)
			scope := fixture.bindings[0].Workspace.Scope
			if err := transport.Queue(context.Background(), scope,
				activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 6, 30, 0, time.UTC))); err != nil {
				t.Fatal(err)
			}
			if err := transport.DeliverPending(context.Background(), scope, 1); !errors.Is(err, ErrAttentionRequired) {
				t.Fatalf("missing credential error=%v, want ErrAttentionRequired", err)
			}
			if len(credentials.reads) != test.wantReads || len(factory.profiles) != 0 {
				t.Fatalf("missing credential reached wrong boundary: reads=%v factory=%d", credentials.reads, len(factory.profiles))
			}
		})
	}
}

func TestActivityTransportBindsActorAssuranceToFabricModeBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      types.FabricMode
		assurance types.Assurance
	}{
		{name: "private actor on public Fabric", mode: types.FabricModePublic, assurance: types.AssurancePrivateAuthenticated},
		{name: "public actor on private Fabric", mode: types.FabricModePrivate, assurance: types.AssurancePublicKeyContinuity},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newActivityTransportFixture(t, 1, true)
			routes := activityRouteSourceForFixture(fixture)
			value := routes.routes[fixture.bindings[0].Workspace.Scope]
			value.profile.Mode = test.mode
			routes.routes[fixture.bindings[0].Workspace.Scope] = value
			credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
			var network int
			client := &activityTestClient{presence: func(context.Context, ActivityPresenceRequest) (ActivityPresenceResponse, error) {
				network++
				return ActivityPresenceResponse{}, nil
			}}
			factory := &activityTestClientFactory{client: client}
			transport := activityTestTransport(t, fixture, routes, credentials,
				&activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, factory)
			scope := fixture.bindings[0].Workspace.Scope
			presence := activityTestPresence(activityTestIDOne, time.Date(2026, 8, 28, 10, 6, 40, 0, time.UTC))
			presence.Actor.Assurance = test.assurance
			if err := transport.SendPresence(context.Background(), scope, presence); !errors.Is(err, projectstate.ErrInvalidActivity) {
				t.Fatalf("presence assurance mismatch=%v, want ErrInvalidActivity", err)
			}
			ordinary := activityTestOrdinary(activityTestIDTwo, time.Date(2026, 8, 28, 10, 6, 41, 0, time.UTC))
			ordinary.Actor.Assurance = test.assurance
			if err := transport.Queue(context.Background(), scope, ordinary); !errors.Is(err, projectstate.ErrInvalidActivity) {
				t.Fatalf("queue assurance mismatch=%v, want ErrInvalidActivity", err)
			}
			if len(credentials.reads) != 0 || len(factory.profiles) != 0 || network != 0 ||
				activityTableCount(t, fixture, "activity_ledger") != 0 {
				t.Fatalf("assurance mismatch reached effects: reads=%v factory=%d network=%d ledger=%d",
					credentials.reads, len(factory.profiles), network, activityTableCount(t, fixture, "activity_ledger"))
			}
		})
	}
}

func TestActivityTransportConflictBlocksOnlyExactBindingBeforeCredentialOrNetwork(t *testing.T) {
	fixture := newActivityTransportFixture(t, 2, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token-0", "keyring:activity-1": "token-1"}}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{fixture.bindings[0].Workspace.Scope: true}}
	policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	var network int
	client := &activityTestClient{accept: func(_ context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
		network++
		return ActivityAcceptResponse{Receipt: activityTestReceipt(t, request, activityTestPolicy(1), int64(network)), PolicyJSON: policyRaw, PolicyDigest: policyDigest}, nil
	}}
	transport := activityTestTransport(t, fixture, routes, credentials, gate, &activityTestClientFactory{client: client})
	for index, binding := range fixture.bindings {
		if err := transport.Queue(context.Background(), binding.Workspace.Scope,
			activityTestOrdinary([]string{activityTestIDOne, activityTestIDTwo}[index], time.Date(2026, 8, 28, 10, 7, index, 0, time.UTC))); err != nil {
			t.Fatal(err)
		}
	}
	if err := transport.DeliverPending(context.Background(), fixture.bindings[0].Workspace.Scope, 1); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
		t.Fatalf("conflicted binding error=%v", err)
	}
	if len(credentials.reads) != 0 || network != 0 {
		t.Fatalf("conflicted binding reached credential/network: reads=%v network=%d", credentials.reads, network)
	}
	if err := transport.DeliverPending(context.Background(), fixture.bindings[1].Workspace.Scope, 1); err != nil {
		t.Fatal(err)
	}
	if network != 1 || fmt.Sprint(credentials.reads) != "[keyring:activity-1]" {
		t.Fatalf("sibling delivery=(network=%d reads=%v)", network, credentials.reads)
	}
}

func TestActivityTransportConflictRecheckIsAtomicWithAckAndPull(t *testing.T) {
	t.Run("acknowledge", func(t *testing.T) {
		fixture := newActivityTransportFixture(t, 1, true)
		policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
		client := &activityTestClient{accept: func(_ context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
			return ActivityAcceptResponse{Receipt: activityTestReceipt(t, request, activityTestPolicy(1), 1), PolicyJSON: policyRaw, PolicyDigest: policyDigest}, nil
		}}
		gate := &activityLateConflictGate{store: fixture.store, workspaces: fixture.workspaces, scope: fixture.bindings[0].Workspace.Scope}
		transport := activityTestTransport(t, fixture, activityRouteSourceForFixture(fixture),
			&activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}, gate,
			&activityTestClientFactory{client: client})
		activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 7, 30, 0, time.UTC))
		if err := transport.Queue(context.Background(), gate.scope, activity); err != nil {
			t.Fatal(err)
		}
		if err := transport.DeliverPending(context.Background(), gate.scope, 1); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
			t.Fatalf("late-conflict acknowledgement=%v, want ErrWorkspaceConflicted", err)
		}
		var receipts, delivered int
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_ingress_receipts`).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_outbound_queue WHERE state='delivered'`).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if receipts != 0 || delivered != 0 {
			t.Fatalf("late conflict converged acknowledgement: receipts=%d delivered=%d", receipts, delivered)
		}
	})

	t.Run("pull", func(t *testing.T) {
		fixture := newActivityTransportFixture(t, 1, true)
		policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
		delivery := activityTestDelivery(t,
			activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 7, 31, 0, time.UTC)),
			types.WorkspaceID("00000000-0000-4000-8000-000000000099"), 1, activityTestPolicy(1))
		client := &activityTestClient{pull: func(context.Context, ActivityPullRequest) (ActivityPullResponse, error) {
			return ActivityPullResponse{PolicyJSON: policyRaw, PolicyDigest: policyDigest,
				Deliveries: []localstore.ActivityPullDelivery{delivery}, NextSequence: 1}, nil
		}}
		gate := &activityLateConflictGate{store: fixture.store, workspaces: fixture.workspaces, scope: fixture.bindings[0].Workspace.Scope}
		transport := activityTestTransport(t, fixture, activityRouteSourceForFixture(fixture),
			&activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}, gate,
			&activityTestClientFactory{client: client})
		if err := transport.Pull(context.Background(), gate.scope, 1); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
			t.Fatalf("late-conflict pull=%v, want ErrWorkspaceConflicted", err)
		}
		cursor, err := fixture.activities.Cursor(context.Background(), fixture.activityKey[0])
		if err != nil {
			t.Fatal(err)
		}
		if cursor != 0 || activityTableCount(t, fixture, "activity_ledger") != 0 {
			t.Fatalf("late conflict converged pull: cursor=%d ledger=%d", cursor, activityTableCount(t, fixture, "activity_ledger"))
		}
	})
}

func TestActivityTransportNeverAppliesOperationOrAdvancesStream(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	routes := activityRouteSourceForFixture(fixture)
	credentials := &activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}}
	policyRaw, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	client := &activityTestClient{accept: func(_ context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
		return ActivityAcceptResponse{Receipt: activityTestReceipt(t, request, activityTestPolicy(1), 1), PolicyJSON: policyRaw, PolicyDigest: policyDigest}, nil
	}}
	transport := activityTestTransport(t, fixture, routes, credentials, fixture.workspaces, &activityTestClientFactory{client: client})
	var beforeStream int64
	if err := fixture.store.DB().QueryRow(`SELECT stream_version FROM fabric_cursors WHERE project_id=? AND workspace_id=?`, fixture.activityKey[0].ProjectID, fixture.activityKey[0].WorkspaceID).Scan(&beforeStream); err != nil {
		t.Fatal(err)
	}
	var beforeOperations, beforePortableQueue int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM workspace_overlay_operations`).Scan(&beforeOperations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM sync_queue`).Scan(&beforePortableQueue); err != nil {
		t.Fatal(err)
	}
	scope := fixture.bindings[0].Workspace.Scope
	if err := transport.Queue(context.Background(), scope, activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 8, 0, 5, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if err := transport.DeliverPending(context.Background(), scope, 1); err != nil {
		t.Fatal(err)
	}
	var afterStream int64
	var afterOperations, afterPortableQueue int
	if err := fixture.store.DB().QueryRow(`SELECT stream_version FROM fabric_cursors WHERE project_id=? AND workspace_id=?`, fixture.activityKey[0].ProjectID, fixture.activityKey[0].WorkspaceID).Scan(&afterStream); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM workspace_overlay_operations`).Scan(&afterOperations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM sync_queue`).Scan(&afterPortableQueue); err != nil {
		t.Fatal(err)
	}
	if beforeStream != afterStream || beforeOperations != afterOperations || beforePortableQueue != afterPortableQueue {
		t.Fatalf("portable state changed: stream %d->%d operations %d->%d queue %d->%d", beforeStream, afterStream, beforeOperations, afterOperations, beforePortableQueue, afterPortableQueue)
	}
}

func TestActivityTransportErrorsRedactSecretsBytesActorsAndCompleteRoutes(t *testing.T) {
	fixture := newActivityTransportFixture(t, 1, true)
	scope := fixture.bindings[0].Workspace.Scope
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 9, 0, 6, time.UTC))
	activityRaw, _ := projectstate.CanonicalActivity(activity)
	forbidden := []string{
		"super-secret-token", fixture.profiles[0].CredentialRef, fixture.bindings[0].AttachmentRef,
		fixture.profiles[0].ProfileID, fixture.profiles[0].BaseURL,
		fixture.bindings[0].Workspace.Scope.ProjectID, string(fixture.bindings[0].Workspace.Scope.WorkspaceID),
		fixture.bindings[0].RemoteProjectID, fixture.bindings[0].FabricInstanceID, fixture.bindings[0].StreamID,
		fixture.bindings[0].CanonicalRef, activityTestAgentID, activity.ID, "task moved", string(activityRaw),
	}
	gate := &activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}
	tests := []struct {
		name string
		call func() error
	}{
		{name: "route", call: func() error {
			routes := activityRouteSourceForFixture(fixture)
			routes.err = errors.New(strings.Join(forbidden, "|"))
			transport := activityTestTransport(t, fixture, routes, &activityTestCredentials{}, gate, &activityTestClientFactory{})
			return transport.Queue(context.Background(), scope, activity)
		}},
		{name: "credential", call: func() error {
			routes := activityRouteSourceForFixture(fixture)
			credentials := &activityTestCredentials{err: errors.New(strings.Join(forbidden, "|"))}
			transport := activityTestTransport(t, fixture, routes, credentials, gate, &activityTestClientFactory{})
			if err := transport.Queue(context.Background(), scope, activity); err != nil {
				return err
			}
			return transport.DeliverPending(context.Background(), scope, 1)
		}},
		{name: "factory", call: func() error {
			routes := activityRouteSourceForFixture(fixture)
			credentials := &activityTestCredentials{values: map[string]string{fixture.profiles[0].CredentialRef: "super-secret-token"}}
			factory := &activityTestClientFactory{err: errors.New(strings.Join(forbidden, "|"))}
			transport := activityTestTransport(t, fixture, routes, credentials, gate, factory)
			return transport.DeliverPending(context.Background(), scope, 1)
		}},
		{name: "network", call: func() error {
			routes := activityRouteSourceForFixture(fixture)
			credentials := &activityTestCredentials{values: map[string]string{fixture.profiles[0].CredentialRef: "super-secret-token"}}
			client := &activityTestClient{accept: func(context.Context, ActivityAcceptRequest) (ActivityAcceptResponse, error) {
				return ActivityAcceptResponse{}, errors.New(strings.Join(forbidden, "|"))
			}}
			transport := activityTestTransport(t, fixture, routes, credentials, gate, &activityTestClientFactory{client: client})
			return transport.DeliverPending(context.Background(), scope, 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("error=nil")
			}
			for _, value := range forbidden {
				if value != "" && strings.Contains(err.Error(), value) {
					t.Fatalf("error exposed forbidden value %q: %q", value, err)
				}
			}
		})
	}
}

func TestActivityTransportErrorsPreserveOnlySafeCauses(t *testing.T) {
	safeCauses := []error{
		context.Canceled,
		context.DeadlineExceeded,
		projectstate.ErrInvalidActivity,
		projectstate.ErrUnknownActivityVersion,
		projectstate.ErrInvalidActivityPolicy,
		localstore.ErrActivityPolicyUnavailable,
		localstore.ErrActivityPolicyChanged,
		localstore.ErrActivityNotFound,
		localstore.ErrActivityReplayConflict,
		localstore.ErrActivityCursorConflict,
		localstore.ErrActivityLifecycleConflict,
		localstore.ErrWorkspaceConflicted,
	}
	for index, cause := range safeCauses {
		t.Run(fmt.Sprintf("cause-%02d", index), func(t *testing.T) {
			const forbidden = "secret-token|attachment|complete-route|actor-bytes"
			wrapped := fmt.Errorf("%s: %w", forbidden, cause)

			routeFixture := newActivityTransportFixture(t, 1, true)
			routes := activityRouteSourceForFixture(routeFixture)
			routes.err = wrapped
			routeTransport := activityTestTransport(t, routeFixture, routes, &activityTestCredentials{},
				&activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, &activityTestClientFactory{})
			routeErr := routeTransport.Queue(context.Background(), routeFixture.bindings[0].Workspace.Scope,
				activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 9, 30, 0, time.UTC)))
			if !errors.Is(routeErr, cause) || strings.Contains(routeErr.Error(), forbidden) {
				t.Fatalf("route classified error=%q, want safe cause %v without evidence", routeErr, cause)
			}

			fabricFixture := newActivityTransportFixture(t, 1, true)
			client := &activityTestClient{accept: func(context.Context, ActivityAcceptRequest) (ActivityAcceptResponse, error) {
				return ActivityAcceptResponse{}, wrapped
			}}
			fabricTransport := activityTestTransport(t, fabricFixture, activityRouteSourceForFixture(fabricFixture),
				&activityTestCredentials{values: map[string]string{"keyring:activity-0": "token"}},
				&activityTestConflictGate{open: map[types.WorkspaceScope]bool{}}, &activityTestClientFactory{client: client})
			if err := fabricTransport.Queue(context.Background(), fabricFixture.bindings[0].Workspace.Scope,
				activityTestOrdinary(activityTestIDOne, time.Date(2026, 8, 28, 10, 9, 31, 0, time.UTC))); err != nil {
				t.Fatal(err)
			}
			fabricErr := fabricTransport.DeliverPending(context.Background(), fabricFixture.bindings[0].Workspace.Scope, 1)
			if !errors.Is(fabricErr, cause) || strings.Contains(fabricErr.Error(), forbidden) {
				t.Fatalf("Fabric classified error=%q, want safe cause %v without evidence", fabricErr, cause)
			}
		})
	}
}
