package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		input    ResolveInput
		required bool
		expected string
		wantErr  bool
	}{
		{
			name:     "flag takes priority",
			input:    ResolveInput{Flag: "from-flag", Local: "from-local", Global: "from-global"},
			required: true,
			expected: "from-flag",
		},
		{
			name:     "local overrides global",
			input:    ResolveInput{Local: "from-local", Global: "from-global"},
			required: true,
			expected: "from-local",
		},
		{
			name:     "global used if no local",
			input:    ResolveInput{Global: "from-global"},
			required: true,
			expected: "from-global",
		},
		{
			name:     "environment used after config",
			input:    ResolveInput{EnvKey: "WORMHOLE_RESOLVE_TEST"},
			required: true,
			expected: "from-environment",
		},
		{
			name:     "default used after empty environment",
			input:    ResolveInput{EnvKey: "WORMHOLE_RESOLVE_UNSET_TEST", Default: "from-default"},
			required: true,
			expected: "from-default",
		},
		{
			name:     "required error if all empty",
			input:    ResolveInput{},
			required: true,
			wantErr:  true,
		},
		{
			name:     "optional returns empty if all empty",
			input:    ResolveInput{},
			required: false,
			expected: "",
		},
	}

	t.Setenv("WORMHOLE_RESOLVE_TEST", "from-environment")
	if err := os.Unsetenv("WORMHOLE_RESOLVE_UNSET_TEST"); err != nil {
		t.Fatalf("unset environment: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.input, tt.required)
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

func TestResolveConvenienceFunctions(t *testing.T) {
	local := Config{Server: "https://local.example", Project: "project-local"}
	global := Config{Server: "https://global.example"}

	if got, err := ResolveOwner("owner-flag", Config{}, Config{}); err != nil || got != "owner-flag" {
		t.Fatalf("ResolveOwner flag: got %q, err %v", got, err)
	}
	if got, err := ResolveRepositories("repo-flag"); err != nil || got != "repo-flag" {
		t.Fatalf("ResolveRepositories flag: got %q, err %v", got, err)
	}
	if got, err := ResolveProject("project-flag", local); err != nil || got != "project-flag" {
		t.Fatalf("ResolveProject flag: got %q, err %v", got, err)
	}
	if got, err := ResolveProject("", local); err != nil || got != "project-local" {
		t.Fatalf("ResolveProject local: got %q, err %v", got, err)
	}
	if _, err := ResolveProject("", Config{}); err == nil {
		t.Fatal("ResolveProject empty: got nil error")
	}
	if got, err := ResolveServer("server-flag", local, global); err != nil || got != "server-flag" {
		t.Fatalf("ResolveServer flag: got %q, err %v", got, err)
	}
	if got, err := ResolveServer("", local, global); err != nil || got != "https://local.example" {
		t.Fatalf("ResolveServer local: got %q, err %v", got, err)
	}
	if got, err := ResolveServer("", Config{}, global); err != nil || got != "https://global.example" {
		t.Fatalf("ResolveServer global: got %q, err %v", got, err)
	}
	if _, err := ResolveServer("", Config{}, Config{}); err == nil {
		t.Fatal("ResolveServer empty: got nil error")
	}
}

func TestResolveOwnerAndRepositoriesFromGit(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test Owner")
	runGit(t, repo, "remote", "add", "origin", "https://example.invalid/acme/wormhole.git")

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldCWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	if got, err := ResolveOwner("", Config{}, Config{}); err != nil || got != "Test Owner" {
		t.Fatalf("ResolveOwner git config: got %q, err %v", got, err)
	}
	if got, err := ResolveRepositories(""); err != nil || got != "https://example.invalid/acme/wormhole.git" {
		t.Fatalf("ResolveRepositories origin: got %q, err %v", got, err)
	}
}

func TestResolveRepositoriesWithoutRepositoryReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	got, err := ResolveRepositories("")
	if err != nil || got != "" {
		t.Fatalf("ResolveRepositories outside repo: got %q, err %v", got, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(dir, "global.gitconfig"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestGitConfigUserName(t *testing.T) {
	// Only runs if git is available and we're in a repo
	name, err := gitConfigUserName()
	if err != nil {
		t.Logf("skipping git config test (not in repo or git not available): %v", err)
		return
	}
	if name == "" {
		t.Errorf("expected non-empty name from git config")
	}
}

func TestGitRemoteGetURL(t *testing.T) {
	url, err := gitRemoteGetURL("origin")
	if err != nil {
		t.Logf("skipping git remote test (no origin remote): %v", err)
		return
	}
	if url == "" {
		t.Errorf("expected non-empty URL from git remote")
	}
}
