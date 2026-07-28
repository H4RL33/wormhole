package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrCheckoutCollision = errors.New("localstore: checkout collision")

// WorkspaceRepo persists immutable workspace bindings and their scoped local
// state in the Gateway control database.
type WorkspaceRepo struct {
	db *sql.DB
}

type WorkspaceRecord struct {
	Binding  types.WorkspaceBinding
	Snapshot projectstate.Snapshot
	State    string
}

type WorkspaceOperation struct {
	Generation    int64
	OperationID   string
	OperationJSON json.RawMessage
	State         string
}

func NewWorkspaceRepo(db *sql.DB) *WorkspaceRepo {
	return &WorkspaceRepo{db: db}
}

// RegisterWorkspace atomically checks checkout identity collisions and stores
// the canonical accepted snapshot. An exact repeat returns the stored binding.
func (r *WorkspaceRepo) RegisterWorkspace(ctx context.Context, candidate types.WorkspaceBinding, tree projectstate.Tree) (types.WorkspaceBinding, bool, error) {
	if r == nil || r.db == nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace: unavailable repository")
	}
	if err := candidate.Validate(); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace: %w", err)
	}
	snapshot, encodedTree, err := canonicalStoredTree(tree)
	if err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace snapshot: %w", err)
	}
	if snapshot.Config.ProjectID != candidate.Scope.ProjectID || snapshot.Config.Repository != candidate.Repository || string(snapshot.Digest) != candidate.AcceptedTreeDigest {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace: accepted snapshot differs from binding")
	}
	repositoryJSON, err := json.Marshal(candidate.Repository)
	if err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: encode repository identity: %w", err)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: acquire registration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: begin workspace registration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	existing, existingBytes, err := queryWorkspaceByCheckout(ctx, conn, candidate.Checkout.Device, candidate.Checkout.Inode)
	if err == nil {
		exact := existing.Binding.Scope.ProjectID == candidate.Scope.ProjectID &&
			existing.Binding.Checkout == candidate.Checkout && existing.Binding.Repository == candidate.Repository &&
			existing.Binding.AcceptedCommitSHA == candidate.AcceptedCommitSHA &&
			existing.Binding.AcceptedTreeDigest == candidate.AcceptedTreeDigest && bytes.Equal(existingBytes, encodedTree)
		if !exact {
			return types.WorkspaceBinding{}, false, ErrCheckoutCollision
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: commit repeated workspace registration: %w", err)
		}
		committed = true
		return existing.Binding, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: inspect checkout registration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO workspace_bindings
		(project_id, workspace_id, checkout_path, checkout_device, checkout_inode,
		 repository_identity_json, accepted_ref, accepted_commit, accepted_digest,
		 accepted_snapshot, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'clean')
	`, candidate.Scope.ProjectID, candidate.Scope.WorkspaceID, candidate.Checkout.CanonicalPath,
		candidate.Checkout.Device, candidate.Checkout.Inode, string(repositoryJSON), candidate.AcceptedRef,
		candidate.AcceptedCommitSHA, candidate.AcceptedTreeDigest, encodedTree); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: insert workspace binding: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: commit workspace registration: %w", err)
	}
	committed = true
	return candidate, true, nil
}

// Workspace returns one validated record by its complete immutable scope.
func (r *WorkspaceRepo) Workspace(ctx context.Context, scope types.WorkspaceScope) (WorkspaceRecord, error) {
	if r == nil || r.db == nil || !types.CanonicalUUID(scope.ProjectID) || !types.CanonicalUUID(string(scope.WorkspaceID)) {
		return WorkspaceRecord{}, ErrNotFound
	}
	record, _, err := queryWorkspaceByScope(ctx, r.db, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceRecord{}, ErrNotFound
	}
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: get workspace: %w", err)
	}
	return record, nil
}

// ResolveWorkingDirectory resolves a pre-canonicalized working directory from
// persisted values only. Filesystem identity checks remain the Service's job.
func (r *WorkspaceRepo) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error) {
	if err := observed.Validate(); err != nil {
		return types.WorkspaceBinding{}, ErrNotFound
	}
	bindings, err := r.RegisteredWorkspaces(ctx)
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	longest := -1
	var match types.WorkspaceBinding
	ambiguous := false
	for _, binding := range bindings {
		if !pathContains(binding.Checkout.CanonicalPath, observed.WorkingDirectory) {
			continue
		}
		length := len(binding.Checkout.CanonicalPath)
		switch {
		case length > longest:
			longest, match, ambiguous = length, binding, false
		case length == longest && binding.Scope != match.Scope:
			ambiguous = true
		}
	}
	if longest < 0 || ambiguous {
		return types.WorkspaceBinding{}, ErrNotFound
	}
	return match, nil
}

// RegisteredWorkspaces returns every validated binding in stable scope order.
func (r *WorkspaceRepo) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("localstore: list workspaces: unavailable repository")
	}
	rows, err := r.db.QueryContext(ctx, workspaceSelect+` ORDER BY project_id, workspace_id`)
	if err != nil {
		return nil, fmt.Errorf("localstore: list workspaces: %w", err)
	}
	defer rows.Close()
	bindings := make([]types.WorkspaceBinding, 0)
	for rows.Next() {
		record, _, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore: scan registered workspace: %w", err)
		}
		bindings = append(bindings, record.Binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate registered workspaces: %w", err)
	}
	return bindings, nil
}

// AppendWorkspaceOperation allocates the next generation and inserts the
// canonical operation under one immediate transaction for one exact scope.
func (r *WorkspaceRepo) AppendWorkspaceOperation(ctx context.Context, scope types.WorkspaceScope, operation projectstate.OperationV1) (WorkspaceOperation, error) {
	if !types.CanonicalUUID(operation.ID) {
		return WorkspaceOperation{}, fmt.Errorf("localstore: append workspace operation: invalid operation ID")
	}
	operationJSON, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: encode workspace operation: %w", err)
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: acquire operation connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: begin workspace operation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if _, _, err := queryWorkspaceByScope(ctx, conn, scope); errors.Is(err, sql.ErrNoRows) {
		return WorkspaceOperation{}, ErrNotFound
	} else if err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: verify operation workspace: %w", err)
	}
	var generation int64
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(generation), 0) + 1
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&generation); err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: allocate workspace generation: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO workspace_overlay_operations
		(project_id, workspace_id, generation, operation_id, operation_json, state)
		VALUES (?, ?, ?, ?, ?, 'active')
	`, scope.ProjectID, scope.WorkspaceID, generation, operation.ID, string(operationJSON)); err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: insert workspace operation: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return WorkspaceOperation{}, fmt.Errorf("localstore: commit workspace operation: %w", err)
	}
	committed = true
	return WorkspaceOperation{Generation: generation, OperationID: operation.ID, OperationJSON: operationJSON, State: "active"}, nil
}

