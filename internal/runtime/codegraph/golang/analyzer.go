// Package golang extracts a deterministic, storage-independent semantic graph
// from an exact set of Go source bytes.
package golang

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"
)

const (
	MaxPackages    = 2_000
	MaxSymbols     = 250_000
	MaxEdges       = 2_000_000
	MaxDiagnostics = 1_000

	maxDiagnosticBytes = 1_024
)

var ErrInvalidInput = errors.New("codegraph golang: invalid analysis input")
var ErrLimitExceeded = errors.New("codegraph golang: analysis limit exceeded")
var ErrPackageLoad = errors.New("codegraph golang: package loading failed")

type SymbolKind string

const (
	SymbolFunction  SymbolKind = "function"
	SymbolMethod    SymbolKind = "method"
	SymbolType      SymbolKind = "type"
	SymbolInterface SymbolKind = "interface"
	SymbolConstant  SymbolKind = "constant"
	SymbolVariable  SymbolKind = "variable"
)

type Relationship string

const (
	RelationshipContains   Relationship = "contains"
	RelationshipDefines    Relationship = "defines"
	RelationshipImports    Relationship = "imports"
	RelationshipCalls      Relationship = "calls"
	RelationshipReferences Relationship = "references"
	RelationshipUsesType   Relationship = "uses_type"
)

type Provenance string

const (
	ProvenanceGoPackages Provenance = "go_packages"
	ProvenanceGoTypes    Provenance = "go_types"
	ProvenanceGoAST      Provenance = "go_ast"
	ProvenanceParser     Provenance = "parser"
	ProvenanceHeuristic  Provenance = "heuristic"
)

type PackageVariant string

const (
	VariantProduction   PackageVariant = "production"
	VariantInternalTest PackageVariant = "internal_test"
	VariantExternalTest PackageVariant = "external_test"
)

type DiagnosticSeverity string

const (
	DiagnosticWarning DiagnosticSeverity = "warning"
	DiagnosticError   DiagnosticSeverity = "error"
)

type Limits struct {
	MaxPackages    int
	MaxSymbols     int
	MaxEdges       int
	MaxDiagnostics int
}

var DefaultLimits = Limits{
	MaxPackages: MaxPackages, MaxSymbols: MaxSymbols, MaxEdges: MaxEdges, MaxDiagnostics: MaxDiagnostics,
}

type SourceFile struct {
	Path   string
	Bytes  []byte
	SHA256 string
}

type Request struct {
	Checkout           string
	ExpectedModulePath string
	Files              []SourceFile
	// Suppress contains repository-relative untracked or ignored Go paths.
	// Analyze also discovers and suppresses any additional on-disk Go paths
	// absent from Files without reading their contents.
	Suppress []string
	Limits   Limits
}

type Package struct {
	ID         string
	ImportPath string
	Name       string
	ForTest    string
	Variant    PackageVariant
}

type File struct {
	ID        string
	PackageID string
	Path      string
}

type Symbol struct {
	ID            string
	Fingerprint   string
	PackageID     string
	FileID        string
	FilePath      string
	Kind          SymbolKind
	Name          string
	QualifiedName string
	Signature     string
	StartByte     int64
	EndByte       int64
	StartLine     int
	EndLine       int
}

type Edge struct {
	ID           string
	SourceID     string
	TargetID     string
	Relationship Relationship
	Confidence   float64
	Provenance   Provenance
}

type Diagnostic struct {
	ID       string
	Severity DiagnosticSeverity
	Code     string
	Message  string
}

type Result struct {
	ModulePath  string
	Packages    []Package
	Files       []File
	Symbols     []Symbol
	Edges       []Edge
	Diagnostics []Diagnostic
}

type loadedPackage struct {
	load   *packages.Package
	record Package
	files  []loadedFile
}

type loadedFile struct {
	path string
	file *ast.File
}

type symbolDefinition struct {
	record Symbol
	object types.Object
	decl   ast.Node
	pkg    *packages.Package
}

