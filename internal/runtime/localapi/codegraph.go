package localapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	codegraphindex "github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	codegraphquery "github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

const codeGraphSourcePermission = "code_graph.source.read"

var ErrCodeGraphRepositoryBinding = errors.New("code graph lifecycle: no ready bootstrapped repository binding")
var ErrCodeGraphRepositoryMismatch = errors.New("code graph lifecycle: checkout origin is outside the approved repository scope")

type CodeGraphLifecycleOperation string

const (
	CodeGraphEnable       CodeGraphLifecycleOperation = "enable"
	CodeGraphDisable      CodeGraphLifecycleOperation = "disable"
	CodeGraphStatus       CodeGraphLifecycleOperation = "status"
	CodeGraphRebuild      CodeGraphLifecycleOperation = "rebuild"
	CodeGraphCheckoutSet  CodeGraphLifecycleOperation = "checkout_set"
	CodeGraphCheckoutShow CodeGraphLifecycleOperation = "checkout_show"
)

type CodeGraphLifecycleRequest struct {
	Operation CodeGraphLifecycleOperation `json:"operation"`
	ProjectID string                      `json:"project_id"`
	Checkout  string                      `json:"checkout,omitempty"`
}

type CodeGraphLifecycleStatus struct {
	ProjectID       string `json:"project_id"`
	Enabled         bool   `json:"enabled"`
	ActiveCheckout  string `json:"active_checkout"`
	CanonicalRemote string `json:"canonical_remote"`
	ActiveRevision  string `json:"active_revision"`
	IndexedCommit   string `json:"indexed_commit"`
}

// CodeGraphLifecycle is the human-only CLI API over Gateway-owned SQLite.
// It is deliberately not registered as an MCP tool.
type CodeGraphLifecycle struct {
	db      *sql.DB
	store   *codegraphstore.Store
	index   *codegraphindex.Index
	project string
	mu      *sync.Mutex
	// beforeBuild is a same-package test seam after initial authorization and
	// before the long candidate build.
	beforeBuild func()
}

