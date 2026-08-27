package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	setupJournalSchemaVersion   = 1
	setupJournalLockName        = ".store.lock"
	setupJournalTemporaryPrefix = ".tmp-"
	maxSetupJournalRecordBytes  = 64 * 1024
	maxSetupJournalStoreEntries = 512
	maxSetupJournalTempEntries  = 512
	maxSetupRootBytes           = 4096
	maxSetupConfirmedChanges    = 64
	redactedSetupFailureMessage = "setup stage failed; inspect the owning component for details"
)

var (
	ErrInvalidStateDigest                = errors.New("config: invalid setup state digest")
	ErrInvalidConfirmedPlan              = errors.New("config: invalid confirmed setup plan")
	ErrConfirmedPlanRequired             = errors.New("config: confirmed setup plan required")
	ErrConfirmedPlanDrift                = errors.New("config: confirmed setup plan drift")
	ErrSetupJournalNotFound              = errors.New("config: setup journal not found")
	ErrInvalidSetupJournal               = errors.New("config: invalid setup journal")
	ErrAmbiguousSetupJournal             = errors.New("config: ambiguous setup journal authority")
	ErrUnsafeSetupJournalStore           = errors.New("config: unsafe owner-only setup journal store")
	ErrSetupJournalFilesystemUnsupported = errors.New("config: owner-only setup journal filesystem unsupported")
)

type StateDigest string

// SHA256StateDigest returns the canonical digest of one exact observed state.
func SHA256StateDigest(data []byte) StateDigest {
	digest := sha256.Sum256(data)
	return StateDigest("sha256:" + hex.EncodeToString(digest[:]))
}

// ParseStateDigest accepts only sha256 followed by 64 lowercase hexadecimal digits.
func ParseStateDigest(value string) (StateDigest, error) {
	if !validStateDigest(StateDigest(value)) {
		return "", ErrInvalidStateDigest
	}
	return StateDigest(value), nil
}

