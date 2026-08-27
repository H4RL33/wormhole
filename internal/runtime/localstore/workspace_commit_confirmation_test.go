package localstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	sqlite "modernc.org/sqlite"
)

func TestCompactTargetedCommitConfirmationJournalAPIAndExactPrepare(t *testing.T) {
	fixture := newMaterializationFixtureWithoutJournal(t)
	journalID := "00000000-0000-4000-8000-000000000051"
	prepared := validPreparedMaterialization(t, fixture.binding, journalID)
	prepareCompactConfirmationFixture(&prepared)

	prior := captureMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID)
	if prior.targetID != journalID || prior.targetState != "" || prior.currentOwnerID != "" || prior.postimageDigest == nil {
		t.Fatalf("prepare prior=%+v, want absent target and owner with tagged postimage", prior)
	}
	next := rolledBackMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	})

	match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitPrior {
		t.Fatalf("prepare prior confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
	if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	match, err = fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitNext {
		t.Fatalf("prepare next confirmation=(%v,%v), want (Next,nil)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationJournalTransitionMatrix(t *testing.T) {
	for _, transition := range []struct {
		name string
		from string
		to   string
	}{
		{name: "publish", from: "prepared", to: "published"},
		{name: "preserve_old", from: "prepared", to: "recovered_old"},
		{name: "recover_old", from: "prepared", to: "recovered_old"},
		{name: "recover_new_from_prepared", from: "prepared", to: "recovered_new"},
		{name: "recover_new_from_published", from: "published", to: "recovered_new"},
	} {
		t.Run(transition.name, func(t *testing.T) {
			fixture := newMaterializationFixtureWithoutJournal(t)
			journalID := "00000000-0000-4000-8000-000000000051"
			prepared := validPreparedMaterialization(t, fixture.binding, journalID)
			prepareCompactConfirmationFixture(&prepared)
			if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				current, err := tx.PrepareMaterialization(context.Background(), prepared)
				if err != nil {
					return err
				}
				if transition.from == "published" {
					_, err = tx.TransitionMaterialization(context.Background(), current, "published")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}

			prior := captureMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID)
			next := rolledBackMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID, func(tx *WorkspaceMutationTx) error {
				current, err := tx.MaterializationByJournalID(context.Background(), journalID)
				if err != nil {
					return err
				}
				_, err = tx.TransitionMaterialization(context.Background(), *current, transition.to)
				return err
			})
			if next.targetState != transition.to {
				t.Fatalf("next state=%q, want %q", next.targetState, transition.to)
			}
			if transition.to == "recovered_old" && next.currentOwnerID != "" {
				t.Fatalf("recovered-old current owner=%q, want absent", next.currentOwnerID)
			}

			match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
			if err != nil || match != WorkspaceCommitPrior {
				t.Fatalf("prior confirmation=(%v,%v), want (Prior,nil)", match, err)
			}
			if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
				current, err := tx.MaterializationByJournalID(context.Background(), journalID)
				if err != nil {
					return err
				}
				_, err = tx.TransitionMaterialization(context.Background(), *current, transition.to)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			match, err = fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
			if err != nil || match != WorkspaceCommitNext {
				t.Fatalf("next confirmation=(%v,%v), want (Next,nil)", match, err)
			}
		})
	}
}

func TestCompactTargetedCommitConfirmationJournalRejectsMismatchAndClassifiesThird(t *testing.T) {
	fixture := newMaterializationFixtureWithoutJournal(t)
	journalID := "00000000-0000-4000-8000-000000000051"
	prepared := validPreparedMaterialization(t, fixture.binding, journalID)
	prepareCompactConfirmationFixture(&prepared)
	prior := captureMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID)
	next := rolledBackMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	})

	for _, test := range []struct {
		name        string
		prior, next WorkspaceCommitConfirmation
	}{
		{name: "target mismatch", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			v.targetID = "00000000-0000-4000-8000-000000000052"
			return v
		}()},
		{name: "expected absent token claims another owner", prior: func() WorkspaceCommitConfirmation {
			v := prior
			v.currentOwnerID = "00000000-0000-4000-8000-000000000052"
			return v
		}(), next: next},
		{name: "revision mismatch", prior: prior, next: func() WorkspaceCommitConfirmation { v := next; v.revision++; return v }()},
		{name: "zero prior", prior: WorkspaceCommitConfirmation{}, next: next},
		{name: "identical", prior: prior, next: prior},
	} {
		t.Run(test.name, func(t *testing.T) {
			match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), test.prior, test.next)
			if err == nil || match != WorkspaceCommitThird {
				t.Fatalf("invalid confirmation=(%v,%v), want (Third,error)", match, err)
			}
		})
	}

	third := next
	third.targetState = "recovered_new"
	if match := classifyWorkspaceCommitConfirmation(third, prior, next); match != WorkspaceCommitThird {
		t.Fatalf("alternate same-revision transition=%v, want Third", match)
	}
}

