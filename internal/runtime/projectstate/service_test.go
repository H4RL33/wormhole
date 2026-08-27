package projectstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestRegisterWorkspaceIdempotent(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	request := RegisterWorkspaceRequest{Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit}
	first, err := service.RegisterWorkspace(context.Background(), request)
	if err != nil || !first.Created {
		t.Fatalf("first registration created=%v err=%v", first.Created, err)
	}
	if err := first.Binding.Validate(); err != nil {
		t.Fatalf("registered binding: %v", err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER reject_workspace_insert BEFORE INSERT ON workspace_bindings BEGIN SELECT RAISE(ABORT, 'idempotent registration inserted'); END;
		CREATE TRIGGER reject_workspace_update BEFORE UPDATE ON workspace_bindings BEGIN SELECT RAISE(ABORT, 'idempotent registration updated'); END;
		CREATE TRIGGER reject_workspace_delete BEFORE DELETE ON workspace_bindings BEGIN SELECT RAISE(ABORT, 'idempotent registration deleted'); END;
	`); err != nil {
		t.Fatal(err)
	}
	second, err := service.RegisterWorkspace(context.Background(), request)
	if err != nil || second.Created {
		t.Fatalf("repeat registration created=%v err=%v", second.Created, err)
	}
	if second.Binding != first.Binding {
		t.Fatalf("repeat binding=%+v, want %+v", second.Binding, first.Binding)
	}
}

func TestRegisterWorkspaceIdempotentRevisionBaselineSurvivesExactReregistrationAfterReopen(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	request := RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	}
	first, err := service.RegisterWorkspace(context.Background(), request)
	if err != nil || !first.Created {
		t.Fatalf("first registration=(%+v,%v)", first, err)
	}
	assertRegistrationRevisionBaseline(t, store, first.Binding.Scope)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, service = openProjectStateServiceAt(t, databasePath)
	second, err := service.RegisterWorkspace(context.Background(), request)
	if err != nil || second.Created || second.Binding != first.Binding {
		t.Fatalf("exact registration after reopen=(%+v,%v), want read-only %+v", second, err, first.Binding)
	}
	assertRegistrationRevisionBaseline(t, store, second.Binding.Scope)
}

func assertRegistrationRevisionBaseline(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) {
	t.Helper()
	var revision int64
	if err := store.DB().QueryRow(`
		SELECT workspace_revision FROM workspace_bindings
		WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("registration workspace revision=%d, want baseline 1", revision)
	}
}

func TestWorkspaceStashExactRetryPreservesReceiptAndRevision(t *testing.T) {
	fixture := newStashServiceFixture(t)
	first, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := workspaceRevisionForServiceTest(t, fixture.store, fixture.req.Scope)
	beforeRaw := captureStashRawState(t, fixture.store)
	beforeReceipt, err := fixture.service.registration.repo.TransitionReceipt(context.Background(), fixture.req.Scope, fixture.req.RequestID)
	if err != nil || beforeReceipt == nil {
		t.Fatalf("first stash receipt=(%+v,%v)", beforeReceipt, err)
	}
	fixture.service.transition.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000002", nil }

	retry, err := fixture.service.Stash(context.Background(), fixture.req)
	if err != nil || retry != first {
		t.Fatalf("exact retry Stash()=(%+v,%v), want %+v", retry, err, first)
	}
	if afterRevision := workspaceRevisionForServiceTest(t, fixture.store, fixture.req.Scope); afterRevision != beforeRevision {
		t.Fatalf("exact retry workspace revision=%d, want %d", afterRevision, beforeRevision)
	}
	if afterRaw := captureStashRawState(t, fixture.store); !reflect.DeepEqual(afterRaw, beforeRaw) {
		t.Fatal("exact retry changed durable stash evidence")
	}
	afterReceipt, err := fixture.service.registration.repo.TransitionReceipt(context.Background(), fixture.req.Scope, fixture.req.RequestID)
	if err != nil || !reflect.DeepEqual(afterReceipt, beforeReceipt) {
		t.Fatalf("exact retry receipt=(%+v,%v), want %+v", afterReceipt, err, beforeReceipt)
	}
}

func workspaceRevisionForServiceTest(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) int64 {
	t.Helper()
	var revision int64
	if err := store.DB().QueryRow(`
		SELECT workspace_revision FROM workspace_bindings
		WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestRegisterWorkspaceCheckoutCollision(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	first := RegisterWorkspaceRequest{Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit}
	if _, err := service.RegisterWorkspace(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	secondProject := "00000000-0000-4000-8000-000000000002"
	secondCommit := commitSnapshot(t, repository.root, secondProject, repository.identity)
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: secondProject, ExpectedRepository: repository.identity, ExpectedCommit: secondCommit,
	})
	if !errors.Is(err, localstore.ErrCheckoutCollision) {
		t.Fatalf("collision error=%v, want ErrCheckoutCollision", err)
	}
}

func TestTwoWorktreesDistinct(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	worktree := filepath.Join(t.TempDir(), "second-worktree")
	runGit(t, repository.root, "worktree", "add", "-b", "second-worktree", worktree, repository.commit)
	_, service := openProjectStateService(t, "")

	first, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: worktree, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Binding.Scope.WorkspaceID == second.Binding.Scope.WorkspaceID {
		t.Fatal("two worktrees received the same workspace ID")
	}
	if first.Binding.Checkout == second.Binding.Checkout {
		t.Fatal("two worktrees received the same checkout identity")
	}
}

func TestRegisterWorkspaceReadsCommittedTree(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	if err := os.WriteFile(filepath.Join(repository.root, ".wormhole", "config.toml"), []byte("untrusted working tree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	result, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace used the caller working tree: %v", err)
	}
	if result.Binding.AcceptedCommitSHA != repository.commit {
		t.Fatalf("accepted commit=%q, want %q", result.Binding.AcceptedCommitSHA, repository.commit)
	}
}

func TestRegisterWorkspaceRejectsCallerIdentityMismatch(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	wrong := types.RepositoryIdentity{Provider: "github", ImmutableID: "R_wrong", CanonicalRemote: "https://github.com/acme/wrong"}
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: wrong, ExpectedCommit: repository.commit,
	})
	if err == nil {
		t.Fatal("RegisterWorkspace accepted a repository identity differing from the committed snapshot")
	}
}

func TestRegisterWorkspaceRejectsHeadMismatch(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: strings.Repeat("a", 40),
	})
	if !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("HEAD mismatch error=%v, want ErrInvalidRegistration", err)
	}
}

func TestRegisterWorkspaceRejectsSymlinkRoot(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	link := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository.root, link); err != nil {
		t.Fatal(err)
	}
	_, service := openProjectStateService(t, "")
	_, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: link, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err == nil {
		t.Fatal("RegisterWorkspace accepted a symlink checkout root")
	}
}

func TestNewServiceRejectsBackupRootInsideRepository(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	backup := filepath.Join(repository.root, ".private-backups")
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{LegacyIntegrationBackupRoot: backup}); err == nil {
		t.Fatal("NewService accepted a repository-contained process-private backup root")
	}
}

func TestNewServiceBackupRootIsOwnerOnly(t *testing.T) {
	backup := filepath.Join(t.TempDir(), "state", "legacy-backups")
	_, _ = openProjectStateService(t, backup)
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("backup mode=%#o, want 0700", info.Mode().Perm())
	}
}

func TestNewServiceDoesNotRepurposePermissiveDirectoryAsBackupRoot(t *testing.T) {
	backup := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{LegacyIntegrationBackupRoot: backup}); err == nil {
		t.Fatal("NewService repurposed a permissive existing directory")
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("rejected backup mode=%#o, want unchanged 0755", info.Mode().Perm())
	}
}

func TestNewServiceRejectsInvalidBackupRoots(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := localstore.NewWorkspaceRepo(store.DB())
	if _, err := NewService(repo, ServiceConfig{LegacyIntegrationBackupRoot: "relative"}); err == nil {
		t.Fatal("NewService accepted a relative backup root")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "backup-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(repo, ServiceConfig{LegacyIntegrationBackupRoot: link}); err == nil {
		t.Fatal("NewService accepted a symlinked backup root")
	}
}

func TestRegisterWorkspaceStatusIsScoped(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
		Root: repository.root, ExpectedProjectID: repository.projectID, ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if status.Binding != registered.Binding || status.State != "clean" {
		t.Fatalf("status=%+v", status)
	}
	wrong := registered.Binding.Scope
	wrong.ProjectID = "00000000-0000-4000-8000-000000000002"
	if _, err := service.Status(context.Background(), wrong); !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("wrong-project Status error=%v, want ErrNotFound", err)
	}
}

func TestStatusExposesCandidateDigestAndOverlayGeneration(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)

	accepted, err := service.Status(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.CandidatePresent || accepted.CandidateDigest != accepted.AcceptedSnapshot.Digest || accepted.OverlayGeneration != 0 {
		t.Fatalf("clean status=%+v", accepted)
	}
}

func TestServiceViewReturnsOwnedComposedSnapshots(t *testing.T) {
	t.Run("accepted only", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		_, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot

		got, err := service.View(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if got.ThroughGeneration != 0 || !reflect.DeepEqual(got.Snapshot, accepted) {
			t.Fatalf("accepted View = %+v, want generation 0 and accepted snapshot", got)
		}
	})

	t.Run("overlay operation and deep ownership", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		_, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
		operation := servicePutTaskOperation(accepted,
			"99999999-9999-4999-8999-999999999991",
			"22222222-2222-4222-8222-222222222222",
			"overlay task",
		)
		want, err := state.ApplyOperation(accepted, operation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err != nil {
			t.Fatal(err)
		}

		got, err := service.View(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if got.ThroughGeneration != 1 || !reflect.DeepEqual(got.Snapshot, want) {
			t.Fatalf("overlay View = %+v, want generation 1 and composed snapshot", got)
		}
		got.Snapshot.Tasks[operation.PutRecord.Record.Task.ID].Value.Title = "mutated caller result"
		fresh, err := service.View(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fresh.Snapshot, want) {
			t.Fatal("mutating returned View snapshot changed a later view")
		}
	})
}

func TestServiceViewSelectsCandidatesAndSurvivesConflictAndRestart(t *testing.T) {
	t.Run("direct candidate", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
		directOperation := servicePutTaskOperation(accepted,
			"99999999-9999-4999-8999-999999999991",
			"22222222-2222-4222-8222-222222222222", "direct candidate")
		direct, err := state.ApplyOperation(accepted, directOperation)
		if err != nil {
			t.Fatal(err)
		}
		insertServiceCandidate(t, store, registered.Binding.Scope, accepted.Digest, direct, nil, 0)

		got, err := service.View(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if got.ThroughGeneration != 0 || !reflect.DeepEqual(got.Snapshot, direct) {
			t.Fatalf("direct candidate View = %+v, want generation 0 and direct snapshot", got)
		}
	})

	t.Run("rebased candidate with later operation", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
		directOperation := servicePutTaskOperation(accepted,
			"99999999-9999-4999-8999-999999999991",
			"22222222-2222-4222-8222-222222222222", "direct candidate")
		direct, err := state.ApplyOperation(accepted, directOperation)
		if err != nil {
			t.Fatal(err)
		}
		rebasedOperation := servicePutTaskOperation(accepted,
			"99999999-9999-4999-8999-999999999992",
			"33333333-3333-4333-8333-333333333333", "rebased candidate")
		rebased, err := state.ApplyOperation(accepted, rebasedOperation)
		if err != nil {
			t.Fatal(err)
		}
		insertServiceCandidate(t, store, registered.Binding.Scope, accepted.Digest, direct, &rebased, 7)
		active := servicePutTaskOperation(rebased,
			"99999999-9999-4999-8999-999999999993",
			"44444444-4444-4444-8444-444444444444", "after rebase")
		want, err := state.ApplyOperation(rebased, active)
		if err != nil {
			t.Fatal(err)
		}
		insertServiceOperation(t, store, registered.Binding.Scope, 9, active, "active")

		got, err := service.View(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if got.ThroughGeneration != 9 || !reflect.DeepEqual(got.Snapshot, want) {
			t.Fatalf("rebased View = %+v, want generation 9 and rebased composition", got)
		}
	})

	t.Run("conflict and restart", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		databasePath := filepath.Join(t.TempDir(), "gateway.db")
		store, service := openProjectStateServiceAt(t, databasePath)
		registered := registerGitRepository(t, service, repository)
		accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
		operation := servicePutTaskOperation(accepted,
			"99999999-9999-4999-8999-999999999991",
			"22222222-2222-4222-8222-222222222222", "conflicted overlay")
		want, err := state.ApplyOperation(accepted, operation)
		if err != nil {
			t.Fatal(err)
		}
		insertServiceOperation(t, store, registered.Binding.Scope, 1, operation, "active")
		insertServiceConflict(t, store, registered.Binding.Scope, "view-conflict", state.RecordKey{Kind: "task", ID: operation.PutRecord.Record.Task.ID}, "open")
		setServiceWorkspaceState(t, store, registered.Binding.Scope, "conflicted")

		beforeRestart, err := service.View(context.Background(), registered.Binding.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if beforeRestart.ThroughGeneration != 1 || !reflect.DeepEqual(beforeRestart.Snapshot, want) {
			t.Fatalf("conflicted View = %+v, want generation 1 and composed snapshot", beforeRestart)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		_, reopened := openProjectStateServiceAt(t, databasePath)
		afterRestart, err := reopened.View(context.Background(), registered.Binding.Scope)
		if err != nil || !reflect.DeepEqual(afterRestart, beforeRestart) {
			t.Fatalf("restarted View = %+v, %v; want %+v", afterRestart, err, beforeRestart)
		}
	})
}

func TestServiceViewIsolatesSiblingWorkspaces(t *testing.T) {
	firstRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	secondRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	_, service := openProjectStateService(t, "")
	first := registerGitRepository(t, service, firstRepository)
	second := registerGitRepository(t, service, secondRepository)
	firstAccepted := mustServiceStatus(t, service, first.Binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(firstAccepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222", "first workspace only")
	if _, err := service.Apply(context.Background(), first.Binding.Scope, operation); err != nil {
		t.Fatal(err)
	}

	firstView, err := service.View(context.Background(), first.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	secondView, err := service.View(context.Background(), second.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if firstView.ThroughGeneration != 1 || firstView.Snapshot.Tasks[operation.PutRecord.Record.Task.ID].Value == nil {
		t.Fatalf("first workspace View = %+v, want its operation", firstView)
	}
	if secondView.ThroughGeneration != 0 || len(secondView.Snapshot.Tasks) != 0 {
		t.Fatalf("sibling workspace View = %+v, want accepted-only snapshot", secondView)
	}
}

func TestRecoveryStatusCompositionUsesDatabaseOnly(t *testing.T) {
	for _, history := range []string{"empty", "accepted", "recovered_old", "recovered_new"} {
		t.Run(history, func(t *testing.T) {
			service, scope, root := recoveryNoWorkServiceFixture(t, history)
			var want WorkspaceStatus
			var wantDisposition localstore.WorkspaceCurrentMaterialization
			var wantCandidate *localstore.WorkspaceCandidateRecord
			if err := service.registration.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
				composed, err := loadComposedWorkspace(context.Background(), tx)
				if err != nil {
					return err
				}
				want = composed.status
				wantDisposition, err = readCurrentMaterializationWorkset(context.Background(), tx)
				if err != nil {
					return err
				}
				wantCandidate, err = tx.Candidate(context.Background())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if want.PublicationClassification != "" || want.PublicationReviewDigest != "" {
				t.Fatalf("fixture status has nonzero review fields: %+v", want)
			}
			if err := os.Rename(root, root+"-moved"); err != nil {
				t.Fatal(err)
			}

			observerCalls := 0
			var got checkpointRecoveryPlan
			err := service.registration.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
				var err error
				got, err = planCheckpointRecovery(context.Background(), tx, func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
					observerCalls++
					panic("recovery no-work reached Git/origin observer")
				})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if observerCalls != 0 || got.proof.kind != checkpointRecoveryNoWork || got.proof.driver != nil ||
				!reflect.DeepEqual(got.observation, checkpointRecoveryGitObservation{}) || !reflect.DeepEqual(got.proof.status, want) ||
				got.proof.status.PublicationClassification != "" || got.proof.status.PublicationReviewDigest != "" {
				t.Fatalf("database-only %s plan=%+v observerCalls=%d, want exact zero-review no-work", history, got, observerCalls)
			}

			got.proof.workspace.Snapshot.Project.Name = "mutated proof workspace"
			if got.proof.candidate != nil {
				got.proof.candidate.DirectSnapshot.Project.Name = "mutated proof candidate"
			}
			if len(got.proof.disposition.Journals) != 0 {
				got.proof.disposition.Journals[0].CandidateTree[0].Data[0] ^= 0xff
			}
			if !reflect.DeepEqual(got.proof.status, want) {
				t.Fatal("database-only recovery status aliases proof workspace, candidate, or disposition")
			}
			freshDisposition := readCheckpointDisposition(t, service, scope)
			if !reflect.DeepEqual(freshDisposition, wantDisposition) {
				t.Fatal("mutating returned recovery proof changed durable disposition")
			}
			var freshCandidate *localstore.WorkspaceCandidateRecord
			if err := service.registration.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
				var err error
				freshCandidate, err = tx.Candidate(context.Background())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !equalCheckpointRecoveryCandidates(freshCandidate, wantCandidate) {
				t.Fatal("mutating returned recovery proof changed durable candidate")
			}
		})
	}

	t.Run("driver reaches observer boundary", func(t *testing.T) {
		for _, test := range []struct {
			name string
			kind checkpointRecoveryKind
		}{
			{name: "prepared", kind: checkpointRecoveryPrepared},
			{name: "published", kind: checkpointRecoveryPublished},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture, request := recoveryDriverPlanFixture(t, test.name)
				observerCalls := 0
				var wantObservation checkpointRecoveryGitObservation
				var got checkpointRecoveryPlan
				if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), request.Scope, func(tx *localstore.WorkspaceMutationTx) error {
					var err error
					got, err = planCheckpointRecovery(context.Background(), tx, func(_ context.Context, proof checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
						observerCalls++
						if proof.kind != test.kind || proof.driver == nil || proof.driver.State != test.name {
							t.Fatalf("%s observer proof=%+v", test.name, proof)
						}
						wantObservation = recoveryPlannerObservation(t, proof, test.name)
						return wantObservation, nil
					})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				gotDriverState := ""
				if got.proof.driver != nil {
					gotDriverState = got.proof.driver.State
				}
				if observerCalls != 1 || got.proof.kind != test.kind || got.proof.driver == nil || got.proof.driver.State != test.name ||
					reflect.DeepEqual(wantObservation, checkpointRecoveryGitObservation{}) || !reflect.DeepEqual(got.observation, wantObservation) {
					t.Fatalf("%s recovery plan=(kind=%d, driver=%q, observationEqual=%t) observerCalls=%d, want exact nonzero propagation",
						test.name, got.proof.kind, gotDriverState, reflect.DeepEqual(got.observation, wantObservation), observerCalls)
				}

				injected := errors.New("injected " + test.name + " recovery observer failure")
				errorCalls := 0
				var rejected checkpointRecoveryPlan
				err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), request.Scope, func(tx *localstore.WorkspaceMutationTx) error {
					var err error
					rejected, err = planCheckpointRecovery(context.Background(), tx, func(_ context.Context, proof checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
						errorCalls++
						if proof.kind != test.kind || proof.driver == nil || proof.driver.State != test.name {
							t.Fatalf("%s error observer proof=%+v", test.name, proof)
						}
						return wantObservation, injected
					})
					return err
				})
				if errorCalls != 1 || err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) ||
					!errors.Is(err, injected) || !reflect.DeepEqual(rejected, checkpointRecoveryPlan{}) {
					t.Fatalf("rejected %s recovery plan=(zero=%t, err=%v) calls=%d, want exact zero sentinel and cause",
						test.name, reflect.DeepEqual(rejected, checkpointRecoveryPlan{}), err, errorCalls)
				}

				if test.kind == checkpointRecoveryPrepared {
					var missing checkpointRecoveryPlan
					err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), request.Scope, func(tx *localstore.WorkspaceMutationTx) error {
						var err error
						missing, err = planCheckpointRecovery(context.Background(), tx, nil)
						return err
					})
					if err == nil || !errors.Is(err, ErrCheckpointRecoveryPrecondition) || !reflect.DeepEqual(missing, checkpointRecoveryPlan{}) {
						t.Fatalf("nil-observer recovery plan=(%+v,%v), want exact zero precondition error", missing, err)
					}
				}
			})
		}
	})
}

func recoveryDriverPlanFixture(t *testing.T, driverState string) (*checkpointCoordinatorFixture, CheckpointRequest) {
	t.Helper()
	fixture, request, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	if driverState == "prepared" {
		publishErr := errors.New("retain prepared recovery driver")
		baseFactory := fixture.service.checkpoint.prepareCheckpointArtifact
		fixture.service.checkpoint.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
			handle, err := baseFactory(ctx, input)
			if err == nil {
				handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
			}
			return handle, err
		}
		if result, err := fixture.service.Checkpoint(context.Background(), request); result != (CheckpointResult{}) || !errors.Is(err, publishErr) {
			t.Fatalf("prepare recovery driver=(%+v,%v)", result, err)
		}
		return fixture, request
	}
	if driverState != "published" {
		t.Fatalf("unknown recovery driver state %q", driverState)
	}
	if result, err := fixture.service.Checkpoint(context.Background(), request); err != nil || result.JournalID == "" {
		t.Fatalf("publish recovery driver=(%+v,%v)", result, err)
	}
	return fixture, request
}

func recoveryPlannerObservation(t *testing.T, proof checkpointRecoveryProof, driverState string) checkpointRecoveryGitObservation {
	t.Helper()
	tree, err := state.EncodeTree(proof.workspace.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.DecodeTree(cloneCheckpointTree(tree))
	if err != nil {
		t.Fatal(err)
	}
	originValue := observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "github.com", Path: "acme/recovery-" + driverState}
	originDigest, err := digestObservedOrigin(originValue)
	if err != nil {
		t.Fatal(err)
	}
	binding := proof.workspace.Binding
	position := gitBasePosition{
		root: binding.Checkout.CanonicalPath, checkout: binding.Checkout,
		acceptedRef: binding.AcceptedRef, commit: binding.AcceptedCommitSHA,
	}
	return checkpointRecoveryGitObservation{
		position: position,
		committed: committedWorkspace{
			root: position.root, checkout: position.checkout, acceptedRef: position.acceptedRef,
			commit: position.commit, tree: tree, snapshot: snapshot,
		},
		origin: publicationOriginObservation{
			root: position.root, checkout: position.checkout, origin: originValue, digest: originDigest,
		},
		finalPosition: position,
	}
}

func recoveryNoWorkServiceFixture(t *testing.T, history string) (*Service, types.WorkspaceScope, string) {
	t.Helper()
	if history == "empty" {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		_, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		return service, registered.Binding.Scope, repository.root
	}

	fixture, request, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	if history == "recovered_old" {
		publishErr := errors.New("retain prepared recovery fixture")
		baseFactory := fixture.service.checkpoint.prepareCheckpointArtifact
		fixture.service.checkpoint.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
			handle, err := baseFactory(ctx, input)
			if err == nil {
				handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
			}
			return handle, err
		}
		if result, err := fixture.service.Checkpoint(context.Background(), request); result != (CheckpointResult{}) || !errors.Is(err, publishErr) {
			t.Fatalf("prepare recovered-old fixture=(%+v,%v)", result, err)
		}
	} else if _, err := fixture.service.Checkpoint(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), request.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		disposition, err := readCurrentMaterializationWorkset(context.Background(), tx)
		if err != nil {
			return err
		}
		if len(disposition.Journals) != 1 {
			return fmt.Errorf("recovery fixture journals=%d, want 1", len(disposition.Journals))
		}
		switch history {
		case "accepted":
			_, err = tx.AcceptMaterialization(context.Background(), disposition.Journals[0])
		case "recovered_old", "recovered_new":
			_, err = tx.TransitionMaterialization(context.Background(), disposition.Journals[0], history)
		default:
			return fmt.Errorf("unknown recovery fixture history %q", history)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return fixture.service, request.Scope, fixture.repository.root
}

func TestStatusSelectsRebasedCandidateAndBoundary(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot

	directOperation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"direct candidate",
	)
	direct, err := state.ApplyOperation(accepted, directOperation)
	if err != nil {
		t.Fatal(err)
	}
	rebasedOperation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999992",
		"33333333-3333-4333-8333-333333333333",
		"rebased candidate",
	)
	rebased, err := state.ApplyOperation(accepted, rebasedOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertServiceCandidate(t, store, registered.Binding.Scope, accepted.Digest, direct, &rebased, 7)

	active := servicePutTaskOperation(
		rebased,
		"99999999-9999-4999-8999-999999999993",
		"44444444-4444-4444-8444-444444444444",
		"after boundary",
	)
	insertServiceOperation(t, store, registered.Binding.Scope, 9, active, "active")
	want, err := state.ApplyOperation(rebased, active)
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.Status(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateDigest != want.Digest || got.OverlayGeneration != 9 {
		t.Fatalf("rebased status digest=%q generation=%d, want %q/9", got.CandidateDigest, got.OverlayGeneration, want.Digest)
	}
	if got.AcceptedSnapshot.Digest != accepted.Digest {
		t.Fatalf("accepted snapshot digest=%q, want %q", got.AcceptedSnapshot.Digest, accepted.Digest)
	}
}

func TestServiceDiffLastWriterAttribution(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope)
	first := servicePutTaskOperation(
		base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"first writer",
	)
	first.Actor.HumanPrincipalID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	intermediate, err := state.ApplyOperation(base.AcceptedSnapshot, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(
		intermediate,
		"99999999-9999-4999-8999-999999999992",
		"22222222-2222-4222-8222-222222222222",
		"second writer",
	)
	second.Actor.HumanPrincipalID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	wantView, err := state.ApplyOperation(intermediate, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyBatch(context.Background(), registered.Binding.Scope, []state.OperationV1{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, service = openProjectStateServiceAt(t, databasePath)

	got, err := service.Diff(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.SemanticDiff.BaseDigest != base.AcceptedSnapshot.Digest || got.SemanticDiff.ViewDigest != wantView.Digest {
		t.Fatalf("Diff digests = %q -> %q, want %q -> %q", got.SemanticDiff.BaseDigest, got.SemanticDiff.ViewDigest, base.AcceptedSnapshot.Digest, wantView.Digest)
	}
	if len(got.SemanticDiff.Changes) != 1 || got.SemanticDiff.Changes[0].Key != (state.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"}) {
		t.Fatalf("Diff changes = %+v", got.SemanticDiff.Changes)
	}
	if got.SemanticDiff.Changes[0].Actor != nil || got.SemanticDiff.Changes[0].Fields[0].Actor != nil {
		t.Fatalf("mixed lifecycle writers must be conservatively unattributed: %+v", got.SemanticDiff.Changes[0])
	}
}

func TestServiceDiffAttributesDifferentKeysIndependently(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	first := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "first key")
	first.Actor.HumanPrincipalID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	intermediate, err := state.ApplyOperation(base, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(intermediate, "99999999-9999-4999-8999-999999999992", "33333333-3333-4333-8333-333333333333", "second key")
	second.Actor.HumanPrincipalID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, err := service.ApplyBatch(context.Background(), registered.Binding.Scope, []state.OperationV1{first, second}); err != nil {
		t.Fatal(err)
	}

	got, err := service.Diff(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SemanticDiff.Changes) != 2 || got.SemanticDiff.Changes[0].Actor == nil || *got.SemanticDiff.Changes[0].Actor != first.Actor || got.SemanticDiff.Changes[1].Actor == nil || *got.SemanticDiff.Changes[1].Actor != second.Actor {
		t.Fatalf("different-key attribution = %+v", got.SemanticDiff.Changes)
	}
}

func TestServiceDiffLeavesCandidateOnlyChangesUnattributed(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "direct only")
	direct, err := state.ApplyOperation(accepted, operation)
	if err != nil {
		t.Fatal(err)
	}
	insertServiceCandidate(t, store, registered.Binding.Scope, accepted.Digest, direct, nil, 0)

	got, err := service.Diff(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SemanticDiff.Changes) != 1 || got.SemanticDiff.Changes[0].Actor != nil || got.SemanticDiff.ViewDigest != direct.Digest {
		t.Fatalf("candidate-only Diff = %+v", got)
	}
}

func TestServiceDiffAttributesOnlyPostRebaseActiveRows(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	directOperation := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "direct")
	directOperation.Actor.HumanPrincipalID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	direct, err := state.ApplyOperation(accepted, directOperation)
	if err != nil {
		t.Fatal(err)
	}
	rebasedOperation := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999992", "22222222-2222-4222-8222-222222222222", "rebased")
	rebasedOperation.Actor.HumanPrincipalID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	rebased, err := state.ApplyOperation(accepted, rebasedOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertServiceCandidate(t, store, registered.Binding.Scope, accepted.Digest, direct, &rebased, 7)
	insertServiceOperation(t, store, registered.Binding.Scope, 5, rebasedOperation, "rebased")
	active := servicePutTaskOperation(rebased, "99999999-9999-4999-8999-999999999993", "33333333-3333-4333-8333-333333333333", "post-boundary")
	active.Actor.HumanPrincipalID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	insertServiceOperation(t, store, registered.Binding.Scope, 9, active, "active")

	got, err := service.Diff(context.Background(), registered.Binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SemanticDiff.Changes) != 2 || got.SemanticDiff.Changes[0].Key.ID != "22222222-2222-4222-8222-222222222222" || got.SemanticDiff.Changes[0].Actor != nil {
		t.Fatalf("rebased candidate attribution = %+v", got.SemanticDiff.Changes)
	}
	if got.SemanticDiff.Changes[1].Key.ID != "33333333-3333-4333-8333-333333333333" || got.SemanticDiff.Changes[1].Actor == nil || *got.SemanticDiff.Changes[1].Actor != active.Actor {
		t.Fatalf("post-boundary attribution = %+v", got.SemanticDiff.Changes)
	}
}

func TestServiceDiffScopeIsolationAndCorruptionIsReadOnly(t *testing.T) {
	projectID := "00000000-0000-4000-8000-000000000001"
	repositoryA := createGitRepository(t, projectID)
	repositoryB := createGitRepository(t, projectID)
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	a := registerGitRepository(t, service, repositoryA)
	b := registerGitRepository(t, service, repositoryB)
	base := mustServiceStatus(t, service, a.Binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "workspace A")
	if _, err := service.Apply(context.Background(), a.Binding.Scope, operation); err != nil {
		t.Fatal(err)
	}
	const corruptJSON = "{"
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state)
		VALUES (?, ?, 1, '99999999-9999-4999-8999-999999999992', ?, 'active')
	`, b.Binding.Scope.ProjectID, b.Binding.Scope.WorkspaceID, corruptJSON); err != nil {
		t.Fatal(err)
	}

	healthy, err := service.Diff(context.Background(), a.Binding.Scope)
	if err != nil || len(healthy.SemanticDiff.Changes) != 1 {
		t.Fatalf("healthy workspace Diff = %+v, %v", healthy, err)
	}
	corrupt, err := service.Diff(context.Background(), b.Binding.Scope)
	if err == nil || !reflect.DeepEqual(corrupt, WorkspaceDiff{}) {
		t.Fatalf("corrupt workspace Diff = %+v, %v", corrupt, err)
	}
	wrong := a.Binding.Scope
	wrong.WorkspaceID = types.WorkspaceID("ffffffff-ffff-4fff-8fff-ffffffffffff")
	missing, err := service.Diff(context.Background(), wrong)
	if !errors.Is(err, localstore.ErrNotFound) || !reflect.DeepEqual(missing, WorkspaceDiff{}) {
		t.Fatalf("wrong-scope Diff = %+v, %v", missing, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
	afterRestart, err := reopenedService.Diff(context.Background(), a.Binding.Scope)
	if err != nil || !reflect.DeepEqual(afterRestart, healthy) {
		t.Fatalf("restarted healthy Diff = %+v, %v; want %+v", afterRestart, err, healthy)
	}
	var raw []byte
	if err := reopenedStore.DB().QueryRow(`
		SELECT operation_json FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND generation=1
	`, b.Binding.Scope.ProjectID, b.Binding.Scope.WorkspaceID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) != corruptJSON || readServiceWorkspaceState(t, reopenedStore, b.Binding.Scope) != "clean" {
		t.Fatalf("Diff mutated corrupt workspace: json=%q state=%q", raw, readServiceWorkspaceState(t, reopenedStore, b.Binding.Scope))
	}
}

func TestApplyReturnsComposedWorkspaceStatus(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope)
	operation := servicePutTaskOperation(
		base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"single apply",
	)
	want, err := state.ApplyOperation(base.AcceptedSnapshot, operation)
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.Apply(context.Background(), registered.Binding.Scope, operation)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "pending" || got.CandidateDigest != want.Digest || got.OverlayGeneration != 1 {
		t.Fatalf("Apply status=%+v, want pending/%q/1", got, want.Digest)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.CandidateDigest != got.CandidateDigest || status.OverlayGeneration != got.OverlayGeneration || status.State != got.State {
		t.Fatalf("Status after Apply=%+v, Apply returned %+v", status, got)
	}
}

func TestApplyBatchAppendsConsecutiveChainedOperationsDurably(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope)
	first := servicePutTaskOperation(
		base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"first batch operation",
	)
	intermediate, err := state.ApplyOperation(base.AcceptedSnapshot, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(
		intermediate,
		"99999999-9999-4999-8999-999999999992",
		"33333333-3333-4333-8333-333333333333",
		"second batch operation",
	)
	want, err := state.ApplyOperation(intermediate, second)
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.ApplyBatch(context.Background(), registered.Binding.Scope, []state.OperationV1{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "pending" || got.CandidateDigest != want.Digest || got.OverlayGeneration != 2 {
		t.Fatalf("ApplyBatch status=%+v, want pending/%q/2", got, want.Digest)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openProjectStateServiceAt(t, databasePath)
	afterRestart := mustServiceStatus(t, reopened, registered.Binding.Scope)
	if afterRestart.CandidateDigest != want.Digest || afterRestart.OverlayGeneration != 2 || afterRestart.State != "pending" {
		t.Fatalf("status after reopen=%+v, want pending/%q/2", afterRestart, want.Digest)
	}
	if afterRestart.AcceptedSnapshot.Digest != base.AcceptedSnapshot.Digest {
		t.Fatalf("ApplyBatch changed accepted snapshot: got %q want %q", afterRestart.AcceptedSnapshot.Digest, base.AcceptedSnapshot.Digest)
	}
}

func TestApplyRejectsGenerationNotAboveComposedBoundary(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot

	directOperation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"direct candidate",
	)
	direct, err := state.ApplyOperation(accepted, directOperation)
	if err != nil {
		t.Fatal(err)
	}
	rebasedOperation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999992",
		"33333333-3333-4333-8333-333333333333",
		"rebased candidate",
	)
	rebased, err := state.ApplyOperation(accepted, rebasedOperation)
	if err != nil {
		t.Fatal(err)
	}
	insertServiceCandidate(t, store, registered.Binding.Scope, accepted.Digest, direct, &rebased, 7)
	operation := servicePutTaskOperation(
		rebased,
		"99999999-9999-4999-8999-999999999993",
		"44444444-4444-4444-8444-444444444444",
		"must not be hidden below boundary",
	)

	if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err == nil {
		t.Fatal("Apply accepted a generation that would be hidden below the composed boundary")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
	if got := countServiceOperations(t, reopenedStore, registered.Binding.Scope); got != 0 {
		t.Fatalf("failed Apply persisted %d operation rows", got)
	}
	status := mustServiceStatus(t, reopenedService, registered.Binding.Scope)
	if status.State != "clean" || status.CandidateDigest != rebased.Digest || status.OverlayGeneration != 7 {
		t.Fatalf("status after failed Apply=%+v, want clean/%q/7", status, rebased.Digest)
	}
}

func TestApplyTargetingOpenConflictRollsBack(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope)
	operation := servicePutTaskOperation(
		base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"conflicted target",
	)
	insertServiceConflict(t, store, registered.Binding.Scope, "conflict-target", state.RecordKey{Kind: "task", ID: operation.PutRecord.Record.Task.ID}, "open")
	setServiceWorkspaceState(t, store, registered.Binding.Scope, "conflicted")

	if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); !errors.Is(err, localstore.ErrWorkspaceConflicted) {
		t.Fatalf("Apply targeted conflict error=%v, want ErrWorkspaceConflicted", err)
	}
	if got := countServiceOperations(t, store, registered.Binding.Scope); got != 0 {
		t.Fatalf("targeted conflict persisted %d operations", got)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.State != "conflicted" || status.CandidateDigest != base.CandidateDigest || status.OverlayGeneration != 0 {
		t.Fatalf("status after targeted conflict=%+v", status)
	}
}

func TestApplyUnrelatedToOpenConflictPreservesConflictedState(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope)
	operation := servicePutTaskOperation(
		base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"unrelated edit",
	)
	insertServiceConflict(t, store, registered.Binding.Scope, "conflict-other", state.RecordKey{Kind: "task", ID: "33333333-3333-4333-8333-333333333333"}, "open")
	setServiceWorkspaceState(t, store, registered.Binding.Scope, "conflicted")
	want, err := state.ApplyOperation(base.AcceptedSnapshot, operation)
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.Apply(context.Background(), registered.Binding.Scope, operation)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "conflicted" || got.CandidateDigest != want.Digest || got.OverlayGeneration != 1 {
		t.Fatalf("unrelated Apply status=%+v, want conflicted/%q/1", got, want.Digest)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.State != "conflicted" || status.CandidateDigest != want.Digest || status.OverlayGeneration != 1 {
		t.Fatalf("persisted unrelated Apply status=%+v", status)
	}
}

func TestStatusAndApplyRejectConflictStateMismatch(t *testing.T) {
	tests := []struct {
		name           string
		workspaceState string
		openConflict   bool
	}{
		{name: "clean with open conflict", workspaceState: "clean", openConflict: true},
		{name: "pending with open conflict", workspaceState: "pending", openConflict: true},
		{name: "conflicted without open conflict", workspaceState: "conflicted", openConflict: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, service := openProjectStateServiceAt(t, databasePath)
			registered := registerGitRepository(t, service, repository)
			base := mustServiceStatus(t, service, registered.Binding.Scope)
			if test.openConflict {
				insertServiceConflict(t, store, registered.Binding.Scope, "state-mismatch", state.RecordKey{Kind: "task", ID: "33333333-3333-4333-8333-333333333333"}, "open")
			}
			setServiceWorkspaceState(t, store, registered.Binding.Scope, test.workspaceState)
			if _, err := service.Status(context.Background(), registered.Binding.Scope); err == nil {
				t.Fatal("Status accepted mismatched conflict state and evidence")
			}
			if got, err := service.Diff(context.Background(), registered.Binding.Scope); err == nil || !reflect.DeepEqual(got, WorkspaceDiff{}) {
				t.Fatalf("Diff accepted mismatched conflict state and evidence: got=%+v err=%v", got, err)
			}
			operation := servicePutTaskOperation(
				base.AcceptedSnapshot,
				"99999999-9999-4999-8999-999999999991",
				"22222222-2222-4222-8222-222222222222",
				"must not apply through mismatch",
			)
			if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err == nil {
				t.Fatal("Apply accepted mismatched conflict state and evidence")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, _ := openProjectStateServiceAt(t, databasePath)
			if got := countServiceOperations(t, reopenedStore, registered.Binding.Scope); got != 0 {
				t.Fatalf("mismatched workspace persisted %d operations", got)
			}
			if got := readServiceWorkspaceState(t, reopenedStore, registered.Binding.Scope); got != test.workspaceState {
				t.Fatalf("workspace state after reopen=%q, want %q", got, test.workspaceState)
			}
		})
	}
}

func TestStatusAndApplyConflictStateInvariantControls(t *testing.T) {
	t.Run("consistent conflicted workspace", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		base := mustServiceStatus(t, service, registered.Binding.Scope)
		insertServiceConflict(t, store, registered.Binding.Scope, "consistent-open", state.RecordKey{Kind: "task", ID: "33333333-3333-4333-8333-333333333333"}, "open")
		setServiceWorkspaceState(t, store, registered.Binding.Scope, "conflicted")
		if status, err := service.Status(context.Background(), registered.Binding.Scope); err != nil || status.State != "conflicted" {
			t.Fatalf("consistent Status=%+v err=%v", status, err)
		}
		operation := servicePutTaskOperation(base.AcceptedSnapshot, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "unrelated")
		if status, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err != nil || status.State != "conflicted" {
			t.Fatalf("consistent Apply=%+v err=%v", status, err)
		}
	})

	t.Run("neighbor workspace open conflict", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		primary := registerGitRepository(t, service, repository)
		neighborRoot := filepath.Join(t.TempDir(), "neighbor-worktree")
		runGit(t, repository.root, "worktree", "add", "-b", "service-conflict-neighbor", neighborRoot, repository.commit)
		neighbor, err := service.RegisterWorkspace(context.Background(), RegisterWorkspaceRequest{
			Root: neighborRoot, ExpectedProjectID: repository.projectID,
			ExpectedRepository: repository.identity, ExpectedCommit: repository.commit,
		})
		if err != nil {
			t.Fatal(err)
		}
		insertServiceConflict(t, store, neighbor.Binding.Scope, "neighbor-open", state.RecordKey{Kind: "task", ID: "33333333-3333-4333-8333-333333333333"}, "open")
		setServiceWorkspaceState(t, store, neighbor.Binding.Scope, "conflicted")
		base := mustServiceStatus(t, service, primary.Binding.Scope)
		operation := servicePutTaskOperation(base.AcceptedSnapshot, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "primary")
		if status, err := service.Apply(context.Background(), primary.Binding.Scope, operation); err != nil || status.State != "pending" {
			t.Fatalf("primary Apply with neighbor conflict=%+v err=%v", status, err)
		}
		if status, err := service.Status(context.Background(), neighbor.Binding.Scope); err != nil || status.State != "conflicted" {
			t.Fatalf("neighbor Status=%+v err=%v", status, err)
		}
	})
}

func TestApplyBatchRejectsEmptyDuplicateAndNonLocalActorsWithoutWrites(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, store *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int)
	}{
		{name: "empty", prepare: func(_ *testing.T, _ *localstore.Store, scope types.WorkspaceScope, _ state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			return scope, nil, 0
		}},
		{name: "duplicate operation IDs", prepare: func(t *testing.T, _ *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			first := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "first")
			intermediate, err := state.ApplyOperation(base, first)
			if err != nil {
				t.Fatal(err)
			}
			second := servicePutTaskOperation(intermediate, first.ID, "33333333-3333-4333-8333-333333333333", "second")
			return scope, []state.OperationV1{first, second}, 0
		}},
		{name: "non-local actor", prepare: func(_ *testing.T, _ *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			operation := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "remote")
			operation.Actor.Assurance = types.AssurancePublicKeyContinuity
			return scope, []state.OperationV1{operation}, 0
		}},
		{name: "historical actor", prepare: func(_ *testing.T, _ *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			operation := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "historical")
			operation.Actor.Assurance = types.AssuranceLegacy
			return scope, []state.OperationV1{operation}, 0
		}},
		{name: "stale second operation", prepare: func(_ *testing.T, _ *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			first := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "first")
			stale := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999992", "33333333-3333-4333-8333-333333333333", "stale")
			return scope, []state.OperationV1{first, stale}, 0
		}},
		{name: "existing operation ID collision", prepare: func(t *testing.T, store *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			operation := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "collision")
			insertServiceOperation(t, store, scope, 1, operation, "rebased")
			return scope, []state.OperationV1{operation}, 1
		}},
		{name: "wrong project scope", prepare: func(_ *testing.T, _ *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			operation := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "wrong project")
			scope.ProjectID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			return scope, []state.OperationV1{operation}, 0
		}},
		{name: "wrong workspace scope", prepare: func(_ *testing.T, _ *localstore.Store, scope types.WorkspaceScope, base state.Snapshot) (types.WorkspaceScope, []state.OperationV1, int) {
			operation := servicePutTaskOperation(base, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "wrong workspace")
			scope.WorkspaceID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			return scope, []state.OperationV1{operation}, 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, service := openProjectStateServiceAt(t, databasePath)
			registered := registerGitRepository(t, service, repository)
			base := mustServiceStatus(t, service, registered.Binding.Scope)
			callScope, operations, expectedRows := test.prepare(t, store, registered.Binding.Scope, base.AcceptedSnapshot)
			if _, err := service.ApplyBatch(context.Background(), callScope, operations); err == nil {
				t.Fatal("ApplyBatch accepted invalid input")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
			if got := countServiceOperations(t, reopenedStore, registered.Binding.Scope); got != expectedRows {
				t.Fatalf("rejected batch left %d operations, want %d", got, expectedRows)
			}
			after := mustServiceStatus(t, reopenedService, registered.Binding.Scope)
			if after.State != "clean" || after.CandidateDigest != base.CandidateDigest || after.OverlayGeneration != 0 {
				t.Fatalf("status after rejected batch=%+v", after)
			}
		})
	}
}