func NewCodeGraphLifecycle(ctx context.Context, db *sql.DB, projectID string) (*CodeGraphLifecycle, error) {
	graphStore, err := codegraphstore.Open(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	return &CodeGraphLifecycle{db: db, store: graphStore, index: codegraphindex.New(graphStore), project: projectID, mu: &sync.Mutex{}}, nil
}

func (lifecycle *CodeGraphLifecycle) Execute(ctx context.Context, request CodeGraphLifecycleRequest) (CodeGraphLifecycleStatus, error) {
	return lifecycle.executeWithBinding(ctx, request, codeGraphRepositoryBinding{})
}

func (s *Server) executePrivateCodeGraphLifecycle(ctx context.Context, request CodeGraphLifecycleRequest) (CodeGraphLifecycleStatus, error) {
	if request.ProjectID == "" {
		return CodeGraphLifecycleStatus{}, errors.New("code graph lifecycle: project_id is required")
	}
	if !codeGraphLifecycleOperationSupported(request.Operation) {
		return CodeGraphLifecycleStatus{}, errors.New("code graph lifecycle: unsupported operation")
	}
	binding := codeGraphRepositoryBinding{}
	if codeGraphLifecycleRequiresBinding(request.Operation) {
		var err error
		binding, err = s.resolveCodeGraphLifecycleBinding(ctx, request.ProjectID)
		if err != nil {
			return CodeGraphLifecycleStatus{}, err
		}
	}
	runtime, err := s.ensureCodeGraphRuntime(ctx, request.ProjectID)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	return runtime.Lifecycle.executeWithBinding(ctx, request, binding)
}

func codeGraphLifecycleOperationSupported(operation CodeGraphLifecycleOperation) bool {
	switch operation {
	case CodeGraphEnable, CodeGraphDisable, CodeGraphStatus, CodeGraphRebuild, CodeGraphCheckoutSet, CodeGraphCheckoutShow:
		return true
	default:
		return false
	}
}

func codeGraphLifecycleRequiresBinding(operation CodeGraphLifecycleOperation) bool {
	return operation == CodeGraphEnable || operation == CodeGraphCheckoutSet || operation == CodeGraphRebuild
}

func (s *Server) resolveCodeGraphLifecycleBinding(ctx context.Context, projectID string) (codeGraphRepositoryBinding, error) {
	multi, err := runtimeconfig.LoadMultiOrg()
	if err != nil {
		if errors.Is(err, runtimeconfig.ErrNoCredentials) {
			return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: no credential profile binds this project")
		}
		return codeGraphRepositoryBinding{}, fmt.Errorf("code graph lifecycle: load credential inventory: %w", err)
	}
	matchedProfile := ""
	var matched runtimeconfig.Credentials
	for profile, org := range multi.Orgs {
		if org.Credentials.ProjectID != projectID {
			continue
		}
		if matchedProfile != "" {
			return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: multiple credential profiles bind this project")
		}
		matchedProfile, matched = profile, org.Credentials
	}
	if matchedProfile == "" {
		return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: no credential profile binds this project")
	}
	if s == nil || s.store == nil {
		return codeGraphRepositoryBinding{}, errors.New("code graph lifecycle: Gateway store unavailable")
	}
	if err := s.store.ValidateReadyCheckpoint(ctx, projectID, matched.AgentID, matched.PassportID, matchedProfile); err != nil {
		return codeGraphRepositoryBinding{}, fmt.Errorf("code graph lifecycle: active credential is not a ready bootstrapped checkpoint: %w", err)
	}
	return codeGraphRepositoryBinding{profile: matchedProfile, agent: matched.AgentID, passport: matched.PassportID}, nil
}

func (lifecycle *CodeGraphLifecycle) executeWithBinding(ctx context.Context, request CodeGraphLifecycleRequest, binding codeGraphRepositoryBinding) (CodeGraphLifecycleStatus, error) {
	if lifecycle == nil || lifecycle.mu == nil || request.ProjectID != lifecycle.project {
		return CodeGraphLifecycleStatus{}, errors.New("code graph lifecycle: invalid project scope")
	}
	if (binding.profile == "") != (binding.agent == "") || (binding.profile == "") != (binding.passport == "") {
		return CodeGraphLifecycleStatus{}, ErrCodeGraphRepositoryBinding
	}
	switch request.Operation {
	case CodeGraphEnable:
		return lifecycle.enable(ctx, request.Checkout, binding)
	case CodeGraphDisable:
		return CodeGraphLifecycleStatus{}, lifecycle.Disable(ctx)
	case CodeGraphStatus, CodeGraphCheckoutShow:
		return lifecycle.Status(ctx)
	case CodeGraphRebuild:
		return lifecycle.rebuild(ctx, binding)
	case CodeGraphCheckoutSet:
		return lifecycle.setCheckout(ctx, request.Checkout, binding)
	default:
		return CodeGraphLifecycleStatus{}, errors.New("code graph lifecycle: unsupported operation")
	}
}

func (lifecycle *CodeGraphLifecycle) Enable(ctx context.Context, checkout string) (CodeGraphLifecycleStatus, error) {
	return lifecycle.enable(ctx, checkout, codeGraphRepositoryBinding{})
}

func (lifecycle *CodeGraphLifecycle) enable(ctx context.Context, checkout string, binding codeGraphRepositoryBinding) (CodeGraphLifecycleStatus, error) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.publishCheckout(ctx, checkout, binding)
}

func (lifecycle *CodeGraphLifecycle) SetCheckout(ctx context.Context, checkout string) (CodeGraphLifecycleStatus, error) {
	return lifecycle.setCheckout(ctx, checkout, codeGraphRepositoryBinding{})
}

func (lifecycle *CodeGraphLifecycle) setCheckout(ctx context.Context, checkout string, binding codeGraphRepositoryBinding) (CodeGraphLifecycleStatus, error) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	project, err := lifecycle.store.ProjectConfig(ctx)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	if !project.Enabled {
		return CodeGraphLifecycleStatus{}, codegraphindex.ErrProjectDisabled
	}
	return lifecycle.publishCheckout(ctx, checkout, binding)
}

