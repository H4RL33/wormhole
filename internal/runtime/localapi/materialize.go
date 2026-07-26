package localapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	integrationStateTarget = ".wormhole/integration-state.json"
	managedBeginMarker     = "<!-- wormhole:managed-begin integration-manifest/v1 -->"
	managedEndMarker       = "<!-- wormhole:managed-end integration-manifest/v1 -->"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	slugPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

	ErrIntegrationDrift       = errors.New("integration materialization drift")
	ErrUnsafeIntegrationPath  = errors.New("unsafe integration materialization path")
	ErrIntegrationUnsupported = errors.New("integration materialization unsupported")
)

type IntegrationOperation string

const (
	IntegrationApply    IntegrationOperation = "apply"
	IntegrationUpdate   IntegrationOperation = "update"
	IntegrationRemove   IntegrationOperation = "remove"
	IntegrationRollback IntegrationOperation = "rollback"
)

type IntegrationManifest struct {
	SchemaVersion      int                        `json:"schema_version"`
	ManifestID         string                     `json:"manifest_id"`
	ManifestVersion    int64                      `json:"manifest_version"`
	ProjectID          string                     `json:"project_id"`
	Source             string                     `json:"source"`
	CreatedAt          string                     `json:"created_at"`
	ToolContractDigest string                     `json:"tool_contract_digest"`
	ManifestDigest     string                     `json:"manifest_digest"`
	RoleFilters        []string                   `json:"role_filters"`
	Entries            []IntegrationManifestEntry `json:"entries"`
}

type IntegrationManifestEntry struct {
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	Content       string   `json:"content"`
	ContentDigest string   `json:"content_digest"`
	MergePolicy   string   `json:"merge_policy"`
	Required      bool     `json:"required"`
	RoleFilters   []string `json:"role_filters"`
}

type IntegrationFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type IntegrationTargetState struct {
	Kind               string                  `json:"kind"`
	Target             string                  `json:"target"`
	ContentDigest      string                  `json:"content_digest"`
	RenderedDigest     string                  `json:"rendered_digest"`
	OwnershipMode      string                  `json:"ownership_mode"`
	InsertionSeparator string                  `json:"insertion_separator,omitempty"`
	CreatedDirectories []string                `json:"created_directories,omitempty"`
	CreatedTarget      bool                    `json:"created_target"`
	FileMode           uint32                  `json:"file_mode"`
	FileIdentity       IntegrationFileIdentity `json:"file_identity"`
}

type IntegrationState struct {
	SchemaVersion                    int                      `json:"schema_version"`
	ProjectID                        string                   `json:"project_id"`
	ActiveManifestID                 *string                  `json:"active_manifest_id"`
	ActiveManifestVersion            *int64                   `json:"active_manifest_version"`
	ActiveManifestDigest             *string                  `json:"active_manifest_digest"`
	PendingManifestID                *string                  `json:"pending_manifest_id"`
	PendingManifestVersion           *int64                   `json:"pending_manifest_version"`
	PendingManifestDigest            *string                  `json:"pending_manifest_digest"`
	ResolvedRole                     string                   `json:"resolved_role"`
	ApprovalState                    string                   `json:"approval_state"`
	MaterializationState             string                   `json:"materialization_state"`
	ConnectionState                  string                   `json:"connection_state"`
	GuidanceActive                   bool                     `json:"guidance_active"`
	CompatibilityState               string                   `json:"compatibility_state"`
	DriftDetected                    bool                     `json:"drift_detected"`
	RollbackCandidateManifestVersion *int64                   `json:"rollback_candidate_manifest_version"`
	RollbackCandidateManifestDigest  *string                  `json:"rollback_candidate_manifest_digest"`
	LastVerifiedAt                   string                   `json:"last_verified_at"`
	PreservedTargets                 []string                 `json:"preserved_targets"`
	Targets                          []IntegrationTargetState `json:"targets"`
}

type IntegrationMaterializationRequest struct {
	Operation    IntegrationOperation
	Manifest     *IntegrationManifest
	State        *IntegrationState
	ProjectID    string
	ResolvedRole string
	Revoked      bool
	Offline      bool
	VerifiedAt   time.Time
}

