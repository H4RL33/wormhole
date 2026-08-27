package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspacePublicationPolicyBootstrapAndConfiguredCAS(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11)

	var bootstrap WorkspacePublicationPolicyRecord
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		bootstrap, err = tx.PublicationPolicy(context.Background())
		if err != nil {
			return err
		}
		history, err := publicationPolicyHistoryForTest(tx, context.Background())
		if err != nil {
			return err
		}
		if len(history) != 1 || !reflect.DeepEqual(history[0], bootstrap) {
			t.Fatalf("bootstrap history=%+v, want [%+v]", history, bootstrap)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Repository != binding.Repository || bootstrap.OriginDigest != nil ||
		bootstrap.Classification != types.PublicationUnclassified || bootstrap.PolicyRevision != 1 ||
		bootstrap.TransitionKind != "bootstrap" || bootstrap.ChangedBy != nil || bootstrap.ChangedAt != nil {
		t.Fatalf("bootstrap=%+v", bootstrap)
	}

	origin := publicationTestDigest('a')
	actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
	changedAt := time.Date(2026, 8, 1, 12, 34, 56, 0, time.UTC)
	next := WorkspacePublicationPolicyRecord{
		Repository: binding.Repository, OriginDigest: &origin,
		Classification: types.PublicationPublicGit, PolicyRevision: 2,
		TransitionKind: "configured", ChangedBy: &actor, ChangedAt: &changedAt,
	}
	var configured WorkspacePublicationPolicyRecord
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		configured, err = tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{
			Expected: bootstrap,
			Next:     next,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configured, next) {
		t.Fatalf("configured=%+v, want %+v", configured, next)
	}

	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{
			Expected: bootstrap,
			Next:     next,
		})
		return err
	})
	if !errors.Is(err, ErrPublicationConfigurationCAS) {
		t.Fatalf("stale transition error=%v, want ErrPublicationConfigurationCAS", err)
	}

	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		current, err := tx.PublicationPolicy(context.Background())
		if err != nil {
			return err
		}
		history, err := publicationPolicyHistoryForTest(tx, context.Background())
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, next) || len(history) != 2 ||
			!reflect.DeepEqual(history[0], bootstrap) || !reflect.DeepEqual(history[1], next) {
			t.Fatalf("current/history=(%+v,%+v)", current, history)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publicationPolicyHistoryForTest(tx *WorkspaceMutationTx, ctx context.Context) ([]WorkspacePublicationPolicyRecord, error) {
	_, history, err := tx.auditPublicationPolicyState(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]WorkspacePublicationPolicyRecord, len(history))
	for index := range history {
		records[index] = cloneWorkspacePublicationPolicyRecord(history[index].Record)
	}
	return records, nil
}

