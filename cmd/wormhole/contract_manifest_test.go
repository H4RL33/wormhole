package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/config"
	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
)

type alphaCLIContract struct {
	Mode        string            `json:"mode"`
	CLI         alphaCLI          `json:"cli"`
	Environment []string          `json:"environment"`
	Paths       map[string]string `json:"paths"`
	Local       alphaLocal        `json:"local_protocol"`
	Migrations  []string          `json:"migrations"`
	Artifacts   alphaArtifacts    `json:"artifacts"`
}

type alphaCLI struct {
	Commands     []alphaCommand   `json:"commands"`
	ConfigFiles  alphaConfigFiles `json:"config_files"`
	ExitBehavior map[string]int   `json:"exit_behavior"`
}

type alphaConfigFiles struct {
	CredentialFields    []string `json:"credential_fields"`
	CredentialsFormat   string   `json:"credentials_format"`
	ProjectConfigFields []string `json:"project_config_fields"`
	ProjectConfigFormat string   `json:"project_config_format"`
}

type alphaCommand struct {
	Name        string   `json:"name"`
	Usage       string   `json:"usage"`
	Flags       []string `json:"flags"`
	Positionals []string `json:"positionals"`
	HelpExit    int      `json:"help_exit"`
}

type alphaLocal struct {
	Transport           string              `json:"transport"`
	Framing             string              `json:"framing"`
	JSONRPCVersion      string              `json:"jsonrpc_version"`
	MCPProtocolVersion  string              `json:"mcp_protocol_version"`
	Methods             []string            `json:"methods"`
	ServerNotifications []alphaNotification `json:"server_notifications"`
}

type alphaNotification struct {
	Method         string   `json:"method"`
	EnvelopeFields []string `json:"envelope_fields"`
	Params         string   `json:"params"`
}

type alphaArtifacts struct {
	Binaries         []string `json:"binaries"`
	FabricImage      string   `json:"fabric_image"`
	ReleasePlatforms []string `json:"release_platforms"`
}

func TestAlphaContractCLI(t *testing.T) {
	manifest := readAlphaCLIContract(t)
	if manifest.Mode != "alpha-inventory" {
		t.Fatalf("mode = %q, want alpha-inventory", manifest.Mode)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"help"}, &stdout, &stderr); got != manifest.CLI.ExitBehavior["help"] {
		t.Fatalf("help exit = %d, manifest = %d", got, manifest.CLI.ExitBehavior["help"])
	}
	help := stdout.String()
	stdout.Reset()
	stderr.Reset()
	if got := run(nil, &stdout, &stderr); got != manifest.CLI.ExitBehavior["missing_command"] {
		t.Fatalf("missing command exit = %d, manifest = %d", got, manifest.CLI.ExitBehavior["missing_command"])
	}
	stdout.Reset()
	stderr.Reset()
	if got := run([]string{"not-a-command"}, &stdout, &stderr); got != manifest.CLI.ExitBehavior["unknown_command"] {
		t.Fatalf("unknown command exit = %d, manifest = %d", got, manifest.CLI.ExitBehavior["unknown_command"])
	}

	actualUsage := commandUsageLines(help)
	for _, command := range manifest.CLI.Commands {
		gotUsage, ok := actualUsage[command.Name]
		if !ok {
			t.Errorf("%s is missing from top-level help", command.Name)
			continue
		}
		if gotUsage.Usage != command.Usage {
			t.Errorf("%s usage = %q, manifest = %q", command.Name, gotUsage.Usage, command.Usage)
		}
		if !reflect.DeepEqual(gotUsage.Positionals, command.Positionals) {
			t.Errorf("%s positionals = %v, manifest = %v", command.Name, gotUsage.Positionals, command.Positionals)
		}

		args := append(strings.Fields(command.Name), "--help")
		stdout.Reset()
		stderr.Reset()
		gotExit := run(args, &stdout, &stderr)
		if gotExit != command.HelpExit {
			t.Errorf("%s --help exit = %d, manifest = %d", command.Name, gotExit, command.HelpExit)
		}
		gotFlags := flagNames(stdout.String() + stderr.String())
		if !reflect.DeepEqual(gotFlags, command.Flags) {
			t.Errorf("%s flags = %v, manifest = %v", command.Name, gotFlags, command.Flags)
		}
	}
	if len(actualUsage) != len(manifest.CLI.Commands) {
		t.Fatalf("help lists %d commands, manifest has %d", len(actualUsage), len(manifest.CLI.Commands))
	}

	actualConfigFiles := alphaConfigFiles{
		CredentialFields:    taggedFieldNames(t, credentials{}, "json"),
		CredentialsFormat:   "json",
		ProjectConfigFields: taggedFieldNames(t, config.Config{}, "toml"),
		ProjectConfigFormat: "toml",
	}
	if !reflect.DeepEqual(actualConfigFiles, manifest.CLI.ConfigFiles) {
		t.Fatalf("config files = %#v, manifest = %#v", actualConfigFiles, manifest.CLI.ConfigFiles)
	}
}