// Analyze loads Go packages from checkout while overlaying every tracked file
// with its exact indexed bytes and suppressing every other on-disk Go file.
func Analyze(ctx context.Context, request Request) (Result, error) {
	root, sources, overlay, limits, err := prepare(request)
	if err != nil {
		return Result{}, err
	}
	configuration := &packages.Config{
		Context:    ctx,
		Mode:       packages.LoadSyntax | packages.NeedModule | packages.NeedForTest,
		Dir:        root,
		Env:        hermeticEnvironment(),
		BuildFlags: []string{"-mod=readonly"},
		Tests:      true,
		Overlay:    overlay,
	}
	loaded, loadErr := packages.Load(configuration, "./...")
	if loadErr != nil {
		return Result{}, fmt.Errorf("%w: %s", ErrPackageLoad, sanitizeDiagnostic(root, loadErr.Error()))
	}

	result := Result{}
	diagnostics, diagnosticErr := collectDiagnostics(root, loaded, limits.MaxDiagnostics)
	result.Diagnostics = diagnostics
	if diagnosticErr != nil {
		return result, diagnosticErr
	}
	if len(diagnostics) != 0 {
		return result, fmt.Errorf("%w: %s", ErrPackageLoad, diagnostics[0].Message)
	}

	modulePath, selected, err := selectPackages(root, request.ExpectedModulePath, loaded, limits.MaxPackages)
	if err != nil {
		return result, err
	}
	result.ModulePath = modulePath
	result.Files = buildFiles(modulePath, sources)
	fileByPath := make(map[string]*File, len(result.Files))
	for index := range result.Files {
		fileByPath[result.Files[index].Path] = &result.Files[index]
	}
	for index := range selected {
		result.Packages = append(result.Packages, selected[index].record)
		for _, loadedFile := range selected[index].files {
			if file := fileByPath[loadedFile.path]; file != nil && file.PackageID == "" {
				file.PackageID = selected[index].record.ID
			}
		}
	}

	definitions, objectTargets, err := extractSymbols(selected, fileByPath, limits.MaxSymbols)
	if err != nil {
		return result, err
	}
	result.Symbols = make([]Symbol, 0, len(definitions))
	for _, definition := range definitions {
		result.Symbols = append(result.Symbols, definition.record)
	}
	edges, err := extractEdges(selected, definitions, objectTargets, fileByPath, limits.MaxEdges)
	if err != nil {
		return result, err
	}
	result.Edges = edges
	sortResult(&result)
	return result, nil
}

func prepare(request Request) (string, map[string]SourceFile, map[string][]byte, Limits, error) {
	limits, err := resolvedLimits(request.Limits)
	if err != nil {
		return "", nil, nil, Limits{}, err
	}
	absolute, err := filepath.Abs(request.Checkout)
	if err != nil {
		return "", nil, nil, Limits{}, fmt.Errorf("%w: checkout: %v", ErrInvalidInput, err)
	}
	root, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, nil, Limits{}, fmt.Errorf("%w: checkout: %v", ErrInvalidInput, err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", nil, nil, Limits{}, fmt.Errorf("%w: checkout is not a directory", ErrInvalidInput)
	}
	root = filepath.Clean(root)
	sources := make(map[string]SourceFile, len(request.Files))
	overlay := make(map[string][]byte, len(request.Files)+len(request.Suppress))
	for _, source := range request.Files {
		clean, err := cleanRelativeGoPath(source.Path)
		if err != nil {
			return "", nil, nil, Limits{}, err
		}
		if _, exists := sources[clean]; exists {
			return "", nil, nil, Limits{}, fmt.Errorf("%w: duplicate source path %q", ErrInvalidInput, clean)
		}
		if source.SHA256 != "" && source.SHA256 != sha256Value(source.Bytes) {
			return "", nil, nil, Limits{}, fmt.Errorf("%w: source hash mismatch for %q", ErrInvalidInput, clean)
		}
		source.Path = clean
		source.Bytes = append([]byte(nil), source.Bytes...)
		sources[clean] = source
		overlay[filepath.Join(root, filepath.FromSlash(clean))] = source.Bytes
	}
	for _, suppressed := range request.Suppress {
		clean, err := cleanRelativeGoPath(suppressed)
		if err != nil {
			return "", nil, nil, Limits{}, err
		}
		if _, tracked := sources[clean]; tracked {
			return "", nil, nil, Limits{}, fmt.Errorf("%w: tracked path %q is also suppressed", ErrInvalidInput, clean)
		}
		overlay[filepath.Join(root, filepath.FromSlash(clean))] = suppressedSource
	}
	if err := suppressUntracked(root, sources, overlay); err != nil {
		return "", nil, nil, Limits{}, err
	}
	return root, sources, overlay, limits, nil
}

