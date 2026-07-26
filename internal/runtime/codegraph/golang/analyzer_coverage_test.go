package golang

import (
	"context"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestPrepareRejectsEveryInventoryOverlayConflict(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, _, _, err := prepare(Request{Checkout: missing}); err == nil {
		t.Fatal("prepare accepted missing checkout")
	}
	file := filepath.Join(t.TempDir(), "checkout")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := prepare(Request{Checkout: file}); err == nil {
		t.Fatal("prepare accepted file checkout")
	}
	root := t.TempDir()
	tests := []Request{
		{Checkout: root, Files: []SourceFile{{Path: "../escape.go"}}},
		{Checkout: root, Files: []SourceFile{{Path: "a.go"}, {Path: "a.go"}}},
		{Checkout: root, Files: []SourceFile{{Path: "a.go", Bytes: []byte("package p"), SHA256: "sha256:wrong"}}},
		{Checkout: root, Suppress: []string{"../escape.go"}},
		{Checkout: root, Files: []SourceFile{{Path: "a.go"}}, Suppress: []string{"a.go"}},
	}
	for i, request := range tests {
		if _, _, _, _, err := prepare(request); err == nil {
			t.Fatalf("prepare conflict %d succeeded", i)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, overlay, _, err := prepare(Request{Checkout: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := overlay[filepath.Join(root, "untracked.go")]; !ok {
		t.Fatal("prepare did not suppress an untracked Go file")
	}
}

func TestAnalyzerVariantAndSyntaxSelectionGuards(t *testing.T) {
	for _, test := range []struct {
		pkg  *packages.Package
		want PackageVariant
	}{
		{&packages.Package{}, VariantProduction},
		{&packages.Package{PkgPath: "example/p", ForTest: "example/p"}, VariantInternalTest},
		{&packages.Package{PkgPath: "example/p_test", ForTest: "example/p"}, VariantExternalTest},
		{&packages.Package{PkgPath: "example/generated", ForTest: "example/p"}, ""},
	} {
		if got := packageVariant(test.pkg); got != test.want {
			t.Fatalf("packageVariant(%+v) = %q, want %q", test.pkg, got, test.want)
		}
	}
	root := t.TempDir()
	if _, err := selectedSyntaxFiles(root, &packages.Package{Syntax: []*ast.File{{}}}, VariantProduction); err == nil {
		t.Fatal("selectedSyntaxFiles accepted mismatched syntax and filenames")
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	files, err := selectedSyntaxFiles(root, &packages.Package{Syntax: []*ast.File{{}}, CompiledGoFiles: []string{outside}}, VariantProduction)
	if err != nil || len(files) != 0 {
		t.Fatalf("outside syntax files = %+v, err=%v", files, err)
	}
	if _, _, err := selectPackages(root, "", nil, 1); err == nil {
		t.Fatal("selectPackages accepted no checkout package")
	}
}

func TestCalledIdentifierAndReceiverNameHandleWrappedCallsAndReceivers(t *testing.T) {
	identifier := ast.NewIdent("Call")
	wrapped := []ast.Expr{
		identifier,
		&ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: identifier},
		&ast.IndexExpr{X: identifier, Index: ast.NewIdent("T")},
		&ast.IndexListExpr{X: identifier, Indices: []ast.Expr{ast.NewIdent("T")}},
		&ast.ParenExpr{X: identifier},
	}
	for _, expression := range wrapped {
		if got := calledIdentifier(expression); got == nil || got.Name != "Call" {
			t.Fatalf("calledIdentifier(%T) = %#v", expression, got)
		}
	}
	if got := calledIdentifier(&ast.BasicLit{}); got != nil {
		t.Fatalf("calledIdentifier basic literal = %#v", got)
	}

	pkg := types.NewPackage("example/p", "p")
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Receiver", nil), types.NewStruct(nil, nil), nil)
	makeMethod := func(receiver types.Type) *types.Func {
		recv := types.NewVar(token.NoPos, pkg, "recv", receiver)
		return types.NewFunc(token.NoPos, pkg, "Method", types.NewSignatureType(recv, nil, nil, nil, nil, false))
	}
	if got := receiverName(types.NewVar(token.NoPos, pkg, "v", types.Typ[types.Int])); got != "" {
		t.Fatalf("non-function receiver = %q", got)
	}
	if got := receiverName(types.NewFunc(token.NoPos, pkg, "Free", types.NewSignatureType(nil, nil, nil, nil, nil, false))); got != "" {
		t.Fatalf("free function receiver = %q", got)
	}
	if got := receiverName(makeMethod(named)); got != "(Receiver)" {
		t.Fatalf("value receiver = %q", got)
	}
	if got := receiverName(makeMethod(types.NewPointer(named))); got != "(*Receiver)" {
		t.Fatalf("pointer receiver = %q", got)
	}
	if got := receiverName(makeMethod(types.Typ[types.Int])); got == "" {
		t.Fatal("unnamed receiver was not rendered")
	}
}

func TestSelectPackagesRejectsAmbiguousAndBoundedPackageSets(t *testing.T) {
	root := t.TempDir()
	module := func(modulePath string) *packages.Module {
		return &packages.Module{Path: modulePath, Dir: root}
	}
	pkg := func(importPath, file string) *packages.Package {
		return &packages.Package{
			ID: importPath, PkgPath: importPath, Name: filepath.Base(importPath), Module: module("example.test/mod"),
			Syntax: []*ast.File{{}}, CompiledGoFiles: []string{filepath.Join(root, file)},
		}
	}

	valid := pkg("example.test/mod/a", "a.go")
	modulePath, selected, err := selectPackages(root, "example.test/mod", []*packages.Package{
		{},
		{ID: "outside", PkgPath: "outside", Name: "outside", Module: &packages.Module{Path: "outside", Dir: filepath.Join(root, "missing")}},
		{ID: "generated", PkgPath: "example.test/mod/generated", Name: "generated", ForTest: "example.test/mod/a", Module: module("example.test/mod")},
		{ID: "empty", PkgPath: "example.test/mod/empty", Name: "empty", Module: module("example.test/mod")},
		valid,
		valid,
	}, 2)
	if err != nil || modulePath != "example.test/mod" || len(selected) != 1 {
		t.Fatalf("selected package set = path %q, packages %d, err=%v", modulePath, len(selected), err)
	}

	otherModule := pkg("example.test/mod/b", "b.go")
	otherModule.Module = module("example.test/other")
	if _, _, err := selectPackages(root, "", []*packages.Package{valid, otherModule}, 2); err == nil {
		t.Fatal("selectPackages accepted multiple module paths")
	}

	second := pkg("example.test/mod/b", "b.go")
	if _, _, err := selectPackages(root, "", []*packages.Package{valid, second}, 1); err == nil {
		t.Fatal("selectPackages ignored package maximum")
	}
	if _, _, err := selectPackages(root, "example.test/renamed", []*packages.Package{valid}, 1); err == nil {
		t.Fatal("selectPackages ignored expected module mismatch")
	}
}

func TestAnalyzerDiagnosticAndTypeHelpersCoverBoundaryShapes(t *testing.T) {
	root := t.TempDir()
	message := root + "\n\t" + string(rune(0x7f)) + strings.Repeat("x", maxDiagnosticBytes+20)
	sanitized := sanitizeDiagnostic(root, message)
	if strings.Contains(sanitized, root) || strings.ContainsAny(sanitized, "\n\r\t") || len(sanitized) != maxDiagnosticBytes {
		t.Fatalf("sanitizeDiagnostic boundary result has length %d: %q", len(sanitized), sanitized)
	}
	loaded := []*packages.Package{{ID: "same", Errors: []packages.Error{{Msg: "z"}, {Msg: "a"}}}}
	if _, err := collectDiagnostics(root, loaded, 1); err == nil {
		t.Fatal("collectDiagnostics ignored maximum")
	}
	diagnostics, err := collectDiagnostics(root, loaded, 2)
	if err != nil || len(diagnostics) != 2 || !strings.HasSuffix(diagnostics[0].Message, "a") {
		t.Fatalf("collectDiagnostics = %+v, err=%v", diagnostics, err)
	}

	identifier := ast.NewIdent("value")
	if usedObject(nil, identifier) != nil || usedObject(&types.Info{}, nil) != nil {
		t.Fatal("usedObject accepted nil inputs")
	}
	object := types.NewVar(token.NoPos, types.NewPackage("example.test/p", "p"), "value", types.Typ[types.Int])
	info := &types.Info{Defs: map[*ast.Ident]types.Object{identifier: object}}
	if got := usedObject(info, identifier); got != object {
		t.Fatalf("usedObject definition fallback = %v, want %v", got, object)
	}
	if got := stableTypeString(nil); got != "" {
		t.Fatalf("stableTypeString(nil) = %q", got)
	}

	result := Result{Diagnostics: []Diagnostic{{ID: "z"}, {ID: "a"}}}
	sortResult(&result)
	if result.Diagnostics[0].ID != "a" {
		t.Fatalf("sortResult diagnostics = %+v", result.Diagnostics)
	}
}

func TestSymbolAndEdgeExtractionRejectsIncompleteSemanticInputs(t *testing.T) {
	if _, _, err := extractSymbols([]loadedPackage{{files: []loadedFile{{path: "missing.go", file: &ast.File{}}}}}, nil, 1); err == nil {
		t.Fatal("extractSymbols accepted a non-inventory syntax file")
	}
	declaration := &ast.FuncDecl{Name: ast.NewIdent("MissingObject")}
	selected := []loadedPackage{{
		load:  &packages.Package{TypesInfo: &types.Info{Defs: map[*ast.Ident]types.Object{}}},
		files: []loadedFile{{path: "a.go", file: &ast.File{Decls: []ast.Decl{declaration}}}},
	}}
	if _, _, err := extractSymbols(selected, map[string]*File{"a.go": {Path: "a.go"}}, 1); err == nil {
		t.Fatal("extractSymbols accepted a declaration without a type object")
	}

	fileSet := token.NewFileSet()
	file := fileSet.AddFile("a.go", -1, 10)
	if _, _, _, _, err := sourceRange(fileSet, token.NoPos, token.NoPos); err == nil {
		t.Fatal("sourceRange accepted positions outside the file set")
	}
	position := file.Pos(1)
	if _, _, _, _, err := sourceRange(fileSet, position, position); err == nil {
		t.Fatal("sourceRange accepted an empty range")
	}

	selected = []loadedPackage{{record: Package{ID: "package"}, files: []loadedFile{{path: "a.go"}}}}
	if _, err := extractEdges(selected, nil, nil, map[string]*File{"a.go": {ID: "file"}}, 0); err == nil {
		t.Fatal("extractEdges ignored contains-edge maximum")
	}
	definition := symbolDefinition{record: Symbol{ID: "symbol", FileID: "file"}}
	if _, err := extractEdges(nil, []symbolDefinition{definition}, nil, nil, 0); err == nil {
		t.Fatal("extractEdges ignored defines-edge maximum")
	}
}

func TestAnalyzeExtractsEveryDeclarationKindAndPropagatesSymbolLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/declarations\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := []byte(`package declarations

type Contract interface { Run() }
const Answer = 42
var _, Kept = 0, 1
func First() {}
func Second() {}
`)
	request := Request{Checkout: root, Files: []SourceFile{{Path: "declarations.go", Bytes: source}}}
	result, err := Analyze(context.Background(), request)
	if err != nil {
		t.Fatalf("Analyze declarations: %v", err)
	}
	if len(result.Symbols) != 5 {
		t.Fatalf("symbols = %+v, want interface, constant, variable, and two functions", result.Symbols)
	}

	request.Limits = DefaultLimits
	request.Limits.MaxSymbols = 1
	if _, err := Analyze(context.Background(), request); err == nil {
		t.Fatal("Analyze ignored symbol maximum")
	}
	request.Limits = Limits{}
	request.ExpectedModulePath = "example.test/renamed"
	if _, err := Analyze(context.Background(), request); err == nil {
		t.Fatal("Analyze ignored expected module path")
	}
}

func TestAnalyzerRemainingSemanticBoundaryBranches(t *testing.T) {
	root := t.TempDir()
	empty := &packages.Package{
		ID: "empty", PkgPath: "example.test/mod/empty", Name: "empty",
		Module: &packages.Module{Path: "example.test/mod", Dir: root},
	}
	modulePath, selected, err := selectPackages(root, "", []*packages.Package{empty}, 1)
	if err != nil || modulePath != "example.test/mod" || len(selected) != 0 {
		t.Fatalf("empty package selection = path %q packages %d err=%v", modulePath, len(selected), err)
	}

	pkg := types.NewPackage("example.test/p", "p")
	object := types.NewVar(token.NoPos, pkg, "Value", types.Typ[types.Int])
	loaded := loadedPackage{record: Package{ImportPath: pkg.Path()}, load: &packages.Package{Fset: token.NewFileSet()}}
	if _, err := makeDefinition(&loaded, &File{}, "a.go", SymbolVariable, "Value", "", object, &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("Value")}}, ""); err == nil {
		t.Fatal("makeDefinition accepted a declaration without source positions")
	}

	noSource := []loadedPackage{{
		record: Package{}, load: &packages.Package{Imports: map[string]*packages.Package{}},
		files: []loadedFile{{path: "a.go"}},
	}}
	if _, err := extractEdges(noSource, nil, nil, map[string]*File{"a.go": {ID: "file"}}, 1); err != nil {
		t.Fatalf("extractEdges empty source endpoint: %v", err)
	}

	imports := []loadedPackage{
		{record: Package{ID: "p", ImportPath: "p", Variant: VariantProduction}, load: &packages.Package{Imports: map[string]*packages.Package{"q": {}}}},
		{record: Package{ID: "q", ImportPath: "q", Variant: VariantProduction}, load: &packages.Package{Imports: map[string]*packages.Package{}}},
	}
	if _, err := extractEdges(imports, nil, nil, nil, 0); err == nil {
		t.Fatal("extractEdges ignored import-edge maximum")
	}

	basicCall := &ast.ExprStmt{X: &ast.CallExpr{Fun: &ast.BasicLit{Kind: token.INT, Value: "1"}}}
	definition := symbolDefinition{
		record: Symbol{}, decl: basicCall,
		pkg: &packages.Package{TypesInfo: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}},
	}
	if _, err := extractEdges(nil, []symbolDefinition{definition}, nil, nil, 1); err != nil {
		t.Fatalf("extractEdges non-identifier call: %v", err)
	}

	first := ast.NewIdent("Value")
	second := ast.NewIdent("Value")
	references := &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: first}, &ast.ExprStmt{X: second}}}
	definition = symbolDefinition{
		record: Symbol{ID: "source", FileID: "file"}, decl: references,
		pkg: &packages.Package{TypesInfo: &types.Info{Uses: map[*ast.Ident]types.Object{first: object, second: object}, Types: map[ast.Expr]types.TypeAndValue{}}},
	}
	if _, err := extractEdges(nil, []symbolDefinition{definition}, map[string]string{objectKey(object): "target"}, nil, 1); err == nil {
		t.Fatal("extractEdges ignored reference-edge maximum")
	}

	localType := types.NewNamed(types.NewTypeName(token.NoPos, nil, "Local", nil), types.NewStruct(nil, nil), nil)
	if got := stableTypeString(localType); got != "Local" {
		t.Fatalf("stableTypeString package-less named type = %q", got)
	}
}