type IntegrationMaterializationChange struct {
	Target string
	Before []byte
	After  []byte
	Mode   fs.FileMode
	State  IntegrationTargetState
	Remove bool
}

type IntegrationMaterializationPreview struct {
	Operation      IntegrationOperation
	ProjectID      string
	ResolvedRole   string
	ExpectedDigest string
	Diff           string
	Changes        []IntegrationMaterializationChange
	Preserved      []string
}

type preparedMaterializationChange struct {
	change IntegrationMaterializationChange
	parent *materializationParentHandle
}

type IntegrationMaterializer struct {
	root                              string
	testBeforeMaterializationChange   func(int, IntegrationMaterializationChange) error
	testBeforeMaterializationUnlink   func() error
	testAfterMaterializationMutation  func() error
	testAfterIntegrationStateMutation func() error
}

func NewIntegrationMaterializer(root string) (*IntegrationMaterializer, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve integration root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect integration root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: project root must be a non-symlink directory", ErrUnsafeIntegrationPath)
	}
	return &IntegrationMaterializer{root: absolute}, nil
}

func (m *IntegrationMaterializer) Preview(request IntegrationMaterializationRequest) (IntegrationMaterializationPreview, error) {
	if err := validateMaterializationRequest(request); err != nil {
		return IntegrationMaterializationPreview{}, err
	}
	preview := IntegrationMaterializationPreview{
		Operation: request.Operation, ProjectID: request.ProjectID, ResolvedRole: request.ResolvedRole,
	}
	if request.Manifest != nil {
		preview.ExpectedDigest = request.Manifest.ManifestDigest
	} else if request.State != nil && request.State.ActiveManifestDigest != nil {
		preview.ExpectedDigest = *request.State.ActiveManifestDigest
	}
	tracked := make(map[string]IntegrationTargetState)
	if request.State != nil {
		for _, target := range request.State.Targets {
			tracked[target.Target] = target
		}
	}

	var err error
	switch request.Operation {
	case IntegrationApply, IntegrationUpdate, IntegrationRollback:
		preview.Changes, err = m.previewInstall(request, tracked)
	case IntegrationRemove:
		preview.Changes, preview.Preserved, err = m.previewRemove(tracked)
	default:
		err = fmt.Errorf("unknown integration operation %q", request.Operation)
	}
	if err != nil {
		return IntegrationMaterializationPreview{}, err
	}
	sort.Slice(preview.Changes, func(i, j int) bool { return preview.Changes[i].Target < preview.Changes[j].Target })
	for _, change := range preview.Changes {
		preview.Diff += wholeFileUnifiedDiff(change.Target, change.Before, change.After)
	}
	return preview, nil
}

