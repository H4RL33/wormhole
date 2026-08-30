package mcp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/google/uuid"
)

func TestActivityAuthorizationAPISurface(t *testing.T) {
	var _ func(*PublicBoundProofResolver, context.Context, string, string, json.RawMessage, types.PublicRequestProof) (ActivityMutationAuthority, error) = (*PublicBoundProofResolver).AuthorizeActivityMutation
	var _ func(*PublicBoundProofResolver, context.Context, string, string, json.RawMessage, types.PublicRequestProof, ActivityReadFunc) error = (*PublicBoundProofResolver).ResolveActivityRead
}

func TestResolveAttachmentAuthorityInTxUsesExactAttachmentRef(t *testing.T) {
	f := newMutationFixture(t)
	a := f.attach(1)
	raw := canonicalMutationJSON(t, []byte(`{"version":1,"attachment_ref":"`+a.Attachment.AttachmentRef+`"}`))
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.activity.pull", raw, a.Attachment.AttachmentRef, f.transport.OccurredAt, bytesOf(61, 32), seed[:])
	r := realBoundResolverForDB(t, f, publicRuntimeDB(t))
	v, err := r.verifier.VerifyBound("wormhole.activity.pull", a.Attachment.AttachmentRef, raw, proof)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := r.identity.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := r.resolveAttachmentAuthorityInTx(context.Background(), tx, f.projectID, uuid.NewString(), v); !errors.Is(err, coregit.ErrStreamNotFound) {
		t.Fatalf("error=%v", err)
	}
}

func TestResolveActivityReadRejectsNilCallback(t *testing.T) {
	f := newMutationFixture(t)
	a := f.attach(1)
	raw := canonicalMutationJSON(t, []byte(`{"version":1,"attachment_ref":"`+a.Attachment.AttachmentRef+`"}`))
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.activity.pull", raw, a.Attachment.AttachmentRef, f.transport.OccurredAt, bytesOf(62, 32), seed[:])
	resolver := realBoundResolverForDB(t, f, publicRuntimeDB(t))
	if err := resolver.ResolveActivityRead(context.Background(), "wormhole.activity.pull", a.Attachment.AttachmentRef, raw, proof, nil); !errors.Is(err, identity.ErrInvalidPublicIdentity) {
		t.Fatalf("error=%v", err)
	}
}

func TestAuthorizeActivityMutationCommitsNonce(t *testing.T) {
	f := newMutationFixture(t)
	a := f.attach(1)
	raw := canonicalMutationJSON(t, []byte(`{"version":1,"attachment_ref":"`+a.Attachment.AttachmentRef+`"}`))
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "activity.accept", raw, a.Attachment.AttachmentRef, f.transport.OccurredAt, bytesOf(63, 32), seed[:])
	resolver := realBoundResolverForDB(t, f, publicRuntimeDB(t))
	var before int
	if err := f.db.QueryRow(`SELECT count(*) FROM public_request_nonces WHERE project_id=$1`, f.projectID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	got, err := resolver.AuthorizeActivityMutation(context.Background(), "activity.accept", a.Attachment.AttachmentRef, raw, proof)
	if err != nil {
		t.Fatal(err)
	}
	if got.Authority.AttachmentRef != a.Attachment.AttachmentRef {
		t.Fatalf("attachment=%q", got.Authority.AttachmentRef)
	}
	var after int
	if err := f.db.QueryRow(`SELECT count(*) FROM public_request_nonces WHERE project_id=$1`, f.projectID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after-before != 1 {
		t.Fatalf("nonce delta=%d", after-before)
	}
}

func TestResolveActivityReadCommitsNonceOnlyOnSuccessfulCallback(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[failed], func(t *testing.T) {
			f := newMutationFixture(t)
			attached := f.attach(1)
			db := publicRuntimeDB(t)
			resolver := realBoundResolverForDB(t, f, db)
			raw := canonicalMutationJSON(t, []byte(`{"version":1,"attachment_ref":"`+attached.Attachment.AttachmentRef+`"}`))
			seed := sha256.Sum256([]byte(f.projectID))
			proof := signedBoundProof(t, f.fabricID, "wormhole.activity.pull", raw, attached.Attachment.AttachmentRef, f.transport.OccurredAt, bytesOf(62, 32), seed[:])
			var before int
			if err := f.db.QueryRow(`SELECT count(*) FROM public_request_nonces WHERE project_id=$1`, f.projectID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			beforeCounts := mutationCounts(t, f.db, f.projectID)
			stop := errors.New("stop")
			err := resolver.ResolveActivityRead(context.Background(), "wormhole.activity.pull", attached.Attachment.AttachmentRef, raw, proof, func(ctx context.Context, tx *sql.Tx, read VerifiedActivityRead) error {
				if read.Authority.ProjectID != f.projectID || read.Attachment.AttachmentRef != attached.Attachment.AttachmentRef {
					t.Fatalf("wrong bound read: %+v", read)
				}
				var isolation, readOnly, project string
				if err := tx.QueryRowContext(ctx, `SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only'),current_setting('wormhole.project_id',true)`).Scan(&isolation, &readOnly, &project); err != nil {
					return err
				}
				if isolation != "repeatable read" || readOnly != "off" || project != f.projectID {
					t.Fatalf("tx=(%s,%s,%s)", isolation, readOnly, project)
				}
				if failed {
					return stop
				}
				return nil
			})
			if failed && !errors.Is(err, stop) {
				t.Fatalf("error=%v", err)
			}
			if !failed && err != nil {
				t.Fatal(err)
			}
			var after int
			if err := f.db.QueryRow(`SELECT count(*) FROM public_request_nonces WHERE project_id=$1`, f.projectID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if delta := after - before; delta != map[bool]int{false: 1, true: 0}[failed] {
				t.Fatalf("nonce delta=%d", delta)
			}
			afterCounts := mutationCounts(t, f.db, f.projectID)
			for table, n := range beforeCounts {
				if table != "public_request_nonces" && afterCounts[table] != n {
					t.Fatalf("%s changed: %d -> %d", table, n, afterCounts[table])
				}
			}
		})
	}
}

func TestResolveActivityReadForcedRLSHidesSecondProject(t *testing.T) {
	f := newMutationFixture(t)
	a := f.attach(9)
	other := newMutationFixture(t)
	otherA := other.attach(10)
	raw := canonicalMutationJSON(t, []byte(`{"version":1,"attachment_ref":"`+a.Attachment.AttachmentRef+`"}`))
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.activity.pull", raw, a.Attachment.AttachmentRef, f.transport.OccurredAt, bytesOf(64, 32), seed[:])
	r := realBoundResolverForDB(t, f, publicRuntimeDB(t))
	err := r.ResolveActivityRead(context.Background(), "wormhole.activity.pull", a.Attachment.AttachmentRef, raw, proof, func(ctx context.Context, tx *sql.Tx, _ VerifiedActivityRead) error {
		var project, workspace, ref string
		err := tx.QueryRowContext(ctx, `SELECT project_id,workspace_id,canonical_ref FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND attachment_ref=$2`, other.projectID, otherA.Attachment.AttachmentRef).Scan(&project, &workspace, &ref)
		if !errors.Is(err, sql.ErrNoRows) {
			return errors.New("forced RLS exposed second project")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
