package types

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrInvalidFabricRoute = errors.New("types: invalid Fabric route")

type FabricMode string

const (
	FabricModePublic  FabricMode = "public"
	FabricModePrivate FabricMode = "private"
)

type FabricProfile struct {
	ProfileID        string
	Alias            string
	FabricInstanceID string
	BaseURL          string
	Mode             FabricMode
	CredentialRef    string
}

type RemoteBindingKey struct {
	ProjectID        string
	WorkspaceID      WorkspaceID
	FabricInstanceID string
	RemoteProjectID  string
	StreamID         string
}

// ActivityRouteKey is the complete immutable routing authority for ActivityV1.
// It is an internal value and deliberately has no wire-format tags.
type ActivityRouteKey struct {
	ProjectID        string
	WorkspaceID      WorkspaceID
	FabricInstanceID string
	RemoteProjectID  string
	StreamID         string
	CanonicalRef     string
}

// ActivityOriginKey adds the origin identity needed for Activity idempotency.
// It is an internal value and deliberately has no wire-format tags.
type ActivityOriginKey struct {
	Route             ActivityRouteKey
	SourceWorkspaceID WorkspaceID
	ActivityID        string
}

type FabricBinding struct {
	Workspace        WorkspaceBinding
	ProfileID        string
	FabricInstanceID string
	RemoteProjectID  string
	StreamID         string
	AttachmentRef    string
	CanonicalRef     string
	Writable         bool
}

func (p FabricProfile) Validate() error {
	if !CanonicalUUID(p.ProfileID) || !handleComponentPattern.MatchString(p.Alias) || !CanonicalUUID(p.FabricInstanceID) || !validFabricBaseURL(p.BaseURL) {
		return fmt.Errorf("%w: invalid profile", ErrInvalidFabricRoute)
	}
	switch p.Mode {
	case FabricModePublic, FabricModePrivate:
		return nil
	default:
		return fmt.Errorf("%w: invalid mode", ErrInvalidFabricRoute)
	}
}

func (k RemoteBindingKey) Validate() error {
	if !CanonicalUUID(k.ProjectID) || !CanonicalUUID(string(k.WorkspaceID)) || !CanonicalUUID(k.FabricInstanceID) || !CanonicalUUID(k.RemoteProjectID) || !CanonicalUUID(k.StreamID) {
		return fmt.Errorf("%w: incomplete remote binding key", ErrInvalidFabricRoute)
	}
	return nil
}

func (k ActivityRouteKey) Validate() error {
	if !CanonicalUUID(k.ProjectID) || !CanonicalUUID(string(k.WorkspaceID)) || !CanonicalUUID(k.FabricInstanceID) || !CanonicalUUID(k.RemoteProjectID) || !CanonicalUUID(k.StreamID) || k.CanonicalRef == "" {
		return fmt.Errorf("%w: incomplete activity route key", ErrInvalidFabricRoute)
	}
	if !validBranchRef(k.CanonicalRef) {
		return fmt.Errorf("%w: invalid activity canonical ref", ErrInvalidFabricRoute)
	}
	name := strings.TrimPrefix(k.CanonicalRef, "refs/heads/")
	for index := 0; index < len(name); index++ {
		character := name[index]
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || strings.ContainsRune("._/-", rune(character))) {
			return fmt.Errorf("%w: invalid activity canonical ref", ErrInvalidFabricRoute)
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." {
			return fmt.Errorf("%w: invalid activity canonical ref", ErrInvalidFabricRoute)
		}
	}
	return nil
}

func (k ActivityOriginKey) Validate() error {
	if err := k.Route.Validate(); err != nil {
		return err
	}
	if !CanonicalUUID(string(k.SourceWorkspaceID)) || !CanonicalUUID(k.ActivityID) {
		return fmt.Errorf("%w: invalid activity origin key", ErrInvalidFabricRoute)
	}
	return nil
}

func (b FabricBinding) ValidateWithProfile(profile FabricProfile) error {
	if err := b.Workspace.Validate(); err != nil {
		return fmt.Errorf("%w: invalid workspace: %v", ErrInvalidFabricRoute, err)
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if !CanonicalUUID(b.ProfileID) || !CanonicalUUID(b.FabricInstanceID) || !CanonicalUUID(b.RemoteProjectID) || !CanonicalUUID(b.StreamID) || !CanonicalUUID(b.AttachmentRef) {
		return fmt.Errorf("%w: invalid binding identifiers", ErrInvalidFabricRoute)
	}
	if b.ProfileID != profile.ProfileID || b.FabricInstanceID != profile.FabricInstanceID {
		return fmt.Errorf("%w: profile mismatch", ErrInvalidFabricRoute)
	}
	if b.CanonicalRef != b.Workspace.AcceptedRef {
		return fmt.Errorf("%w: canonical ref mismatch", ErrInvalidFabricRoute)
	}
	if err := b.RemoteKey().Validate(); err != nil {
		return err
	}
	return nil
}

func (b FabricBinding) RemoteKey() RemoteBindingKey {
	return RemoteBindingKey{
		ProjectID:        b.Workspace.Scope.ProjectID,
		WorkspaceID:      b.Workspace.Scope.WorkspaceID,
		FabricInstanceID: b.FabricInstanceID,
		RemoteProjectID:  b.RemoteProjectID,
		StreamID:         b.StreamID,
	}
}

func validFabricBaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Scheme != strings.ToLower(parsed.Scheme) || parsed.Hostname() != strings.ToLower(parsed.Hostname()) {
		return false
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}).String() == value
}
