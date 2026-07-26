// Package config defines Gateway-local, project-scoped Code Graph settings.
// Code Graph is experimental and disabled unless a human-controlled lifecycle
// operation persists an enabled project configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

const DefaultProjectSourceByteCeiling int64 = 64 * 1024

var ErrInvalidProject = errors.New("codegraph config: invalid project configuration")

// Project is the one local Code Graph configuration and approved checkout for
// a Wormhole project. It contains no Fabric-synchronised state.
type Project struct {
	ProjectID                string
	Enabled                  bool
	CanonicalRemote          string
	ActiveCheckout           string
	ProjectSourceByteCeiling int64
	LastSuccessfulBuild      *time.Time
}

// CanonicalRemote returns the stable repository identity used by passport
// scope, persisted Code Graph config, and Git-origin validation.
func CanonicalRemote(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: canonical remote is required", ErrInvalidProject)
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("%w: canonical remote cannot contain query or fragment", ErrInvalidProject)
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword || parsed.Scheme == "http" || parsed.Scheme == "https" {
				return "", fmt.Errorf("%w: canonical remote cannot contain credentials", ErrInvalidProject)
			}
		}
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = canonicalRepositoryPath(parsed.Path)
		return parsed.String(), nil
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		if colon := strings.Index(value[at+1:], ":"); colon >= 0 {
			colon += at + 1
			return value[:at+1] + strings.ToLower(value[at+1:colon]) + ":" + strings.TrimPrefix(canonicalRepositoryPath("/"+value[colon+1:]), "/"), nil
		}
	}
	return strings.TrimSuffix(strings.TrimSuffix(value, "/"), ".git") + ".git", nil
}

func canonicalRepositoryPath(value string) string {
	clean := path.Clean("/" + strings.TrimSpace(value))
	clean = strings.TrimSuffix(clean, "/")
	clean = strings.TrimSuffix(clean, ".git")
	return clean + ".git"
}

// DefaultProject returns the disabled state for a project with no persisted
// Code Graph configuration.
func DefaultProject(projectID string) Project {
	return Project{
		ProjectID:                projectID,
		ProjectSourceByteCeiling: DefaultProjectSourceByteCeiling,
	}
}

// ValidateProject enforces the storage boundary. Canonicalisation and remote
// verification belong to the later human-controlled CLI lifecycle.
func ValidateProject(project Project) error {
	if strings.TrimSpace(project.ProjectID) == "" {
		return fmt.Errorf("%w: project id is required", ErrInvalidProject)
	}
	if project.ProjectSourceByteCeiling <= 0 {
		return fmt.Errorf("%w: project source byte ceiling must be positive", ErrInvalidProject)
	}
	if project.Enabled && strings.TrimSpace(project.CanonicalRemote) == "" {
		return fmt.Errorf("%w: enabled project requires a canonical remote", ErrInvalidProject)
	}
	if project.Enabled && strings.TrimSpace(project.ActiveCheckout) == "" {
		return fmt.Errorf("%w: enabled project requires an active checkout", ErrInvalidProject)
	}
	return nil
}
