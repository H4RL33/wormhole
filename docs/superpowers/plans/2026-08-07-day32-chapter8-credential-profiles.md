# Chapter 8 — Multi-Passport Credential Profiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `cmd/wormhole-cli`'s single fixed `~/.wormhole/credentials.json` (overwritten on every `wormhole join`) with a keyed credential-profile store, so one machine can hold multiple passports (one per project+role) at once — the hard blocker M4's three-session test (manager/backend/frontend, Chapter 12) needs and that ROADMAP-ALPHA2.md flags explicitly for Chapter 8.

**Architecture:** New file `cmd/wormhole-cli/profiles.go` owns the keyed-store primitives (path derivation, sanitization, listing, resolution) as pure functions with no network I/O, kept separate from `main.go`'s existing command/HTTP-wiring responsibility. `main.go` is modified to call these primitives from `runJoin`/`runConnect` instead of the old fixed-path `defaultTokenFilePath` (deleted), and gains two new subcommands, `whoami` and `profile list`, that read local credential files only — no server calls.

**Tech Stack:** Go stdlib only (`os`, `path/filepath`, `encoding/json`, `sort`, `strings`, `time`), matching the rest of `cmd/wormhole-cli`'s zero-dependency convention. `net/http/httptest` for existing join/connect test patterns (unaffected — no protocol change).

## Global Constraints

- No new third-party dependencies (project-wide convention, `go.mod` has zero non-stdlib deps for `cmd/wormhole-cli`).
- `cmd/wormhole-cli` may not import `internal/mcp` or `internal/core/*` (`docs/architecture.md` §2 module boundary) — all wire-shape/constant mirroring stays local, same pattern as the existing `rpcRequest`/`registerAgentInput` mirrors in `main.go`.
- Every existing test in `cmd/wormhole-cli/main_test.go` must keep passing unless this plan explicitly says to delete/replace it.
- File mode `0600` for credential files, `0700` for their parent directory (existing `writeCredentials` convention — do not weaken).
- No silent default when >1 credential profile exists and no `--profile` given (roadmap Chapter 8, bullet 4) — must be a hard error, not a guess.
- Single-profile case must keep working with zero flags (roadmap Chapter 8, bullet 4) — backward compatible with the pre-Chapter-8 one-passport flow.

---

## Task 1: Credential-profile store primitives (`profiles.go`)

**Files:**
- Create: `cmd/wormhole-cli/profiles.go`
- Create: `cmd/wormhole-cli/profiles_test.go`

**Interfaces:**
- Consumes: nothing from other tasks (pure new file, stdlib only). Reads the `credentials` struct defined in `cmd/wormhole-cli/main.go` (fields: `Server, ProjectID, AgentID, PassportID, Token string; IssuedAt time.Time`) — this task does NOT modify that struct (Task 2 adds `Role` to it; write `profiles.go` to compile against the struct as it exists today, `Role` gets added in Task 2 with no changes needed here since Go structs decode missing fields as zero values).
- Produces (for Task 2 and Task 3 to call):
  - `func profilesDir() (string, error)` — `~/.wormhole/credentials`
  - `func sanitizeComponent(s string) string`
  - `func validateProfileName(name string) error`
  - `func defaultProfileName(project, role string) string`
  - `func resolveCredentialsPath(tokenFile, profile, project, role string) (string, error)`
  - `type profileEntry struct { Name, Project, Role, AgentID string; IssuedAt, ExpiresAt time.Time }`
  - `func readCredentials(path string) (credentials, error)`
  - `func listCredentialProfiles(dir string) ([]profileEntry, error)`
  - `func resolveWhoamiProfile(dir string) (profileEntry, error)`
  - `const cliTokenTTL = 30 * 24 * time.Hour`

- [ ] **Step 1: Write the failing tests**

