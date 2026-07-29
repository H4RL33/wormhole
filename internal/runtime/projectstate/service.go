package projectstate

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrInvalidRegistration = errors.New("projectstate: invalid workspace registration")

const workspaceRegistrationTimeout = 30 * time.Second

type RegisterWorkspaceRequest struct {
	Root               string
	ExpectedProjectID  string
	ExpectedRepository types.RepositoryIdentity
	ExpectedCommit     string
}

type RegisterWorkspaceResult struct {
	Binding types.WorkspaceBinding
	Created bool
}

type ServiceConfig struct {
	LegacyIntegrationBackupRoot string
}

type WorkspaceStatus struct {
	Binding           types.WorkspaceBinding
	State             string
	AcceptedSnapshot  state.Snapshot
	CandidateDigest   state.Digest
	OverlayGeneration int64
}

type Service struct {
	repo                *localstore.WorkspaceRepo
	legacyBackupRoot    string
	registrationTimeout time.Duration
	readWorkingTree     func(string) (state.Tree, error)
	now                 func() time.Time
}

func NewService(repo *localstore.WorkspaceRepo, config ServiceConfig) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("projectstate: workspace repository is required")
	}
	service := &Service{
		repo: repo, registrationTimeout: workspaceRegistrationTimeout,
		readWorkingTree: ReadWorkingTreeNoFollow, now: time.Now,
	}
	if config.LegacyIntegrationBackupRoot == "" {
		return service, nil
	}
	backupCandidate, err := validatePrivateBackupRootPath(config.LegacyIntegrationBackupRoot)
	if err != nil {
		return nil, err
	}
	bindings, err := repo.RegisteredWorkspaces(context.Background())
	if err != nil {
		return nil, fmt.Errorf("projectstate: validate backup root against workspaces: %w", err)
	}
	for _, binding := range bindings {
		if pathsOverlap(backupCandidate, binding.Checkout.CanonicalPath) {
			return nil, fmt.Errorf("projectstate: legacy backup root overlaps a registered repository")
		}
	}
	contained, err := pathIsRepositoryContained(context.Background(), backupCandidate)
	if err != nil {
		return nil, err
	}
	if contained {
		return nil, fmt.Errorf("projectstate: legacy backup root is contained in a repository")
	}
	backupRoot, err := preparePrivateBackupRoot(backupCandidate)
	if err != nil {
		return nil, err
	}
	service.legacyBackupRoot = backupRoot
	return service, nil
}

func (s *Service) RegisterWorkspace(ctx context.Context, req RegisterWorkspaceRequest) (RegisterWorkspaceResult, error) {
	if s == nil || s.repo == nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("projectstate: service is unavailable")
	}
	registrationCtx, cancel := registrationContext(ctx, s.registrationTimeout)
	defer cancel()
	if !types.CanonicalUUID(req.ExpectedProjectID) || !validCommit(req.ExpectedCommit) {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: invalid project or commit identity", ErrInvalidRegistration)
	}
	if err := req.ExpectedRepository.Validate(); err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: %v", ErrInvalidRegistration, err)
	}
	observed, err := inspectCommittedWorkspace(registrationCtx, req.Root, req.ExpectedCommit)
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: %w", ErrInvalidRegistration, err)
	}
	if s.legacyBackupRoot != "" && pathsOverlap(s.legacyBackupRoot, observed.root) {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: process-private backup root overlaps repository", ErrInvalidRegistration)
	}
	if observed.snapshot.Config.ProjectID != req.ExpectedProjectID {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: committed project identity differs from request", ErrInvalidRegistration)
	}
	if observed.snapshot.Config.Repository != req.ExpectedRepository {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: committed repository identity differs from request", ErrInvalidRegistration)
	}
	canonicalTree, err := state.EncodeTree(observed.snapshot)
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: encode committed snapshot: %v", ErrInvalidRegistration, err)
	}
	revalidated, err := checkoutIdentity(observed.root)
	if err != nil || revalidated != observed.checkout {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: checkout identity changed before persistence", ErrInvalidRegistration)
	}
	workspaceID, err := newWorkspaceID()
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("projectstate: generate workspace ID: %w", err)
	}
	binding := types.WorkspaceBinding{
		Scope:    types.WorkspaceScope{ProjectID: req.ExpectedProjectID, WorkspaceID: workspaceID},
		Checkout: observed.checkout, Repository: req.ExpectedRepository,
		AcceptedRef: observed.acceptedRef, AcceptedCommitSHA: req.ExpectedCommit,
		AcceptedTreeDigest: string(observed.snapshot.Digest),
	}
	persisted, created, err := s.repo.RegisterWorkspace(registrationCtx, binding, canonicalTree)
	if err != nil {
		return RegisterWorkspaceResult{}, err
	}
	return RegisterWorkspaceResult{Binding: persisted, Created: created}, nil
}

func registrationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func (s *Service) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error) {
	if s == nil || s.repo == nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	if err := observed.Validate(); err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	resolved, err := filepath.EvalSymlinks(observed.WorkingDirectory)
	if err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	resolved = filepath.Clean(resolved)
	binding, err := s.repo.ResolveWorkingDirectory(ctx, types.WorkspaceContext{WorkingDirectory: resolved})
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	if err := verifyBindingCheckout(binding); err != nil {
		return types.WorkspaceBinding{}, localstore.ErrNotFound
	}
	return binding, nil
}

func (s *Service) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("projectstate: service is unavailable")
	}
	return s.repo.RegisteredWorkspaces(ctx)
}

func (s *Service) Status(ctx context.Context, scope types.WorkspaceScope) (WorkspaceStatus, error) {
	if s == nil || s.repo == nil {
		return WorkspaceStatus{}, localstore.ErrNotFound
	}
	var status WorkspaceStatus
	err := s.repo.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		status, _, _, err = readComposedWorkspace(ctx, tx)
		return err
	})
	if err != nil {
		return WorkspaceStatus{}, err
	}
	return status, nil
}

func (s *Service) Diff(ctx context.Context, scope types.WorkspaceScope) (Diff, error) {
	if s == nil || s.repo == nil {
		return Diff{}, localstore.ErrNotFound
	}
	var result Diff
	err := s.repo.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		status, view, operations, err := readComposedWorkspace(ctx, tx)
		if err != nil {
			return err
		}
		actors := make(map[state.RecordKey]types.ActorEnvelope, len(operations))
		for _, stored := range operations {
			key := operationTargetKey(stored.Operation)
			if key.Kind == "" || key.ID == "" {
				return fmt.Errorf("projectstate: active operation has no target key")
			}
			actors[key] = stored.Operation.Actor
		}
		result, err = SemanticDiff(status.AcceptedSnapshot, view.Snapshot, actors)
		return err
	})
	if err != nil {
		return Diff{}, err
	}
	return result, nil
}

func (s *Service) Apply(ctx context.Context, scope types.WorkspaceScope, operation state.OperationV1) (WorkspaceStatus, error) {
	return s.ApplyBatch(ctx, scope, []state.OperationV1{operation})
}

