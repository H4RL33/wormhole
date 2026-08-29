package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type publicSyncFixture struct {
	store       *Store
	db          *sql.DB
	projectID   string
	fabricID    string
	streamID    string
	workspaceID string
	attachment  string
	humanID     string
	agentID     string
	publicKey   ed25519.PublicKey
	fingerprint string
	now         time.Time
}

func newPublicSyncFixture(t *testing.T) publicSyncFixture {
	t.Helper()
	s := testStore(t)
	db := s.db
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(publicKey)
	f := publicSyncFixture{
		store: s, db: db,
		fabricID:    "11111111-1111-4111-8111-111111112231",
		streamID:    "22222222-2222-4222-8222-222222222231",
		workspaceID: "33333333-3333-4333-8333-333333333231",
		attachment:  freshAttachmentRef(t),
		humanID:     freshAttachmentRef(t),
		agentID:     freshAttachmentRef(t),
		publicKey:   publicKey, fingerprint: "sha256:" + hex.EncodeToString(sum[:]),
		now: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := db.QueryRow(`INSERT INTO projects(name,owner) VALUES($1,'test') RETURNING id`, "public-sync-"+t.Name()).Scan(&f.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agents(id,owner,model) VALUES($1,'test','test-model')`, f.agentID); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err = db.Exec(`INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES($1,$2,'github','2231','https://github.com/test/public','refs/heads/main','public')`, f.projectID, f.fabricID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,current_version,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES($1,$2,$3,'refs/heads/main','refs/heads/main',0,$4::text,$4::text,$5::text)`, f.projectID, f.fabricID, f.streamID, digest, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest) VALUES($1,$2,$3,'refs/heads/main',0,'initial',$5::text,'[]',$4::text,'[]',$4::text)`, f.projectID, f.fabricID, f.streamID, digest, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES($1,$2,$3,$4,$5,'github','2231','refs/heads/main','refs/heads/main',true)`, f.projectID, f.fabricID, f.streamID, f.workspaceID, f.attachment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM projects WHERE id=$1`, f.projectID) })
	return f
}

func freshAttachmentRef(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func (f publicSyncFixture) observedActor(kind types.ActorKind, id string) projectstate.ActorV1 {
	return projectstate.ActorV1{SchemaVersion: 1, Kind: "actor", ID: id, ActorKind: kind, DisplayName: "Tracked actor", PublicKeys: []projectstate.PublicKeyV1{{KeyID: "tracked-key", Algorithm: "ed25519", PublicKeyBase64: base64.StdEncoding.EncodeToString(f.publicKey)}}, Extensions: projectstate.ExtensionsV1{}}
}

func (f publicSyncFixture) activation() PublicHumanActivation {
	return PublicHumanActivation{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", SourceVersion: 0,
		ObservedHuman:  f.observedActor(types.ActorHuman, f.humanID),
		TransportActor: types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: f.humanID, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: f.now},
		KeyFingerprint: f.fingerprint, PublicKey: [ed25519.PublicKeySize]byte(f.publicKey),
	}
}

func (f publicSyncFixture) begin(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestActivatePublicHumanThenNonceThenIssuerClaimSatisfiesImmediateFKs(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	scope, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation())
	if err != nil || scope.Actor.HumanPrincipalID != f.humanID {
		t.Fatalf("activation scope=%+v err=%v", scope, err)
	}
	claim := PublicNonceUse{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", KeyFingerprint: f.fingerprint, Claim: PublicNonceClaim{NonceHash: strings.Repeat("b", 64), ExpiresAt: f.now.Add(5 * time.Minute)}}
	if err := f.store.ConsumePublicNonceInTx(context.Background(), tx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND fabric_instance_id=$3 AND attachment_ref=$4`, f.fingerprint, f.projectID, f.fabricID, f.attachment); err != nil {
		t.Fatalf("claim issuer after key+nonce: %v", err)
	}
	if _, err := tx.Exec(`RESET ROLE`); err != nil {
		t.Fatalf("reset runtime role for owner readback: %v", err)
	}
	var keys, nonces, claimed int
	if err := tx.QueryRow(`SELECT (SELECT count(*) FROM fabric_public_actor_keys WHERE project_id=$1),(SELECT count(*) FROM public_request_nonces WHERE project_id=$1),(SELECT count(*) FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND public_issuer_key_fingerprint IS NOT NULL)`, f.projectID).Scan(&keys, &nonces, &claimed); err != nil {
		t.Fatal(err)
	}
	if keys != 1 || nonces != 1 || claimed != 1 {
		t.Fatalf("rows key=%d nonce=%d claimed=%d", keys, nonces, claimed)
	}
}

