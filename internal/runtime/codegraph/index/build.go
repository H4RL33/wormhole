package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cgconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	cggo "github.com/H4RL33/wormhole/internal/runtime/codegraph/golang"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

const maxModuleFileBytes = 4 << 20
const maxStoredDiagnosticBytes = 1_024
const failedBuildCleanupTimeout = 5 * time.Second

var ErrCheckoutChanged = errors.New("codegraph index: checkout changed during build")
var ErrProjectDisabled = errors.New("codegraph index: project Code Graph is disabled")
var ErrApprovedCheckoutMismatch = errors.New("codegraph index: build does not match approved checkout")

type BuildRequest struct {
	ProjectID          string
	RevisionID         string
	Checkout           string
	CanonicalRemote    string
	ExpectedModulePath string
	CreatedAt          time.Time
	InventoryLimits    InventoryLimits
	AnalysisLimits     cggo.Limits
	PublicationGuard   store.PublicationGuard
}

type lifecyclePublication struct {
	expected       cgconfig.Project
	expectedActive string
	next           cgconfig.Project
}

// Build constructs a candidate from exact tracked working-tree bytes and
// publishes it only after the checkout and root module are revalidated.
func (index *Index) Build(ctx context.Context, request BuildRequest) (buildErr error) {
	if index == nil || index.store == nil {
		return errors.New("codegraph index: nil store")
	}
	if request.ProjectID == "" || request.RevisionID == "" {
		return fmt.Errorf("%w: project and revision ids are required", ErrInvalidCandidate)
	}
	if err := index.store.BeginBuild(ctx, request.RevisionID); err != nil {
		return err
	}
	defer func() { buildErr = errors.Join(buildErr, index.endBuild(request.RevisionID)) }()
	approved, err := index.store.ProjectConfig(ctx)
	if err != nil {
		return err
	}
	if approved.ProjectID != request.ProjectID {
		return fmt.Errorf("%w: project scope differs from bound store", ErrApprovedCheckoutMismatch)
	}
	if !approved.Enabled {
		return ErrProjectDisabled
	}
	return index.build(ctx, request, approved, nil)
}

// BuildForLifecycle constructs and validates a candidate using next without
// making it visible, then atomically publishes the candidate and next config.
// The existing configuration and active graph remain untouched on failure.
func (index *Index) BuildForLifecycle(ctx context.Context, request BuildRequest, next cgconfig.Project) (buildErr error) {
	if index == nil || index.store == nil {
		return errors.New("codegraph index: nil store")
	}
	if request.ProjectID == "" || request.RevisionID == "" || next.ProjectID != request.ProjectID {
		return fmt.Errorf("%w: project and revision ids are required and must match configuration", ErrInvalidCandidate)
	}
	if err := index.store.BeginBuild(ctx, request.RevisionID); err != nil {
		return err
	}
	defer func() { buildErr = errors.Join(buildErr, index.endBuild(request.RevisionID)) }()
	if err := cgconfig.ValidateProject(next); err != nil {
		return err
	}
	if !next.Enabled {
		return ErrProjectDisabled
	}
	expected, err := index.store.ProjectConfig(ctx)
	if err != nil {
		return err
	}
	expectedActive := ""
	if active, activeErr := index.store.ActiveRevision(ctx); activeErr == nil {
		expectedActive = active.ID
	} else if !errors.Is(activeErr, store.ErrNotFound) {
		return activeErr
	}
	return index.build(ctx, request, next, &lifecyclePublication{expected: expected, expectedActive: expectedActive, next: next})
}

func (index *Index) endBuild(token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), failedBuildCleanupTimeout)
	defer cancel()
	return index.store.EndBuild(ctx, token)
}

