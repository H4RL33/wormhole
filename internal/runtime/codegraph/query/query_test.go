package query_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/source"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestExecuteRanksExactThenLexicalThenPackageFileMatches(t *testing.T) {
	service, checkout := newQueryFixture(t)
	request := query.Request{
		Intent:       "trace registration authentication",
		EntrySymbols: []string{"example.com/app/auth.RegisterAgent"},
		MaxDepth:     0,
		MaxNodes:     20,
	}
	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Matches) < 3 {
		t.Fatalf("matches = %#v", result.Matches)
	}
	want := []struct {
		qualified string
		reason    query.MatchReason
	}{
		{"example.com/app/auth.RegisterAgent", query.MatchExactQualified},
		{"example.com/app/auth.AuthenticateRegistration", query.MatchLexicalSymbol},
		{"example.com/app/registration.Helper", query.MatchPackageFile},
	}
	for index, expected := range want {
		if result.Matches[index].QualifiedName != expected.qualified || result.Matches[index].Reason != expected.reason {
			t.Fatalf("match[%d] = %#v, want %q/%q", index, result.Matches[index], expected.qualified, expected.reason)
		}
	}
	if result.RevisionID != "revision-query" || result.IndexedCommit != strings.Repeat("a", 40) {
		t.Fatalf("revision identity = %#v", result)
	}
	if result.ActiveCheckout != checkout || result.CanonicalRemote != "https://example.com/app.git" {
		t.Fatalf("pinned freshness basis = %q/%q", result.ActiveCheckout, result.CanonicalRemote)
	}
	if result.Completeness == "" || result.OmittedNodeCount != 0 || result.OmissionReason != "" {
		t.Fatalf("completeness metadata = %#v", result)
	}
}

