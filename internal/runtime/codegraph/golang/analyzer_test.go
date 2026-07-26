package golang_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	cggo "github.com/H4RL33/wormhole/internal/runtime/codegraph/golang"
)

func TestAnalyzeExtractsTrackedDeclarationsAndExactEdges(t *testing.T) {
	root, files := semanticFixture(t)
	result, err := cggo.Analyze(context.Background(), cggo.Request{
		Checkout: root,
		Files:    files,
		Suppress: []string{"untracked.go"},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	packagePaths := make([]string, 0, len(result.Packages))
	for _, pkg := range result.Packages {
		packagePaths = append(packagePaths, pkg.ImportPath+"|"+string(pkg.Variant))
	}
	for _, want := range []string{
		"example.com/fixture|production",
		"example.com/fixture|internal_test",
		"example.com/fixture/sub|production",
		"example.com/fixture_test|external_test",
	} {
		if !contains(packagePaths, want) {
			t.Errorf("packages = %v, missing %q", packagePaths, want)
		}
	}

	symbols := map[string]cggo.Symbol{}
	for _, symbol := range result.Symbols {
		symbols[symbol.QualifiedName] = symbol
		if symbol.ID == "" || symbol.Fingerprint == "" || symbol.FileID == "" || symbol.StartByte < 0 || symbol.EndByte <= symbol.StartByte {
			t.Errorf("invalid symbol: %#v", symbol)
		}
	}
	for qualifiedName, kind := range map[string]cggo.SymbolKind{
		"example.com/fixture.Answer":            cggo.SymbolConstant,
		"example.com/fixture.Counter":           cggo.SymbolType,
		"example.com/fixture.Global":            cggo.SymbolVariable,
		"example.com/fixture.Reader":            cggo.SymbolInterface,
		"example.com/fixture.NewCounter":        cggo.SymbolFunction,
		"example.com/fixture.helper":            cggo.SymbolFunction,
		"example.com/fixture.(*Counter).Read":   cggo.SymbolMethod,
		"example.com/fixture/sub.Use":           cggo.SymbolFunction,
		"example.com/fixture.TestInternal":      cggo.SymbolFunction,
		"example.com/fixture_test.TestExternal": cggo.SymbolFunction,
	} {
		if got, ok := symbols[qualifiedName]; !ok || got.Kind != kind {
			t.Errorf("symbol %q = %#v, want kind %q", qualifiedName, got, kind)
		}
	}
	if _, exists := symbols["example.com/fixture.Untracked"]; exists {
		t.Fatal("untracked declaration entered semantic result")
	}
	if _, exists := symbols["example.com/fixture.Ignored"]; exists {
		t.Fatal("ignored declaration entered semantic result")
	}
	if _, exists := symbols["example.com/fixture.BuildExcluded"]; exists {
		t.Fatal("build-excluded declaration entered semantic result")
	}

	nodes := make(map[string]struct{}, len(result.Packages)+len(result.Files)+len(result.Symbols))
	for _, pkg := range result.Packages {
		nodes[pkg.ID] = struct{}{}
	}
	for _, file := range result.Files {
		nodes[file.ID] = struct{}{}
	}
	for _, symbol := range result.Symbols {
		nodes[symbol.ID] = struct{}{}
	}
	for _, edge := range result.Edges {
		if _, ok := nodes[edge.SourceID]; !ok {
			t.Errorf("edge source is dangling: %#v", edge)
		}
		if _, ok := nodes[edge.TargetID]; !ok {
			t.Errorf("edge target is dangling: %#v", edge)
		}
		if edge.Provenance == cggo.ProvenanceHeuristic || edge.Confidence != 1 {
			t.Errorf("edge is not exact: %#v", edge)
		}
	}
	assertEdge(t, result, symbols["example.com/fixture.NewCounter"].ID, symbols["example.com/fixture.helper"].ID, cggo.RelationshipCalls, cggo.ProvenanceGoTypes)
	assertEdge(t, result, symbols["example.com/fixture.NewCounter"].ID, symbols["example.com/fixture/sub.Use"].ID, cggo.RelationshipCalls, cggo.ProvenanceGoTypes)
	assertEdge(t, result, symbols["example.com/fixture.Global"].ID, symbols["example.com/fixture.Answer"].ID, cggo.RelationshipReferences, cggo.ProvenanceGoTypes)
	assertEdge(t, result, symbols["example.com/fixture.NewCounter"].ID, symbols["example.com/fixture.Counter"].ID, cggo.RelationshipUsesType, cggo.ProvenanceGoTypes)
	if !hasRelationship(result.Edges, cggo.RelationshipContains) ||
		!hasRelationship(result.Edges, cggo.RelationshipDefines) ||
		!hasRelationship(result.Edges, cggo.RelationshipImports) {
		t.Fatalf("missing structural relationships: %#v", result.Edges)
	}
}

func TestAnalyzeSymbolIdentityIgnoresBodiesCommentsAndWhitespace(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/stable\n\ngo 1.26\n")
	firstSource := []byte("package stable\n\n// F returns one.\nfunc F(value int) int { return value + 1 }\n")
	secondSource := []byte("package stable\n\n// entirely changed\nfunc F( value int ) int {\n\tprintln(value)\n\treturn value + 200\n}\n")
	first := analyzeOne(t, root, "stable.go", firstSource)
	second := analyzeOne(t, root, "stable.go", secondSource)
	if first.ID != second.ID || first.Fingerprint != second.Fingerprint {
		t.Fatalf("body/comment/whitespace edit changed identity:\nfirst=%#v\nsecond=%#v", first, second)
	}
	third := analyzeOne(t, root, "stable.go", []byte("package stable\n\nfunc F(value string) string { return value }\n"))
	if first.ID == third.ID || first.Fingerprint == third.Fingerprint {
		t.Fatalf("signature edit preserved identity:\nfirst=%#v\nthird=%#v", first, third)
	}
	fourth := analyzeOne(t, root, "stable.go", []byte("package stable\n\nfunc Renamed(value int) int { return value }\n"))
	if first.ID == fourth.ID {
		t.Fatalf("rename preserved identity: %q", first.ID)
	}
}

func TestIdentityEditFixtures(t *testing.T) {
	bodyBefore := analyzeFixtureSymbol(t, "body-edit", "before")
	bodyAfter := analyzeFixtureSymbol(t, "body-edit", "after")
	if bodyBefore.ID != bodyAfter.ID || bodyBefore.Fingerprint != bodyAfter.Fingerprint {
		t.Fatalf("body-edit fixture changed identity:\nbefore=%#v\nafter=%#v", bodyBefore, bodyAfter)
	}
	signatureBefore := analyzeFixtureSymbol(t, "signature-edit", "before")
	signatureAfter := analyzeFixtureSymbol(t, "signature-edit", "after")
	if signatureBefore.ID == signatureAfter.ID || signatureBefore.Fingerprint == signatureAfter.Fingerprint {
		t.Fatalf("signature-edit fixture preserved identity:\nbefore=%#v\nafter=%#v", signatureBefore, signatureAfter)
	}
}

func TestAnalyzeIsDeterministicAndIgnoresHostGoEnvironment(t *testing.T) {
	root, files := semanticFixture(t)
	t.Setenv("GOPACKAGESDRIVER", filepath.Join(root, "missing-driver"))
	t.Setenv("GOWORK", filepath.Join(root, "missing.work"))
	t.Setenv("GOENV", filepath.Join(root, "missing.env"))
	t.Setenv("GOFLAGS", "-tags=host-leak")
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("CGO_ENABLED", "1")
	request := cggo.Request{Checkout: root, Files: reverseFiles(files), Suppress: []string{"untracked.go"}}
	first, err := cggo.Analyze(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Files = files
	second, err := cggo.Analyze(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ by input ordering or host environment:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !sort.SliceIsSorted(first.Symbols, func(i, j int) bool { return first.Symbols[i].ID < first.Symbols[j].ID }) {
		t.Fatal("symbols are not deterministically sorted")
	}
}

func TestAnalyzeFailsClosedOnInvalidInputAndLimits(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/limits\n\ngo 1.26\n")
	valid := cggo.SourceFile{Path: "a.go", Bytes: []byte("package limits\nfunc A() {}\n")}
	requests := []struct {
		name    string
		request cggo.Request
		want    error
	}{
		{name: "path traversal", request: cggo.Request{Checkout: root, Files: []cggo.SourceFile{{Path: "../a.go", Bytes: valid.Bytes}}}, want: cggo.ErrInvalidInput},
		{name: "duplicate", request: cggo.Request{Checkout: root, Files: []cggo.SourceFile{valid, valid}}, want: cggo.ErrInvalidInput},
		{name: "package limit", request: cggo.Request{Checkout: root, Files: []cggo.SourceFile{valid}, Limits: cggo.Limits{MaxPackages: 1, MaxSymbols: 1, MaxEdges: 1, MaxDiagnostics: 1}}, want: cggo.ErrLimitExceeded},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			_, err := cggo.Analyze(context.Background(), test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Analyze() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAnalyzePropagatesCallEdgeLimit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/edges\n\ngo 1.26\n")
	source := []byte(`package edges
func A() { B(); C() }
func B() {}
func C() {}
`)
	_, err := cggo.Analyze(context.Background(), cggo.Request{
		Checkout: root,
		Files:    []cggo.SourceFile{{Path: "edges.go", Bytes: source}},
		Limits:   cggo.Limits{MaxPackages: 10, MaxSymbols: 10, MaxEdges: 5, MaxDiagnostics: 10},
	})
	if !errors.Is(err, cggo.ErrLimitExceeded) {
		t.Fatalf("Analyze() error = %v, want ErrLimitExceeded", err)
	}
}

func TestAnalyzeClassifiesLocalTypeConversionAsTypeUseNotCall(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/conversion\n\ngo 1.26\n")
	source := []byte("package conversion\ntype Local int\nfunc Convert(value int) Local { return Local(value) }\n")
	result, err := cggo.Analyze(context.Background(), cggo.Request{
		Checkout: root, Files: []cggo.SourceFile{{Path: "conversion.go", Bytes: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var convertID, localID string
	for _, symbol := range result.Symbols {
		switch symbol.QualifiedName {
		case "example.com/conversion.Convert":
			convertID = symbol.ID
		case "example.com/conversion.Local":
			localID = symbol.ID
		}
	}
	assertEdge(t, result, convertID, localID, cggo.RelationshipUsesType, cggo.ProvenanceGoTypes)
	for _, edge := range result.Edges {
		if edge.SourceID == convertID && edge.TargetID == localID && edge.Relationship == cggo.RelationshipCalls {
			t.Fatalf("type conversion was classified as a call: %#v", edge)
		}
	}
}

func TestAnalyzeDoesNotInventCallTargetForFunctionVariable(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/indirect\n\ngo 1.26\n")
	source := []byte("package indirect\nvar Callback = Helper\nfunc Invoke() { Callback() }\nfunc Helper() {}\n")
	result, err := cggo.Analyze(context.Background(), cggo.Request{
		Checkout: root, Files: []cggo.SourceFile{{Path: "indirect.go", Bytes: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for _, symbol := range result.Symbols {
		ids[symbol.Name] = symbol.ID
	}
	assertEdge(t, result, ids["Invoke"], ids["Callback"], cggo.RelationshipReferences, cggo.ProvenanceGoTypes)
	for _, edge := range result.Edges {
		if edge.SourceID == ids["Invoke"] && edge.TargetID == ids["Callback"] && edge.Relationship == cggo.RelationshipCalls {
			t.Fatalf("function variable was presented as an exact call target: %#v", edge)
		}
	}
}

func TestAnalyzeSupportsMultipleProductionAndTestInitDeclarations(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/inits\n\ngo 1.26\n")
	contents := map[string]string{
		"a.go":             "package inits\nfunc init() {}\n",
		"b.go":             "package inits\nfunc init() {}\n",
		"internal_test.go": "package inits\nfunc init() {}\n",
		"external_test.go": "package inits_test\nfunc init() {}\n",
	}
	files := make([]cggo.SourceFile, 0, len(contents))
	for path, content := range contents {
		write(t, root, path, content)
		files = append(files, cggo.SourceFile{Path: path, Bytes: []byte(content)})
	}
	result, err := cggo.Analyze(context.Background(), cggo.Request{Checkout: root, Files: files})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	ids := make(map[string]struct{})
	var initCount int
	for _, symbol := range result.Symbols {
		if symbol.Name != "init" {
			continue
		}
		initCount++
		ids[symbol.ID] = struct{}{}
	}
	if initCount != 4 || len(ids) != 4 {
		t.Fatalf("init symbols count=%d unique_ids=%d symbols=%#v", initCount, len(ids), result.Symbols)
	}
}

func TestAnalyzePinsLinuxAMD64V1BuildContext(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/target\n\ngo 1.26\n")
	contents := map[string]string{
		"common.go":  "package target\nfunc Common() {}\n",
		"linux.go":   "//go:build linux && amd64\n\npackage target\nfunc LinuxAMD64() {}\n",
		"windows.go": "//go:build windows\n\npackage target\nfunc Windows() {}\n",
		"v3.go":      "//go:build amd64.v3\n\npackage target\nfunc AMD64V3() {}\n",
	}
	files := make([]cggo.SourceFile, 0, len(contents))
	for path, content := range contents {
		write(t, root, path, content)
		files = append(files, cggo.SourceFile{Path: path, Bytes: []byte(content)})
	}
	t.Setenv("GOOS", "windows")
	t.Setenv("GOARCH", "arm64")
	t.Setenv("GOAMD64", "v4")
	first, err := cggo.Analyze(context.Background(), cggo.Request{Checkout: root, Files: reverseFiles(files)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GOOS", "linux"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GOARCH", "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GOAMD64", "v3"); err != nil {
		t.Fatal(err)
	}
	second, err := cggo.Analyze(context.Background(), cggo.Request{Checkout: root, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("host build environment changed result:\nfirst=%#v\nsecond=%#v", first, second)
	}
	names := make([]string, 0, len(first.Symbols))
	for _, symbol := range first.Symbols {
		names = append(names, symbol.Name)
	}
	if !contains(names, "LinuxAMD64") || contains(names, "Windows") || contains(names, "AMD64V3") {
		t.Fatalf("pinned build-context symbols = %v", names)
	}
}

func semanticFixture(t *testing.T) (string, []cggo.SourceFile) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/fixture\n\ngo 1.26\n")
	contents := map[string]string{
		"fixture.go": `package fixture

import "example.com/fixture/sub"

type Counter[T ~int] struct { Value T }
type Reader interface { Read() int }
const Answer = 42
var Global = Answer

func NewCounter[T ~int](value T) *Counter[T] {
	helper()
	sub.Use()
	return &Counter[T]{Value: value}
}
func (counter *Counter[T]) Read() int { return int(counter.Value) }
func helper() {}
`,
		"fixture_test.go": `package fixture
import "testing"
func TestInternal(t *testing.T) { _ = NewCounter(1) }
`,
		"external_test.go": `package fixture_test
import (
	"testing"
	fixture "example.com/fixture"
)
func TestExternal(t *testing.T) { _ = fixture.NewCounter(1) }
`,
		"sub/sub.go": `package sub
func Use() {}
`,
		"excluded.go": `//go:build never_enabled

package fixture
func BuildExcluded() {}
`,
	}
	paths := make([]string, 0, len(contents))
	for path := range contents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]cggo.SourceFile, 0, len(paths))
	for _, path := range paths {
		content := contents[path]
		write(t, root, path, content)
		files = append(files, cggo.SourceFile{Path: path, Bytes: []byte(content)})
	}
	write(t, root, "untracked.go", "package fixture\nfunc Untracked() {}\n")
	write(t, root, "ignored.go", "package fixture\nfunc Ignored() {}\n")
	return root, files
}

func analyzeOne(t *testing.T, root, path string, source []byte) cggo.Symbol {
	t.Helper()
	write(t, root, path, string(source))
	result, err := cggo.Analyze(context.Background(), cggo.Request{Checkout: root, Files: []cggo.SourceFile{{Path: path, Bytes: source}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 1 {
		t.Fatalf("symbols = %#v", result.Symbols)
	}
	return result.Symbols[0]
}

func analyzeFixtureSymbol(t *testing.T, fixture, version string) cggo.Symbol {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "testdata", "codegraph", fixture, version)
	source, err := os.ReadFile(filepath.Join(root, "stable.go"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := cggo.Analyze(context.Background(), cggo.Request{
		Checkout: root,
		Files:    []cggo.SourceFile{{Path: "stable.go", Bytes: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 1 {
		t.Fatalf("fixture symbols = %#v", result.Symbols)
	}
	return result.Symbols[0]
}

func assertEdge(t *testing.T, result cggo.Result, source, target string, relationship cggo.Relationship, provenance cggo.Provenance) {
	t.Helper()
	for _, edge := range result.Edges {
		if edge.SourceID == source && edge.TargetID == target && edge.Relationship == relationship && edge.Provenance == provenance {
			return
		}
	}
	t.Errorf("missing edge %s -%s/%s-> %s", source, relationship, provenance, target)
}

func hasRelationship(edges []cggo.Edge, relationship cggo.Relationship) bool {
	for _, edge := range edges {
		if edge.Relationship == relationship {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func reverseFiles(files []cggo.SourceFile) []cggo.SourceFile {
	reversed := append([]cggo.SourceFile(nil), files...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func write(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
