package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const mutationTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type mutationFixture struct {
	t           *testing.T
	db          *sql.DB
	coordinator *MutationCoordinator
	projectID   string
	fabricID    string
	repository  types.RepositoryIdentity
	actor       projectstate.ActorV1
	transport   types.ActorEnvelope
	publicKey   [ed25519.PublicKeySize]byte
	fingerprint string
	tree        projectstate.Tree
	observation coregit.RefObservation
	policy      projectstate.EffectiveActivityPolicyV1
}

func newMutationFixture(t *testing.T) *mutationFixture {
	t.Helper()
	db := testDB(t)
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil || version != 22 || dirty {
		t.Fatalf("schema_migrations = (%d,%v,%v), want (22,false,nil)", version, dirty, err)
	}
	projectID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO projects(id,name,owner) VALUES($1,$2,$3)`, projectID, "mutation-"+projectID[:8], "mutation-test"); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		cleanupDB, err := sql.Open("postgres", types.LoadConfig().DatabaseURL)
		if err == nil {
			_, _ = cleanupDB.Exec(`DELETE FROM projects WHERE id=$1`, projectID)
			_ = cleanupDB.Close()
		}
	})
	fabricID := uuid.NewString()
	repository := types.RepositoryIdentity{
		Provider:        "github",
		ImmutableID:     "123456789",
		CanonicalRemote: "https://github.com/wormhole/" + projectID,
	}
	if _, err := db.Exec(`INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES($1,$2,$3,$4,$5,'refs/heads/main','public')`, projectID, fabricID, repository.Provider, repository.ImmutableID, repository.CanonicalRemote); err != nil {
		t.Fatalf("seed repository binding: %v", err)
	}
	seed := sha256.Sum256([]byte(projectID))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicBytes := privateKey.Public().(ed25519.PublicKey)
	var publicKey [ed25519.PublicKeySize]byte
	copy(publicKey[:], publicBytes)
	keyDigest := sha256.Sum256(publicBytes)
	fingerprint := "sha256:" + hex.EncodeToString(keyDigest[:])
	humanID := uuid.NewString()
	actor := projectstate.ActorV1{
		SchemaVersion: 1,
		Kind:          "actor",
		ID:            humanID,
		ActorKind:     types.ActorHuman,
		DisplayName:   "Mutation Human",
		PublicKeys: []projectstate.PublicKeyV1{{
			KeyID:           fingerprint,
			Algorithm:       "ed25519",
			PublicKeyBase64: base64.StdEncoding.EncodeToString(publicBytes),
		}},
		Extensions: projectstate.ExtensionsV1{},
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	transport := types.ActorEnvelope{
		ActorKind:        types.ActorHuman,
		HumanPrincipalID: humanID,
		Assurance:        types.AssurancePublicKeyContinuity,
		OccurredAt:       now,
	}
	snapshot := projectstate.Snapshot{
		Config: projectstate.ConfigV1{
			SnapshotVersion: 1,
			ProjectID:       projectID,
			Handle:          types.ProjectHandle{Namespace: "wormhole", Name: "mutation"},
			Repository:      repository,
		},
		Project: projectstate.ProjectV1{
			SchemaVersion: 1,
			Kind:          "project",
			ID:            projectID,
			Name:          "Mutation Test",
			Aliases:       []string{},
			CreatedAt:     now.Add(-time.Hour),
			UpdatedAt:     now.Add(-time.Hour),
			Extensions:    projectstate.ExtensionsV1{},
		},
		Actors:    map[string]projectstate.Record[projectstate.ActorV1]{humanID: {Value: &actor}},
		Tasks:     map[string]projectstate.Record[projectstate.TaskV1]{},
		TaskLinks: map[string]projectstate.Record[projectstate.TaskLinkV1]{},
		Articles:  map[string]projectstate.KBRecord{},
		Channels:  map[string]projectstate.Record[projectstate.ChannelV1]{},
		Events:    map[string]projectstate.EventV1{},
		GitLinks:  map[string]projectstate.Record[projectstate.GitLinkV1]{},
	}
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	coordinator, err := NewMutationCoordinator(identity.NewStore(db), coregit.NewStreamStore(db), coregit.NewActivityStore(db))
	if err != nil {
		t.Fatalf("NewMutationCoordinator: %v", err)
	}
	return &mutationFixture{
		t: t, db: db, coordinator: coordinator, projectID: projectID, fabricID: fabricID,
		repository: repository, actor: actor, transport: transport, publicKey: publicKey,
		fingerprint: fingerprint, tree: tree,
		observation: coregit.RefObservation{Repository: repository, RefName: "refs/heads/main", CommitSHA: mutationTestCommit, ObservedAt: now},
		policy: projectstate.EffectiveActivityPolicyV1{
			SchemaVersion: 1, PolicyVersion: 1, OrdinaryMaxAgeSeconds: 2_592_000,
			OrdinaryMaxRows: 10_000, TerminalDefaultAgeSeconds: 2_592_000,
			TerminalMaximumAgeSeconds: 31_536_000, TerminalRetentionSeconds: 2_592_000,
		},
	}
}

func (f *mutationFixture) command(nonceByte byte) InitialAttachCommand {
	f.t.Helper()
	digest, err := projectstate.DigestTree(f.tree)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := json.Marshal(SyncAttachV2Args{
		Version: 2, Repository: f.repository, CanonicalRef: f.observation.RefName,
		BaseCommitSHA: f.observation.CommitSHA, BaseTreeDigest: digest,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	canonical := canonicalMutationJSON(f.t, raw)
	return InitialAttachCommand{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, Repository: f.repository,
		CanonicalRef: f.observation.RefName, Observation: f.observation, ObservedTree: f.tree,
		ObservedHuman: f.actor, TransportActor: f.transport, KeyFingerprint: f.fingerprint,
		PublicKey: f.publicKey, Nonce: identity.PublicNonceClaim{NonceHash: strings.Repeat(fmt.Sprintf("%x", nonceByte&0xf), 64), ExpiresAt: f.transport.OccurredAt.Add(5 * time.Minute)},
		Policy: f.policy, CanonicalRequest: canonical,
	}
}

func canonicalMutationJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func (f *mutationFixture) attach(nonceByte byte) InitialAttachResult {
	f.t.Helper()
	result, err := f.coordinator.ExecuteInitialAttach(context.Background(), f.command(nonceByte))
	if err != nil {
		f.t.Fatalf("ExecuteInitialAttach: %v", err)
	}
	return result
}

func (f *mutationFixture) authority(result InitialAttachResult) identity.MutationAuthority {
	return identity.MutationAuthority{
		Scope:                types.ActorScope{ProjectID: f.projectID, Actor: f.transport},
		FabricInstanceID:     result.Attachment.Key.FabricInstanceID,
		StreamID:             result.Attachment.Key.StreamID,
		WorkspaceID:          result.Attachment.WorkspaceID,
		CanonicalRef:         result.Attachment.CanonicalRef,
		AttachmentRef:        result.Attachment.AttachmentRef,
		IssuerKeyFingerprint: result.Attachment.IssuerKeyFingerprint,
	}
}

func mutationCounts(t *testing.T, db *sql.DB, projectID string) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{
		"fabric_streams", "fabric_stream_versions", "fabric_workspace_stream_bindings",
		"fabric_activity_policy_versions", "fabric_activity_policy_current",
		"fabric_public_actor_keys", "public_request_nonces", "audit_log",
	} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id=$1`, projectID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func TestMutationCoordinatorRejectsInvalidCanonicalPayloadBeforeSQL(t *testing.T) {
	coordinator, err := NewMutationCoordinator(identity.NewStore(nil), coregit.NewStreamStore(nil), coregit.NewActivityStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	validAuthority := identity.MutationAuthority{Scope: types.ActorScope{}}
	for name, payload := range map[string][]byte{
		"empty":      nil,
		"whitespace": []byte(`{ "ok": true }`),
		"duplicate":  []byte(`{"ok":true,"ok":true}`),
		"trailing":   []byte(`{"ok":true}{}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := coordinator.Execute(context.Background(), validAuthority, "sync.test", payload, func(context.Context, *sql.Tx, VerifiedMutation) error { return nil }); err == nil {
				t.Fatal("Execute accepted invalid payload")
			}
		})
	}
}

func authorizeFixtureMutation(t *testing.T, f *mutationFixture, attached InitialAttachResult, nonceByte byte) (PublicMutationAuthority, *MutationCoordinator, []byte) {
	t.Helper()
	scope := boundReadArguments(attached, 0).SyncV2Scope
	raw, proof := mutationAuthorizationRequest(t, f, scope, nonceByte, "")
	runtimeDB := publicRuntimeDB(t)
	authorized, err := realBoundResolverForDB(t, f, runtimeDB).AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
	if err != nil {
		t.Fatalf("AuthorizeMutation: %v", err)
	}
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), coregit.NewActivityStore(runtimeDB))
	if err != nil {
		t.Fatal(err)
	}
	return authorized, coordinator, raw
}

func authorizeFixtureAgentMutation(t *testing.T, f *mutationFixture, nonceByte byte) (PublicMutationAuthority, *MutationCoordinator, []byte, identity.PublicAgentSession) {
	t.Helper()
	snapshot, err := projectstate.DecodeTree(f.tree)
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.NewString()
	agent := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: agentID, ActorKind: types.ActorAgent,
		DisplayName: "Mutation Agent", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	snapshot.Actors[agentID] = projectstate.Record[projectstate.ActorV1]{Value: &agent}
	f.tree, err = projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attached := f.attach(nonceByte)
	tx, err := f.coordinator.identity.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.coordinator.identity.IssuePublicAgentSessionInTx(context.Background(), tx, identity.PublicAgentSessionIssue{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: attached.Attachment.Key.StreamID,
		WorkspaceID: attached.Attachment.WorkspaceID, CanonicalRef: attached.Attachment.CanonicalRef,
		AttachmentRef: attached.Attachment.AttachmentRef, IssuerKeyFingerprint: f.fingerprint,
		AgentID: agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5",
		SourceVersion: attached.Attachment.SourceVersion, IssuedAt: f.transport.OccurredAt,
	})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	scope := boundReadArguments(attached, 0).SyncV2Scope
	raw, _ := mutationAuthorizationRequest(t, f, scope, nonceByte+1, "")
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundSessionProof(t, f.fabricID, "wormhole.sync.push", raw, scope.AttachmentRef, session.SessionID, f.transport.OccurredAt, bytesOf(nonceByte+1, 32), seed[:])
	runtimeDB := publicRuntimeDB(t)
	authorized, err := realBoundResolverForDB(t, f, runtimeDB).AuthorizeMutation(context.Background(), "wormhole.sync.push", raw, scope, proof)
	if err != nil {
		t.Fatalf("AuthorizeMutation agent: %v", err)
	}
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), coregit.NewActivityStore(runtimeDB))
	if err != nil {
		t.Fatal(err)
	}
	return authorized, coordinator, raw, session
}

func TestMutationCoordinatorExecutePublicRejectsInvalidCommandBeforeMutationSQL(t *testing.T) {
	coordinator, err := NewMutationCoordinator(identity.NewStore(nil), coregit.NewStreamStore(nil), coregit.NewActivityStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	authorized := PublicMutationAuthority{
		Authority: identity.MutationAuthority{
			Scope: types.ActorScope{ProjectID: "00000000-0000-4000-8000-000000000001", Actor: types.ActorEnvelope{
				ActorKind: types.ActorHuman, HumanPrincipalID: "00000000-0000-4000-8000-000000000002",
				Assurance: types.AssurancePublicKeyContinuity, OccurredAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
			}},
		},
		SignedScope: SyncV2Scope{Version: projectstate.SyncProtocolVersionV2},
	}
	for _, assurance := range []types.Assurance{types.AssuranceLocal, types.AssuranceLegacy, types.AssuranceUnknown, types.AssurancePrivateAuthenticated} {
		t.Run(string(assurance), func(t *testing.T) {
			candidate := authorized
			candidate.Authority.Scope.Actor.Assurance = assurance
			called := false
			err := coordinator.ExecutePublic(context.Background(), candidate, "sync.push", []byte(`{"version":2}`), func(context.Context, *sql.Tx, VerifiedMutation) error {
				called = true
				return nil
			})
			if !errors.Is(err, errInvalidMutation) || called {
				t.Fatalf("ExecutePublic = (called=%v, error=%v), want errInvalidMutation before SQL", called, err)
			}
		})
	}
	t.Run("session mismatch", func(t *testing.T) {
		candidate := authorized
		candidate.Authority.SessionID = "00000000-0000-4000-8000-000000000003"
		called := false
		err := coordinator.ExecutePublic(context.Background(), candidate, "sync.push", []byte(`{"version":2}`), func(context.Context, *sql.Tx, VerifiedMutation) error {
			called = true
			return nil
		})
		if !errors.Is(err, errInvalidMutation) || called {
			t.Fatalf("ExecutePublic = (called=%v, error=%v), want errInvalidMutation before SQL", called, err)
		}
	})
}

func TestMutationCoordinatorExecutePublicRevalidatesFreshAttachmentIssuerSessionAndScope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *mutationFixture, *PublicMutationAuthority)
		want   error
	}{
		{name: "non-writable attachment", want: identity.ErrPublicAuthentication, mutate: func(t *testing.T, f *mutationFixture, authorized *PublicMutationAuthority) {
			if _, err := f.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, authorized.Authority.AttachmentRef); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "revoked issuer", want: identity.ErrPublicAuthentication, mutate: func(t *testing.T, f *mutationFixture, authorized *PublicMutationAuthority) {
			if _, err := f.db.Exec(`UPDATE fabric_public_actor_keys SET revoked_at=now() WHERE project_id=$1 AND key_fingerprint=$2`, f.projectID, authorized.Authority.IssuerKeyFingerprint); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "signed repository", want: coregit.ErrStreamPrecondition, mutate: func(_ *testing.T, _ *mutationFixture, authorized *PublicMutationAuthority) {
			authorized.SignedScope.Repository.ImmutableID = "987654321"
		}},
		{name: "signed ref", want: coregit.ErrStreamPrecondition, mutate: func(_ *testing.T, _ *mutationFixture, authorized *PublicMutationAuthority) {
			authorized.SignedScope.CanonicalRef = "refs/heads/other"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(11)
			authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 12)
			test.mutate(t, f, &authorized)
			before := task2MutationSnapshot(t, f.db, f.projectID)
			called := false
			err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(context.Context, *sql.Tx, VerifiedMutation) error {
				called = true
				return nil
			})
			if !errors.Is(err, test.want) || called {
				t.Fatalf("ExecutePublic = (called=%v, error=%v), want %v before callback", called, err, test.want)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *mutationFixture, identity.PublicAgentSession)
	}{
		{name: "revoked agent session", mutate: func(t *testing.T, f *mutationFixture, session identity.PublicAgentSession) {
			if _, err := f.db.Exec(`UPDATE fabric_public_agent_sessions SET revoked_at=now() WHERE project_id=$1 AND session_id=$2`, f.projectID, session.SessionID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "altered session actor", mutate: func(t *testing.T, f *mutationFixture, session identity.PublicAgentSession) {
			if _, err := f.db.Exec(`UPDATE fabric_public_agent_sessions SET agent_id=$1 WHERE project_id=$2 AND session_id=$3`, uuid.NewString(), f.projectID, session.SessionID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newMutationFixture(t)
			authorized, coordinator, raw, session := authorizeFixtureAgentMutation(t, f, 28)
			test.mutate(t, f, session)
			before := task2MutationSnapshot(t, f.db, f.projectID)
			called := false
			err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(context.Context, *sql.Tx, VerifiedMutation) error {
				called = true
				return nil
			})
			if !errors.Is(err, identity.ErrPublicAuthentication) || called {
				t.Fatalf("ExecutePublic = (called=%v, error=%v), want authentication failure before callback", called, err)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestMutationCoordinatorExecutePublicRejectsRemovedAcceptedTrackedHuman(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(35)
	authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 36)
	removeAcceptedTrackedHuman(t, f.db, attached, f.actor.ID)
	before := task2MutationSnapshot(t, f.db, f.projectID)
	called := false
	err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(context.Context, *sql.Tx, VerifiedMutation) error {
		called = true
		return nil
	})
	if !errors.Is(err, identity.ErrPublicAuthentication) || called {
		t.Fatalf("ExecutePublic = (called=%v, error=%v), want authentication failure before callback", called, err)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
}

func TestMutationCoordinatorExecutePublicDistinctNonceRaceRevalidatesFreshAttachment(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(32)
	scope := boundReadArguments(attached, 0).SyncV2Scope
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, f, runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), coregit.NewActivityStore(runtimeDB))
	if err != nil {
		t.Fatal(err)
	}
	authorized := make([]PublicMutationAuthority, 2)
	raw := make([]json.RawMessage, 2)
	for index := range authorized {
		var proof types.PublicRequestProof
		raw[index], proof = mutationAuthorizationRequest(t, f, scope, byte(33+index), "")
		authorized[index], err = resolver.AuthorizeMutation(context.Background(), "wormhole.sync.push", raw[index], scope, proof)
		if err != nil {
			t.Fatalf("AuthorizeMutation %d: %v", index, err)
		}
	}
	before := task2MutationSnapshot(t, f.db, f.projectID)
	callbackCalls := 0
	var callbackMu sync.Mutex
	errs := raceAtRealAttachmentLock(t, f.db, coordinator, coregit.AttachmentLookup{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, AttachmentRef: attached.Attachment.AttachmentRef,
	}, []func() error{
		func() error {
			return coordinator.ExecutePublic(context.Background(), authorized[0], "sync.push.race.0", raw[0], func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
				callbackMu.Lock()
				callbackCalls++
				callbackMu.Unlock()
				_, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef)
				return err
			})
		},
		func() error {
			return coordinator.ExecutePublic(context.Background(), authorized[1], "sync.push.race.1", raw[1], func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
				callbackMu.Lock()
				callbackCalls++
				callbackMu.Unlock()
				_, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef)
				return err
			})
		},
	})
	winners, denied := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, identity.ErrPublicAuthentication):
			denied++
		default:
			t.Fatalf("ExecutePublic race %d error = %v", index, err)
		}
	}
	if winners != 1 || denied != 1 || callbackCalls != 1 {
		t.Fatalf("race outcomes winners=%d denied=%d callbacks=%d", winners, denied, callbackCalls)
	}
	after := task2MutationSnapshot(t, f.db, f.projectID)
	for _, table := range task2MutationTables {
		switch table {
		case "fabric_workspace_stream_bindings":
			if len(after[table]) != len(before[table]) {
				t.Errorf("%s row count changed", table)
			}
		case "audit_log":
			if len(after[table]) != len(before[table])+1 {
				t.Errorf("audit row delta=%d, want 1", len(after[table])-len(before[table]))
			}
		default:
			if !reflect.DeepEqual(after[table], before[table]) {
				t.Errorf("%s changed across distinct-nonce race", table)
			}
		}
	}
	var writable bool
	if err := f.db.QueryRow(`SELECT writable FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef).Scan(&writable); err != nil || writable {
		t.Fatalf("post-race writable = (%v, %v), want false,nil", writable, err)
	}
}

func TestMutationCoordinatorExecutePublicBurnedNonceSurvivesCallbackAndAuditFailure(t *testing.T) {
	for _, auditFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback", true: "audit"}[auditFailure], func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(13)
			authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 14)
			action := "sync.push.callback_failure"
			want := errors.New("forced callback failure")
			if auditFailure {
				action = "sync.push.audit_failure"
				installAuditFailure(t, f.db, f.projectID, action)
			}
			before := task2MutationSnapshot(t, f.db, f.projectID)
			err := coordinator.ExecutePublic(context.Background(), authorized, action, raw, func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
				if _, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef); err != nil {
					return err
				}
				if !auditFailure {
					return want
				}
				return nil
			})
			if auditFailure {
				if err == nil || !strings.Contains(err.Error(), "forced mutation audit failure") {
					t.Fatalf("ExecutePublic error = %v, want forced audit failure", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("ExecutePublic error = %v, want callback sentinel", err)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestMutationCoordinatorExecutePublicRechecksMutableEvidenceInCoreCallback(t *testing.T) {
	tests := map[string]func(*SyncV2Scope){
		"base commit": func(scope *SyncV2Scope) { scope.BaseCommitSHA = strings.Repeat("b", 40) },
		"base tree": func(scope *SyncV2Scope) {
			scope.BaseTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("b", 64))
		},
		"stream version": func(scope *SyncV2Scope) { scope.ExpectedStreamVersion++ },
		"live tree": func(scope *SyncV2Scope) {
			scope.ExpectedLiveTreeDigest = projectstate.Digest("sha256:" + strings.Repeat("c", 64))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(30)
			authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 31)
			mutate(&authorized.SignedScope)
			before := task2MutationSnapshot(t, f.db, f.projectID)
			called := false
			err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
				called = true
				_, err := coordinator.streams.ApplyPublicOperationInTx(ctx, tx, verified.Scope, coregit.ApplyPublicOperationInput{
					Attachment: verified.Attachment,
					Precondition: coregit.SyncPrecondition{
						Repository: authorized.SignedScope.Repository, CanonicalRef: authorized.SignedScope.CanonicalRef,
						BaseCommitSHA: authorized.SignedScope.BaseCommitSHA, BaseTreeDigest: authorized.SignedScope.BaseTreeDigest,
						ExpectedStreamVersion:  authorized.SignedScope.ExpectedStreamVersion,
						ExpectedLiveTreeDigest: authorized.SignedScope.ExpectedLiveTreeDigest,
					},
					Operation: mutationPutActorOperation(f, attached.State, authorized.SignedScope.ExpectedLiveTreeDigest),
				})
				return err
			})
			if !errors.Is(err, coregit.ErrStreamPrecondition) || !called {
				t.Fatalf("ExecutePublic = (called=%v, error=%v), want Core precondition rejection in callback", called, err)
			}
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
		})
	}
}

