// Package query provides deterministic lexical entry ranking, bounded
// structural traversal, and authorized source assembly over one coherent
// active Code Graph revision.
package query

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/source"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

const (
	MaxTraversalDepth   = 8
	MaxTraversalNodes   = 1_000
	MaxTraversalEdges   = 4_000
	MaxSearchCandidates = 512
	MaxMatchResults     = 64
	MaxEntrySymbols     = 64
	MaxIntentBytes      = 2_048
	defaultMaxNodes     = 100
	maxFollowUps        = 32
	maxEvidenceEdges    = 2_048
)

var ErrInvalidRequest = errors.New("codegraph query: invalid request")
var ErrUnavailable = errors.New("codegraph query: active project configuration is unavailable")

type MatchReason string

const (
	MatchExactQualified MatchReason = "exact_qualified_name"
	MatchExactName      MatchReason = "exact_name"
	MatchLexicalSymbol  MatchReason = "lexical_symbol"
	MatchPackageFile    MatchReason = "package_or_file"
)

type Completeness string

const (
	CompletenessComplete Completeness = "complete"
	CompletenessPartial  Completeness = "partial"
)

const (
	OmissionNodeBudget  = "node_budget_exhausted"
	OmissionMatchBudget = "match_budget_exhausted"
	OmissionEdgeBudget  = "edge_budget_exhausted"
)

type Direction string

const (
	DirectionBoth     Direction = "both"
	DirectionOutbound Direction = "outbound"
	DirectionInbound  Direction = "inbound"
)

type Request struct {
	Intent               string
	EntrySymbols         []string
	IncludeEdges         []store.Relationship
	Direction            Direction
	MaxDepth             int
	MinimumConfidence    float64
	MaxNodes             int
	RequestedSourceBytes int64
	SourceAuthorized     bool
}

type Match struct {
	SymbolID      string
	QualifiedName string
	FilePath      string
	Reason        MatchReason
	Rank          int
}

type Node struct {
	ID       string
	Kind     store.NodeKind
	Name     string
	Path     string
	Distance int
}

type Edge struct {
	ID            string
	SourceNodeID  string
	TargetNodeID  string
	Relationship  store.Relationship
	Confidence    float64
	Provenance    store.Provenance
	Authoritative bool
}

type StructuralPath struct {
	FromNodeID string
	EdgeID     string
	ToNodeID   string
	Depth      int
}

type Result struct {
	RevisionID               string
	IndexedCommit            string
	ActiveCheckout           string
	CanonicalRemote          string
	IndexedFiles             []store.File
	Matches                  []Match
	Nodes                    []Node
	Edges                    []Edge
	StructuralPaths          []StructuralPath
	Sources                  []source.Outcome
	EffectiveSourceBudget    int64
	SourceBytes              int64
	RefreshRecommended       bool
	Completeness             Completeness
	OmittedNodeCount         int
	OmittedEdgeCount         int
	OmissionReason           string
	SuggestedFollowUpSymbols []string
}

type Service struct {
	store               *store.Store
	globalSourceCeiling int64
}

func New(graphStore *store.Store, globalSourceCeiling int64) *Service {
	return &Service{store: graphStore, globalSourceCeiling: globalSourceCeiling}
}

type querySnapshot struct {
	revision      store.Revision
	projectConfig config.Project
	ranked        []rankedMatch
	matches       []Match
	traversal     traversalResult
	symbols       []store.SymbolRecord
	files         []store.File
	droppedMatch  int
	matchFollowUp []string
}

// Execute performs bounded SQL candidate reads and frontier traversal inside
// one ReadActive transaction, then assembles source from that pinned metadata.
func (service *Service) Execute(ctx context.Context, request Request) (Result, error) {
	return service.execute(ctx, request, nil)
}

