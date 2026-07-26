// Package index inventories exact Git-tracked Go bytes, constructs semantic
// candidate graphs, validates their invariants, and publishes them atomically
// for the Gateway-local Code Graph.
package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	cgconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

// CheckoutInspection is read-only current Git metadata for one approved
// checkout. It never writes, publishes, or changes graph state.
type CheckoutInspection struct {
	CanonicalRemote       string
	Commit                string
	TrackedGoFileCount    int
	DirtyTrackedFileCount int
	InventoryCompared     bool
	InventoryMatches      bool
}

// InspectCheckout reads HEAD, tracked Go inventory count, and dirty tracked
// Go paths. Callers use it only after resolving an approved checkout.
func InspectCheckout(ctx context.Context, checkout string) (CheckoutInspection, error) {
	return inspectCheckout(ctx, checkout, nil, false)
}

// InspectCheckoutAgainst compares the current bounded, security-checked
// tracked Go working bytes with file fingerprints captured from one active
// revision snapshot. Git dirtiness remains display metadata and is not used as
// a proxy for graph freshness.
func InspectCheckoutAgainst(ctx context.Context, checkout string, expected []store.File) (CheckoutInspection, error) {
	return inspectCheckout(ctx, checkout, expected, true)
}

func inspectCheckout(ctx context.Context, checkout string, expected []store.File, compare bool) (CheckoutInspection, error) {
	root, err := canonicalCheckout(checkout)
	if err != nil {
		return CheckoutInspection{}, err
	}
	gitRootOutput, err := gitOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return CheckoutInspection{}, fmt.Errorf("%w: %v", ErrCheckoutRoot, err)
	}
	gitRoot, err := canonicalCheckout(strings.TrimSpace(string(gitRootOutput)))
	if err != nil || gitRoot != root {
		return CheckoutInspection{}, fmt.Errorf("%w: checkout=%q", ErrCheckoutRoot, root)
	}
	remote, err := gitOutput(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return CheckoutInspection{}, fmt.Errorf("codegraph index: resolve canonical remote: %w", err)
	}
	canonicalRemote, err := cgconfig.CanonicalRemote(strings.TrimSpace(string(remote)))
	if err != nil {
		return CheckoutInspection{}, fmt.Errorf("codegraph index: resolve canonical remote: %w", err)
	}
	head, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return CheckoutInspection{}, err
	}
	commit := strings.TrimSpace(string(head))
	if !lowerHexOfLength(commit, 40, 64) {
		return CheckoutInspection{}, errors.New("codegraph index: invalid checkout commit")
	}
	stageOutput, err := gitOutput(ctx, root, "ls-files", "-z", "--cached", "--stage", "--", "*.go")
	if err != nil {
		return CheckoutInspection{}, err
	}
	entries, trackedPaths, comparable, err := parseInspectionStage(stageOutput)
	if err != nil {
		return CheckoutInspection{}, err
	}
	dirty, err := gitOutput(ctx, root, "diff", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return CheckoutInspection{}, err
	}
	inspection := CheckoutInspection{CanonicalRemote: canonicalRemote, Commit: commit, TrackedGoFileCount: len(trackedPaths), DirtyTrackedFileCount: uniqueNULRecordCount(dirty)}
	if compare {
		inspection.InventoryCompared = true
		inspection.InventoryMatches, err = inspectionInventoryMatches(root, entries, trackedPaths, comparable, expected)
		if err != nil {
			return CheckoutInspection{}, err
		}
	}
	return inspection, nil
}

func parseInspectionStage(output []byte) ([]trackedStageEntry, map[string]struct{}, bool, error) {
	records := bytes.Split(output, []byte{0})
	entries := make([]trackedStageEntry, 0, len(records))
	paths := make(map[string]struct{}, len(records))
	comparable := true
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab <= 0 || tab == len(record)-1 {
			return nil, nil, false, fmt.Errorf("%w: malformed Git stage record", ErrUnsupportedTrackedFile)
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || !lowerHexOfLength(fields[1], 40, 64) {
			return nil, nil, false, fmt.Errorf("%w: mode/object is not a regular tracked entry", ErrUnsupportedTrackedFile)
		}
		filePath := string(record[tab+1:])
		if !utf8.ValidString(filePath) || strings.Contains(filePath, "\\") || !strings.HasSuffix(filePath, ".go") || path.Clean(filePath) != filePath || !filepath.IsLocal(filepath.FromSlash(filePath)) {
			return nil, nil, false, fmt.Errorf("%w: unsafe tracked path", ErrUnsupportedTrackedFile)
		}
		paths[filePath] = struct{}{}
		if fields[2] != "0" {
			comparable = false
			continue
		}
		entries = append(entries, trackedStageEntry{path: filePath, mode: fields[0]})
	}
	if len(paths) > DefaultInventoryLimits.MaxFiles {
		return nil, nil, false, fmt.Errorf("%w: files=%d maximum=%d", ErrInventoryLimit, len(paths), DefaultInventoryLimits.MaxFiles)
	}
	return entries, paths, comparable, nil
}

