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
