package config_test

import (
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
)

func TestDefaultProjectIsDisabled(t *testing.T) {
	t.Parallel()

	got := config.DefaultProject("project-a")
	if got.ProjectID != "project-a" {
		t.Fatalf("ProjectID = %q, want project-a", got.ProjectID)
	}
	if got.Enabled {
		t.Fatal("Code Graph must be disabled by default")
	}
	if got.CanonicalRemote != "" || got.ActiveCheckout != "" {
		t.Fatalf("default project selected source: remote=%q checkout=%q", got.CanonicalRemote, got.ActiveCheckout)
	}
	if got.ProjectSourceByteCeiling <= 0 {
		t.Fatalf("ProjectSourceByteCeiling = %d, want positive bounded default", got.ProjectSourceByteCeiling)
	}
}

func TestCanonicalRemoteRejectsHTTPSCredentialsWithoutLeakingThem(t *testing.T) {
	const secret = "super-secret-token"
	_, err := config.CanonicalRemote("https://agent:" + secret + "@github.com/Acme/Repo.git")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("CanonicalRemote(credential origin) error = %v", err)
	}
}

func TestValidateProjectRequiresBindingAndOneCheckout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project config.Project
		wantErr bool
	}{
		{name: "disabled binding", project: config.DefaultProject("project-a")},
		{name: "enabled approved checkout", project: config.Project{
			ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/acme/repo.git",
			ActiveCheckout: "/work/repo", ProjectSourceByteCeiling: 4096,
		}},
		{name: "missing project", project: config.Project{ProjectSourceByteCeiling: 1}, wantErr: true},
		{name: "enabled without remote", project: config.Project{
			ProjectID: "project-a", Enabled: true, ActiveCheckout: "/work/repo", ProjectSourceByteCeiling: 1,
		}, wantErr: true},
		{name: "enabled without checkout", project: config.Project{
			ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/acme/repo.git", ProjectSourceByteCeiling: 1,
		}, wantErr: true},
		{name: "unbounded source", project: config.Project{ProjectID: "project-a"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.ValidateProject(tt.project)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateProject() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCanonicalRemoteNormalizesEquivalentRepositoryIdentities(t *testing.T) {
	want := "https://github.com/Acme/Repo.git"
	for _, value := range []string{
		"HTTPS://GitHub.COM/Acme/Repo", "https://github.com/Acme/Repo.git/", "https://github.com/Acme/Repo/",
	} {
		got, err := config.CanonicalRemote(value)
		if err != nil || got != want {
			t.Errorf("CanonicalRemote(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	other, err := config.CanonicalRemote("https://github.com/Acme/Other.git")
	if err != nil || other == want {
		t.Fatalf("different repository normalized to %q, %v", other, err)
	}
}
