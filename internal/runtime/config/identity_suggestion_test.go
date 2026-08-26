package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIdentitySuggestionReadsOnlyFixedLocalKeys(t *testing.T) {
	root := t.TempDir()
	runner := &identitySuggestionRunner{values: map[string]string{
		gitIdentityNameKey:       "Ada Lovelace\n",
		gitIdentityEmailKey:      "ada@example.test\n",
		gitIdentitySigningKeyKey: "0123456789ABCDEF0123456789ABCDEF01234567\n",
		gitIdentityFormatKey:     "openpgp\n",
	}}

	got, err := SuggestGitIdentity(t.Context(), runner, root)
	if err != nil {
		t.Fatalf("SuggestGitIdentity: %v", err)
	}
	want := GitIdentitySuggestion{
		DisplayName:   "Ada Lovelace",
		Email:         "ada@example.test",
		SigningKey:    "0123456789ABCDEF0123456789ABCDEF01234567",
		CommitSigning: true,
	}
	if got != want {
		t.Fatalf("suggestion = %+v, want %+v", got, want)
	}
	runner.requireFixedReadOnlyCalls(t, root)
}

func TestGitIdentitySuggestionTreatsUnsetLocalValuesAsEmpty(t *testing.T) {
	root := t.TempDir()
	runner := &identitySuggestionRunner{values: map[string]string{}}

	got, err := SuggestGitIdentity(t.Context(), runner, root)
	if err != nil {
		t.Fatalf("SuggestGitIdentity: %v", err)
	}
	if got != (GitIdentitySuggestion{}) {
		t.Fatalf("suggestion = %+v, want empty", got)
	}
	runner.requireFixedReadOnlyCalls(t, root)
}

func TestGitIdentitySuggestionRejectsUnsafeOutput(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		key    string
		value  string
		format string
	}{
		{name: "duplicate", key: gitIdentityNameKey, value: "Ada\nGrace\n"},
		{name: "missing newline", key: gitIdentityEmailKey, value: "ada@example.test"},
		{name: "nul", key: gitIdentitySigningKeyKey, value: "key\x00\n"},
		{name: "bounded", key: gitIdentityNameKey, value: strings.Repeat("a", maxGitIdentityValueBytes+1) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{test.key: test.value}
			if test.format != "" {
				values[gitIdentityFormatKey] = test.format
			}
			runner := &identitySuggestionRunner{values: values}
			if _, err := SuggestGitIdentity(t.Context(), runner, root); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
				t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
			}
			runner.requireFixedReadOnlyCalls(t, root)
		})
	}
}

func TestGitIdentitySuggestionRejectsNoncanonicalRootsBeforeGit(t *testing.T) {
	root := t.TempDir()
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	tests := []struct {
		name string
		root string
	}{
		{name: "relative", root: "."},
		{name: "not clean", root: root + string(filepath.Separator)},
		{name: "symlink", root: linked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &identitySuggestionRunner{}
			if _, err := SuggestGitIdentity(t.Context(), runner, test.root); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
				t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("git calls = %+v, want none", runner.calls)
			}
		})
	}
}

func TestGitIdentitySuggestionOnlyOffersOpenPGPSigning(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		format string
		want   GitIdentitySuggestion
	}{
		{
			name:   "openpgp",
			format: "openpgp\n",
			want:   GitIdentitySuggestion{SigningKey: "0123456789ABCDEF0123456789ABCDEF01234567", CommitSigning: true},
		},
		{
			name: "default openpgp",
			want: GitIdentitySuggestion{SigningKey: "0123456789ABCDEF0123456789ABCDEF01234567", CommitSigning: true},
		},
		{name: "ssh", format: "ssh\n"},
		{name: "x509", format: "x509\n"},
		{name: "unknown", format: "other\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &identitySuggestionRunner{values: map[string]string{
				gitIdentitySigningKeyKey: "0123456789ABCDEF0123456789ABCDEF01234567\n",
			}}
			if test.format != "" {
				runner.values[gitIdentityFormatKey] = test.format
			}
			got, err := SuggestGitIdentity(context.Background(), runner, root)
			if err != nil {
				t.Fatalf("SuggestGitIdentity: %v", err)
			}
			if got != test.want {
				t.Fatalf("suggestion = %+v, want %+v", got, test.want)
			}
			runner.requireFixedReadOnlyCalls(t, root)
		})
	}
}

type identitySuggestionCall struct {
	executable string
	args       []string
}

type identitySuggestionRunner struct {
	values map[string]string
	calls  []identitySuggestionCall
}

func (runner *identitySuggestionRunner) Run(_ context.Context, executable string, args ...string) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, identitySuggestionCall{executable: executable, args: append([]string(nil), args...)})
	if len(args) != 6 || args[0] != "-C" || args[2] != "config" || args[3] != "--local" || args[4] != "--get" {
		return nil, nil, errors.New("unexpected Git invocation")
	}
	if value, ok := runner.values[args[5]]; ok {
		return []byte(value), nil, nil
	}
	return nil, nil, &CommandExitError{ExitCode: 1}
}

func (runner *identitySuggestionRunner) requireFixedReadOnlyCalls(t *testing.T, root string) {
	t.Helper()
	wantKeys := []string{gitIdentityNameKey, gitIdentityEmailKey, gitIdentitySigningKeyKey, gitIdentityFormatKey}
	if len(runner.calls) != len(wantKeys) {
		t.Fatalf("git call count = %d, want %d: %+v", len(runner.calls), len(wantKeys), runner.calls)
	}
	for index, key := range wantKeys {
		call := runner.calls[index]
		if call.executable != "git" {
			t.Fatalf("call %d executable = %q, want git", index, call.executable)
		}
		wantArgs := []string{"-C", root, "config", "--local", "--get", key}
		if strings.Join(call.args, "\x00") != strings.Join(wantArgs, "\x00") {
			t.Fatalf("call %d args = %q, want %q", index, call.args, wantArgs)
		}
	}
}
