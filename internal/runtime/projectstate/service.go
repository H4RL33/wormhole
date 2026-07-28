package projectstate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrInvalidRegistration = errors.New("projectstate: invalid workspace registration")

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
	Binding          types.WorkspaceBinding
	State            string
	AcceptedSnapshot state.Snapshot
}

type Service struct {
	repo             *localstore.WorkspaceRepo
	legacyBackupRoot string
}

func NewService(repo *localstore.WorkspaceRepo, config ServiceConfig) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("projectstate: workspace repository is required")
	}
	service := &Service{repo: repo}
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
	if !types.CanonicalUUID(req.ExpectedProjectID) || !validCommit(req.ExpectedCommit) {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: invalid project or commit identity", ErrInvalidRegistration)
	}
	if err := req.ExpectedRepository.Validate(); err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: %v", ErrInvalidRegistration, err)
	}
	observed, err := inspectCommittedWorkspace(ctx, req.Root, req.ExpectedCommit)
	if err != nil {
		return RegisterWorkspaceResult{}, fmt.Errorf("%w: %v", ErrInvalidRegistration, err)
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
	persisted, created, err := s.repo.RegisterWorkspace(ctx, binding, canonicalTree)
	if err != nil {
		return RegisterWorkspaceResult{}, err
	}
	return RegisterWorkspaceResult{Binding: persisted, Created: created}, nil
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
	record, err := s.repo.Workspace(ctx, scope)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if err := verifyBindingCheckout(record.Binding); err != nil {
		return WorkspaceStatus{}, localstore.ErrNotFound
	}
	return WorkspaceStatus{Binding: record.Binding, State: record.State, AcceptedSnapshot: record.Snapshot}, nil
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