// Apply commits a previously approved, digest-bound request. Candidate
// verification, approval persistence, and the durable authoritative journal
// are deliberately supplied by the Gateway cache owner; this repository layer
// owns only contained filesystem mutation and its inspection projection.
func (m *IntegrationMaterializer) Apply(request IntegrationMaterializationRequest) (IntegrationState, error) {
	preview, err := m.Preview(request)
	if err != nil {
		return IntegrationState{}, err
	}
	root, err := openMaterializationRoot(m.root)
	if err != nil {
		return IntegrationState{}, err
	}
	defer root.close()

	prepared := make([]preparedMaterializationChange, 0, len(preview.Changes))
	for _, change := range preview.Changes {
		parent, openErr := root.openParent(change.Target, false)
		if openErr != nil {
			closePreparedMaterializationParents(prepared)
			root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
			return IntegrationState{}, openErr
		}
		current, _, _, exists, readErr := parent.read()
		if readErr != nil {
			parent.close()
			closePreparedMaterializationParents(prepared)
			return IntegrationState{}, readErr
		}
		if exists != (change.Before != nil) || !bytes.Equal(current, change.Before) {
			parent.close()
			closePreparedMaterializationParents(prepared)
			return IntegrationState{}, fmt.Errorf("%w: target %q changed after preview", ErrIntegrationDrift, change.Target)
		}
		prepared = append(prepared, preparedMaterializationChange{change: change, parent: parent})
	}
	for index := range prepared {
		if prepared[index].change.Remove || !prepared[index].parent.missing {
			continue
		}
		prepared[index].parent.close()
		parent, openErr := root.openParent(prepared[index].change.Target, true)
		if openErr != nil {
			closePreparedMaterializationParents(prepared)
			root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
			return IntegrationState{}, openErr
		}
		prepared[index].parent = parent
		prepared[index].change.State.CreatedDirectories = append([]string(nil), parent.created...)
	}
	defer closePreparedMaterializationParents(prepared)

	applied := make([]preparedMaterializationChange, 0, len(prepared))
	for index := range prepared {
		item := &prepared[index]
		if m.testBeforeMaterializationChange != nil {
			if hookErr := m.testBeforeMaterializationChange(index, item.change); hookErr != nil {
				rollbackMaterializationChanges(applied)
				root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
				return IntegrationState{}, hookErr
			}
		}
		applied = append(applied, *item)
		if item.change.Remove {
			err = item.parent.unlink(item.change.Before, m.testBeforeMaterializationUnlink)
		} else {
			var identity IntegrationFileIdentity
			identity, err = item.parent.atomicWrite(item.change.Before, item.change.Before != nil, item.change.After, item.change.Mode, m.testAfterMaterializationMutation)
			item.change.State.FileIdentity = identity
			applied[len(applied)-1].change.State.FileIdentity = identity
		}
		if err != nil {
			if request.Operation == IntegrationRemove && errors.Is(err, ErrIntegrationDrift) {
				applied = applied[:len(applied)-1]
				preview.Preserved = append(preview.Preserved, item.change.Target)
				sort.Strings(preview.Preserved)
				err = nil
				continue
			}
			rollbackMaterializationChanges(applied)
			root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
			return IntegrationState{}, fmt.Errorf("materialize %q: %w", item.change.Target, err)
		}
	}

	next := nextIntegrationState(request, preview, prepared)
	projection, err := marshalIntegrationState(next)
	if err != nil {
		rollbackMaterializationChanges(applied)
		root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
		return IntegrationState{}, err
	}
	stateParent, err := root.openParent(integrationStateTarget, true)
	if err != nil {
		rollbackMaterializationChanges(applied)
		root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
		return IntegrationState{}, err
	}
	defer stateParent.close()
	stateBefore, stateMode, _, stateExists, err := stateParent.read()
	if err != nil {
		rollbackMaterializationChanges(applied)
		root.removeCreatedDirectories(append(createdMaterializationDirectories(prepared), stateParent.created...))
		return IntegrationState{}, err
	}
	if _, err = stateParent.atomicWrite(stateBefore, stateExists, projection, 0o600, m.testAfterIntegrationStateMutation); err != nil {
		rollbackMaterializationFile(stateParent, stateBefore, stateExists, projection, stateMode.Perm())
		rollbackMaterializationChanges(applied)
		root.removeCreatedDirectories(append(createdMaterializationDirectories(prepared), stateParent.created...))
		return IntegrationState{}, fmt.Errorf("write integration state projection: %w", err)
	}
	if request.Operation == IntegrationRemove {
		root.removeCreatedDirectories(createdMaterializationDirectories(prepared))
	}
	return next, materializationResultError(next)
}

// recoverRollback restores only bytes whose current value still matches an
// interrupted journal's intended value. It uses the same held-root,
// descriptor-relative operations as normal materialization and fails closed
// on any divergent user change.
func (m *IntegrationMaterializer) recoverRollback(preview IntegrationMaterializationPreview, authoritative IntegrationState) error {
	root, err := openMaterializationRoot(m.root)
	if err != nil {
		return err
	}
	defer root.close()
	matches := func(current []byte, exists bool, expected []byte) bool {
		if expected == nil {
			return !exists
		}
		return exists && bytes.Equal(current, expected)
	}
	for index := len(preview.Changes) - 1; index >= 0; index-- {
		change := preview.Changes[index]
		parent, openErr := root.openParent(change.Target, false)
		if openErr != nil {
			return openErr
		}
		current, mode, _, exists, readErr := parent.read()
		if readErr != nil {
			parent.close()
			return readErr
		}
		if matches(current, exists, change.Before) {
			parent.close()
			continue
		}
		if !matches(current, exists, change.After) {
			parent.close()
			return fmt.Errorf("%w: recovery target %q diverged", ErrIntegrationDrift, change.Target)
		}
		if change.Before == nil {
			err = parent.unlink(change.After, nil)
		} else {
			restoreMode := change.Mode
			if restoreMode == 0 {
				restoreMode = mode.Perm()
			}
			_, err = parent.atomicWrite(current, exists, change.Before, restoreMode, nil)
		}
		parent.close()
		if err != nil {
			return fmt.Errorf("recover integration target %q: %w", change.Target, err)
		}
	}
	projection, err := marshalIntegrationState(authoritative)
	if err != nil {
		return err
	}
	stateParent, err := root.openParent(integrationStateTarget, true)
	if err != nil {
		return err
	}
	defer stateParent.close()
	current, _, _, exists, err := stateParent.read()
	if err != nil {
		return err
	}
	if _, err := stateParent.atomicWrite(current, exists, projection, 0o600, nil); err != nil {
		return fmt.Errorf("recover integration state projection: %w", err)
	}
	return nil
}

