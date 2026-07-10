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
	want := "proj_.._etc_passwd_id"
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
