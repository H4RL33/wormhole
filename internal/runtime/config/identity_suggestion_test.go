package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIdentitySuggestionUsesOnlyFixedReadOnlyGitArgv(t *testing.T) {
	root := t.TempDir()
	runner := &identitySuggestionRunner{root: root, values: map[string]identitySuggestionResponse{
		gitIdentityNameKey:          {stdout: "Ada Lovelace\n"},
		gitIdentityEmailKey:         {stdout: "ada@example.test\n"},
		gitIdentitySigningKeyKey:    {stdout: "0123456789ABCDEF0123456789ABCDEF01234567\n"},
		gitIdentityCommitSigningKey: {stdout: "true\n"},
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

func TestGitIdentitySuggestionTreatsUnsetValuesAsEmpty(t *testing.T) {
	root := t.TempDir()
	runner := &identitySuggestionRunner{root: root}

	got, err := SuggestGitIdentity(t.Context(), runner, root)
	if err != nil {
		t.Fatalf("SuggestGitIdentity: %v", err)
	}
	if got != (GitIdentitySuggestion{}) {
		t.Fatalf("suggestion = %+v, want empty", got)
	}
	runner.requireFixedReadOnlyCalls(t, root)
}

func TestGitIdentitySuggestionRejectsDuplicateMalformedAndBoundedOutput(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "duplicate get-all values", key: gitIdentityNameKey, value: "Ada\nGrace\n"},
		{name: "missing newline", key: gitIdentityEmailKey, value: "ada@example.test"},
		{name: "nul", key: gitIdentitySigningKeyKey, value: "key\x00\n"},
		{name: "unicode C1 control", key: gitIdentityNameKey, value: "Ada\u0085Lovelace\n"},
		{name: "bounded", key: gitIdentityNameKey, value: strings.Repeat("a", maxGitIdentityValueBytes+1) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &identitySuggestionRunner{root: root, values: map[string]identitySuggestionResponse{
				test.key: {stdout: test.value},
			}}
			if _, err := SuggestGitIdentity(t.Context(), runner, root); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
				t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
			}
			runner.requireFixedReadOnlyCalls(t, root)
		})
	}
}

func TestGitIdentitySuggestionRejectsActualDuplicateGitConfig(t *testing.T) {
	root := t.TempDir()
	runIdentitySuggestionGit(t, "init", "--quiet", root)
	runIdentitySuggestionGit(t, "-C", root, "config", "--local", "--add", gitIdentityNameKey, "Ada Lovelace")
	runIdentitySuggestionGit(t, "-C", root, "config", "--local", "--add", gitIdentityNameKey, "Grace Hopper")

	if _, err := SuggestGitIdentity(t.Context(), NewCommandRunner(), root); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
		t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
	}
}

func TestGitIdentitySuggestionRejectsActualMalformedCommitSigning(t *testing.T) {
	root := t.TempDir()
	runIdentitySuggestionGit(t, "init", "--quiet", root)
	runIdentitySuggestionGit(t, "-C", root, "config", "--local", gitIdentityCommitSigningKey, "enabled")

	if _, err := SuggestGitIdentity(t.Context(), NewCommandRunner(), root); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
		t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
	}
}

func TestGitIdentitySuggestionRejectsMalformedCommitSigning(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		output string
	}{
		{name: "non boolean", output: "enabled\n"},
		{name: "duplicate booleans", output: "true\nfalse\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &identitySuggestionRunner{root: root, values: map[string]identitySuggestionResponse{
				gitIdentityCommitSigningKey: {stdout: test.output},
			}}
			if _, err := SuggestGitIdentity(t.Context(), runner, root); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
				t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
			}
			runner.requireFixedReadOnlyCalls(t, root)
		})
	}
}

func TestGitIdentitySuggestionRequiresVerifiedRepositoryTopLevel(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	tests := []struct {
		name     string
		root     string
		topLevel string
	}{
		{name: "child root mismatch", root: child, topLevel: root},
		{name: "worktree top level", root: root, topLevel: root},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &identitySuggestionRunner{root: test.root, topLevel: identitySuggestionResponse{stdout: test.topLevel + "\n"}}
			got, err := SuggestGitIdentity(t.Context(), runner, test.root)
			if test.name == "child root mismatch" {
				if !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
					t.Fatalf("SuggestGitIdentity error = %v, want ErrInvalidGitIdentitySuggestion", err)
				}
				if len(runner.calls) != 1 {
					t.Fatalf("git calls = %+v, want only top-level verification", runner.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("SuggestGitIdentity: %v", err)
			}
			if got != (GitIdentitySuggestion{}) {
				t.Fatalf("suggestion = %+v, want empty", got)
			}
			runner.requireFixedReadOnlyCalls(t, test.root)
		})
	}
}