func (s *Service) ApplyBatch(ctx context.Context, scope types.WorkspaceScope, operations []state.OperationV1) (WorkspaceStatus, error) {
	if s == nil || s.repo == nil {
		return WorkspaceStatus{}, localstore.ErrNotFound
	}
	if len(operations) == 0 {
		return WorkspaceStatus{}, fmt.Errorf("projectstate: operation batch is empty")
	}
	canonical := make([][]byte, len(operations))
	targets := make([]state.RecordKey, len(operations))
	seen := make(map[string]struct{}, len(operations))
	for index, operation := range operations {
		if err := operation.Actor.ValidateLocalAction(); err != nil {
			return WorkspaceStatus{}, err
		}
		encoded, err := state.CanonicalOperation(operation)
		if err != nil {
			return WorkspaceStatus{}, err
		}
		if _, duplicate := seen[operation.ID]; duplicate {
			return WorkspaceStatus{}, fmt.Errorf("projectstate: duplicate operation ID %s", operation.ID)
		}
		seen[operation.ID] = struct{}{}
		canonical[index] = encoded
		targets[index] = operationTargetKey(operation)
	}

	var result WorkspaceStatus
	err := s.repo.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		status, view, _, err := readComposedWorkspace(ctx, tx)
		if err != nil {
			return err
		}
		workspaceState := "pending"
		openConflicts, err := tx.HasOpenConflicts(ctx)
		if err != nil {
			return err
		}
		if openConflicts {
			targeted, err := tx.HasOpenConflictForKeys(ctx, targets)
			if err != nil {
				return err
			}
			if targeted {
				return localstore.ErrWorkspaceConflicted
			}
			workspaceState = "conflicted"
		}
		nextGeneration, err := tx.NextGeneration(ctx)
		if err != nil {
			return err
		}
		if nextGeneration <= view.ThroughGeneration {
			return fmt.Errorf("projectstate: next operation generation %d does not follow composed generation %d", nextGeneration, view.ThroughGeneration)
		}
		inserts := make([]localstore.WorkspaceOperationInsert, 0, len(operations))
		current := view.Snapshot
		for index, operation := range operations {
			current, err = state.ApplyOperation(current, operation)
			if err != nil {
				return err
			}
			inserts = append(inserts, localstore.WorkspaceOperationInsert{
				Generation: nextGeneration + int64(index), OperationID: operation.ID, OperationJSON: canonical[index],
			})
		}
		if err := tx.InsertActiveOperations(ctx, inserts); err != nil {
			return err
		}
		if err := tx.SetStatus(ctx, workspaceState); err != nil {
			return err
		}
		status.State = workspaceState
		status.CandidateDigest = current.Digest
		status.OverlayGeneration = nextGeneration + int64(len(operations)) - 1
		result = status
		return nil
	})
	if err != nil {
		return WorkspaceStatus{}, err
	}
	return result, nil
}

func operationTargetKey(operation state.OperationV1) state.RecordKey {
	switch operation.Kind {
	case state.OperationPutRecord:
		value := operation.PutRecord.Record
		switch {
		case value.Project != nil:
			return state.RecordKey{Kind: "project", ID: value.Project.ID}
		case value.Actor != nil:
			return state.RecordKey{Kind: "actor", ID: value.Actor.ID}
		case value.Task != nil:
			return state.RecordKey{Kind: "task", ID: value.Task.ID}
		case value.TaskLink != nil:
			return state.RecordKey{Kind: "task_link", ID: value.TaskLink.ID}
		case value.Channel != nil:
			return state.RecordKey{Kind: "channel", ID: value.Channel.ID}
		case value.Event != nil:
			return state.RecordKey{Kind: "event", ID: value.Event.ID}
		case value.GitLink != nil:
			return state.RecordKey{Kind: "git_link", ID: value.GitLink.ID}
		}
	case state.OperationPutKBArticle:
		return state.RecordKey{Kind: "kb_article", ID: operation.PutKBArticle.Record.ID}
	case state.OperationTombstone:
		return operation.Tombstone.Key
	case state.OperationResurrect:
		return operation.Resurrect.Key
	}
	return state.RecordKey{}
}

func readComposedWorkspace(ctx context.Context, tx *localstore.WorkspaceMutationTx) (WorkspaceStatus, ComposedView, []StoredOperation, error) {
	record, err := tx.Workspace(ctx)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	if err := verifyBindingCheckout(record.Binding); err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, localstore.ErrNotFound
	}
	openConflicts, err := tx.HasOpenConflicts(ctx)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	if (record.State == "conflicted") != openConflicts {
		return WorkspaceStatus{}, ComposedView{}, nil, fmt.Errorf("projectstate: workspace conflict state does not match open conflict evidence")
	}
	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	start, boundary := selectCandidateStart(record.Snapshot, candidate)
	rows, err := tx.ActiveOperationsAfter(ctx, boundary)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	operations, err := decodeStoredOperations(rows)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	view, err := Compose(start, boundary, operations)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	return WorkspaceStatus{
		Binding: record.Binding, State: record.State, AcceptedSnapshot: record.Snapshot,
		CandidateDigest: view.Snapshot.Digest, OverlayGeneration: view.ThroughGeneration,
	}, view, operations, nil
}

