package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxGitIdentityRootBytes     = 4096
	maxGitIdentityValueBytes    = 1024
	gitIdentityNameKey          = "user.name"
	gitIdentityEmailKey         = "user.email"
	gitIdentitySigningKeyKey    = "user.signingkey"
	gitIdentityCommitSigningKey = "commit.gpgsign"
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
	if err := verifyGitRepositoryTopLevel(ctx, runner, root); err != nil {
		return GitIdentitySuggestion{}, err
	}

	keys := []string{gitIdentityNameKey, gitIdentityEmailKey, gitIdentitySigningKeyKey}
	rawValues := make(map[string][]byte, len(keys))
	for _, key := range keys {
		output, unset, err := readLocalGitIdentityValue(ctx, runner, root, key, false)
		if err != nil {
			return GitIdentitySuggestion{}, err
		}
		if !unset {
			rawValues[key] = output
		}
	}
	commitSigningOutput, commitSigningUnset, err := readLocalGitIdentityValue(ctx, runner, root, gitIdentityCommitSigningKey, true)
	if err != nil {
		return GitIdentitySuggestion{}, err
	}

	values := make(map[string]string, len(rawValues))
	for _, key := range keys {
		output, ok := rawValues[key]
		if !ok {
			continue
		}
		value, err := parseSingleGitValue(output, maxGitIdentityValueBytes)
		if err != nil {
			return GitIdentitySuggestion{}, err
		}
		values[key] = value
	}

	suggestion := GitIdentitySuggestion{
		DisplayName: values[gitIdentityNameKey],
		Email:       values[gitIdentityEmailKey],
	}
	if isOpenPGPSigningReference(values[gitIdentitySigningKeyKey]) {
		suggestion.SigningKey = values[gitIdentitySigningKeyKey]
	}
	if !commitSigningUnset {
		value, err := parseSingleGitValue(commitSigningOutput, maxGitIdentityValueBytes)
		if err != nil || (value != "true" && value != "false") {
			return GitIdentitySuggestion{}, ErrInvalidGitIdentitySuggestion
		}
		suggestion.CommitSigning = value == "true"
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

func verifyGitRepositoryTopLevel(ctx context.Context, runner CommandRunner, root string) error {
	stdout, stderr, err := runner.Run(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("config: verify Git repository root: %w", err)
	}
	if len(stderr) != 0 {
		return ErrInvalidGitIdentitySuggestion
	}
	topLevel, err := parseSingleGitValue(stdout, maxGitIdentityRootBytes)
	if err != nil || topLevel != root {
		return ErrInvalidGitIdentitySuggestion
	}
	return nil
}

func readLocalGitIdentityValue(ctx context.Context, runner CommandRunner, root, key string, boolean bool) ([]byte, bool, error) {
	args := []string{"-C", root, "config", "--local"}
	if boolean {
		args = append(args, "--bool")
	}
	args = append(args, "--get-all", key)
	stdout, stderr, err := runner.Run(ctx, "git", args...)
	if err != nil {
		var exitErr *CommandExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode == 1 && len(stdout) == 0 && len(stderr) == 0 {
			return nil, true, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		if errors.As(err, &exitErr) {
			return nil, false, ErrInvalidGitIdentitySuggestion
		}
		return nil, false, fmt.Errorf("config: read local Git %s: %w", key, err)
	}
	if len(stderr) != 0 {
		return nil, false, ErrInvalidGitIdentitySuggestion
	}
	return stdout, false, nil
}

func parseSingleGitValue(output []byte, maxBytes int) (string, error) {
	if len(output) == 0 || len(output) > maxBytes+1 || output[len(output)-1] != '\n' || bytes.Count(output, []byte{'\n'}) != 1 {
		return "", ErrInvalidGitIdentitySuggestion
	}
	value := output[:len(output)-1]
	if !utf8.Valid(value) {
		return "", ErrInvalidGitIdentitySuggestion
	}
	for _, character := range string(value) {
		if unicode.IsControl(character) {
			return "", ErrInvalidGitIdentitySuggestion
		}
	}
	return string(value), nil
}

func isOpenPGPSigningReference(value string) bool {
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		value = value[2:]
	}
	if len(value) != 16 && len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && !(character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}