Create `cmd/wormhole-cli/profiles_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizeComponent_ReplacesUnsafeChars(t *testing.T) {
	got := sanitizeComponent("proj/../etc\\passwd id")
	if strings.ContainsAny(got, `/\`) {
		t.Fatalf("sanitizeComponent left a path separator: %q", got)
	}
	want := "proj___etc_passwd_id"
	if got != want {
		t.Fatalf("sanitizeComponent: got %q, want %q", got, want)
	}
}

func TestSanitizeComponent_KeepsSafeChars(t *testing.T) {
	got := sanitizeComponent("backend-engineer_v2.1")
	want := "backend-engineer_v2.1"
	if got != want {
		t.Fatalf("sanitizeComponent: got %q, want %q", got, want)
	}
}

func TestValidateProfileName_RejectsPathSeparators(t *testing.T) {
	for _, name := range []string{"a/b", `a\b`, "../etc", "..", "."} {
		if err := validateProfileName(name); err == nil {
			t.Fatalf("validateProfileName(%q): got nil error, want error", name)
		}
	}
}

func TestValidateProfileName_RejectsEmpty(t *testing.T) {
	if err := validateProfileName(""); err == nil {
		t.Fatal("validateProfileName(\"\"): got nil error, want error")
	}
}

func TestValidateProfileName_AcceptsSafeName(t *testing.T) {
	if err := validateProfileName("proj-1__backend-engineer"); err != nil {
		t.Fatalf("validateProfileName: got %v, want nil", err)
	}
}

func TestDefaultProfileName_CombinesProjectAndRole(t *testing.T) {
	got := defaultProfileName("proj-1", "backend-engineer")
	want := "proj-1__backend-engineer"
	if got != want {
		t.Fatalf("defaultProfileName: got %q, want %q", got, want)
	}
}

func TestDefaultProfileName_EmptyRoleDefaultsToDefault(t *testing.T) {
	got := defaultProfileName("proj-1", "")
	want := "proj-1__default"
	if got != want {
		t.Fatalf("defaultProfileName: got %q, want %q", got, want)
	}
}

func TestDefaultProfileName_DistinctRolesProduceDistinctNames(t *testing.T) {
	a := defaultProfileName("proj-1", "backend-engineer")
	b := defaultProfileName("proj-1", "frontend-engineer")
	if a == b {
		t.Fatalf("defaultProfileName collision: both roles produced %q", a)
	}
}

func TestResolveCredentialsPath_TokenFileWins(t *testing.T) {
	got, err := resolveCredentialsPath("/explicit/path.json", "myprofile", "proj-1", "backend-engineer")
	if err != nil {
		t.Fatalf("resolveCredentialsPath: %v", err)
	}
	if got != "/explicit/path.json" {
		t.Fatalf("resolveCredentialsPath: got %q, want %q", got, "/explicit/path.json")
	}
}

func TestResolveCredentialsPath_ExplicitProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := resolveCredentialsPath("", "myprofile", "proj-1", "backend-engineer")
	if err != nil {
		t.Fatalf("resolveCredentialsPath: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("credentials", "myprofile.json")) {
		t.Fatalf("resolveCredentialsPath: got %q, want suffix .../credentials/myprofile.json", got)
	}
}

func TestResolveCredentialsPath_ExplicitProfile_RejectsUnsafeName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := resolveCredentialsPath("", "../escape", "proj-1", "backend-engineer"); err == nil {
		t.Fatal("resolveCredentialsPath: got nil error for unsafe --profile name, want error")
	}
}

func TestResolveCredentialsPath_DefaultDerivedFromProjectAndRole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := resolveCredentialsPath("", "", "proj-1", "backend-engineer")
	if err != nil {
		t.Fatalf("resolveCredentialsPath: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("credentials", "proj-1__backend-engineer.json")) {
		t.Fatalf("resolveCredentialsPath: got %q, want suffix .../credentials/proj-1__backend-engineer.json", got)
	}
}

func TestListCredentialProfiles_MissingDirReturnsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		t.Fatalf("listCredentialProfiles: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("listCredentialProfiles: got %d entries, want 0", len(entries))
	}
}

func writeTestCredentials(t *testing.T, dir, name string, creds credentials) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestListCredentialProfiles_ReturnsAllSortedByName(t *testing.T) {
	dir := t.TempDir()
	issuedAt := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{
		ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt,
	})
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{
		ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt,
	})

	entries, err := listCredentialProfiles(dir)
	if err != nil {
		t.Fatalf("listCredentialProfiles: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("listCredentialProfiles: got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "proj-1__backend-engineer" || entries[1].Name != "proj-1__frontend-engineer" {
		t.Fatalf("listCredentialProfiles order: got %q, %q", entries[0].Name, entries[1].Name)
	}
	if entries[0].AgentID != "agent-1" || entries[0].Role != "backend-engineer" {
		t.Fatalf("listCredentialProfiles fields: got %+v", entries[0])
	}
	wantExpiry := issuedAt.Add(cliTokenTTL)
	if !entries[0].ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("listCredentialProfiles ExpiresAt: got %v, want %v", entries[0].ExpiresAt, wantExpiry)
	}
}

func TestListCredentialProfiles_IgnoresNonJSONFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		t.Fatalf("listCredentialProfiles: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("listCredentialProfiles: got %d entries, want 0", len(entries))
	}
}

func TestResolveWhoamiProfile_NoProfiles_Errors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "empty")
	if _, err := resolveWhoamiProfile(dir); err == nil {
		t.Fatal("resolveWhoamiProfile: got nil error for zero profiles, want error")
	}
}

func TestResolveWhoamiProfile_OneProfile_AutoSelects(t *testing.T) {
	dir := t.TempDir()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{
		ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: time.Now(),
	})
	entry, err := resolveWhoamiProfile(dir)
	if err != nil {
		t.Fatalf("resolveWhoamiProfile: %v", err)
	}
	if entry.Name != "proj-1__backend-engineer" {
		t.Fatalf("resolveWhoamiProfile: got %q, want %q", entry.Name, "proj-1__backend-engineer")
	}
}

func TestResolveWhoamiProfile_MultipleProfiles_Errors(t *testing.T) {
	dir := t.TempDir()
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	_, err := resolveWhoamiProfile(dir)
	if err == nil {
		t.Fatal("resolveWhoamiProfile: got nil error for two profiles, want error")
	}
	if !strings.Contains(err.Error(), "proj-1__backend-engineer") || !strings.Contains(err.Error(), "proj-1__frontend-engineer") {
		t.Fatalf("resolveWhoamiProfile error should name both profiles: got %q", err.Error())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (compile error — nothing defined yet)**

Run: `cd /home/harley/vault/projects/wormhole && go test ./cmd/wormhole-cli/... -run TestSanitizeComponent -v`
Expected: FAIL — `undefined: sanitizeComponent` (build failure, confirms nothing exists yet)

- [ ] **Step 3: Write `profiles.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cliTokenTTL mirrors identity.tokenTTL (internal/core/identity/identity.go)
// for local display purposes only (profile list / whoami "expires_at"
// column). cmd/wormhole-cli cannot import internal/core/identity
// (docs/architecture.md module boundary), and wormhole.agent.register does
// not return expires_at, so this is a best-effort local mirror, not an
// authoritative value.
const cliTokenTTL = 30 * 24 * time.Hour

