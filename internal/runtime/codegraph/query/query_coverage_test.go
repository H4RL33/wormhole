package query

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

func TestTraversalAndRankingHelpersCoverEveryDirectionAndTieBreak(t *testing.T) {
	edge := store.Edge{ID: "edge", SourceNodeID: "source", TargetNodeID: "target"}
	for _, test := range []struct {
		direction Direction
		frontier  map[string]struct{}
		from      string
		to        string
		ok        bool
	}{
		{DirectionOutbound, stringSet([]string{"source"}), "source", "target", true},
		{DirectionInbound, stringSet([]string{"target"}), "target", "source", true},
		{DirectionBoth, stringSet([]string{"source"}), "source", "target", true},
		{DirectionBoth, stringSet([]string{"target"}), "target", "source", true},
		{DirectionInbound, stringSet([]string{"source"}), "", "", false},
	} {
		from, to, ok := traversalStep(test.direction, test.frontier, edge)
		if from != test.from || to != test.to || ok != test.ok {
			t.Fatalf("traversalStep(%q) = %q/%q/%t, want %q/%q/%t", test.direction, from, to, ok, test.from, test.to, test.ok)
		}
	}

	base := rankedMatch{category: 2, entryOrder: 2, overlap: 2, match: Match{QualifiedName: "z.Symbol", SymbolID: "z"}}
	tests := []struct {
		name  string
		left  rankedMatch
		right rankedMatch
	}{
		{"category", func() rankedMatch { v := base; v.category = 1; return v }(), base},
		{"entry order", func() rankedMatch { v := base; v.entryOrder = 1; return v }(), base},
		{"overlap", func() rankedMatch { v := base; v.overlap = 3; return v }(), base},
		{"evidence present", func() rankedMatch { v := base; v.evidence.found = true; return v }(), base},
		{"qualified name", func() rankedMatch { v := base; v.match.QualifiedName = "a.Symbol"; return v }(), base},
		{"symbol id", func() rankedMatch { v := base; v.match.SymbolID = "a"; return v }(), base},
	}
	for _, test := range tests {
		values := []rankedMatch{test.right, test.left}
		sortRanked(values)
		if !reflect.DeepEqual(values[0], test.left) {
			t.Fatalf("%s tie break ordered %+v before %+v", test.name, values[0], test.left)
		}
	}
}

func TestQueryValidationRejectsEveryBoundAndUnavailableServiceShape(t *testing.T) {
	invalid := []Request{
		{Intent: strings.Repeat("x", MaxIntentBytes+1)},
		{EntrySymbols: []string{""}},
		{Intent: "intent", MaxDepth: MaxTraversalDepth + 1},
		{Intent: "intent", MinimumConfidence: math.NaN()},
		{Intent: "intent", MaxNodes: MaxTraversalNodes + 1},
		{Intent: "intent", Direction: "sideways"},
		{Intent: "intent", IncludeEdges: []store.Relationship{"unsupported"}},
	}
	for index, request := range invalid {
		if _, _, _, err := validateRequest(request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid request %d error = %v", index, err)
		}
	}
	if _, err := (*Service)(nil).Execute(context.Background(), Request{Intent: "intent"}); err == nil {
		t.Fatal("nil query service unexpectedly executed")
	}
	service := &Service{store: &store.Store{}, globalSourceCeiling: 0}
	if _, err := service.Execute(context.Background(), Request{Intent: "intent", RequestedSourceBytes: 1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid global source ceiling error = %v", err)
	}

	if got := packagePart("NoPackage"); got != "NoPackage" {
		t.Fatalf("packagePart = %q", got)
	}
}

func TestCodeGraphEdgeStrengthAndSelectionOrderingIsDeterministic(t *testing.T) {
	relationships := []store.Relationship{
		store.RelationshipCalls, store.RelationshipReferences, store.RelationshipUsesType,
		store.RelationshipImports, store.RelationshipDefines, store.RelationshipContains,
	}
	for index, relationship := range relationships {
		if got := relationshipStrength(relationship); got != index {
			t.Fatalf("relationshipStrength(%q) = %d, want %d", relationship, got, index)
		}
	}
	provenances := []store.Provenance{
		store.ProvenanceGoTypes, store.ProvenanceGoPackages, store.ProvenanceGoAST,
		store.ProvenanceParser, store.ProvenanceHeuristic,
	}
	for index, provenance := range provenances {
		if got := provenanceStrength(provenance); got != index {
			t.Fatalf("provenanceStrength(%q) = %d, want %d", provenance, got, index)
		}
	}
	base := store.Edge{ID: "z", Relationship: store.RelationshipReferences, Confidence: .5, Provenance: store.ProvenanceParser}
	for _, stronger := range []store.Edge{
		{ID: "z", Relationship: store.RelationshipCalls, Confidence: .5, Provenance: store.ProvenanceParser},
		{ID: "z", Relationship: store.RelationshipReferences, Confidence: .9, Provenance: store.ProvenanceParser},
		{ID: "z", Relationship: store.RelationshipReferences, Confidence: .5, Provenance: store.ProvenanceGoTypes},
		{ID: "a", Relationship: store.RelationshipReferences, Confidence: .5, Provenance: store.ProvenanceParser},
	} {
		if !storeEdgeLess(stronger, base) {
			t.Fatalf("storeEdgeLess(%+v, %+v) = false", stronger, base)
		}
	}
	for _, direction := range []Direction{DirectionOutbound, DirectionInbound, DirectionBoth} {
		selection := edgeSelection(direction, []string{"node"}, map[string]struct{}{"z": {}, "a": {}})
		if !reflect.DeepEqual(selection.ExcludedEdgeIDs, []string{"a", "z"}) {
			t.Fatalf("%s exclusions = %v", direction, selection.ExcludedEdgeIDs)
		}
		if direction == DirectionOutbound && len(selection.SourceNodeIDs) != 1 || direction == DirectionInbound && len(selection.TargetNodeIDs) != 1 || direction == DirectionBoth && (len(selection.SourceNodeIDs) != 1 || len(selection.TargetNodeIDs) != 1) {
			t.Fatalf("%s selection = %+v", direction, selection)
		}
	}
}

func TestBoundedSetHelpersPreserveUniquenessAndLimits(t *testing.T) {
	set := stringSet([]string{"a", "b"})
	if !containsSet(set, "a") || containsSet(set, "missing") {
		t.Fatalf("containsSet = %+v", set)
	}
	if got := minimumInt(7, 3, 5); got != 3 {
		t.Fatalf("minimumInt = %d", got)
	}
	if got := appendUniqueBounded([]string{"a"}, []string{"a", "b", "c"}, 2); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("appendUniqueBounded = %v", got)
	}
}