func TestWorkspacePublicationPolicyFailsClosedOnMissingOrDisagreeingHistory(t *testing.T) {
	for _, test := range []struct {
		name              string
		currentShouldFail bool
		mutate            func(t *testing.T, store *Store, scope types.WorkspaceScope)
	}{
		{
			name:              "missing current",
			currentShouldFail: true,
			mutate: func(t *testing.T, store *Store, scope types.WorkspaceScope) {
				if _, err := store.DB().Exec(`DELETE FROM workspace_publication_policies WHERE project_id=? AND workspace_id=?`, scope.ProjectID, scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing history",
			mutate: func(t *testing.T, store *Store, scope types.WorkspaceScope) {
				if _, err := store.DB().Exec(`DELETE FROM workspace_publication_policy_history WHERE project_id=? AND workspace_id=?`, scope.ProjectID, scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "disagreeing final history",
			mutate: func(t *testing.T, store *Store, scope types.WorkspaceScope) {
				if _, err := store.DB().Exec(`UPDATE workspace_publication_policy_history SET repository_identity_json=? WHERE project_id=? AND workspace_id=?`,
					`{"provider":"github","immutable_id":"other","canonical_remote":"https://github.com/acme/other"}`,
					scope.ProjectID, scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			test.mutate(t, store, binding.Scope)
			var currentErr error
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, currentErr = tx.PublicationPolicy(context.Background())
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.currentShouldFail != (currentErr != nil) {
				t.Fatalf("PublicationPolicy error=%v, currentShouldFail=%t", currentErr, test.currentShouldFail)
			}
			if err := repo.AuditWorkspaceHistory(context.Background(), binding.Scope); err == nil {
				t.Fatal("AuditWorkspaceHistory accepted missing or disagreeing policy history")
			}
		})
	}
}

func TestWorkspaceRegistrationPublicationRowsAreAtomic(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := workspaceBinding(
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11)
	tree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	binding = bindingWithTreeDigest(t, binding, tree)
	if _, err := store.DB().Exec(`
		CREATE TRIGGER reject_publication_history_registration
		BEFORE INSERT ON workspace_publication_policy_history
		BEGIN SELECT RAISE(ABORT,'reject publication history'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.RegisterWorkspace(context.Background(), binding, tree); err == nil {
		t.Fatal("registration succeeded despite rejected history insert")
	}
	for _, table := range []string{"workspace_bindings", "workspace_publication_policies", "workspace_publication_policy_history"} {
		var count int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed registration retained %d %s rows", count, table)
		}
	}
}

func TestRepeatedWorkspaceRegistrationIgnoresProposedWorkspaceIDAndAcceptedRef(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	tree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	candidate := binding
	candidate.Scope.WorkspaceID = "00000000-0000-4000-8000-000000000099"
	candidate.AcceptedRef = "refs/heads/other"
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())
	got, created, err := repo.RegisterWorkspace(context.Background(), candidate, tree)
	if err != nil || created || got != binding {
		t.Fatalf("repeated registration=(%+v,%v,%v), want stored binding,false,nil", got, created, err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("repeated registration mutated binding/policy/history: before=%v after=%v", before, after)
	}
}

func TestWorkspacePublicationPolicyStrictReadersRejectCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, db *sql.DB, scope types.WorkspaceScope)
	}{
		{
			name: "unknown repository field",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				updatePublicationPair(t, db, scope, `repository_identity_json='{"provider":"github","immutable_id":"repo","canonical_remote":"https://github.com/acme/repo","extra":true}'`)
			},
		},
		{
			name: "trailing repository JSON",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				updatePublicationPair(t, db, scope, `repository_identity_json='{"provider":"github","immutable_id":"repo","canonical_remote":"https://github.com/acme/repo"} {}'`)
			},
		},
		{
			name: "fractional policy revision",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				withIgnoredSQLiteChecks(t, db, func() { updatePublicationPair(t, db, scope, `policy_revision=1.5`) })
			},
		},
		{
			name: "configured agent actor",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				agent := types.ActorEnvelope{
					ActorKind: types.ActorAgent, AgentID: "00000000-0000-4000-8000-000000000031",
					AccountableHumanID: "00000000-0000-4000-8000-000000000021",
					SessionID:          "session", HarnessName: "codex", HarnessVersion: "1",
					Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
				}
				actorJSON, err := projectstate.CanonicalJSON(agent)
				if err != nil {
					t.Fatal(err)
				}
				setConfiguredPublicationPair(t, db, scope, string(actorJSON), "sha256:"+strings.Repeat("a", 64), "2026-08-01 12:00:00 +0000 UTC")
			},
		},
		{
			name: "noncanonical actor JSON",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
				actorJSON, err := projectstate.CanonicalJSON(actor)
				if err != nil {
					t.Fatal(err)
				}
				setConfiguredPublicationPair(t, db, scope, strings.TrimSpace(string(actorJSON)), "sha256:"+strings.Repeat("a", 64), "2026-08-01 12:00:00 +0000 UTC")
			},
		},
		{
			name: "uppercase digest",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
				actorJSON, err := projectstate.CanonicalJSON(actor)
				if err != nil {
					t.Fatal(err)
				}
				setConfiguredPublicationPair(t, db, scope, string(actorJSON), "sha256:"+strings.Repeat("A", 64), "2026-08-01 12:00:00 +0000 UTC")
			},
		},
		{
			name: "non UTC changed timestamp",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
				actorJSON, err := projectstate.CanonicalJSON(actor)
				if err != nil {
					t.Fatal(err)
				}
				setConfiguredPublicationPair(t, db, scope, string(actorJSON), "sha256:"+strings.Repeat("a", 64), "2026-08-01 13:00:00 +0100 CET")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011",
				"/checkout", 1, 11)
			test.mutate(t, store.DB(), binding.Scope)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, currentErr := tx.PublicationPolicy(context.Background())
				if currentErr == nil {
					t.Fatal("PublicationPolicy accepted corrupt current row")
				}
				_, err := publicationPolicyHistoryForTest(tx, context.Background())
				return err
			})
			if err == nil {
				t.Fatal("publication history audit accepted corrupt row")
			}
		})
	}
}

func TestWorkspacePublicationPolicyRejectsHistoryGap(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	bootstrap, configured := configurePublicationPolicy(t, repo, binding, types.PublicationPublicGit)
	origin := publicationTestDigest('b')
	changedAt := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	invalidated := WorkspacePublicationPolicyRecord{
		Repository: binding.Repository, OriginDigest: &origin,
		Classification: types.PublicationUnclassified, PolicyRevision: 3,
		TransitionKind: "origin_invalidated", ChangedAt: &changedAt,
	}
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: configured, Next: invalidated})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`DELETE FROM workspace_publication_policy_history WHERE project_id=? AND workspace_id=? AND policy_revision=2`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		current, err := tx.PublicationPolicy(context.Background())
		if err == nil && !equalWorkspacePublicationPolicyRecords(current, invalidated) {
			t.Fatalf("current policy=%+v, want %+v", current, invalidated)
		}
		return err
	})
	if err != nil {
		t.Fatalf("current-only policy was poisoned by history gap after %+v -> %+v: %v", bootstrap, invalidated, err)
	}
	if err := repo.AuditWorkspaceHistory(context.Background(), binding.Scope); err == nil {
		t.Fatal("AuditWorkspaceHistory accepted publication history gap")
	}
}

func TestWorkspacePublicationPolicyRejectsImpossibleHistoryProgression(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, db *sql.DB, scope types.WorkspaceScope)
	}{
		{
			name: "configured revision one",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				actorJSON, err := projectstate.CanonicalJSON(publicationTestHuman("00000000-0000-4000-8000-000000000021"))
				if err != nil {
					t.Fatal(err)
				}
				setConfiguredPublicationPair(t, db, scope, string(actorJSON), "sha256:"+strings.Repeat("a", 64), "2026-08-01 12:00:00 +0000 UTC")
			},
		},
		{
			name: "later bootstrap",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				withIgnoredSQLiteChecks(t, db, func() {
					updatePublicationPair(t, db, scope, `policy_revision=2,origin_digest=NULL,classification='unclassified',transition_kind='bootstrap',changed_actor_json=NULL,changed_at=NULL`)
				})
			},
		},
		{
			name: "repository changed while configured",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				appendImpossibleConfiguredRevision(t, db, scope, types.RepositoryIdentity{Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other"}, publicationTestDigest('a'))
			},
		},
		{
			name: "origin changed while configured",
			mutate: func(t *testing.T, db *sql.DB, scope types.WorkspaceScope) {
				appendImpossibleConfiguredRevision(t, db, scope, types.RepositoryIdentity{}, publicationTestDigest('b'))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			if test.name == "repository changed while configured" || test.name == "origin changed while configured" {
				_, _ = configurePublicationPolicy(t, repo, binding, types.PublicationPublicGit)
			}
			test.mutate(t, store.DB(), binding.Scope)
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.PublicationPolicy(context.Background())
				return err
			})
			if err != nil {
				t.Fatalf("current-only PublicationPolicy was poisoned by impossible history: %v", err)
			}
			if err := repo.AuditWorkspaceHistory(context.Background(), binding.Scope); err == nil {
				t.Fatal("AuditWorkspaceHistory accepted impossible publication history progression")
			}
		})
	}
}

func TestWorkspacePublicationRepositoryInvalidationRepairsPolicyBinding(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	oldRepository := types.RepositoryIdentity{Provider: "github", ImmutableID: "old", CanonicalRemote: "https://github.com/acme/old"}
	oldJSON, err := json.Marshal(oldRepository)
	if err != nil {
		t.Fatal(err)
	}
	updatePublicationPair(t, store.DB(), binding.Scope, `repository_identity_json='`+string(oldJSON)+`'`)
	var expected WorkspacePublicationPolicyRecord
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		expected, err = tx.PublicationPolicy(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := publicationTestDigest('c')
	changedAt := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	next := WorkspacePublicationPolicyRecord{
		Repository: binding.Repository, OriginDigest: &origin, Classification: types.PublicationUnclassified,
		PolicyRevision: 2, TransitionKind: "repository_invalidated", ChangedAt: &changedAt,
	}
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: expected, Next: next})
		if err == nil && !equalWorkspacePublicationPolicyRecords(got, next) {
			t.Fatalf("repository invalidation=%+v, want %+v", got, next)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePublicationPolicyExplicitConfiguredUnclassifiedAndDeepOwnership(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	_, configured := configurePublicationPolicy(t, repo, binding, types.PublicationUnclassified)
	*configured.OriginDigest = publicationTestDigest('f')
	configured.ChangedBy.HumanPrincipalID = "00000000-0000-4000-8000-000000000099"
	*configured.ChangedAt = configured.ChangedAt.Add(time.Hour)
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		current, err := tx.PublicationPolicy(context.Background())
		if err != nil {
			return err
		}
		if current.Classification != types.PublicationUnclassified || current.OriginDigest == nil ||
			*current.OriginDigest != publicationTestDigest('a') || current.ChangedBy.HumanPrincipalID != "00000000-0000-4000-8000-000000000021" ||
			!current.ChangedAt.Equal(time.Date(2026, 8, 1, 12, 34, 56, 0, time.UTC)) {
			t.Fatalf("mutated caller alias changed stored publication=%+v", current)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePublicationPolicyConfiguresEveryClassification(t *testing.T) {
	for index, classification := range []types.PublicationClassification{
		types.PublicationUnclassified,
		types.PublicationLocalOnly,
		types.PublicationPublicGit,
		types.PublicationPrivateGit,
	} {
		t.Run(string(classification), func(t *testing.T) {
			_, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011", "/checkout", uint64(index+1), uint64(index+11))
			_, configured := configurePublicationPolicy(t, repo, binding, classification)
			if configured.Classification != classification || configured.TransitionKind != "configured" ||
				configured.PolicyRevision != 2 || configured.OriginDigest == nil || configured.ChangedBy == nil || configured.ChangedAt == nil {
				t.Fatalf("configured %q policy=%+v", classification, configured)
			}
		})
	}
}

func TestWorkspacePublicationPolicyRejectsOverflowedPersistedLineageWithoutMutation(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if _, err := store.DB().Exec(`UPDATE workspace_publication_policies SET policy_revision=? WHERE project_id=? AND workspace_id=?`, int64(math.MaxInt64), binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE workspace_publication_policy_history SET policy_revision=? WHERE project_id=? AND workspace_id=?`, int64(math.MaxInt64), binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{})
		return err
	})
	if err == nil {
		t.Fatal("overflowed publication lineage was accepted")
	}
	var currentRevision, historyRevision int64
	if err := store.DB().QueryRow(`SELECT policy_revision FROM workspace_publication_policies WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&currentRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT policy_revision FROM workspace_publication_policy_history WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&historyRevision); err != nil {
		t.Fatal(err)
	}
	if currentRevision != math.MaxInt64 || historyRevision != math.MaxInt64 {
		t.Fatalf("overflow rejection mutated revisions=(%d,%d)", currentRevision, historyRevision)
	}
}

func TestWorkspacePublicationPolicyRejectsInvalidSemanticallyEqualExpected(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	_, configured := configurePublicationPolicy(t, repo, binding, types.PublicationPublicGit)
	expected := cloneWorkspacePublicationPolicyRecord(configured)
	cet := time.FixedZone("CET", 60*60)
	changedAt := expected.ChangedAt.In(cet)
	expected.ChangedAt = &changedAt
	next := publicationOriginInvalidation(binding.Repository, configured.PolicyRevision+1, 'b')
	before := readAtomicWorkspaceRawSnapshot(t, store.DB())

	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: expected, Next: next})
		return err
	})
	if err == nil {
		t.Fatal("non-UTC Expected authorized a publication transition")
	}
	if errors.Is(err, ErrPublicationConfigurationCAS) {
		t.Fatalf("invalid Expected error=%v, want validation error rather than stale CAS", err)
	}
	if after := readAtomicWorkspaceRawSnapshot(t, store.DB()); !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid Expected mutated workspace state: before=%v after=%v", before, after)
	}
}

func TestWorkspacePublicationPolicyTreatsZeroOffsetActorTimesSemantically(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	var bootstrap WorkspacePublicationPolicyRecord
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		bootstrap, err = tx.PublicationPolicy(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := publicationTestDigest('a')
	actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
	actor.OccurredAt = actor.OccurredAt.In(time.FixedZone("GMT", 0))
	changedAt := time.Date(2026, 8, 1, 12, 34, 56, 0, time.UTC)
	next := WorkspacePublicationPolicyRecord{
		Repository: binding.Repository, OriginDigest: &origin, Classification: types.PublicationPublicGit,
		PolicyRevision: 2, TransitionKind: "configured", ChangedBy: &actor, ChangedAt: &changedAt,
	}
	var configured WorkspacePublicationPolicyRecord
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		configured, err = tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: bootstrap, Next: next})
		return err
	})
	if err != nil {
		t.Fatalf("configure with zero-offset fixed-zone actor: %v", err)
	}
	expected := cloneWorkspacePublicationPolicyRecord(configured)
	expected.ChangedBy.OccurredAt = expected.ChangedBy.OccurredAt.In(time.FixedZone("GMT", 0))
	invalidated := publicationOriginInvalidation(binding.Repository, configured.PolicyRevision+1, 'b')
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: expected, Next: invalidated})
		return err
	})
	if err != nil {
		t.Fatalf("semantically equal zero-offset Expected actor: %v", err)
	}
}

func TestWorkspacePublicationPolicyTransitionsAreScopeIsolatedAndRestartSafe(t *testing.T) {
	databasePath := t.TempDir() + "/gateway.db"
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	target := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/one", 1, 11)
	neighbor := createBinding(t, repo, target.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/two", 1, 12)
	other := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000011", "/three", 2, 11)
	_, configured := configurePublicationPolicy(t, repo, target, types.PublicationPrivateGit)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	for _, fixture := range []struct {
		binding types.WorkspaceBinding
		want    WorkspacePublicationPolicyRecord
	}{
		{target, configured},
		{neighbor, WorkspacePublicationPolicyRecord{Repository: neighbor.Repository, Classification: types.PublicationUnclassified, PolicyRevision: 1, TransitionKind: "bootstrap"}},
		{other, WorkspacePublicationPolicyRecord{Repository: other.Repository, Classification: types.PublicationUnclassified, PolicyRevision: 1, TransitionKind: "bootstrap"}},
	} {
		err := repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.PublicationPolicy(context.Background())
			if err == nil && !equalWorkspacePublicationPolicyRecords(got, fixture.want) {
				t.Fatalf("scope %v publication=%+v, want %+v", fixture.binding.Scope, got, fixture.want)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspacePublicationPolicyRejectsInvalidNextWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WorkspacePublicationPolicyRecord)
	}{
		{"revision", func(next *WorkspacePublicationPolicyRecord) { next.PolicyRevision = 1 }},
		{"digest", func(next *WorkspacePublicationPolicyRecord) {
			value := projectstate.Digest("sha256:" + strings.Repeat("A", 64))
			next.OriginDigest = &value
		}},
		{"kind", func(next *WorkspacePublicationPolicyRecord) { next.TransitionKind = "future" }},
		{"missing actor", func(next *WorkspacePublicationPolicyRecord) { next.ChangedBy = nil }},
		{"agent actor", func(next *WorkspacePublicationPolicyRecord) {
			next.ChangedBy = &types.ActorEnvelope{
				ActorKind: types.ActorAgent, AgentID: "00000000-0000-4000-8000-000000000031",
				AccountableHumanID: "00000000-0000-4000-8000-000000000021", SessionID: "s",
				HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssuranceLocal,
				OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			var bootstrap WorkspacePublicationPolicyRecord
			err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				var err error
				bootstrap, err = tx.PublicationPolicy(context.Background())
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			origin := publicationTestDigest('a')
			actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
			changedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
			next := WorkspacePublicationPolicyRecord{Repository: binding.Repository, OriginDigest: &origin, Classification: types.PublicationUnclassified, PolicyRevision: 2, TransitionKind: "configured", ChangedBy: &actor, ChangedAt: &changedAt}
			test.mutate(&next)
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				_, err := tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{Expected: bootstrap, Next: next})
				return err
			})
			if err == nil {
				t.Fatal("invalid publication transition succeeded")
			}
			var currentRevision, historyCount int
			if err := store.DB().QueryRow(`SELECT policy_revision FROM workspace_publication_policies WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&currentRevision); err != nil {
				t.Fatal(err)
			}
			if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_publication_policy_history WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&historyCount); err != nil {
				t.Fatal(err)
			}
			if currentRevision != 1 || historyCount != 1 {
				t.Fatalf("invalid transition mutated current/history=(%d,%d)", currentRevision, historyCount)
			}
		})
	}
}