var suppressedSource = []byte("//go:build wormhole_codegraph_suppressed\n\npackage ignored\n")

func suppressUntracked(root string, sources map[string]SourceFile, overlay map[string][]byte) error {
	return filepath.WalkDir(root, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("%w: enumerate source paths", ErrInvalidInput)
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		relative, err := filepath.Rel(root, fullPath)
		if err != nil {
			return fmt.Errorf("%w: enumerate source paths", ErrInvalidInput)
		}
		clean := filepath.ToSlash(relative)
		if _, tracked := sources[clean]; !tracked {
			overlay[fullPath] = suppressedSource
		}
		return nil
	})
}

func resolvedLimits(limits Limits) (Limits, error) {
	if limits == (Limits{}) {
		return DefaultLimits, nil
	}
	if limits.MaxPackages <= 0 || limits.MaxSymbols <= 0 || limits.MaxEdges <= 0 || limits.MaxDiagnostics <= 0 {
		return Limits{}, fmt.Errorf("%w: all limits must be positive", ErrInvalidInput)
	}
	return limits, nil
}

func cleanRelativeGoPath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.Contains(value, "\\") || !strings.HasSuffix(value, ".go") || path.Clean(value) != value || !filepath.IsLocal(filepath.FromSlash(value)) {
		return "", fmt.Errorf("%w: unsafe Go path", ErrInvalidInput)
	}
	return value, nil
}

func hermeticEnvironment() []string {
	blocked := map[string]struct{}{
		"GOPACKAGESDRIVER": {}, "GOWORK": {}, "GOENV": {}, "GOFLAGS": {}, "GOTOOLCHAIN": {}, "CGO_ENABLED": {},
		"GOOS": {}, "GOARCH": {}, "GO386": {}, "GOAMD64": {}, "GOARM": {}, "GOARM64": {}, "GOMIPS": {}, "GOMIPS64": {},
		"GOPPC64": {}, "GORISCV64": {}, "GOWASM": {}, "GOEXPERIMENT": {}, "GO111MODULE": {},
	}
	environment := make([]string, 0, len(os.Environ())+11)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if _, remove := blocked[name]; !remove {
			environment = append(environment, variable)
		}
	}
	return append(environment,
		"GOPACKAGESDRIVER=off",
		"GOWORK=off",
		"GOENV=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=local",
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
		"GOAMD64=v1",
		"GOEXPERIMENT=",
		"GO111MODULE=on",
	)
}

func collectDiagnostics(root string, loaded []*packages.Package, maximum int) ([]Diagnostic, error) {
	type item struct{ pkg, message string }
	items := make([]item, 0)
	for _, pkg := range loaded {
		for _, packageError := range pkg.Errors {
			items = append(items, item{pkg: pkg.ID, message: sanitizeDiagnostic(root, packageError.Error())})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].pkg == items[j].pkg {
			return items[i].message < items[j].message
		}
		return items[i].pkg < items[j].pkg
	})
	if len(items) > maximum {
		return nil, fmt.Errorf("%w: diagnostics=%d maximum=%d", ErrLimitExceeded, len(items), maximum)
	}
	diagnostics := make([]Diagnostic, 0, len(items))
	for _, current := range items {
		diagnostics = append(diagnostics, Diagnostic{
			ID: identity("diagnostic", current.pkg, current.message), Severity: DiagnosticError, Code: "go_package_error", Message: current.message,
		})
	}
	return diagnostics, nil
}

