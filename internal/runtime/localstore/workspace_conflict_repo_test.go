package localstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceConflictOccurrencesNewReuseResolveAndReopen(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	evidence := WorkspaceConflictEvidence{
		ConflictID:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Key:          state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"},
		FieldPath:    "/title",
		ConflictKind: "same_field",
		BaseJSON:     " {\n  \"present\": true, \"value\": \"base\"\n}",
		OursJSON:     "{\"present\":true,\"value\":\"ours\"}",
		TheirsJSON:   "{\"present\":true,\"value\":\"theirs\"}\n",
	}

	var first WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if len(got) != 1 {
			t.Fatalf("new occurrences=%+v, want one", got)
		}
		first = got[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceConflictEvidence != evidence {
		t.Fatalf("new evidence=%+v, want byte-exact %+v", first.WorkspaceConflictEvidence, evidence)
	}
	if !validCanonicalUUIDv4(first.OccurrenceID) || !validUTCTimestamp(first.CreatedAt) {
		t.Fatalf("new occurrence metadata=%+v", first)
	}

	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 1, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0] != first {
			t.Fatalf("reused occurrences=%+v, want %+v", got, first)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	resolvedAt := time.Date(2026, 7, 29, 12, 2, 3, 0, time.UTC)
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), nil, resolvedAt)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			t.Fatalf("resolved open occurrences=%+v, want none", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var reopened WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 3, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if len(got) != 1 {
			t.Fatalf("reopened occurrences=%+v, want one", got)
		}
		reopened = got[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reopened.OccurrenceID == first.OccurrenceID || !validUTCTimestamp(reopened.CreatedAt) {
		t.Fatalf("reopened occurrence=%+v, first=%+v", reopened, first)
	}
}

func TestWorkspaceConflictOccurrencesRejectInvalidReceiverScopeAndInput(t *testing.T) {
	ctx := context.Background()
	resolvedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	valid := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})

	var nilTx *WorkspaceMutationTx
	if got, err := nilTx.OpenConflictOccurrences(ctx); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil OpenConflictOccurrences=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	if got, err := nilTx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{valid}, resolvedAt); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("nil ReplaceOpenConflictOccurrences=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}
	if got, err := (&WorkspaceMutationTx{}).OpenConflictOccurrences(ctx); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("empty OpenConflictOccurrences=(%+v,%v), want (nil,ErrNotFound)", got, err)
	}

	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	invalid := []struct {
		name string
		edit func(*WorkspaceConflictEvidence)
	}{
		{name: "digest prefix", edit: func(e *WorkspaceConflictEvidence) { e.ConflictID = strings.Repeat("a", 64) }},
		{name: "digest uppercase", edit: func(e *WorkspaceConflictEvidence) { e.ConflictID = "sha256:" + strings.Repeat("A", 64) }},
		{name: "record kind", edit: func(e *WorkspaceConflictEvidence) { e.Key.Kind = "unknown" }},
		{name: "record ID", edit: func(e *WorkspaceConflictEvidence) { e.Key.ID = "BAD" }},
		{name: "foreign project record", edit: func(e *WorkspaceConflictEvidence) {
			e.Key = state.RecordKey{Kind: "project", ID: "00000000-0000-4000-8000-000000000002"}
		}},
		{name: "relative field path", edit: func(e *WorkspaceConflictEvidence) { e.FieldPath = "title" }},
		{name: "malformed field escape", edit: func(e *WorkspaceConflictEvidence) { e.FieldPath = "/a~2b" }},
		{name: "control field path", edit: func(e *WorkspaceConflictEvidence) { e.FieldPath = "/a\nb" }},
		{name: "empty conflict kind", edit: func(e *WorkspaceConflictEvidence) { e.ConflictKind = "" }},
		{name: "unsafe conflict kind", edit: func(e *WorkspaceConflictEvidence) { e.ConflictKind = "Same-Field" }},
		{name: "empty base evidence", edit: func(e *WorkspaceConflictEvidence) { e.BaseJSON = "" }},
		{name: "invalid UTF-8 evidence", edit: func(e *WorkspaceConflictEvidence) { e.OursJSON = string([]byte{0xff}) }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			evidence := valid
			test.edit(&evidence)
			err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{evidence}, resolvedAt)
				if err == nil || got != nil || errors.Is(err, ErrNotFound) {
					t.Fatalf("invalid replacement=(%+v,%v), want content error", got, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, badTime := range []time.Time{{}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.FixedZone("offset", 60))} {
		if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{valid}, badTime)
			if err == nil || got != nil {
				t.Fatalf("invalid timestamp replacement=(%+v,%v)", got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.WithImmediateWorkspace(ctx, binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(ctx, []WorkspaceConflictEvidence{valid, valid}, resolvedAt)
		if err == nil || got != nil {
			t.Fatalf("duplicate replacement=(%+v,%v)", got, err)
		}
		originalScope := tx.scope
		tx.scope = types.WorkspaceScope{ProjectID: "BAD", WorkspaceID: originalScope.WorkspaceID}
		defer func() { tx.scope = originalScope }()
		if got, err := tx.OpenConflictOccurrences(ctx); !errors.Is(err, ErrNotFound) || got != nil {
			t.Fatalf("invalid-scope open=(%+v,%v), want ErrNotFound", got, err)
		}
		if got, err := tx.ReplaceOpenConflictOccurrences(ctx, nil, resolvedAt); !errors.Is(err, ErrNotFound) || got != nil {
			t.Fatalf("invalid-scope replace=(%+v,%v), want ErrNotFound", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_conflicts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid calls left conflict count=%d err=%v", count, err)
	}
}

func TestWorkspaceConflictOccurrencesAcceptRootPointerAndNeutralKind(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "project", ID: binding.Scope.ProjectID})
	evidence.FieldPath = ""
	evidence.ConflictKind = "future_conflict_2"
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].WorkspaceConflictEvidence != evidence {
			t.Fatalf("root/neutral occurrence=%+v, want %+v", got, evidence)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceConflictOccurrencesRejectChangedOpenEvidenceWithoutMutation(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011",
		"/checkout", 1, 11,
	)
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
	resolvedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	var original WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, resolvedAt)
		if err == nil {
			original = got[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	changed := evidence
	changed.OursJSON += " "
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{changed}, resolvedAt.Add(time.Second))
		return err
	}); err == nil {
		t.Fatal("same open conflict ID with changed evidence succeeded")
	}
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0] != original {
			t.Fatalf("open evidence after mismatch=%+v, want %+v", got, original)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceConflictOccurrencesExactScopeIsolation(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	a := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout-a", 1, 11)
	b := createBinding(t, repo, a.Scope.ProjectID, "00000000-0000-4000-8000-000000000012", "/checkout-b", 2, 12)
	c := createBinding(t, repo, "00000000-0000-4000-8000-000000000002", string(a.Scope.WorkspaceID), "/checkout-c", 3, 13)
	key := state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"}
	fixtures := []struct {
		scope    types.WorkspaceScope
		evidence WorkspaceConflictEvidence
	}{
		{scope: a.Scope, evidence: workspaceConflictEvidence(1, key)},
		{scope: b.Scope, evidence: workspaceConflictEvidence(1, key)},
		{scope: c.Scope, evidence: workspaceConflictEvidence(1, key)},
	}
	for index := range fixtures {
		fixtures[index].evidence.BaseJSON = fmt.Sprintf("{\"present\":true,\"value\":\"base-%d\"}\n", index)
		fixture := fixtures[index]
		if err := repo.WithImmediateWorkspace(context.Background(), fixture.scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{fixture.evidence}, time.Date(2026, 7, 29, 12, index, 0, 0, time.UTC))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range fixtures {
		if err := repo.WithImmediateWorkspace(context.Background(), fixture.scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.OpenConflictOccurrences(context.Background())
			if err != nil {
				return err
			}
			if len(got) != 1 || got[0].WorkspaceConflictEvidence != fixture.evidence {
				t.Fatalf("scope %+v open=%+v, want %+v", fixture.scope, got, fixture.evidence)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.WithImmediateWorkspace(context.Background(), a.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), nil, time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures[1:] {
		if err := repo.WithImmediateWorkspace(context.Background(), fixture.scope, func(tx *WorkspaceMutationTx) error {
			got, err := tx.OpenConflictOccurrences(context.Background())
			if err != nil || len(got) != 1 || got[0].WorkspaceConflictEvidence != fixture.evidence {
				t.Fatalf("neighbor scope after A resolve=(%+v,%v)", got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspaceConflictOccurrencesCanonicalOrderAndNonAliasing(t *testing.T) {
	_, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	recordID1 := "00000000-0000-4000-8000-000000000021"
	recordID2 := "00000000-0000-4000-8000-000000000022"
	desired := []WorkspaceConflictEvidence{
		workspaceConflictEvidence(8, state.RecordKey{Kind: "git_link", ID: recordID1}),
		workspaceConflictEvidence(7, state.RecordKey{Kind: "event", ID: recordID1}),
		workspaceConflictEvidence(6, state.RecordKey{Kind: "channel", ID: recordID1}),
		workspaceConflictEvidence(5, state.RecordKey{Kind: "kb_article", ID: recordID1}),
		workspaceConflictEvidence(4, state.RecordKey{Kind: "task_link", ID: recordID1}),
		workspaceConflictEvidence(3, state.RecordKey{Kind: "task", ID: recordID2}),
		workspaceConflictEvidence(2, state.RecordKey{Kind: "task", ID: recordID1}),
		workspaceConflictEvidence(1, state.RecordKey{Kind: "actor", ID: recordID1}),
		workspaceConflictEvidence(9, state.RecordKey{Kind: "project", ID: binding.Scope.ProjectID}),
	}
	desired[5].FieldPath = "/z"
	desired[6].FieldPath = "/a"
	var got []WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		got, err = tx.ReplaceOpenConflictOccurrences(context.Background(), desired, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{desired[8].ConflictID, desired[7].ConflictID, desired[6].ConflictID, desired[5].ConflictID, desired[4].ConflictID, desired[3].ConflictID, desired[2].ConflictID, desired[1].ConflictID, desired[0].ConflictID}
	gotIDs := make([]string, len(got))
	for index := range got {
		gotIDs[index] = got[index].ConflictID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("canonical occurrence order=%v, want %v", gotIDs, wantIDs)
	}
	wantFirst := got[0]
	desired[8].BaseJSON = "mutated input"
	got[0].BaseJSON = "mutated output"
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		again, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil {
			return err
		}
		if len(again) != len(wantIDs) || again[0] != wantFirst {
			t.Fatalf("persisted occurrences aliased caller data: %+v", again)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceConflictOccurrencesHistoryAndRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
	var first WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
		if err == nil {
			first = got[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo = NewWorkspaceRepo(store.DB())
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil || len(got) != 1 || got[0] != first {
			t.Fatalf("restart open occurrences=(%+v,%v), want %+v", got, err, first)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resolvedAt := time.Date(2026, 7, 29, 12, 2, 3, 456000000, time.FixedZone("zero-offset", 0))
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), nil, resolvedAt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var stateValue string
	var storedResolvedAt time.Time
	var storedBase, storedOurs, storedTheirs string
	var storedCreatedAt time.Time
	if err := store.DB().QueryRow(`
		SELECT state,resolved_at,base_json,ours_json,theirs_json,created_at
		FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND occurrence_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, first.OccurrenceID).Scan(
		&stateValue, &storedResolvedAt, &storedBase, &storedOurs, &storedTheirs, &storedCreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if stateValue != "resolved" || !storedResolvedAt.Equal(resolvedAt) || !validUTCTimestamp(storedResolvedAt) || storedResolvedAt.Location() != time.UTC ||
		storedBase != evidence.BaseJSON || storedOurs != evidence.OursJSON || storedTheirs != evidence.TheirsJSON ||
		!storedCreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("resolved history=(%q,%v,%q,%q,%q,%v), want exact evidence/time %+v", stateValue, storedResolvedAt, storedBase, storedOurs, storedTheirs, storedCreatedAt, first)
	}
	var reopened WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, resolvedAt.Add(time.Second))
		if err == nil {
			reopened = got[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var historyCount int
	if err := store.DB().QueryRow(`
		SELECT count(*) FROM workspace_conflicts WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 2 || reopened.OccurrenceID == first.OccurrenceID {
		t.Fatalf("history count=%d reopened=%+v first=%+v", historyCount, reopened, first)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil || len(got) != 1 || got[0] != reopened {
			t.Fatalf("reopened restart occurrences=(%+v,%v), want %+v", got, err, reopened)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceConflictOccurrencesAcceptMigratedV1OccurrenceAfterRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
	createdAt := time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC)
	insertRawWorkspaceConflictOccurrence(t, store, binding.Scope, evidence.ConflictID, evidence, "open", createdAt, nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = NewWorkspaceRepo(store.DB())
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil {
			return err
		}
		want := WorkspaceConflictOccurrence{WorkspaceConflictEvidence: evidence, OccurrenceID: evidence.ConflictID, CreatedAt: createdAt}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("migrated occurrence=%+v, want %+v", got, want)
		}
		reused, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
		if err != nil || len(reused) != 1 || reused[0] != want {
			t.Fatalf("reused migrated occurrence=(%+v,%v), want %+v", reused, err, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceConflictOccurrencesCorruptSelectedRowsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "non-v4 occurrence", column: "occurrence_id", value: "00000000-0000-1000-8000-000000000031"},
		{name: "uppercase conflict digest", column: "conflict_id", value: "sha256:" + strings.Repeat("A", 64)},
		{name: "unknown record kind", column: "record_kind", value: "unknown"},
		{name: "invalid record ID", column: "record_id", value: "BAD"},
		{name: "relative field path", column: "field_path", value: "title"},
		{name: "malformed field escape", column: "field_path", value: "/a~"},
		{name: "unsafe conflict kind", column: "conflict_kind", value: "same-field"},
		{name: "empty base evidence", column: "base_json", value: ""},
		{name: "invalid UTF-8 evidence", column: "ours_json", value: []byte{0xff}},
		{name: "invalid creation time", column: "created_at", value: "not-a-time"},
		{name: "zero creation time", column: "created_at", value: time.Time{}},
		{name: "offset creation time", column: "created_at", value: time.Date(2026, 7, 29, 12, 0, 0, 0, time.FixedZone("offset", 60))},
		{name: "open row resolved timestamp", column: "resolved_at", value: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, err := Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewWorkspaceRepo(store.DB())
			binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
			occurrenceID := "00000000-0000-4000-8000-000000000031"
			insertRawWorkspaceConflictOccurrence(t, store, binding.Scope, occurrenceID, evidence, "open", time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC), nil)
			query := fmt.Sprintf("UPDATE workspace_conflicts SET %s=? WHERE project_id=? AND workspace_id=?", test.column)
			if _, err := store.DB().Exec(query, test.value, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			repo = NewWorkspaceRepo(store.DB())
			err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				got, err := tx.OpenConflictOccurrences(context.Background())
				if err == nil || got != nil {
					t.Fatalf("corrupt open occurrences=(%+v,%v), want fail closed", got, err)
				}
				got, replaceErr := tx.ReplaceOpenConflictOccurrences(context.Background(), nil, time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC))
				if replaceErr == nil || got != nil {
					t.Fatalf("corrupt replacement=(%+v,%v), want fail closed", got, replaceErr)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceConflictOccurrencesDuplicatePersistedSemanticIDFailsClosed(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
	if _, err := store.DB().Exec(`DROP INDEX workspace_one_open_semantic_conflict`); err != nil {
		t.Fatal(err)
	}
	insertRawWorkspaceConflictOccurrence(t, store, binding.Scope, "00000000-0000-4000-8000-000000000031", evidence, "open", time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC), nil)
	insertRawWorkspaceConflictOccurrence(t, store, binding.Scope, "00000000-0000-4000-8000-000000000032", evidence, "open", time.Date(2026, 7, 29, 11, 0, 1, 0, time.UTC), nil)
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.OpenConflictOccurrences(context.Background())
		return err
	})
	if err == nil {
		t.Fatal("duplicate persisted semantic conflict IDs were served")
	}
}

func TestWorkspaceConflictOccurrencesStatementFailureRollsBackAtomically(t *testing.T) {
	t.Run("resolve then insert abort", func(t *testing.T) {
		databasePath, store, repo, binding := openConflictFaultStore(t)
		oldEvidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
		newEvidence := workspaceConflictEvidence(2, state.RecordKey{Kind: "channel", ID: "00000000-0000-4000-8000-000000000022"})
		original := replaceOneConflict(t, repo, binding.Scope, oldEvidence)
		if _, err := store.DB().Exec(`
			CREATE TRIGGER abort_second_conflict_insert
			BEFORE INSERT ON workspace_conflicts
			WHEN NEW.conflict_id='sha256:0000000000000000000000000000000000000000000000000000000000000002'
			BEGIN SELECT RAISE(ABORT, 'injected conflict insert fault'); END
		`); err != nil {
			t.Fatal(err)
		}
		err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
			_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{newEvidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
			return err
		})
		if err == nil {
			t.Fatal("replacement hid later insert trigger failure")
		}
		assertOpenConflictAfterReopen(t, databasePath, store, binding.Scope, original)
	})
}

func TestWorkspaceConflictOccurrencesQueryFailureAndCallbackRollback(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	closedTx := &WorkspaceMutationTx{conn: conn, scope: binding.Scope}
	if got, err := closedTx.OpenConflictOccurrences(context.Background()); err == nil || got != nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("closed-connection open=(%+v,%v), want query error", got, err)
	}
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
	if got, err := closedTx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)); err == nil || got != nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("closed-connection replace=(%+v,%v), want query error", got, err)
	}

	callbackErr := errors.New("conflict callback rollback fixture")
	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if _, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("callback rollback error=%v, want fixture without commit sentinel", err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM workspace_conflicts WHERE project_id=? AND workspace_id=?`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("callback rollback conflict count=%d err=%v", count, err)
	}

	err = repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if err := tx.SetStatus(context.Background(), "pending"); err != nil {
			return err
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := tx.OpenConflictOccurrences(canceled)
		return err
	})
	if err == nil {
		t.Fatal("canceled conflict query succeeded")
	}
	record, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil || record.State != "clean" {
		t.Fatalf("query-failure rollback workspace=(%+v,%v), want clean", record, err)
	}
}

func TestWorkspaceConflictOccurrencesCommitFailureRollsBackWithSentinel(t *testing.T) {
	databasePath, store, repo, binding := openConflictFaultStore(t)
	evidence := workspaceConflictEvidence(1, state.RecordKey{Kind: "task", ID: "00000000-0000-4000-8000-000000000021"})
	err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		if _, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		if _, err := tx.conn.ExecContext(context.Background(), `PRAGMA defer_foreign_keys=ON`); err != nil {
			return err
		}
		_, err := tx.conn.ExecContext(context.Background(), `
			INSERT INTO workspace_conflicts
			(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
			 conflict_kind,base_json,ours_json,theirs_json,state)
			VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098',
			 '00000000-0000-4000-8000-000000000097',
			 'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
			 'task','00000000-0000-4000-8000-000000000096','/title',
			 'same_field','{}','{}','{}','open')
		`)
		return err
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("commit failure error=%v, want ErrCommitOutcomeUnknown", err)
	}
	assertConflictHistoryCountAfterReopen(t, databasePath, store, binding.Scope, 0)
}

func openConflictFaultStore(t *testing.T) (string, *Store, *WorkspaceRepo, types.WorkspaceBinding) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkspaceRepo(store.DB())
	binding := createBinding(t, repo, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	return databasePath, store, repo, binding
}

func replaceOneConflict(t *testing.T, repo *WorkspaceRepo, scope types.WorkspaceScope, evidence WorkspaceConflictEvidence) WorkspaceConflictOccurrence {
	t.Helper()
	var occurrence WorkspaceConflictOccurrence
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC))
		if err == nil {
			occurrence = got[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return occurrence
}

func assertOpenConflictAfterReopen(t *testing.T, databasePath string, store *Store, scope types.WorkspaceScope, want WorkspaceConflictOccurrence) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	repo := NewWorkspaceRepo(reopened.DB())
	if err := repo.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		got, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil || len(got) != 1 || got[0] != want {
			t.Fatalf("reopened open occurrences=(%+v,%v), want %+v", got, err, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertConflictHistoryCountAfterReopen(t *testing.T, databasePath string, store *Store, scope types.WorkspaceScope, want int) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var got int
	if err := reopened.DB().QueryRow(`
		SELECT count(*) FROM workspace_conflicts WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&got); err != nil || got != want {
		t.Fatalf("reopened conflict history count=%d err=%v, want %d", got, err, want)
	}
}

func insertRawWorkspaceConflictOccurrence(t *testing.T, store *Store, scope types.WorkspaceScope, occurrenceID string, evidence WorkspaceConflictEvidence, conflictState string, createdAt time.Time, resolvedAt *time.Time) {
	t.Helper()
	var resolved any
	if resolvedAt != nil {
		resolved = *resolvedAt
	}
	result, err := store.DB().Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state,created_at,resolved_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, scope.ProjectID, scope.WorkspaceID, occurrenceID, evidence.ConflictID,
		evidence.Key.Kind, evidence.Key.ID, evidence.FieldPath, evidence.ConflictKind,
		evidence.BaseJSON, evidence.OursJSON, evidence.TheirsJSON, conflictState, createdAt, resolved)
	if err != nil {
		t.Fatalf("insert raw workspace conflict occurrence: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("raw conflict insert affected=%d err=%v", affected, err)
	}
}

func workspaceConflictEvidence(index int, key state.RecordKey) WorkspaceConflictEvidence {
	return WorkspaceConflictEvidence{
		ConflictID:   fmt.Sprintf("sha256:%064x", index),
		Key:          key,
		FieldPath:    "/field~0name/child~1name",
		ConflictKind: "same_field",
		BaseJSON:     " {\"present\":true,\"value\":\"base\"}\n",
		OursJSON:     "{\"present\":true,\"value\":\"ours\"}",
		TheirsJSON:   "{\"present\":true,\"value\":\"theirs\"} ",
	}
}