func (lifecycle *CodeGraphLifecycle) publishCheckout(ctx context.Context, checkout string, exact codeGraphRepositoryBinding) (CodeGraphLifecycleStatus, error) {
	inspection, err := codegraphindex.InspectCheckout(ctx, checkout)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	binding, err := lifecycle.approvedRepository(ctx, lifecycle.db, exact, inspection.CanonicalRemote)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	canonical, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	next, err := lifecycle.store.ProjectConfig(ctx)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	next.Enabled = true
	next.CanonicalRemote = inspection.CanonicalRemote
	next.ActiveCheckout = filepath.Clean(canonical)
	if next.ProjectSourceByteCeiling <= 0 {
		next.ProjectSourceByteCeiling = codegraphconfig.DefaultProjectSourceByteCeiling
	}
	if lifecycle.beforeBuild != nil {
		lifecycle.beforeBuild()
	}
	revisionID := fmt.Sprintf("cli-lifecycle-%d", time.Now().UTC().UnixNano())
	if err := lifecycle.index.BuildForLifecycle(ctx, codegraphindex.BuildRequest{
		ProjectID: lifecycle.project, RevisionID: revisionID,
		PublicationGuard: lifecycle.repositoryPublicationGuard(binding, inspection.CanonicalRemote),
	}, next); err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	return lifecycle.Status(ctx)
}

func (lifecycle *CodeGraphLifecycle) Rebuild(ctx context.Context) (CodeGraphLifecycleStatus, error) {
	return lifecycle.rebuild(ctx, codeGraphRepositoryBinding{})
}

func (lifecycle *CodeGraphLifecycle) rebuild(ctx context.Context, exact codeGraphRepositoryBinding) (CodeGraphLifecycleStatus, error) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	project, err := lifecycle.store.ProjectConfig(ctx)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	if !project.Enabled {
		return CodeGraphLifecycleStatus{}, codegraphindex.ErrProjectDisabled
	}
	binding, err := lifecycle.approvedRepository(ctx, lifecycle.db, exact, project.CanonicalRemote)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	if lifecycle.beforeBuild != nil {
		lifecycle.beforeBuild()
	}
	revisionID := fmt.Sprintf("cli-rebuild-%d", time.Now().UTC().UnixNano())
	if err := lifecycle.index.Build(ctx, codegraphindex.BuildRequest{
		ProjectID: lifecycle.project, RevisionID: revisionID,
		PublicationGuard: lifecycle.repositoryPublicationGuard(binding, project.CanonicalRemote),
	}); err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	return lifecycle.Status(ctx)
}

func (lifecycle *CodeGraphLifecycle) Disable(ctx context.Context) error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.store.Disable(ctx)
}

func (lifecycle *CodeGraphLifecycle) Status(ctx context.Context) (CodeGraphLifecycleStatus, error) {
	project, err := lifecycle.store.ProjectConfig(ctx)
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	status := CodeGraphLifecycleStatus{ProjectID: lifecycle.project, Enabled: project.Enabled, ActiveCheckout: project.ActiveCheckout, CanonicalRemote: project.CanonicalRemote}
	active, err := lifecycle.store.ActiveRevision(ctx)
	if errors.Is(err, codegraphstore.ErrNotFound) {
		return status, nil
	}
	if err != nil {
		return CodeGraphLifecycleStatus{}, err
	}
	status.ActiveRevision, status.IndexedCommit = active.ID, active.IndexedCommit
	return status, nil
}

type codeGraphRepositoryBinding struct {
	profile  string
	agent    string
	passport string
}

type codeGraphRepositoryReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (lifecycle *CodeGraphLifecycle) approvedRepository(ctx context.Context, reader codeGraphRepositoryReader, exact codeGraphRepositoryBinding, remote string) (codeGraphRepositoryBinding, error) {
	approved, identities, err := lifecycle.approvedRepositories(ctx, reader, exact)
	if err != nil {
		return codeGraphRepositoryBinding{}, err
	}
	if _, ok := approved[remote]; !ok {
		return codeGraphRepositoryBinding{}, ErrCodeGraphRepositoryMismatch
	}
	if exact.profile != "" {
		return exact, nil
	}
	if len(identities) != 1 {
		return codeGraphRepositoryBinding{}, ErrCodeGraphRepositoryBinding
	}
	for binding := range identities {
		return binding, nil
	}
	return codeGraphRepositoryBinding{}, ErrCodeGraphRepositoryBinding
}