func TestApplyBatchInsertOrStatusFailureRollsBackEverything(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{
			name: "second insert",
			trigger: `CREATE TRIGGER reject_service_second_insert
				BEFORE INSERT ON workspace_overlay_operations
				WHEN NEW.generation=2
				BEGIN SELECT RAISE(ABORT, 'reject second service insert'); END`,
		},
		{
			name: "status update",
			trigger: `CREATE TRIGGER reject_service_status
				BEFORE UPDATE ON workspace_bindings
				WHEN NEW.status='pending'
				BEGIN SELECT RAISE(ABORT, 'reject service status'); END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, service := openProjectStateServiceAt(t, databasePath)
			registered := registerGitRepository(t, service, repository)
			base := mustServiceStatus(t, service, registered.Binding.Scope)
			first := servicePutTaskOperation(base.AcceptedSnapshot, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "first")
			intermediate, err := state.ApplyOperation(base.AcceptedSnapshot, first)
			if err != nil {
				t.Fatal(err)
			}
			second := servicePutTaskOperation(intermediate, "99999999-9999-4999-8999-999999999992", "33333333-3333-4333-8333-333333333333", "second")
			if _, err := store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}

			if _, err := service.ApplyBatch(context.Background(), registered.Binding.Scope, []state.OperationV1{first, second}); err == nil {
				t.Fatal("ApplyBatch hid an injected persistence failure")
			}
			if _, err := store.DB().Exec(`DROP TRIGGER IF EXISTS reject_service_second_insert; DROP TRIGGER IF EXISTS reject_service_status`); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
			if got := countServiceOperations(t, reopenedStore, registered.Binding.Scope); got != 0 {
				t.Fatalf("failed batch left %d operation rows", got)
			}
			after := mustServiceStatus(t, reopenedService, registered.Binding.Scope)
			if after.State != "clean" || after.CandidateDigest != base.CandidateDigest || after.OverlayGeneration != 0 {
				t.Fatalf("status after failed batch=%+v", after)
			}
		})
	}
}

func TestNewOperationsPersistExactCanonicalBytes(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	base := mustServiceStatus(t, service, registered.Binding.Scope)
	first := servicePutTaskOperation(base.AcceptedSnapshot, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "first")
	intermediate, err := state.ApplyOperation(base.AcceptedSnapshot, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(intermediate, "99999999-9999-4999-8999-999999999992", "33333333-3333-4333-8333-333333333333", "second")
	if _, err := service.ApplyBatch(context.Background(), registered.Binding.Scope, []state.OperationV1{first, second}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.DB().Query(`
		SELECT generation, operation_json FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? ORDER BY generation
	`, registered.Binding.Scope.ProjectID, registered.Binding.Scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantOperations := []state.OperationV1{first, second}
	index := 0
	for rows.Next() {
		if index >= len(wantOperations) {
			t.Fatal("unexpected extra operation row")
		}
		var generation int64
		var raw []byte
		if err := rows.Scan(&generation, &raw); err != nil {
			t.Fatal(err)
		}
		want, err := state.CanonicalOperation(wantOperations[index])
		if err != nil {
			t.Fatal(err)
		}
		if generation != int64(index+1) || !bytes.Equal(raw, want) {
			t.Fatalf("row %d generation=%d bytes=%q, want generation=%d bytes=%q", index, generation, raw, index+1, want)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(wantOperations) {
		t.Fatalf("persisted %d operations, want %d", index, len(wantOperations))
	}
}

func TestOperationTargetKeyCoversEveryOperationVariant(t *testing.T) {
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	const (
		viewDigest state.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		recordID                = "22222222-2222-4222-8222-222222222222"
	)
	referencedID := "33333333-3333-4333-8333-333333333333"
	bodyDigest := state.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	actor := types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
		Assurance: types.AssuranceLocal, OccurredAt: now,
	}
	project := state.ProjectV1{SchemaVersion: 1, Kind: "project", ID: recordID, Name: "Target", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}}
	actorRecord := state.ActorV1{SchemaVersion: 1, Kind: "actor", ID: recordID, ActorKind: types.ActorAgent, DisplayName: "Target", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{}}
	task := state.TaskV1{SchemaVersion: 1, Kind: "task", ID: recordID, ParentTaskID: &referencedID, Title: "Target", Status: "todo", Priority: 1, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}}
	taskLink := state.TaskLinkV1{SchemaVersion: 1, Kind: "task_link", ID: recordID, TaskID: referencedID, LinkType: "task", TargetID: referencedID, Extensions: state.ExtensionsV1{}}
	channel := state.ChannelV1{SchemaVersion: 1, Kind: "channel", ID: recordID, Name: "target", CreatedAt: now, Extensions: state.ExtensionsV1{}}
	event := state.EventV1{SchemaVersion: 1, Kind: "event", ID: recordID, ChannelID: referencedID, ActorID: referencedID, EventType: "targeted", Payload: []byte(`{}`), CreatedAt: now, Extensions: state.ExtensionsV1{}}
	gitLink := state.GitLinkV1{SchemaVersion: 1, Kind: "git_link", ID: recordID, TaskID: &referencedID, Repository: "acme/wormhole", ActorID: referencedID, CreatedAt: now, Extensions: state.ExtensionsV1{}}
	article := state.KBArticleV1{SchemaVersion: 1, Kind: "kb_article", ID: recordID, Title: "Target", Frontmatter: map[string]json.RawMessage{}, AuthorActorID: referencedID, RelatedArticleIDs: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}}

	operation := func(kind state.OperationKind) state.OperationV1 {
		return state.OperationV1{
			SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999999", Kind: kind,
			ExpectedViewDigest: viewDigest, Actor: actor,
		}
	}
	put := func(record state.RecordValueV1) state.OperationV1 {
		result := operation(state.OperationPutRecord)
		result.PutRecord = &state.PutRecordV1{Record: record}
		return result
	}
	tombstone := func(kind string) state.OperationV1 {
		result := operation(state.OperationTombstone)
		result.Tombstone = &state.TombstoneOperationV1{
			Key: state.RecordKey{Kind: kind, ID: recordID}, ExpectedContentDigest: viewDigest,
		}
		if kind == "kb_article" {
			result.Tombstone.ExpectedBodyDigest = &bodyDigest
		}
		return result
	}
	resurrect := func(kind string, record state.RecordValueV1) state.OperationV1 {
		result := operation(state.OperationResurrect)
		result.Resurrect = &state.ResurrectOperationV1{
			Key: state.RecordKey{Kind: kind, ID: recordID}, ExpectedTombstoneDigest: viewDigest, Record: record,
		}
		return result
	}
	resurrectArticle := operation(state.OperationResurrect)
	body := "body\n"
	resurrectArticle.Resurrect = &state.ResurrectOperationV1{
		Key: state.RecordKey{Kind: "kb_article", ID: recordID}, ExpectedTombstoneDigest: viewDigest,
		KBRecord: &article, KBBody: &body,
	}
	putArticle := operation(state.OperationPutKBArticle)
	putArticle.PutKBArticle = &state.PutKBArticleV1{Record: article, Body: body}

	tests := []struct {
		name      string
		operation state.OperationV1
		want      state.RecordKey
	}{
		{name: "put project", operation: put(state.RecordValueV1{Project: &project}), want: state.RecordKey{Kind: "project", ID: recordID}},
		{name: "put actor", operation: put(state.RecordValueV1{Actor: &actorRecord}), want: state.RecordKey{Kind: "actor", ID: recordID}},
		{name: "put task", operation: put(state.RecordValueV1{Task: &task}), want: state.RecordKey{Kind: "task", ID: recordID}},
		{name: "put task link", operation: put(state.RecordValueV1{TaskLink: &taskLink}), want: state.RecordKey{Kind: "task_link", ID: recordID}},
		{name: "put channel", operation: put(state.RecordValueV1{Channel: &channel}), want: state.RecordKey{Kind: "channel", ID: recordID}},
		{name: "put event", operation: put(state.RecordValueV1{Event: &event}), want: state.RecordKey{Kind: "event", ID: recordID}},
		{name: "put git link", operation: put(state.RecordValueV1{GitLink: &gitLink}), want: state.RecordKey{Kind: "git_link", ID: recordID}},
		{name: "put KB article", operation: putArticle, want: state.RecordKey{Kind: "kb_article", ID: recordID}},
	}
	for _, kind := range []string{"actor", "task", "task_link", "kb_article", "channel"} {
		tests = append(tests, struct {
			name      string
			operation state.OperationV1
			want      state.RecordKey
		}{name: "tombstone " + kind, operation: tombstone(kind), want: state.RecordKey{Kind: kind, ID: recordID}})
	}
	for _, test := range []struct {
		kind   string
		record state.RecordValueV1
	}{
		{kind: "actor", record: state.RecordValueV1{Actor: &actorRecord}},
		{kind: "task", record: state.RecordValueV1{Task: &task}},
		{kind: "task_link", record: state.RecordValueV1{TaskLink: &taskLink}},
		{kind: "channel", record: state.RecordValueV1{Channel: &channel}},
	} {
		tests = append(tests, struct {
			name      string
			operation state.OperationV1
			want      state.RecordKey
		}{name: "resurrect " + test.kind, operation: resurrect(test.kind, test.record), want: state.RecordKey{Kind: test.kind, ID: recordID}})
	}
	tests = append(tests, struct {
		name      string
		operation state.OperationV1
		want      state.RecordKey
	}{name: "resurrect kb_article", operation: resurrectArticle, want: state.RecordKey{Kind: "kb_article", ID: recordID}})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := state.CanonicalOperation(test.operation)
			if err != nil {
				t.Fatalf("operation is not structurally valid: %v", err)
			}
			canonical, err := state.DecodeOperation(raw)
			if err != nil {
				t.Fatalf("operation is not canonical: %v", err)
			}
			if got := operationTargetKey(canonical); got != test.want {
				t.Fatalf("operationTargetKey()=%+v, want exact target %+v", got, test.want)
			}
		})
	}
}

func TestStatusAndApplyFailClosedOnCorruptPersistedState(t *testing.T) {
	for _, test := range []struct {
		name         string
		corrupt      func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding)
		expectedRows int
	}{
		{
			name: "candidate",
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				t.Helper()
				if _, err := store.DB().Exec(`
					INSERT INTO workspace_candidates
					(project_id, workspace_id, accepted_base_digest, working_tree_digest,
					 direct_tree, rebased_tree, rebased_through_generation, imported_by, imported_at)
					VALUES (?, ?, ?, ?, ?, NULL, 0, '00000000-0000-4000-8000-000000000071', ?)
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID,
					binding.AcceptedTreeDigest, binding.AcceptedTreeDigest, []byte{1}, time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "active operation",
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				t.Helper()
				if _, err := store.DB().Exec(`
					INSERT INTO workspace_overlay_operations
					(project_id, workspace_id, generation, operation_id, operation_json, state)
					VALUES (?, ?, 1, '99999999-9999-4999-8999-999999999991', '{', 'active')
				`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			expectedRows: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, service := openProjectStateServiceAt(t, databasePath)
			registered := registerGitRepository(t, service, repository)
			base := mustServiceStatus(t, service, registered.Binding.Scope)
			test.corrupt(t, store, registered.Binding)

			if _, err := service.Status(context.Background(), registered.Binding.Scope); err == nil {
				t.Fatal("Status served corrupt persisted state")
			}
			if got, err := service.Diff(context.Background(), registered.Binding.Scope); err == nil || !reflect.DeepEqual(got, WorkspaceDiff{}) {
				t.Fatalf("Diff served corrupt persisted state: got=%+v err=%v", got, err)
			}
			operation := servicePutTaskOperation(
				base.AcceptedSnapshot,
				"99999999-9999-4999-8999-999999999992",
				"22222222-2222-4222-8222-222222222222",
				"must not persist",
			)
			if _, err := service.Apply(context.Background(), registered.Binding.Scope, operation); err == nil {
				t.Fatal("Apply wrote through corrupt persisted state")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, _ := openProjectStateServiceAt(t, databasePath)
			if got := countServiceOperations(t, reopenedStore, registered.Binding.Scope); got != test.expectedRows {
				t.Fatalf("corrupt-state Apply left %d operation rows, want %d", got, test.expectedRows)
			}
			if got := serviceWorkspaceState(t, reopenedStore, registered.Binding.Scope); got != "clean" {
				t.Fatalf("corrupt-state Apply changed workspace status to %q", got)
			}
		})
	}
}