func (service *Service) execute(ctx context.Context, request Request, afterSnapshot func(store.Revision)) (Result, error) {
	request, relationships, relationshipSet, err := validateRequest(request)
	if err != nil {
		return Result{}, err
	}
	if service == nil || service.store == nil {
		return Result{}, errors.New("codegraph query: nil store")
	}
	if request.RequestedSourceBytes > 0 && service.globalSourceCeiling <= 0 {
		return Result{}, fmt.Errorf("%w: global source ceiling is invalid", ErrInvalidRequest)
	}
	terms := sortedTokens(request.Intent)
	var captured querySnapshot
	err = service.store.ReadActive(ctx, func(snapshot *store.Snapshot) error {
		captured.revision = snapshot.Revision()
		if afterSnapshot != nil {
			afterSnapshot(captured.revision)
		}
		captured.projectConfig, err = snapshot.ProjectConfig(ctx)
		if err != nil {
			return err
		}
		if !captured.projectConfig.Enabled || captured.projectConfig.ActiveCheckout == "" || captured.projectConfig.ProjectSourceByteCeiling <= 0 {
			return ErrUnavailable
		}
		captured.files, err = snapshot.Files(ctx)
		if err != nil {
			return err
		}
		records, total, err := snapshot.SearchSymbols(ctx, request.EntrySymbols, terms, MaxSearchCandidates)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		captured.ranked, err = rankMatches(ctx, request, records)
		if err != nil {
			return err
		}
		if err := addStructuralEvidence(ctx, snapshot, request, relationships, captured.ranked); err != nil {
			return err
		}
		sortRanked(captured.ranked)
		matchLimit := minimumInt(MaxMatchResults, request.MaxNodes)
		if len(captured.ranked) > matchLimit {
			captured.droppedMatch += len(captured.ranked) - matchLimit
			for _, candidate := range captured.ranked[matchLimit:] {
				if len(captured.matchFollowUp) >= maxFollowUps {
					break
				}
				captured.matchFollowUp = append(captured.matchFollowUp, candidate.record.Symbol.QualifiedName)
			}
			captured.ranked = captured.ranked[:matchLimit]
		}
		if total > len(records) {
			captured.droppedMatch += total - len(records)
		}
		for index := range captured.ranked {
			captured.ranked[index].match.Rank = index + 1
			captured.matches = append(captured.matches, captured.ranked[index].match)
		}
		captured.traversal, err = traverse(ctx, snapshot, request, relationships, relationshipSet, captured.ranked)
		if err != nil {
			return err
		}
		symbolIDs := make([]string, 0, len(captured.traversal.nodes))
		for _, node := range captured.traversal.nodes {
			if node.Kind == store.NodeSymbol {
				symbolIDs = append(symbolIDs, node.ID)
			}
		}
		captured.symbols, err = snapshot.SymbolRecords(ctx, symbolIDs)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	result := Result{
		RevisionID: captured.revision.ID, IndexedCommit: captured.revision.IndexedCommit,
		ActiveCheckout: captured.projectConfig.ActiveCheckout, CanonicalRemote: captured.projectConfig.CanonicalRemote,
		IndexedFiles: append([]store.File(nil), captured.files...),
		Matches:      captured.matches, Nodes: captured.traversal.nodes, Edges: captured.traversal.edges,
		StructuralPaths: captured.traversal.paths, Completeness: CompletenessComplete,
		OmittedNodeCount:         captured.traversal.omitted + captured.droppedMatch,
		OmittedEdgeCount:         captured.traversal.omittedEdges,
		SuggestedFollowUpSymbols: appendUniqueBounded(captured.matchFollowUp, captured.traversal.followUps, maxFollowUps),
	}
	if captured.droppedMatch > 0 {
		result.OmissionReason = OmissionMatchBudget
	} else if captured.traversal.omitted > 0 {
		result.OmissionReason = OmissionNodeBudget
	} else if captured.traversal.omittedEdges > 0 {
		result.OmissionReason = OmissionEdgeBudget
	}
	if request.RequestedSourceBytes > 0 {
		assembled, err := source.Assemble(ctx, source.Request{
			Checkout: captured.projectConfig.ActiveCheckout, Authorized: request.SourceAuthorized,
			RequestedBytes: request.RequestedSourceBytes,
			ProjectCeiling: captured.projectConfig.ProjectSourceByteCeiling,
			GlobalCeiling:  service.globalSourceCeiling,
			Candidates:     sourceCandidates(captured.traversal.nodes, captured.symbols),
		})
		if err != nil {
			return Result{}, err
		}
		result.Sources = assembled.Outcomes
		result.EffectiveSourceBudget = assembled.EffectiveBudget
		result.SourceBytes = assembled.ReturnedBytes
		result.OmittedNodeCount += assembled.OmittedNodeCount
		if result.OmissionReason == "" {
			result.OmissionReason = assembled.OmissionReason
		}
		result.SuggestedFollowUpSymbols = appendUniqueBounded(result.SuggestedFollowUpSymbols, assembled.SuggestedFollowUpSymbols, maxFollowUps)
		for _, outcome := range assembled.Outcomes {
			result.RefreshRecommended = result.RefreshRecommended || outcome.RefreshRecommended
		}
	}
	if result.OmittedNodeCount > 0 || result.OmittedEdgeCount > 0 {
		result.Completeness = CompletenessPartial
	}
	return result, nil
}

func validateRequest(request Request) (Request, []store.Relationship, map[store.Relationship]struct{}, error) {
	request.Intent = strings.TrimSpace(request.Intent)
	if request.Intent == "" && len(request.EntrySymbols) == 0 {
		return Request{}, nil, nil, fmt.Errorf("%w: intent or entry symbol is required", ErrInvalidRequest)
	}
	if len(request.Intent) > MaxIntentBytes || !utf8.ValidString(request.Intent) || len(request.EntrySymbols) > MaxEntrySymbols {
		return Request{}, nil, nil, fmt.Errorf("%w: lexical input exceeds limit", ErrInvalidRequest)
	}
	for _, entry := range request.EntrySymbols {
		if entry == "" || len(entry) > 512 || !utf8.ValidString(entry) {
			return Request{}, nil, nil, fmt.Errorf("%w: invalid entry symbol", ErrInvalidRequest)
		}
	}
	if request.MaxDepth < 0 || request.MaxDepth > MaxTraversalDepth || math.IsNaN(request.MinimumConfidence) || math.IsInf(request.MinimumConfidence, 0) || request.MinimumConfidence < 0 || request.MinimumConfidence > 1 {
		return Request{}, nil, nil, fmt.Errorf("%w: traversal bound is invalid", ErrInvalidRequest)
	}
	if request.MaxNodes == 0 {
		request.MaxNodes = defaultMaxNodes
	}
	if request.MaxNodes < 1 || request.MaxNodes > MaxTraversalNodes || request.RequestedSourceBytes < 0 {
		return Request{}, nil, nil, fmt.Errorf("%w: query budget is invalid", ErrInvalidRequest)
	}
	if request.Direction == "" {
		request.Direction = DirectionBoth
	}
	if request.Direction != DirectionBoth && request.Direction != DirectionOutbound && request.Direction != DirectionInbound {
		return Request{}, nil, nil, fmt.Errorf("%w: traversal direction is invalid", ErrInvalidRequest)
	}
	allowed := map[store.Relationship]struct{}{
		store.RelationshipContains: {}, store.RelationshipDefines: {}, store.RelationshipImports: {},
		store.RelationshipCalls: {}, store.RelationshipReferences: {}, store.RelationshipUsesType: {},
	}
	set := make(map[store.Relationship]struct{}, len(request.IncludeEdges))
	for _, relationship := range request.IncludeEdges {
		if _, valid := allowed[relationship]; !valid {
			return Request{}, nil, nil, fmt.Errorf("%w: unsupported relationship %q", ErrInvalidRequest, relationship)
		}
		set[relationship] = struct{}{}
	}
	if len(set) == 0 {
		set = allowed
	}
	relationships := make([]store.Relationship, 0, len(set))
	for relationship := range set {
		relationships = append(relationships, relationship)
	}
	sort.Slice(relationships, func(i, j int) bool { return relationships[i] < relationships[j] })
	return request, relationships, set, nil
}

type structuralEvidence struct {
	found        bool
	distance     int
	relationship int
	confidence   float64
	provenance   int
}

type rankedMatch struct {
	match      Match
	record     store.SymbolRecord
	category   int
	overlap    int
	entryOrder int
	evidence   structuralEvidence
}

func rankMatches(ctx context.Context, request Request, records []store.SymbolRecord) ([]rankedMatch, error) {
	intentTokens := tokenSet(request.Intent)
	entries := make(map[string]int, len(request.EntrySymbols))
	for index, entry := range request.EntrySymbols {
		if _, exists := entries[entry]; !exists {
			entries[entry] = index
		}
	}
	ranked := make([]rankedMatch, 0, len(records))
	for index, record := range records {
		if index&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		current := rankedMatch{category: 4, entryOrder: MaxEntrySymbols + 1, record: record, match: Match{
			SymbolID: record.Symbol.ID, QualifiedName: record.Symbol.QualifiedName, FilePath: record.FilePath,
		}}
		if order, exact := entries[record.Symbol.QualifiedName]; exact {
			current.category, current.entryOrder, current.match.Reason = 0, order, MatchExactQualified
		} else if order, exact := entries[record.Name]; exact {
			current.category, current.entryOrder, current.match.Reason = 1, order, MatchExactName
		} else if overlap := tokenOverlap(intentTokens, tokenSet(record.Name+" "+record.Symbol.Signature)); overlap > 0 {
			current.category, current.overlap, current.match.Reason = 2, overlap, MatchLexicalSymbol
		} else if overlap := tokenOverlap(intentTokens, tokenSet(record.FilePath+" "+packagePart(record.Symbol.QualifiedName))); overlap > 0 {
			current.category, current.overlap, current.match.Reason = 3, overlap, MatchPackageFile
		}
		if current.category < 4 {
			ranked = append(ranked, current)
		}
	}
	return ranked, nil
}

func addStructuralEvidence(ctx context.Context, snapshot *store.Snapshot, request Request, relationships []store.Relationship, ranked []rankedMatch) error {
	var seeds []string
	for _, candidate := range ranked {
		if candidate.category <= 1 {
			seeds = append(seeds, candidate.record.Symbol.ID)
		}
	}
	if len(seeds) == 0 {
		return nil
	}
	if request.MaxDepth == 0 {
		return nil
	}
	candidates := make(map[string]int, len(ranked))
	for index := range ranked {
		candidates[ranked[index].record.Symbol.ID] = index
	}
	visited := stringSet(seeds)
	frontier := append([]string(nil), seeds...)
	edgesRead := 0
	seenEdges := make(map[string]struct{}, maxEvidenceEdges)
	for distance := 1; distance <= request.MaxDepth && len(frontier) > 0 && len(visited) < MaxTraversalNodes && edgesRead < maxEvidenceEdges; distance++ {
		edges, err := snapshot.EdgesForNodes(ctx, edgeSelection(request.Direction, frontier, seenEdges), relationships, request.MinimumConfidence, maxEvidenceEdges-edgesRead)
		if err != nil {
			return err
		}
		edgesRead += len(edges)
		sort.Slice(edges, func(i, j int) bool { return storeEdgeLess(edges[i], edges[j]) })
		frontierSet := stringSet(frontier)
		var next []string
		for _, edge := range edges {
			seenEdges[edge.ID] = struct{}{}
			_, other, traversable := traversalStep(request.Direction, frontierSet, edge)
			if !traversable {
				continue
			}
			if index, candidate := candidates[other]; candidate {
				evidence := structuralEvidence{found: true, distance: distance, relationship: relationshipStrength(edge.Relationship), confidence: edge.Confidence, provenance: provenanceStrength(edge.Provenance)}
				if !ranked[index].evidence.found || evidenceLess(evidence, ranked[index].evidence) {
					ranked[index].evidence = evidence
				}
			}
			if _, seen := visited[other]; !seen && len(visited) < MaxTraversalNodes {
				visited[other] = struct{}{}
				next = append(next, other)
			}
		}
		sort.Strings(next)
		frontier = next
	}
	return nil
}

func sortRanked(ranked []rankedMatch) {
	sort.Slice(ranked, func(i, j int) bool {
		left, right := ranked[i], ranked[j]
		if left.category != right.category {
			return left.category < right.category
		}
		if left.entryOrder != right.entryOrder {
			return left.entryOrder < right.entryOrder
		}
		if left.overlap != right.overlap {
			return left.overlap > right.overlap
		}
		if left.evidence.found != right.evidence.found {
			return left.evidence.found
		}
		if left.evidence.found && evidenceLess(left.evidence, right.evidence) {
			return true
		}
		if left.evidence.found && evidenceLess(right.evidence, left.evidence) {
			return false
		}
		if left.match.QualifiedName != right.match.QualifiedName {
			return left.match.QualifiedName < right.match.QualifiedName
		}
		return left.match.SymbolID < right.match.SymbolID
	})
}

func evidenceLess(left, right structuralEvidence) bool {
	if left.distance != right.distance {
		return left.distance < right.distance
	}
	if left.relationship != right.relationship {
		return left.relationship < right.relationship
	}
	if left.confidence != right.confidence {
		return left.confidence > right.confidence
	}
	return left.provenance < right.provenance
}

type traversalResult struct {
	nodes        []Node
	edges        []Edge
	paths        []StructuralPath
	omitted      int
	omittedEdges int
	followUps    []string
}

func traverse(ctx context.Context, snapshot *store.Snapshot, request Request, relationships []store.Relationship, relationshipSet map[store.Relationship]struct{}, ranked []rankedMatch) (traversalResult, error) {
	type queued struct {
		id       string
		distance int
	}
	visited := make(map[string]int, request.MaxNodes)
	order := make([]queued, 0, request.MaxNodes)
	omitted := make(map[string]struct{})
	qualified := make(map[string]string, len(ranked))
	for _, match := range ranked {
		qualified[match.record.Symbol.ID] = match.record.Symbol.QualifiedName
		if len(visited) >= request.MaxNodes {
			omitted[match.record.Symbol.ID] = struct{}{}
			continue
		}
		if _, exists := visited[match.record.Symbol.ID]; !exists {
			visited[match.record.Symbol.ID] = 0
			order = append(order, queued{id: match.record.Symbol.ID})
		}
	}
	seenEdges := make(map[string]struct{})
	var expandedNodes []string
	result := traversalResult{}
	frontierStart := 0
	for depth := 0; depth < request.MaxDepth && frontierStart < len(order); depth++ {
		if err := ctx.Err(); err != nil {
			return traversalResult{}, err
		}
		frontierEnd := len(order)
		frontier := make([]string, 0, frontierEnd-frontierStart)
		for _, item := range order[frontierStart:frontierEnd] {
			frontier = append(frontier, item.id)
		}
		expandedNodes = append(expandedNodes, frontier...)
		remainingEdges := MaxTraversalEdges - len(seenEdges)
		if remainingEdges == 0 {
			break
		}
		edges, err := snapshot.EdgesForNodes(ctx, edgeSelection(request.Direction, frontier, seenEdges), relationships, request.MinimumConfidence, remainingEdges)
		if err != nil {
			return traversalResult{}, err
		}
		sort.Slice(edges, func(i, j int) bool { return storeEdgeLess(edges[i], edges[j]) })
		frontierSet := stringSet(frontier)
		for _, edge := range edges {
			if _, allowed := relationshipSet[edge.Relationship]; !allowed {
				continue
			}
			from, other, traversable := traversalStep(request.Direction, frontierSet, edge)
			if !traversable {
				continue
			}
			if _, exists := visited[other]; !exists {
				if len(visited) >= request.MaxNodes {
					omitted[other] = struct{}{}
					continue
				}
				visited[other] = depth + 1
				order = append(order, queued{id: other, distance: depth + 1})
			}
			if _, exists := seenEdges[edge.ID]; exists {
				continue
			}
			if _, sourceIncluded := visited[edge.SourceNodeID]; !sourceIncluded {
				continue
			}
			if _, targetIncluded := visited[edge.TargetNodeID]; !targetIncluded {
				continue
			}
			seenEdges[edge.ID] = struct{}{}
			result.edges = append(result.edges, Edge{ID: edge.ID, SourceNodeID: edge.SourceNodeID, TargetNodeID: edge.TargetNodeID, Relationship: edge.Relationship, Confidence: edge.Confidence, Provenance: edge.Provenance, Authoritative: edge.Provenance != store.ProvenanceHeuristic})
			result.paths = append(result.paths, StructuralPath{FromNodeID: from, EdgeID: edge.ID, ToNodeID: other, Depth: depth + 1})
		}
		frontierStart = frontierEnd
	}
	if len(expandedNodes) > 0 {
		omittedEdges, err := snapshot.CountEdgesForNodes(ctx, edgeSelection(request.Direction, expandedNodes, seenEdges), relationships, request.MinimumConfidence)
		if err != nil {
			return traversalResult{}, err
		}
		result.omittedEdges = omittedEdges
	}
	ids := make([]string, 0, len(order))
	for _, item := range order {
		ids = append(ids, item.id)
	}
	nodes, err := snapshot.NodesByIDs(ctx, ids)
	if err != nil {
		return traversalResult{}, err
	}
	nodeByID := make(map[string]store.Node, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	for _, item := range order {
		node, exists := nodeByID[item.id]
		if !exists {
			return traversalResult{}, fmt.Errorf("codegraph query: traversed node missing from pinned revision")
		}
		result.nodes = append(result.nodes, Node{ID: node.ID, Kind: node.Kind, Name: node.Name, Path: node.Path, Distance: item.distance})
	}
	result.omitted = len(omitted)
	omittedIDs := make([]string, 0, len(omitted))
	for id := range omitted {
		omittedIDs = append(omittedIDs, id)
	}
	sort.Strings(omittedIDs)
	if len(omittedIDs) > 0 {
		records, err := snapshot.SymbolRecords(ctx, omittedIDs)
		if err != nil {
			return traversalResult{}, err
		}
		for _, record := range records {
			qualified[record.Symbol.ID] = record.Symbol.QualifiedName
		}
	}
	for _, id := range omittedIDs {
		if value := qualified[id]; value != "" && len(result.followUps) < maxFollowUps {
			result.followUps = append(result.followUps, value)
		}
	}
	return result, nil
}

func sourceCandidates(nodes []Node, records []store.SymbolRecord) []source.Candidate {
	recordByID := make(map[string]store.SymbolRecord, len(records))
	for _, record := range records {
		recordByID[record.Symbol.ID] = record
	}
	result := make([]source.Candidate, 0, len(records))
	for rank, node := range nodes {
		record, exists := recordByID[node.ID]
		if !exists {
			continue
		}
		result = append(result, source.Candidate{SymbolID: node.ID, QualifiedName: record.Symbol.QualifiedName, FilePath: record.FilePath, IndexedHash: record.IndexedHash, IndexedByteSize: record.FileByteSize, StartByte: record.Symbol.StartByte, EndByte: record.Symbol.EndByte, StartLine: record.Symbol.StartLine, EndLine: record.Symbol.EndLine, Rank: rank + 1})
	}
	return result
}

func traversalStep(direction Direction, frontier map[string]struct{}, edge store.Edge) (string, string, bool) {
	_, source := frontier[edge.SourceNodeID]
	_, target := frontier[edge.TargetNodeID]
	if source && (direction == DirectionBoth || direction == DirectionOutbound) {
		return edge.SourceNodeID, edge.TargetNodeID, true
	}
	if target && (direction == DirectionBoth || direction == DirectionInbound) {
		return edge.TargetNodeID, edge.SourceNodeID, true
	}
	if target && direction == DirectionBoth {
		return edge.TargetNodeID, edge.SourceNodeID, true
	}
	return "", "", false
}

func storeEdgeLess(left, right store.Edge) bool {
	if relationshipStrength(left.Relationship) != relationshipStrength(right.Relationship) {
		return relationshipStrength(left.Relationship) < relationshipStrength(right.Relationship)
	}
	if left.Confidence != right.Confidence {
		return left.Confidence > right.Confidence
	}
	if provenanceStrength(left.Provenance) != provenanceStrength(right.Provenance) {
		return provenanceStrength(left.Provenance) < provenanceStrength(right.Provenance)
	}
	return left.ID < right.ID
}

func relationshipStrength(value store.Relationship) int {
	switch value {
	case store.RelationshipCalls:
		return 0
	case store.RelationshipReferences:
		return 1
	case store.RelationshipUsesType:
		return 2
	case store.RelationshipImports:
		return 3
	case store.RelationshipDefines:
		return 4
	default:
		return 5
	}
}

func provenanceStrength(value store.Provenance) int {
	switch value {
	case store.ProvenanceGoTypes:
		return 0
	case store.ProvenanceGoPackages:
		return 1
	case store.ProvenanceGoAST:
		return 2
	case store.ProvenanceParser:
		return 3
	default:
		return 4
	}
}

func tokenSet(value string) map[string]struct{} {
	words := tokenize(value)
	result := make(map[string]struct{}, len(words))
	for _, word := range words {
		if word != "" {
			result[word] = struct{}{}
		}
	}
	return result
}

func tokenize(value string) []string {
	runes := []rune(value)
	var result []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			result = append(result, strings.ToLower(string(runes[start:end])))
		}
		start = -1
	}
	for index, character := range runes {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			flush(index)
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		previous := runes[index-1]
		var next rune
		if index+1 < len(runes) {
			next = runes[index+1]
		}
		camel := unicode.IsUpper(character) && (unicode.IsLower(previous) || unicode.IsDigit(previous))
		acronym := unicode.IsUpper(character) && unicode.IsUpper(previous) && next != 0 && unicode.IsLower(next)
		if camel || acronym {
			flush(index)
			start = index
		}
	}
	flush(len(runes))
	return result
}

func sortedTokens(value string) []string {
	values := tokenize(value)
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	values = values[:0]
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
func tokenOverlap(left, right map[string]struct{}) int {
	count := 0
	for value := range left {
		if _, ok := right[value]; ok {
			count++
		}
	}
	return count
}
func packagePart(value string) string {
	if index := strings.LastIndex(value, "."); index >= 0 {
		return value[:index]
	}
	return value
}
func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
func edgeSelection(direction Direction, frontier []string, excluded map[string]struct{}) store.EdgeSelection {
	selection := store.EdgeSelection{ExcludedEdgeIDs: make([]string, 0, len(excluded))}
	switch direction {
	case DirectionOutbound:
		selection.SourceNodeIDs = frontier
	case DirectionInbound:
		selection.TargetNodeIDs = frontier
	default:
		selection.SourceNodeIDs = frontier
		selection.TargetNodeIDs = frontier
	}
	for id := range excluded {
		selection.ExcludedEdgeIDs = append(selection.ExcludedEdgeIDs, id)
	}
	sort.Strings(selection.ExcludedEdgeIDs)
	return selection
}
func containsSet(values map[string]struct{}, value string) bool { _, ok := values[value]; return ok }
func minimumInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
func appendUniqueBounded(existing, additional []string, maximum int) []string {
	set := stringSet(existing)
	for _, value := range additional {
		if _, ok := set[value]; !ok && len(existing) < maximum {
			existing = append(existing, value)
			set[value] = struct{}{}
		}
	}
	return existing
}