func TestAlphaContractEnvironmentPathsAndLocalProtocol(t *testing.T) {
	manifest := readAlphaCLIContract(t)
	if got := productionEnvironment(t); !reflect.DeepEqual(got, manifest.Environment) {
		t.Fatalf("environment = %v, manifest = %v", got, manifest.Environment)
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	runtimeDir := filepath.Join(root, "run")
	dataDir := filepath.Join(root, "data")
	configDir := filepath.Join(root, "config")
	tempDir := filepath.Join(root, "tmp")
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tempDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	for _, directory := range []string{home, runtimeDir, dataDir, configDir, tempDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credentialDir := filepath.Join(home, ".wormhole", "credentials")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialDir, "contract.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeCfg, err := runtimeconfig.Load("contract")
	if err != nil {
		t.Fatalf("runtime config: %v", err)
	}

	xdgGlobalConfig := config.GlobalConfigPath()
	xdgSocket := gatewaySocketPath()
	if runtimeCfg.DBPath != filepath.Join(dataDir, "wormhole", "wormholed.db") {
		t.Fatalf("XDG runtime database = %q", runtimeCfg.DBPath)
	}
	if runtimeCfg.SocketPath != filepath.Join(runtimeDir, "wormhole", "wormholed.sock") ||
		xdgSocket != runtimeCfg.SocketPath {
		t.Fatalf("XDG runtime socket: config = %q, CLI = %q", runtimeCfg.SocketPath, xdgSocket)
	}
	if xdgGlobalConfig != filepath.Join(configDir, "wormhole", "config.toml") {
		t.Fatalf("XDG global config = %q", xdgGlobalConfig)
	}

	projectRoot := filepath.Join(root, "project")
	nestedProjectDir := filepath.Join(projectRoot, "nested")
	projectConfig := filepath.Join(projectRoot, ".wormhole", "config.toml")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nestedProjectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectConfig, []byte("project = \"contract\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nestedProjectDir); err != nil {
		t.Fatal(err)
	}
	resolvedProjectConfig := config.LocalConfigPath()
	if err := os.Chdir(originalDir); err != nil {
		t.Fatal(err)
	}
	if resolvedProjectConfig != projectConfig {
		t.Fatalf("project config = %q, want %q", resolvedProjectConfig, projectConfig)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	fallbackCfg, err := runtimeconfig.Load("contract")
	if err != nil {
		t.Fatalf("fallback runtime config: %v", err)
	}
	fallbackGlobalConfig := config.GlobalConfigPath()
	fallbackSocket := gatewaySocketPath()
	if fallbackCfg.DBPath != filepath.Join(home, ".local", "share", "wormhole", "wormholed.db") {
		t.Fatalf("fallback runtime database = %q", fallbackCfg.DBPath)
	}
	if fallbackCfg.SocketPath != filepath.Join(tempDir, "wormhole-runtime", "wormhole", "wormholed.sock") ||
		fallbackSocket != fallbackCfg.SocketPath {
		t.Fatalf("fallback runtime socket: config = %q, CLI = %q", fallbackCfg.SocketPath, fallbackSocket)
	}
	if fallbackGlobalConfig != filepath.Join(home, ".config", "wormhole", "config.toml") {
		t.Fatalf("fallback global config = %q", fallbackGlobalConfig)
	}

	actualPaths := map[string]string{
		"credentials":               strings.Replace(filepath.Join(credentialDir, "*.json"), home, "~", 1),
		"global_config":             strings.Replace(xdgGlobalConfig, configDir, "$XDG_CONFIG_HOME", 1),
		"global_config_fallback":    strings.Replace(fallbackGlobalConfig, home, "~", 1),
		"project_config":            strings.TrimPrefix(resolvedProjectConfig, projectRoot+string(filepath.Separator)),
		"runtime_database":          strings.Replace(runtimeCfg.DBPath, dataDir, "$XDG_DATA_HOME", 1),
		"runtime_database_fallback": strings.Replace(fallbackCfg.DBPath, home, "~", 1),
		"runtime_socket":            strings.Replace(xdgSocket, runtimeDir, "$XDG_RUNTIME_DIR", 1),
		"runtime_socket_fallback":   strings.Replace(fallbackSocket, tempDir, "$TMPDIR", 1),
	}
	if filepath.Base(runtimeCfg.DBPath) != "wormholed.db" {
		t.Fatalf("runtime database basename = %q, want retained wormholed.db", filepath.Base(runtimeCfg.DBPath))
	}
	if filepath.Base(fallbackSocket) != "wormholed.sock" {
		t.Fatalf("runtime socket basename = %q, want retained wormholed.sock", filepath.Base(fallbackSocket))
	}
	if got, err := profilesDir(); err != nil || got != credentialDir {
		t.Fatalf("credentials dir = %q, %v", got, err)
	}
	if !reflect.DeepEqual(actualPaths, manifest.Paths) {
		t.Fatalf("paths = %#v, manifest = %#v", actualPaths, manifest.Paths)
	}

	source, err := os.ReadFile("../../internal/runtime/localapi/mcp.go")
	if err != nil {
		t.Fatalf("read local protocol source: %v", err)
	}
	dispatchBody := goFunctionSource(t, source, "func (s *Server) dispatchMCPMessage")
	methodPattern := regexp.MustCompile(`(?m)^\s*case "([^"]+)":`)
	matches := methodPattern.FindAllSubmatch(dispatchBody, -1)
	methods := make([]string, 0, len(matches))
	for _, match := range matches {
		methods = append(methods, string(match[1]))
	}
	sort.Strings(methods)
	notificationPattern := regexp.MustCompile(`(?m)^\s*if err := writeMCPNotification\([^"\n]*"([^"]+)"`)
	notificationMatches := notificationPattern.FindAllSubmatch(source, -1)
	notifications := make([]alphaNotification, 0, len(notificationMatches))
	for _, match := range notificationMatches {
		notifications = append(notifications, alphaNotification{
			Method:         string(match[1]),
			EnvelopeFields: []string{"jsonrpc", "method", "params"},
			Params:         "event-payload",
		})
	}
	sort.Slice(notifications, func(i, j int) bool {
		return notifications[i].Method < notifications[j].Method
	})
	actualLocal := alphaLocal{
		Transport:           "unix-domain-socket",
		Framing:             "newline-delimited-json",
		JSONRPCVersion:      "2.0",
		MCPProtocolVersion:  "2025-11-25",
		Methods:             methods,
		ServerNotifications: notifications,
	}
	if !bytes.Contains(source, []byte(`ProtocolVersion: "2025-11-25"`)) ||
		!bytes.Contains(source, []byte("newline-delimited-JSON")) {
		t.Fatal("local protocol constants or framing documentation drifted")
	}
	if !reflect.DeepEqual(actualLocal, manifest.Local) {
		t.Fatalf("local protocol = %#v, manifest = %#v", actualLocal, manifest.Local)
	}
}

func TestAlphaContractMigrationsAndArtifacts(t *testing.T) {
	manifest := readAlphaCLIContract(t)
	matches, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	migrations := make([]string, 0, len(matches))
	for _, up := range matches {
		name := strings.TrimSuffix(filepath.Base(up), ".up.sql")
		down := strings.TrimSuffix(up, ".up.sql") + ".down.sql"
		if _, err := os.Stat(down); err != nil {
			t.Fatalf("migration %s has no down pair: %v", name, err)
		}
		migrations = append(migrations, name)
	}
	sort.Strings(migrations)
	if !reflect.DeepEqual(migrations, manifest.Migrations) {
		t.Fatalf("migrations = %v, manifest = %v", migrations, manifest.Migrations)
	}

	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	binaryMatch := regexp.MustCompile(`(?m)^BINARIES := (.+)$`).FindSubmatch(makefile)
	if binaryMatch == nil {
		t.Fatal("Makefile has no BINARIES declaration")
	}
	binaries := strings.Fields(string(binaryMatch[1]))
	sort.Strings(binaries)
	releaseScript, err := os.ReadFile("../../.github/scripts/build-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	releaseWorkflow, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	actual := alphaArtifacts{
		Binaries:         binaries,
		FabricImage:      "ghcr.io/h4rl33/wormhole-fabric",
		ReleasePlatforms: []string{"linux/amd64", "linux/arm64"},
	}
	if !bytes.Contains(releaseWorkflow, []byte("IMAGE: "+actual.FabricImage)) {
		t.Fatalf("release workflow does not publish %s", actual.FabricImage)
	}
	if !bytes.Contains(releaseScript, []byte("for arch in amd64 arm64; do")) {
		t.Fatal("release script platform loop drifted")
	}
	if !reflect.DeepEqual(actual, manifest.Artifacts) {
		t.Fatalf("artifacts = %#v, manifest = %#v", actual, manifest.Artifacts)
	}
}

func readAlphaCLIContract(t *testing.T) alphaCLIContract {
	t.Helper()
	data, err := os.ReadFile("../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatalf("read alpha contract: %v", err)
	}
	var manifest alphaCLIContract
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode alpha contract: %v", err)
	}
	return manifest
}

type parsedCommandUsage struct {
	Usage       string
	Positionals []string
}

func commandUsageLines(help string) map[string]parsedCommandUsage {
	commands := map[string]parsedCommandUsage{}
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "wormhole ") {
			continue
		}
		parts := regexp.MustCompile(`\s{2,}`).Split(line, 2)
		usage := strings.TrimPrefix(parts[0], "wormhole ")
		if strings.HasPrefix(usage, "-") {
			continue
		}
		nameParts := []string{}
		positionals := []string{}
		for _, field := range strings.Fields(usage) {
			if field == "[flags]" {
				continue
			}
			if strings.HasPrefix(field, "<") || strings.HasPrefix(field, "[") {
				positionals = append(positionals, field)
				continue
			}
			if len(positionals) == 0 {
				nameParts = append(nameParts, field)
			} else {
				positionals = append(positionals, field)
			}
		}
		if len(nameParts) == 0 {
			continue
		}
		commands[strings.Join(nameParts, " ")] = parsedCommandUsage{
			Usage:       usage,
			Positionals: positionals,
		}
	}
	return commands
}