func configurePublicationPolicy(t *testing.T, repo *WorkspaceRepo, binding types.WorkspaceBinding, classification types.PublicationClassification) (WorkspacePublicationPolicyRecord, WorkspacePublicationPolicyRecord) {
	t.Helper()
	var bootstrap, configured WorkspacePublicationPolicyRecord
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		bootstrap, err = tx.PublicationPolicy(context.Background())
		if err != nil {
			return err
		}
		origin := publicationTestDigest('a')
		actor := publicationTestHuman("00000000-0000-4000-8000-000000000021")
		changedAt := time.Date(2026, 8, 1, 12, 34, 56, 0, time.UTC)
		configured, err = tx.ReconfigurePublication(context.Background(), WorkspacePublicationPolicyTransition{
			Expected: bootstrap,
			Next: WorkspacePublicationPolicyRecord{
				Repository: binding.Repository, OriginDigest: &origin, Classification: classification,
				PolicyRevision: 2, TransitionKind: "configured", ChangedBy: &actor, ChangedAt: &changedAt,
			},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return bootstrap, configured
}

func publicationOriginInvalidation(repository types.RepositoryIdentity, revision int64, digestByte byte) WorkspacePublicationPolicyRecord {
	origin := publicationTestDigest(digestByte)
	changedAt := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	return WorkspacePublicationPolicyRecord{
		Repository: repository, OriginDigest: &origin, Classification: types.PublicationUnclassified,
		PolicyRevision: revision, TransitionKind: "origin_invalidated", ChangedAt: &changedAt,
	}
}

func updatePublicationPair(t *testing.T, db *sql.DB, scope types.WorkspaceScope, assignment string) {
	t.Helper()
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
		if _, err := db.Exec(`UPDATE `+table+` SET `+assignment+` WHERE project_id=? AND workspace_id=?`, scope.ProjectID, scope.WorkspaceID); err != nil {
			t.Fatalf("corrupt %s: %v", table, err)
		}
	}
}

func setConfiguredPublicationPair(t *testing.T, db *sql.DB, scope types.WorkspaceScope, actorJSON, digest, changedAt string) {
	t.Helper()
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
		if _, err := db.Exec(`UPDATE `+table+` SET origin_digest=?,classification='public_git',transition_kind='configured',changed_actor_json=?,changed_at=? WHERE project_id=? AND workspace_id=?`, digest, actorJSON, changedAt, scope.ProjectID, scope.WorkspaceID); err != nil {
			t.Fatal(err)
		}
	}
}