func TestCompactTargetedCommitConfirmationJournalIgnoresUnrelatedTerminalDrift(t *testing.T) {
	fixture := newMaterializationFixtureWithoutJournal(t)
	journalID := "00000000-0000-4000-8000-000000000051"
	prior := captureMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID)
	prepared := validPreparedMaterialization(t, fixture.binding, journalID)
	prepareCompactConfirmationFixture(&prepared)
	next := rolledBackMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	})

	proof := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
	makeMaterializationFixture(t, fixture.store, fixture.repo, fixture.binding, "accepted", &proof)
	match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitPrior {
		t.Fatalf("unrelated terminal drift confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationJournalReadFailureReturnsZero(t *testing.T) {
	fixture := newMaterializationFixtureWithoutJournal(t)
	journalID := "00000000-0000-4000-8000-000000000051"
	prior := captureMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID)
	prepared := validPreparedMaterialization(t, fixture.binding, journalID)
	prepareCompactConfirmationFixture(&prepared)
	next := rolledBackMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, journalID, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	match, err := fixture.repo.ConfirmWorkspaceCommit(ctx, prior, next)
	if !errors.Is(err, context.Canceled) || match != WorkspaceCommitThird {
		t.Fatalf("canceled confirmation=(%v,%v), want (Third,context.Canceled)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationJournalDifferentCurrentOwnerIsThird(t *testing.T) {
	fixture := newMaterializationFixtureWithoutJournal(t)
	targetID := "00000000-0000-4000-8000-000000000051"
	target := validPreparedMaterialization(t, fixture.binding, targetID)
	prepareCompactConfirmationFixture(&target)
	prior := captureMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, targetID)
	next := rolledBackMaterializationConfirmation(t, fixture.repo, fixture.binding.Scope, targetID, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), target)
		return err
	})

	otherID := "00000000-0000-4000-8000-000000000052"
	other := validPreparedMaterialization(t, fixture.binding, otherID)
	prepareCompactConfirmationFixture(&other)
	if err := fixture.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), other)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitThird {
		t.Fatalf("different current owner confirmation=(%v,%v), want (Third,nil)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationJournalUsesOneCoherentSnapshot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout-a", 1, 11,
	)
	journalID := "00000000-0000-4000-8000-000000000051"
	prepared := validPreparedMaterialization(t, binding, journalID)
	prepareCompactConfirmationFixture(&prepared)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.PrepareMaterialization(context.Background(), prepared)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	prior := captureMaterializationConfirmation(t, repo, binding.Scope, journalID)
	next := rolledBackMaterializationConfirmation(t, repo, binding.Scope, journalID, func(tx *WorkspaceMutationTx) error {
		current, err := tx.MaterializationByJournalID(context.Background(), journalID)
		if err != nil {
			return err
		}
		_, err = tx.TransitionMaterialization(context.Background(), *current, "published")
		return err
	})

	readStarted := make(chan struct{})
	writerFinished := make(chan struct{})
	readDB := openCompactConfirmationSnapshotDB(t, databasePath, readStarted, writerFinished)
	readRepo := NewWorkspaceRepo(readDB)
	writerError := make(chan error, 1)
	go func() {
		<-readStarted
		err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			current, err := tx.MaterializationByJournalID(context.Background(), journalID)
			if err != nil {
				return err
			}
			_, err = tx.TransitionMaterialization(context.Background(), *current, "published")
			return err
		})
		writerError <- err
		close(writerFinished)
	}()
	match, err := readRepo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitPrior {
		t.Fatalf("raced confirmation=(%v,%v), want coherent Prior", match, err)
	}
	if err := <-writerError; err != nil {
		t.Fatal(err)
	}
	match, err = readRepo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitNext {
		t.Fatalf("post-writer confirmation=(%v,%v), want Next", match, err)
	}
}

