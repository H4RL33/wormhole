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