func closePreparedMaterializationParents(prepared []preparedMaterializationChange) {
	for index := len(prepared) - 1; index >= 0; index-- {
		prepared[index].parent.close()
	}
}

func rollbackMaterializationChanges(applied []preparedMaterializationChange) {
	for index := len(applied) - 1; index >= 0; index-- {
		item := applied[index]
		rollbackMaterializationFile(item.parent, item.change.Before, item.change.Before != nil, item.change.After, item.change.Mode)
	}
}

func rollbackMaterializationFile(parent *materializationParentHandle, before []byte, beforeExists bool, after []byte, mode fs.FileMode) {
	current, _, _, exists, err := parent.read()
	if err != nil || exists == beforeExists && bytes.Equal(current, before) {
		return
	}
	if !beforeExists {
		if exists && bytes.Equal(current, after) {
			_ = parent.unlink(after, nil)
		}
		return
	}
	if !exists || bytes.Equal(current, after) {
		_, _ = parent.atomicWrite(current, exists, before, mode, nil)
	}
}

func createdMaterializationDirectories(prepared []preparedMaterializationChange) []string {
	var directories []string
	for _, item := range prepared {
		directories = append(directories, item.change.State.CreatedDirectories...)
	}
	return directories
}

func nextIntegrationState(request IntegrationMaterializationRequest, preview IntegrationMaterializationPreview, prepared []preparedMaterializationChange) IntegrationState {
	next := IntegrationState{
		SchemaVersion: 1, ProjectID: request.ProjectID, ResolvedRole: request.ResolvedRole,
		ApprovalState: "approved", MaterializationState: "applied", ConnectionState: "online",
		GuidanceActive: true, CompatibilityState: "compatible",
		PreservedTargets: append([]string(nil), preview.Preserved...), Targets: []IntegrationTargetState{},
	}
	if request.State != nil {
		next.RollbackCandidateManifestVersion = cloneInt64Pointer(request.State.RollbackCandidateManifestVersion)
		next.RollbackCandidateManifestDigest = cloneStringPointer(request.State.RollbackCandidateManifestDigest)
		if request.State.CompatibilityState != "" {
			next.CompatibilityState = request.State.CompatibilityState
		}
	}
	if request.Offline {
		next.ConnectionState = "offline"
	}
	if !request.VerifiedAt.IsZero() {
		next.LastVerifiedAt = request.VerifiedAt.UTC().Format(time.RFC3339Nano)
	} else if request.State != nil {
		next.LastVerifiedAt = request.State.LastVerifiedAt
	}
	if request.Operation == IntegrationRemove {
		next.GuidanceActive = false
		if request.Revoked {
			next.ApprovalState = "revoked"
		} else {
			next.ApprovalState = "none"
		}
		next.MaterializationState = "not_applied"
		if len(preview.Preserved) > 0 {
			next.MaterializationState = "removal_required"
			next.ConnectionState = "attention_required"
			next.DriftDetected = true
			if request.State != nil {
				next.ActiveManifestID = cloneStringPointer(request.State.ActiveManifestID)
				next.ActiveManifestVersion = cloneInt64Pointer(request.State.ActiveManifestVersion)
				next.ActiveManifestDigest = cloneStringPointer(request.State.ActiveManifestDigest)
				preserved := make(map[string]struct{}, len(preview.Preserved))
				for _, target := range preview.Preserved {
					preserved[target] = struct{}{}
				}
				for _, target := range request.State.Targets {
					if _, ok := preserved[target.Target]; ok {
						next.Targets = append(next.Targets, target)
					}
				}
			}
		}
		sort.Slice(next.Targets, func(i, j int) bool { return next.Targets[i].Target < next.Targets[j].Target })
		return next
	}

	manifest := request.Manifest
	next.ActiveManifestID = stringPointer(manifest.ManifestID)
	next.ActiveManifestVersion = int64Pointer(manifest.ManifestVersion)
	next.ActiveManifestDigest = stringPointer(manifest.ManifestDigest)
	changed := make(map[string]IntegrationTargetState, len(prepared))
	for _, item := range prepared {
		if !item.change.Remove {
			changed[item.change.Target] = item.change.State
		}
	}
	tracked := make(map[string]IntegrationTargetState)
	if request.State != nil {
		for _, target := range request.State.Targets {
			tracked[target.Target] = target
		}
	}
	for _, entry := range manifest.Entries {
		if !matchesRole(entry.RoleFilters, request.ResolvedRole) {
			continue
		}
		if state, ok := changed[entry.Target]; ok {
			next.Targets = append(next.Targets, state)
		} else if state, ok := tracked[entry.Target]; ok {
			state.ContentDigest = entry.ContentDigest
			next.Targets = append(next.Targets, state)
		}
	}
	sort.Slice(next.Targets, func(i, j int) bool { return next.Targets[i].Target < next.Targets[j].Target })
	return next
}