// profilesDir is where keyed credential profiles live:
// ~/.wormhole/credentials/<name>.json (Chapter 8). Distinct from the
// legacy single ~/.wormhole/credentials.json path an explicit --token-file
// can still target directly.
func profilesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".wormhole", "credentials"), nil
}

// sanitizeComponent replaces any character outside [A-Za-z0-9._-] with "_",
// so a project ID or role name containing a path separator can't escape
// profilesDir when folded into a derived filename.
func sanitizeComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// validateProfileName rejects an explicit --profile value that could escape
// profilesDir (path separators, ".." traversal, empty string) rather than
// silently sanitizing it — a human picked this name to find the file again
// later, silently rewriting it would defeat that.
func validateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("profile name %q must not contain path separators", name)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("profile name %q must not contain %q", name, "..")
	}
	return nil
}

// defaultProfileName derives the keyed filename stem (without ".json") from
// project and role: "<project>__<role>". role defaults to "default" when
// empty (e.g. wormhole connect, or wormhole join without --role) so the
// filename pattern stays uniform.
func defaultProfileName(project, role string) string {
	if role == "" {
		role = "default"
	}
	return sanitizeComponent(project) + "__" + sanitizeComponent(role)
}

// resolveCredentialsPath picks where a join/connect run writes its
// credentials, in priority order: explicit --token-file (arbitrary path,
// unchanged pre-Chapter-8 behavior) > explicit --profile name (written into
// profilesDir, name validated not sanitized) > the project/role-derived
// default key. This replaces the pre-Chapter-8 behavior of always writing
// one fixed ~/.wormhole/credentials.json, which silently clobbered any
// prior credentials on every join.
func resolveCredentialsPath(tokenFile, profile, project, role string) (string, error) {
	if tokenFile != "" {
		return tokenFile, nil
	}
	dir, err := profilesDir()
	if err != nil {
		return "", err
	}
	if profile != "" {
		if err := validateProfileName(profile); err != nil {
			return "", fmt.Errorf("--profile: %w", err)
		}
		return filepath.Join(dir, profile+".json"), nil
	}
	return filepath.Join(dir, defaultProfileName(project, role)+".json"), nil
}

