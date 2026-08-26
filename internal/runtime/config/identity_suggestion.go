package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxGitIdentityRootBytes  = 4096
	maxGitIdentityValueBytes = 1024
	gitIdentityNameKey       = "user.name"
	gitIdentityEmailKey      = "user.email"
	gitIdentitySigningKeyKey = "user.signingkey"
	gitIdentityFormatKey     = "gpg.format"
)

// ErrInvalidGitIdentitySuggestion reports malformed Git config output or an
// invalid repository root. Its error does not include Git config values.
var ErrInvalidGitIdentitySuggestion = errors.New("config: invalid Git identity suggestion")

// GitIdentitySuggestion is unconfirmed local metadata suggested from the
// repository's local Git configuration.
type GitIdentitySuggestion struct {
	DisplayName   string
	Email         string
	SigningKey    string
	CommitSigning bool
}

// SuggestGitIdentity reads only fixed, local Git identity configuration from
// canonicalRoot. It never invokes credential helpers or signing tools, reads
// key files, or changes repository state.
func SuggestGitIdentity(ctx context.Context, runner CommandRunner, canonicalRoot string) (GitIdentitySuggestion, error) {
	root, err := validateCanonicalGitRoot(canonicalRoot)
	if err != nil {
		return GitIdentitySuggestion{}, err
	}
	if runner == nil {
		return GitIdentitySuggestion{}, ErrInvalidGitIdentitySuggestion
	}

	keys := []string{
		gitIdentityNameKey,
		gitIdentityEmailKey,
		gitIdentitySigningKeyKey,
		gitIdentityFormatKey,
	}
	rawValues := make(map[string][]byte, len(keys))
	for _, key := range keys {
		output, unset, err := readLocalGitIdentityValue(ctx, runner, root, key)
		if err != nil {
			return GitIdentitySuggestion{}, err
		}
		if !unset {
			rawValues[key] = output
		}
	}
	values := make(map[string]string, len(rawValues))
	for _, key := range keys {
		output, ok := rawValues[key]
		if !ok {
			continue
		}
		value, err := parseSingleGitIdentityValue(output)
		if err != nil {
			return GitIdentitySuggestion{}, err
		}
		values[key] = value
	}

	format := values[gitIdentityFormatKey]
	suggestion := GitIdentitySuggestion{
		DisplayName: values[gitIdentityNameKey],
		Email:       values[gitIdentityEmailKey],
	}
	if values[gitIdentitySigningKeyKey] != "" && (format == "" || strings.EqualFold(format, "openpgp")) {
		suggestion.SigningKey = values[gitIdentitySigningKeyKey]
		suggestion.CommitSigning = true
	}
	return suggestion, nil
}

func validateCanonicalGitRoot(root string) (string, error) {
	if root == "" || len(root) > maxGitIdentityRootBytes || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return "", ErrInvalidGitIdentitySuggestion
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolved) != root {
		return "", ErrInvalidGitIdentitySuggestion
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", ErrInvalidGitIdentitySuggestion
	}
	return root, nil
}

func readLocalGitIdentityValue(ctx context.Context, runner CommandRunner, root, key string) ([]byte, bool, error) {
	stdout, stderr, err := runner.Run(ctx, "git", "-C", root, "config", "--local", "--get", key)
	if err != nil {
		var exitErr *CommandExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode == 1 && len(stdout) == 0 && len(stderr) == 0 {
			return nil, true, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, fmt.Errorf("config: read local Git %s: %w", key, err)
	}
	if len(stderr) != 0 {
		return nil, false, ErrInvalidGitIdentitySuggestion
	}
	return stdout, false, nil
}

func parseSingleGitIdentityValue(output []byte) (string, error) {
	if len(output) == 0 || len(output) > maxGitIdentityValueBytes+1 || output[len(output)-1] != '\n' || bytes.Count(output, []byte{'\n'}) != 1 {
		return "", ErrInvalidGitIdentitySuggestion
	}
	value := output[:len(output)-1]
	if !utf8.Valid(value) {
		return "", ErrInvalidGitIdentitySuggestion
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", ErrInvalidGitIdentitySuggestion
		}
	}
	return string(value), nil
}