func (lifecycle *CodeGraphLifecycle) repositoryPublicationGuard(binding codeGraphRepositoryBinding, remote string) codegraphstore.PublicationGuard {
	return func(ctx context.Context, reader codegraphstore.PublicationReader) error {
		approved, _, err := lifecycle.approvedRepositories(ctx, reader, binding)
		if err != nil {
			return err
		}
		if _, ok := approved[remote]; !ok {
			return ErrCodeGraphRepositoryMismatch
		}
		return nil
	}
}

func (lifecycle *CodeGraphLifecycle) approvedRepositories(ctx context.Context, reader codeGraphRepositoryReader, exact codeGraphRepositoryBinding) (map[string]struct{}, map[codeGraphRepositoryBinding]struct{}, error) {
	rows, err := reader.QueryContext(ctx, `
		SELECT passport.repositories, attempt.credential_profile, agent.id, passport.id
		FROM bootstrap_metadata AS bootstrap
		JOIN projects AS project ON project.namespace_id = bootstrap.namespace_id AND project.id = bootstrap.namespace_id
		JOIN passports AS passport ON passport.namespace_id = bootstrap.namespace_id AND passport.project_id = bootstrap.namespace_id
		JOIN agents AS agent ON agent.namespace_id = bootstrap.namespace_id AND agent.id = passport.agent_id
		JOIN auth_scopes AS scope ON scope.namespace_id = bootstrap.namespace_id AND scope.agent_id = agent.id AND scope.passport_id = passport.id
		JOIN whoami_cache AS whoami ON whoami.agent_id = agent.id AND whoami.project_id = bootstrap.namespace_id
		JOIN enrolment_attempts AS attempt ON attempt.project_id = bootstrap.namespace_id AND attempt.agent_id = agent.id
			AND attempt.passport_id = passport.id AND attempt.state = 'ready' AND attempt.terminal = 1
		WHERE bootstrap.namespace_id = ? AND bootstrap.schema_version = 1
		  AND (? = '' OR (attempt.credential_profile = ? AND agent.id = ? AND passport.id = ?))
	`, lifecycle.project, exact.profile, exact.profile, exact.agent, exact.passport)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCodeGraphRepositoryBinding, err)
	}
	defer rows.Close()
	approved := make(map[string]struct{})
	identities := make(map[codeGraphRepositoryBinding]struct{})
	for rows.Next() {
		var encoded, profile, agentID, passportID string
		if err := rows.Scan(&encoded, &profile, &agentID, &passportID); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrCodeGraphRepositoryBinding, err)
		}
		identities[codeGraphRepositoryBinding{profile: profile, agent: agentID, passport: passportID}] = struct{}{}
		var repositories []string
		if err := json.Unmarshal([]byte(encoded), &repositories); err != nil {
			return nil, nil, fmt.Errorf("%w: malformed repository scope", ErrCodeGraphRepositoryBinding)
		}
		for _, repository := range repositories {
			canonical, err := codegraphconfig.CanonicalRemote(repository)
			if err != nil || strings.TrimSpace(repository) != repository {
				return nil, nil, ErrCodeGraphRepositoryBinding
			}
			approved[canonical] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCodeGraphRepositoryBinding, err)
	}
	if len(approved) == 0 || (exact.profile == "" && len(identities) != 1) {
		return nil, nil, ErrCodeGraphRepositoryBinding
	}
	return approved, identities, nil
}

type codeGraphQueryArgs struct {
	Intent               string   `json:"intent,omitempty"`
	EntrySymbols         []string `json:"entry_symbols,omitempty"`
	IncludeEdges         []string `json:"include_edges,omitempty"`
	MaxDepth             int      `json:"max_depth,omitempty"`
	MinimumConfidence    float64  `json:"minimum_confidence,omitempty"`
	RequestedSourceBytes int64    `json:"requested_source_bytes,omitempty"`
	ProjectID            string   `json:"project_id"`
}

type codeGraphProjectArgs struct {
	ProjectID string `json:"project_id"`
}