func TestStatusAndApplyIgnoreCorruptionInNeighborWorkspace(t *testing.T) {
	projectID := "00000000-0000-4000-8000-000000000001"
	repositoryA := createGitRepository(t, projectID)
	repositoryB := createGitRepository(t, projectID)
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, service := openProjectStateServiceAt(t, databasePath)
	a := registerGitRepository(t, service, repositoryA)
	b := registerGitRepository(t, service, repositoryB)
	base := mustServiceStatus(t, service, a.Binding.Scope)
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state)
		VALUES (?, ?, 1, '99999999-9999-4999-8999-999999999991', '{', 'active')
	`, b.Binding.Scope.ProjectID, b.Binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Status(context.Background(), b.Binding.Scope); err == nil {
		t.Fatal("neighbor corruption fixture was unexpectedly readable")
	}
	operation := servicePutTaskOperation(
		base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"isolated workspace edit",
	)
	got, err := service.Apply(context.Background(), a.Binding.Scope, operation)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "pending" || got.OverlayGeneration != 1 {
		t.Fatalf("isolated Apply status=%+v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
	if _, err := reopenedService.Status(context.Background(), a.Binding.Scope); err != nil {
		t.Fatalf("neighbor corruption poisoned healthy workspace after reopen: %v", err)
	}
	if got := countServiceOperations(t, reopenedStore, a.Binding.Scope); got != 1 {
		t.Fatalf("healthy workspace operation rows=%d, want 1", got)
	}
	if got := countServiceOperations(t, reopenedStore, b.Binding.Scope); got != 1 {
		t.Fatalf("corrupt neighbor operation rows=%d, want 1", got)
	}
	if got := serviceWorkspaceState(t, reopenedStore, b.Binding.Scope); got != "clean" {
		t.Fatalf("healthy Apply changed neighbor status to %q", got)
	}
}

func mustServiceStatus(t *testing.T, service *Service, scope types.WorkspaceScope) WorkspaceStatus {
	t.Helper()
	status, err := service.Status(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func servicePutTaskOperation(snapshot state.Snapshot, operationID, taskID, title string) state.OperationV1 {
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	task := state.TaskV1{
		SchemaVersion: 1, Kind: "task", ID: taskID, Title: title, Description: "service operation",
		Status: "todo", Priority: 1, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
	}
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest,
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
			Assurance: types.AssuranceLocal, OccurredAt: now,
		},
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}
}

func insertServiceOperation(t *testing.T, store *localstore.Store, scope types.WorkspaceScope, generation int64, operation state.OperationV1, operationState string) {
	t.Helper()
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state)
		VALUES (?, ?, ?, ?, ?, ?)
	`, scope.ProjectID, scope.WorkspaceID, generation, operation.ID, raw, operationState); err != nil {
		t.Fatal(err)
	}
}