func (r *WorkspaceRepo) ListWorkspaceOperations(ctx context.Context, projectID string, workspaceID types.WorkspaceID, afterGeneration int64) ([]WorkspaceOperation, error) {
	scope := types.WorkspaceScope{ProjectID: projectID, WorkspaceID: workspaceID}
	if _, err := r.Workspace(ctx, scope); err != nil {
		return nil, err
	}
	if afterGeneration < 0 {
		return nil, fmt.Errorf("localstore: list workspace operations: invalid generation")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT generation, operation_id, operation_json, state
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND generation>?
		ORDER BY generation
	`, projectID, workspaceID, afterGeneration)
	if err != nil {
		return nil, fmt.Errorf("localstore: list workspace operations: %w", err)
	}
	defer rows.Close()
	operations := make([]WorkspaceOperation, 0)
	for rows.Next() {
		var operation WorkspaceOperation
		var operationJSON string
		if err := rows.Scan(&operation.Generation, &operation.OperationID, &operationJSON, &operation.State); err != nil {
			return nil, fmt.Errorf("localstore: scan workspace operation: %w", err)
		}
		operation.OperationJSON = json.RawMessage(operationJSON)
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate workspace operations: %w", err)
	}
	return operations, nil
}

const workspaceSelect = `
	SELECT project_id, workspace_id, checkout_path, checkout_device, checkout_inode,
	       repository_identity_json, accepted_ref, accepted_commit, accepted_digest,
	       accepted_snapshot, status
	FROM workspace_bindings`

type workspaceScanner interface {
	Scan(...any) error
}

type workspaceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryWorkspaceByCheckout(ctx context.Context, queryer workspaceQueryer, device, inode uint64) (WorkspaceRecord, []byte, error) {
	return scanWorkspace(queryer.QueryRowContext(ctx, workspaceSelect+` WHERE checkout_device=? AND checkout_inode=?`, device, inode))
}

func queryWorkspaceByScope(ctx context.Context, queryer workspaceQueryer, scope types.WorkspaceScope) (WorkspaceRecord, []byte, error) {
	return scanWorkspace(queryer.QueryRowContext(ctx, workspaceSelect+` WHERE project_id=? AND workspace_id=?`, scope.ProjectID, scope.WorkspaceID))
}

func scanWorkspace(scanner workspaceScanner) (WorkspaceRecord, []byte, error) {
	var record WorkspaceRecord
	var repositoryJSON string
	var snapshotBytes []byte
	if err := scanner.Scan(
		&record.Binding.Scope.ProjectID, &record.Binding.Scope.WorkspaceID,
		&record.Binding.Checkout.CanonicalPath, &record.Binding.Checkout.Device, &record.Binding.Checkout.Inode,
		&repositoryJSON, &record.Binding.AcceptedRef, &record.Binding.AcceptedCommitSHA,
		&record.Binding.AcceptedTreeDigest, &snapshotBytes, &record.State,
	); err != nil {
		return WorkspaceRecord{}, nil, err
	}
	if err := json.Unmarshal([]byte(repositoryJSON), &record.Binding.Repository); err != nil {
		return WorkspaceRecord{}, nil, fmt.Errorf("decode repository identity: %w", err)
	}
	canonicalRepository, err := json.Marshal(record.Binding.Repository)
	if err != nil || string(canonicalRepository) != repositoryJSON {
		return WorkspaceRecord{}, nil, fmt.Errorf("repository identity is not canonical")
	}
	if err := record.Binding.Validate(); err != nil {
		return WorkspaceRecord{}, nil, err
	}
	tree, err := decodeFileList(snapshotBytes)
	if err != nil {
		return WorkspaceRecord{}, nil, fmt.Errorf("decode accepted snapshot: %w", err)
	}
	record.Snapshot, err = projectstate.DecodeTree(tree)
	if err != nil {
		return WorkspaceRecord{}, nil, fmt.Errorf("validate accepted snapshot: %w", err)
	}
	if record.Snapshot.Config.ProjectID != record.Binding.Scope.ProjectID ||
		record.Snapshot.Config.Repository != record.Binding.Repository ||
		string(record.Snapshot.Digest) != record.Binding.AcceptedTreeDigest {
		return WorkspaceRecord{}, nil, fmt.Errorf("accepted snapshot differs from workspace binding")
	}
	switch record.State {
	case "clean", "pending", "conflicted", "blocked":
	default:
		return WorkspaceRecord{}, nil, fmt.Errorf("invalid workspace state %q", record.State)
	}
	return record, snapshotBytes, nil
}

func canonicalStoredTree(tree projectstate.Tree) (projectstate.Snapshot, []byte, error) {
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	canonical, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	encoded, err := encodeFileList(canonical)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	return snapshot, encoded, nil
}

func encodeFileList(tree projectstate.Tree) ([]byte, error) {
	files := append(projectstate.Tree(nil), tree...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.BigEndian, uint64(len(files))); err != nil {
		return nil, err
	}
	previous := ""
	for index, file := range files {
		if !validStoredFilePath(file.Path) || (index > 0 && file.Path == previous) {
			return nil, fmt.Errorf("localstore: invalid file-list path %q", file.Path)
		}
		previous = file.Path
		if err := writeLengthPrefixed(&buffer, []byte(file.Path)); err != nil {
			return nil, err
		}
		if err := writeLengthPrefixed(&buffer, file.Data); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

func decodeFileList(encoded []byte) (projectstate.Tree, error) {
	reader := bytes.NewReader(encoded)
	count, err := readUint64(reader)
	if err != nil {
		return nil, err
	}
	if count > uint64(len(encoded))/16+1 {
		return nil, fmt.Errorf("localstore: invalid file-list count")
	}
	tree := make(projectstate.Tree, 0, int(count))
	previous := ""
	for index := uint64(0); index < count; index++ {
		pathBytes, err := readLengthPrefixed(reader)
		if err != nil {
			return nil, err
		}
		data, err := readLengthPrefixed(reader)
		if err != nil {
			return nil, err
		}
		filePath := string(pathBytes)
		if !validStoredFilePath(filePath) || (index > 0 && filePath <= previous) {
			return nil, fmt.Errorf("localstore: invalid encoded file-list path %q", filePath)
		}
		previous = filePath
		tree = append(tree, projectstate.File{Path: filePath, Data: data})
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("localstore: trailing file-list bytes")
	}
	return tree, nil
}

func validStoredFilePath(value string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return value != "" && value == clean && !strings.HasPrefix(value, "/") && value != ".." &&
		!strings.HasPrefix(value, "../") && !strings.ContainsRune(value, 0)
}

func writeLengthPrefixed(writer io.Writer, value []byte) error {
	if err := binary.Write(writer, binary.BigEndian, uint64(len(value))); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

func readLengthPrefixed(reader *bytes.Reader) ([]byte, error) {
	length, err := readUint64(reader)
	if err != nil {
		return nil, err
	}
	if length > uint64(reader.Len()) {
		return nil, fmt.Errorf("localstore: truncated file-list value")
	}
	value := make([]byte, int(length))
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, err
	}
	return value, nil
}

func readUint64(reader io.Reader) (uint64, error) {
	var value uint64
	if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
		return 0, fmt.Errorf("localstore: read file-list length: %w", err)
	}
	return value, nil
}

func pathContains(root, candidate string) bool {
	if root == candidate {
		return true
	}
	return strings.HasPrefix(candidate, root+string(filepath.Separator))
}