type codeGraphQueryResult struct {
	GraphRevision            string                    `json:"graph_revision"`
	CurrentGitCommit         string                    `json:"current_git_commit"`
	WorkingTreeStatus        string                    `json:"working_tree_status" enum:"clean,dirty"`
	GraphNotCurrent          bool                      `json:"graph_not_current"`
	RebuildRecommended       bool                      `json:"rebuild_recommended"`
	Matches                  []codeGraphMatch          `json:"matches"`
	Nodes                    []codeGraphNode           `json:"nodes"`
	Edges                    []codeGraphEdge           `json:"edges"`
	StructuralPaths          []codeGraphStructuralPath `json:"structural_paths"`
	Sources                  []codeGraphSourceOutcome  `json:"sources"`
	EffectiveSourceBudget    int64                     `json:"effective_source_budget"`
	SourceBytes              int64                     `json:"source_bytes"`
	RefreshRecommended       bool                      `json:"refresh_recommended"`
	Completeness             string                    `json:"completeness"`
	OmittedNodeCount         int                       `json:"omitted_node_count"`
	OmittedEdgeCount         int                       `json:"omitted_edge_count"`
	OmissionReason           string                    `json:"omission_reason,omitempty"`
	SuggestedFollowUpSymbols []string                  `json:"suggested_follow_up_symbols"`
}

type codeGraphMatch struct {
	SymbolID      string `json:"symbol_id"`
	QualifiedName string `json:"qualified_name"`
	FilePath      string `json:"file_path"`
	Reason        string `json:"reason"`
	Rank          int    `json:"rank"`
}
type codeGraphNode struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Distance int    `json:"distance"`
}
type codeGraphEdge struct {
	ID            string  `json:"id"`
	SourceNodeID  string  `json:"source_node_id"`
	TargetNodeID  string  `json:"target_node_id"`
	Relationship  string  `json:"relationship"`
	Confidence    float64 `json:"confidence"`
	Provenance    string  `json:"provenance"`
	Authoritative bool    `json:"authoritative"`
}
type codeGraphStructuralPath struct {
	FromNodeID string `json:"from_node_id"`
	EdgeID     string `json:"edge_id"`
	ToNodeID   string `json:"to_node_id"`
	Depth      int    `json:"depth"`
}
type codeGraphSourceOutcome struct {
	SymbolID             string `json:"symbol_id"`
	QualifiedName        string `json:"qualified_name"`
	FilePath             string `json:"file_path"`
	StartByte            int64  `json:"start_byte"`
	EndByte              int64  `json:"end_byte"`
	StartLine            int    `json:"start_line"`
	EndLine              int    `json:"end_line"`
	SourceIncluded       bool   `json:"source_included"`
	Source               string `json:"source,omitempty"`
	ReturnedBytes        int64  `json:"returned_bytes"`
	SourceOmissionReason string `json:"source_omission_reason,omitempty"`
	RefreshRecommended   bool   `json:"refresh_recommended,omitempty"`
	RequiredPermission   string `json:"required_permission,omitempty"`
}

type codeGraphStatusResult struct {
	State                 string                `json:"state" enum:"disabled,initializing,ready,degraded,stale,error"`
	ActiveCheckout        string                `json:"active_checkout"`
	CanonicalRemote       string                `json:"canonical_remote"`
	ActiveRevision        string                `json:"active_revision"`
	IndexedCommit         string                `json:"indexed_commit"`
	TrackedGoFileCount    int                   `json:"tracked_go_file_count"`
	SymbolCount           int                   `json:"symbol_count"`
	EdgeCount             int                   `json:"edge_count"`
	DirtyTrackedFileCount int                   `json:"dirty_tracked_file_count"`
	LastSuccessfulBuild   *time.Time            `json:"last_successful_build"`
	DatabaseSize          int64                 `json:"database_size"`
	LatestDiagnostics     []codeGraphDiagnostic `json:"latest_diagnostics"`
}