func insertServiceCandidate(t *testing.T, store *localstore.Store, scope types.WorkspaceScope, accepted state.Digest, direct state.Snapshot, rebased *state.Snapshot, boundary int64) {
	t.Helper()
	directBytes := encodeServiceSnapshot(t, direct)
	var rebasedBytes []byte
	if rebased != nil {
		rebasedBytes = encodeServiceSnapshot(t, *rebased)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_candidates
		(project_id, workspace_id, accepted_base_digest, working_tree_digest, direct_tree,
		 rebased_tree, rebased_through_generation, imported_by, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '00000000-0000-4000-8000-000000000071', ?)
	`, scope.ProjectID, scope.WorkspaceID, accepted, direct.Digest, directBytes, rebasedBytes, boundary,
		time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func insertServiceConflict(t *testing.T, store *localstore.Store, scope types.WorkspaceScope, conflictID string, key state.RecordKey, conflictState string) {
	t.Helper()
	digest := sha256.Sum256([]byte(conflictID))
	semanticID := fmt.Sprintf("sha256:%x", digest)
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_conflicts
		(project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id, field_path,
		 conflict_kind, base_json, ours_json, theirs_json, state)
		VALUES (?, ?, ?, ?, ?, ?, '/title', 'same_field', '{}', '{}', '{}', ?)
	`, scope.ProjectID, scope.WorkspaceID, semanticID, semanticID, key.Kind, key.ID, conflictState); err != nil {
		t.Fatal(err)
	}
}

