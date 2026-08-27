package projectstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestStage1ASoleWorkspaceAuthorities(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)

	t.Run("superseded authorities stay absent", func(t *testing.T) {
		for _, forbidden := range []string{
			"Workspace" + "CheckpointCommitState",
			"Capture" + "CheckpointCommitState",
			"Confirm" + "CheckpointCommit",
			"Workspace" + "Materialization" + "Disposition",
			"Materialization" + "Disposition",
			"Workspace" + "Restore" + "RetryState",
			"Restore" + "RetryState",
			"." + "Operation" + "Audit(",
			"." + "PublicationPolicy" + "History(",
		} {
			assertArchitectureSourceAbsent(t, sources, forbidden)
		}
	})

	t.Run("one owner remains for each authority family", func(t *testing.T) {
		assertArchitectureSourceMatchesExactly(t, sources, "localstore."+"Open(", "cmd/gatewayd/gatewayd.go", 1)
		for _, authority := range []string{
			"type workspace" + "RevisionTracker struct",
			"func (tx *WorkspaceMutationTx) mark" + "WorkspaceDirty",
			"func (tx *WorkspaceMutationTx) finalize" + "WorkspaceRevision",
		} {
			assertArchitectureSourceMatchesExactly(t, sources, authority, "internal/runtime/localstore/workspace_revision_repo.go", 1)
		}
		assertArchitectureSourceMatchesExactly(t, sources,
			"func (tx *WorkspaceMutationTx) Current"+"Materialization",
			"internal/runtime/localstore/workspace_current_mutation_repo.go", 1)
		assertArchitectureSourceMatchesExactly(t, sources,
			"func (tx *WorkspaceMutationTx) Restore"+"CurrentState",
			"internal/runtime/localstore/workspace_restore_retry_repo.go", 1)
		assertArchitectureSourceMatchesExactly(t, sources,
			"func (r *WorkspaceRepo) Audit"+"WorkspaceHistory",
			"internal/runtime/localstore/workspace_history_audit.go", 1)
		assertArchitectureSourceMatchesExactly(t, sources,
			"func (r *WorkspaceRepo) Confirm"+"WorkspaceCommit",
			"internal/runtime/localstore/workspace_commit_confirmation.go", 1)
	})

	t.Run("workspace representation sweep stays frozen", func(t *testing.T) {
		workspaceSources := make(map[string]string)
		for path, source := range sources {
			if strings.HasPrefix(path, "internal/runtime/localstore/workspace") {
				workspaceSources[path] = source
			}
		}
		for _, forbidden := range []string{"CAST(", "StorageClasses"} {
			assertArchitectureSourceAbsent(t, workspaceSources, forbidden)
		}
		typeofCount := 0
		for _, source := range workspaceSources {
			typeofCount += strings.Count(source, "typeof(")
		}
		if typeofCount != 1 {
			t.Fatalf("workspace production typeof occurrences=%d, want exactly 1", typeofCount)
		}
		assertArchitectureSourceMatchesExactly(t, workspaceSources, "typeof(workspace_revision)",
			"internal/runtime/localstore/workspace_repo.go", 1)
		if count := architectureRegexpCount(workspaceSources,
			regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*Raw\b`)); count != 0 {
			t.Fatalf("workspace production identifiers ending Raw=%d, want 0", count)
		}
	})
}

func TestServiceRegistrationCoordinatorAuthority(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	if !strings.Contains(service, "registrationCoordinator") {
		t.Fatal("service.go must own a registrationCoordinator")
	}
	delegates := map[string]string{
		"RegisterWorkspace":       "registration.registerWorkspace(",
		"ResolveWorkingDirectory": "s.registration.resolveWorkingDirectory(",
		"RegisteredWorkspaces":    "s.registration.registeredWorkspaces(",
	}
	for method, delegation := range delegates {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if body == "" {
			t.Fatalf("service.go missing facade method %s", method)
		}
		if !strings.Contains(body, delegation) {
			t.Fatalf("service.go method %s must delegate to registration coordinator", method)
		}
		if count := strings.Count(body, delegation); count != 1 {
			t.Fatalf("service.go method %s has %d registration coordinator delegates, want exactly one", method, count)
		}
		if strings.Contains(body, "inspectCommittedWorkspace(") || strings.Contains(body, "filepath.EvalSymlinks(") || strings.Contains(body, "verifyBindingCheckout(") || strings.Contains(body, ".repo.RegisterWorkspace(") {
			t.Fatalf("service.go method %s retains registration, path resolution, or checkout verification logic", method)
		}
	}
}

func TestServiceWorkspaceCoordinatorAuthority(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	if !strings.Contains(service, "workspaceCoordinator") {
		t.Fatal("service.go must own a workspaceCoordinator")
	}
	delegates := map[string]string{
		"Status":     "s.workspace.status(",
		"Diff":       "s.workspace.diff(",
		"Apply":      "s.workspace.apply(",
		"ApplyBatch": "s.workspace.applyBatch(",
	}
	for method, delegation := range delegates {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if body == "" {
			t.Fatalf("service.go missing facade method %s", method)
		}
		if strings.Count(body, delegation) != 1 {
			t.Fatalf("service.go method %s must have exactly one workspace coordinator delegate", method)
		}
		if strings.Contains(body, "WithImmediateWorkspace(") || strings.Contains(body, "readComposedWorkspace(") || strings.Contains(body, "readPublicationReview(") {
			t.Fatalf("service.go method %s retains workspace composition or transaction logic", method)
		}
	}
	for _, forbidden := range []string{"readComposedWorkspace(", "loadComposedWorkspace(", "Compose("} {
		if strings.Contains(service, forbidden) {
			t.Fatalf("service.go retains workspace authority %q", forbidden)
		}
	}
	workspace := sources["internal/runtime/projectstate/workspace_coordinator.go"]
	if !strings.Contains(workspace, "func (c *workspaceCoordinator) applyBatch(") || !strings.Contains(workspace, "func readComposedWorkspace(") {
		t.Fatal("workspace coordinator must own mutation and composition entrypoints")
	}
	if strings.Contains(workspace, ".Status(") || strings.Contains(workspace, ".Diff(") || strings.Contains(workspace, ".Apply(") || strings.Contains(workspace, ".ApplyBatch(") {
		t.Fatal("workspace coordinator must not call public Service workspace methods")
	}
}

func TestServicePublicationCoordinatorAuthority(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	if !strings.Contains(service, "publicationCoordinator") {
		t.Fatal("service.go must own a publicationCoordinator")
	}
	for method, delegation := range map[string]string{
		"PublicationConfiguration": "s.publication.configuration(",
		"ReconfigurePublication":   "s.publication.reconfigure(",
	} {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if strings.Count(body, delegation) != 1 {
			t.Fatalf("service.go method %s must delegate exactly once", method)
		}
		if strings.Contains(body, "WithImmediateWorkspace(") || strings.Contains(body, "observePublication") || strings.Contains(body, "newPublicationInvalidation(") {
			t.Fatalf("service.go method %s retains publication authority", method)
		}
	}
	forbidden := []string{"s.publicationReviewInTransaction(", "s.publicationTrustObserver()"}
	for _, source := range []string{sources["internal/runtime/projectstate/checkpoint.go"], sources["internal/runtime/projectstate/workspace_coordinator.go"]} {
		for _, value := range forbidden {
			if strings.Contains(source, value) {
				t.Fatalf("production coordinator consumer retains Service publication shim %q", value)
			}
		}
	}
	if strings.Contains(service, "syncPublicationCoordinator") {
		t.Fatal("service.go must not synchronize mutable publication seams at runtime")
	}
	for _, path := range []string{
		"internal/runtime/projectstate/publication_coordinator.go",
		"internal/runtime/projectstate/publication_policy.go",
		"internal/runtime/projectstate/publication_review_service.go",
	} {
		if strings.Contains(sources[path], "*Service") {
			t.Fatalf("publication owner %s must not depend on Service", path)
		}
	}
}

func TestServiceCheckpointCoordinatorAuthority(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	if !strings.Contains(service, "checkpointCoordinator") {
		t.Fatal("service.go must own a checkpointCoordinator")
	}
	for method, delegation := range map[string]string{
		"Checkpoint": "s.checkpoint.checkpoint(",
		"Recover":    "s.checkpoint.recover(",
	} {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if strings.Count(body, delegation) != 1 {
			t.Fatalf("service.go method %s must delegate exactly once", method)
		}
		if strings.Contains(body, "WithImmediateWorkspace(") || strings.Contains(body, "checkpointGate") || strings.Contains(body, "prepareCheckpointArtifact") {
			t.Fatalf("service.go method %s retains checkpoint or recovery authority", method)
		}
	}
	coordinator := sources["internal/runtime/projectstate/checkpoint_coordinator.go"]
	if strings.Count(coordinator, "*checkpointGateSet") != 1 {
		t.Fatalf("checkpoint coordinator must own exactly one gate pointer")
	}
	for _, source := range []string{sources["internal/runtime/projectstate/checkpoint.go"], sources["internal/runtime/projectstate/checkpoint_recovery.go"]} {
		if strings.Contains(source, "*Service") {
			t.Fatal("checkpoint/recovery owners must not depend on Service")
		}
	}
	if strings.Contains(coordinator, "checkpointGates") || strings.Contains(service, "checkpointGates") {
		t.Fatal("checkpoint gate ownership must be coordinator-local")
	}
}

func TestServiceGitBaseCoordinatorAuthority(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	if !strings.Contains(service, "gitBaseCoordinator") {
		t.Fatal("service.go must own a gitBaseCoordinator")
	}
	for method, delegation := range map[string]string{
		"ObserveGitBase":   "s.gitBase.observe(",
		"RefreshWorkspace": "s.gitBase.refresh(",
	} {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if strings.Count(body, delegation) != 1 {
			t.Fatalf("service.go method %s must delegate exactly once", method)
		}
		if strings.Contains(body, "observeGitBaseOutside(") || strings.Contains(body, "WithImmediateWorkspace") || strings.Contains(body, "readGitBasePosition(") {
			t.Fatalf("service.go method %s retains Git-base authority", method)
		}
	}
	if strings.Contains(service, "observeGitBase                   ") {
		t.Fatal("Service must not own the Git observer seam")
	}
	for _, path := range []string{
		"internal/runtime/projectstate/git_observer.go",
		"internal/runtime/projectstate/git_base_coordinator.go",
	} {
		source := sources[path]
		if strings.Contains(source, "*Service") || strings.Contains(source, ".ObserveGitBase(") || strings.Contains(source, ".RefreshWorkspace(") {
			t.Fatalf("Git-base owner %s must not depend on Service or public facade", path)
		}
	}
	if strings.Contains(service, "syncGitBase") {
		t.Fatal("service.go must not synchronize mutable Git-base seams")
	}
}

func TestServiceTransitionCoordinatorAuthority(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	if !strings.Contains(service, "transitionCoordinator") {
		t.Fatal("service.go must own a transitionCoordinator")
	}
	for method, delegation := range map[string]string{
		"Import":          "s.transition.importWorkspace(",
		"ReconcileImport": "s.transition.importWorkspace(",
		"Stash":           "s.transition.stash(",
		"RestoreStash":    "s.transition.restoreStash(",
	} {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if strings.Count(body, delegation) != 1 {
			t.Fatalf("service.go method %s must delegate exactly once", method)
		}
		if strings.Contains(body, "WithImmediateWorkspace") || strings.Contains(body, "readWorkingTree(") || strings.Contains(body, "newStashID(") {
			t.Fatalf("service.go method %s retains transition authority", method)
		}
	}
	serviceType := architectureMethodBody(service, "type Service struct")
	if strings.Contains(serviceType, "readWorkingTree") || strings.Contains(serviceType, "newStashID") {
		t.Fatal("Service must not own transition-specific seams")
	}
	for _, path := range []string{
		"internal/runtime/projectstate/transition_coordinator.go",
		"internal/runtime/projectstate/import.go",
		"internal/runtime/projectstate/stash.go",
		"internal/runtime/projectstate/restore.go",
	} {
		source := sources[path]
		if strings.Contains(source, "*Service") || strings.Contains(source, ".Import(") || strings.Contains(source, ".Stash(") || strings.Contains(source, ".RestoreStash(") {
			t.Fatalf("transition owner %s must not depend on Service or public facade", path)
		}
	}
}

func TestProjectstateServiceIsCoordinatorFacade(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sources := architectureProductionGoSources(t, root)
	service := sources["internal/runtime/projectstate/service.go"]
	typeBody := architectureMethodBody(service, "type Service struct")
	wantFields := []string{"registration", "workspace", "publication", "checkpoint", "gitBase", "transition"}
	for _, field := range wantFields {
		if !regexp.MustCompile(`(?m)^\t` + field + `\s+\*`).MatchString(typeBody) {
			t.Fatalf("Service missing coordinator field %s", field)
		}
	}
	coordinatorFields := 0
	for _, line := range strings.Split(typeBody, "\n") {
		if strings.HasPrefix(line, "\t") && strings.Contains(line, "*") {
			coordinatorFields++
		}
	}
	if coordinatorFields != len(wantFields) {
		t.Fatalf("Service coordinator pointer fields=%d, want exactly %d", coordinatorFields, len(wantFields))
	}
	for _, forbidden := range []string{"repo ", "legacyBackupRoot", "registrationTimeout", "observePublication", "withImmediateWorkspace", "now ", "checkpointGate", "writer"} {
		if strings.Contains(typeBody, forbidden) {
			t.Fatalf("Service retains lifecycle dependency %q", forbidden)
		}
	}
	for _, method := range []string{"PublicationConfiguration", "ReconfigurePublication", "RegisterWorkspace", "ResolveWorkingDirectory", "RegisteredWorkspaces", "ObserveGitBase", "RefreshWorkspace", "Status", "Diff", "Apply", "ApplyBatch", "Checkpoint", "Recover", "Import", "Stash", "RestoreStash"} {
		body := architectureMethodBody(service, "func (s *Service) "+method)
		if body == "" {
			t.Fatalf("Service missing public facade method %s", method)
		}
		if strings.Contains(body, ".repo.") || strings.Contains(body, "WithImmediateWorkspace") || strings.Contains(body, "observePublication") || strings.Contains(body, "readWorkingTree(") || strings.Contains(body, "newStashID(") {
			t.Fatalf("Service method %s retains direct lifecycle authority", method)
		}
	}
}

func architectureMethodBody(source, signature string) string {
	start := strings.Index(source, signature)
	if start < 0 {
		return ""
	}
	next := strings.Index(source[start+len(signature):], "\nfunc ")
	if next < 0 {
		return source[start:]
	}
	return source[start : start+len(signature)+next]
}

func TestCompactTargetedCommitConfirmationNeverReplays(t *testing.T) {
	t.Run("journal", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
		realWithImmediate := fixture.service.registration.repo.WithImmediateWorkspace
		transactionCalls := 0
		unknown := fmt.Errorf("synthetic compact confirmation: %w", localstore.ErrCommitOutcomeUnknown)
		fixture.service.checkpoint.withImmediateWorkspace = func(
			ctx context.Context,
			scope types.WorkspaceScope,
			fn func(*localstore.WorkspaceMutationTx) error,
		) error {
			transactionCalls++
			err := realWithImmediate(ctx, scope, fn)
			if err == nil && transactionCalls == 2 {
				return unknown
			}
			return err
		}
		confirmationCalls := 0
		fixture.service.checkpoint.confirmWorkspaceCommit = func(
			context.Context,
			localstore.WorkspaceCommitConfirmation,
			localstore.WorkspaceCommitConfirmation,
		) (localstore.WorkspaceCommitMatch, error) {
			confirmationCalls++
			return localstore.WorkspaceCommitNext, nil
		}

		result, err := fixture.service.Checkpoint(context.Background(), req)
		if err != nil || result.JournalID == "" {
			t.Fatalf("compact journal confirmation=(%+v,%v), want successful result", result, err)
		}
		if confirmationCalls != 1 || transactionCalls != 2 || fixture.prepareCalls != 1 ||
			fixture.publishCalls != 1 || fixture.closeCalls != 1 {
			t.Fatalf("confirmation=%d transactions=%d prepare=%d publish=%d close=%d, want 1/2/1/1/1 without replay",
				confirmationCalls, transactionCalls, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
		}
	})
	t.Run("publication", func(t *testing.T) {
		fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
		current := mustPublicationConfiguration(t, fixture.service, fixture.binding.Scope)
		req := publicationRequest(t, fixture.binding, current, types.PublicationPublicGit, diffActorEnvelope())
		fixture.service.publication.now = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
		realWithImmediate := fixture.service.registration.repo.WithImmediateWorkspace
		transactionCalls := 0
		unknown := fmt.Errorf("synthetic compact publication confirmation: %w", localstore.ErrCommitOutcomeUnknown)
		fixture.service.publication.withImmediateWorkspace = func(
			ctx context.Context,
			scope types.WorkspaceScope,
			fn func(*localstore.WorkspaceMutationTx) error,
		) error {
			transactionCalls++
			err := realWithImmediate(ctx, scope, fn)
			if err == nil {
				return unknown
			}
			return err
		}
		got, err := fixture.service.ReconfigurePublication(context.Background(), req)
		if err != nil || got.PolicyRevision != current.PolicyRevision+1 {
			t.Fatalf("compact publication confirmation=(%+v,%v), want successful transition", got, err)
		}
		if transactionCalls != 1 {
			t.Fatalf("transactions=%d, want 1 without replay", transactionCalls)
		}
	})
}

func architectureProductionGoSources(t *testing.T, root string) map[string]string {
	t.Helper()
	sources := make(map[string]string)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(relative)] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return sources
}

func assertArchitectureSourceAbsent(t *testing.T, sources map[string]string, forbidden string) {
	t.Helper()
	for path, source := range sources {
		if strings.Contains(source, forbidden) {
			t.Fatalf("superseded authority %q remains in %s", forbidden, path)
		}
	}
}

func assertArchitectureSourceMatchesExactly(t *testing.T, sources map[string]string, authority, want string, wantCount int) {
	t.Helper()
	matches := make([]string, 0, 1)
	count := 0
	for path, source := range sources {
		occurrences := strings.Count(source, authority)
		if occurrences != 0 {
			matches = append(matches, path)
			count += occurrences
		}
	}
	sort.Strings(matches)
	if !reflect.DeepEqual(matches, []string{want}) || count != wantCount {
		t.Fatalf("authority %q owners=%v occurrences=%d, want [%s] and %d", authority, matches, count, want, wantCount)
	}
}

func architectureRegexpCount(sources map[string]string, pattern *regexp.Regexp) int {
	count := 0
	for _, source := range sources {
		count += len(pattern.FindAllStringIndex(source, -1))
	}
	return count
}

func TestCoarsePrivateCorruptionFailsClosedWithoutCrossScopeMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) architectureFixture
		prepare func(*testing.T, *architectureFixture)
		corrupt func(*testing.T, *localstore.Store, types.WorkspaceBinding)
		attempt func(*testing.T, *architectureFixture) error
	}{
		{
			name: "malformed binding repository JSON",
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`UPDATE workspace_bindings SET repository_identity_json='{'
					WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed accepted snapshot",
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`UPDATE workspace_bindings SET accepted_snapshot=X'7B'
					WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed candidate direct tree",
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					INSERT INTO workspace_candidates
					(project_id,workspace_id,accepted_base_digest,working_tree_digest,direct_tree,
					 rebased_tree,rebased_through_generation,imported_by,imported_at)
					VALUES (?,?,?,?,X'7B',NULL,0,'00000000-0000-4000-8000-000000000071',?)`,
					binding.Scope.ProjectID, binding.Scope.WorkspaceID, binding.AcceptedTreeDigest,
					binding.AcceptedTreeDigest, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed active operation JSON",
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`
					INSERT INTO workspace_overlay_operations
					(project_id,workspace_id,generation,operation_id,operation_json,state)
					VALUES (?,?,1,'99999999-9999-4999-8999-999999999991','{','active')`,
					binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "malformed exact named-stash tree",
			prepare: prepareArchitectureRestoreStash,
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`UPDATE workspace_stashes SET composed_tree=X'7B'
					WHERE project_id=? AND workspace_id=? AND stash_id='20000000-0000-4000-8000-000000000001'`,
					binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: attemptArchitectureRestoreStash,
		},
		{
			name:    "semantically malformed exact named-receipt result",
			prepare: prepareArchitectureStashReceipt,
			corrupt: corruptArchitectureStashReceipt,
			attempt: attemptArchitectureStashRetry,
		},
		{
			name:    "malformed current publication policy origin digest",
			prepare: prepareArchitecturePublication,
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`UPDATE workspace_publication_policies SET origin_digest='bad'
					WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: attemptArchitectureStatus,
		},
		{
			name:    "malformed current materialization journal candidate tree",
			fixture: newArchitectureRecoveryFixture,
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`UPDATE workspace_materializations SET candidate_tree=X'7B'
					WHERE project_id=? AND workspace_id=? AND state='prepared'`,
					binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: attemptArchitectureRecover,
		},
		{
			name:    "malformed open-conflict logical evidence",
			prepare: prepareArchitectureRestoreOpenConflict,
			corrupt: func(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
				if _, err := store.DB().Exec(`UPDATE workspace_conflicts SET base_json='null'
					WHERE project_id=? AND workspace_id=? AND state='open'`,
					binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: attemptArchitectureRestoreStash,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var fixture architectureFixture
			if test.fixture != nil {
				fixture = test.fixture(t)
			} else {
				fixture = newArchitectureFixture(t)
			}
			if test.prepare != nil {
				test.prepare(t, &fixture)
			}
			test.corrupt(t, fixture.store, fixture.target)

			beforeDB := captureArchitectureRawDB(t, fixture.store)
			beforeRevisions := captureArchitectureRevisions(t, fixture.store)
			beforeTargetGit := captureArchitectureGit(t, fixture.targetRepository.root)
			beforeSiblingGit := captureArchitectureGit(t, fixture.siblingRepository.root)
			beforeArtifacts := captureArchitectureArtifacts(t, fixture.artifactRoots)
			fixture.service.transition.readWorkingTree = func(string) (state.Tree, error) {
				panic("corrupt service call reached working-tree mutation path")
			}
			attempt := test.attempt
			if attempt == nil {
				attempt = attemptArchitectureApply
			}
			if err := attempt(t, &fixture); err == nil {
				t.Fatal("corrupt service call returned no error")
			}
			assertArchitecturePreserved(t, fixture.store, beforeDB, beforeRevisions,
				fixture.targetRepository.root, beforeTargetGit,
				fixture.siblingRepository.root, beforeSiblingGit, beforeArtifacts)
		})
	}
}

func TestCurrentWorksetIgnoresTerminalCorruptionAndAuditReportsIt(t *testing.T) {
	for _, test := range []struct {
		name           string
		fixture        func(*testing.T) architectureFixture
		blockedFixture func(*testing.T) architectureFixture
		prepare        func(*testing.T, *architectureFixture)
		corruptCurrent func(*testing.T, *architectureFixture)
		attempt        func(*testing.T, *architectureFixture) error
	}{
		{
			name: "import",
			prepare: func(t *testing.T, fixture *architectureFixture) {
				direct := restoreProjectName(t, fixture.base.AcceptedSnapshot, "imported project", time.Minute)
				writeImportSnapshot(t, fixture.targetRepository.root, direct)
			},
			corruptCurrent: func(t *testing.T, fixture *architectureFixture) {
				insertServiceCandidate(t, fixture.store, fixture.target.Scope, fixture.base.AcceptedSnapshot.Digest,
					fixture.base.AcceptedSnapshot, nil, 0)
				corruptArchitectureCandidate(t, fixture)
			},
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				_, err := fixture.service.Import(context.Background(), ImportRequest{
					Scope: fixture.target.Scope, Root: fixture.targetRepository.root, Actor: diffActorEnvelope(),
				})
				return err
			},
		},
		{
			name: "stash",
			prepare: func(t *testing.T, fixture *architectureFixture) {
				fixture.service.transition.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000011", nil }
				direct := restoreProjectName(t, fixture.base.AcceptedSnapshot, "stashed project", time.Minute)
				insertServiceCandidate(t, fixture.store, fixture.target.Scope, fixture.base.AcceptedSnapshot.Digest, direct, nil, 0)
				setServiceWorkspaceState(t, fixture.store, fixture.target.Scope, "pending")
				fixture.stashRequest = StashRequest{
					Scope: fixture.target.Scope, RequestID: "40000000-0000-4000-8000-000000000011",
					Actor: diffActorEnvelope(), Label: "current-workset stash",
				}
			},
			corruptCurrent: corruptArchitectureCandidate,
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				_, err := fixture.service.Stash(context.Background(), fixture.stashRequest)
				return err
			},
		},
		{
			name: "restore",
			prepare: func(t *testing.T, fixture *architectureFixture) {
				prepareArchitectureRestoreStash(t, fixture)
			},
			corruptCurrent: func(t *testing.T, fixture *architectureFixture) {
				if _, err := fixture.store.DB().Exec(`UPDATE workspace_stashes SET composed_tree=X'7B'
					WHERE project_id=? AND workspace_id=? AND stash_id=?`, fixture.target.Scope.ProjectID,
					fixture.target.Scope.WorkspaceID, fixture.restoreRequest.StashID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				_, err := fixture.service.RestoreStash(context.Background(), fixture.restoreRequest)
				return err
			},
		},
		{
			name: "git_observation",
			prepare: func(t *testing.T, fixture *architectureFixture) {
				runGit(t, fixture.targetRepository.root, "switch", "-c", "current-workset")
			},
			corruptCurrent: func(t *testing.T, fixture *architectureFixture) {
				operation := servicePutTaskOperation(fixture.base.AcceptedSnapshot,
					"90000000-0000-4000-8000-000000000098",
					"80000000-0000-4000-8000-000000000098", "corrupt current")
				insertServiceOperation(t, fixture.store, fixture.target.Scope, 1, operation, "active")
				if _, err := fixture.store.DB().Exec(`UPDATE workspace_overlay_operations SET operation_json='{}'
					WHERE project_id=? AND workspace_id=? AND generation=1`, fixture.target.Scope.ProjectID,
					fixture.target.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
				setServiceWorkspaceState(t, fixture.store, fixture.target.Scope, "pending")
			},
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				_, err := fixture.service.ObserveGitBase(context.Background(), ObserveGitBaseRequest{
					Scope: fixture.target.Scope, ExpectedBinding: fixture.target,
					Root: fixture.targetRepository.root, ExpectedCommit: fixture.targetRepository.commit,
				})
				return err
			},
		},
		{
			name:    "checkpoint",
			fixture: newArchitectureCheckpointFixture,
			corruptCurrent: func(t *testing.T, fixture *architectureFixture) {
				if _, err := fixture.store.DB().Exec(`INSERT INTO workspace_overlay_operations
					(project_id,workspace_id,generation,operation_id,operation_json,state)
					VALUES (?,?,1,'99999999-9999-4999-8999-999999999991','{','active')`,
					fixture.target.Scope.ProjectID, fixture.target.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
				fixture.service.checkpoint.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
					panic("corrupt current checkpoint workset allocated an artifact")
				}
			},
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				_, err := fixture.service.Checkpoint(context.Background(), fixture.checkpointRequest)
				return err
			},
		},
		{
			name:           "recovery",
			blockedFixture: newArchitectureRecoveryFixture,
			prepare: func(t *testing.T, fixture *architectureFixture) {
				fixture.recoverScope = fixture.target.Scope
			},
			corruptCurrent: func(t *testing.T, fixture *architectureFixture) {
				if _, err := fixture.store.DB().Exec(`UPDATE workspace_materializations SET candidate_tree=X'7B'
					WHERE project_id=? AND workspace_id=? AND state='prepared'`,
					fixture.target.Scope.ProjectID, fixture.target.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				if err := attemptArchitectureCurrentRecovery(t, fixture); err != nil {
					return err
				}
				recoveredNew := newArchitectureRecoveredNewFixture(t)
				defer recoveredNew.store.Close()
				return attemptArchitectureCurrentRecovery(t, &recoveredNew)
			},
		},
		{
			name: "publication",
			prepare: func(t *testing.T, fixture *architectureFixture) {
				prepareArchitecturePublication(t, fixture)
			},
			corruptCurrent: func(t *testing.T, fixture *architectureFixture) {
				if _, err := fixture.store.DB().Exec(`UPDATE workspace_publication_policies SET origin_digest='bad'
					WHERE project_id=? AND workspace_id=?`, fixture.target.Scope.ProjectID,
					fixture.target.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
			attempt: func(t *testing.T, fixture *architectureFixture) error {
				current, err := fixture.service.PublicationConfiguration(context.Background(), fixture.target.Scope)
				if err != nil {
					return err
				}
				fixture.service.transition.now = func() time.Time { return time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC) }
				_, err = fixture.service.ReconfigurePublication(context.Background(), publicationRequest(
					t, fixture.target, current, types.PublicationPublicGit, diffActorEnvelope(),
				))
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := test.fixture
			if factory == nil {
				factory = newArchitectureFixture
			}
			fixture := factory(t)
			defer fixture.store.Close()
			if test.prepare != nil {
				test.prepare(t, &fixture)
			}
			corruptArchitectureUnrelatedTerminalOperation(t, fixture.store, fixture.target.Scope)

			beforeAudit := captureArchitectureRawDB(t, fixture.store)
			if err := fixture.service.registration.repo.AuditWorkspaceHistory(context.Background(), fixture.target.Scope); err == nil {
				t.Fatal("AuditWorkspaceHistory accepted corrupt terminal operation")
			}
			if err := fixture.service.registration.repo.AuditWorkspaceHistory(context.Background(), fixture.sibling.Scope); err != nil {
				t.Fatalf("sibling AuditWorkspaceHistory: %v", err)
			}
			if afterAudit := captureArchitectureRawDB(t, fixture.store); !reflect.DeepEqual(afterAudit, beforeAudit) {
				t.Fatal("AuditWorkspaceHistory mutated retained evidence")
			}

			siblingBefore := captureArchitectureGit(t, fixture.siblingRepository.root)
			siblingStatus := mustServiceStatus(t, fixture.service, fixture.sibling.Scope)
			if err := test.attempt(t, &fixture); err != nil {
				t.Fatalf("current-workset lifecycle remained coupled to corrupt terminal history: %v", err)
			}
			if siblingAfter := captureArchitectureGit(t, fixture.siblingRepository.root); !reflect.DeepEqual(siblingAfter, siblingBefore) {
				t.Fatal("current-workset lifecycle changed sibling Git state")
			}
			if after := mustServiceStatus(t, fixture.service, siblingStatus.Binding.Scope); !reflect.DeepEqual(after, siblingStatus) {
				t.Fatal("current-workset lifecycle changed sibling workspace state")
			}

			blockedFactory := test.blockedFixture
			if blockedFactory == nil {
				blockedFactory = factory
			}
			blocked := blockedFactory(t)
			defer blocked.store.Close()
			if test.prepare != nil {
				test.prepare(t, &blocked)
			}
			test.corruptCurrent(t, &blocked)
			beforeBlocked := captureArchitectureRawDB(t, blocked.store)
			beforeBlockedRevisions := captureArchitectureRevisions(t, blocked.store)
			beforeBlockedGit := captureArchitectureGit(t, blocked.targetRepository.root)
			beforeBlockedSiblingGit := captureArchitectureGit(t, blocked.siblingRepository.root)
			if err := test.attempt(t, &blocked); err == nil {
				t.Fatal("corrupt current or named authority did not block lifecycle")
			}
			assertArchitecturePreserved(t, blocked.store, beforeBlocked,
				beforeBlockedRevisions, blocked.targetRepository.root, beforeBlockedGit,
				blocked.siblingRepository.root, beforeBlockedSiblingGit, map[string]architecturePathState{})
		})
	}
}

func corruptArchitectureCandidate(t *testing.T, fixture *architectureFixture) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`UPDATE workspace_candidates SET direct_tree=X'7B'
		WHERE project_id=? AND workspace_id=?`, fixture.target.Scope.ProjectID, fixture.target.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func corruptArchitectureUnrelatedTerminalOperation(t *testing.T, store *localstore.Store, scope types.WorkspaceScope) {
	t.Helper()
	operation := servicePutTaskOperation(state.Snapshot{},
		"90000000-0000-4000-8000-000000000099",
		"80000000-0000-4000-8000-000000000099", "retained terminal")
	operation.ExpectedViewDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state)
		VALUES (?,?,?,?,?,'discarded')`, scope.ProjectID, scope.WorkspaceID, 99, operation.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET operation_json='{}'
		WHERE project_id=? AND workspace_id=? AND generation=99`, scope.ProjectID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

type architectureFixture struct {
	targetRepository, siblingRepository gitRepository
	store                               *localstore.Store
	service                             *Service
	target                              types.WorkspaceBinding
	sibling                             types.WorkspaceBinding
	base                                WorkspaceStatus
	stashRequest                        StashRequest
	restoreRequest                      RestoreStashRequest
	checkpointRequest                   CheckpointRequest
	recoverScope                        types.WorkspaceScope
	artifactRoots                       []string
}

func newArchitectureFixture(t *testing.T) architectureFixture {
	t.Helper()
	projectID := "00000000-0000-4000-8000-000000000001"
	targetRepository := createGitRepository(t, projectID)
	siblingRepository := createGitRepository(t, projectID)
	store, service := openProjectStateService(t, "")
	target := registerGitRepository(t, service, targetRepository)
	sibling := registerGitRepository(t, service, siblingRepository)
	return architectureFixture{
		targetRepository: targetRepository, siblingRepository: siblingRepository,
		store: store, service: service, target: target.Binding, sibling: sibling.Binding,
		base: mustServiceStatus(t, service, target.Binding.Scope),
	}
}

func prepareArchitectureRestoreStash(t *testing.T, fixture *architectureFixture) {
	t.Helper()
	const stashID = "20000000-0000-4000-8000-000000000001"
	fixture.service.transition.newStashID = func() (string, error) { return stashID, nil }
	direct := restoreProjectName(t, fixture.base.AcceptedSnapshot, "stashed project", time.Minute)
	insertServiceCandidate(t, fixture.store, fixture.target.Scope, fixture.base.AcceptedSnapshot.Digest, direct, nil, 0)
	setServiceWorkspaceState(t, fixture.store, fixture.target.Scope, "pending")
	fixture.stashRequest = StashRequest{
		Scope: fixture.target.Scope, RequestID: "40000000-0000-4000-8000-000000000001",
		Actor: diffActorEnvelope(), Label: "architecture stash",
	}
	if _, err := fixture.service.Stash(context.Background(), fixture.stashRequest); err != nil {
		t.Fatal(err)
	}
	fixture.restoreRequest = RestoreStashRequest{
		Scope: fixture.target.Scope, RequestID: "50000000-0000-4000-8000-000000000001",
		StashID: stashID, Actor: diffActorEnvelope(),
	}
}

func prepareArchitectureRestoreOpenConflict(t *testing.T, fixture *architectureFixture) {
	t.Helper()
	prepareArchitectureRestoreStash(t, fixture)
	insertRestoreValidStaleConflict(t, fixture.store, fixture.target.Scope)
	setServiceWorkspaceState(t, fixture.store, fixture.target.Scope, "conflicted")
}

func prepareArchitectureStashReceipt(t *testing.T, fixture *architectureFixture) {
	t.Helper()
	fixture.service.transition.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000001", nil }
	fixture.stashRequest = StashRequest{
		Scope: fixture.target.Scope, RequestID: "40000000-0000-4000-8000-000000000001",
		Actor: diffActorEnvelope(), Label: "architecture receipt",
	}
	if _, err := fixture.service.Stash(context.Background(), fixture.stashRequest); err != nil {
		t.Fatal(err)
	}
}

func prepareArchitecturePublication(t *testing.T, fixture *architectureFixture) {
	t.Helper()
	configurePublicationForTest(t, publicationServiceFixture{
		repository: fixture.targetRepository, store: fixture.store,
		service: fixture.service, binding: fixture.target,
	}, types.PublicationLocalOnly, diffActorEnvelope(), time.Date(2026, 8, 24, 12, 30, 0, 0, time.UTC))
}

func corruptArchitectureStashReceipt(t *testing.T, store *localstore.Store, binding types.WorkspaceBinding) {
	t.Helper()
	var raw []byte
	if err := store.DB().QueryRow(`SELECT result_json FROM workspace_transition_receipts
		WHERE project_id=? AND workspace_id=? AND request_id='40000000-0000-4000-8000-000000000001'`,
		binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var receipt stashReceiptV1
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Result.OperationCount = -1
	corrupt, err := state.CanonicalJSON(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE workspace_transition_receipts SET result_json=?
		WHERE project_id=? AND workspace_id=? AND request_id='40000000-0000-4000-8000-000000000001'`,
		corrupt, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func newArchitectureRecoveryFixture(t *testing.T) architectureFixture {
	t.Helper()
	checkpoint, request := recoveryDriverPlanFixture(t, "prepared")
	recoveryEnsureDriverArtifactEvidence(t, checkpoint.service, request.Scope)
	disposition := readCheckpointDisposition(t, checkpoint.service, request.Scope)
	siblingRepository := createGitRepository(t, request.Scope.ProjectID)
	_ = registerGitRepository(t, checkpoint.service, siblingRepository)
	return architectureFixture{
		targetRepository: checkpoint.repository, siblingRepository: siblingRepository,
		store: checkpoint.store, service: checkpoint.service, target: checkpoint.binding,
		recoverScope:  request.Scope,
		artifactRoots: []string{disposition.Journals[0].StagePath, disposition.Journals[0].BackupPath},
	}
}

func newArchitectureCheckpointFixture(t *testing.T) architectureFixture {
	t.Helper()
	checkpoint, request, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	siblingRepository := createGitRepository(t, request.Scope.ProjectID)
	sibling := registerGitRepository(t, checkpoint.service, siblingRepository)
	return architectureFixture{
		targetRepository: checkpoint.repository, siblingRepository: siblingRepository,
		store: checkpoint.store, service: checkpoint.service, target: checkpoint.binding,
		sibling: sibling.Binding, base: mustServiceStatus(t, checkpoint.service, checkpoint.binding.Scope),
		checkpointRequest: request,
	}
}

func newArchitectureRecoveredNewFixture(t *testing.T) architectureFixture {
	t.Helper()
	fixture := newArchitectureRecoveryFixture(t)
	if err := fixture.service.registration.repo.WithImmediateWorkspace(context.Background(), fixture.recoverScope, func(tx *localstore.WorkspaceMutationTx) error {
		proof, err := loadCheckpointRecoveryDisposition(context.Background(), tx)
		if err != nil {
			return err
		}
		return applyCheckpointRecoveryOutcome(context.Background(), tx, proof, checkpointRecoveryFilesystemRecoveredNew)
	}); err != nil {
		fixture.store.Close()
		t.Fatal(err)
	}
	return fixture
}

func attemptArchitectureApply(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	operation := servicePutTaskOperation(fixture.base.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999992",
		"22222222-2222-4222-8222-222222222222", "must not persist")
	got, err := fixture.service.Apply(context.Background(), fixture.target.Scope, operation)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("Apply() result=%+v, want zero", got)
	}
	return err
}

func attemptArchitectureRestoreStash(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	fixture.service.transition.now = func() time.Time { panic("corrupt stash restore consulted mutation clock") }
	got, err := fixture.service.RestoreStash(context.Background(), fixture.restoreRequest)
	if !reflect.DeepEqual(got, RestoreStashResult{}) {
		t.Fatalf("RestoreStash() result=%+v, want zero", got)
	}
	return err
}

func attemptArchitectureStashRetry(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	got, err := fixture.service.Stash(context.Background(), fixture.stashRequest)
	if got != (StashResult{}) {
		t.Fatalf("Stash() result=%+v, want zero", got)
	}
	return err
}

func attemptArchitectureStatus(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	fixture.service.publication.now = func() time.Time { panic("corrupt publication policy reached invalidation mutation") }
	got, err := fixture.service.Status(context.Background(), fixture.target.Scope)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("Status() result=%+v, want zero", got)
	}
	return err
}

func attemptArchitectureRecover(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	fixture.service.checkpoint.observeRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
		panic("corrupt recovery journal observed Git")
	}
	fixture.service.checkpoint.recoverFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		panic("corrupt recovery journal touched filesystem")
	}
	got, err := fixture.service.Recover(context.Background(), fixture.recoverScope)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("Recover() result=%+v, want zero", got)
	}
	return err
}