// profileEntry is one row of `wormhole profile list` / `wormhole whoami`
// output: a credentials file's identifying fields, plus the filename stem
// (Name) a human passes back via --profile.
type profileEntry struct {
	Name      string
	Project   string
	Role      string
	AgentID   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// readCredentials loads and decodes one credentials JSON file.
func readCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, err
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, fmt.Errorf("decode: %w", err)
	}
	return creds, nil
}

// listCredentialProfiles scans dir for "*.json" credential files and
// decodes each into a profileEntry. A missing dir (no profiles created yet)
// returns an empty slice, not an error. Entries are sorted by Name for
// deterministic output.
func listCredentialProfiles(dir string) ([]profileEntry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read profiles directory: %w", err)
	}
	entries := make([]profileEntry, 0, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		creds, err := readCredentials(path)
		if err != nil {
			return nil, fmt.Errorf("read profile %q: %w", f.Name(), err)
		}
		entries = append(entries, profileEntry{
			Name:      strings.TrimSuffix(f.Name(), ".json"),
			Project:   creds.ProjectID,
			Role:      creds.Role,
			AgentID:   creds.AgentID,
			IssuedAt:  creds.IssuedAt,
			ExpiresAt: creds.IssuedAt.Add(cliTokenTTL),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// resolveWhoamiProfile picks which credentials file `wormhole whoami` reads
// when --profile is omitted: the sole profile if exactly one exists
// (single-profile case stays flag-free, matching pre-Chapter-8 ergonomics),
// else an error listing every candidate name — never guesses among several.
func resolveWhoamiProfile(dir string) (profileEntry, error) {
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		return profileEntry{}, err
	}
	if len(entries) == 0 {
		return profileEntry{}, fmt.Errorf("no stored credential profiles found under %s (run 'wormhole join' or 'wormhole connect' first)", dir)
	}
	if len(entries) > 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		return profileEntry{}, fmt.Errorf("multiple credential profiles found, specify --profile: %s", strings.Join(names, ", "))
	}
	return entries[0], nil
}
```

Note: this references `credentials.Role`, a field that does not exist on the `credentials` struct yet (Task 2 adds it). Since this task's own tests construct `credentials{...}` literals with a `Role:` field, **Task 1 must also add the `Role` field to the `credentials` struct in `main.go`** as part of this step (one-line addition, does not touch any other `main.go` behavior):

In `cmd/wormhole-cli/main.go`, find:
```go
type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
}
```
Replace with:
```go
type credentials struct {
	Server     string    `json:"server"`
	ProjectID  string    `json:"project_id"`
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
	// Role is the resolved role template name (Chapter 6's --role), empty
	// when join/connect ran without one. Chapter 8: read back by
	// listCredentialProfiles/resolveWhoamiProfile for `wormhole whoami` /
	// `wormhole profile list` display.
	Role string `json:"role,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/harley/vault/projects/wormhole && go build ./... && go test ./cmd/wormhole-cli/... -v`
Expected: PASS for every new test in `profiles_test.go`; all pre-existing tests in `main_test.go` still PASS (the `Role` field addition is additive, zero-value-compatible with every existing test literal).

- [ ] **Step 5: Commit**

```bash
cd /home/harley/vault/projects/wormhole
git add cmd/wormhole-cli/profiles.go cmd/wormhole-cli/profiles_test.go cmd/wormhole-cli/main.go
git commit -m "feat(cli): keyed credential-profile store primitives (Chapter 8 task 1)"
```

---

## Task 2: Wire `join`/`connect` to the profile store, delete the fixed default path

**Files:**
- Modify: `cmd/wormhole-cli/main.go`
- Test: `cmd/wormhole-cli/main_test.go`

**Interfaces:**
- Consumes: `resolveCredentialsPath(tokenFile, profile, project, role string) (string, error)` and `credentials.Role` field, both from Task 1 (already committed, available as regular package-level symbols — no import needed, same package).
- Produces: `runJoin` and `runConnect` both gain a `--profile` flag; nothing downstream in this plan depends on new exported names from this task (Task 3 only depends on Task 1's `profiles.go` symbols).

- [ ] **Step 1: Write the failing tests**

Add to `cmd/wormhole-cli/main_test.go` (append at end of file):

```go
// TestRunJoin_DefaultProfile_DerivedFromProjectAndRole confirms Chapter 8:
// with neither --token-file nor --profile given, join writes into
// ~/.wormhole/credentials/<project>__<role>.json instead of the old fixed
// ~/.wormhole/credentials.json.
func TestRunJoin_DefaultProfile_DerivedFromProjectAndRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--role", "backend-engineer",
		"--owner", "harley",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	wantPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__backend-engineer.json")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	if creds.Role != "backend-engineer" || creds.ProjectID != "proj-1" {
		t.Fatalf("credentials: got %+v", creds)
	}
}

// TestRunJoin_TwoRoles_DoNotClobberEachOther confirms Chapter 8's core
// requirement: joining the same project with two different roles produces
// two separate credential files, neither overwriting the other.
func TestRunJoin_TwoRoles_DoNotClobberEachOther(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	for _, role := range []string{"backend-engineer", "frontend-engineer"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"join",
			"--server", srv.URL,
			"--project", "proj-1",
			"--role", role,
			"--owner", "harley",
		}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("join role=%s exit code: got %d, want 0, stderr: %q", role, code, stderr.String())
		}
	}

	backendPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__backend-engineer.json")
	frontendPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__frontend-engineer.json")
	if _, err := os.Stat(backendPath); err != nil {
		t.Fatalf("backend profile missing: %v", err)
	}
	if _, err := os.Stat(frontendPath); err != nil {
		t.Fatalf("frontend profile missing: %v", err)
	}
}

// TestRunJoin_ExplicitProfile_WritesNamedFile confirms --profile picks the
// filename directly, bypassing the project/role-derived default.
func TestRunJoin_ExplicitProfile_WritesNamedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := fakeServer(t, func(t *testing.T, in searchArticlesInput) (searchArticlesOutput, *callResponse) {
		return searchArticlesOutput{Articles: []articleSummary{}}, nil
	})
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", srv.URL,
		"--project", "proj-1",
		"--profile", "my-manager-session",
		"--owner", "harley",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	wantPath := filepath.Join(home, ".wormhole", "credentials", "my-manager-session.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat %s: %v", wantPath, err)
	}
}

// TestRunJoin_ExplicitProfile_RejectsUnsafeName confirms a --profile value
// that could escape the credentials directory is rejected, not sanitized.
func TestRunJoin_ExplicitProfile_RejectsUnsafeName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"join",
		"--server", "http://example.invalid",
		"--project", "proj-1",
		"--profile", "../escape",
		"--owner", "harley",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero, stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "profile") {
		t.Fatalf("stderr should mention the profile-name error: got %q", stderr.String())
	}
}

// TestRunConnect_DefaultProfile_DerivedFromProject confirms connect (no
// --role flag) derives its default profile key using the "default" role
// placeholder.
func TestRunConnect_DefaultProfile_DerivedFromProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeBin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		out := registerAgentOutput{AgentID: "agent-1", PassportID: "passport-1", Token: "sekrit-token", Repositories: []string{}, Roles: []string{}}
		outRaw, _ := json.Marshal(out)
		resultRaw, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"connect",
		"--server", srv.URL,
		"--project", "proj-1",
		"--owner", "harley",
		"--claude-bin", claudeBin,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}

	wantPath := filepath.Join(home, ".wormhole", "credentials", "proj-1__default.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat %s: %v", wantPath, err)
	}
}
```

Delete the now-obsolete fixed-path test (superseded by the tests above): remove `TestDefaultTokenFilePath_UnderWormholeDir` (lines ~363-372 of `cmd/wormhole-cli/main_test.go` as of Chapter 7) entirely.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/harley/vault/projects/wormhole && go test ./cmd/wormhole-cli/... -run 'TestRunJoin_DefaultProfile|TestRunJoin_TwoRoles|TestRunJoin_ExplicitProfile|TestRunConnect_DefaultProfile' -v`
Expected: FAIL — `flag provided but not defined: -profile`, and default path still resolves to the old fixed `credentials.json`.

- [ ] **Step 3: Modify `main.go`**

Delete the `defaultTokenFilePath` function entirely (superseded by `profilesDir`/`resolveCredentialsPath` from Task 1):
```go
// defaultTokenFilePath is where credentials land when --token-file isn't
// given: ~/.wormhole/credentials.json.
func defaultTokenFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".wormhole", "credentials.json"), nil
}
```

In `runJoin`, add the `--profile` flag next to the existing `--token-file` flag:
```go
	tokenFile := fs.String("token-file", "", "path to write issued credentials to (overrides --profile and the derived default)")
	profile := fs.String("profile", "", "profile name to store credentials under (default: derived from --project and --role, e.g. proj-1__backend-engineer)")