func materializationResultError(state IntegrationState) error {
	if state.MaterializationState == "removal_required" {
		return fmt.Errorf("%w: preserved targets: %s", ErrIntegrationDrift, strings.Join(state.PreservedTargets, ", "))
	}
	return nil
}

func stringPointer(value string) *string { return &value }
func int64Pointer(value int64) *int64    { return &value }

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return int64Pointer(*value)
}

func validateMaterializationRequest(request IntegrationMaterializationRequest) error {
	if request.ProjectID == "" {
		return errors.New("integration project is required")
	}
	if request.ResolvedRole == "" || !validSlug(request.ResolvedRole) {
		return errors.New("integration resolved role must be one valid slug")
	}
	if request.Operation == IntegrationRemove {
		if request.State == nil {
			return errors.New("integration remove requires tracked state")
		}
		if request.State.ProjectID != request.ProjectID {
			return errors.New("integration state project does not match request")
		}
		return nil
	}
	if request.Manifest == nil {
		return fmt.Errorf("integration %s requires a manifest", request.Operation)
	}
	if request.Manifest.ProjectID != request.ProjectID {
		return errors.New("integration manifest project does not match request")
	}
	return validateMaterializationManifest(*request.Manifest, request.ResolvedRole)
}

func validateMaterializationManifest(manifest IntegrationManifest, role string) error {
	if manifest.SchemaVersion != 1 || manifest.ManifestID == "" || manifest.ManifestVersion < 1 {
		return errors.New("invalid integration manifest identity or version")
	}
	if !digestPattern.MatchString(manifest.ManifestDigest) {
		return errors.New("invalid integration manifest digest")
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries) > 64 {
		return errors.New("integration manifest must contain 1 through 64 entries")
	}
	seen := make(map[string]struct{}, len(manifest.Entries))
	total := 0
	for _, entry := range manifest.Entries {
		if _, duplicate := seen[entry.Target]; duplicate {
			return fmt.Errorf("duplicate integration target %q", entry.Target)
		}
		seen[entry.Target] = struct{}{}
		if err := validateIntegrationEntry(entry); err != nil {
			return err
		}
		total += len(entry.Content)
	}
	if total > 1<<20 {
		return errors.New("integration manifest content exceeds 1048576 bytes")
	}
	if !matchesRole(manifest.RoleFilters, role) {
		return errors.New("integration manifest does not apply to resolved role")
	}
	return nil
}

