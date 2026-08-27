package types

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	testProjectID        = "33333333-3333-4333-8333-333333333333"
	testWorkspaceID      = "44444444-4444-4444-8444-444444444444"
	testProfileID        = "55555555-5555-4555-8555-555555555555"
	testFabricInstanceID = "66666666-6666-4666-8666-666666666666"
	testRemoteProjectID  = "77777777-7777-4777-8777-777777777777"
	testStreamID         = "88888888-8888-4888-8888-888888888888"
	testAttachmentRef    = "99999999-9999-4999-8999-999999999999"
)

func testFabricProfile() FabricProfile {
	return FabricProfile{
		ProfileID:        testProfileID,
		Alias:            "primary",
		FabricInstanceID: testFabricInstanceID,
		BaseURL:          "https://fabric.example.test",
		Mode:             FabricModePrivate,
		CredentialRef:    "keyring:primary",
	}
}

func testFabricBinding() FabricBinding {
	return FabricBinding{
		Workspace: WorkspaceBinding{
			Scope:       WorkspaceScope{ProjectID: testProjectID, WorkspaceID: WorkspaceID(testWorkspaceID)},
			Checkout:    CheckoutIdentity{CanonicalPath: "/work/wormhole", Device: 1, Inode: 2},
			Repository:  RepositoryIdentity{Provider: "github", ImmutableID: "R_kgDOExample-1", CanonicalRemote: "https://github.com/acme/wormhole"},
			AcceptedRef: "refs/heads/main", AcceptedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AcceptedTreeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		ProfileID:        testProfileID,
		FabricInstanceID: testFabricInstanceID,
		RemoteProjectID:  testRemoteProjectID,
		StreamID:         testStreamID,
		AttachmentRef:    testAttachmentRef,
		CanonicalRef:     "refs/heads/main",
		Writable:         true,
	}
}

func TestFabricBindingRequiresCanonicalWorkspaceAndMatchingInstance(t *testing.T) {
	profile := testFabricProfile()
	binding := testFabricBinding()
	if err := binding.ValidateWithProfile(profile); err != nil {
		t.Fatalf("ValidateWithProfile(valid): %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*FabricBinding, *FabricProfile)
	}{
		{"project ID", func(b *FabricBinding, _ *FabricProfile) { b.Workspace.Scope.ProjectID = "not-a-uuid" }},
		{"workspace ID", func(b *FabricBinding, _ *FabricProfile) { b.Workspace.Scope.WorkspaceID = "not-a-uuid" }},
		{"profile ID", func(b *FabricBinding, _ *FabricProfile) { b.ProfileID = "not-a-uuid" }},
		{"profile ID equality", func(_ *FabricBinding, p *FabricProfile) { p.ProfileID = testAttachmentRef }},
		{"Fabric instance ID", func(b *FabricBinding, _ *FabricProfile) { b.FabricInstanceID = "not-a-uuid" }},
		{"remote project ID", func(b *FabricBinding, _ *FabricProfile) { b.RemoteProjectID = "not-a-uuid" }},
		{"stream ID", func(b *FabricBinding, _ *FabricProfile) { b.StreamID = "not-a-uuid" }},
		{"attachment ref", func(b *FabricBinding, _ *FabricProfile) { b.AttachmentRef = "not-a-uuid" }},
		{"repository identity", func(b *FabricBinding, _ *FabricProfile) { b.Workspace.Repository.ImmutableID = "" }},
		{"canonical ref", func(b *FabricBinding, _ *FabricProfile) { b.CanonicalRef = "refs/heads/other" }},
		{"profile instance equality", func(b *FabricBinding, p *FabricProfile) { p.FabricInstanceID = testAttachmentRef }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalidBinding := binding
			invalidProfile := profile
			tc.mutate(&invalidBinding, &invalidProfile)
			if err := invalidBinding.ValidateWithProfile(invalidProfile); !errors.Is(err, ErrInvalidFabricRoute) {
				t.Fatalf("ValidateWithProfile() error = %v, want ErrInvalidFabricRoute", err)
			}
		})
	}
}

func TestFabricProfileRequiresCanonicalProfileID(t *testing.T) {
	profile := testFabricProfile()
	profile.ProfileID = "not-a-uuid"
	if err := profile.Validate(); !errors.Is(err, ErrInvalidFabricRoute) {
		t.Fatalf("Validate() error = %v, want ErrInvalidFabricRoute", err)
	}
}

func TestFabricBindingHasNoCredentialAuthority(t *testing.T) {
	for _, value := range []any{FabricBinding{}, RemoteBindingKey{}} {
		typeOfValue := reflect.TypeOf(value)
		for index := 0; index < typeOfValue.NumField(); index++ {
			field := strings.ToLower(typeOfValue.Field(index).Name)
			for _, forbidden := range []string{"credential", "token", "secret"} {
				if strings.Contains(field, forbidden) {
					t.Fatalf("%s.%s grants %s authority", typeOfValue.Name(), typeOfValue.Field(index).Name, forbidden)
				}
			}
		}
	}
}

func TestRemoteBindingKeyRejectsEveryPartialCombination(t *testing.T) {
	valid := RemoteBindingKey{
		ProjectID:        testProjectID,
		WorkspaceID:      WorkspaceID(testWorkspaceID),
		FabricInstanceID: testFabricInstanceID,
		RemoteProjectID:  testRemoteProjectID,
		StreamID:         testStreamID,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid): %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*RemoteBindingKey)
	}{
		{"project ID", func(key *RemoteBindingKey) { key.ProjectID = "" }},
		{"workspace ID", func(key *RemoteBindingKey) { key.WorkspaceID = "" }},
		{"Fabric instance ID", func(key *RemoteBindingKey) { key.FabricInstanceID = "" }},
		{"remote project ID", func(key *RemoteBindingKey) { key.RemoteProjectID = "" }},
		{"stream ID", func(key *RemoteBindingKey) { key.StreamID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			partial := valid
			tc.mutate(&partial)
			if err := partial.Validate(); !errors.Is(err, ErrInvalidFabricRoute) {
				t.Fatalf("Validate() error = %v, want ErrInvalidFabricRoute", err)
			}
		})
	}
}
