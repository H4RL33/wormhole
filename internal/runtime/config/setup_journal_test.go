package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	testSetupWorkspaceID = types.WorkspaceID("20000000-0000-4000-8000-000000000001")
	testSetupHumanID     = "30000000-0000-4000-8000-000000000001"
)

func TestStateDigestVectorsAndStrictParsing(t *testing.T) {
	wantEmpty := StateDigest("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if got := SHA256StateDigest(nil); got != wantEmpty {
		t.Fatalf("SHA256StateDigest(nil) = %q, want %q", got, wantEmpty)
	}
	wantABC := StateDigest("sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if got := SHA256StateDigest([]byte("abc")); got != wantABC {
		t.Fatalf("SHA256StateDigest(abc) = %q, want %q", got, wantABC)
	}
	if got, err := ParseStateDigest(string(wantABC)); err != nil || got != wantABC {
		t.Fatalf("ParseStateDigest(valid) = %q, %v", got, err)
	}
	for _, value := range []string{"", "absent", "sha256:", "SHA256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 63), "sha256:" + strings.Repeat("g", 64)} {
		if _, err := ParseStateDigest(value); !errors.Is(err, ErrInvalidStateDigest) {
			t.Errorf("ParseStateDigest(%q) error = %v, want ErrInvalidStateDigest", value, err)
		}
	}
}

func TestSetupJournalStagesAreExactOrderedAndUnique(t *testing.T) {
	want := []SetupStage{
		StageProjectValidated,
		StageGatewayReady,
		StageWorkspaceRegistered,
		StageIdentitySelected,
		StagePublicationClassified,
		StageBaseImported,
		StageConnectorsApplied,
		StageFinalVerified,
	}
	wantText := []SetupStage{"project_validated", "gateway_ready", "workspace_registered", "identity_selected", "publication_classified", "base_imported", "connectors_applied", "final_verified"}
	if !reflect.DeepEqual(orderedSetupStages, want) || !reflect.DeepEqual(want, wantText) {
		t.Fatalf("orderedSetupStages = %#v, want %#v", orderedSetupStages, wantText)
	}
	seen := map[SetupStage]bool{}
	for _, stage := range orderedSetupStages {
		if seen[stage] || !validSetupStage(stage) {
			t.Fatalf("invalid or duplicate setup stage %q", stage)
		}
		seen[stage] = true
	}
	for _, forbidden := range []SetupStage{"fabric_selected", "fabric_attached", "code_graph_selected", "code_graph_built"} {
		if validSetupStage(forbidden) {
			t.Fatalf("out-of-scope stage %q accepted", forbidden)
		}
	}
}

func TestConfirmedPlanRequiresOrderedUniqueChanges(t *testing.T) {
	valid := testSetupSelection()
	if err := validateSetupSelection(valid); err != nil {
		t.Fatalf("valid selection: %v", err)
	}
	mutations := []func(*SetupSelection){
		func(value *SetupSelection) {
			value.Changes = append(value.Changes[:1], append([]ConfirmedChange{value.Changes[0]}, value.Changes[1:]...)...)
		},
		func(value *SetupSelection) { value.Changes[0], value.Changes[1] = value.Changes[1], value.Changes[0] },
		func(value *SetupSelection) { value.Changes[0].DesiredDigest = value.Changes[0].PriorDigest },
		func(value *SetupSelection) { value.ConnectorAdapters = []string{"codex", "codex"} },
		func(value *SetupSelection) { value.ConnectorAdapters = []string{"fabric"} },
		func(value *SetupSelection) { value.Changes[0].Subject = "/home/alice/.config/private" },
		func(value *SetupSelection) { value.Changes = nil },
	}
	for index, mutate := range mutations {
		candidate := cloneSetupSelection(valid)
		mutate(&candidate)
		if err := validateSetupSelection(candidate); !errors.Is(err, ErrInvalidConfirmedPlan) {
			t.Errorf("mutation %d error = %v, want ErrInvalidConfirmedPlan", index, err)
		}
	}
}

func TestConfirmedPlanAcceptsExplicitUnclassifiedPublication(t *testing.T) {
	selection := testSetupSelection()
	selection.PublicationVisibility = string(types.PublicationUnclassified)
	if err := validateSetupSelection(selection); err != nil {
		t.Fatalf("explicit unclassified publication: %v", err)
	}
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, selection); err != nil {
		t.Fatalf("persist explicit unclassified publication: %v", err)
	}
	got, resumable, err := store.Resumable(context.Background(), projectRoot)
	if err != nil || !resumable || got.Selection == nil || got.Selection.PublicationVisibility != string(types.PublicationUnclassified) {
		t.Fatalf("resumed explicit unclassified publication = %+v, %v, %v", got, resumable, err)
	}
}