func flagNames(help string) []string {
	pattern := regexp.MustCompile(`(?m)^\s+-([a-z][a-z0-9-]*)(?:\s|$)`)
	matches := pattern.FindAllStringSubmatch(help, -1)
	flags := make([]string, 0, len(matches))
	for _, match := range matches {
		flags = append(flags, match[1])
	}
	sort.Strings(flags)
	return flags
}

func goFunctionSource(t *testing.T, source []byte, signature string) []byte {
	t.Helper()
	start := bytes.Index(source, []byte(signature))
	if start < 0 {
		t.Fatalf("source has no %s", signature)
	}
	remaining := source[start+len(signature):]
	nextFunction := bytes.Index(remaining, []byte("\nfunc "))
	if nextFunction < 0 {
		return source[start:]
	}
	return source[start : start+len(signature)+nextFunction]
}

func productionEnvironment(t *testing.T) []string {
	t.Helper()
	names := map[string]struct{}{}
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					pkg, pkgOK := selector.X.(*ast.Ident)
					if pkgOK && pkg.Name == "os" && selector.Sel.Name == "UserHomeDir" {
						names["HOME"] = struct{}{}
					}
					if pkgOK && pkg.Name == "os" && selector.Sel.Name == "TempDir" {
						names["TMPDIR"] = struct{}{}
					}
				}
				if len(call.Args) == 0 {
					return true
				}
				literal, literalOK := call.Args[0].(*ast.BasicLit)
				if !literalOK || literal.Kind != token.STRING {
					return true
				}
				isEnvironmentLookup := false
				switch function := call.Fun.(type) {
				case *ast.SelectorExpr:
					pkg, ok := function.X.(*ast.Ident)
					isEnvironmentLookup = ok && pkg.Name == "os" &&
						(function.Sel.Name == "Getenv" || function.Sel.Name == "LookupEnv")
				case *ast.Ident:
					isEnvironmentLookup = function.Name == "getEnv"
				}
				if !isEnvironmentLookup {
					return true
				}
				name, err := strconv.Unquote(literal.Value)
				if err == nil && (strings.HasPrefix(name, "WORMHOLE_") || strings.HasPrefix(name, "XDG_")) {
					names[name] = struct{}{}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk production sources: %v", err)
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func taggedFieldNames(t *testing.T, value any, tagName string) []string {
	t.Helper()
	valueType := reflect.TypeOf(value)
	fields := make([]string, 0, valueType.NumField())
	for i := 0; i < valueType.NumField(); i++ {
		name := strings.Split(valueType.Field(i).Tag.Get(tagName), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("%s field %s has no %s tag", valueType, valueType.Field(i).Name, tagName)
		}
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}