type codeGraphDiagnostic struct {
	ID        string    `json:"id"`
	Severity  string    `json:"severity"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
type codeGraphRebuildResult struct {
	GraphRevision string `json:"graph_revision"`
	IndexedCommit string `json:"indexed_commit"`
	State         string `json:"state"`
}

func decodeCodeGraphArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("code graph: invalid request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("code graph: invalid request")
	}
	return nil
}

func (s *Server) resolveCodeGraphRuntime(projectID string) (CodeGraphRuntime, error) {
	if projectID == "" {
		return CodeGraphRuntime{}, errors.New("code graph: project_id is required")
	}
	org, err := s.resolveOrgContext(projectID)
	if err != nil {
		return CodeGraphRuntime{}, err
	}
	// resolveOrgContext intentionally retains legacy single-org fallback. Code
	// Graph does not: a caller must name precisely its configured project.
	if org.ProjectID != projectID {
		return CodeGraphRuntime{}, errors.New("code graph: project scope is not configured")
	}
	raw, ok := s.codeGraphs.Load(projectID)
	if !ok {
		return CodeGraphRuntime{}, errors.New("code graph: project runtime unavailable")
	}
	runtime, ok := raw.(CodeGraphRuntime)
	if !ok || !validCodeGraphRuntime(projectID, runtime) {
		return CodeGraphRuntime{}, errors.New("code graph: project runtime unavailable")
	}
	return runtime, nil
}

func (s *Server) localPermissionGranted(ctx context.Context, requiredPermission, projectID string) (bool, error) {
	org, err := s.resolveOrgContext(projectID)
	if err != nil {
		return false, err
	}
	if org.ProjectID != projectID {
		return false, errors.New("permission denied: project scope is not configured")
	}
	var cached localstore.WhoAmICache
	if expectedAgent, ok := s.authorizationAgents.Load(projectID); ok {
		cached, err = s.store.GetCachedWhoAmIForAgentProject(ctx, expectedAgent.(string), projectID)
	} else {
		cached, err = s.store.GetCachedWhoAmIForProject(ctx, projectID)
	}
	if err != nil {
		if errors.Is(err, localstore.ErrNotFound) {
			return false, fmt.Errorf("permission denied: no authenticated scope cached for project %s; call wormhole.agent.whoami while online", projectID)
		}
		return false, fmt.Errorf("localapi: authorize %s: %w", requiredPermission, err)
	}
	for _, permission := range cached.Permissions {
		if permission == requiredPermission {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) handleCodeGraphQuery(ctx context.Context, raw json.RawMessage) (any, error) {
	var args codeGraphQueryArgs
	if err := decodeCodeGraphArgs(raw, &args); err != nil {
		return nil, err
	}
	runtime, err := s.resolveCodeGraphRuntime(args.ProjectID)
	if err != nil {
		return nil, err
	}
	sourceAuthorized, err := s.localPermissionGranted(ctx, codeGraphSourcePermission, args.ProjectID)
	if err != nil {
		return nil, err
	}
	relationships := make([]codegraphstore.Relationship, len(args.IncludeEdges))
	for i, value := range args.IncludeEdges {
		if value != string(codegraphstore.RelationshipCalls) && value != string(codegraphstore.RelationshipReferences) && value != string(codegraphstore.RelationshipUsesType) {
			return nil, errors.New("code graph: invalid include_edges")
		}
		relationships[i] = codegraphstore.Relationship(value)
	}
	result, err := runtime.Query.Execute(ctx, codegraphquery.Request{Intent: args.Intent, EntrySymbols: args.EntrySymbols, IncludeEdges: relationships, MaxDepth: args.MaxDepth, MinimumConfidence: args.MinimumConfidence, RequestedSourceBytes: args.RequestedSourceBytes, SourceAuthorized: sourceAuthorized})
	if err != nil {
		return nil, err
	}
	inspection, err := codegraphindex.InspectCheckoutAgainst(ctx, result.ActiveCheckout, result.IndexedFiles)
	if err != nil {
		return nil, err
	}
	out := queryResultForMCP(result, inspection.DirtyTrackedFileCount)
	out.CurrentGitCommit = inspection.Commit
	if !inspection.InventoryMatches || inspection.CanonicalRemote != result.CanonicalRemote {
		out.GraphNotCurrent = true
		out.RebuildRecommended = true
		out.RefreshRecommended = true
	}
	return out, nil
}

func queryResultForMCP(result codegraphquery.Result, dirty int) codeGraphQueryResult {
	out := codeGraphQueryResult{GraphRevision: result.RevisionID, CurrentGitCommit: result.IndexedCommit, WorkingTreeStatus: workingTreeStatus(dirty), EffectiveSourceBudget: result.EffectiveSourceBudget, SourceBytes: result.SourceBytes, RefreshRecommended: result.RefreshRecommended, Completeness: string(result.Completeness), OmittedNodeCount: result.OmittedNodeCount, OmittedEdgeCount: result.OmittedEdgeCount, OmissionReason: result.OmissionReason, SuggestedFollowUpSymbols: result.SuggestedFollowUpSymbols}
	for _, value := range result.Matches {
		out.Matches = append(out.Matches, codeGraphMatch{SymbolID: value.SymbolID, QualifiedName: value.QualifiedName, FilePath: value.FilePath, Reason: string(value.Reason), Rank: value.Rank})
	}
	for _, value := range result.Nodes {
		out.Nodes = append(out.Nodes, codeGraphNode{ID: value.ID, Kind: string(value.Kind), Name: value.Name, Path: value.Path, Distance: value.Distance})
	}
	for _, value := range result.Edges {
		out.Edges = append(out.Edges, codeGraphEdge{ID: value.ID, SourceNodeID: value.SourceNodeID, TargetNodeID: value.TargetNodeID, Relationship: string(value.Relationship), Confidence: value.Confidence, Provenance: string(value.Provenance), Authoritative: value.Authoritative})
	}
	for _, value := range result.StructuralPaths {
		out.StructuralPaths = append(out.StructuralPaths, codeGraphStructuralPath{FromNodeID: value.FromNodeID, EdgeID: value.EdgeID, ToNodeID: value.ToNodeID, Depth: value.Depth})
	}
	for _, value := range result.Sources {
		out.Sources = append(out.Sources, codeGraphSourceOutcome{SymbolID: value.SymbolID, QualifiedName: value.QualifiedName, FilePath: value.FilePath, StartByte: value.StartByte, EndByte: value.EndByte, StartLine: value.StartLine, EndLine: value.EndLine, SourceIncluded: value.SourceIncluded, Source: value.Source, ReturnedBytes: value.ReturnedBytes, SourceOmissionReason: value.OmissionReason, RefreshRecommended: value.RefreshRecommended, RequiredPermission: value.RequiredPermission})
	}
	return out
}

func (s *Server) handleCodeGraphStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var args codeGraphProjectArgs
	if err := decodeCodeGraphArgs(raw, &args); err != nil {
		return nil, err
	}
	runtime, err := s.resolveCodeGraphRuntime(args.ProjectID)
	if err != nil {
		return nil, err
	}
	project, err := runtime.Store.ProjectConfig(ctx)
	if err != nil {
		return nil, err
	}
	out := codeGraphStatusResult{State: "disabled", ActiveCheckout: project.ActiveCheckout, CanonicalRemote: project.CanonicalRemote, LastSuccessfulBuild: project.LastSuccessfulBuild, LatestDiagnostics: []codeGraphDiagnostic{}}
	if out.DatabaseSize, err = runtime.Store.DatabaseSize(ctx); err != nil {
		return nil, err
	}
	building := !runtime.lifecycleMu.TryLock()
	if !building {
		runtime.lifecycleMu.Unlock()
	}
	var revision codegraphstore.Revision
	var counts codegraphstore.PayloadCounts
	var indexedFiles []codegraphstore.File
	activeErr := runtime.Store.ReadActive(ctx, func(snapshot *codegraphstore.Snapshot) error {
		revision = snapshot.Revision()
		var snapshotErr error
		project, snapshotErr = snapshot.ProjectConfig(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		counts, snapshotErr = snapshot.PayloadCounts(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		indexedFiles, snapshotErr = snapshot.Files(ctx)
		return snapshotErr
	})
	hasActive := activeErr == nil
	if activeErr != nil && !errors.Is(activeErr, codegraphstore.ErrNotFound) {
		return nil, activeErr
	}
	if hasActive {
		out.ActiveCheckout, out.CanonicalRemote, out.LastSuccessfulBuild = project.ActiveCheckout, project.CanonicalRemote, project.LastSuccessfulBuild
		out.ActiveRevision, out.IndexedCommit = revision.ID, revision.IndexedCommit
		out.SymbolCount, out.EdgeCount = counts.Symbols, counts.Edges
	}
	if !project.Enabled {
		return out, nil
	}
	diagnostics, err := runtime.Store.LatestDiagnostics(ctx)
	if err != nil {
		return nil, err
	}
	for _, value := range diagnostics {
		out.LatestDiagnostics = append(out.LatestDiagnostics, codeGraphDiagnostic{ID: value.ID, Severity: string(value.Severity), Code: value.Code, Message: value.Message, CreatedAt: value.CreatedAt})
	}
	var inspection codegraphindex.CheckoutInspection
	if hasActive {
		inspection, err = codegraphindex.InspectCheckoutAgainst(ctx, project.ActiveCheckout, indexedFiles)
	} else {
		inspection, err = codegraphindex.InspectCheckout(ctx, project.ActiveCheckout)
	}
	if err != nil {
		out.LatestDiagnostics = append(out.LatestDiagnostics, codeGraphHealthDiagnostic("checkout_inspection_failed", "approved checkout inspection failed"))
		if hasActive {
			out.State = "degraded"
		} else {
			out.State = "error"
		}
		return out, nil
	}
	out.TrackedGoFileCount, out.DirtyTrackedFileCount = inspection.TrackedGoFileCount, inspection.DirtyTrackedFileCount
	remoteMismatch := inspection.CanonicalRemote != project.CanonicalRemote
	if remoteMismatch && !hasActive {
		out.LatestDiagnostics = append(out.LatestDiagnostics, codeGraphHealthDiagnostic("canonical_remote_mismatch", "checkout origin differs from approved canonical remote"))
		out.State = "error"
		return out, nil
	}
	if remoteMismatch {
		out.LatestDiagnostics = append(out.LatestDiagnostics, codeGraphHealthWarning("canonical_remote_mismatch", "checkout origin differs from approved canonical remote"))
	}
	if !hasActive {
		if hasErrorDiagnostics(out.LatestDiagnostics) {
			out.State = "error"
		} else {
			out.State = "initializing"
		}
	} else if hasErrorDiagnostics(out.LatestDiagnostics) {
		out.State = "degraded"
	} else if building {
		out.State = "initializing"
	} else if !inspection.InventoryMatches || remoteMismatch {
		out.State = "stale"
	} else {
		out.State = "ready"
	}
	return out, nil
}

func codeGraphHealthDiagnostic(code, message string) codeGraphDiagnostic {
	return codeGraphDiagnostic{ID: "health/" + code, Severity: "error", Code: code, Message: message, CreatedAt: time.Now().UTC()}
}

func codeGraphHealthWarning(code, message string) codeGraphDiagnostic {
	return codeGraphDiagnostic{ID: "health/" + code, Severity: "warning", Code: code, Message: message, CreatedAt: time.Now().UTC()}
}

func (s *Server) handleCodeGraphRebuild(ctx context.Context, raw json.RawMessage) (any, error) {
	var args codeGraphProjectArgs
	if err := decodeCodeGraphArgs(raw, &args); err != nil {
		return nil, err
	}
	runtime, err := s.resolveCodeGraphRuntime(args.ProjectID)
	if err != nil {
		return nil, err
	}
	if !runtime.lifecycleMu.TryLock() {
		return nil, errors.New("code graph: rebuild already in progress")
	}
	defer runtime.lifecycleMu.Unlock()
	if err := runtime.Index.Build(ctx, codegraphindex.BuildRequest{ProjectID: args.ProjectID, RevisionID: fmt.Sprintf("mcp-rebuild-%d", time.Now().UTC().UnixNano())}); err != nil {
		return nil, err
	}
	revision, err := runtime.Store.ActiveRevision(ctx)
	if err != nil {
		return nil, err
	}
	return codeGraphRebuildResult{GraphRevision: revision.ID, IndexedCommit: revision.IndexedCommit, State: "ready"}, nil
}

func workingTreeStatus(dirty int) string {
	if dirty > 0 {
		return "dirty"
	}
	return "clean"
}
func hasErrorDiagnostics(values []codeGraphDiagnostic) bool {
	for _, value := range values {
		if value.Severity == "error" {
			return true
		}
	}
	return false
}