```

Replace the join path-resolution block:
```go
	path := *tokenFile
	if path == "" {
		defaultPath, err := defaultTokenFilePath()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole join: %v\n", err)
			return 1
		}
		path = defaultPath
	}
```
with:
```go
	path, err := resolveCredentialsPath(*tokenFile, *profile, *project, *role)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole join: %v\n", err)
		return 1
	}
```
(Note: this introduces a second `err` binding in `runJoin` — the existing `out, err := doRegister(...)` a few lines above already declared `err` in this scope, so this becomes `path, err = resolveCredentialsPath(...)` with `=` not `:=`, since `path` is new but `err` already exists — use `path, err := resolveCredentialsPath(...)` only if `path` is the first new variable in that statement, which it is, so `:=` is correct Go (at least one new variable on the left side permits `:=` even when `err` is reused).)

In `runJoin`, add `Role: *role,` to the `credentials{...}` literal:
```go
	creds := credentials{
		Server:     *server,
		ProjectID:  *project,
		AgentID:    out.AgentID,
		PassportID: out.PassportID,
		Token:      out.Token,
		IssuedAt:   out.IssuedAt,
		Role:       *role,
	}
```

In `runConnect`, add the same `--profile` flag:
```go
	tokenFile := fs.String("token-file", "", "path to write issued credentials to (overrides --profile and the derived default)")
	profile := fs.String("profile", "", "profile name to store credentials under (default: derived from --project, e.g. proj-1__default)")