func (index *Index) build(ctx context.Context, request BuildRequest, approved cgconfig.Project, lifecycle *lifecyclePublication) error {
	approvedRoot, err := canonicalCheckout(approved.ActiveCheckout)
	if err != nil {
		return fmt.Errorf("%w: persisted checkout is invalid", ErrApprovedCheckoutMismatch)
	}
	if request.Checkout != "" {
		requestedRoot, requestedErr := canonicalCheckout(request.Checkout)
		if requestedErr != nil || requestedRoot != approvedRoot {
			return fmt.Errorf("%w: checkout differs from persisted project config", ErrApprovedCheckoutMismatch)
		}
	}
	if request.CanonicalRemote != "" && request.CanonicalRemote != approved.CanonicalRemote {
		return fmt.Errorf("%w: remote differs from persisted project config", ErrApprovedCheckoutMismatch)
	}
	request.Checkout = approvedRoot
	request.CanonicalRemote = approved.CanonicalRemote
	inventoryLimits := request.InventoryLimits
	if inventoryLimits == (InventoryLimits{}) {
		inventoryLimits = DefaultInventoryLimits
	}
	inventory, err := LoadGitInventoryWithLimits(ctx, request.Checkout, request.CanonicalRemote, inventoryLimits)
	if err != nil {
		return err
	}
	moduleBefore, err := snapshotModuleFile(inventory.Root)
	if err != nil {
		return err
	}
	createdAt := request.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if err := index.store.CreateCandidate(ctx, store.Revision{
		ProjectID: request.ProjectID, ID: request.RevisionID, IndexedCommit: inventory.Commit, CreatedAt: createdAt,
	}); err != nil {
		return err
	}

	sources := make([]cggo.SourceFile, 0, len(inventory.Files))
	for _, file := range inventory.Files {
		sources = append(sources, cggo.SourceFile{Path: file.Path, Bytes: file.Bytes, SHA256: file.SHA256})
	}
	analysis, analyzeErr := cggo.Analyze(ctx, cggo.Request{
		Checkout: inventory.Root, ExpectedModulePath: request.ExpectedModulePath,
		Files: sources, Limits: request.AnalysisLimits,
	})
	if analyzeErr != nil {
		return index.failBuild(ctx, request, analysis.Diagnostics, "analysis_failed", analyzeErr)
	}
	moduleAfter, err := snapshotModuleFile(inventory.Root)
	if err != nil || moduleBefore != moduleAfter {
		if err == nil {
			err = ErrCheckoutChanged
		}
		return index.failBuild(ctx, request, analysis.Diagnostics, "checkout_changed", err)
	}
	revalidated, err := LoadGitInventoryWithLimits(ctx, inventory.Root, request.CanonicalRemote, inventoryLimits)
	if err != nil || !equalInventory(inventory, revalidated) {
		if err == nil {
			err = ErrCheckoutChanged
		}
		return index.failBuild(ctx, request, analysis.Diagnostics, "checkout_changed", err)
	}
	if err := index.writeCandidate(ctx, request, inventory, analysis); err != nil {
		return index.failBuild(ctx, request, analysis.Diagnostics, "candidate_write_failed", err)
	}
	moduleFinal, err := snapshotModuleFile(inventory.Root)
	if err != nil || moduleBefore != moduleFinal {
		if err == nil {
			err = ErrCheckoutChanged
		}
		return index.failBuild(ctx, request, analysis.Diagnostics, "checkout_changed", err)
	}
	finalInventory, err := LoadGitInventoryWithLimits(ctx, inventory.Root, request.CanonicalRemote, inventoryLimits)
	if err != nil || !equalInventory(inventory, finalInventory) {
		if err == nil {
			err = ErrCheckoutChanged
		}
		return index.failBuild(ctx, request, analysis.Diagnostics, "checkout_changed", err)
	}
	if lifecycle == nil {
		approvedFinal, err := index.store.ProjectConfig(ctx)
		if err != nil || !approvedConfigMatches(approvedFinal, request.ProjectID, approvedRoot, request.CanonicalRemote) {
			if err == nil {
				err = ErrApprovedCheckoutMismatch
			}
			return index.failBuild(ctx, request, analysis.Diagnostics, "approved_checkout_changed", err)
		}
	}
	if err := index.publishCompletedBuild(ctx, request, approved, lifecycle, analysis.Diagnostics); err != nil {
		return err
	}
	return nil
}

func (index *Index) publishCompletedBuild(ctx context.Context, request BuildRequest, approved cgconfig.Project, lifecycle *lifecyclePublication, diagnostics []cggo.Diagnostic) error {
	if lifecycle == nil {
		return index.publishBuild(ctx, request, approved, diagnostics)
	}
	err := index.store.PublishCandidateWithConfigGuarded(ctx, request.RevisionID, lifecycle.expected, lifecycle.expectedActive, lifecycle.next, request.PublicationGuard, func(ctx context.Context, snapshot *store.Snapshot) error {
		return validateCandidate(ctx, snapshot)
	})
	if err != nil {
		return index.failBuild(ctx, request, diagnostics, "lifecycle_publication_failed", err)
	}
	return nil
}

func (index *Index) publishBuild(ctx context.Context, request BuildRequest, approved cgconfig.Project, diagnostics []cggo.Diagnostic) error {
	err := index.store.PublishCandidateGuarded(ctx, request.RevisionID, request.PublicationGuard, func(ctx context.Context, snapshot *store.Snapshot) error {
		if err := validateCandidate(ctx, snapshot); err != nil {
			return err
		}
		current, err := snapshot.ProjectConfig(ctx)
		if err != nil {
			return err
		}
		if !current.Enabled || current.ProjectID != approved.ProjectID ||
			current.ActiveCheckout != approved.ActiveCheckout || current.CanonicalRemote != approved.CanonicalRemote {
			return fmt.Errorf("%w: persisted approval changed before publication", ErrApprovedCheckoutMismatch)
		}
		return nil
	})
	if err != nil {
		return index.failBuild(ctx, request, diagnostics, "publication_failed", err)
	}
	return nil
}