func validStateDigest(value StateDigest) bool {
	text := string(value)
	if len(text) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(text, "sha256:") {
		return false
	}
	for _, character := range text[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type SetupStage string

const (
	StageProjectValidated      SetupStage = "project_validated"
	StageGatewayReady          SetupStage = "gateway_ready"
	StageWorkspaceRegistered   SetupStage = "workspace_registered"
	StageIdentitySelected      SetupStage = "identity_selected"
	StagePublicationClassified SetupStage = "publication_classified"
	StageBaseImported          SetupStage = "base_imported"
	StageConnectorsApplied     SetupStage = "connectors_applied"
	StageFinalVerified         SetupStage = "final_verified"
)

var orderedSetupStages = []SetupStage{
	StageProjectValidated,
	StageGatewayReady,
	StageWorkspaceRegistered,
	StageIdentitySelected,
	StagePublicationClassified,
	StageBaseImported,
	StageConnectorsApplied,
	StageFinalVerified,
}

func validSetupStage(stage SetupStage) bool {
	return setupStageIndex(stage) >= 0
}

func setupStageIndex(stage SetupStage) int {
	for index, candidate := range orderedSetupStages {
		if candidate == stage {
			return index
		}
	}
	return -1
}

// ConfirmedChange binds one displayed setup action to exact prior and desired
// observations. The journal coordinates the confirmation but does not perform
// or become authoritative for the owning component's transition.
type ConfirmedChange struct {
	Stage         SetupStage  `json:"stage"`
	Subject       string      `json:"subject"`
	Action        string      `json:"action"`
	PriorDigest   StateDigest `json:"prior_digest"`
	DesiredDigest StateDigest `json:"desired_digest"`
}

// SetupSelection is the bounded, complete plan approved before external setup
// effects. Changes are ordered by stage and then retain displayed order.
type SetupSelection struct {
	ConnectorAdapters        []string                         `json:"connector_adapters"`
	PublicationVisibility    string                           `json:"publication_visibility"`
	PublicationBindingDigest StateDigest                      `json:"publication_binding_digest"`
	Identity                 types.ConfirmedIdentitySelection `json:"identity"`
	PlanDigest               StateDigest                      `json:"plan_digest"`
	Changes                  []ConfirmedChange                `json:"changes"`
}

type SetupJournalState string

const (
	SetupJournalActive    SetupJournalState = "active"
	SetupJournalCompleted SetupJournalState = "completed"
	SetupJournalReplaced  SetupJournalState = "replaced"
)

// BackupReference is an opaque pointer into the connector adapter's owner-only
// backup store. Raw connector bytes never enter this journal.
type BackupReference string

// SetupFailure is deliberately redacted. Component-owned diagnostics remain
// with the component that can enforce their secret boundary.
type SetupFailure struct {
	Stage      SetupStage `json:"stage"`
	Message    string     `json:"message"`
	RecordedAt time.Time  `json:"recorded_at"`
}

// SetupJournal is the canonical schema-v1 recovery record for one confirmed
// high-level setup plan. Workspace, identity, service, project-state, and
// connector stores remain authoritative for their own effects.
type SetupJournal struct {
	SchemaVersion       int               `json:"schema_version"`
	JournalID           string            `json:"journal_id"`
	CanonicalRoot       string            `json:"canonical_root"`
	State               SetupJournalState `json:"state"`
	WorkspaceID         types.WorkspaceID `json:"workspace_id,omitempty"`
	IdentityPrincipalID string            `json:"identity_principal_id,omitempty"`
	Selection           *SetupSelection   `json:"selection,omitempty"`
	CompletedStages     []SetupStage      `json:"completed_stages"`
	ConnectorBackups    []BackupReference `json:"connector_backups"`
	LastError           *SetupFailure     `json:"last_error,omitempty"`
	ReplacesJournalID   string            `json:"replaces_journal_id,omitempty"`
	ReplacedByJournalID string            `json:"replaced_by_journal_id,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	CompletedAt         *time.Time        `json:"completed_at,omitempty"`
}

// SetupJournalStore owns Linux descriptor-relative setup journal persistence.
// Its hooks are package-private fault seams, never ambient global state.
type SetupJournalStore struct {
	root        string
	clock       func() time.Time
	random      func([]byte) (int, error)
	fault       func(string) error
	atomicWrite func(int, string, []byte, []byte, fs.FileMode, bool) error
}

// OpenSetupJournalStore opens the owner-only machine-private XDG data store.
func OpenSetupJournalStore() (*SetupJournalStore, error) {
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil, fmt.Errorf("%w: data root unavailable", ErrUnsafeSetupJournalStore)
		}
		dataRoot = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(dataRoot) {
		return nil, fmt.Errorf("%w: data root must be absolute", ErrUnsafeSetupJournalStore)
	}
	return OpenSetupJournalStoreAt(filepath.Join(filepath.Clean(dataRoot), "wormhole", "setup-journals"))
}

// OpenSetupJournalStoreAt opens or creates an explicit owner-only store root.
func OpenSetupJournalStoreAt(root string) (*SetupJournalStore, error) {
	canonicalRoot, fd, err := openSetupJournalRoot(root)
	if err != nil {
		return nil, err
	}
	if err := closeSetupJournalFD(fd); err != nil {
		return nil, err
	}
	store := &SetupJournalStore{root: canonicalRoot, clock: time.Now, random: rand.Read}
	store.atomicWrite = store.atomicWriteFile
	return store, nil
}

// Begin returns the one active journal for canonical root or durably creates an
// unconfirmed journal with no authorised effects.
func (s *SetupJournalStore) Begin(ctx context.Context, root string) (SetupJournal, error) {
	canonicalRoot, err := canonicalSetupProjectRoot(root)
	if err != nil {
		return SetupJournal{}, err
	}
	return s.withLockedJournalResult(ctx, func(fd int, records map[string]SetupJournal) (SetupJournal, error) {
		if active := activeSetupJournalForRoot(records, canonicalRoot); active != nil {
			return cloneSetupJournal(*active), nil
		}
		identifier, err := s.newJournalUUID(records)
		if err != nil {
			return SetupJournal{}, err
		}
		now, err := s.now()
		if err != nil {
			return SetupJournal{}, err
		}
		journal := SetupJournal{
			SchemaVersion: setupJournalSchemaVersion, JournalID: identifier, CanonicalRoot: canonicalRoot,
			State: SetupJournalActive, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.writeJournal(fd, journal, nil); err != nil {
			return SetupJournal{}, err
		}
		return cloneSetupJournal(journal), nil
	})
}

// SetSelection durably freezes the complete confirmed plan. Repeating the
// exact selection is idempotent; any different selection is drift.
func (s *SetupJournalStore) SetSelection(ctx context.Context, journalID string, selection SetupSelection) error {
	if err := validateSetupSelection(selection); err != nil {
		return err
	}
	selection = cloneSetupSelectionValue(selection)
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if journal.State != SetupJournalActive {
			return false, ErrConfirmedPlanDrift
		}
		if journal.Selection != nil {
			if equalSetupSelections(*journal.Selection, selection) {
				return false, nil
			}
			return false, ErrConfirmedPlanDrift
		}
		if len(journal.CompletedStages) != 0 || journal.WorkspaceID != "" || journal.IdentityPrincipalID != "" || len(journal.ConnectorBackups) != 0 {
			return false, ErrConfirmedPlanDrift
		}
		journal.Selection = &selection
		return true, nil
	})
}

// BindWorkspace records only the workspace authority's opaque identifier.
func (s *SetupJournalStore) BindWorkspace(ctx context.Context, journalID string, workspaceID types.WorkspaceID) error {
	if !types.CanonicalUUID(string(workspaceID)) {
		return ErrConfirmedPlanDrift
	}
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if err := requireConfirmedActive(journal); err != nil {
			return false, err
		}
		if journal.WorkspaceID == workspaceID {
			return false, nil
		}
		if journal.WorkspaceID != "" || len(journal.CompletedStages) != setupStageIndex(StageWorkspaceRegistered) {
			return false, ErrConfirmedPlanDrift
		}
		journal.WorkspaceID = workspaceID
		return true, nil
	})
}

// BindIdentity records only the local-identity authority's principal UUID.
func (s *SetupJournalStore) BindIdentity(ctx context.Context, journalID, principalID string) error {
	if !types.CanonicalUUID(principalID) {
		return ErrConfirmedPlanDrift
	}
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if err := requireConfirmedActive(journal); err != nil {
			return false, err
		}
		if journal.IdentityPrincipalID == principalID {
			return false, nil
		}
		if journal.IdentityPrincipalID != "" || len(journal.CompletedStages) != setupStageIndex(StageIdentitySelected) {
			return false, ErrConfirmedPlanDrift
		}
		journal.IdentityPrincipalID = principalID
		return true, nil
	})
}

// RecordConnectorBackup records an opaque connector-owned backup reference.
func (s *SetupJournalStore) RecordConnectorBackup(ctx context.Context, journalID string, reference BackupReference) error {
	adapter, valid := parseBackupReference(reference)
	if !valid {
		return ErrConfirmedPlanDrift
	}
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if err := requireConfirmedActive(journal); err != nil {
			return false, err
		}
		if !containsString(journal.Selection.ConnectorAdapters, adapter) {
			return false, ErrConfirmedPlanDrift
		}
		for _, existing := range journal.ConnectorBackups {
			existingAdapter, _ := parseBackupReference(existing)
			if existing == reference {
				return false, nil
			}
			if existingAdapter == adapter {
				return false, ErrConfirmedPlanDrift
			}
		}
		if len(journal.CompletedStages) != setupStageIndex(StageConnectorsApplied) {
			return false, ErrConfirmedPlanDrift
		}
		journal.ConnectorBackups = append(journal.ConnectorBackups, reference)
		return true, nil
	})
}

// MarkCompleted advances only the next stage in the exact schema-v1 prefix.
func (s *SetupJournalStore) MarkCompleted(ctx context.Context, journalID string, stage SetupStage) error {
	if !validSetupStage(stage) {
		return ErrConfirmedPlanDrift
	}
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if err := requireConfirmedActive(journal); err != nil {
			return false, err
		}
		index := setupStageIndex(stage)
		if index < len(journal.CompletedStages) {
			if journal.CompletedStages[index] == stage {
				return false, nil
			}
			return false, ErrConfirmedPlanDrift
		}
		if index != len(journal.CompletedStages) {
			return false, ErrConfirmedPlanDrift
		}
		if stage == StageWorkspaceRegistered && journal.WorkspaceID == "" {
			return false, ErrConfirmedPlanDrift
		}
		if stage == StageIdentitySelected && journal.IdentityPrincipalID == "" {
			return false, ErrConfirmedPlanDrift
		}
		journal.CompletedStages = append(journal.CompletedStages, stage)
		journal.LastError = nil
		return true, nil
	})
}

// RecordLastError persists a fixed redacted diagnostic for the next stage.
func (s *SetupJournalStore) RecordLastError(ctx context.Context, journalID string, stage SetupStage, cause error) error {
	if !validSetupStage(stage) || cause == nil {
		return ErrConfirmedPlanDrift
	}
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if err := requireConfirmedActive(journal); err != nil {
			return false, err
		}
		if setupStageIndex(stage) != len(journal.CompletedStages) {
			return false, ErrConfirmedPlanDrift
		}
		now, err := s.now()
		if err != nil {
			return false, err
		}
		journal.LastError = &SetupFailure{Stage: stage, Message: redactedSetupFailureMessage, RecordedAt: now}
		return true, nil
	})
}

// BeginConfirmedReplacement creates a separately confirmed, effect-free
// successor for one active journal at root. The old record is retained as
// terminal evidence and interrupted two-record publication is recoverable.
func (s *SetupJournalStore) BeginConfirmedReplacement(ctx context.Context, root, replacedJournalID string, selection SetupSelection) (SetupJournal, error) {
	canonicalRoot, err := canonicalSetupProjectRoot(root)
	if err != nil {
		return SetupJournal{}, err
	}
	if !types.CanonicalUUID(replacedJournalID) {
		return SetupJournal{}, ErrConfirmedPlanDrift
	}
	if err := validateSetupSelection(selection); err != nil {
		return SetupJournal{}, err
	}
	selection = cloneSetupSelectionValue(selection)
	return s.withLockedJournalResult(ctx, func(fd int, records map[string]SetupJournal) (SetupJournal, error) {
		old, exists := records[replacedJournalID]
		if !exists || old.CanonicalRoot != canonicalRoot || old.Selection == nil {
			return SetupJournal{}, ErrConfirmedPlanDrift
		}
		if old.State == SetupJournalReplaced {
			replacement, ok := records[old.ReplacedByJournalID]
			if !ok || replacement.State != SetupJournalActive || replacement.ReplacesJournalID != old.JournalID || !equalSetupSelections(*replacement.Selection, selection) {
				return SetupJournal{}, ErrConfirmedPlanDrift
			}
			return cloneSetupJournal(replacement), nil
		}
		if old.State != SetupJournalActive || activeSetupJournalForRoot(records, canonicalRoot).JournalID != old.JournalID {
			return SetupJournal{}, ErrConfirmedPlanDrift
		}
		identifier, err := s.newJournalUUID(records)
		if err != nil {
			return SetupJournal{}, err
		}
		now, err := s.now()
		if err != nil {
			return SetupJournal{}, err
		}
		if now.Before(old.UpdatedAt) {
			return SetupJournal{}, ErrInvalidSetupJournal
		}
		replacement := SetupJournal{
			SchemaVersion: setupJournalSchemaVersion, JournalID: identifier, CanonicalRoot: canonicalRoot,
			State: SetupJournalActive, Selection: &selection, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
			ReplacesJournalID: old.JournalID, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.writeJournal(fd, replacement, nil); err != nil {
			return SetupJournal{}, err
		}
		if err := s.injectFault("replacement_after_new"); err != nil {
			return SetupJournal{}, err
		}
		old.State = SetupJournalReplaced
		old.ReplacedByJournalID = replacement.JournalID
		old.UpdatedAt = now
		old.CompletedAt = timePointer(now)
		priorOld := records[old.JournalID]
		if err := s.writeJournal(fd, old, &priorOld); err != nil {
			return SetupJournal{}, err
		}
		if err := s.injectFault("replacement_after_old"); err != nil {
			return SetupJournal{}, err
		}
		return cloneSetupJournal(replacement), nil
	})
}

// Resumable returns the single active journal for canonical root. It refuses
// corrupt or ambiguous store topology without selecting an authority.
func (s *SetupJournalStore) Resumable(ctx context.Context, root string) (SetupJournal, bool, error) {
	canonicalRoot, err := canonicalSetupProjectRoot(root)
	if err != nil {
		return SetupJournal{}, false, err
	}
	journal, err := s.withLockedJournalResult(ctx, func(_ int, records map[string]SetupJournal) (SetupJournal, error) {
		if active := activeSetupJournalForRoot(records, canonicalRoot); active != nil {
			return cloneSetupJournal(*active), nil
		}
		return SetupJournal{}, nil
	})
	return journal, journal.JournalID != "", err
}

// Complete terminalises a journal only after the full eight-stage prefix.
func (s *SetupJournalStore) Complete(ctx context.Context, journalID string) error {
	return s.mutate(ctx, journalID, func(journal *SetupJournal) (bool, error) {
		if journal.State == SetupJournalCompleted {
			return false, nil
		}
		if err := requireConfirmedActive(journal); err != nil {
			return false, err
		}
		if len(journal.CompletedStages) != len(orderedSetupStages) {
			return false, ErrConfirmedPlanDrift
		}
		now, err := s.now()
		if err != nil {
			return false, err
		}
		journal.State = SetupJournalCompleted
		journal.CompletedAt = timePointer(now)
		journal.LastError = nil
		return true, nil
	})
}

func (s *SetupJournalStore) mutate(ctx context.Context, journalID string, mutate func(*SetupJournal) (bool, error)) error {
	if !types.CanonicalUUID(journalID) {
		return ErrSetupJournalNotFound
	}
	_, err := s.withLockedJournalResult(ctx, func(fd int, records map[string]SetupJournal) (SetupJournal, error) {
		journal, exists := records[journalID]
		if !exists {
			return SetupJournal{}, ErrSetupJournalNotFound
		}
		prior := cloneSetupJournal(journal)
		changed, err := mutate(&journal)
		if err != nil || !changed {
			return SetupJournal{}, err
		}
		now, err := s.now()
		if err != nil {
			return SetupJournal{}, err
		}
		if now.Before(journal.UpdatedAt) {
			return SetupJournal{}, ErrInvalidSetupJournal
		}
		journal.UpdatedAt = now
		if journal.CompletedAt != nil && journal.CompletedAt.Before(journal.CreatedAt) {
			return SetupJournal{}, ErrInvalidSetupJournal
		}
		return SetupJournal{}, s.writeJournal(fd, journal, &prior)
	})
	return err
}

func (s *SetupJournalStore) withLockedJournalResult(ctx context.Context, operation func(int, map[string]SetupJournal) (SetupJournal, error)) (SetupJournal, error) {
	if s == nil || s.root == "" || s.atomicWrite == nil || s.clock == nil || s.random == nil {
		return SetupJournal{}, ErrUnsafeSetupJournalStore
	}
	if err := ctx.Err(); err != nil {
		return SetupJournal{}, err
	}
	fd, err := s.openRoot()
	if err != nil {
		return SetupJournal{}, err
	}
	defer closeSetupJournalFD(fd)
	unlock, err := lockSetupJournalStore(ctx, fd)
	if err != nil {
		return SetupJournal{}, err
	}
	defer unlock()
	records, err := s.loadAndRecoverTopology(fd)
	if err != nil {
		return SetupJournal{}, err
	}
	return operation(fd, records)
}

type setupJournalRecoveryAction struct {
	canonicalRoot string
	predecessorID string
	successorID   string
}

type preparedSetupJournalRecovery struct {
	prior SetupJournal
	next  SetupJournal
}

func (s *SetupJournalStore) loadAndRecoverTopology(fd int) (map[string]SetupJournal, error) {
	names, err := listSetupJournalNames(fd)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	records := map[string]SetupJournal{}
	temporaryNames := make([]string, 0)
	for _, name := range names {
		if name == setupJournalLockName {
			continue
		}
		if isSetupJournalTemporaryName(name) {
			if _, exists, readErr := readSetupJournalFile(fd, name); readErr != nil || !exists {
				if readErr != nil {
					return nil, readErr
				}
				return nil, ErrInvalidSetupJournal
			}
			temporaryNames = append(temporaryNames, name)
			continue
		}
		identifier := parseSetupJournalRecordName(name)
		if identifier == "" {
			return nil, ErrInvalidSetupJournal
		}
		journal, exists, err := readSetupJournalRecord(fd, name)
		if err != nil || !exists || journal.JournalID != identifier {
			if err != nil {
				return nil, err
			}
			return nil, ErrInvalidSetupJournal
		}
		records[identifier] = journal
	}
	actions, err := classifySetupJournalTopology(records)
	if err != nil {
		return nil, err
	}
	prepared, err := s.prepareSetupJournalRecovery(records, actions)
	if err != nil {
		return nil, err
	}
	for _, recovery := range prepared {
		if err := s.writeJournal(fd, recovery.next, &recovery.prior); err != nil {
			return nil, err
		}
		records[recovery.next.JournalID] = recovery.next
	}
	if err := retireSetupJournalTemporaryFiles(fd, temporaryNames); err != nil {
		return nil, err
	}
	return records, nil
}

// classifySetupJournalTopology is pure: it validates every record, root,
// replacement link, and cycle against a projected recovered topology and
// returns a canonical-root-ordered action list without consulting the clock or
// filesystem. Callers must finish preparing every action before applying any.
func classifySetupJournalTopology(records map[string]SetupJournal) ([]setupJournalRecoveryAction, error) {
	identifiers := make([]string, 0, len(records))
	projected := make(map[string]SetupJournal, len(records))
	for identifier, journal := range records {
		if identifier != journal.JournalID || validateSetupJournal(journal) != nil {
			return nil, ErrInvalidSetupJournal
		}
		identifiers = append(identifiers, identifier)
		projected[identifier] = cloneSetupJournal(journal)
	}
	sort.Strings(identifiers)
	byRoot := map[string][]string{}
	for id, journal := range projected {
		if journal.State == SetupJournalActive {
			byRoot[journal.CanonicalRoot] = append(byRoot[journal.CanonicalRoot], id)
		}
	}
	roots := make([]string, 0, len(byRoot))
	for root := range byRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	actions := make([]setupJournalRecoveryAction, 0)
	for _, root := range roots {
		rootIdentifiers := byRoot[root]
		sort.Strings(rootIdentifiers)
		switch len(rootIdentifiers) {
		case 1:
			journal := projected[rootIdentifiers[0]]
			if journal.ReplacesJournalID != "" {
				old, ok := projected[journal.ReplacesJournalID]
				if !ok || old.CanonicalRoot != root || old.State != SetupJournalReplaced || old.ReplacedByJournalID != journal.JournalID {
					return nil, ErrAmbiguousSetupJournal
				}
			}
		case 2:
			first, second := projected[rootIdentifiers[0]], projected[rootIdentifiers[1]]
			old, replacement, ok := exactInterruptedReplacement(first, second)
			if !ok {
				old, replacement, ok = exactInterruptedReplacement(second, first)
			}
			if !ok {
				return nil, ErrAmbiguousSetupJournal
			}
			actions = append(actions, setupJournalRecoveryAction{
				canonicalRoot: root,
				predecessorID: old.JournalID,
				successorID:   replacement.JournalID,
			})
			old.State = SetupJournalReplaced
			old.ReplacedByJournalID = replacement.JournalID
			old.CompletedAt = timePointer(old.UpdatedAt)
			if err := validateSetupJournal(old); err != nil {
				return nil, ErrInvalidSetupJournal
			}
			projected[old.JournalID] = old
		default:
			return nil, ErrAmbiguousSetupJournal
		}
	}
	for _, identifier := range identifiers {
		journal := projected[identifier]
		if journal.State == SetupJournalReplaced {
			replacement, ok := projected[journal.ReplacedByJournalID]
			if !ok || replacement.ReplacesJournalID != journal.JournalID || replacement.CanonicalRoot != journal.CanonicalRoot {
				return nil, ErrInvalidSetupJournal
			}
		}
		if journal.ReplacesJournalID != "" {
			predecessor, ok := projected[journal.ReplacesJournalID]
			if !ok || predecessor.State != SetupJournalReplaced || predecessor.ReplacedByJournalID != journal.JournalID || predecessor.CanonicalRoot != journal.CanonicalRoot {
				return nil, ErrInvalidSetupJournal
			}
		}
	}
	for _, identifier := range identifiers {
		seen := map[string]bool{}
		current := identifier
		for current != "" {
			if seen[current] {
				return nil, ErrInvalidSetupJournal
			}
			seen[current] = true
			journal, ok := projected[current]
			if !ok {
				return nil, ErrInvalidSetupJournal
			}
			current = journal.ReplacesJournalID
		}
	}
	return actions, nil
}

func (s *SetupJournalStore) prepareSetupJournalRecovery(records map[string]SetupJournal, actions []setupJournalRecoveryAction) ([]preparedSetupJournalRecovery, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	now, err := s.now()
	if err != nil {
		return nil, err
	}
	prepared := make([]preparedSetupJournalRecovery, 0, len(actions))
	for _, action := range actions {
		old, oldExists := records[action.predecessorID]
		replacement, replacementExists := records[action.successorID]
		if !oldExists || !replacementExists || old.CanonicalRoot != action.canonicalRoot ||
			now.Before(old.UpdatedAt) || now.Before(replacement.CreatedAt) {
			return nil, ErrInvalidSetupJournal
		}
		prior := cloneSetupJournal(old)
		old.State = SetupJournalReplaced
		old.ReplacedByJournalID = replacement.JournalID
		old.UpdatedAt = now
		old.CompletedAt = timePointer(now)
		if err := validateSetupJournal(old); err != nil {
			return nil, ErrInvalidSetupJournal
		}
		if _, err := marshalCanonicalSetupJournal(prior); err != nil {
			return nil, ErrInvalidSetupJournal
		}
		if _, err := marshalCanonicalSetupJournal(old); err != nil {
			return nil, ErrInvalidSetupJournal
		}
		prepared = append(prepared, preparedSetupJournalRecovery{prior: prior, next: old})
	}
	return prepared, nil
}

func exactInterruptedReplacement(old, replacement SetupJournal) (SetupJournal, SetupJournal, bool) {
	return old, replacement, old.State == SetupJournalActive && replacement.State == SetupJournalActive &&
		old.Selection != nil &&
		replacement.ReplacesJournalID == old.JournalID && replacement.CanonicalRoot == old.CanonicalRoot &&
		!replacement.CreatedAt.Before(old.UpdatedAt) &&
		replacementIsClean(replacement)
}

func replacementIsClean(journal SetupJournal) bool {
	return journal.Selection != nil && journal.WorkspaceID == "" && journal.IdentityPrincipalID == "" &&
		len(journal.CompletedStages) == 0 && len(journal.ConnectorBackups) == 0 && journal.LastError == nil &&
		journal.ReplacedByJournalID == "" && journal.CompletedAt == nil
}

func activeSetupJournalForRoot(records map[string]SetupJournal, root string) *SetupJournal {
	for _, journal := range records {
		if journal.CanonicalRoot == root && journal.State == SetupJournalActive {
			copy := journal
			return &copy
		}
	}
	return nil
}

func (s *SetupJournalStore) writeJournal(fd int, journal SetupJournal, prior *SetupJournal) error {
	if err := validateSetupJournal(journal); err != nil {
		return err
	}
	data, err := marshalCanonicalSetupJournal(journal)
	if err != nil {
		return err
	}
	var expected []byte
	if prior != nil {
		if prior.JournalID != journal.JournalID {
			return ErrConfirmedPlanDrift
		}
		expected, err = marshalCanonicalSetupJournal(*prior)
		if err != nil {
			return err
		}
	}
	return s.atomicWrite(fd, setupJournalRecordName(journal.JournalID), data, expected, 0o600, prior != nil)
}

func (s *SetupJournalStore) atomicWriteFile(fd int, name string, data, expected []byte, mode fs.FileMode, replace bool) error {
	return atomicSetupJournalWrite(fd, name, data, expected, mode, replace, s.random, s.fault)
}

func (s *SetupJournalStore) openRoot() (int, error) {
	canonical, fd, err := openSetupJournalRoot(s.root)
	if err != nil {
		return -1, err
	}
	if canonical != s.root {
		_ = closeSetupJournalFD(fd)
		return -1, ErrUnsafeSetupJournalStore
	}
	return fd, nil
}

func (s *SetupJournalStore) newJournalUUID(records map[string]SetupJournal) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		data := make([]byte, 16)
		if _, err := io.ReadFull(setupRandomReader{s.random}, data); err != nil {
			return "", fmt.Errorf("config: generate setup journal UUID: %w", err)
		}
		data[6] = data[6]&0x0f | 0x40
		data[8] = data[8]&0x3f | 0x80
		identifier := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
		if _, exists := records[identifier]; !exists {
			return identifier, nil
		}
	}
	return "", ErrAmbiguousSetupJournal
}

func (s *SetupJournalStore) now() (time.Time, error) {
	now := s.clock().UTC()
	if now.IsZero() {
		return time.Time{}, ErrInvalidSetupJournal
	}
	return now, nil
}

func (s *SetupJournalStore) injectFault(point string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(point)
}

func requireConfirmedActive(journal *SetupJournal) error {
	if journal.State != SetupJournalActive {
		return ErrConfirmedPlanDrift
	}
	if journal.Selection == nil {
		return ErrConfirmedPlanRequired
	}
	return nil
}

func validateSetupSelection(selection SetupSelection) error {
	if selection.ConnectorAdapters == nil || selection.Changes == nil || len(selection.Changes) > maxSetupConfirmedChanges ||
		selection.Identity.Validate() != nil || !validStateDigest(selection.PublicationBindingDigest) || !validStateDigest(selection.PlanDigest) {
		return ErrInvalidConfirmedPlan
	}
	classification := types.PublicationClassification(selection.PublicationVisibility)
	if classification.Validate() != nil {
		return ErrInvalidConfirmedPlan
	}
	seenAdapters := map[string]bool{}
	for _, adapter := range selection.ConnectorAdapters {
		if (adapter != "codex" && adapter != "claude") || seenAdapters[adapter] {
			return ErrInvalidConfirmedPlan
		}
		seenAdapters[adapter] = true
	}
	priorStage := -1
	seenChanges := map[string]bool{}
	for _, change := range selection.Changes {
		stage := setupStageIndex(change.Stage)
		key := string(change.Stage) + "\x00" + change.Subject
		if stage < 0 || stage < priorStage || seenChanges[key] || !validConfirmedChangeVocabulary(change, seenAdapters) ||
			!validStateDigest(change.PriorDigest) || !validStateDigest(change.DesiredDigest) || change.PriorDigest == change.DesiredDigest {
			return ErrInvalidConfirmedPlan
		}
		priorStage = stage
		seenChanges[key] = true
	}
	return nil
}

type setupChangeSubject string
type setupChangeAction string

const (
	setupSubjectProject         setupChangeSubject = "project"
	setupSubjectGatewayService  setupChangeSubject = "gateway-service"
	setupSubjectWorkspace       setupChangeSubject = "workspace"
	setupSubjectIdentity        setupChangeSubject = "identity"
	setupSubjectPublication     setupChangeSubject = "publication"
	setupSubjectBase            setupChangeSubject = "base"
	setupSubjectConnectorCodex  setupChangeSubject = "connector:codex"
	setupSubjectConnectorClaude setupChangeSubject = "connector:claude"
	setupSubjectFinal           setupChangeSubject = "setup"

	setupActionValidate       setupChangeAction = "validate"
	setupActionEnsure         setupChangeAction = "ensure"
	setupActionRegister       setupChangeAction = "register"
	setupActionEnsureSelected setupChangeAction = "ensure-selected"
	setupActionClassify       setupChangeAction = "classify"
	setupActionImport         setupChangeAction = "import"
	setupActionInstall        setupChangeAction = "install"
	setupActionVerify         setupChangeAction = "verify"
)

func validConfirmedChangeVocabulary(change ConfirmedChange, adapters map[string]bool) bool {
	subject, action := setupChangeSubject(change.Subject), setupChangeAction(change.Action)
	switch change.Stage {
	case StageProjectValidated:
		return subject == setupSubjectProject && action == setupActionValidate
	case StageGatewayReady:
		return subject == setupSubjectGatewayService && action == setupActionEnsure
	case StageWorkspaceRegistered:
		return subject == setupSubjectWorkspace && action == setupActionRegister
	case StageIdentitySelected:
		return subject == setupSubjectIdentity && action == setupActionEnsureSelected
	case StagePublicationClassified:
		return subject == setupSubjectPublication && action == setupActionClassify
	case StageBaseImported:
		return subject == setupSubjectBase && action == setupActionImport
	case StageConnectorsApplied:
		return action == setupActionInstall &&
			((subject == setupSubjectConnectorCodex && adapters["codex"]) ||
				(subject == setupSubjectConnectorClaude && adapters["claude"]))
	case StageFinalVerified:
		return subject == setupSubjectFinal && action == setupActionVerify
	default:
		return false
	}
}

func validateSetupJournal(journal SetupJournal) error {
	if journal.SchemaVersion != setupJournalSchemaVersion || !types.CanonicalUUID(journal.JournalID) ||
		!validCanonicalSetupRoot(journal.CanonicalRoot) || journal.CompletedStages == nil || journal.ConnectorBackups == nil ||
		!validSetupTime(journal.CreatedAt) || !validSetupTime(journal.UpdatedAt) || journal.UpdatedAt.Before(journal.CreatedAt) {
		return fmt.Errorf("%w: header schema=%t id=%t root=%t stages=%t backups=%t created=%t updated=%t order=%t", ErrInvalidSetupJournal,
			journal.SchemaVersion == setupJournalSchemaVersion, types.CanonicalUUID(journal.JournalID), validCanonicalSetupRoot(journal.CanonicalRoot),
			journal.CompletedStages != nil, journal.ConnectorBackups != nil, validSetupTime(journal.CreatedAt), validSetupTime(journal.UpdatedAt), !journal.UpdatedAt.Before(journal.CreatedAt))
	}
	if journal.Selection != nil {
		if err := validateSetupSelection(*journal.Selection); err != nil {
			return fmt.Errorf("%w: selection", ErrInvalidSetupJournal)
		}
	}
	if journal.Selection == nil && (len(journal.CompletedStages) != 0 || journal.WorkspaceID != "" || journal.IdentityPrincipalID != "" || len(journal.ConnectorBackups) != 0 || journal.LastError != nil) {
		return fmt.Errorf("%w: effects before confirmation", ErrInvalidSetupJournal)
	}
	for index, stage := range journal.CompletedStages {
		if index >= len(orderedSetupStages) || stage != orderedSetupStages[index] {
			return fmt.Errorf("%w: completed stage prefix", ErrInvalidSetupJournal)
		}
	}
	if journal.WorkspaceID != "" && !types.CanonicalUUID(string(journal.WorkspaceID)) {
		return fmt.Errorf("%w: workspace binding", ErrInvalidSetupJournal)
	}
	if (journal.WorkspaceID != "" && len(journal.CompletedStages) < setupStageIndex(StageWorkspaceRegistered)) ||
		(journal.WorkspaceID == "" && len(journal.CompletedStages) > setupStageIndex(StageWorkspaceRegistered)) {
		return fmt.Errorf("%w: workspace binding prefix", ErrInvalidSetupJournal)
	}
	if journal.IdentityPrincipalID != "" && !types.CanonicalUUID(journal.IdentityPrincipalID) {
		return fmt.Errorf("%w: identity binding", ErrInvalidSetupJournal)
	}
	if (journal.IdentityPrincipalID != "" && len(journal.CompletedStages) < setupStageIndex(StageIdentitySelected)) ||
		(journal.IdentityPrincipalID == "" && len(journal.CompletedStages) > setupStageIndex(StageIdentitySelected)) {
		return fmt.Errorf("%w: identity binding prefix", ErrInvalidSetupJournal)
	}
	if len(journal.ConnectorBackups) != 0 && len(journal.CompletedStages) < setupStageIndex(StageConnectorsApplied) {
		return fmt.Errorf("%w: connector backup prefix", ErrInvalidSetupJournal)
	}
	backupAdapters := map[string]bool{}
	for _, reference := range journal.ConnectorBackups {
		adapter, ok := parseBackupReference(reference)
		if !ok || backupAdapters[adapter] || journal.Selection == nil || !containsString(journal.Selection.ConnectorAdapters, adapter) {
			return fmt.Errorf("%w: connector backup", ErrInvalidSetupJournal)
		}
		backupAdapters[adapter] = true
	}
	if journal.LastError != nil {
		if !validSetupStage(journal.LastError.Stage) || journal.LastError.Message != redactedSetupFailureMessage || !validSetupTime(journal.LastError.RecordedAt) || setupStageIndex(journal.LastError.Stage) != len(journal.CompletedStages) {
			return fmt.Errorf("%w: redacted failure", ErrInvalidSetupJournal)
		}
		if journal.LastError.RecordedAt.Before(journal.CreatedAt) || journal.LastError.RecordedAt.After(journal.UpdatedAt) {
			return fmt.Errorf("%w: redacted failure time", ErrInvalidSetupJournal)
		}
	}
	if journal.ReplacesJournalID != "" && (!types.CanonicalUUID(journal.ReplacesJournalID) || journal.ReplacesJournalID == journal.JournalID) {
		return fmt.Errorf("%w: replacement predecessor", ErrInvalidSetupJournal)
	}
	if journal.ReplacedByJournalID != "" && (!types.CanonicalUUID(journal.ReplacedByJournalID) || journal.ReplacedByJournalID == journal.JournalID) {
		return fmt.Errorf("%w: replacement successor", ErrInvalidSetupJournal)
	}
	switch journal.State {
	case SetupJournalActive:
		if journal.ReplacedByJournalID != "" || journal.CompletedAt != nil {
			return fmt.Errorf("%w: active terminal fields", ErrInvalidSetupJournal)
		}
	case SetupJournalCompleted:
		if len(journal.CompletedStages) != len(orderedSetupStages) || journal.CompletedAt == nil || journal.ReplacedByJournalID != "" || journal.LastError != nil {
			return fmt.Errorf("%w: completed terminal fields", ErrInvalidSetupJournal)
		}
	case SetupJournalReplaced:
		if journal.Selection == nil || journal.CompletedAt == nil || journal.ReplacedByJournalID == "" {
			return fmt.Errorf("%w: replaced terminal fields", ErrInvalidSetupJournal)
		}
	default:
		return fmt.Errorf("%w: state", ErrInvalidSetupJournal)
	}
	if journal.CompletedAt != nil && (!validSetupTime(*journal.CompletedAt) || journal.CompletedAt.Before(journal.CreatedAt) || journal.CompletedAt.After(journal.UpdatedAt)) {
		return fmt.Errorf("%w: completion time", ErrInvalidSetupJournal)
	}
	return nil
}

func canonicalSetupProjectRoot(root string) (string, error) {
	if root == "" || len(root) > maxSetupRootBytes || strings.ContainsRune(root, 0) {
		return "", ErrInvalidSetupJournal
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", ErrInvalidSetupJournal
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", ErrInvalidSetupJournal
	}
	canonical := filepath.Clean(resolved)
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() || !validCanonicalSetupRoot(canonical) {
		return "", ErrInvalidSetupJournal
	}
	return canonical, nil
}

func validCanonicalSetupRoot(root string) bool {
	return root != "" && len(root) <= maxSetupRootBytes && filepath.IsAbs(root) && filepath.Clean(root) == root && root != string(filepath.Separator) && !strings.ContainsRune(root, 0)
}

func parseBackupReference(reference BackupReference) (string, bool) {
	parts := strings.Split(string(reference), ":")
	if len(parts) != 4 || parts[0] != "connector-backup" || parts[1] != "v1" || (parts[2] != "codex" && parts[2] != "claude") || !types.CanonicalUUID(parts[3]) {
		return "", false
	}
	return parts[2], true
}

func readSetupJournalRecord(fd int, name string) (SetupJournal, bool, error) {
	data, exists, err := readSetupJournalFile(fd, name)
	if err != nil || !exists {
		return SetupJournal{}, exists, err
	}
	var journal SetupJournal
	if err := strictSetupJSONDecode(data, &journal); err != nil {
		return SetupJournal{}, false, fmt.Errorf("%w: strict JSON", ErrInvalidSetupJournal)
	}
	canonical, err := marshalCanonicalSetupJournal(journal)
	if err != nil {
		return SetupJournal{}, false, fmt.Errorf("%w: encode canonical JSON", ErrInvalidSetupJournal)
	}
	if !bytes.Equal(data, canonical) {
		return SetupJournal{}, false, fmt.Errorf("%w: noncanonical JSON", ErrInvalidSetupJournal)
	}
	if err := validateSetupJournal(journal); err != nil {
		return SetupJournal{}, false, fmt.Errorf("%w: logical record: %v", ErrInvalidSetupJournal, err)
	}
	return journal, true, nil
}

func marshalCanonicalSetupJournal(journal SetupJournal) ([]byte, error) {
	data, err := json.Marshal(journal)
	if err != nil {
		return nil, ErrInvalidSetupJournal
	}
	if len(data)+1 > maxSetupJournalRecordBytes {
		return nil, ErrInvalidSetupJournal
	}
	return append(data, '\n'), nil
}

func strictSetupJSONDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanSetupJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func scanSetupJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	return scanSetupJSONToken(decoder, token)
}

func scanSetupJSONToken(decoder *json.Decoder, token json.Token) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := scanSetupJSONToken(decoder, value); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := scanSetupJSONToken(decoder, value); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func cloneSetupJournal(journal SetupJournal) SetupJournal {
	clone := journal
	clone.CompletedStages = append([]SetupStage{}, journal.CompletedStages...)
	clone.ConnectorBackups = append([]BackupReference{}, journal.ConnectorBackups...)
	if journal.Selection != nil {
		selection := cloneSetupSelectionValue(*journal.Selection)
		clone.Selection = &selection
	}
	if journal.LastError != nil {
		failure := *journal.LastError
		clone.LastError = &failure
	}
	if journal.CompletedAt != nil {
		completed := *journal.CompletedAt
		clone.CompletedAt = &completed
	}
	return clone
}

func cloneSetupSelectionValue(selection SetupSelection) SetupSelection {
	clone := selection
	clone.ConnectorAdapters = append([]string{}, selection.ConnectorAdapters...)
	clone.Changes = append([]ConfirmedChange{}, selection.Changes...)
	return clone
}

func equalSetupSelections(left, right SetupSelection) bool {
	return reflect.DeepEqual(left, right)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validSetupTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func timePointer(value time.Time) *time.Time { return &value }

func setupJournalRecordName(identifier string) string { return "setup-" + identifier + ".json" }

func parseSetupJournalRecordName(name string) string {
	if !strings.HasPrefix(name, "setup-") || !strings.HasSuffix(name, ".json") {
		return ""
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(name, "setup-"), ".json")
	if !types.CanonicalUUID(identifier) {
		return ""
	}
	return identifier
}

func isSetupJournalTemporaryName(name string) bool {
	if !strings.HasPrefix(name, setupJournalTemporaryPrefix) || len(name) != len(setupJournalTemporaryPrefix)+32 {
		return false
	}
	for _, character := range name[len(setupJournalTemporaryPrefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type setupRandomReader struct{ read func([]byte) (int, error) }

func (reader setupRandomReader) Read(data []byte) (int, error) { return reader.read(data) }