func TestActivatePublicHumanRejectsZeroMultipleAgentAndMismatchedKeys(t *testing.T) {
	f := newPublicSyncFixture(t)
	tests := map[string]func(*PublicHumanActivation){
		"zero key": func(in *PublicHumanActivation) { in.ObservedHuman.PublicKeys = []projectstate.PublicKeyV1{} },
		"duplicate matching key": func(in *PublicHumanActivation) {
			in.ObservedHuman.PublicKeys = append(in.ObservedHuman.PublicKeys, in.ObservedHuman.PublicKeys[0])
		},
		"agent actor":       func(in *PublicHumanActivation) { in.ObservedHuman.ActorKind = types.ActorAgent },
		"human mismatch":    func(in *PublicHumanActivation) { in.ObservedHuman.ID = "55555555-5555-4555-8555-555555555299" },
		"empty fingerprint": func(in *PublicHumanActivation) { in.KeyFingerprint = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			tx := f.begin(t)
			in := f.activation()
			mutate(&in)
			if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, in); !errors.Is(err, ErrInvalidPublicIdentity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestActivatePublicHumanRollsBackWhenFirstNonceFails(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation()); err != nil {
		t.Fatal(err)
	}
	claim := PublicNonceUse{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", KeyFingerprint: f.fingerprint, Claim: PublicNonceClaim{NonceHash: strings.Repeat("d", 64), ExpiresAt: f.now.Add(5 * time.Minute)}}
	if err := f.store.ConsumePublicNonceInTx(context.Background(), tx, claim); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConsumePublicNonceInTx(context.Background(), tx, claim); !errors.Is(err, ErrPublicNonceReplay) {
		t.Fatalf("second nonce error=%v, want replay", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var keys, nonces int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM fabric_public_actor_keys WHERE project_id=$1),(SELECT count(*) FROM public_request_nonces WHERE project_id=$1)`, f.projectID).Scan(&keys, &nonces); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || nonces != 0 {
		t.Fatalf("failed first nonce retained keys=%d nonces=%d", keys, nonces)
	}
}

func TestPublicNonceReplayAndConcurrentUseHaveExactlyOneWinner(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim := PublicNonceUse{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", KeyFingerprint: f.fingerprint, Claim: PublicNonceClaim{NonceHash: strings.Repeat("c", 64), ExpiresAt: f.now.Add(5 * time.Minute)}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
			if err == nil {
				_, err = tx.ExecContext(context.Background(), `SET LOCAL ROLE wormhole_fabric_runtime`)
			}
			if err == nil {
				err = f.store.ConsumePublicNonceInTx(context.Background(), tx, claim)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	winners, replays := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrPublicNonceReplay):
			replays++
		default:
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if winners != 1 || replays != 1 {
		t.Fatalf("winners=%d replays=%d", winners, replays)
	}
}

func TestPublicAgentSessionExactRetryConflictAndExpiry(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND attachment_ref=$3`, f.fingerprint, f.projectID, f.attachment); err != nil {
		t.Fatal(err)
	}
	issue := PublicAgentSessionIssue{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AgentID: f.agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", SourceVersion: 0, IssuedAt: f.now}
	first, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil || replay.SessionID != first.SessionID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := issue
	changed.HarnessVersion = "2"
	if _, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, changed); !errors.Is(err, ErrPublicSessionConflict) {
		t.Fatalf("changed metadata error=%v", err)
	}
	issue.IssuedAt = first.ExpiresAt
	second, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil || second.SessionID == first.SessionID {
		t.Fatalf("replacement=%+v err=%v", second, err)
	}
	var revokedAt time.Time
	if err := tx.QueryRow(`SELECT revoked_at FROM fabric_public_agent_sessions WHERE session_id=$1`, first.SessionID).Scan(&revokedAt); err != nil || !revokedAt.Equal(first.ExpiresAt) {
		t.Fatalf("old revoked_at=%v err=%v, want %v", revokedAt, err, first.ExpiresAt)
	}
}

func TestPublicAgentSessionConcurrentExactIssueReturnsOneDurableSession(t *testing.T) {
	f := newPublicSyncFixture(t)
	setup := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), setup, f.activation()); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND attachment_ref=$3`, f.fingerprint, f.projectID, f.attachment); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(); err != nil {
		t.Fatal(err)
	}
	issue := PublicAgentSessionIssue{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AgentID: f.agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", SourceVersion: 0, IssuedAt: f.now}
	results := make(chan PublicAgentSession, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
			if err == nil {
				_, err = tx.ExecContext(context.Background(), `SET LOCAL ROLE wormhole_fabric_runtime`)
			}
			var result PublicAgentSession
			if err == nil {
				result, err = f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var sessionID string
	for result := range results {
		if sessionID == "" {
			sessionID = result.SessionID
		}
		if result.SessionID != sessionID {
			t.Fatalf("concurrent session IDs %q and %q", sessionID, result.SessionID)
		}
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1 AND attachment_ref=$2 AND agent_id=$3`, f.projectID, f.attachment, f.agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable sessions=%d, want 1", count)
	}
}