func withIgnoredSQLiteChecks(t *testing.T, db *sql.DB, fn func()) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	fn()
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
}

func appendImpossibleConfiguredRevision(t *testing.T, db *sql.DB, scope types.WorkspaceScope, repository types.RepositoryIdentity, origin projectstate.Digest) {
	t.Helper()
	repositoryJSON, err := json.Marshal(repository)
	if err != nil {
		t.Fatal(err)
	}
	actorJSON, err := projectstate.CanonicalJSON(publicationTestHuman("00000000-0000-4000-8000-000000000021"))
	if err != nil {
		t.Fatal(err)
	}
	changedAt := "2026-08-01 13:00:00 +0000 UTC"
	if _, err := db.Exec(`
		INSERT INTO workspace_publication_policy_history
		(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
		 classification,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,3,?,?,'private_git','configured',?,?)
	`, scope.ProjectID, scope.WorkspaceID, string(repositoryJSON), string(origin), string(actorJSON), changedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE workspace_publication_policies SET policy_revision=3,repository_identity_json=?,
		 origin_digest=?,classification='private_git',transition_kind='configured',changed_actor_json=?,changed_at=?
		WHERE project_id=? AND workspace_id=?
	`, string(repositoryJSON), string(origin), string(actorJSON), changedAt, scope.ProjectID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func publicationTestDigest(char byte) projectstate.Digest {
	return projectstate.Digest("sha256:" + strings.Repeat(string(char), 64))
}

func publicationTestHuman(id string) types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: id,
		Assurance:  types.AssuranceLocal,
		OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
}