func sanitizeDiagnostic(root, message string) string {
	message = strings.ReplaceAll(message, root, "<checkout>")
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
	if len(message) > maxDiagnosticBytes {
		message = message[:maxDiagnosticBytes]
	}
	return message
}

func selectPackages(root, expectedModulePath string, loaded []*packages.Package, maximum int) (string, []loadedPackage, error) {
	selected := make([]loadedPackage, 0, len(loaded))
	modulePath := ""
	seen := make(map[string]struct{}, len(loaded))
	for _, pkg := range loaded {
		if pkg.Module == nil || pkg.Module.Path == "" || pkg.Module.Dir == "" || pkg.Name == "main" && strings.HasSuffix(pkg.ID, ".test") {
			continue
		}
		moduleDir, err := filepath.EvalSymlinks(pkg.Module.Dir)
		if err != nil || filepath.Clean(moduleDir) != root {
			continue
		}
		if modulePath == "" {
			modulePath = pkg.Module.Path
		} else if modulePath != pkg.Module.Path {
			return "", nil, fmt.Errorf("%w: checkout resolved multiple module paths", ErrPackageLoad)
		}
		variant := packageVariant(pkg)
		if variant == "" {
			continue
		}
		key := pkg.PkgPath + "\x00" + string(variant)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		files, err := selectedSyntaxFiles(root, pkg, variant)
		if err != nil {
			return "", nil, err
		}
		if len(files) == 0 {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, loadedPackage{load: pkg, record: Package{
			ID: identity("package", pkg.Module.Path, pkg.PkgPath, string(variant)), ImportPath: pkg.PkgPath, Name: pkg.Name, ForTest: pkg.ForTest, Variant: variant,
		}, files: files})
		if len(selected) > maximum {
			return "", nil, fmt.Errorf("%w: packages=%d maximum=%d", ErrLimitExceeded, len(selected), maximum)
		}
	}
	if modulePath == "" {
		return "", nil, fmt.Errorf("%w: no package in checkout module", ErrPackageLoad)
	}
	if expectedModulePath != "" && modulePath != expectedModulePath {
		return "", nil, fmt.Errorf("%w: module path changed", ErrPackageLoad)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].record.ID < selected[j].record.ID })
	return modulePath, selected, nil
}

func packageVariant(pkg *packages.Package) PackageVariant {
	if pkg.ForTest == "" {
		return VariantProduction
	}
	if pkg.PkgPath == pkg.ForTest {
		return VariantInternalTest
	}
	if strings.HasSuffix(pkg.PkgPath, "_test") {
		return VariantExternalTest
	}
	return ""
}