func TestCompactTargetedCommitConfirmationPublicationTransitionMatrix(t *testing.T) {
	for _, class := range []WorkspacePublicationTransitionClass{
		WorkspacePublicationConfigured,
		WorkspacePublicationStickyInvalidation,
	} {
		t.Run(string(class), func(t *testing.T) {
			fixture := newPublicationConfirmationFixture(t, class)
			prior := capturePublicationConfirmation(t, fixture.repo, fixture.binding.Scope, class)
			next := rolledBackPublicationConfirmation(t, fixture.repo, fixture.binding.Scope, class, fixture.transition)
			if prior.targetKind != workspaceCommitPublication || prior.transitionClass != class || prior.postimageDigest != nil ||
				prior.targetID != "" || prior.targetState != "" || prior.currentOwnerID != "" {
				t.Fatalf("publication prior has mixed shape: %+v", prior)
			}
			if next.revision != prior.revision+1 || next.authorityDigest == prior.authorityDigest {
				t.Fatalf("publication transition prior=%+v next=%+v", prior, next)
			}

			match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
			if err != nil || match != WorkspaceCommitPrior {
				t.Fatalf("prior confirmation=(%v,%v), want (Prior,nil)", match, err)
			}
			commitPublicationTransition(t, fixture.repo, fixture.binding.Scope, fixture.transition)
			match, err = fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
			if err != nil || match != WorkspaceCommitNext {
				t.Fatalf("next confirmation=(%v,%v), want (Next,nil)", match, err)
			}
		})
	}
}