func TestGitIdentitySuggestionUsesActualWorktreeTopLevel(t *testing.T) {
	mainRoot := t.TempDir()
	runIdentitySuggestionGit(t, "init", "--quiet", mainRoot)
	runIdentitySuggestionGit(t, "-C", mainRoot, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "--quiet", "-m", "initial")

	worktreeRoot := filepath.Join(t.TempDir(), "linked-worktree")
	runIdentitySuggestionGit(t, "-C", mainRoot, "worktree", "add", "--quiet", "--detach", worktreeRoot, "HEAD")
	if got, err := SuggestGitIdentity(t.Context(), NewCommandRunner(), worktreeRoot); err != nil || got != (GitIdentitySuggestion{}) {
		t.Fatalf("SuggestGitIdentity = (%+v, %v), want (empty, nil)", got, err)
	}

	child := filepath.Join(worktreeRoot, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatalf("mkdir worktree child: %v", err)
	}
	if _, err := SuggestGitIdentity(t.Context(), NewCommandRunner(), child); !errors.Is(err, ErrInvalidGitIdentitySuggestion) {
		t.Fatalf("SuggestGitIdentity child error = %v, want ErrInvalidGitIdentitySuggestion", err)
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

func TestGitIdentitySuggestionOnlyExposesRecognizedOpenPGPReferences(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name       string
		signingKey string
		wantKey    string
	}{
		{name: "v4 fingerprint", signingKey: "0123456789ABCDEF0123456789ABCDEF01234567", wantKey: "0123456789ABCDEF0123456789ABCDEF01234567"},
		{name: "v5 fingerprint", signingKey: "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", wantKey: "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"},
		{name: "key identifier", signingKey: "0x0123456789ABCDEF", wantKey: "0x0123456789ABCDEF"},
		{name: "ssh", signingKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI"},
		{name: "x509", signingKey: "CN=developer@example.test"},
		{name: "path", signingKey: "/home/user/.ssh/id_ed25519.pub"},
		{name: "file URL", signingKey: "file:///home/user/private.key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &identitySuggestionRunner{root: root, values: map[string]identitySuggestionResponse{
				gitIdentitySigningKeyKey:    {stdout: test.signingKey + "\n"},
				gitIdentityCommitSigningKey: {stdout: "false\n"},
			}}
			got, err := SuggestGitIdentity(context.Background(), runner, root)
			if err != nil {
				t.Fatalf("SuggestGitIdentity: %v", err)
			}
			if got.SigningKey != test.wantKey || got.CommitSigning {
				t.Fatalf("suggestion = %+v, want SigningKey=%q and CommitSigning=false", got, test.wantKey)
			}
			runner.requireFixedReadOnlyCalls(t, root)
		})
	}
}

type identitySuggestionResponse struct {
	stdout string
	stderr string
	err    error
}

type identitySuggestionCall struct {
	executable string
	args       []string
}

type identitySuggestionRunner struct {
	root     string
	topLevel identitySuggestionResponse
	values   map[string]identitySuggestionResponse
	calls    []identitySuggestionCall
}

func (runner *identitySuggestionRunner) Run(_ context.Context, executable string, args ...string) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, identitySuggestionCall{executable: executable, args: append([]string(nil), args...)})
	if len(args) == 4 && args[0] == "-C" && args[2] == "rev-parse" && args[3] == "--show-toplevel" {
		response := runner.topLevel
		if response.stdout == "" && response.stderr == "" && response.err == nil {
			response.stdout = runner.root + "\n"
		}
		return []byte(response.stdout), []byte(response.stderr), response.err
	}
	if len(args) != 6 && len(args) != 7 {
		return nil, nil, errors.New("unexpected Git invocation")
	}
	if args[0] != "-C" || args[2] != "config" || args[3] != "--local" || args[len(args)-2] != "--get-all" {
		return nil, nil, errors.New("unexpected Git invocation")
	}
	key := args[len(args)-1]
	if key == gitIdentityCommitSigningKey && (len(args) != 7 || args[4] != "--bool") {
		return nil, nil, errors.New("commit signing must use --bool --get-all")
	}
	if key != gitIdentityCommitSigningKey && len(args) != 6 {
		return nil, nil, errors.New("identity values must use --get-all")
	}
	if response, ok := runner.values[key]; ok {
		return []byte(response.stdout), []byte(response.stderr), response.err
	}
	return nil, nil, &CommandExitError{ExitCode: 1}
}

func (runner *identitySuggestionRunner) requireFixedReadOnlyCalls(t *testing.T, root string) {
	t.Helper()
	want := [][]string{
		{"-C", root, "rev-parse", "--show-toplevel"},
		{"-C", root, "config", "--local", "--get-all", gitIdentityNameKey},
		{"-C", root, "config", "--local", "--get-all", gitIdentityEmailKey},
		{"-C", root, "config", "--local", "--get-all", gitIdentitySigningKeyKey},
		{"-C", root, "config", "--local", "--bool", "--get-all", gitIdentityCommitSigningKey},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("git call count = %d, want %d: %+v", len(runner.calls), len(want), runner.calls)
	}
	for index, args := range want {
		call := runner.calls[index]
		if call.executable != "git" {
			t.Fatalf("call %d executable = %q, want git", index, call.executable)
		}
		if strings.Join(call.args, "\x00") != strings.Join(args, "\x00") {
			t.Fatalf("call %d args = %q, want %q", index, call.args, args)
		}
	}
}

func runIdentitySuggestionGit(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %q: %v (%s)", args, err, output)
	}
}