func approvedConfigMatches(project cgconfig.Project, projectID, root, remote string) bool {
	if !project.Enabled || project.ProjectID != projectID || project.CanonicalRemote != remote {
		return false
	}
	configuredRoot, err := canonicalCheckout(project.ActiveCheckout)
	return err == nil && configuredRoot == root
}

func (index *Index) writeCandidate(ctx context.Context, request BuildRequest, inventory GitInventory, analysis cggo.Result) error {
	repositoryID := deterministicID("repository", request.ProjectID, inventory.CanonicalRemote)
	if err := index.store.PutNode(ctx, store.Node{
		ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: repositoryID,
		Kind: store.NodeRepository, Name: request.ProjectID, Path: inventory.CanonicalRemote,
	}); err != nil {
		return err
	}
	for _, pkg := range analysis.Packages {
		if err := index.store.PutNode(ctx, store.Node{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: pkg.ID,
			Kind: store.NodePackage, Name: pkg.ImportPath, Path: pkg.ImportPath,
		}); err != nil {
			return err
		}
		if err := index.store.PutEdge(ctx, store.Edge{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID,
			ID:           deterministicID("edge", repositoryID, pkg.ID, string(store.RelationshipContains), string(store.ProvenanceGoPackages)),
			SourceNodeID: repositoryID, TargetNodeID: pkg.ID, Relationship: store.RelationshipContains,
			Confidence: 1, Provenance: store.ProvenanceGoPackages,
		}); err != nil {
			return err
		}
	}
	semanticFiles := make(map[string]cggo.File, len(analysis.Files))
	for _, file := range analysis.Files {
		semanticFiles[file.Path] = file
	}
	for _, tracked := range inventory.Files {
		semantic, exists := semanticFiles[tracked.Path]
		if !exists {
			return fmt.Errorf("%w: semantic adapter omitted tracked file %q", ErrInvalidCandidate, tracked.Path)
		}
		if err := index.store.PutNode(ctx, store.Node{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: semantic.ID,
			Kind: store.NodeFile, Name: filepath.Base(tracked.Path), Path: tracked.Path,
		}); err != nil {
			return err
		}
		if err := index.store.PutFile(ctx, store.File{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: semantic.ID,
			Path: tracked.Path, IndexedHash: tracked.SHA256, ByteSize: int64(len(tracked.Bytes)),
		}); err != nil {
			return err
		}
		if semantic.PackageID == "" {
			if err := index.store.PutEdge(ctx, store.Edge{
				ProjectID: request.ProjectID, RevisionID: request.RevisionID,
				ID:           deterministicID("edge", repositoryID, semantic.ID, string(store.RelationshipContains), string(store.ProvenanceGoPackages)),
				SourceNodeID: repositoryID, TargetNodeID: semantic.ID, Relationship: store.RelationshipContains,
				Confidence: 1, Provenance: store.ProvenanceGoPackages,
			}); err != nil {
				return err
			}
		}
	}
	for _, symbol := range analysis.Symbols {
		if err := index.store.PutNode(ctx, store.Node{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: symbol.ID,
			Kind: store.NodeSymbol, Name: symbol.Name, Path: symbol.FilePath,
		}); err != nil {
			return err
		}
		if err := index.store.PutSymbol(ctx, store.Symbol{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: symbol.ID,
			FileID: symbol.FileID, QualifiedName: symbol.QualifiedName, Signature: symbol.Signature,
			StartByte: symbol.StartByte, EndByte: symbol.EndByte, StartLine: symbol.StartLine, EndLine: symbol.EndLine,
		}); err != nil {
			return err
		}
	}
	for _, edge := range analysis.Edges {
		relationship, err := storeRelationship(edge.Relationship)
		if err != nil {
			return err
		}
		provenance, err := storeProvenance(edge.Provenance)
		if err != nil {
			return err
		}
		if err := index.store.PutEdge(ctx, store.Edge{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: edge.ID,
			SourceNodeID: edge.SourceID, TargetNodeID: edge.TargetID,
			Relationship: relationship, Confidence: edge.Confidence, Provenance: provenance,
		}); err != nil {
			return err
		}
	}
	for _, diagnostic := range analysis.Diagnostics {
		if err := index.store.PutDiagnostic(ctx, store.Diagnostic{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: diagnostic.ID,
			Severity: diagnosticSeverity(diagnostic.Severity), Code: diagnostic.Code,
			Message: boundedDiagnostic(diagnostic.Message), CreatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (index *Index) failBuild(ctx context.Context, request BuildRequest, diagnostics []cggo.Diagnostic, code string, buildErr error) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), failedBuildCleanupTimeout)
	defer cancel()
	// Validation failure may already have atomically failed the candidate, and
	// an ambiguous commit error may mean it is active. Never try to mutate a
	// non-candidate revision during cleanup.
	if revision, err := index.store.Revision(cleanupContext, request.RevisionID); err == nil && revision.State != store.RevisionCandidate {
		return buildErr
	}
	var failures []error
	for _, diagnostic := range diagnostics {
		if err := index.store.PutDiagnostic(cleanupContext, store.Diagnostic{
			ProjectID: request.ProjectID, RevisionID: request.RevisionID, ID: diagnostic.ID,
			Severity: diagnosticSeverity(diagnostic.Severity), Code: diagnostic.Code,
			Message: boundedDiagnostic(diagnostic.Message), CreatedAt: time.Now().UTC(),
		}); err != nil {
			failures = append(failures, err)
		}
	}
	if err := index.store.FailCandidate(cleanupContext, request.RevisionID, code, boundedDiagnostic(buildErr.Error())); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(append([]error{buildErr}, failures...)...)
}

type moduleSnapshot struct {
	path   string
	sha256 string
	size   int64
}

func snapshotModuleFile(root string) (moduleSnapshot, error) {
	modulePath := filepath.Join(root, "go.mod")
	info, err := os.Lstat(modulePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxModuleFileBytes {
		return moduleSnapshot{}, fmt.Errorf("%w: root go.mod is missing, irregular, or too large", ErrInvalidCandidate)
	}
	resolved, err := filepath.EvalSymlinks(modulePath)
	if err != nil || resolved != modulePath {
		return moduleSnapshot{}, fmt.Errorf("%w: root go.mod is not a canonical regular file", ErrInvalidCandidate)
	}
	content, err := os.ReadFile(modulePath)
	if err != nil || int64(len(content)) != info.Size() {
		return moduleSnapshot{}, fmt.Errorf("%w: root go.mod changed while reading", ErrCheckoutChanged)
	}
	digest := sha256.Sum256(content)
	return moduleSnapshot{path: modulePath, sha256: hex.EncodeToString(digest[:]), size: info.Size()}, nil
}

func equalInventory(first, second GitInventory) bool {
	if first.Root != second.Root || first.CanonicalRemote != second.CanonicalRemote || first.Commit != second.Commit || first.TotalBytes != second.TotalBytes || len(first.Files) != len(second.Files) {
		return false
	}
	for index := range first.Files {
		left, right := first.Files[index], second.Files[index]
		if left.Path != right.Path || left.Mode != right.Mode || left.SHA256 != right.SHA256 || !bytes.Equal(left.Bytes, right.Bytes) {
			return false
		}
	}
	return true
}

func storeRelationship(value cggo.Relationship) (store.Relationship, error) {
	switch value {
	case cggo.RelationshipContains:
		return store.RelationshipContains, nil
	case cggo.RelationshipDefines:
		return store.RelationshipDefines, nil
	case cggo.RelationshipImports:
		return store.RelationshipImports, nil
	case cggo.RelationshipCalls:
		return store.RelationshipCalls, nil
	case cggo.RelationshipReferences:
		return store.RelationshipReferences, nil
	case cggo.RelationshipUsesType:
		return store.RelationshipUsesType, nil
	default:
		return "", fmt.Errorf("%w: unsupported relationship %q", ErrInvalidCandidate, value)
	}
}

func storeProvenance(value cggo.Provenance) (store.Provenance, error) {
	switch value {
	case cggo.ProvenanceGoPackages:
		return store.ProvenanceGoPackages, nil
	case cggo.ProvenanceGoTypes:
		return store.ProvenanceGoTypes, nil
	case cggo.ProvenanceGoAST:
		return store.ProvenanceGoAST, nil
	case cggo.ProvenanceParser:
		return store.ProvenanceParser, nil
	case cggo.ProvenanceHeuristic:
		return store.ProvenanceHeuristic, nil
	default:
		return "", fmt.Errorf("%w: unsupported provenance %q", ErrInvalidCandidate, value)
	}
}

func diagnosticSeverity(value cggo.DiagnosticSeverity) store.DiagnosticSeverity {
	if value == cggo.DiagnosticWarning {
		return store.DiagnosticWarning
	}
	return store.DiagnosticError
}

func boundedDiagnostic(message string) string {
	message = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxStoredDiagnosticBytes {
		message = message[:maxStoredDiagnosticBytes]
	}
	return message
}

func deterministicID(domain string, fields ...string) string {
	hash := sha256.New()
	var length [8]byte
	allFields := append([]string{domain}, fields...)
	for _, field := range allFields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return "cg:" + domain + ":" + hex.EncodeToString(hash.Sum(nil))
}
