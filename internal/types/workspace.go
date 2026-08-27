package types

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrInvalidRepositoryIdentity = errors.New("types: invalid repository identity")
	ErrInvalidWorkspaceContext   = errors.New("types: invalid workspace context")
	ErrInvalidWorkspaceBinding   = errors.New("types: invalid workspace binding")
)

var (
	handleComponentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	immutableIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	commitPattern          = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type WorkspaceID string

type WorkspaceScope struct {
	ProjectID   string      `json:"project_id"`
	WorkspaceID WorkspaceID `json:"workspace_id"`
}

type ProjectHandle struct {
	Namespace string
	Name      string
}

type RepositoryIdentity struct {
	Provider        string `json:"provider" toml:"provider"`
	ImmutableID     string `json:"immutable_id" toml:"immutable_id"`
	CanonicalRemote string `json:"canonical_remote" toml:"canonical_remote"`
}

type CheckoutIdentity struct {
	CanonicalPath string
	Device        uint64
	Inode         uint64
}

type WorkspaceContext struct {
	WorkingDirectory string `json:"working_directory"`
}

type WorkspaceBinding struct {
	Scope              WorkspaceScope
	Checkout           CheckoutIdentity
	Repository         RepositoryIdentity
	AcceptedRef        string
	AcceptedCommitSHA  string
	AcceptedTreeDigest string
}

func (h ProjectHandle) Validate() error {
	if !handleComponentPattern.MatchString(h.Namespace) || !handleComponentPattern.MatchString(h.Name) {
		return fmt.Errorf("invalid project handle %q/%q", h.Namespace, h.Name)
	}
	return nil
}

func (r RepositoryIdentity) Validate() error {
	if r.Provider == "" {
		if r.ImmutableID != "" || r.CanonicalRemote != "" {
			return fmt.Errorf("%w: local-only identity must be empty", ErrInvalidRepositoryIdentity)
		}
		return nil
	}
	if !handleComponentPattern.MatchString(r.Provider) || !immutableIDPattern.MatchString(r.ImmutableID) || r.CanonicalRemote == "" {
		return fmt.Errorf("%w: missing or malformed provider identity", ErrInvalidRepositoryIdentity)
	}
	remote, err := url.Parse(r.CanonicalRemote)
	if err != nil || remote.Scheme == "" || remote.Host == "" || remote.User != nil || remote.RawQuery != "" || remote.Fragment != "" {
		return fmt.Errorf("%w: malformed canonical remote", ErrInvalidRepositoryIdentity)
	}
	if remote.Scheme != strings.ToLower(remote.Scheme) || remote.Hostname() != strings.ToLower(remote.Hostname()) || remote.Path == "" || remote.Path == "/" {
		return fmt.Errorf("%w: canonical remote is not normalized", ErrInvalidRepositoryIdentity)
	}
	canonicalRemote := (&url.URL{Scheme: remote.Scheme, Host: remote.Host, Path: remote.Path}).String()
	if canonicalRemote != r.CanonicalRemote {
		return fmt.Errorf("%w: canonical remote has an equivalent non-canonical serialization", ErrInvalidRepositoryIdentity)
	}
	if strings.HasSuffix(remote.Path, "/") || strings.HasSuffix(remote.Path, ".git") || path.Clean(remote.Path) != remote.Path || hasDotPathSegment(remote.EscapedPath()) {
		return fmt.Errorf("%w: canonical remote path is not normalized", ErrInvalidRepositoryIdentity)
	}
	return nil
}

func (c WorkspaceContext) Validate() error {
	if c.WorkingDirectory == "" || strings.ContainsRune(c.WorkingDirectory, 0) || !filepath.IsAbs(c.WorkingDirectory) || filepath.Clean(c.WorkingDirectory) != c.WorkingDirectory {
		return fmt.Errorf("%w: working_directory must be absolute and clean", ErrInvalidWorkspaceContext)
	}
	return nil
}

func (b WorkspaceBinding) Validate() error {
	if !CanonicalUUID(b.Scope.ProjectID) || !CanonicalUUID(string(b.Scope.WorkspaceID)) {
		return fmt.Errorf("%w: invalid scope", ErrInvalidWorkspaceBinding)
	}
	if err := (WorkspaceContext{WorkingDirectory: b.Checkout.CanonicalPath}).Validate(); err != nil || b.Checkout.Device == 0 || b.Checkout.Inode == 0 {
		return fmt.Errorf("%w: invalid checkout", ErrInvalidWorkspaceBinding)
	}
	if err := b.Repository.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkspaceBinding, err)
	}
	if !validBranchRef(b.AcceptedRef) || !commitPattern.MatchString(b.AcceptedCommitSHA) || !digestPattern.MatchString(b.AcceptedTreeDigest) {
		return fmt.Errorf("%w: invalid accepted base", ErrInvalidWorkspaceBinding)
	}
	return nil
}

func hasDotPathSegment(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." || strings.EqualFold(segment, "%2e") || strings.EqualFold(segment, "%2e%2e") {
			return true
		}
	}
	return false
}

func validBranchRef(value string) bool {
	if value == "" {
		return true
	}
	const prefix = "refs/heads/"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	name := strings.TrimPrefix(value, prefix)
	if name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") || strings.ContainsAny(name, " ~^:?*[\\") {
		return false
	}
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}
