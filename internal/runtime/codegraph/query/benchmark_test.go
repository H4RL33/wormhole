package query_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	codegraphindex "github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

type benchmarkCorpus struct {
	SchemaVersion int              `json:"schema_version"`
	Queries       []benchmarkQuery `json:"queries"`
}

type benchmarkQuery struct {
	ID                         string                 `json:"id"`
	Question                   string                 `json:"question"`
	EntrySymbols               []string               `json:"entry_symbols"`
	ExpectedFiles              []string               `json:"expected_files"`
	ExpectedRelationshipPath   []benchmarkPathSegment `json:"expected_relationship_path"`
	ExpectedAuthoritativeEdges []string               `json:"expected_authoritative_edges"`
	ExpectedHeuristicEdges     []string               `json:"expected_heuristic_edges"`
	SufficiencyCriteria        string                 `json:"sufficiency_criteria"`
}

type benchmarkPathSegment struct {
	From         string `json:"from"`
	Relationship string `json:"relationship"`
	To           string `json:"to"`
}

type benchmarkRun struct {
	Environment benchmarkEnvironment `json:"environment"`
	Outcomes    []benchmarkOutcome   `json:"outcomes"`
}

type benchmarkEnvironment struct {
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GoVersion   string `json:"go_version"`
	GitRevision string `json:"git_revision"`
	TreeState   string `json:"tree_state"`
}

type benchmarkOutcome struct {
	ID                         string   `json:"id"`
	Question                   string   `json:"question"`
	GraphRevision              string   `json:"graph_revision"`
	IndexedCommit              string   `json:"indexed_commit"`
	DurationNanoseconds        int64    `json:"duration_nanoseconds"`
	SourceBytesReturned        int64    `json:"source_bytes_returned"`
	IrrelevantSourceBytes      int64    `json:"irrelevant_source_bytes"`
	OmittedNodes               int      `json:"omitted_nodes"`
	OmittedEdges               int      `json:"omitted_edges"`
	OmissionReason             string   `json:"omission_reason,omitempty"`
	Completeness               string   `json:"completeness"`
	SelectedFiles              []string `json:"selected_files"`
	MatchedSymbols             []string `json:"matched_symbols"`
	ObservedAuthoritativeEdges []string `json:"observed_authoritative_edges"`
	ObservedHeuristicEdges     []string `json:"observed_heuristic_edges"`
	MissingExpectedFiles       []string `json:"missing_expected_files"`
	MissingExpectedPath        []string `json:"missing_expected_relationship_path"`
	MissingAuthoritativeEdges  []string `json:"missing_expected_authoritative_edges"`
	MissingHeuristicEdges      []string `json:"missing_expected_heuristic_edges"`
	SufficiencyCriteria        string   `json:"sufficiency_criteria"`
	Sufficiency                string   `json:"sufficiency"`
	SufficiencyReason          string   `json:"sufficiency_reason"`
}

type observedBenchmarkEdge struct {
	from, relationship, to string
	authoritative          bool
}

func TestBenchmarkCorpusContract(t *testing.T) {
	corpus := loadBenchmarkCorpus(t)
	wantQuestions := []string{
		"Where is agent registration authenticated?",
		"Trace a task status update into its emitted event.",
		"Where are public sync-v2 descriptors, closed schemas, strict proof carriage, and safe failures frozen without live dispatch?",
		"Which code writes Passport audit records?",
		"Trace a local task write into the durable outbound queue.",
		"Where is project isolation enforced for local SQLite queries?",
	}
	if corpus.SchemaVersion != 1 {
		t.Fatalf("benchmark schema version = %d, want 1", corpus.SchemaVersion)
	}
	gotQuestions := make([]string, 0, len(corpus.Queries))
	seenIDs := make(map[string]struct{}, len(corpus.Queries))
	for _, query := range corpus.Queries {
		gotQuestions = append(gotQuestions, query.Question)
		if query.ID == "" {
			t.Error("benchmark query has empty id")
		}
		if _, duplicate := seenIDs[query.ID]; duplicate {
			t.Errorf("duplicate benchmark query id %q", query.ID)
		}
		seenIDs[query.ID] = struct{}{}
		if len(query.EntrySymbols) == 0 || len(query.ExpectedFiles) == 0 || len(query.ExpectedRelationshipPath) == 0 || query.SufficiencyCriteria == "" {
			t.Errorf("benchmark query %q is missing expected entries, files, path, or sufficiency criteria", query.ID)
		}
		if query.ExpectedAuthoritativeEdges == nil || query.ExpectedHeuristicEdges == nil {
			t.Errorf("benchmark query %q must record authoritative and heuristic expectations separately", query.ID)
		}
	}
	if !reflect.DeepEqual(gotQuestions, wantQuestions) {
		t.Fatalf("benchmark questions = %#v, want exact ordered corpus %#v", gotQuestions, wantQuestions)
	}
}