func selectedSyntaxFiles(root string, pkg *packages.Package, variant PackageVariant) ([]loadedFile, error) {
	files := make([]loadedFile, 0, len(pkg.Syntax))
	for index, syntax := range pkg.Syntax {
		if index >= len(pkg.CompiledGoFiles) {
			return nil, fmt.Errorf("%w: package syntax/file mismatch", ErrPackageLoad)
		}
		fullPath := filepath.Clean(pkg.CompiledGoFiles[index])
		relative, err := filepath.Rel(root, fullPath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		goPath := filepath.ToSlash(relative)
		isTest := strings.HasSuffix(goPath, "_test.go")
		if variant == VariantProduction && isTest || variant != VariantProduction && !isTest {
			continue
		}
		files = append(files, loadedFile{path: goPath, file: syntax})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func buildFiles(modulePath string, sources map[string]SourceFile) []File {
	files := make([]File, 0, len(sources))
	for sourcePath := range sources {
		files = append(files, File{ID: identity("file", modulePath, sourcePath), Path: sourcePath})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })
	return files
}

func extractSymbols(selected []loadedPackage, fileByPath map[string]*File, maximum int) ([]symbolDefinition, map[string]string, error) {
	definitions := make([]symbolDefinition, 0)
	objectTargets := make(map[string]string)
	for packageIndex := range selected {
		pkg := &selected[packageIndex]
		for _, loadedFile := range pkg.files {
			fileRecord := fileByPath[loadedFile.path]
			if fileRecord == nil {
				return nil, nil, fmt.Errorf("%w: package loaded non-inventory path %q", ErrPackageLoad, loadedFile.path)
			}
			initOrdinal := 0
			for _, declaration := range loadedFile.file.Decls {
				switch current := declaration.(type) {
				case *ast.FuncDecl:
					object := pkg.load.TypesInfo.Defs[current.Name]
					kind := SymbolFunction
					receiver := ""
					if current.Recv != nil {
						kind = SymbolMethod
						receiver = receiverName(object)
					}
					ordinal := ""
					if current.Name.Name == "init" {
						initOrdinal++
						ordinal = fmt.Sprintf("%s:%d", loadedFile.path, initOrdinal)
					}
					definition, err := makeDefinition(pkg, fileRecord, loadedFile.path, kind, current.Name.Name, receiver, object, current, ordinal)
					if err != nil {
						return nil, nil, err
					}
					definitions = append(definitions, definition)
				case *ast.GenDecl:
					for _, specification := range current.Specs {
						switch spec := specification.(type) {
						case *ast.TypeSpec:
							kind := SymbolType
							if _, ok := spec.Type.(*ast.InterfaceType); ok {
								kind = SymbolInterface
							}
							definition, err := makeDefinition(pkg, fileRecord, loadedFile.path, kind, spec.Name.Name, "", pkg.load.TypesInfo.Defs[spec.Name], spec, "")
							if err != nil {
								return nil, nil, err
							}
							definitions = append(definitions, definition)
						case *ast.ValueSpec:
							kind := SymbolVariable
							if current.Tok == token.CONST {
								kind = SymbolConstant
							}
							for _, name := range spec.Names {
								if name.Name == "_" {
									continue
								}
								definition, err := makeDefinition(pkg, fileRecord, loadedFile.path, kind, name.Name, "", pkg.load.TypesInfo.Defs[name], spec, "")
								if err != nil {
									return nil, nil, err
								}
								definitions = append(definitions, definition)
							}
						}
					}
				}
				if len(definitions) > maximum {
					return nil, nil, fmt.Errorf("%w: symbols=%d maximum=%d", ErrLimitExceeded, len(definitions), maximum)
				}
			}
		}
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].record.ID < definitions[j].record.ID })
	seenIDs := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, duplicate := seenIDs[definition.record.ID]; duplicate {
			return nil, nil, fmt.Errorf("%w: deterministic symbol collision", ErrPackageLoad)
		}
		seenIDs[definition.record.ID] = struct{}{}
		// init cannot be referenced or called from Go source. Multiple valid
		// init declarations intentionally share one object shape, so they are
		// symbols but never semantic reference targets.
		if definition.object != nil && definition.record.Name != "init" {
			key := objectKey(definition.object)
			if existing, exists := objectTargets[key]; exists && existing != definition.record.ID {
				return nil, nil, fmt.Errorf("%w: semantic object collision", ErrPackageLoad)
			}
			objectTargets[key] = definition.record.ID
		}
	}
	return definitions, objectTargets, nil
}

func makeDefinition(pkg *loadedPackage, file *File, filePath string, kind SymbolKind, name, receiver string, object types.Object, declaration ast.Node, ordinal string) (symbolDefinition, error) {
	if object == nil {
		return symbolDefinition{}, fmt.Errorf("%w: declaration %q has no type object", ErrPackageLoad, name)
	}
	signature := stableTypeString(object.Type())
	qualifiedName := pkg.record.ImportPath + "." + name
	if receiver != "" {
		qualifiedName = pkg.record.ImportPath + "." + receiver + "." + name
	}
	fingerprint := digest("symbol-fingerprint", pkg.record.ImportPath, string(pkg.record.Variant), filePath, string(kind), receiver, name, signature, ordinal)
	startByte, endByte, startLine, endLine, err := sourceRange(pkg.load.Fset, declaration.Pos(), declaration.End())
	if err != nil {
		return symbolDefinition{}, err
	}
	record := Symbol{
		ID: identity("symbol", fingerprint), Fingerprint: "sha256:" + fingerprint,
		PackageID: pkg.record.ID, FileID: file.ID, FilePath: filePath, Kind: kind, Name: name,
		QualifiedName: qualifiedName, Signature: signature,
		StartByte: startByte, EndByte: endByte, StartLine: startLine, EndLine: endLine,
	}
	return symbolDefinition{record: record, object: object, decl: declaration, pkg: pkg.load}, nil
}