func TestCompactTargetedCommitConfirmationPublicationRejectsMismatchAndClassifiesThird(t *testing.T) {
	fixture := newPublicationConfirmationFixture(t, WorkspacePublicationConfigured)
	prior := capturePublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured)
	next := rolledBackPublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured, fixture.transition)

	for _, test := range []struct {
		name        string
		prior, next WorkspaceCommitConfirmation
	}{
		{name: "target class mismatch", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			v.transitionClass = WorkspacePublicationStickyInvalidation
			return v
		}()},
		{name: "revision mismatch", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			v.revision++
			return v
		}()},
		{name: "mixed journal target", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			v.targetID = "00000000-0000-4000-8000-000000000051"
			return v
		}()},
		{name: "mixed journal state", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			v.targetState = "published"
			return v
		}()},
		{name: "mixed journal owner", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			v.currentOwnerID = "00000000-0000-4000-8000-000000000051"
			return v
		}()},
		{name: "mixed journal postimage", prior: prior, next: func() WorkspaceCommitConfirmation {
			v := next
			digest := publicationTestDigest('f')
			v.postimageDigest = &digest
			return v
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), test.prior, test.next)
			if err == nil || match != WorkspaceCommitThird {
				t.Fatalf("invalid confirmation=(%v,%v), want (Third,error)", match, err)
			}
		})
	}
	commitPublicationTransition(t, fixture.repo, fixture.binding.Scope, fixture.transition)
	mismatchedDigest := next
	mismatchedDigest.authorityDigest = publicationTestDigest('e')
	match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, mismatchedDigest)
	if err != nil || match != WorkspaceCommitThird {
		t.Fatalf("target digest mismatch=(%v,%v), want (Third,nil)", match, err)
	}

	fixture = newPublicationConfirmationFixture(t, WorkspacePublicationConfigured)
	prior = capturePublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured)
	next = rolledBackPublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured, fixture.transition)
	alternate := fixture.transition
	alternate.Next.Classification = types.PublicationPrivateGit
	commitPublicationTransition(t, fixture.repo, fixture.binding.Scope, alternate)
	match, err = fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitThird {
		t.Fatalf("alternate same-revision transition=(%v,%v), want (Third,nil)", match, err)
	}

	fixture = newPublicationConfirmationFixture(t, WorkspacePublicationConfigured)
	prior = capturePublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured)
	next = rolledBackPublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured, fixture.transition)
	commitPublicationTransition(t, fixture.repo, fixture.binding.Scope, fixture.transition)
	current := readCurrentPublicationPolicy(t, fixture.repo, fixture.binding.Scope)
	future := fixture.transition.Next
	future.PolicyRevision = current.PolicyRevision + 1
	future.Classification = types.PublicationPrivateGit
	changedAt := future.ChangedAt.Add(time.Hour)
	future.ChangedAt = &changedAt
	commitPublicationTransition(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationPolicyTransition{Expected: current, Next: future})
	match, err = fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitThird {
		t.Fatalf("later third state=(%v,%v), want (Third,nil)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationPublicationIgnoresUnrelatedHistoryDrift(t *testing.T) {
	fixture := newPublicationConfirmationFixture(t, WorkspacePublicationConfigured)
	prior := capturePublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured)
	next := rolledBackPublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured, fixture.transition)
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_publication_policy_history
		SET recorded_at='2020-01-01 00:00:00+00:00' WHERE project_id=? AND workspace_id=? AND policy_revision=1`,
		fixture.binding.Scope.ProjectID, fixture.binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	match, err := fixture.repo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitPrior {
		t.Fatalf("history drift confirmation=(%v,%v), want (Prior,nil)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationPublicationReadFailureReturnsZero(t *testing.T) {
	fixture := newPublicationConfirmationFixture(t, WorkspacePublicationConfigured)
	prior := capturePublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured)
	next := rolledBackPublicationConfirmation(t, fixture.repo, fixture.binding.Scope, WorkspacePublicationConfigured, fixture.transition)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	match, err := fixture.repo.ConfirmWorkspaceCommit(ctx, prior, next)
	if !errors.Is(err, context.Canceled) || match != WorkspaceCommitThird {
		t.Fatalf("canceled confirmation=(%v,%v), want (Third,context.Canceled)", match, err)
	}
}

func TestCompactTargetedCommitConfirmationPublicationUsesOneCoherentSnapshot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	fixture := buildPublicationConfirmationFixture(t, store, repo, binding, WorkspacePublicationConfigured)
	prior := capturePublicationConfirmation(t, repo, binding.Scope, WorkspacePublicationConfigured)
	next := rolledBackPublicationConfirmation(t, repo, binding.Scope, WorkspacePublicationConfigured, fixture.transition)

	readStarted := make(chan struct{})
	writerFinished := make(chan struct{})
	readDB := openCompactConfirmationSnapshotDB(t, databasePath, readStarted, writerFinished)
	readRepo := NewWorkspaceRepo(readDB)
	writerError := make(chan error, 1)
	go func() {
		<-readStarted
		alternate := fixture.transition
		alternate.Next.Classification = types.PublicationPrivateGit
		err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			if _, err := tx.conn.ExecContext(context.Background(), `UPDATE workspace_publication_policy_history
				SET recorded_at='2020-01-01 00:00:00+00:00' WHERE project_id=? AND workspace_id=? AND policy_revision=1`,
				binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
				return err
			}
			_, err := tx.ReconfigurePublication(context.Background(), alternate)
			return err
		})
		writerError <- err
		close(writerFinished)
	}()
	match, err := readRepo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitPrior {
		t.Fatalf("raced confirmation=(%v,%v), want coherent Prior", match, err)
	}
	if err := <-writerError; err != nil {
		t.Fatal(err)
	}
	match, err = readRepo.ConfirmWorkspaceCommit(context.Background(), prior, next)
	if err != nil || match != WorkspaceCommitThird {
		t.Fatalf("post-writer confirmation=(%v,%v), want Third", match, err)
	}
}

type publicationConfirmationFixture struct {
	store      *Store
	repo       *WorkspaceRepo
	binding    types.WorkspaceBinding
	transition WorkspacePublicationPolicyTransition
}

func newPublicationConfirmationFixture(t *testing.T, class WorkspacePublicationTransitionClass) publicationConfirmationFixture {
	t.Helper()
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	return buildPublicationConfirmationFixture(t, store, repo, binding, class)
}

func buildPublicationConfirmationFixture(t *testing.T, store *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding, class WorkspacePublicationTransitionClass) publicationConfirmationFixture {
	t.Helper()
	current := readCurrentPublicationPolicy(t, repo, binding.Scope)
	origin := publicationTestDigest('a')
	actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
	changedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	configured := WorkspacePublicationPolicyRecord{
		Repository: binding.Repository, OriginDigest: &origin, Classification: types.PublicationPublicGit,
		PolicyRevision: current.PolicyRevision + 1, TransitionKind: "configured", ChangedBy: &actor, ChangedAt: &changedAt,
	}
	if class == WorkspacePublicationStickyInvalidation {
		commitPublicationTransition(t, repo, binding.Scope, WorkspacePublicationPolicyTransition{Expected: current, Next: configured})
		current = readCurrentPublicationPolicy(t, repo, binding.Scope)
		changedAt = changedAt.Add(time.Hour)
		invalidatedOrigin := publicationTestDigest('b')
		configured = WorkspacePublicationPolicyRecord{
			Repository: binding.Repository, OriginDigest: &invalidatedOrigin, Classification: types.PublicationUnclassified,
			PolicyRevision: current.PolicyRevision + 1, TransitionKind: "origin_invalidated", ChangedAt: &changedAt,
		}
	}
	return publicationConfirmationFixture{
		store: store, repo: repo, binding: binding,
		transition: WorkspacePublicationPolicyTransition{Expected: current, Next: configured},
	}
}

func readCurrentPublicationPolicy(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope) WorkspacePublicationPolicyRecord {
	t.Helper()
	var current WorkspacePublicationPolicyRecord
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		current, err = tx.PublicationPolicy(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return current
}

func capturePublicationConfirmation(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, class WorkspacePublicationTransitionClass) WorkspaceCommitConfirmation {
	t.Helper()
	var captured WorkspaceCommitConfirmation
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		captured, err = tx.CapturePublicationCommitConfirmation(context.Background(), class)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return captured
}

func rolledBackPublicationConfirmation(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, class WorkspacePublicationTransitionClass, transition WorkspacePublicationPolicyTransition) WorkspaceCommitConfirmation {
	t.Helper()
	rollback := errors.New("publication confirmation fixture rollback")
	var captured WorkspaceCommitConfirmation
	err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		if _, err := tx.ReconfigurePublication(context.Background(), transition); err != nil {
			return err
		}
		var err error
		captured, err = tx.CapturePublicationCommitConfirmation(context.Background(), class)
		if err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rolled-back publication confirmation error=%v", err)
	}
	return captured
}

func commitPublicationTransition(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, transition WorkspacePublicationPolicyTransition) {
	t.Helper()
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReconfigurePublication(context.Background(), transition)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactTargetedCommitConfirmationJournalOpaqueToken(t *testing.T) {
	typeOfToken := reflect.TypeOf(WorkspaceCommitConfirmation{})
	for index := 0; index < typeOfToken.NumField(); index++ {
		if typeOfToken.Field(index).PkgPath == "" {
			t.Fatalf("opaque token field %q is exported", typeOfToken.Field(index).Name)
		}
	}
}

func captureMaterializationConfirmation(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, journalID string) WorkspaceCommitConfirmation {
	t.Helper()
	var captured WorkspaceCommitConfirmation
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		var err error
		captured, err = tx.CaptureMaterializationCommitConfirmation(context.Background(), journalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return captured
}

func prepareCompactConfirmationFixture(prepared *WorkspaceMaterializationRecord) {
	operations := "{\"schema_version\":1,\"initial_through_generation\":0,\"operations\":[]}\n"
	review := "{}\n"
	prior := "{}\n"
	prepared.IncludedOperationsJSON = &operations
	prepared.PublicationReviewJSON = &review
	prepared.PriorCandidateJSON = &prior
}

func rolledBackMaterializationConfirmation(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, journalID string, mutate func(*WorkspaceMutationTx) error) WorkspaceCommitConfirmation {
	t.Helper()
	rollback := errors.New("compact confirmation fixture rollback")
	var captured WorkspaceCommitConfirmation
	err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		var err error
		captured, err = tx.CaptureMaterializationCommitConfirmation(context.Background(), journalID)
		if err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rolled-back confirmation error=%v", err)
	}
	return captured
}

type compactConfirmationSnapshotDriver struct {
	inner          driver.Driver
	readStarted    chan<- struct{}
	writerFinished <-chan struct{}
	once           sync.Once
}

func (wrapped *compactConfirmationSnapshotDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &compactConfirmationSnapshotConn{Conn: connection, wrapped: wrapped}, nil
}

type compactConfirmationSnapshotConn struct {
	driver.Conn
	wrapped *compactConfirmationSnapshotDriver
}

func (connection *compactConfirmationSnapshotConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *compactConfirmationSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !strings.Contains(strings.ToLower(query), "from workspace_bindings") {
		return rows, err
	}
	return &compactConfirmationSnapshotRows{Rows: rows, wrapped: connection.wrapped}, nil
}

func (connection *compactConfirmationSnapshotConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (connection *compactConfirmationSnapshotConn) Ping(ctx context.Context) error {
	return connection.Conn.(driver.Pinger).Ping(ctx)
}

type compactConfirmationSnapshotRows struct {
	driver.Rows
	wrapped *compactConfirmationSnapshotDriver
}

func (rows *compactConfirmationSnapshotRows) Next(destinations []driver.Value) error {
	err := rows.Rows.Next(destinations)
	if err == nil {
		rows.wrapped.once.Do(func() {
			close(rows.wrapped.readStarted)
			<-rows.wrapped.writerFinished
		})
	}
	return err
}

var compactConfirmationSnapshotSequence atomic.Uint64

func openCompactConfirmationSnapshotDB(t *testing.T, databasePath string, readStarted chan<- struct{}, writerFinished <-chan struct{}) *sql.DB {
	t.Helper()
	wrapper := &compactConfirmationSnapshotDriver{
		inner: &sqlite.Driver{}, readStarted: readStarted, writerFinished: writerFinished,
	}
	driverName := fmt.Sprintf("localstore-compact-confirmation-snapshot-%d", compactConfirmationSnapshotSequence.Add(1))
	sql.Register(driverName, wrapper)
	db, err := sql.Open(driverName, sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
