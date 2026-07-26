package config_test

import (
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
)

func TestCanonicalRemoteCoversSupportedIdentityShapesAndRejectsUnsafeURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "empty", value: " \t", wantErr: true},
		{name: "query", value: "https://example.com/acme/repo?token=secret", wantErr: true},
		{name: "fragment", value: "ssh://git@example.com/acme/repo#main", wantErr: true},
		{name: "https username", value: "https://git@example.com/acme/repo", wantErr: true},
		{name: "ssh password", value: "ssh://git:secret@example.com/acme/repo", wantErr: true},
		{name: "ssh username", value: "ssh://git@Example.COM/acme/repo/", want: "ssh://git@example.com/acme/repo.git"},
		{name: "scp syntax", value: "git@GitHub.COM:Acme/Repo/", want: "git@github.com:Acme/Repo.git"},
		{name: "fallback path", value: "/srv/git/acme/repo/", want: "/srv/git/acme/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.CanonicalRemote(tt.value)
			if tt.wantErr {
				if !errors.Is(err, config.ErrInvalidProject) {
					t.Fatalf("CanonicalRemote(%q) error = %v, want ErrInvalidProject", tt.value, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("CanonicalRemote(%q) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
}