func validateIntegrationEntry(entry IntegrationManifestEntry) error {
	if !utf8.ValidString(entry.Content) || len(entry.Content) > 262144 || strings.ContainsAny(entry.Content, "\x00\r") ||
		!strings.HasSuffix(entry.Content, "\n") || strings.HasSuffix(entry.Content, "\n\n") || strings.Trim(entry.Content, "\n") == "" {
		return fmt.Errorf("invalid integration content for %q", entry.Target)
	}
	if strings.Contains(entry.Content, "<!-- wormhole:") {
		return fmt.Errorf("integration content for %q contains a managed marker", entry.Target)
	}
	if entry.ContentDigest != materializationSHA256([]byte(entry.Content)) {
		return fmt.Errorf("integration content digest mismatch for %q", entry.Target)
	}
	if err := validateMaterializationTarget(entry); err != nil {
		return err
	}
	if err := validateSortedRoles(entry.RoleFilters); err != nil {
		return fmt.Errorf("integration target %q role filters: %w", entry.Target, err)
	}
	return nil
}

func validateMaterializationTarget(entry IntegrationManifestEntry) error {
	if entry.Target == "AGENTS.md" {
		if entry.Kind != "agents_bootstrap" || entry.MergePolicy != "managed_section" {
			return fmt.Errorf("invalid kind or merge policy for %q", entry.Target)
		}
		return nil
	}
	if strings.Contains(entry.Target, "\\") || strings.Contains(entry.Target, "%") || filepath.IsAbs(entry.Target) {
		return fmt.Errorf("%w: invalid target %q", ErrUnsafeIntegrationPath, entry.Target)
	}
	parts := strings.Split(entry.Target, "/")
	if len(parts) < 4 || parts[0] != ".agents" || parts[1] != "skills" || parts[2] == "" ||
		!strings.HasPrefix(parts[2], "wormhole-") || !validSlug(strings.TrimPrefix(parts[2], "wormhole-")) {
		return fmt.Errorf("%w: invalid target %q", ErrUnsafeIntegrationPath, entry.Target)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: invalid target %q", ErrUnsafeIntegrationPath, entry.Target)
		}
	}
	if entry.Kind == "skill" && entry.MergePolicy == "managed_file" && len(parts) == 4 && parts[3] == "SKILL.md" {
		return nil
	}
	if entry.Kind == "reference" && entry.MergePolicy == "managed_file" && len(parts) == 5 && parts[3] == "references" &&
		strings.HasSuffix(parts[4], ".md") && validSlug(strings.TrimSuffix(parts[4], ".md")) {
		return nil
	}
	return fmt.Errorf("%w: invalid kind, policy, or target %q", ErrUnsafeIntegrationPath, entry.Target)
}

func validSlug(value string) bool {
	return len(value) >= 1 && len(value) <= 63 && slugPattern.MatchString(value)
}

func validateSortedRoles(roles []string) error {
	if len(roles) > 64 {
		return errors.New("too many roles")
	}
	for index, role := range roles {
		if !validSlug(role) || (index > 0 && roles[index-1] >= role) {
			return errors.New("roles must be valid, unique, and bytewise sorted")
		}
	}
	return nil
}

func matchesRole(filters []string, role string) bool {
	return len(filters) == 0 || sort.SearchStrings(filters, role) < len(filters) && filters[sort.SearchStrings(filters, role)] == role
}