```

Replace `runConnect`'s path-resolution block (identical shape to join's, but `role` is `""` since connect has no `--role` flag):
```go
	path := *tokenFile
	if path == "" {
		defaultPath, err := defaultTokenFilePath()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
			return 1
		}
		path = defaultPath
	}
```
with:
```go
	path, err := resolveCredentialsPath(*tokenFile, *profile, *project, "")
	if err != nil {
		fmt.Fprintf(stderr, "wormhole connect: %v\n", err)
		return 1
	}
```
(Same `:=` note as above — `path` is new, `err` is reused, valid Go.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/harley/vault/projects/wormhole && go build ./... && go vet ./... && go test ./cmd/wormhole-cli/... -v`
Expected: PASS for all new tests and all pre-existing `main_test.go` tests (every existing test passes `--token-file` explicitly, so `resolveCredentialsPath`'s first branch — `tokenFile != ""` — makes this a no-behavior-change path for them).

- [ ] **Step 5: Commit**

```bash
cd /home/harley/vault/projects/wormhole
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "feat(cli): join/connect write keyed credential profiles, not one fixed file (Chapter 8 task 2)"
```

---

## Task 3: `wormhole whoami` and `wormhole profile list` subcommands

**Files:**
- Modify: `cmd/wormhole-cli/main.go`
- Test: `cmd/wormhole-cli/main_test.go`

**Interfaces:**
- Consumes: `profilesDir()`, `profileEntry`, `readCredentials()`, `listCredentialProfiles()`, `resolveWhoamiProfile()`, `validateProfileName()`, `cliTokenTTL` — all from Task 1's `profiles.go` (already committed).
- Produces: `runWhoami(args []string, stdout, stderr io.Writer) int`, `runProfile(args []string, stdout, stderr io.Writer) int`, both dispatched from `run()`. Nothing later in this plan depends on these.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/wormhole-cli/main_test.go`:

```go
func TestRun_WhoamiCommand_NoProfiles_PrintsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "no stored credential profiles") {
		t.Fatalf("stderr: got %q", stderr.String())
	}
}

func TestRun_WhoamiCommand_SingleProfile_AutoSelects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{
		ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1",
		IssuedAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"proj-1__backend-engineer", "proj-1", "backend-engineer", "agent-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRun_WhoamiCommand_MultipleProfiles_RequiresFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero (ambiguous profile)")
	}
	if !strings.Contains(stderr.String(), "--profile") {
		t.Fatalf("stderr should prompt for --profile: got %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"whoami", "--profile", "proj-1__frontend-engineer"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "agent-2") {
		t.Fatalf("stdout missing agent-2: got %q", stdout.String())
	}
}

func TestRun_WhoamiCommand_UnknownProfile_PrintsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami", "--profile", "does-not-exist"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
}

func TestRun_ProfileListCommand_Empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no stored credential profiles") {
		t.Fatalf("stdout: got %q", stdout.String())
	}
}