func TestMutationCoordinatorExecutePublicCommitsOneTypedAuditWithTransportActor(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(15)
	authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 16)
	if err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push", raw, func(context.Context, *sql.Tx, VerifiedMutation) error { return nil }); err != nil {
		t.Fatalf("ExecutePublic: %v", err)
	}
	digest := sha256.Sum256(raw)
	actorJSON, err := json.Marshal(authorized.Authority.Scope.Actor)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var payload, storedActor []byte
	var requestDigest, assurance string
	if err := f.db.QueryRow(`SELECT count(*),min(canonical_payload_json::text)::bytea,min(actor_envelope_json::text)::bytea,min(request_digest),min(assurance) FROM audit_log WHERE project_id=$1 AND action='sync.push'`, f.projectID).Scan(&count, &payload, &storedActor, &requestDigest, &assurance); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !bytes.Equal(payload, raw) || !bytes.Equal(storedActor, actorJSON) || requestDigest != "sha256:"+hex.EncodeToString(digest[:]) || assurance != string(types.AssurancePublicKeyContinuity) {
		t.Fatalf("audit = count %d payload %s actor %s digest %s assurance %s", count, payload, storedActor, requestDigest, assurance)
	}
}

func TestMutationCoordinatorExecutePublicDeferredCommitRejectionRollsBackDomainAndAudit(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(26)
	authorized, coordinator, raw := authorizeFixtureMutation(t, f, attached, 27)
	if _, err := f.db.Exec(`CREATE FUNCTION wormhole_test_reject_deferred_mutation_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'sync.push.deferred_commit_failure' THEN
				RAISE EXCEPTION 'forced deferred mutation commit failure';
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE CONSTRAINT TRIGGER wormhole_test_reject_deferred_mutation_commit
		AFTER INSERT ON audit_log
		DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW EXECUTE FUNCTION wormhole_test_reject_deferred_mutation_commit()`); err != nil {
		t.Fatalf("install deferred commit trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS wormhole_test_reject_deferred_mutation_commit ON audit_log;
			DROP FUNCTION IF EXISTS wormhole_test_reject_deferred_mutation_commit()`)
	})
	before := task2MutationSnapshot(t, f.db, f.projectID)
	err := coordinator.ExecutePublic(context.Background(), authorized, "sync.push.deferred_commit_failure", raw, func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
		_, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "forced deferred mutation commit failure") {
		t.Fatalf("ExecutePublic error = %v, want deferred commit failure", err)
	}
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, f.db, f.projectID), 0)
}

func TestExecuteInitialAttachRejectsNoncanonicalEvidenceBeforeSQL(t *testing.T) {
	coordinator, err := NewMutationCoordinator(identity.NewStore(nil), coregit.NewStreamStore(nil), coregit.NewActivityStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*InitialAttachCommand){
		"zero":           func(*InitialAttachCommand) {},
		"unknown JSON":   func(c *InitialAttachCommand) { c.CanonicalRequest = []byte(`{"version":2,"extra":true}`) },
		"duplicate JSON": func(c *InitialAttachCommand) { c.CanonicalRequest = []byte(`{"version":2,"version":2}`) },
	} {
		t.Run(name, func(t *testing.T) {
			var command InitialAttachCommand
			mutate(&command)
			if _, err := coordinator.ExecuteInitialAttach(context.Background(), command); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
}

func TestExecuteInitialAttachRejectsInvalidEvidenceBeforeSQL(t *testing.T) {
	f := newMutationFixture(t)
	coordinator, err := NewMutationCoordinator(identity.NewStore(nil), coregit.NewStreamStore(nil), coregit.NewActivityStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*InitialAttachCommand){
		"repository": func(c *InitialAttachCommand) { c.Repository = types.RepositoryIdentity{} },
		"ref":        func(c *InitialAttachCommand) { c.Observation.RefName, c.CanonicalRef = "main", "main" },
		"commit":     func(c *InitialAttachCommand) { c.Observation.CommitSHA = "not-a-commit" },
		"tree": func(c *InitialAttachCommand) {
			c.ObservedTree = projectstate.Tree{{Path: "../bad", Data: []byte("bad")}}
		},
		"fingerprint":   func(c *InitialAttachCommand) { c.KeyFingerprint = "sha256:" + strings.Repeat("f", 64) },
		"public key":    func(c *InitialAttachCommand) { c.PublicKey[0] ^= 1 },
		"nonce hash":    func(c *InitialAttachCommand) { c.Nonce.NonceHash = strings.Repeat("A", 64) },
		"nonce expiry":  func(c *InitialAttachCommand) { c.Nonce.ExpiresAt = c.Nonce.ExpiresAt.Add(time.Second) },
		"tracked human": func(c *InitialAttachCommand) { c.ObservedHuman.ID = uuid.NewString() },
		"policy":        func(c *InitialAttachCommand) { c.Policy.TerminalRetentionSeconds = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			command := f.command(12)
			mutate(&command)
			if _, err := coordinator.ExecuteInitialAttach(context.Background(), command); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
				t.Fatalf("error = %v, want ErrInvalidPublicIdentity", err)
			}
		})
	}
}

func TestExecuteInitialAttachFirstActivationUsesForeignKeySafeOrder(t *testing.T) {
	f := newMutationFixture(t)
	before := mutationCounts(t, f.db, f.projectID)
	result := f.attach(1)
	after := mutationCounts(t, f.db, f.projectID)
	if result.Attachment.Key.ProjectID != f.projectID || result.Attachment.Key.FabricInstanceID != f.fabricID ||
		result.Attachment.CanonicalRef != f.observation.RefName || result.Attachment.Repository != f.repository ||
		result.Attachment.IssuerKeyFingerprint != f.fingerprint || !result.Attachment.Writable || result.State.Version != 0 || result.Policy != f.policy {
		t.Fatalf("attach result = %+v", result)
	}
	for table, want := range map[string]int{
		"fabric_streams": 1, "fabric_stream_versions": 1, "fabric_workspace_stream_bindings": 1,
		"fabric_activity_policy_versions": 1, "fabric_activity_policy_current": 1,
		"fabric_public_actor_keys": 1, "public_request_nonces": 1, "audit_log": 1,
	} {
		if after[table]-before[table] != want {
			t.Errorf("%s delta = %d, want %d", table, after[table]-before[table], want)
		}
	}
}

func TestMutationCoordinatorCommitsDomainAndTypedAuditTogether(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(2)
	authority := f.authority(attached)
	payload := []byte(`{"operation":"coordinator-commit"}`)
	callback := false
	err := f.coordinator.Execute(context.Background(), authority, "sync.coordinator.commit", payload, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		callback = true
		if verified.Attachment != attached.Attachment || verified.State.Version != attached.State.Version || verified.Scope.ProjectID != f.projectID {
			t.Fatalf("verified mutation = %+v", verified)
		}
		_, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=writable WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef)
		return err
	})
	if err != nil || !callback {
		t.Fatalf("Execute = callback %v, error %v", callback, err)
	}
	var count int
	var stored []byte
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1 AND action='sync.coordinator.commit'`, f.projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT canonical_payload_json FROM audit_log WHERE project_id=$1 AND action='sync.coordinator.commit'`, f.projectID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !bytes.Equal(stored, payload) {
		t.Fatalf("audit = count %d payload %s", count, stored)
	}
}

func TestMutationCoordinatorRevalidatesCompleteBoundAuthorityBeforeCallback(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(3)
	base := f.authority(attached)
	for name, mutate := range map[string]func(*identity.MutationAuthority){
		"project":       func(a *identity.MutationAuthority) { a.Scope.ProjectID = uuid.NewString() },
		"fabric":        func(a *identity.MutationAuthority) { a.FabricInstanceID = uuid.NewString() },
		"stream":        func(a *identity.MutationAuthority) { a.StreamID = uuid.NewString() },
		"workspace":     func(a *identity.MutationAuthority) { a.WorkspaceID = uuid.NewString() },
		"canonical ref": func(a *identity.MutationAuthority) { a.CanonicalRef = "refs/heads/other" },
		"attachment":    func(a *identity.MutationAuthority) { a.AttachmentRef = uuid.NewString() },
		"issuer":        func(a *identity.MutationAuthority) { a.IssuerKeyFingerprint = "sha256:" + strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			authority := base
			mutate(&authority)
			called := false
			err := f.coordinator.Execute(context.Background(), authority, "sync.route.reject", []byte(`{"ok":true}`), func(context.Context, *sql.Tx, VerifiedMutation) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("Execute = called %v, error %v; want rejection before callback", called, err)
			}
		})
	}

	if _, err := f.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef); err != nil {
		t.Fatal(err)
	}
	called := false
	err := f.coordinator.Execute(context.Background(), base, "sync.route.readonly", []byte(`{"ok":true}`), func(context.Context, *sql.Tx, VerifiedMutation) error {
		called = true
		return nil
	})
	if !errors.Is(err, identity.ErrPublicAuthentication) || called {
		t.Fatalf("non-writable route = called %v, error %v", called, err)
	}
}

func TestMutationCoordinatorCallbackFailureRollsBackWithoutAudit(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(4)
	want := errors.New("callback failed")
	err := f.coordinator.Execute(context.Background(), f.authority(attached), "sync.callback.failure", []byte(`{"ok":true}`), func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
		if _, err := tx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=false WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef); err != nil {
			return err
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want callback sentinel", err)
	}
	var writable bool
	var audit int
	if err := f.db.QueryRow(`SELECT writable FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND attachment_ref=$2`, f.projectID, attached.Attachment.AttachmentRef).Scan(&writable); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1 AND action='sync.callback.failure'`, f.projectID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !writable || audit != 0 {
		t.Fatalf("rollback writable=%v audit=%d", writable, audit)
	}
}

func TestExecuteInitialAttachExactRetryConsumesNonceBeforeReadOnlyReplay(t *testing.T) {
	f := newMutationFixture(t)
	first := f.attach(5)
	tx, err := f.coordinator.identity.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := f.coordinator.streams.AdvanceAcceptedObservedRefInTx(context.Background(), tx, types.ActorScope{ProjectID: f.projectID, Actor: f.transport}, coregit.AdvanceAcceptedInput{
		Key:  first.Attachment.Key,
		Ref:  coregit.RefObservation{Repository: f.repository, RefName: first.Attachment.CanonicalRef, CommitSHA: strings.Repeat("b", 40), ObservedAt: f.observation.ObservedAt.Add(time.Minute)},
		Tree: f.tree, ExpectedVersion: first.State.Version,
		ExpectedAcceptedCommitSHA: first.State.AcceptedCommitSHA, ExpectedAcceptedTreeDigest: first.State.Accepted.Digest,
		ExpectedLiveTreeDigest: first.State.Live.Digest,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("advance before replay: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := mutationCounts(t, f.db, f.projectID)
	retry, err := f.coordinator.ExecuteInitialAttach(context.Background(), f.command(6))
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	after := mutationCounts(t, f.db, f.projectID)
	if retry.Attachment != first.Attachment || retry.State.Version != advanced.Version || retry.State.Version <= first.State.Version || retry.Policy != first.Policy {
		t.Fatalf("retry = %+v, want attachment %+v current version %d", retry, first.Attachment, advanced.Version)
	}
	for table := range before {
		wantDelta := 0
		if table == "public_request_nonces" {
			wantDelta = 1
		}
		if after[table]-before[table] != wantDelta {
			t.Errorf("%s retry delta=%d, want %d", table, after[table]-before[table], wantDelta)
		}
	}
}

func TestExecuteInitialAttachDeniedRetryConsumesNonceWithoutDomainMutation(t *testing.T) {
	f := newMutationFixture(t)
	_ = f.attach(7)
	before := mutationCounts(t, f.db, f.projectID)
	changed := f.command(8)
	changed.Observation.CommitSHA = strings.Repeat("b", 40)
	args := SyncAttachV2Args{Version: 2, Repository: changed.Repository, CanonicalRef: changed.CanonicalRef, BaseCommitSHA: changed.Observation.CommitSHA}
	args.BaseTreeDigest, _ = projectstate.DigestTree(changed.ObservedTree)
	raw, _ := json.Marshal(args)
	changed.CanonicalRequest = canonicalMutationJSON(t, raw)
	_, err := f.coordinator.ExecuteInitialAttach(context.Background(), changed)
	if !errors.Is(err, coregit.ErrPublicAttachReplay) && !errors.Is(err, coregit.ErrStreamPrecondition) {
		t.Fatalf("changed retry error = %v", err)
	}
	after := mutationCounts(t, f.db, f.projectID)
	for table := range before {
		wantDelta := 0
		if table == "public_request_nonces" {
			wantDelta = 1
		}
		if after[table]-before[table] != wantDelta {
			t.Errorf("%s denied retry delta=%d, want %d", table, after[table]-before[table], wantDelta)
		}
	}
}

func TestExecuteInitialAttachConcurrentDistinctNoncesHaveOneAttachmentWinner(t *testing.T) {
	f := newMutationFixture(t)
	commands := []InitialAttachCommand{f.command(9), f.command(10)}
	results := make([]InitialAttachResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range commands {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.coordinator.ExecuteInitialAttach(context.Background(), commands[i])
		}(index)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent attach %d: %v", i, err)
		}
	}
	if results[0].Attachment != results[1].Attachment {
		t.Fatalf("attachments differ: %+v / %+v", results[0].Attachment, results[1].Attachment)
	}
	counts := mutationCounts(t, f.db, f.projectID)
	for table, want := range map[string]int{
		"fabric_streams": 1, "fabric_stream_versions": 1, "fabric_workspace_stream_bindings": 1,
		"fabric_activity_policy_versions": 1, "fabric_activity_policy_current": 1,
		"fabric_public_actor_keys": 1, "public_request_nonces": 2, "audit_log": 1,
	} {
		if counts[table] != want {
			t.Errorf("%s count=%d, want %d", table, counts[table], want)
		}
	}
}

func installAuditFailure(t *testing.T, db *sql.DB, projectID, action string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "mutation_audit_fail_" + suffix
	functionName := schemaName + ".reject_audit"
	triggerName := "mutation_audit_fail_tr_" + suffix
	statement := fmt.Sprintf(`CREATE SCHEMA %s;
	CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
		IF NEW.project_id=%s::uuid AND NEW.action=%s THEN
			RAISE EXCEPTION 'forced mutation audit failure';
		END IF;
		RETURN NEW;
	END $$;
	CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`,
		schemaName, functionName, quoteLiteral(projectID), quoteLiteral(action), triggerName, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("install audit failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON audit_log; DROP SCHEMA IF EXISTS %s CASCADE`, triggerName, schemaName))
	})
}

func waitForBlockedMutationSessions(t *testing.T, adminDB *sql.DB, blockerPID int, want int) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked int
		err := adminDB.QueryRow(`WITH RECURSIVE blocked(pid) AS (
			SELECT pid FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)) AND state='active' AND wait_event_type='Lock'
			UNION
			SELECT activity.pid FROM pg_stat_activity activity JOIN blocked upstream
			ON upstream.pid = ANY(pg_blocking_pids(activity.pid))
			WHERE activity.state='active' AND activity.wait_event_type='Lock'
		)
		SELECT count(*) FROM blocked`, blockerPID).Scan(&blocked)
		if err != nil {
			return fmt.Errorf("read blocked mutation sessions: %w", err)
		}
		if blocked >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("blocked mutation sessions=%d, want at least %d", blocked, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func raceAtRealAttachmentLock(t *testing.T, adminDB *sql.DB, coordinator *MutationCoordinator, lookup coregit.AttachmentLookup, calls []func() error) []error {
	t.Helper()
	if len(calls) != 2 {
		t.Fatalf("race call count=%d, want 2", len(calls))
	}
	blocker, err := coordinator.identity.BeginProjectTx(context.Background(), lookup.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var blockerPID int
	if err := blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.streams.LockAttachmentInTx(context.Background(), blocker, lookup); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, len(calls))
	errs := make([]error, len(calls))
	var wait sync.WaitGroup
	for index := range calls {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			ready <- struct{}{}
			errs[index] = calls[index]()
		}(index)
	}
	for range calls {
		<-ready
	}
	if err := waitForBlockedMutationSessions(t, adminDB, blockerPID, len(calls)); err != nil {
		_ = blocker.Rollback()
		wait.Wait()
		t.Fatalf("%v; call errors after release=%v", err, errs)
	}
	if err := blocker.Rollback(); err != nil {
		wait.Wait()
		t.Fatal(err)
	}
	wait.Wait()
	return errs
}

func removeAcceptedTrackedHuman(t *testing.T, db *sql.DB, attached InitialAttachResult, humanID string) {
	t.Helper()
	snapshot := attached.State.Accepted
	delete(snapshot.Actors, humanID)
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatalf("encode accepted snapshot without tracked human: %v", err)
	}
	stored, err := coregit.EncodeStoredTree(tree)
	if err != nil {
		t.Fatalf("encode stored snapshot without tracked human: %v", err)
	}
	digest, err := projectstate.DigestTree(tree)
	if err != nil {
		t.Fatalf("digest snapshot without tracked human: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	versionResult, err := tx.Exec(`UPDATE fabric_stream_versions
		SET canonical_live_tree=$1,live_tree_digest=$2,canonical_accepted_tree=$1,accepted_tree_digest=$2
		WHERE project_id=$3 AND fabric_instance_id=$4 AND stream_id=$5 AND canonical_ref=$6 AND version=$7`,
		stored, string(digest), attached.Attachment.Key.ProjectID, attached.Attachment.Key.FabricInstanceID,
		attached.Attachment.Key.StreamID, attached.Attachment.CanonicalRef, attached.State.Version)
	if err != nil {
		t.Fatalf("rewrite accepted stream version without tracked human: %v", err)
	}
	streamResult, err := tx.Exec(`UPDATE fabric_streams SET live_tree_digest=$1,accepted_tree_digest=$1
		WHERE project_id=$2 AND fabric_instance_id=$3 AND stream_id=$4 AND canonical_ref=$5`,
		string(digest), attached.Attachment.Key.ProjectID, attached.Attachment.Key.FabricInstanceID,
		attached.Attachment.Key.StreamID, attached.Attachment.CanonicalRef)
	if err != nil {
		t.Fatalf("rewrite current stream summary without tracked human: %v", err)
	}
	for name, result := range map[string]sql.Result{"version": versionResult, "stream": streamResult} {
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			t.Fatalf("rewrite %s rows = (%d, %v), want (1,nil)", name, rows, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	readTx, err := identity.NewStore(db).BeginProjectTx(context.Background(), attached.Attachment.Key.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	altered, err := coregit.NewStreamStore(db).LockAttachmentInTx(context.Background(), readTx, coregit.AttachmentLookup{
		ProjectID: attached.Attachment.Key.ProjectID, FabricInstanceID: attached.Attachment.Key.FabricInstanceID,
		AttachmentRef: attached.Attachment.AttachmentRef,
	})
	if err != nil {
		t.Fatalf("read altered accepted snapshot: %v", err)
	}
	if _, exists := altered.State.Accepted.Actors[humanID]; exists {
		t.Fatal("tracked human remains in altered accepted snapshot")
	}
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func mutationProjectDigest(t *testing.T, db *sql.DB, projectID string) string {
	t.Helper()
	var digest string
	err := db.QueryRow(`SELECT md5(jsonb_build_object(
		'bindings',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.attachment_ref),'[]'::jsonb) FROM fabric_workspace_stream_bindings x WHERE x.project_id=$1),
		'streams',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.stream_id),'[]'::jsonb) FROM fabric_streams x WHERE x.project_id=$1),
		'versions',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.stream_id,x.version),'[]'::jsonb) FROM fabric_stream_versions x WHERE x.project_id=$1),
		'requests',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.operation_id),'[]'::jsonb) FROM fabric_stream_requests x WHERE x.project_id=$1),
		'conflicts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.conflict_id),'[]'::jsonb) FROM fabric_stream_conflicts x WHERE x.project_id=$1),
		'sessions',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.session_id),'[]'::jsonb) FROM fabric_public_agent_sessions x WHERE x.project_id=$1),
		'activity',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.activity_id),'[]'::jsonb) FROM fabric_activity_ingress_receipts x WHERE x.project_id=$1),
		'lifecycle',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.activity_id,x.lifecycle_kind),'[]'::jsonb) FROM fabric_activity_lifecycle x WHERE x.project_id=$1),
		'audit',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.seq),'[]'::jsonb) FROM audit_log x WHERE x.project_id=$1)
	)::text)`, projectID).Scan(&digest)
	if err != nil {
		t.Fatalf("project digest: %v", err)
	}
	return digest
}

func mutationPutActorOperation(f *mutationFixture, state coregit.StreamTransition, expected projectstate.Digest) projectstate.OperationV1 {
	actorID := uuid.NewString()
	record := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: actorID, ActorKind: types.ActorAgent,
		DisplayName: "Rollback Agent", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: uuid.NewString(), Kind: projectstate.OperationPutRecord,
		ExpectedViewDigest: expected, Actor: f.transport,
		PutRecord: &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Actor: &record}},
	}
}

func mutationPrecondition(f *mutationFixture, result InitialAttachResult) coregit.SyncPrecondition {
	return coregit.SyncPrecondition{
		Repository: f.repository, CanonicalRef: result.Attachment.CanonicalRef,
		BaseCommitSHA: result.State.AcceptedCommitSHA, BaseTreeDigest: result.State.Accepted.Digest,
		ExpectedStreamVersion: result.State.Version, ExpectedLiveTreeDigest: result.State.Live.Digest,
	}
}

func mutationLifecycleInput(f *mutationFixture, attached InitialAttachResult) coregit.AcceptActivityInput {
	activityID := uuid.NewString()
	activity := projectstate.ActivityV1{
		SchemaVersion: 1, ID: activityID, Class: projectstate.ActivityLifecycleV1,
		Actor: f.transport, CreatedAt: f.transport.OccurredAt,
		Lifecycle: &projectstate.ActivityLifecycleProjectionV1{Kind: projectstate.ActivityLifecycleDeliveryV1, ReferenceID: uuid.NewString()},
	}
	policyDigest, err := projectstate.DigestActivityPolicy(f.policy)
	if err != nil {
		f.t.Fatal(err)
	}
	return coregit.AcceptActivityInput{
		Key: coregit.FabricActivityOriginKey{
			Stream:            coregit.FabricActivityStreamKey{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: attached.Attachment.Key.StreamID, CanonicalRef: attached.Attachment.CanonicalRef},
			SourceWorkspaceID: attached.Attachment.WorkspaceID, ActivityID: activityID,
		},
		Activity: activity, IssuedActor: f.transport, PolicyVersion: f.policy.PolicyVersion, PolicyDigest: policyDigest,
	}
}

func TestMutationCoordinatorRollsBackEveryMutationWhenAuditFails(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(*mutationFixture, InitialAttachResult)
		callback func(*mutationFixture, InitialAttachResult) MutationFunc
	}{
		{
			name: "public agent-session issue",
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				return func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
					_, err := f.coordinator.identity.IssuePublicAgentSessionInTx(ctx, tx, identity.PublicAgentSessionIssue{
						ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: attached.Attachment.Key.StreamID,
						WorkspaceID: attached.Attachment.WorkspaceID, CanonicalRef: attached.Attachment.CanonicalRef,
						AttachmentRef: attached.Attachment.AttachmentRef, IssuerKeyFingerprint: f.fingerprint,
						AgentID: uuid.NewString(), HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "1",
						SourceVersion: attached.Attachment.SourceVersion, IssuedAt: f.transport.OccurredAt,
					})
					return err
				}
			},
		},
		{
			name: "push applied",
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				return func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
					_, err := f.coordinator.streams.ApplyPublicOperationInTx(ctx, tx, verified.Scope, coregit.ApplyPublicOperationInput{
						Attachment: verified.Attachment, Precondition: mutationPrecondition(f, attached),
						Operation: mutationPutActorOperation(f, attached.State, attached.State.Live.Digest),
					})
					return err
				}
			},
		},
		{
			name: "push durable operation-precondition conflict",
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				return func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
					badDigest := projectstate.Digest("sha256:" + strings.Repeat("f", 64))
					_, err := f.coordinator.streams.ApplyOperationInTx(ctx, tx, verified.Scope, coregit.ApplyStreamOperationInput{
						Key: verified.Attachment.Key, WorkspaceID: verified.Attachment.WorkspaceID,
						ExpectedVersion: attached.State.Version, ExpectedTreeDigest: badDigest,
						Operation: mutationPutActorOperation(f, attached.State, badDigest),
					})
					return err
				}
			},
		},
		{
			name: "observed accepted-ref advance",
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				return func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
					_, err := f.coordinator.streams.AdvanceAcceptedObservedRefInTx(ctx, tx, verified.Scope, coregit.AdvanceAcceptedInput{
						Key:  attached.Attachment.Key,
						Ref:  coregit.RefObservation{Repository: f.repository, RefName: attached.Attachment.CanonicalRef, CommitSHA: strings.Repeat("b", 40), ObservedAt: f.observation.ObservedAt.Add(time.Minute)},
						Tree: f.tree, ExpectedVersion: attached.State.Version,
						ExpectedAcceptedCommitSHA: attached.State.AcceptedCommitSHA, ExpectedAcceptedTreeDigest: attached.State.Accepted.Digest,
						ExpectedLiveTreeDigest: attached.State.Live.Digest,
					})
					return err
				}
			},
		},
		{
			name: "named conflict resolution",
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				return func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
					conflictID := uuid.NewString()
					_, err := tx.ExecContext(ctx, `INSERT INTO fabric_stream_conflicts(project_id,fabric_instance_id,stream_id,canonical_ref,conflict_id,detected_at_version,conflict_kind,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state) VALUES($1,$2,$3,$4,$5,$6,'operation_precondition',$7,$7,$7,'{}','open')`, f.projectID, f.fabricID, attached.Attachment.Key.StreamID, attached.Attachment.CanonicalRef, conflictID, attached.State.Version, attached.State.Live.Digest)
					if err != nil {
						return err
					}
					_, err = f.coordinator.streams.ResolveConflictInTx(ctx, tx, verified.Scope, coregit.ResolveStreamConflictInput{
						Attachment: verified.Attachment, ConflictID: conflictID,
						Precondition: mutationPrecondition(f, attached),
						Resolution:   mutationPutActorOperation(f, attached.State, attached.State.Live.Digest),
					})
					return err
				}
			},
		},
		{
			name: "Activity accept",
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				input := mutationLifecycleInput(f, attached)
				return func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
					_, err := f.coordinator.activity.AcceptInTx(ctx, tx, input)
					return err
				}
			},
		},
		{
			name: "Activity lifecycle transition",
			prepare: func(f *mutationFixture, attached InitialAttachResult) {
				input := mutationLifecycleInput(f, attached)
				if _, err := f.coordinator.activity.Accept(context.Background(), input); err != nil {
					f.t.Fatalf("seed lifecycle activity: %v", err)
				}
				f.t.Cleanup(func() {})
			},
			callback: func(f *mutationFixture, attached InitialAttachResult) MutationFunc {
				// Use the persisted lifecycle row selected from this attachment so the
				// callback exercises the real caller-owned transition adapter.
				return func(ctx context.Context, tx *sql.Tx, _ VerifiedMutation) error {
					var activityID, referenceID string
					err := tx.QueryRowContext(ctx, `SELECT activity_id,reference_id FROM fabric_activity_lifecycle WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 ORDER BY activity_id LIMIT 1`, f.projectID, f.fabricID, attached.Attachment.Key.StreamID).Scan(&activityID, &referenceID)
					if err != nil {
						return err
					}
					key := coregit.FabricActivityOriginKey{Stream: coregit.FabricActivityStreamKey{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: attached.Attachment.Key.StreamID, CanonicalRef: attached.Attachment.CanonicalRef}, SourceWorkspaceID: attached.Attachment.WorkspaceID, ActivityID: activityID}
					return f.coordinator.activity.TransitionLifecycleInTx(ctx, tx, key, coregit.ActivityLifecycleTransition{Kind: "delivery", ReferenceID: referenceID, ExpectedState: "pending", NextState: "delivered"})
				}
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(byte(index + 1))
			if test.prepare != nil {
				test.prepare(f, attached)
			}
			action := "sync.audit.rollback." + fmt.Sprintf("%d", index)
			installAuditFailure(t, f.db, f.projectID, action)
			before := mutationProjectDigest(t, f.db, f.projectID)
			err := f.coordinator.Execute(context.Background(), f.authority(attached), action, []byte(`{"ok":true}`), test.callback(f, attached))
			if err == nil || !strings.Contains(err.Error(), "forced mutation audit failure") {
				t.Fatalf("Execute error = %v, want forced audit failure", err)
			}
			after := mutationProjectDigest(t, f.db, f.projectID)
			if after != before {
				t.Fatalf("project digest changed across audit rollback: %s -> %s", before, after)
			}
		})
	}
}

func TestExecuteInitialAttachAuditFailureRollsBackAllOwners(t *testing.T) {
	f := newMutationFixture(t)
	installAuditFailure(t, f.db, f.projectID, "sync.attach")
	before := mutationCounts(t, f.db, f.projectID)
	_, err := f.coordinator.ExecuteInitialAttach(context.Background(), f.command(11))
	if err == nil || !strings.Contains(err.Error(), "forced mutation audit failure") {
		t.Fatalf("ExecuteInitialAttach error = %v, want forced audit failure", err)
	}
	after := mutationCounts(t, f.db, f.projectID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("initial attach rows changed across audit rollback: before=%v after=%v", before, after)
	}
}