func inspectionInventoryMatches(root string, entries []trackedStageEntry, trackedPaths map[string]struct{}, comparable bool, expected []store.File) (bool, error) {
	if len(trackedPaths) != len(expected) {
		comparable = false
	}
	expectedByPath := make(map[string]store.File, len(expected))
	for _, file := range expected {
		if _, duplicate := expectedByPath[file.Path]; duplicate {
			return false, fmt.Errorf("%w: duplicate indexed file path", ErrUnsupportedTrackedFile)
		}
		expectedByPath[file.Path] = file
		if _, present := trackedPaths[file.Path]; !present {
			comparable = false
		}
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return false, fmt.Errorf("%w: open checkout root", ErrCheckoutRoot)
	}
	defer rootHandle.Close()
	var totalBytes int64
	for _, entry := range entries {
		file, err := readTrackedFile(rootHandle, root, entry, DefaultInventoryLimits.MaxFileBytes)
		if errors.Is(err, ErrTrackedFileChanged) {
			comparable = false
			continue
		}
		if err != nil {
			return false, err
		}
		if totalBytes > DefaultInventoryLimits.MaxTotalBytes-int64(len(file.Bytes)) {
			return false, fmt.Errorf("%w: total bytes exceed %d", ErrInventoryLimit, DefaultInventoryLimits.MaxTotalBytes)
		}
		totalBytes += int64(len(file.Bytes))
		indexed, present := expectedByPath[file.Path]
		if !present || indexed.IndexedHash != file.SHA256 || indexed.ByteSize != int64(len(file.Bytes)) {
			comparable = false
		}
	}
	return comparable, nil
}

func uniqueNULRecordCount(value []byte) int {
	paths := make(map[string]struct{})
	start := 0
	for index, current := range value {
		if current != 0 {
			continue
		}
		if index > start {
			paths[string(value[start:index])] = struct{}{}
		}
		start = index + 1
	}
	return len(paths)
}

var ErrInvalidCandidate = errors.New("codegraph index: invalid candidate")

// Index publishes candidates through a project-bound Store.
type Index struct {
	store *store.Store
}

func New(graphStore *store.Store) *Index {
	return &Index{store: graphStore}
}

// Publish validates and atomically publishes revisionID. The Store runs the
// validator inside the same SQLite transaction as the active-pointer swap.
func (index *Index) Publish(ctx context.Context, revisionID string) error {
	if index == nil || index.store == nil {
		return errors.New("codegraph index: nil store")
	}
	return index.store.PublishCandidate(ctx, revisionID, validateCandidate)
}

func validateCandidate(ctx context.Context, snapshot *store.Snapshot) error {
	revision := snapshot.Revision()
	if revision.State != store.RevisionCandidate {
		return invalid("revision is not a candidate")
	}
	if !validGitHash(revision.IndexedCommit) {
		return invalid("indexed commit hash is not canonical")
	}
	nodes, err := snapshot.Nodes(ctx)
	if err != nil {
		return err
	}
	files, err := snapshot.Files(ctx)
	if err != nil {
		return err
	}
	symbols, err := snapshot.Symbols(ctx)
	if err != nil {
		return err
	}
	edges, err := snapshot.Edges(ctx)
	if err != nil {
		return err
	}

	nodesByID := make(map[string]store.Node, len(nodes))
	repositoryCount := 0
	for _, node := range nodes {
		if node.ProjectID != revision.ProjectID || node.RevisionID != revision.ID {
			return invalid("node escaped project or revision scope")
		}
		if node.ID == "" {
			return invalid("node has an empty deterministic id")
		}
		if _, exists := nodesByID[node.ID]; exists {
			return invalid("duplicate deterministic node id")
		}
		nodesByID[node.ID] = node
		if node.Kind == store.NodeRepository {
			repositoryCount++
		}
	}
	if repositoryCount != 1 {
		return invalid("candidate must contain exactly one repository node")
	}

	filesByID := make(map[string]store.File, len(files))
	for _, file := range files {
		if file.ProjectID != revision.ProjectID || file.RevisionID != revision.ID {
			return invalid("file escaped project or revision scope")
		}
		if !validSHA256(file.IndexedHash) {
			return invalid("file indexed hash is not canonical")
		}
		node, exists := nodesByID[file.ID]
		if !exists || node.Kind != store.NodeFile {
			return invalid("file does not reference a file node in the candidate")
		}
		if node.Path != file.Path {
			return invalid("file path differs from its candidate node")
		}
		if _, exists := filesByID[file.ID]; exists {
			return invalid("duplicate deterministic file id")
		}
		filesByID[file.ID] = file
	}

	for _, symbol := range symbols {
		if symbol.ProjectID != revision.ProjectID || symbol.RevisionID != revision.ID {
			return invalid("symbol escaped project or revision scope")
		}
		node, exists := nodesByID[symbol.ID]
		if !exists || node.Kind != store.NodeSymbol {
			return invalid("symbol does not reference a symbol node in the candidate")
		}
		file, exists := filesByID[symbol.FileID]
		if !exists {
			return invalid("symbol file is outside the candidate")
		}
		if node.Path != file.Path {
			return invalid("symbol path differs from its candidate file")
		}
		if symbol.StartByte < 0 || symbol.EndByte <= symbol.StartByte || symbol.EndByte > file.ByteSize {
			return invalid("symbol byte range is outside its candidate file")
		}
		if symbol.StartLine < 1 || symbol.EndLine < symbol.StartLine {
			return invalid("symbol line range is invalid")
		}
	}

	for _, edge := range edges {
		if edge.ProjectID != revision.ProjectID || edge.RevisionID != revision.ID {
			return invalid("edge escaped project or revision scope")
		}
		if _, exists := nodesByID[edge.SourceNodeID]; !exists {
			return invalid("edge source is outside the candidate")
		}
		if _, exists := nodesByID[edge.TargetNodeID]; !exists {
			return invalid("edge target is outside the candidate")
		}
	}
	return nil
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCandidate, reason)
}

func validSHA256(value string) bool {
	return strings.HasPrefix(value, "sha256:") && len(value) == len("sha256:")+64 && lowerHex(value[len("sha256:"):])
}

func validGitHash(value string) bool {
	return (len(value) == 40 || len(value) == 64) && lowerHex(value)
}

func lowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return value != ""
}