func TestBenchmarkResultRequiresExpectedPathsAndProvenance(t *testing.T) {
	base := query.Result{
		Matches: []query.Match{{QualifiedName: "example.invalid.Entry", FilePath: "expected.go"}},
		Nodes: []query.Node{
			{ID: "entry", Name: "Entry", Path: "expected.go"},
			{ID: "target", Name: "Target", Path: "expected.go"},
		},
		Edges: []query.Edge{{
			SourceNodeID: "entry", TargetNodeID: "target", Relationship: codegraphstore.RelationshipCalls,
			Provenance: codegraphstore.ProvenanceGoTypes, Authoritative: true,
		}},
	}
	tests := []struct {
		name      string
		benchmark benchmarkQuery
	}{
		{
			name: "missing expected path",
			benchmark: benchmarkQuery{
				ExpectedFiles:              []string{"expected.go"},
				ExpectedRelationshipPath:   []benchmarkPathSegment{{From: "Entry", Relationship: "calls", To: "Missing"}},
				ExpectedAuthoritativeEdges: []string{"Entry calls Missing"}, ExpectedHeuristicEdges: []string{},
			},
		},
		{
			name: "wrong provenance",
			benchmark: benchmarkQuery{
				ExpectedFiles:              []string{"expected.go"},
				ExpectedRelationshipPath:   []benchmarkPathSegment{{From: "Entry", Relationship: "calls", To: "Target"}},
				ExpectedAuthoritativeEdges: []string{}, ExpectedHeuristicEdges: []string{"Entry calls Target"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if outcome := benchmarkResult(tt.benchmark, base, time.Nanosecond); outcome.Sufficiency == "sufficient" {
				t.Fatalf("benchmark with absent path or wrong provenance classified sufficient: %#v", outcome)
			}
		})
	}
}

// BenchmarkWormholeCodeGraphCorpus is the reproducible Gate B runner. It
// records raw observations but deliberately imposes no latency or byte-
// performance release threshold. Run it with -benchtime=1x for one corpus.
func BenchmarkWormholeCodeGraphCorpus(b *testing.B) {
	corpus := loadBenchmarkCorpus(b)
	root := benchmarkRepositoryRoot(b)
	inspection, err := codegraphindex.InspectCheckout(context.Background(), root)
	if err != nil {
		b.Fatalf("inspect Wormhole checkout: %v", err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(b.TempDir(), "benchmark.db"), OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		b.Fatalf("enable benchmark WAL: %v", err)
	}
	graphStore, err := codegraphstore.Open(context.Background(), db, "task-12-benchmark")
	if err != nil {
		b.Fatal(err)
	}
	project := codegraphconfig.DefaultProject("task-12-benchmark")
	project.Enabled = true
	project.CanonicalRemote = inspection.CanonicalRemote
	project.ActiveCheckout = root
	if err := graphStore.PutProjectConfig(context.Background(), project); err != nil {
		b.Fatal(err)
	}
	if err := codegraphindex.New(graphStore).Build(context.Background(), codegraphindex.BuildRequest{
		ProjectID: "task-12-benchmark", RevisionID: "task-12-wormhole-corpus",
	}); err != nil {
		b.Fatalf("build Wormhole benchmark graph: %v", err)
	}
	service := query.New(graphStore, codegraphconfig.DefaultProjectSourceByteCeiling)
	environment := benchmarkEnvironment{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(),
		GitRevision: strings.TrimSpace(benchmarkGit(b, root, "rev-parse", "HEAD")),
		TreeState:   benchmarkTreeState(b, root),
	}
	b.ResetTimer()
	var run benchmarkRun
	for iteration := 0; iteration < b.N; iteration++ {
		run = benchmarkRun{Environment: environment}
		for _, benchmark := range corpus.Queries {
			started := time.Now()
			result, err := service.Execute(context.Background(), query.Request{
				Intent: benchmark.Question, EntrySymbols: benchmark.EntrySymbols,
				IncludeEdges: []codegraphstore.Relationship{
					codegraphstore.RelationshipCalls,
					codegraphstore.RelationshipReferences,
					codegraphstore.RelationshipUsesType,
				},
				MaxDepth: 4, MaxNodes: 100, RequestedSourceBytes: 16 * 1024, SourceAuthorized: true,
			})
			elapsed := time.Since(started)
			if err != nil {
				b.Fatalf("benchmark %q query: %v", benchmark.ID, err)
			}
			run.Outcomes = append(run.Outcomes, benchmarkResult(benchmark, result, elapsed))
		}
	}
	b.StopTimer()
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("TASK12_BENCHMARK_JSON_BEGIN\n%s\nTASK12_BENCHMARK_JSON_END", raw)
}

func benchmarkResult(benchmark benchmarkQuery, result query.Result, elapsed time.Duration) benchmarkOutcome {
	selectedSet := make(map[string]struct{})
	matchedSymbols := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		matchedSymbols = append(matchedSymbols, match.QualifiedName)
		if match.FilePath != "" {
			selectedSet[match.FilePath] = struct{}{}
		}
	}
	labels := make(map[string]string, len(result.Nodes))
	for _, node := range result.Nodes {
		labels[node.ID] = benchmarkNodeLabel(node.Name, node.Path)
		if strings.HasSuffix(node.Path, ".go") {
			selectedSet[node.Path] = struct{}{}
		}
	}
	expectedFiles := make(map[string]struct{}, len(benchmark.ExpectedFiles))
	for _, path := range benchmark.ExpectedFiles {
		expectedFiles[path] = struct{}{}
	}
	var irrelevant int64
	for _, outcome := range result.Sources {
		if outcome.SourceIncluded {
			selectedSet[outcome.FilePath] = struct{}{}
			if _, expected := expectedFiles[outcome.FilePath]; !expected {
				irrelevant += outcome.ReturnedBytes
			}
		}
	}
	selected := sortedBenchmarkKeys(selectedSet)
	missing := make([]string, 0)
	for _, path := range benchmark.ExpectedFiles {
		if _, found := selectedSet[path]; !found {
			missing = append(missing, path)
		}
	}
	authoritative, heuristic := make([]string, 0), make([]string, 0)
	observedEdges := make([]observedBenchmarkEdge, 0, len(result.Edges))
	for _, edge := range result.Edges {
		label := fmt.Sprintf("%s %s %s [%s]", labels[edge.SourceNodeID], edge.Relationship, labels[edge.TargetNodeID], edge.Provenance)
		observedEdges = append(observedEdges, observedBenchmarkEdge{
			from: labels[edge.SourceNodeID], relationship: string(edge.Relationship), to: labels[edge.TargetNodeID], authoritative: edge.Authoritative,
		})
		if edge.Authoritative {
			authoritative = append(authoritative, label)
		} else {
			heuristic = append(heuristic, label)
		}
	}
	for _, values := range [][]string{matchedSymbols, missing, authoritative, heuristic} {
		sort.Strings(values)
	}
	missingPath := missingExpectedPath(benchmark.ExpectedRelationshipPath, observedEdges)
	missingAuthoritative := missingExpectedEdges(benchmark.ExpectedAuthoritativeEdges, observedEdges, true)
	missingHeuristic := missingExpectedEdges(benchmark.ExpectedHeuristicEdges, observedEdges, false)
	for _, values := range [][]string{missingPath, missingAuthoritative, missingHeuristic} {
		sort.Strings(values)
	}
	sufficiency, reason := "sufficient", "answer evidence contains every expected file, relationship segment, and provenance-specific edge"
	if len(result.Matches) == 0 || countExpectedFiles(selectedSet, expectedFiles) == 0 {
		sufficiency = "useless"
		reason = "no matching symbol or no expected file was selected"
	} else if len(missing) > 0 || len(missingPath) > 0 || len(missingAuthoritative) > 0 || len(missingHeuristic) > 0 {
		sufficiency = "incomplete"
		reason = benchmarkIncompleteReason(missing, missingPath, missingAuthoritative, missingHeuristic)
	}
	return benchmarkOutcome{
		ID: benchmark.ID, Question: benchmark.Question, GraphRevision: result.RevisionID, IndexedCommit: result.IndexedCommit,
		DurationNanoseconds: elapsed.Nanoseconds(), SourceBytesReturned: result.SourceBytes, IrrelevantSourceBytes: irrelevant,
		OmittedNodes: result.OmittedNodeCount, OmittedEdges: result.OmittedEdgeCount, OmissionReason: result.OmissionReason,
		Completeness: string(result.Completeness), SelectedFiles: selected, MatchedSymbols: matchedSymbols,
		ObservedAuthoritativeEdges: authoritative, ObservedHeuristicEdges: heuristic,
		MissingExpectedFiles: missing, MissingExpectedPath: missingPath,
		MissingAuthoritativeEdges: missingAuthoritative, MissingHeuristicEdges: missingHeuristic,
		SufficiencyCriteria: benchmark.SufficiencyCriteria, Sufficiency: sufficiency, SufficiencyReason: reason,
	}
}

func countExpectedFiles(selected, expected map[string]struct{}) int {
	count := 0
	for path := range expected {
		if _, ok := selected[path]; ok {
			count++
		}
	}
	return count
}

func missingExpectedPath(expected []benchmarkPathSegment, observed []observedBenchmarkEdge) []string {
	missing := make([]string, 0)
	for _, segment := range expected {
		if !benchmarkEdgeObserved(segment.From, segment.Relationship, segment.To, observed, nil) {
			missing = append(missing, segment.From+" "+segment.Relationship+" "+segment.To)
		}
	}
	return missing
}

func missingExpectedEdges(expected []string, observed []observedBenchmarkEdge, authoritative bool) []string {
	missing := make([]string, 0)
	for _, value := range expected {
		fields := strings.Fields(value)
		if len(fields) != 3 || !benchmarkEdgeObserved(fields[0], fields[1], fields[2], observed, &authoritative) {
			missing = append(missing, value)
		}
	}
	return missing
}

func benchmarkEdgeObserved(from, relationship, to string, observed []observedBenchmarkEdge, authoritative *bool) bool {
	for _, edge := range observed {
		if benchmarkNodeMatches(edge.from, from) && edge.relationship == relationship && benchmarkNodeMatches(edge.to, to) &&
			(authoritative == nil || edge.authoritative == *authoritative) {
			return true
		}
	}
	return false
}

func benchmarkNodeMatches(observed, expected string) bool {
	observed = strings.SplitN(observed, "@", 2)[0]
	return observed == expected || strings.HasSuffix(observed, "."+expected)
}

func benchmarkIncompleteReason(missingFiles, missingPath, missingAuthoritative, missingHeuristic []string) string {
	parts := make([]string, 0, 5)
	for label, values := range map[string][]string{
		"expected files": missingFiles, "relationship segments": missingPath,
		"authoritative edges": missingAuthoritative, "heuristic edges": missingHeuristic,
	} {
		if len(values) > 0 {
			parts = append(parts, fmt.Sprintf("missing %s: %s", label, strings.Join(values, ", ")))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func benchmarkNodeLabel(name, path string) string {
	if path == "" || path == name {
		return name
	}
	return name + "@" + path
}

func sortedBenchmarkKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func benchmarkRepositoryRoot(t testing.TB) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("WORMHOLE_CODEGRAPH_BENCHMARK_CHECKOUT"))
	if root == "" {
		root = strings.TrimSpace(benchmarkGit(t, ".", "rev-parse", "--show-toplevel"))
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func benchmarkTreeState(t testing.TB, root string) string {
	t.Helper()
	if strings.TrimSpace(benchmarkGit(t, root, "status", "--porcelain=v1", "-uall")) == "" {
		return "clean"
	}
	return "dirty"
}

func benchmarkGit(t testing.TB, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func loadBenchmarkCorpus(t testing.TB) benchmarkCorpus {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "testdata", "codegraph", "benchmark-corpus.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read benchmark corpus %s: %v", path, err)
	}
	var corpus benchmarkCorpus
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("decode benchmark corpus: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("decode benchmark corpus trailing data: %v", err)
	}
	return corpus
}
