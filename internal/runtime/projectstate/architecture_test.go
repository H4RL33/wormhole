package projectstate

import (
	"context"
	"encoding/json"
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
			fixture.service.readWorkingTree = func(string) (state.Tree, error) {
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
				fixture.service.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000011", nil }
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
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newArchitectureFixture(t)
			defer fixture.store.Close()
			test.prepare(t, &fixture)
			corruptArchitectureUnrelatedTerminalOperation(t, fixture.store, fixture.target.Scope)

			beforeAudit := captureArchitectureRawDB(t, fixture.store)
			if err := fixture.service.repo.AuditWorkspaceHistory(context.Background(), fixture.target.Scope); err == nil {
				t.Fatal("AuditWorkspaceHistory accepted corrupt terminal operation")
			}
			if err := fixture.service.repo.AuditWorkspaceHistory(context.Background(), fixture.sibling.Scope); err != nil {
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

			blocked := newArchitectureFixture(t)
			defer blocked.store.Close()
			test.prepare(t, &blocked)
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
	fixture.service.newStashID = func() (string, error) { return stashID, nil }
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
	fixture.service.newStashID = func() (string, error) { return "20000000-0000-4000-8000-000000000001", nil }
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
	fixture.service.now = func() time.Time { panic("corrupt stash restore consulted mutation clock") }
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
	fixture.service.now = func() time.Time { panic("corrupt publication policy reached invalidation mutation") }
	got, err := fixture.service.Status(context.Background(), fixture.target.Scope)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("Status() result=%+v, want zero", got)
	}
	return err
}

func attemptArchitectureRecover(t *testing.T, fixture *architectureFixture) error {
	t.Helper()
	fixture.service.observeCheckpointRecoveryGit = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryGitObservation, error) {
		panic("corrupt recovery journal observed Git")
	}
	fixture.service.recoverCheckpointFilesystem = func(context.Context, checkpointRecoveryProof) (checkpointRecoveryFilesystemOutcome, error) {
		panic("corrupt recovery journal touched filesystem")
	}
	got, err := fixture.service.Recover(context.Background(), fixture.recoverScope)
	if !reflect.DeepEqual(got, WorkspaceStatus{}) {
		t.Fatalf("Recover() result=%+v, want zero", got)
	}
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