func selectCandidateStart(accepted state.Snapshot, candidate *localstore.WorkspaceCandidateRecord) (state.Snapshot, int64) {
	if candidate == nil {
		return accepted, 0
	}
	if candidate.RebasedSnapshot == nil {
		return candidate.DirectSnapshot, 0
	}
	return *candidate.RebasedSnapshot, candidate.RebasedThroughGeneration
}

func decodeStoredOperations(rows []localstore.WorkspaceOperation) ([]StoredOperation, error) {
	operations := make([]StoredOperation, 0, len(rows))
	for _, row := range rows {
		if row.Generation <= 0 || row.State != "active" || !types.CanonicalUUID(row.OperationID) {
			return nil, fmt.Errorf("projectstate: invalid active workspace operation metadata")
		}
		operation, err := state.DecodeOperation(row.OperationJSON)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode active workspace operation: %w", err)
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil || operation.ID != row.OperationID || !bytes.Equal(canonical, row.OperationJSON) {
			return nil, fmt.Errorf("projectstate: active workspace operation does not match its row")
		}
		operations = append(operations, StoredOperation{Generation: row.Generation, Operation: operation})
	}
	return operations, nil
}

func verifyBindingCheckout(binding types.WorkspaceBinding) error {
	canonical, err := canonicalNonSymlinkDirectory(binding.Checkout.CanonicalPath)
	if err != nil || canonical != binding.Checkout.CanonicalPath {
		return fmt.Errorf("projectstate: checkout path changed")
	}
	identity, err := checkoutIdentity(canonical)
	if err != nil {
		return err
	}
	if identity != binding.Checkout {
		return fmt.Errorf("projectstate: checkout filesystem identity changed")
	}
	return nil
}

func preparePrivateBackupRoot(value string) (string, error) {
	clean, err := validatePrivateBackupRootPath(value)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(clean); err == nil {
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
			return "", fmt.Errorf("projectstate: existing legacy backup root is not process-private")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("projectstate: inspect legacy backup root: %w", err)
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return "", fmt.Errorf("projectstate: create legacy backup root: %w", err)
	}
	if err := rejectSymlinkComponents(clean); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || filepath.Clean(resolved) != clean {
		return "", fmt.Errorf("projectstate: legacy backup root must not contain symlinks")
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() || !ownedByCurrentUser(info) {
		return "", fmt.Errorf("projectstate: legacy backup root must be a directory")
	}
	if err := os.Chmod(clean, 0o700); err != nil {
		return "", fmt.Errorf("projectstate: protect legacy backup root: %w", err)
	}
	return clean, nil
}

func validatePrivateBackupRootPath(value string) (string, error) {
	if !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("projectstate: legacy backup root must be absolute")
	}
	clean := filepath.Clean(value)
	filesystemRoot := filepath.VolumeName(clean) + string(filepath.Separator)
	if clean == filesystemRoot {
		return "", fmt.Errorf("projectstate: legacy backup root must be process-private")
	}
	if err := rejectSymlinkComponents(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func pathIsRepositoryContained(ctx context.Context, candidate string) (bool, error) {
	existing := candidate
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("projectstate: inspect legacy backup root ancestor: %w", err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	output, err := readOnlyGit(ctx, existing, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		var exitError *gitExitError
		if errors.As(err, &exitError) && strings.Contains(exitError.stderr, "not a git repository") {
			return false, nil
		}
		return false, fmt.Errorf("projectstate: determine legacy backup repository boundary: %w", err)
	}
	repositoryRoot := filepath.Clean(trimGitLine(output))
	return pathContains(repositoryRoot, candidate), nil
}

func rejectSymlinkComponents(absolute string) error {
	volume := filepath.VolumeName(absolute)
	remainder := strings.TrimPrefix(absolute, volume)
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(remainder, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("projectstate: inspect legacy backup root: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("projectstate: legacy backup root contains a symlink")
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(root, candidate string) bool {
	return root == candidate || strings.HasPrefix(candidate, root+string(filepath.Separator))
}

func validCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func newWorkspaceID() (types.WorkspaceID, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return types.WorkspaceID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])), nil
}