func attemptArchitectureCurrentRecovery(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	fixture.service.checkpoint.observeRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
		panic("current-workset no-op recovery observed Git")
	}
	fixture.service.checkpoint.recoverFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		panic("current-workset no-op recovery touched filesystem")
	}
	_, err := fixture.service.Recover(context.Background(), fixture.recoverScope)
	return err
}

type architectureGitState struct {
	head, index, refs, config string
	worktree                  []string
}

type architecturePathState struct {
	exists  bool
	entries []string
}

func captureArchitectureArtifacts(t *testing.T, roots []string) map[string]architecturePathState {
	t.Helper()
	result := make(map[string]architecturePathState, len(roots))
	for _, root := range roots {
		state := architecturePathState{}
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			result[root] = state
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		state.exists = true
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			data := []byte(nil)
			if !entry.IsDir() {
				data, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			state.entries = append(state.entries,
				fmt.Sprintf("%s\x00%s\x00%x", filepath.ToSlash(relative), info.Mode(), data))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		sort.Strings(state.entries)
		result[root] = state
	}
	return result
}

func captureArchitectureGit(t *testing.T, root string) architectureGitState {
	t.Helper()
	files := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && relative == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() || relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, fmt.Sprintf("%s\x00%s\x00%x", filepath.ToSlash(relative), info.Mode(), data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return architectureGitState{
		head: runGit(t, root, "rev-parse", "HEAD"), index: runGit(t, root, "ls-files", "--stage", "-z"),
		refs:   runGit(t, root, "for-each-ref", "--format=%(refname)%00%(objectname)"),
		config: runGit(t, root, "config", "--local", "--null", "--list"), worktree: files,
	}
}

func captureArchitectureRawDB(t *testing.T, store *localstore.Store) map[string][][]string {
	t.Helper()
	rows, err := store.DB().Query(`SELECT name FROM sqlite_schema
		WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	result := make(map[string][][]string, len(tables))
	for _, table := range tables {
		columns := architectureTableColumns(t, store, table)
		quoted := make([]string, len(columns))
		for index, column := range columns {
			quoted[index] = `quote("` + strings.ReplaceAll(column, `"`, `""`) + `")`
		}
		result[table] = queryImportRawRows(t, store, `SELECT `+strings.Join(quoted, ",")+
			` FROM "`+strings.ReplaceAll(table, `"`, `""`)+`" ORDER BY rowid`)
	}
	return result
}

func architectureTableColumns(t *testing.T, store *localstore.Store, table string) []string {
	t.Helper()
	rows, err := store.DB().Query(`PRAGMA table_info("` + strings.ReplaceAll(table, `"`, `""`) + `")`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	return columns
}

func captureArchitectureRevisions(t *testing.T, store *localstore.Store) [][]string {
	t.Helper()
	return queryImportRawRows(t, store, `SELECT quote(project_id),quote(workspace_id),quote(workspace_revision)
		FROM workspace_bindings ORDER BY project_id,workspace_id`)
}

func assertArchitecturePreserved(t *testing.T, store *localstore.Store, beforeDB map[string][][]string,
	beforeRevisions [][]string, targetRoot string, beforeTarget architectureGitState,
	siblingRoot string, beforeSibling architectureGitState, beforeArtifacts map[string]architecturePathState,
) {
	t.Helper()
	if after := captureArchitectureRawDB(t, store); !reflect.DeepEqual(after, beforeDB) {
		t.Fatalf("corrupt service call changed target or sibling private DB evidence\nbefore=%v\nafter=%v", beforeDB, after)
	}
	if after := captureArchitectureRevisions(t, store); !reflect.DeepEqual(after, beforeRevisions) {
		t.Fatalf("corrupt service call changed workspace revisions: before=%v after=%v", beforeRevisions, after)
	}
	if after := captureArchitectureGit(t, targetRoot); !reflect.DeepEqual(after, beforeTarget) {
		t.Fatal("corrupt service call changed target HEAD, index, refs, config, or worktree")
	}
	if after := captureArchitectureGit(t, siblingRoot); !reflect.DeepEqual(after, beforeSibling) {
		t.Fatal("corrupt service call changed sibling HEAD, index, refs, config, or worktree")
	}
	roots := make([]string, 0, len(beforeArtifacts))
	for root := range beforeArtifacts {
		roots = append(roots, root)
	}
	if after := captureArchitectureArtifacts(t, roots); !reflect.DeepEqual(after, beforeArtifacts) {
		t.Fatalf("corrupt service call changed checkpoint artifact trees: before=%v after=%v", beforeArtifacts, after)
	}
}