func (m *IntegrationMaterializer) previewInstall(request IntegrationMaterializationRequest, tracked map[string]IntegrationTargetState) ([]IntegrationMaterializationChange, error) {
	entries := make([]IntegrationManifestEntry, 0, len(request.Manifest.Entries))
	for _, entry := range request.Manifest.Entries {
		if matchesRole(entry.RoleFilters, request.ResolvedRole) {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("integration manifest selects no entries for resolved role")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Target < entries[j].Target })
	changes := make([]IntegrationMaterializationChange, 0, len(entries))
	selected := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		selected[entry.Target] = struct{}{}
		before, mode, identity, exists, err := m.readTarget(entry.Target)
		if err != nil {
			return nil, err
		}
		targetState, managed := tracked[entry.Target]
		if managed && targetState.FileIdentity != (IntegrationFileIdentity{}) && targetState.FileIdentity != identity {
			return nil, fmt.Errorf("%w: managed target %q identity changed", ErrIntegrationDrift, entry.Target)
		}
		createdTarget := !exists
		createdDirectories := []string(nil)
		if managed {
			createdTarget = targetState.CreatedTarget
			createdDirectories = append(createdDirectories, targetState.CreatedDirectories...)
		}
		change := IntegrationMaterializationChange{Target: entry.Target, Before: before, Mode: 0o644}
		if entry.Kind == "agents_bootstrap" {
			after, separator, err := renderManagedAgents(before, exists, targetState, managed, *request.Manifest, entry)
			if err != nil {
				return nil, err
			}
			change.After = after
			if exists {
				change.Mode = mode.Perm()
			}
			change.State = IntegrationTargetState{
				Kind: entry.Kind, Target: entry.Target, ContentDigest: entry.ContentDigest,
				RenderedDigest: materializationManagedBlockDigest(after), OwnershipMode: "managed_section",
				InsertionSeparator: separator, CreatedDirectories: createdDirectories, CreatedTarget: createdTarget, FileMode: uint32(change.Mode.Perm()),
			}
		} else {
			if exists && !managed {
				return nil, fmt.Errorf("%w: refusing to adopt pre-existing target %q", ErrIntegrationDrift, entry.Target)
			}
			if exists && (targetState.RenderedDigest != materializationSHA256(before) || targetState.OwnershipMode != "managed_file") {
				return nil, fmt.Errorf("%w: managed target %q changed", ErrIntegrationDrift, entry.Target)
			}
			change.After = []byte(entry.Content)
			change.State = IntegrationTargetState{
				Kind: entry.Kind, Target: entry.Target, ContentDigest: entry.ContentDigest,
				RenderedDigest: materializationSHA256(change.After), OwnershipMode: "managed_file",
				CreatedDirectories: createdDirectories, CreatedTarget: createdTarget, FileMode: 0o644,
			}
		}
		if !bytes.Equal(change.Before, change.After) {
			changes = append(changes, change)
		}
	}
	removed := make(map[string]IntegrationTargetState)
	for target, state := range tracked {
		if _, remains := selected[target]; !remains {
			removed[target] = state
		}
	}
	if len(removed) > 0 {
		removeChanges, preserved, err := m.previewRemove(removed)
		if err != nil {
			return nil, err
		}
		if len(preserved) > 0 {
			return nil, fmt.Errorf("%w: obsolete managed targets changed: %s", ErrIntegrationDrift, strings.Join(preserved, ", "))
		}
		changes = append(changes, removeChanges...)
	}
	return changes, nil
}

func renderManagedAgents(before []byte, exists bool, tracked IntegrationTargetState, managed bool, manifest IntegrationManifest, entry IntegrationManifestEntry) ([]byte, string, error) {
	block := []byte(managedBeginMarker + "\n" +
		fmt.Sprintf("<!-- wormhole:manifest id=%s version=%d digest=%s -->\n", manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest) +
		entry.Content + managedEndMarker + "\n")
	beginCount := bytes.Count(before, []byte(managedBeginMarker))
	endCount := bytes.Count(before, []byte(managedEndMarker))
	prefixCount := bytes.Count(before, []byte("<!-- wormhole:"))
	if !managed {
		if beginCount != 0 || endCount != 0 || prefixCount != 0 {
			return nil, "", fmt.Errorf("%w: untracked AGENTS.md contains Wormhole markers", ErrIntegrationDrift)
		}
		if !exists || len(before) == 0 {
			return block, "", nil
		}
		separator := "\n\n"
		if bytes.HasSuffix(before, []byte("\n")) {
			separator = "\n"
		}
		return append(append(bytes.Clone(before), separator...), block...), separator, nil
	}
	if beginCount != 1 || endCount != 1 || prefixCount != 3 || tracked.OwnershipMode != "managed_section" ||
		tracked.RenderedDigest != materializationManagedBlockDigest(before) {
		return nil, "", fmt.Errorf("%w: AGENTS.md managed section changed", ErrIntegrationDrift)
	}
	begin := bytes.Index(before, []byte(managedBeginMarker))
	end := bytes.Index(before, []byte(managedEndMarker))
	if begin < 0 || end < begin {
		return nil, "", fmt.Errorf("%w: AGENTS.md marker order changed", ErrIntegrationDrift)
	}
	end += len(managedEndMarker)
	if end < len(before) && before[end] == '\n' {
		end++
	}
	after := make([]byte, 0, len(before)-end+begin+len(block))
	after = append(after, before[:begin]...)
	after = append(after, block...)
	after = append(after, before[end:]...)
	return after, tracked.InsertionSeparator, nil
}