func TestRun_ProfileListCommand_ListsAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".wormhole", "credentials")
	issuedAt := time.Now()
	writeTestCredentials(t, dir, "proj-1__backend-engineer", credentials{ProjectID: "proj-1", Role: "backend-engineer", AgentID: "agent-1", IssuedAt: issuedAt})
	writeTestCredentials(t, dir, "proj-1__frontend-engineer", credentials{ProjectID: "proj-1", Role: "frontend-engineer", AgentID: "agent-2", IssuedAt: issuedAt})

	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0, stderr: %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"proj-1__backend-engineer", "proj-1__frontend-engineer", "agent-1", "agent-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: got %q", want, out)
		}
	}
}

func TestRun_ProfileCommand_UnknownSubcommand_PrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"profile", "bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/harley/vault/projects/wormhole && go test ./cmd/wormhole-cli/... -run 'TestRun_Whoami|TestRun_Profile' -v`
Expected: FAIL — `wormhole: unknown command "whoami"` / `"profile"` (exit code 2 from the default case in `run()`, not matching each test's expected assertions).

- [ ] **Step 3: Modify `main.go`**

In `run()`, add two new cases:
```go
	switch args[0] {
	case "join":
		return runJoin(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	case "whoami":
		return runWhoami(args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "wormhole: unknown command %q\n\n%s\n", args[0], usage())
		return 2
	}
```

Update `usage()`:
```go
func usage() string {
	return "usage: wormhole <command> [flags]\n\ncommands:\n  join          join a Wormhole project (RFC-0001 §8.5)\n  connect       join a project and register it as a Claude Code MCP connector\n  whoami        show the active (or a named) credential profile\n  profile list  list all stored credential profiles"
}
```

Add both new functions after `runConnect`:
```go
// runWhoami implements `wormhole whoami`: prints one stored credential
// profile's identifying fields (project, role, agent ID, issued/expiry
// times), reading local files only — no server call. With --profile it
// reads that named profile; without it, it auto-selects the sole stored
// profile, or errors if zero or more than one exist (Chapter 8 roadmap:
// "no silent default when more than one profile exists").
func runWhoami(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name to inspect (default: the sole stored profile, if only one exists)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := profilesDir()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole whoami: %v\n", err)
		return 1
	}

	var entry profileEntry
	if *profile != "" {
		if verr := validateProfileName(*profile); verr != nil {
			fmt.Fprintf(stderr, "wormhole whoami: --profile: %v\n", verr)
			return 2
		}
		creds, rerr := readCredentials(filepath.Join(dir, *profile+".json"))
		if rerr != nil {
			fmt.Fprintf(stderr, "wormhole whoami: profile %q: %v\n", *profile, rerr)
			return 1
		}
		entry = profileEntry{
			Name:      *profile,
			Project:   creds.ProjectID,
			Role:      creds.Role,
			AgentID:   creds.AgentID,
			IssuedAt:  creds.IssuedAt,
			ExpiresAt: creds.IssuedAt.Add(cliTokenTTL),
		}
	} else {
		resolved, rerr := resolveWhoamiProfile(dir)
		if rerr != nil {
			fmt.Fprintf(stderr, "wormhole whoami: %v\n", rerr)
			return 1
		}
		entry = resolved
	}

	role := entry.Role
	if role == "" {
		role = "(none)"
	}
	fmt.Fprintf(stdout, "profile=%s project=%s role=%s agent_id=%s issued_at=%s expires_at=%s\n",
		entry.Name, entry.Project, role, entry.AgentID,
		entry.IssuedAt.Format(time.RFC3339), entry.ExpiresAt.Format(time.RFC3339))
	return 0
}

// runProfile dispatches `wormhole profile <subcommand>`. Only "list" exists
// as of Chapter 8.
func runProfile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: wormhole profile list")
		return 2
	}
	return runProfileList(args[1:], stdout, stderr)
}

// runProfileList implements `wormhole profile list`: prints every stored
// credential profile's name, project, role, agent ID, and expiry, reading
// local files only — no server call.
func runProfileList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := profilesDir()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole profile list: %v\n", err)
		return 1
	}
	entries, err := listCredentialProfiles(dir)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole profile list: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no stored credential profiles")
		return 0
	}
	for _, e := range entries {
		role := e.Role
		if role == "" {
			role = "(none)"
		}
		fmt.Fprintf(stdout, "%s  project=%s role=%s agent_id=%s expires_at=%s\n",
			e.Name, e.Project, role, e.AgentID, e.ExpiresAt.Format(time.RFC3339))
	}
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/harley/vault/projects/wormhole && go build ./... && go vet ./... && go test ./cmd/wormhole-cli/... -v`
Expected: PASS for all new tests and every pre-existing test in the package.

- [ ] **Step 5: Commit**

```bash
cd /home/harley/vault/projects/wormhole
git add cmd/wormhole-cli/main.go cmd/wormhole-cli/main_test.go
git commit -m "feat(cli): wormhole whoami and profile list subcommands (Chapter 8 task 3)"
```

---

## Task 4: Full-suite verification, roadmap/ledger update, final review

**Files:**
- Modify: `ROADMAP-ALPHA2.md` (check off Chapter 8's 5 boxes)
- Modify: `.superpowers/sdd/progress.md` (append Chapter 8 entry)
- No production code changes in this task.

**Interfaces:**
- Consumes: nothing new — this task verifies the sum of Tasks 1-3.
- Produces: nothing consumed later (this is Chapter 8's final task).

- [ ] **Step 1: Run the full test suite**

Run: `cd /home/harley/vault/projects/wormhole && go build ./... && go vet ./... && go test ./...`
Expected: All packages pass. (Known pre-existing flakes, unrelated to this chapter's scope: `internal/core/tasks.TestRLSIsolation` and a broader RLS-role-drop flake class in `internal/core/git`/`internal/core/kb` under full-suite concurrent load — see Day 31/Chapter 7 ledger entry. If either appears, rerun that single package alone to confirm it passes in isolation before treating it as this chapter's regression.)

- [ ] **Step 2: Confirm no other file imports the deleted `defaultTokenFilePath`**

Run: `cd /home/harley/vault/projects/wormhole && grep -rn "defaultTokenFilePath" --include='*.go' .`
Expected: no matches (function and its one test were both deleted in Task 2).

- [ ] **Step 3: Check off Chapter 8 in `ROADMAP-ALPHA2.md`**

All 5 bullets under `### Chapter 8 — 2026-08-07` change from `- [ ]` to `- [x]`.

- [ ] **Step 4: Append a Chapter 8 entry to `.superpowers/sdd/progress.md`**

Append (matching the existing per-chapter ledger format used for Chapters 1-7):
```markdown

## Day 32 / Chapter 8 (docs/superpowers/plans/2026-08-07-day32-chapter8-credential-profiles.md, complete)

Task 1 (profiles.go — keyed credential-profile store primitives): complete
Task 2 (join/connect write keyed profiles, defaultTokenFilePath deleted): complete
Task 3 (wormhole whoami / wormhole profile list subcommands): complete
Task 4 (full-suite verification, roadmap checkboxes): complete

Chapter 8: complete. M2 (Role System, Chapters 5-8) closed.
```
(Fill in actual commit SHA ranges for each task from `git log --oneline` once Tasks 1-3 are committed — this plan cannot predict them in advance.)

- [ ] **Step 5: Commit**

```bash
cd /home/harley/vault/projects/wormhole
git add ROADMAP-ALPHA2.md .superpowers/sdd/progress.md
git commit -m "docs(roadmap): mark Chapter 8 complete, close M2"
```

---

## Self-Review Notes (for whoever executes this plan)

- **Spec coverage:** all 5 Chapter 8 roadmap bullets map to a task — bullet 1 (keyed store) → Task 1+2, bullet 2 (`join --role` own profile) → Task 2, bullet 3 (`whoami`/`profile list`) → Task 3, bullet 4 (no silent default, single-profile backward-compat) → Task 1's `resolveWhoamiProfile` + Task 3's tests, bullet 5 (unit tests for the keyed store) → Task 1's `profiles_test.go`.
- **`--token-file` backward compatibility:** every pre-Chapter-8 test in `main_test.go` passes `--token-file` explicitly, and `resolveCredentialsPath`'s first branch returns it verbatim unchanged — zero behavior change for those tests, confirmed by Task 2 Step 4 requiring the full pre-existing suite to stay green.
- **Security:** `validateProfileName` rejects (not sanitizes) path separators and `..` in an explicit `--profile` value, closing a path-traversal vector where a malicious or fat-fingered `--profile ../../etc/cron.d/x` could otherwise write outside `~/.wormhole/credentials/`. `sanitizeComponent` (used only for the *derived* default name from `--project`/`--role`, never for `--profile` itself) takes the replace-not-reject approach since those values are expected to already be safe (server-issued UUIDs, role-template names) and a derived filename has no human expectation of exact-name recall the way `--profile` does.