func TestRevalidateCurrentAndHistoricalPublicAuthority(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	humanScope, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND attachment_ref=$3`, f.fingerprint, f.projectID, f.attachment); err != nil {
		t.Fatal(err)
	}
	issue := PublicAgentSessionIssue{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AgentID: f.agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", SourceVersion: 0, IssuedAt: f.now}
	session, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectstate.Snapshot{Actors: map[string]projectstate.Record[projectstate.ActorV1]{f.humanID: {Value: ptrActor(f.observedActor(types.ActorHuman, f.humanID))}, f.agentID: {Value: ptrActor(f.observedActor(types.ActorAgent, f.agentID))}}, Tasks: map[string]projectstate.Record[projectstate.TaskV1]{}, TaskLinks: map[string]projectstate.Record[projectstate.TaskLinkV1]{}, Articles: map[string]projectstate.KBRecord{}, Channels: map[string]projectstate.Record[projectstate.ChannelV1]{}, Events: map[string]projectstate.EventV1{}, GitLinks: map[string]projectstate.Record[projectstate.GitLinkV1]{}}
	evidence := PublicAuthorityEvidence{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AttachmentSourceVersion: 0, CurrentStreamVersion: 0, Accepted: snapshot}
	authority := MutationAuthority{Scope: types.ActorScope{ProjectID: f.projectID, Actor: types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: f.agentID, AccountableHumanID: f.humanID, SessionID: session.SessionID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", Assurance: types.AssurancePublicKeyContinuity, OccurredAt: f.now.Add(time.Minute)}}, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, SessionID: session.SessionID}
	current, err := f.store.RevalidateMutationAuthorityInTx(context.Background(), tx, authority, evidence)
	if err != nil || current.Actor.AgentID != f.agentID || current.Actor.AccountableHumanID != f.humanID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	historical, err := f.store.ResolveHistoricalPublicSessionActorInTx(context.Background(), tx, f.fabricID, session.SessionID, f.now.Add(time.Hour))
	if err != nil || historical.SessionID != session.SessionID {
		t.Fatalf("historical=%+v err=%v", historical, err)
	}
	if _, err := f.store.ResolveHistoricalPublicSessionActorInTx(context.Background(), tx, f.fabricID, session.SessionID, session.ExpiresAt); !errors.Is(err, ErrPublicAuthentication) {
		t.Fatalf("expiry-boundary history error=%v, want authentication failure", err)
	}
	humanAuthority := MutationAuthority{Scope: humanScope, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint}
	if _, err := f.store.RevalidateMutationAuthorityInTx(context.Background(), tx, humanAuthority, evidence); err != nil {
		t.Fatalf("human revalidation: %v", err)
	}
}

func ptrActor(value projectstate.ActorV1) *projectstate.ActorV1 { return &value }
