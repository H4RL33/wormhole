package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
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

var (
	contractGitExecutable   = lookupContractExecutable("git")
	contractShellExecutable = lookupContractExecutable("sh")
)

type alphaCLIContract struct {
	Mode        string                 `json:"mode"`
	CLI         alphaCLI               `json:"cli"`
	Environment []string               `json:"environment"`
	Paths       map[string]string      `json:"paths"`
	Migrations  alphaMigrationContract `json:"migrations"`
	Artifacts   alphaArtifacts         `json:"artifacts"`
}

type alphaCLI struct {
	Commands                []alphaCommand          `json:"commands"`
	ConfigFiles             alphaConfigFiles        `json:"config_files"`
	ConfigurationPrecedence []alphaConfigPrecedence `json:"configuration_precedence"`
	ExitBehavior            map[string]int          `json:"exit_behavior"`
}

type alphaConfigPrecedence struct {
	Name     string   `json:"name"`
	Commands []string `json:"commands"`
	Sources  []string `json:"sources"`
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

type alphaArtifacts struct {
	Archives             []string `json:"archives"`
	Binaries             []string `json:"binaries"`
	ChecksumManifest     string   `json:"checksum_manifest"`
	FabricImage          string   `json:"fabric_image"`
	FabricImagePlatforms []string `json:"fabric_image_platforms"`
	PublishedFiles       []string `json:"published_files"`
	ReleasePlatforms     []string `json:"release_platforms"`
	SigningSuffixes      []string `json:"signing_suffixes"`
	SPDXSBOMs            []string `json:"spdx_sboms"`
}

type alphaMigrationContract struct {
	Baseline          alphaMigrationBaseline `json:"baseline"`
	CurrentVersion    int                    `json:"current_version"`
	EmptyVersion      int                    `json:"empty_version"`
	Entries           []alphaMigrationEntry  `json:"entries"`
	VerificationPaths []alphaMigrationPath   `json:"verification_paths"`
}

type alphaMigrationBaseline struct {
	Tag     string `json:"tag"`
	Version int    `json:"version"`
}

type alphaMigrationEntry struct {
	DownEmpty bool   `json:"down_empty"`
	DownFile  string `json:"down_file"`
	Name      string `json:"name"`
	UpEmpty   bool   `json:"up_empty"`
	UpFile    string `json:"up_file"`
	Version   int    `json:"version"`
}

type alphaMigrationPath struct {
	Command     string `json:"command"`
	FromTag     string `json:"from_tag,omitempty"`
	FromVersion int    `json:"from_version"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	ToVersion   int    `json:"to_version"`
}

func TestAlphaContractCLIConfigurationPrecedence(t *testing.T) {
	manifest := readAlphaCLIContract(t)
	ownerSources, repositorySources := exerciseGitResolvers(t)
	actual := []alphaConfigPrecedence{
		{
			Name:     "config.resolve.required",
			Commands: []string{},
			Sources:  exerciseGenericResolver(t),
		},
		{
			Name:     "connect.opencode_config",
			Commands: []string{"connect"},
			Sources:  exerciseOpenCodeConfigResolver(t),
		},
		{
			Name:     "join_connect.credential_path",
			Commands: []string{"connect", "join"},
			Sources:  exerciseCredentialPathResolver(t),
		},
		{
			Name:     "join_connect.model",
			Commands: []string{"connect", "join"},
			Sources:  exerciseModelResolver(t),
		},
		{
			Name:     "join_connect.owner",
			Commands: []string{"connect", "join"},
			Sources:  ownerSources,
		},
		{
			Name:     "join_connect.project",
			Commands: []string{"connect", "join"},
			Sources:  exerciseProjectResolver(t),
		},
		{
			Name:     "join_connect.repositories",
			Commands: []string{"connect", "join"},
			Sources:  repositorySources,
		},
		{
			Name:     "join_connect.server",
			Commands: []string{"connect", "join"},
			Sources:  exerciseServerResolver(t),
		},
		{
			Name:     "viewer-key.create.admin_key",
			Commands: []string{"viewer-key create"},
			Sources:  exerciseAdminKeyResolver(t),
		},
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	if !reflect.DeepEqual(actual, manifest.CLI.ConfigurationPrecedence) {
		t.Fatalf("configuration precedence = %#v, manifest = %#v", actual, manifest.CLI.ConfigurationPrecedence)
	}
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

func TestAlphaContractEnvironmentAndPaths(t *testing.T) {
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

}

func TestAlphaContractMigrationsAndArtifacts(t *testing.T) {
	manifest := readAlphaCLIContract(t)
	entries, currentVersion := productionMigrationEntries(t)
	baseline := migrationUpgradeContract(t, currentVersion)
	actualMigrations := alphaMigrationContract{
		Baseline:       baseline,
		CurrentVersion: currentVersion,
		EmptyVersion:   0,
		Entries:        entries,
		VerificationPaths: []alphaMigrationPath{
			{
				Command:     "up",
				FromTag:     baseline.Tag,
				FromVersion: baseline.Version,
				Name:        "alpha_upgrade",
				Source:      ".github/scripts/test-alpha-upgrade.sh",
				ToVersion:   currentVersion,
			},
			{
				Command:     "down -all",
				FromVersion: currentVersion,
				Name:        "current_down",
				Source:      ".github/workflows/migrations.yml",
				ToVersion:   0,
			},
			{
				Command:     "up",
				FromVersion: 0,
				Name:        "empty_up",
				Source:      ".github/workflows/migrations.yml",
				ToVersion:   currentVersion,
			},
		},
	}
	assertMigrationVerificationSources(t, actualMigrations)
	if !reflect.DeepEqual(actualMigrations, manifest.Migrations) {
		t.Fatalf("migrations = %#v, manifest = %#v", actualMigrations, manifest.Migrations)
	}

	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	binaryMatch := regexp.MustCompile(`(?m)^BINARIES := (.+)$`).FindSubmatch(makefile)
	if binaryMatch == nil {
		t.Fatal("Makefile has no BINARIES declaration")
	}
	makefileBinaries := strings.Fields(string(binaryMatch[1]))
	sort.Strings(makefileBinaries)
	actual := productionReleaseArtifacts(t)
	if !reflect.DeepEqual(actual.Binaries, makefileBinaries) {
		t.Fatalf("release binaries = %v, Makefile binaries = %v", actual.Binaries, makefileBinaries)
	}
	releaseWorkflow, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	publishScript, err := os.ReadFile("../../.github/scripts/publish-github-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(releaseWorkflow, []byte("IMAGE: "+actual.FabricImage)) {
		t.Fatalf("release workflow does not publish %s", actual.FabricImage)
	}
	architectures := make([]string, 0, len(actual.FabricImagePlatforms))
	for _, platform := range actual.FabricImagePlatforms {
		parts := strings.Split(platform, "/")
		if len(parts) != 2 || parts[0] != "linux" {
			t.Fatalf("unsupported release platform %q", platform)
		}
		architectures = append(architectures, parts[1])
	}
	if !bytes.Contains(releaseWorkflow, []byte("arch: ["+strings.Join(architectures, ", ")+"]")) {
		t.Fatalf("release workflow image matrix does not match %v", actual.FabricImagePlatforms)
	}
	if !bytes.Contains(releaseWorkflow, []byte("for arch in "+strings.Join(architectures, " ")+"; do")) {
		t.Fatalf("release workflow publish loop does not match %v", actual.FabricImagePlatforms)
	}
	for _, suffix := range actual.SigningSuffixes {
		if !bytes.Contains(releaseWorkflow, []byte(`"${artifact}`+suffix+`"`)) {
			t.Fatalf("release workflow does not create %s sidecars", suffix)
		}
	}
	if !bytes.Contains(releaseWorkflow, []byte("for artifact in dist/release/*; do")) {
		t.Fatal("release workflow does not sign every produced artifact")
	}
	if bytes.Count(publishScript, []byte(`gh release create "$tag" "$release_dir"/*`)) != 2 {
		t.Fatal("GitHub publisher does not upload every release file in both release modes")
	}
	if !reflect.DeepEqual(actual, manifest.Artifacts) {
		t.Fatalf("artifacts = %#v, manifest = %#v", actual, manifest.Artifacts)
	}
}

func productionMigrationEntries(t *testing.T) ([]alphaMigrationEntry, int) {
	t.Helper()
	upFiles, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	pattern := regexp.MustCompile(`^([0-9]{6})_(.+)\.up\.sql$`)
	entries := make([]alphaMigrationEntry, 0, len(upFiles))
	for _, up := range upFiles {
		match := pattern.FindStringSubmatch(filepath.Base(up))
		if match == nil {
			t.Fatalf("migration has invalid name: %s", up)
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("migration version %q: %v", match[1], err)
		}
		down := strings.TrimSuffix(up, ".up.sql") + ".down.sql"
		upBody, err := os.ReadFile(up)
		if err != nil {
			t.Fatalf("read migration %s: %v", up, err)
		}
		downBody, err := os.ReadFile(down)
		if err != nil {
			t.Fatalf("migration %s has no down pair: %v", up, err)
		}
		entries = append(entries, alphaMigrationEntry{
			DownEmpty: len(bytes.TrimSpace(downBody)) == 0,
			DownFile:  "migrations/" + filepath.Base(down),
			Name:      match[2],
			UpEmpty:   len(bytes.TrimSpace(upBody)) == 0,
			UpFile:    "migrations/" + filepath.Base(up),
			Version:   version,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Version < entries[j].Version })
	for index, entry := range entries {
		want := index + 1
		if entry.Version != want {
			t.Fatalf("migration sequence has version %d at index %d, want %d", entry.Version, index, want)
		}
	}
	if len(entries) == 0 {
		t.Fatal("no migrations found")
	}
	return entries, entries[len(entries)-1].Version
}

func migrationUpgradeContract(t *testing.T, currentVersion int) alphaMigrationBaseline {
	t.Helper()
	output, err := exec.Command(
		requireContractExecutable(t, contractShellExecutable, "sh"),
		"../../.github/scripts/test-alpha-upgrade.sh",
		"--print-contract",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("print alpha upgrade contract: %v: %s", err, output)
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			t.Fatalf("invalid alpha upgrade contract line %q", line)
		}
		values[fields[0]] = fields[1]
	}
	version, err := strconv.Atoi(values["baseline_version"])
	if err != nil {
		t.Fatalf("baseline version %q: %v", values["baseline_version"], err)
	}
	scriptCurrentVersion, err := strconv.Atoi(values["current_version"])
	if err != nil {
		t.Fatalf("current version %q: %v", values["current_version"], err)
	}
	if scriptCurrentVersion != currentVersion {
		t.Fatalf("upgrade script current version = %d, migration files = %d", scriptCurrentVersion, currentVersion)
	}
	return alphaMigrationBaseline{Tag: values["baseline_tag"], Version: version}
}

func assertMigrationVerificationSources(t *testing.T, contract alphaMigrationContract) {
	t.Helper()
	upgradeScript, err := os.ReadFile("../../.github/scripts/test-alpha-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile("../../.github/workflows/migrations.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(upgradeScript, []byte(`git archive "$baseline_tag" migrations`)) ||
		!bytes.Contains(upgradeScript, []byte(`migrate -path "$baseline_dir/migrations" -database "$database_url" up`)) ||
		!bytes.Contains(upgradeScript, []byte(`migrate -path migrations -database "$database_url" up`)) {
		t.Fatal("alpha upgrade script does not exercise baseline-to-current up migrations")
	}
	if !bytes.Contains(upgradeScript, []byte(`"$baseline_version:f"`)) ||
		!bytes.Contains(upgradeScript, []byte(`"$current_version:f"`)) {
		t.Fatal("alpha upgrade script does not verify clean baseline and current versions")
	}
	if !bytes.Contains(workflow, []byte(`migrate -path migrations -database "$WORMHOLE_DATABASE_URL" up`)) ||
		!bytes.Contains(workflow, []byte(`migrate -path migrations -database "$WORMHOLE_DATABASE_URL" down -all`)) ||
		!bytes.Contains(workflow, []byte(`run: .github/scripts/test-alpha-upgrade.sh`)) {
		t.Fatal("migration workflow does not exercise empty up/down and alpha upgrade paths")
	}
	if contract.Baseline.Version >= contract.CurrentVersion {
		t.Fatalf("baseline version %d must precede current version %d", contract.Baseline.Version, contract.CurrentVersion)
	}
}

func productionReleaseArtifacts(t *testing.T) alphaArtifacts {
	t.Helper()
	const contractVersion = "9.8.7-contract"
	output, err := exec.Command(
		requireContractExecutable(t, contractShellExecutable, "sh"),
		"../../.github/scripts/build-release.sh",
		"--print-contract",
		contractVersion,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("print release contract: %v: %s", err, output)
	}
	actual := alphaArtifacts{
		ChecksumManifest: "SHA256SUMS",
		FabricImage:      "ghcr.io/h4rl33/wormhole-fabric",
		SigningSuffixes:  []string{".pem", ".sig"},
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			t.Fatalf("invalid release contract line %q", line)
		}
		value := strings.ReplaceAll(fields[1], contractVersion, "{version}")
		switch fields[0] {
		case "archive":
			actual.Archives = append(actual.Archives, value)
		case "binary":
			actual.Binaries = append(actual.Binaries, value)
		case "checksum":
			if value != actual.ChecksumManifest {
				t.Fatalf("checksum manifest = %q, want %q", value, actual.ChecksumManifest)
			}
		case "platform":
			actual.ReleasePlatforms = append(actual.ReleasePlatforms, value)
		case "spdx_sbom":
			actual.SPDXSBOMs = append(actual.SPDXSBOMs, value)
		default:
			t.Fatalf("unknown release contract kind %q", fields[0])
		}
	}
	actual.FabricImagePlatforms = append(actual.FabricImagePlatforms, actual.ReleasePlatforms...)
	baseFiles := append([]string{actual.ChecksumManifest}, actual.Archives...)
	baseFiles = append(baseFiles, actual.SPDXSBOMs...)
	for _, file := range baseFiles {
		actual.PublishedFiles = append(actual.PublishedFiles, file)
		for _, suffix := range actual.SigningSuffixes {
			actual.PublishedFiles = append(actual.PublishedFiles, file+suffix)
		}
	}
	sort.Strings(actual.Archives)
	sort.Strings(actual.Binaries)
	sort.Strings(actual.FabricImagePlatforms)
	sort.Strings(actual.PublishedFiles)
	sort.Strings(actual.ReleasePlatforms)
	sort.Strings(actual.SPDXSBOMs)
	return actual
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

func exerciseGenericResolver(t *testing.T) []string {
	t.Helper()
	const envKey = "WORMHOLE_CONTRACT_RESOLVE"
	t.Setenv(envKey, "environment")
	tests := []struct {
		source   string
		input    config.ResolveInput
		want     string
		wantErr  bool
		required bool
	}{
		{"flag", config.ResolveInput{Flag: "flag", Local: "project", Global: "global", EnvKey: envKey, Default: "default"}, "flag", false, true},
		{"project_config", config.ResolveInput{Local: "project", Global: "global", EnvKey: envKey, Default: "default"}, "project", false, true},
		{"global_config", config.ResolveInput{Global: "global", EnvKey: envKey, Default: "default"}, "global", false, true},
		{"environment", config.ResolveInput{EnvKey: envKey, Default: "default"}, "environment", false, true},
		{"default", config.ResolveInput{Default: "default"}, "default", false, true},
		{"error", config.ResolveInput{}, "", true, true},
	}
	sources := make([]string, 0, len(tests))
	for _, test := range tests {
		got, err := config.Resolve(test.input, test.required)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("config.Resolve %s = %q, %v; want %q, error=%v", test.source, got, err, test.want, test.wantErr)
		}
		sources = append(sources, test.source)
	}
	return sources
}

func exerciseServerResolver(t *testing.T) []string {
	t.Helper()
	project := config.Config{Server: "project"}
	global := config.Config{Server: "global"}
	tests := []struct {
		source  string
		flag    string
		project config.Config
		global  config.Config
		want    string
		wantErr bool
	}{
		{"flag", "flag", project, global, "flag", false},
		{"project_config", "", project, global, "project", false},
		{"global_config", "", config.Config{}, global, "global", false},
		{"error", "", config.Config{}, config.Config{}, "", true},
	}
	sources := make([]string, 0, len(tests))
	for _, test := range tests {
		got, err := config.ResolveServer(test.flag, test.project, test.global)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("ResolveServer %s = %q, %v; want %q, error=%v", test.source, got, err, test.want, test.wantErr)
		}
		sources = append(sources, test.source)
	}
	return sources
}

func exerciseProjectResolver(t *testing.T) []string {
	t.Helper()
	project := config.Config{Project: "project"}
	tests := []struct {
		source  string
		flag    string
		project config.Config
		want    string
		wantErr bool
	}{
		{"flag", "flag", project, "flag", false},
		{"project_config", "", project, "project", false},
		{"error", "", config.Config{}, "", true},
	}
	sources := make([]string, 0, len(tests))
	for _, test := range tests {
		got, err := config.ResolveProject(test.flag, test.project)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("ResolveProject %s = %q, %v; want %q, error=%v", test.source, got, err, test.want, test.wantErr)
		}
		sources = append(sources, test.source)
	}
	return sources
}

func exerciseGitResolvers(t *testing.T) ([]string, []string) {
	t.Helper()
	gitExecutable := requireContractExecutable(t, contractGitExecutable, "git")
	originalPath := os.Getenv("PATH")
	usablePath := filepath.Dir(gitExecutable)
	if originalPath != "" {
		usablePath += string(os.PathListSeparator) + originalPath
	}
	t.Setenv("PATH", usablePath)

	repo := t.TempDir()
	runContractGit(t, repo, "init")
	runContractGit(t, repo, "config", "user.name", "Contract Owner")
	runContractGit(t, repo, "remote", "add", "origin", "https://example.invalid/contract.git")

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "global.gitconfig"))

	if got, err := config.ResolveOwner("flag", config.Config{Role: "ignored"}, config.Config{Role: "ignored"}); err != nil || got != "flag" {
		t.Fatalf("ResolveOwner flag = %q, %v", got, err)
	}
	if got, err := config.ResolveOwner("", config.Config{Role: "ignored"}, config.Config{Role: "ignored"}); err != nil || got != "Contract Owner" {
		t.Fatalf("ResolveOwner git = %q, %v", got, err)
	}
	if got, err := config.ResolveRepositories("flag"); err != nil || got != "flag" {
		t.Fatalf("ResolveRepositories flag = %q, %v", got, err)
	}
	if got, err := config.ResolveRepositories(""); err != nil || got != "https://example.invalid/contract.git" {
		t.Fatalf("ResolveRepositories git = %q, %v", got, err)
	}

	t.Setenv("PATH", t.TempDir())
	if got, err := config.ResolveOwner("", config.Config{}, config.Config{}); err != nil || got == "" {
		t.Fatalf("ResolveOwner OS user fallback = %q, %v", got, err)
	}
	if got, err := config.ResolveRepositories(""); err != nil || got != "" {
		t.Fatalf("ResolveRepositories empty fallback = %q, %v", got, err)
	}
	t.Setenv("PATH", usablePath)

	return []string{"flag", "git_user_name", "os_user", "error"},
		[]string{"flag", "git_origin", "empty_default"}
}

func exerciseModelResolver(t *testing.T) []string {
	t.Helper()
	t.Setenv("WORMHOLE_MODEL", "environment")
	if got := resolveModel("flag"); got != "flag" {
		t.Fatalf("resolveModel flag = %q", got)
	}
	if got := resolveModel(""); got != "environment" {
		t.Fatalf("resolveModel environment = %q", got)
	}
	t.Setenv("WORMHOLE_MODEL", "")
	if got := resolveModel(""); got != "" {
		t.Fatalf("resolveModel empty default = %q", got)
	}
	return []string{"flag", "WORMHOLE_MODEL", "empty_default"}
}

func exerciseCredentialPathResolver(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := resolveCredentialsPath("/contract/token.json", "profile", "project", "role"); err != nil || got != "/contract/token.json" {
		t.Fatalf("credential token-file = %q, %v", got, err)
	}
	wantProfile := filepath.Join(home, ".wormhole", "credentials", "profile.json")
	if got, err := resolveCredentialsPath("", "profile", "project", "role"); err != nil || got != wantProfile {
		t.Fatalf("credential profile = %q, %v; want %q", got, err, wantProfile)
	}
	wantDefault := filepath.Join(home, ".wormhole", "credentials", "project__role.json")
	if got, err := resolveCredentialsPath("", "", "project", "role"); err != nil || got != wantDefault {
		t.Fatalf("credential default = %q, %v; want %q", got, err, wantDefault)
	}
	t.Setenv("HOME", "")
	if _, err := resolveCredentialsPath("", "", "project", "role"); err == nil {
		t.Fatal("credential path without HOME returned no error")
	}
	return []string{"token_file_flag", "profile_flag", "derived_default", "error"}
}

func exerciseOpenCodeConfigResolver(t *testing.T) []string {
	t.Helper()
	if got, err := resolveOpenCodeConfigPath("/contract/opencode.json", t.TempDir()); err != nil || got != "/contract/opencode.json" {
		t.Fatalf("OpenCode explicit path = %q, %v", got, err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(root, "opencode.jsonc")
	if err := os.WriteFile(projectFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveOpenCodeConfigPath("", nested); err != nil || got != projectFile {
		t.Fatalf("OpenCode project path = %q, %v; want %q", got, err, projectFile)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := resolveOpenCodeConfigPath("", t.TempDir()); err != nil || got != filepath.Join(home, ".config", "opencode", "opencode.json") {
		t.Fatalf("OpenCode global default = %q, %v", got, err)
	}
	t.Setenv("HOME", "")
	if _, err := resolveOpenCodeConfigPath("", t.TempDir()); err == nil {
		t.Fatal("OpenCode config without HOME returned no error")
	}
	return []string{"flag", "project_file", "global_default", "error"}
}

func exerciseAdminKeyResolver(t *testing.T) []string {
	t.Helper()
	t.Setenv("WORMHOLE_ADMIN_KEY", "environment")
	if got := resolveAdminKey("flag"); got != "flag" {
		t.Fatalf("resolveAdminKey flag = %q", got)
	}
	if got := resolveAdminKey(""); got != "environment" {
		t.Fatalf("resolveAdminKey environment = %q", got)
	}
	t.Setenv("WORMHOLE_ADMIN_KEY", "")
	if got := resolveAdminKey(""); got != "" {
		t.Fatalf("resolveAdminKey error sentinel = %q, want empty", got)
	}
	return []string{"flag", "WORMHOLE_ADMIN_KEY", "error"}
}

func runContractGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.Command(
		requireContractExecutable(t, contractGitExecutable, "git"),
		append([]string{"-C", directory}, args...)...,
	)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(directory, "global.gitconfig"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func lookupContractExecutable(name string) string {
	path, _ := exec.LookPath(name)
	return path
}

func requireContractExecutable(t *testing.T, path, name string) string {
	t.Helper()
	if path == "" {
		t.Fatalf("%s executable was not available when the contract tests started", name)
	}
	return path
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