func TestExecuteRanksExactUnqualifiedName(t *testing.T) {
	service, _ := newQueryFixture(t)
	result, err := service.Execute(context.Background(), query.Request{EntrySymbols: []string{"RegisterAgent"}, MaxNodes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Reason != query.MatchExactName || result.Matches[0].QualifiedName != "example.com/app/auth.RegisterAgent" {
		t.Fatalf("exact-name result = %#v", result.Matches)
	}
}

func TestExecuteUsesStructuralDistanceAfterLexicalTier(t *testing.T) {
	service, _ := newQueryFixture(t)
	result, err := service.Execute(context.Background(), query.Request{
		Intent: "worker", EntrySymbols: []string{"example.com/app/auth.RegisterAgent"},
		IncludeEdges: []store.Relationship{store.RelationshipCalls}, Direction: query.DirectionOutbound,
		MaxDepth: 2, MaxNodes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/app/auth.RegisterAgent", "example.com/app/auth.ZuluWorker", "example.com/app/auth.AlphaWorker"}
	for index, qualified := range want {
		if len(result.Matches) <= index || result.Matches[index].QualifiedName != qualified {
			t.Fatalf("structural ranking = %#v, want prefix %v", result.Matches, want)
		}
	}
}

func TestExecuteFiltersTraversalAndMarksHeuristicEdgesNonAuthoritative(t *testing.T) {
	service, _ := newQueryFixture(t)
	request := query.Request{
		EntrySymbols:      []string{"RegisterAgent"},
		IncludeEdges:      []store.Relationship{store.RelationshipCalls, store.RelationshipUsesType},
		Direction:         query.DirectionOutbound,
		MaxDepth:          2,
		MinimumConfidence: 0.8,
		MaxNodes:          10,
	}
	first, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("query is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	wantNodes := []string{"register", "authenticate", "zulu-worker", "helper", "token", "alpha-worker"}
	if got := resultNodeIDs(first.Nodes); !reflect.DeepEqual(got, wantNodes) {
		t.Fatalf("traversed nodes = %v, want %v", got, wantNodes)
	}
	if len(first.Edges) != 6 {
		t.Fatalf("edges = %#v, want five calls plus uses_type", first.Edges)
	}
	nodeSet := make(map[string]struct{}, len(first.Nodes))
	for _, node := range first.Nodes {
		nodeSet[node.ID] = struct{}{}
	}
	var heuristicSeen bool
	for _, edge := range first.Edges {
		if _, exists := nodeSet[edge.SourceNodeID]; !exists {
			t.Fatalf("edge source missing from nodes: %#v", edge)
		}
		if _, exists := nodeSet[edge.TargetNodeID]; !exists {
			t.Fatalf("edge target missing from nodes: %#v", edge)
		}
		if edge.Relationship == store.RelationshipReferences || edge.Confidence < 0.8 {
			t.Fatalf("edge escaped filters: %#v", edge)
		}
		if edge.Provenance == store.ProvenanceHeuristic {
			heuristicSeen = true
			if edge.Authoritative {
				t.Fatalf("heuristic edge presented as authoritative: %#v", edge)
			}
		} else if !edge.Authoritative {
			t.Fatalf("exact edge presented as non-authoritative: %#v", edge)
		}
	}
	if !heuristicSeen {
		t.Fatal("heuristic edge absent")
	}
	if first.RevisionID != "revision-query" {
		t.Fatalf("mixed or absent revision: %q", first.RevisionID)
	}
}

func TestExecuteDeduplicatesCyclesAndKeepsCompetingPaths(t *testing.T) {
	service, _ := newQueryFixture(t)
	result, err := service.Execute(context.Background(), query.Request{
		EntrySymbols: []string{"RegisterAgent"},
		IncludeEdges: []store.Relationship{store.RelationshipCalls, store.RelationshipUsesType},
		Direction:    query.DirectionOutbound, MaxDepth: 3, MaxNodes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[string]int, len(result.Nodes))
	for _, node := range result.Nodes {
		nodes[node.ID]++
		if nodes[node.ID] != 1 {
			t.Fatalf("duplicate node in cycle: %#v", result.Nodes)
		}
	}
	edges := make(map[string]int, len(result.Edges))
	var cycleSeen, callsToken, usesToken bool
	for _, edge := range result.Edges {
		edges[edge.ID]++
		if edges[edge.ID] != 1 {
			t.Fatalf("duplicate edge in cycle: %#v", result.Edges)
		}
		cycleSeen = cycleSeen || edge.ID == "token-calls-register"
		callsToken = callsToken || edge.ID == "zulu-calls-token"
		usesToken = usesToken || edge.ID == "auth-uses-token"
	}
	if !cycleSeen || !callsToken || !usesToken || nodes["token"] != 1 {
		t.Fatalf("cycle/competing paths missing: nodes=%#v edges=%#v", result.Nodes, result.Edges)
	}
}

func TestExecuteAppliesDirectionBeforeFanoutLimit(t *testing.T) {
	tests := []struct {
		name      string
		direction query.Direction
		extraEdge func(int) store.Edge
		wantNode  string
	}{
		{
			name: "outbound ignores inbound fanout", direction: query.DirectionOutbound, wantNode: "authenticate",
			extraEdge: func(index int) store.Edge {
				return store.Edge{ID: fmt.Sprintf("aa-inbound-%04d", index), SourceNodeID: "token", TargetNodeID: "register", Relationship: store.RelationshipCalls, Confidence: 1, Provenance: store.ProvenanceGoTypes}
			},
		},
		{
			name: "inbound ignores outbound fanout", direction: query.DirectionInbound, wantNode: "token",
			extraEdge: func(index int) store.Edge {
				return store.Edge{ID: fmt.Sprintf("aa-outbound-%04d", index), SourceNodeID: "register", TargetNodeID: "authenticate", Relationship: store.RelationshipCalls, Confidence: 1, Provenance: store.ProvenanceGoTypes}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			edges := make([]store.Edge, query.MaxTraversalEdges+1)
			for index := range edges {
				edges[index] = test.extraEdge(index)
			}
			service, _ := newQueryFixtureWithEdges(t, edges)
			result, err := service.Execute(context.Background(), query.Request{
				EntrySymbols: []string{"RegisterAgent"}, IncludeEdges: []store.Relationship{store.RelationshipCalls},
				Direction: test.direction, MaxDepth: 1, MaxNodes: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !hasResultNode(result.Nodes, test.wantNode) || result.OmittedEdgeCount != 0 {
				t.Fatalf("directional fanout result = nodes:%v edges:%d omitted:%d", resultNodeIDs(result.Nodes), len(result.Edges), result.OmittedEdgeCount)
			}
		})
	}
}

func TestExecuteReportsExactFilteredOmittedEdgeCount(t *testing.T) {
	edges := make([]store.Edge, query.MaxTraversalEdges+2)
	for index := range edges {
		edges[index] = store.Edge{
			ID: fmt.Sprintf("aa-outbound-%04d", index), SourceNodeID: "register", TargetNodeID: "authenticate",
			Relationship: store.RelationshipCalls, Confidence: 1, Provenance: store.ProvenanceGoTypes,
		}
	}
	service, _ := newQueryFixtureWithEdges(t, edges)
	result, err := service.Execute(context.Background(), query.Request{
		EntrySymbols: []string{"RegisterAgent"}, IncludeEdges: []store.Relationship{store.RelationshipCalls},
		Direction: query.DirectionOutbound, MaxDepth: 1, MaxNodes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture has 4,002 added outbound edges plus the two ordinary
	// register outbound call edges, so exactly four do not fit the 4,000 cap.
	if result.OmittedEdgeCount != 4 || result.OmissionReason != query.OmissionEdgeBudget || result.Completeness != query.CompletenessPartial {
		t.Fatalf("edge omission metadata = omitted:%d reason:%q completeness:%q returned:%d", result.OmittedEdgeCount, result.OmissionReason, result.Completeness, len(result.Edges))
	}
}

func TestExecuteDirectionBothCountsOmittedEdgesOnce(t *testing.T) {
	edges := make([]store.Edge, 5_000)
	for index := range edges {
		edges[index] = store.Edge{
			ID: fmt.Sprintf("both-%04d", index), SourceNodeID: "register", TargetNodeID: "authenticate",
			Relationship: store.RelationshipImports, Confidence: 1, Provenance: store.ProvenanceGoTypes,
		}
	}
	service, _ := newQueryFixtureWithEdges(t, edges)
	result, err := service.Execute(context.Background(), query.Request{
		EntrySymbols: []string{"RegisterAgent"}, IncludeEdges: []store.Relationship{store.RelationshipImports},
		Direction: query.DirectionBoth, MaxDepth: 2, MaxNodes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edges) != query.MaxTraversalEdges || result.OmittedEdgeCount != 1_000 {
		t.Fatalf("DirectionBoth edge accounting = returned:%d omitted:%d, want %d/1000", len(result.Edges), result.OmittedEdgeCount, query.MaxTraversalEdges)
	}
}

func TestExecuteHonorsDepthAndDirection(t *testing.T) {
	service, _ := newQueryFixture(t)
	tests := []struct {
		name      string
		direction query.Direction
		depth     int
		wantNodes []string
	}{
		{name: "depth zero", direction: query.DirectionOutbound, depth: 0, wantNodes: []string{"register"}},
		{name: "outbound depth one", direction: query.DirectionOutbound, depth: 1, wantNodes: []string{"register", "authenticate", "zulu-worker"}},
		{name: "inbound", direction: query.DirectionInbound, depth: 2, wantNodes: []string{"register", "token", "zulu-worker"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := service.Execute(context.Background(), query.Request{
				EntrySymbols: []string{"RegisterAgent"}, IncludeEdges: []store.Relationship{store.RelationshipCalls},
				Direction: test.direction, MaxDepth: test.depth, MaxNodes: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := resultNodeIDs(result.Nodes); !reflect.DeepEqual(got, test.wantNodes) {
				t.Fatalf("nodes = %v, want %v", got, test.wantNodes)
			}
		})
	}
}

func TestExecuteIntegratesAuthorizedDeniedAndStaleSource(t *testing.T) {
	service, checkout := newQueryFixture(t)
	request := query.Request{
		EntrySymbols: []string{"example.com/app/auth.RegisterAgent"}, MaxNodes: 1,
		RequestedSourceBytes: 40, SourceAuthorized: true,
	}
	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveSourceBudget != 40 || result.SourceBytes != 40 || len(result.Sources) != 1 || !result.Sources[0].SourceIncluded || result.Sources[0].Source != strings.Repeat("x", 40) {
		t.Fatalf("authorized source = %#v", result)
	}
	request.SourceAuthorized = false
	denied, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Completeness != query.CompletenessPartial || len(denied.Sources) != 1 || denied.Sources[0].OmissionReason != source.OmissionMissingPermission || denied.Sources[0].RequiredPermission != "code_graph.source.read" {
		t.Fatalf("denied source = %#v", denied)
	}
	if err := os.WriteFile(filepath.Join(checkout, "register.go"), []byte(strings.Repeat("y", 512)), 0o644); err != nil {
		t.Fatal(err)
	}
	request.SourceAuthorized = true
	stale, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.RefreshRecommended || stale.Sources[0].OmissionReason != source.OmissionWorkingTreeChanged || stale.Sources[0].SourceIncluded {
		t.Fatalf("stale source = %#v", stale)
	}
}

func TestExecuteCapsLargeCandidateSetAndReportsStableOmissions(t *testing.T) {
	service, _ := newQueryFixtureWithExtraSymbols(t, 600)
	request := query.Request{Intent: "worker", EntrySymbols: []string{"example.com/app/auth.RegisterAgent"}, MaxNodes: 100}
	first, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("large bounded query was not deterministic")
	}
	if len(first.Matches) != query.MaxMatchResults || len(first.Nodes) != query.MaxMatchResults || first.OmittedNodeCount != 539 || first.OmissionReason != query.OmissionMatchBudget || len(first.SuggestedFollowUpSymbols) == 0 {
		t.Fatalf("bounded result = matches:%d nodes:%d omitted:%d reason:%q followups:%v", len(first.Matches), len(first.Nodes), first.OmittedNodeCount, first.OmissionReason, first.SuggestedFollowUpSymbols)
	}
	if first.Matches[0].Reason != query.MatchExactQualified {
		t.Fatalf("exact entry fell outside SQL candidate cap: %#v", first.Matches[0])
	}
}

func TestExecuteReportsBoundedTraversalOmissionsAndFollowups(t *testing.T) {
	service, _ := newQueryFixture(t)
	result, err := service.Execute(context.Background(), query.Request{
		EntrySymbols: []string{"RegisterAgent"}, IncludeEdges: []store.Relationship{store.RelationshipCalls, store.RelationshipUsesType},
		MaxDepth: 2, MinimumConfidence: 0.8, MaxNodes: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completeness != query.CompletenessPartial || result.OmittedNodeCount != 4 || result.OmissionReason != query.OmissionNodeBudget {
		t.Fatalf("bounded completeness = %#v", result)
	}
	wantFollowUps := []string{
		"example.com/app/auth.AlphaWorker", "example.com/app/registration.Helper",
		"example.com/app/auth.Token", "example.com/app/auth.ZuluWorker",
	}
	if !reflect.DeepEqual(result.SuggestedFollowUpSymbols, wantFollowUps) {
		t.Fatalf("follow-up symbols = %v, want %v", result.SuggestedFollowUpSymbols, wantFollowUps)
	}
}

func TestExecuteRejectsInvalidGrammar(t *testing.T) {
	service, _ := newQueryFixture(t)
	tests := []query.Request{
		{},
		{Intent: "x", IncludeEdges: []store.Relationship{"unknown"}, MaxNodes: 1},
		{Intent: "x", MaxDepth: query.MaxTraversalDepth + 1, MaxNodes: 1},
		{Intent: "x", MinimumConfidence: 1.1, MaxNodes: 1},
		{Intent: "x", MinimumConfidence: math.NaN(), MaxNodes: 1},
		{Intent: "x", MinimumConfidence: math.Inf(1), MaxNodes: 1},
		{Intent: "x", MaxNodes: query.MaxTraversalNodes + 1},
	}
	for _, request := range tests {
		if _, err := service.Execute(context.Background(), request); !errors.Is(err, query.ErrInvalidRequest) {
			t.Errorf("Execute(%#v) error = %v, want ErrInvalidRequest", request, err)
		}
	}
}

func newQueryFixture(t *testing.T) (*query.Service, string) {
	return newQueryFixtureWithOptions(t, 0, nil)
}

func newQueryFixtureWithExtraSymbols(t *testing.T, extraSymbols int) (*query.Service, string) {
	return newQueryFixtureWithOptions(t, extraSymbols, nil)
}

func newQueryFixtureWithEdges(t *testing.T, extraEdges []store.Edge) (*query.Service, string) {
	return newQueryFixtureWithOptions(t, 0, extraEdges)
}

func newQueryFixtureWithOptions(t *testing.T, extraSymbols int, extraEdges []store.Edge) (*query.Service, string) {
	t.Helper()
	checkout := t.TempDir()
	content := []byte(strings.Repeat("x", 512))
	if err := os.WriteFile(filepath.Join(checkout, "register.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "registration.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "gateway.db"), OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	graphStore, err := store.Open(context.Background(), db, "project-query")
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutProjectConfig(context.Background(), config.Project{
		ProjectID: "project-query", Enabled: true, CanonicalRemote: "https://example.com/app.git", ActiveCheckout: checkout,
		ProjectSourceByteCeiling: 256,
	}); err != nil {
		t.Fatal(err)
	}
	seedQueryCandidate(t, graphStore, content, extraSymbols, extraEdges)
	return query.New(graphStore, 512), checkout
}

func seedQueryCandidate(t *testing.T, graphStore *store.Store, content []byte, extraSymbols int, extraEdges []store.Edge) {
	t.Helper()
	ctx := context.Background()
	const revisionID = "revision-query"
	if err := graphStore.CreateCandidate(ctx, store.Revision{
		ProjectID: "project-query", ID: revisionID, IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	nodes := []store.Node{
		{ID: "repository", Kind: store.NodeRepository, Name: "app"},
		{ID: "package-auth", Kind: store.NodePackage, Name: "example.com/app/auth", Path: "example.com/app/auth"},
		{ID: "package-registration", Kind: store.NodePackage, Name: "example.com/app/registration", Path: "example.com/app/registration"},
		{ID: "file-auth", Kind: store.NodeFile, Name: "register.go", Path: "register.go"},
		{ID: "file-registration", Kind: store.NodeFile, Name: "registration.go", Path: "registration.go"},
		{ID: "register", Kind: store.NodeSymbol, Name: "RegisterAgent", Path: "register.go"},
		{ID: "authenticate", Kind: store.NodeSymbol, Name: "AuthenticateRegistration", Path: "register.go"},
		{ID: "token", Kind: store.NodeSymbol, Name: "Token", Path: "register.go"},
		{ID: "helper", Kind: store.NodeSymbol, Name: "Helper", Path: "registration.go"},
		{ID: "unrelated", Kind: store.NodeSymbol, Name: "Unrelated", Path: "register.go"},
		{ID: "alpha-worker", Kind: store.NodeSymbol, Name: "AlphaWorker", Path: "register.go"},
		{ID: "zulu-worker", Kind: store.NodeSymbol, Name: "ZuluWorker", Path: "register.go"},
	}
	for index := 0; index < extraSymbols; index++ {
		id := fmt.Sprintf("bulk-%04d", index)
		nodes = append(nodes, store.Node{ID: id, Kind: store.NodeSymbol, Name: fmt.Sprintf("Bulk%04dWorker", index), Path: "register.go"})
	}
	for _, node := range nodes {
		node.ProjectID, node.RevisionID = "project-query", revisionID
		if err := graphStore.PutNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(content)
	indexedHash := "sha256:" + hex.EncodeToString(digest[:])
	for _, file := range []store.File{
		{ID: "file-auth", Path: "register.go", IndexedHash: indexedHash, ByteSize: int64(len(content))},
		{ID: "file-registration", Path: "registration.go", IndexedHash: indexedHash, ByteSize: int64(len(content))},
	} {
		file.ProjectID, file.RevisionID = "project-query", revisionID
		if err := graphStore.PutFile(ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	symbols := []store.Symbol{
		{ID: "register", FileID: "file-auth", QualifiedName: "example.com/app/auth.RegisterAgent", Signature: "func RegisterAgent()", StartByte: 0, EndByte: 40, StartLine: 1, EndLine: 1},
		{ID: "authenticate", FileID: "file-auth", QualifiedName: "example.com/app/auth.AuthenticateRegistration", Signature: "func AuthenticateRegistration()", StartByte: 40, EndByte: 90, StartLine: 1, EndLine: 1},
		{ID: "token", FileID: "file-auth", QualifiedName: "example.com/app/auth.Token", Signature: "type Token struct{}", StartByte: 90, EndByte: 120, StartLine: 1, EndLine: 1},
		{ID: "helper", FileID: "file-registration", QualifiedName: "example.com/app/registration.Helper", Signature: "func Helper()", StartByte: 0, EndByte: 30, StartLine: 1, EndLine: 1},
		{ID: "unrelated", FileID: "file-auth", QualifiedName: "example.com/app/auth.Unrelated", Signature: "func Unrelated()", StartByte: 120, EndByte: 150, StartLine: 1, EndLine: 1},
		{ID: "alpha-worker", FileID: "file-auth", QualifiedName: "example.com/app/auth.AlphaWorker", Signature: "func AlphaWorker()", StartByte: 150, EndByte: 180, StartLine: 1, EndLine: 1},
		{ID: "zulu-worker", FileID: "file-auth", QualifiedName: "example.com/app/auth.ZuluWorker", Signature: "func ZuluWorker()", StartByte: 180, EndByte: 210, StartLine: 1, EndLine: 1},
	}
	for index := 0; index < extraSymbols; index++ {
		id := fmt.Sprintf("bulk-%04d", index)
		symbols = append(symbols, store.Symbol{ID: id, FileID: "file-auth", QualifiedName: fmt.Sprintf("example.com/app/auth.Bulk%04dWorker", index), Signature: fmt.Sprintf("func Bulk%04dWorker()", index), StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1})
	}
	for _, symbol := range symbols {
		symbol.ProjectID, symbol.RevisionID = "project-query", revisionID
		if err := graphStore.PutSymbol(ctx, symbol); err != nil {
			t.Fatal(err)
		}
	}
	edges := []store.Edge{
		{ID: "repo-auth", SourceNodeID: "repository", TargetNodeID: "package-auth", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoPackages},
		{ID: "repo-reg", SourceNodeID: "repository", TargetNodeID: "package-registration", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoPackages},
		{ID: "auth-file", SourceNodeID: "package-auth", TargetNodeID: "file-auth", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoPackages},
		{ID: "reg-file", SourceNodeID: "package-registration", TargetNodeID: "file-registration", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoPackages},
		{ID: "file-register", SourceNodeID: "file-auth", TargetNodeID: "register", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST},
		{ID: "file-authenticate", SourceNodeID: "file-auth", TargetNodeID: "authenticate", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST},
		{ID: "file-token", SourceNodeID: "file-auth", TargetNodeID: "token", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST},
		{ID: "file-helper", SourceNodeID: "file-registration", TargetNodeID: "helper", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST},
		{ID: "file-unrelated", SourceNodeID: "file-auth", TargetNodeID: "unrelated", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST},
		{ID: "register-calls-auth", SourceNodeID: "register", TargetNodeID: "authenticate", Relationship: store.RelationshipCalls, Confidence: 1, Provenance: store.ProvenanceGoTypes},
		{ID: "auth-uses-token", SourceNodeID: "authenticate", TargetNodeID: "token", Relationship: store.RelationshipUsesType, Confidence: 0.9, Provenance: store.ProvenanceGoTypes},
		{ID: "auth-ref-helper", SourceNodeID: "authenticate", TargetNodeID: "helper", Relationship: store.RelationshipReferences, Confidence: 0.5, Provenance: store.ProvenanceGoTypes},
		{ID: "auth-heuristic-helper", SourceNodeID: "authenticate", TargetNodeID: "helper", Relationship: store.RelationshipCalls, Confidence: 0.95, Provenance: store.ProvenanceHeuristic},
		{ID: "register-calls-zulu", SourceNodeID: "register", TargetNodeID: "zulu-worker", Relationship: store.RelationshipCalls, Confidence: 0.8, Provenance: store.ProvenanceGoAST},
		{ID: "auth-calls-alpha", SourceNodeID: "authenticate", TargetNodeID: "alpha-worker", Relationship: store.RelationshipCalls, Confidence: 0.8, Provenance: store.ProvenanceGoAST},
		{ID: "zulu-calls-token", SourceNodeID: "zulu-worker", TargetNodeID: "token", Relationship: store.RelationshipCalls, Confidence: 0.85, Provenance: store.ProvenanceGoTypes},
		{ID: "token-calls-register", SourceNodeID: "token", TargetNodeID: "register", Relationship: store.RelationshipCalls, Confidence: 1, Provenance: store.ProvenanceGoTypes},
	}
	edges = append(edges, extraEdges...)
	for _, edge := range edges {
		edge.ProjectID, edge.RevisionID = "project-query", revisionID
		if err := graphStore.PutEdge(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	if err := index.New(graphStore).Publish(ctx, revisionID); err != nil {
		t.Fatal(err)
	}
}

func resultNodeIDs(nodes []query.Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func hasResultNode(nodes []query.Node, id string) bool {
	for _, node := range nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}