func setServiceWorkspaceState(t *testing.T, store *localstore.Store, scope types.WorkspaceScope, workspaceState string) {
	t.Helper()
	if _, err := store.DB().Exec(`
		UPDATE workspace_bindings SET status=? WHERE project_id=? AND workspace_id=?
	`, workspaceState, scope.ProjectID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func countServiceOperations(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRow(`
		SELECT COUNT(*) FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func readServiceWorkspaceState(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) string {
	t.Helper()
	var workspaceState string
	if err := store.DB().QueryRow(`
		SELECT status FROM workspace_bindings WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&workspaceState); err != nil {
		t.Fatal(err)
	}
	return workspaceState
}

func serviceWorkspaceState(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) string {
	t.Helper()
	var workspaceState string
	if err := store.DB().QueryRow(`
		SELECT status FROM workspace_bindings WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&workspaceState); err != nil {
		t.Fatal(err)
	}
	return workspaceState
}

func encodeServiceSnapshot(t *testing.T, snapshot state.Snapshot) []byte {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	files := append(state.Tree(nil), tree...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var encoded bytes.Buffer
	write := func(value []byte) {
		if err := binary.Write(&encoded, binary.BigEndian, uint64(len(value))); err != nil {
			t.Fatal(err)
		}
		if _, err := encoded.Write(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := binary.Write(&encoded, binary.BigEndian, uint64(len(files))); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		write([]byte(file.Path))
		write(file.Data)
	}
	return encoded.Bytes()
}

func openProjectStateService(t *testing.T, backupRoot string) (*localstore.Store, *Service) {
	t.Helper()
	store, service := openProjectStateServiceAt(t, filepath.Join(t.TempDir(), "gateway.db"))
	if backupRoot == "" {
		return store, service
	}
	service, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{LegacyIntegrationBackupRoot: backupRoot})
	if err != nil {
		t.Fatal(err)
	}
	return store, service
}

func openProjectStateServiceAt(t *testing.T, databasePath string) (*localstore.Store, *Service) {
	t.Helper()
	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewService(localstore.NewWorkspaceRepo(store.DB()), ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return store, service
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("error=%v, want ErrNotFound", err)
	}
}