func TestConfirmedPlanRejectsUnsafeVocabularyWithoutPersistence(t *testing.T) {
	for _, mutation := range []func(*SetupSelection){
		func(value *SetupSelection) { value.Changes[0].Subject = "bearer:top-secret-token" },
		func(value *SetupSelection) { value.Changes[0].Subject = "config.toml" },
		func(value *SetupSelection) { value.Changes[0].Action = "aws_secret_access_key:value" },
		func(value *SetupSelection) { value.Changes[0].Action = "credential.config" },
		func(value *SetupSelection) { value.Changes[0].Action = "ensure" },
	} {
		candidate := testSetupSelection()
		mutation(&candidate)
		if err := validateSetupSelection(candidate); !errors.Is(err, ErrInvalidConfirmedPlan) {
			t.Errorf("unsafe vocabulary error = %v, want ErrInvalidConfirmedPlan", err)
		}
	}

	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	selection := testSetupSelection()
	selection.Changes[0].Action = "aws_secret_access_key:top-secret-token"
	before := snapshotSetupJournalTree(t, store.root)
	if err := store.SetSelection(context.Background(), journal.JournalID, selection); !errors.Is(err, ErrInvalidConfirmedPlan) {
		t.Errorf("SetSelection(secret-shaped action) error = %v, want ErrInvalidConfirmedPlan", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
	for _, data := range snapshotSetupJournalTree(t, store.root) {
		lower := strings.ToLower(string(data))
		for _, forbidden := range []string{"aws_secret", "top-secret", "credential.config", "config.toml", "bearer:"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("journal persisted forbidden confirmed-plan content %q: %s", forbidden, data)
			}
		}
	}
}

func TestConfirmedPlanAllowsOmittedNoOpsAndMultipleStageChanges(t *testing.T) {
	empty := testSetupSelection()
	empty.Changes = []ConfirmedChange{}
	if err := validateSetupSelection(empty); err != nil {
		t.Fatalf("empty no-op change list: %v", err)
	}

	multiple := testSetupSelection()
	firstConnector := multiple.Changes[6]
	firstConnector.Subject = "connector:codex"
	secondConnector := firstConnector
	secondConnector.Subject = "connector:claude"
	secondConnector.PriorDigest = SHA256StateDigest([]byte("claude-prior"))
	secondConnector.DesiredDigest = SHA256StateDigest([]byte("claude-desired"))
	multiple.Changes = append(append(append([]ConfirmedChange{}, multiple.Changes[:6]...), firstConnector, secondConnector), multiple.Changes[7])
	if err := validateSetupSelection(multiple); err != nil {
		t.Fatalf("multiple ordered connector changes: %v", err)
	}
}

func TestSetupJournalConfirmationIsNilThenImmutable(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Selection != nil || len(journal.CompletedStages) != 0 || journal.State != SetupJournalActive {
		t.Fatalf("new journal carries effects or confirmation: %+v", journal)
	}
	if err := store.MarkCompleted(context.Background(), journal.JournalID, StageProjectValidated); !errors.Is(err, ErrConfirmedPlanRequired) {
		t.Fatalf("MarkCompleted before confirmation error = %v, want ErrConfirmedPlanRequired", err)
	}

	selection := testSetupSelection()
	if err := store.SetSelection(context.Background(), journal.JournalID, selection); err != nil {
		t.Fatal(err)
	}
	selection.ConnectorAdapters[0] = "changed-by-caller"
	selection.Changes[0].Subject = "changed-by-caller"
	got, resumable, err := store.Resumable(context.Background(), projectRoot)
	if err != nil || !resumable || got.Selection == nil {
		t.Fatalf("Resumable = %+v, %v, %v", got, resumable, err)
	}
	if got.Selection.ConnectorAdapters[0] != "codex" || got.Selection.Changes[0].Subject != "project" {
		t.Fatal("stored selection aliases caller memory")
	}
	before := snapshotSetupJournalTree(t, store.root)
	different := testSetupSelection()
	different.PublicationVisibility = "private_git"
	if err := store.SetSelection(context.Background(), journal.JournalID, different); !errors.Is(err, ErrConfirmedPlanDrift) {
		t.Fatalf("changed selection error = %v, want ErrConfirmedPlanDrift", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestSetupJournalMonotonicLifecycleAndBindings(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	before := snapshotSetupJournalTree(t, store.root)
	if err := store.MarkCompleted(context.Background(), journal.JournalID, StageGatewayReady); !errors.Is(err, ErrConfirmedPlanDrift) {
		t.Fatalf("skipped stage error = %v, want ErrConfirmedPlanDrift", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)

	for _, stage := range orderedSetupStages {
		switch stage {
		case StageWorkspaceRegistered:
			if err := store.BindWorkspace(context.Background(), journal.JournalID, testSetupWorkspaceID); err != nil {
				t.Fatal(err)
			}
		case StageIdentitySelected:
			if err := store.BindIdentity(context.Background(), journal.JournalID, testSetupHumanID); err != nil {
				t.Fatal(err)
			}
		case StageConnectorsApplied:
			if err := store.RecordConnectorBackup(context.Background(), journal.JournalID, BackupReference("connector-backup:v1:codex:40000000-0000-4000-8000-000000000001")); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.RecordLastError(context.Background(), journal.JournalID, stage, errors.New("temporary failure")); err != nil {
			t.Fatalf("RecordLastError(%s): %v", stage, err)
		}
		if err := store.MarkCompleted(context.Background(), journal.JournalID, stage); err != nil {
			t.Fatalf("MarkCompleted(%s): %v", stage, err)
		}
		if err := store.MarkCompleted(context.Background(), journal.JournalID, stage); err != nil {
			t.Fatalf("idempotent MarkCompleted(%s): %v", stage, err)
		}
	}
	if err := store.Complete(context.Background(), journal.JournalID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), journal.JournalID); err != nil {
		t.Fatalf("idempotent Complete: %v", err)
	}
	got, resumable, err := store.Resumable(context.Background(), projectRoot)
	if err != nil || resumable || got.JournalID != "" {
		t.Fatalf("Resumable(completed) = %+v, %v, %v", got, resumable, err)
	}
}

func TestConfirmedPlanReferencesCannotRunAheadOfTheirStage(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	before := snapshotSetupJournalTree(t, store.root)
	for name, operation := range map[string]func() error{
		"workspace": func() error {
			return store.BindWorkspace(context.Background(), journal.JournalID, testSetupWorkspaceID)
		},
		"identity": func() error { return store.BindIdentity(context.Background(), journal.JournalID, testSetupHumanID) },
		"backup": func() error {
			return store.RecordConnectorBackup(context.Background(), journal.JournalID, BackupReference("connector-backup:v1:codex:40000000-0000-4000-8000-000000000001"))
		},
	} {
		if err := operation(); !errors.Is(err, ErrConfirmedPlanDrift) {
			t.Errorf("early %s reference error = %v, want ErrConfirmedPlanDrift", name, err)
		}
		assertSetupJournalTreeEqual(t, store.root, before)
	}
}

func TestSetupJournalOneActivePerCanonicalRoot(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	first, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(projectRoot), "alias")
	if err := os.Symlink(projectRoot, alias); err != nil {
		t.Fatal(err)
	}
	second, err := store.Begin(context.Background(), alias)
	if err != nil {
		t.Fatal(err)
	}
	if second.JournalID != first.JournalID || second.CanonicalRoot != projectRoot {
		t.Fatalf("canonical alias created another journal: first=%+v second=%+v", first, second)
	}
}

func TestSetupJournalConfirmedReplacementHasNoCopiedEffects(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	old, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), old.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCompleted(context.Background(), old.JournalID, StageProjectValidated); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCompleted(context.Background(), old.JournalID, StageGatewayReady); err != nil {
		t.Fatal(err)
	}
	if err := store.BindWorkspace(context.Background(), old.JournalID, testSetupWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordLastError(context.Background(), old.JournalID, StageWorkspaceRegistered, errors.New("token=secret path=/home/alice/.config/tool")); err != nil {
		t.Fatal(err)
	}

	replacementSelection := testSetupSelection()
	replacementSelection.PublicationVisibility = "private_git"
	replacement, err := store.BeginConfirmedReplacement(context.Background(), projectRoot, old.JournalID, replacementSelection)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.JournalID == old.JournalID || replacement.ReplacesJournalID != old.JournalID || replacement.Selection == nil {
		t.Fatalf("invalid replacement identity: %+v", replacement)
	}
	if replacement.WorkspaceID != "" || replacement.IdentityPrincipalID != "" || len(replacement.CompletedStages) != 0 || len(replacement.ConnectorBackups) != 0 || replacement.LastError != nil {
		t.Fatalf("replacement copied old effects: %+v", replacement)
	}
	got, resumable, err := store.Resumable(context.Background(), projectRoot)
	if err != nil || !resumable || got.JournalID != replacement.JournalID {
		t.Fatalf("Resumable(replacement) = %+v, %v, %v", got, resumable, err)
	}
}

func TestSetupJournalReplacementRejectsBackwardClockWithoutMutation(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	old, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), old.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	old, _, err = store.Resumable(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotSetupJournalTree(t, store.root)
	store.clock = func() time.Time { return old.UpdatedAt.Add(-time.Second) }
	if _, err := store.BeginConfirmedReplacement(context.Background(), projectRoot, old.JournalID, testSetupSelection()); !errors.Is(err, ErrInvalidSetupJournal) {
		t.Fatalf("BeginConfirmedReplacement(backward clock) error = %v, want ErrInvalidSetupJournal", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestConnectorBackupExactReplayAfterStageIsReadOnly(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	backup := BackupReference("connector-backup:v1:codex:40000000-0000-4000-8000-000000000001")
	for _, stage := range orderedSetupStages[:7] {
		switch stage {
		case StageWorkspaceRegistered:
			if err := store.BindWorkspace(context.Background(), journal.JournalID, testSetupWorkspaceID); err != nil {
				t.Fatal(err)
			}
		case StageIdentitySelected:
			if err := store.BindIdentity(context.Background(), journal.JournalID, testSetupHumanID); err != nil {
				t.Fatal(err)
			}
		case StageConnectorsApplied:
			if err := store.RecordConnectorBackup(context.Background(), journal.JournalID, backup); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.MarkCompleted(context.Background(), journal.JournalID, stage); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotSetupJournalTree(t, store.root)
	if err := store.RecordConnectorBackup(context.Background(), journal.JournalID, backup); err != nil {
		t.Fatalf("exact backup replay after stage: %v", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
	for _, changed := range []BackupReference{
		"connector-backup:v1:codex:40000000-0000-4000-8000-000000000002",
		"connector-backup:v1:claude:40000000-0000-4000-8000-000000000003",
	} {
		if err := store.RecordConnectorBackup(context.Background(), journal.JournalID, changed); !errors.Is(err, ErrConfirmedPlanDrift) {
			t.Errorf("changed backup %q error = %v, want ErrConfirmedPlanDrift", changed, err)
		}
		assertSetupJournalTreeEqual(t, store.root, before)
	}
}

func TestRedactSetupJournalFailureNeverPersistsSecretsOrPaths(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	secret := "Bearer top-secret-token PRIVATE KEY AWS_SECRET_ACCESS_KEY=value /home/alice/.config/codex/config.toml C:\\Users\\alice\\secret.json"
	if err := store.RecordLastError(context.Background(), journal.JournalID, StageProjectValidated, errors.New(secret)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.root, setupJournalRecordName(journal.JournalID)))
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"top-secret", "private key", "aws_secret", "/home/alice", "\\users\\alice", "config.toml", "secret.json"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("journal persisted forbidden failure content %q: %s", forbidden, data)
		}
	}
	got, _, err := store.Resumable(context.Background(), projectRoot)
	if err != nil || got.LastError == nil || got.LastError.Message != redactedSetupFailureMessage {
		t.Fatalf("redacted failure = %+v, err=%v", got.LastError, err)
	}
}

func TestSetupJournalRejectsNonmonotonicFailureTime(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	before := snapshotSetupJournalTree(t, store.root)
	base := journal.UpdatedAt.Add(time.Hour)
	calls := 0
	store.clock = func() time.Time {
		calls++
		if calls == 1 {
			return base.Add(time.Second)
		}
		return base
	}
	if err := store.RecordLastError(context.Background(), journal.JournalID, StageProjectValidated, errors.New("failure")); !errors.Is(err, ErrInvalidSetupJournal) {
		t.Fatalf("RecordLastError(nonmonotonic clock) error = %v, want ErrInvalidSetupJournal", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestSetupJournalUnsupportedPlatformFailsClosed(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux is the supported Stage 2 setup-journal platform")
	}
	if _, err := OpenSetupJournalStoreAt(filepath.Join(t.TempDir(), "journals")); !errors.Is(err, ErrSetupJournalFilesystemUnsupported) {
		t.Fatalf("OpenSetupJournalStoreAt on %s error = %v, want ErrSetupJournalFilesystemUnsupported", runtime.GOOS, err)
	}
}

func TestSetupJournalDefaultStoreUsesOwnerOnlyXDGDataRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("setup journal storage is Linux-only in this Stage 2 tranche")
	}
	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataRoot)
	store, err := OpenSetupJournalStore()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataRoot, "wormhole", "setup-journals")
	if store.root != want {
		t.Fatalf("default setup journal root = %q, want %q", store.root, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("default setup journal root mode = %#o, want 0700", info.Mode().Perm())
	}
	t.Setenv("XDG_DATA_HOME", "relative")
	if _, err := OpenSetupJournalStore(); !errors.Is(err, ErrUnsafeSetupJournalStore) {
		t.Fatalf("OpenSetupJournalStore(relative XDG) error = %v, want ErrUnsafeSetupJournalStore", err)
	}
}

func testSetupSelection() SetupSelection {
	changes := make([]ConfirmedChange, len(orderedSetupStages))
	for index, stage := range orderedSetupStages {
		prior := sha256.Sum256([]byte("prior:" + string(stage)))
		desired := sha256.Sum256([]byte("desired:" + string(stage)))
		changes[index] = ConfirmedChange{
			Stage: stage, Subject: testStageSubject(stage), Action: testStageAction(stage),
			PriorDigest:   StateDigest("sha256:" + strings.ToLower(hexDigest(prior[:]))),
			DesiredDigest: StateDigest("sha256:" + strings.ToLower(hexDigest(desired[:]))),
		}
	}
	return SetupSelection{
		ConnectorAdapters: []string{"codex", "claude"}, PublicationVisibility: "public_git",
		PublicationBindingDigest: SHA256StateDigest([]byte("publication-binding")),
		Identity:                 types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"},
		PlanDigest:               SHA256StateDigest([]byte("rendered-plan")), Changes: changes,
	}
}

func testStageSubject(stage SetupStage) string {
	switch stage {
	case StageProjectValidated:
		return "project"
	case StageGatewayReady:
		return "gateway-service"
	case StageWorkspaceRegistered:
		return "workspace"
	case StageIdentitySelected:
		return "identity"
	case StagePublicationClassified:
		return "publication"
	case StageBaseImported:
		return "base"
	case StageConnectorsApplied:
		return "connector:codex"
	default:
		return "setup"
	}
}

func testStageAction(stage SetupStage) string {
	switch stage {
	case StageProjectValidated:
		return "validate"
	case StageGatewayReady:
		return "ensure"
	case StageWorkspaceRegistered:
		return "register"
	case StageIdentitySelected:
		return "ensure-selected"
	case StagePublicationClassified:
		return "classify"
	case StageBaseImported:
		return "import"
	case StageConnectorsApplied:
		return "install"
	default:
		return "verify"
	}
}

func hexDigest(data []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(data)*2)
	for index, value := range data {
		encoded[index*2] = alphabet[value>>4]
		encoded[index*2+1] = alphabet[value&0x0f]
	}
	return string(encoded)
}

func cloneSetupSelection(value SetupSelection) SetupSelection {
	clone := value
	clone.ConnectorAdapters = append([]string(nil), value.ConnectorAdapters...)
	clone.Changes = append([]ConfirmedChange(nil), value.Changes...)
	return clone
}

func newSetupJournalTestStore(t *testing.T) (*SetupJournalStore, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("setup journal storage is Linux-only in this Stage 2 tranche")
	}
	root := filepath.Join(t.TempDir(), "setup-journals")
	store, err := OpenSetupJournalStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	sequence := byte(1)
	store.random = func(data []byte) (int, error) {
		for index := range data {
			data[index] = sequence
			sequence++
		}
		return len(data), nil
	}
	tick := 0
	store.clock = func() time.Time {
		tick++
		return time.Date(2026, 8, 26, 12, 0, tick, 0, time.UTC)
	}
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return store, projectRoot
}

func snapshotSetupJournalTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]byte{}
	for _, entry := range entries {
		if entry.Name() == setupJournalLockName || strings.HasPrefix(entry.Name(), setupJournalTemporaryPrefix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = data
	}
	return result
}

func assertSetupJournalTreeEqual(t *testing.T, root string, want map[string][]byte) {
	t.Helper()
	if got := snapshotSetupJournalTree(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("setup journal tree changed:\n got=%q\nwant=%q", got, want)
	}
}
