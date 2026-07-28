package types

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRepositoryIdentityValidate(t *testing.T) {
	valid := RepositoryIdentity{Provider: "github", ImmutableID: "R_kgDOExample-1", CanonicalRemote: "https://github.com/acme/wormhole"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := (RepositoryIdentity{}).Validate(); err != nil {
		t.Fatalf("local-only Validate: %v", err)
	}
	for _, remote := range []string{
		"https://user:pass@github.com/acme/wormhole",
		"https://github.com/acme/../wormhole",
		"https://github.com/acme/wormhole.git",
		"https://github.com/acme/wormhole/",
		"https://github.com/acme/wormhole#fragment",
	} {
		invalid := valid
		invalid.CanonicalRemote = remote
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidRepositoryIdentity) {
			t.Errorf("Validate(%q) error = %v, want ErrInvalidRepositoryIdentity", remote, err)
		}
	}
}

func TestProjectHandleValidate(t *testing.T) {
	if err := (ProjectHandle{Namespace: "acme", Name: "wormhole"}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, handle := range []ProjectHandle{{Namespace: "", Name: "wormhole"}, {Namespace: "Acme", Name: "wormhole"}, {Namespace: "acme", Name: "bad/name"}} {
		if err := handle.Validate(); err == nil {
			t.Errorf("Validate(%+v) succeeded", handle)
		}
	}
}

func TestWorkspaceContextValidate(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "work", "wormhole")
	if err := (WorkspaceContext{WorkingDirectory: abs}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, value := range []string{"", "relative/path", abs + string(filepath.Separator) + ".." + string(filepath.Separator) + "wormhole", abs + "\x00bad"} {
		if err := (WorkspaceContext{WorkingDirectory: value}).Validate(); !errors.Is(err, ErrInvalidWorkspaceContext) {
			t.Errorf("Validate(%q) error = %v, want ErrInvalidWorkspaceContext", value, err)
		}
	}
}

func TestWorkspaceBindingValidate(t *testing.T) {
	binding := WorkspaceBinding{
		Scope:       WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "88888888-8888-4888-8888-888888888888"},
		Checkout:    CheckoutIdentity{CanonicalPath: "/work/wormhole", Device: 1, Inode: 2},
		Repository:  RepositoryIdentity{Provider: "github", ImmutableID: "R_kgDOExample-1", CanonicalRemote: "https://github.com/acme/wormhole"},
		AcceptedRef: "refs/heads/main", AcceptedCommitSHA: "0123456789abcdef0123456789abcdef01234567",
		AcceptedTreeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	invalid := binding
	invalid.AcceptedTreeDigest = "sha256:ABC"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidWorkspaceBinding) {
		t.Fatalf("Validate invalid digest error = %v, want ErrInvalidWorkspaceBinding", err)
	}
	for _, mutate := range []func(*WorkspaceBinding){
		func(value *WorkspaceBinding) { value.Checkout.Inode = 0 },
		func(value *WorkspaceBinding) { value.Repository.CanonicalRemote += ".git" },
		func(value *WorkspaceBinding) { value.AcceptedRef = "main" },
		func(value *WorkspaceBinding) { value.AcceptedCommitSHA = "ABC" },
	} {
		invalid := binding
		mutate(&invalid)
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidWorkspaceBinding) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidWorkspaceBinding", invalid, err)
		}
	}
}