func (m *IntegrationMaterializer) previewRemove(tracked map[string]IntegrationTargetState) ([]IntegrationMaterializationChange, []string, error) {
	targets := make([]string, 0, len(tracked))
	for target := range tracked {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	changes := make([]IntegrationMaterializationChange, 0, len(targets))
	var preserved []string
	for _, target := range targets {
		state := tracked[target]
		before, mode, identity, exists, err := m.readTarget(target)
		if err != nil {
			preserved = append(preserved, target)
			continue
		}
		if !exists {
			preserved = append(preserved, target)
			continue
		}
		if state.FileIdentity != (IntegrationFileIdentity{}) && state.FileIdentity != identity {
			preserved = append(preserved, target)
			continue
		}
		change := IntegrationMaterializationChange{Target: target, Before: before, Mode: mode.Perm(), State: state}
		if state.OwnershipMode == "managed_file" {
			if state.RenderedDigest != materializationSHA256(before) {
				preserved = append(preserved, target)
				continue
			}
			change.Remove = true
		} else if state.OwnershipMode == "managed_section" {
			begin := bytes.Index(before, []byte(managedBeginMarker))
			end := bytes.Index(before, []byte(managedEndMarker))
			if begin < 0 || end < begin || bytes.Count(before, []byte("<!-- wormhole:")) != 3 || state.RenderedDigest != materializationManagedBlockDigest(before) {
				preserved = append(preserved, target)
				continue
			}
			end += len(managedEndMarker)
			if end < len(before) && before[end] == '\n' {
				end++
			}
			separatorStart := begin - len(state.InsertionSeparator)
			if separatorStart < 0 || string(before[separatorStart:begin]) != state.InsertionSeparator {
				separatorStart = begin
			}
			change.After = append(bytes.Clone(before[:separatorStart]), before[end:]...)
			if len(change.After) == 0 && state.CreatedTarget {
				change.Remove = true
			}
		} else {
			preserved = append(preserved, target)
			continue
		}
		changes = append(changes, change)
	}
	return changes, preserved, nil
}

func (m *IntegrationMaterializer) readTarget(target string) ([]byte, fs.FileMode, IntegrationFileIdentity, bool, error) {
	entry := IntegrationManifestEntry{Kind: "skill", Target: target, MergePolicy: "managed_file"}
	if target == "AGENTS.md" {
		entry.Kind, entry.MergePolicy = "agents_bootstrap", "managed_section"
	} else if strings.Contains(target, "/references/") {
		entry.Kind = "reference"
	}
	if err := validateMaterializationTarget(entry); err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	root, err := openMaterializationRoot(m.root)
	if err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	defer root.close()
	parent, err := root.openParent(target, false)
	if err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	defer parent.close()
	return parent.read()
}

func materializationManagedBlockDigest(data []byte) string {
	begin := bytes.Index(data, []byte(managedBeginMarker))
	end := bytes.Index(data, []byte(managedEndMarker))
	if begin < 0 || end < begin {
		return ""
	}
	end += len(managedEndMarker)
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return materializationSHA256(data[begin:end])
}

func wholeFileUnifiedDiff(target string, before, after []byte) string {
	if bytes.Equal(before, after) {
		return ""
	}
	oldLines := splitMaterializationLines(before)
	newLines := splitMaterializationLines(after)
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n@@ -%s +%s @@\n", target, target, diffRange(len(oldLines)), diffRange(len(newLines)))
	for _, line := range oldLines {
		out.WriteByte('-')
		out.WriteString(line)
		out.WriteByte('\n')
	}
	for _, line := range newLines {
		out.WriteByte('+')
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func splitMaterializationLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := string(data)
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
	}
	return strings.Split(text, "\n")
}

func diffRange(lines int) string {
	if lines == 0 {
		return "0,0"
	}
	return fmt.Sprintf("1,%d", lines)
}

func materializationSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func marshalIntegrationState(state IntegrationState) ([]byte, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