func sourceRange(fileSet *token.FileSet, start, end token.Pos) (int64, int64, int, int, error) {
	file := fileSet.File(start)
	if file == nil || fileSet.File(end) != file {
		return 0, 0, 0, 0, fmt.Errorf("%w: declaration has invalid source range", ErrPackageLoad)
	}
	startOffset := file.Offset(start)
	endOffset := file.Offset(end)
	if endOffset <= startOffset {
		return 0, 0, 0, 0, fmt.Errorf("%w: declaration has empty source range", ErrPackageLoad)
	}
	return int64(startOffset), int64(endOffset), file.Line(start), file.Line(end), nil
}

func receiverName(object types.Object) string {
	function, ok := object.(*types.Func)
	if !ok {
		return ""
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return ""
	}
	receiverType := signature.Recv().Type()
	pointer := false
	if current, ok := receiverType.(*types.Pointer); ok {
		pointer = true
		receiverType = current.Elem()
	}
	named, ok := receiverType.(*types.Named)
	if !ok {
		return stableTypeString(receiverType)
	}
	if pointer {
		return "(*" + named.Obj().Name() + ")"
	}
	return "(" + named.Obj().Name() + ")"
}

func extractEdges(selected []loadedPackage, definitions []symbolDefinition, objectTargets map[string]string, fileByPath map[string]*File, maximum int) ([]Edge, error) {
	type edgeKey struct {
		source, target string
		relationship   Relationship
		provenance     Provenance
	}
	edges := make(map[edgeKey]Edge)
	add := func(source, target string, relationship Relationship, provenance Provenance) error {
		if source == "" || target == "" {
			return nil
		}
		key := edgeKey{source: source, target: target, relationship: relationship, provenance: provenance}
		if _, exists := edges[key]; exists {
			return nil
		}
		edges[key] = Edge{ID: identity("edge", source, target, string(relationship), string(provenance)), SourceID: source, TargetID: target, Relationship: relationship, Confidence: 1, Provenance: provenance}
		if len(edges) > maximum {
			return fmt.Errorf("%w: edges=%d maximum=%d", ErrLimitExceeded, len(edges), maximum)
		}
		return nil
	}

	packageTargets := make(map[string]string, len(selected))
	for _, pkg := range selected {
		key := pkg.record.ImportPath
		if pkg.record.Variant == VariantProduction || packageTargets[key] == "" {
			packageTargets[key] = pkg.record.ID
		}
		for _, loadedFile := range pkg.files {
			if file := fileByPath[loadedFile.path]; file != nil {
				if err := add(pkg.record.ID, file.ID, RelationshipContains, ProvenanceGoPackages); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, definition := range definitions {
		if err := add(definition.record.FileID, definition.record.ID, RelationshipDefines, ProvenanceGoAST); err != nil {
			return nil, err
		}
	}
	for _, pkg := range selected {
		importPaths := make([]string, 0, len(pkg.load.Imports))
		for importPath := range pkg.load.Imports {
			importPaths = append(importPaths, importPath)
		}
		sort.Strings(importPaths)
		for _, importPath := range importPaths {
			if target := packageTargets[importPath]; target != "" {
				if err := add(pkg.record.ID, target, RelationshipImports, ProvenanceGoPackages); err != nil {
					return nil, err
				}
			}
		}
	}

	for _, definition := range definitions {
		callIdentifiers := make(map[*ast.Ident]struct{})
		var callEdgeErr error
		ast.Inspect(definition.decl, func(node ast.Node) bool {
			if callEdgeErr != nil {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if typeAndValue, known := definition.pkg.TypesInfo.Types[call.Fun]; known && typeAndValue.IsType() {
				// A conversion such as Local(value) is a type use. Leaving
				// its identifier out of callIdentifiers lets the type-use
				// pass below emit the authoritative uses_type edge.
				return true
			}
			identifier := calledIdentifier(call.Fun)
			if identifier == nil {
				return true
			}
			object := usedObject(definition.pkg.TypesInfo, identifier)
			if _, exactFunction := object.(*types.Func); !exactFunction {
				// Calling a function-valued variable does not identify the
				// invoked declaration without data-flow analysis. Retain only
				// the exact reference to that variable in the pass below.
				return true
			}
			callIdentifiers[identifier] = struct{}{}
			if target := objectTargets[objectKey(object)]; target != "" {
				callEdgeErr = add(definition.record.ID, target, RelationshipCalls, ProvenanceGoTypes)
			}
			return callEdgeErr == nil
		})
		if callEdgeErr != nil {
			return nil, callEdgeErr
		}
		var edgeErr error
		ast.Inspect(definition.decl, func(node ast.Node) bool {
			if edgeErr != nil {
				return false
			}
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, call := callIdentifiers[identifier]; call {
				return true
			}
			object := definition.pkg.TypesInfo.Uses[identifier]
			if object == nil {
				return true
			}
			target := objectTargets[objectKey(object)]
			if target == "" {
				return true
			}
			relationship := RelationshipReferences
			if _, typeUse := object.(*types.TypeName); typeUse {
				relationship = RelationshipUsesType
			}
			edgeErr = add(definition.record.ID, target, relationship, ProvenanceGoTypes)
			return edgeErr == nil
		})
		if edgeErr != nil {
			return nil, edgeErr
		}
	}

	result := make([]Edge, 0, len(edges))
	for _, edge := range edges {
		result = append(result, edge)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func calledIdentifier(expression ast.Expr) *ast.Ident {
	switch current := expression.(type) {
	case *ast.Ident:
		return current
	case *ast.SelectorExpr:
		return current.Sel
	case *ast.IndexExpr:
		return calledIdentifier(current.X)
	case *ast.IndexListExpr:
		return calledIdentifier(current.X)
	case *ast.ParenExpr:
		return calledIdentifier(current.X)
	default:
		return nil
	}
}

func usedObject(info *types.Info, identifier *ast.Ident) types.Object {
	if info == nil || identifier == nil {
		return nil
	}
	if object := info.Uses[identifier]; object != nil {
		return object
	}
	return info.Defs[identifier]
}

func objectKey(object types.Object) string {
	if object == nil || object.Pkg() == nil {
		return ""
	}
	kind := fmt.Sprintf("%T", object)
	receiver := ""
	if function, ok := object.(*types.Func); ok {
		receiver = receiverName(function)
	}
	return strings.Join([]string{object.Pkg().Path(), kind, receiver, object.Name(), stableTypeString(object.Type())}, "\x00")
}

func stableTypeString(value types.Type) string {
	if value == nil {
		return ""
	}
	return types.TypeString(value, func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}
		return pkg.Path()
	})
}

func sortResult(result *Result) {
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].ID < result.Packages[j].ID })
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].ID < result.Files[j].ID })
	sort.Slice(result.Symbols, func(i, j int) bool { return result.Symbols[i].ID < result.Symbols[j].ID })
	sort.Slice(result.Edges, func(i, j int) bool { return result.Edges[i].ID < result.Edges[j].ID })
	sort.Slice(result.Diagnostics, func(i, j int) bool { return result.Diagnostics[i].ID < result.Diagnostics[j].ID })
}

func identity(domain string, fields ...string) string {
	return "cg:" + domain + ":" + digest(append([]string{domain}, fields...)...)
}

func digest(fields ...string) string {
	hash := sha256.New()
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sha256Value(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
