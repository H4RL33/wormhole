package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const maxCommittedTreeBytes = 64 << 20

type committedWorkspace struct {
	root        string
	checkout    types.CheckoutIdentity
	acceptedRef string
	commit      string
	tree        state.Tree
	snapshot    state.Snapshot
}

func inspectCommittedWorkspace(ctx context.Context, requestedRoot, expectedCommit string) (committedWorkspace, error) {
	root, err := canonicalNonSymlinkDirectory(requestedRoot)
	if err != nil {
		return committedWorkspace{}, err
	}
	checkout, err := checkoutIdentity(root)
	if err != nil {
		return committedWorkspace{}, err
	}
	gitRootOutput, err := readOnlyGit(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return committedWorkspace{}, fmt.Errorf("projectstate: resolve Git root: %w", err)
	}
	gitRoot := trimGitLine(gitRootOutput)
	gitRoot, err = filepath.Abs(gitRoot)
	if err != nil || filepath.Clean(gitRoot) != root {
		return committedWorkspace{}, fmt.Errorf("projectstate: requested root is not the exact Git checkout root")
	}
	headOutput, err := readOnlyGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return committedWorkspace{}, fmt.Errorf("projectstate: resolve Git HEAD: %w", err)
	}
	head := trimGitLine(headOutput)
	if head != expectedCommit {
		return committedWorkspace{}, fmt.Errorf("projectstate: Git HEAD %q differs from expected commit %q", head, expectedCommit)
	}
	acceptedRef, err := symbolicHead(ctx, root)
	if err != nil {
		return committedWorkspace{}, err
	}
	tree, err := readCommittedTree(ctx, root, expectedCommit)
	if err != nil {
		return committedWorkspace{}, err
	}
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		return committedWorkspace{}, fmt.Errorf("projectstate: decode committed .wormhole tree: %w", err)
	}
	finalHeadOutput, err := readOnlyGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || trimGitLine(finalHeadOutput) != expectedCommit {
		return committedWorkspace{}, fmt.Errorf("projectstate: Git HEAD changed during registration")
	}
	revalidated, err := checkoutIdentity(root)
	if err != nil {
		return committedWorkspace{}, err
	}
	if revalidated != checkout {
		return committedWorkspace{}, fmt.Errorf("projectstate: checkout identity changed during registration")
	}
	return committedWorkspace{root: root, checkout: checkout, acceptedRef: acceptedRef, commit: head, tree: tree, snapshot: snapshot}, nil
}

func canonicalNonSymlinkDirectory(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("projectstate: invalid checkout root")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("projectstate: canonicalize checkout root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("projectstate: inspect checkout root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("projectstate: checkout root must be a non-symlink directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("projectstate: resolve checkout root: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || filepath.Clean(resolved) != absolute {
		return "", fmt.Errorf("projectstate: checkout root contains a symlink")
	}
	return absolute, nil
}

func checkoutIdentity(root string) (types.CheckoutIdentity, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return types.CheckoutIdentity{}, fmt.Errorf("projectstate: stat checkout: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return types.CheckoutIdentity{}, fmt.Errorf("projectstate: checkout is not a non-symlink directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return types.CheckoutIdentity{}, fmt.Errorf("projectstate: checkout has no stable filesystem identity")
	}
	return types.CheckoutIdentity{CanonicalPath: root, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

func symbolicHead(ctx context.Context, root string) (string, error) {
	output, err := readOnlyGit(ctx, root, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		return trimGitLine(output), nil
	}
	var exitError *gitExitError
	if errors.As(err, &exitError) && exitError.code == 1 {
		return "", nil
	}
	return "", fmt.Errorf("projectstate: resolve symbolic Git HEAD: %w", err)
}

func readCommittedTree(ctx context.Context, root, commit string) (state.Tree, error) {
	listing, err := readOnlyGitLimited(ctx, root, maxCommittedTreeBytes, "ls-tree", "-r", "-z", "--full-tree", commit, "--", ".wormhole")
	if err != nil {
		return nil, fmt.Errorf("projectstate: list committed .wormhole tree: %w", err)
	}
	records := bytes.Split(listing, []byte{0})
	tree := make(state.Tree, 0, len(records))
	total := 0
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		metadata, gitPath, found := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !found || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
			return nil, fmt.Errorf("projectstate: .wormhole contains a non-regular committed entry")
		}
		fullPath := string(gitPath)
		if !strings.HasPrefix(fullPath, ".wormhole/") {
			return nil, fmt.Errorf("projectstate: invalid committed .wormhole path %q", fullPath)
		}
		relative := strings.TrimPrefix(fullPath, ".wormhole/")
		if relative == "" || filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))) != relative || strings.ContainsRune(relative, 0) {
			return nil, fmt.Errorf("projectstate: invalid committed .wormhole path %q", fullPath)
		}
		remaining := maxCommittedTreeBytes - total - len(relative)
		if remaining < 0 {
			return nil, fmt.Errorf("projectstate: committed .wormhole tree exceeds size limit")
		}
		contents, err := readOnlyGitLimited(ctx, root, remaining, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, fmt.Errorf("projectstate: read committed .wormhole blob %q: %w", fullPath, err)
		}
		total += len(relative) + len(contents)
		if total > maxCommittedTreeBytes {
			return nil, fmt.Errorf("projectstate: committed .wormhole tree exceeds size limit")
		}
		tree = append(tree, state.File{Path: relative, Data: contents})
	}
	sort.Slice(tree, func(i, j int) bool { return tree[i].Path < tree[j].Path })
	return tree, nil
}

type gitExitError struct {
	code   int
	stderr string
}

func (e *gitExitError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("git exited with status %d", e.code)
	}
	return fmt.Sprintf("git exited with status %d: %s", e.code, e.stderr)
}

func readOnlyGit(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	return readOnlyGitLimited(ctx, root, maxCommittedTreeBytes, arguments...)
}

func readOnlyGitLimited(ctx context.Context, root string, limit int, arguments ...string) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("projectstate: invalid Git output limit")
	}
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "credential.helper=",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "submodule.recurse=false",
		"-C", root,
	}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = sanitizedGitEnvironment()
	command.Stdin = bytes.NewReader(nil)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if len(output) > limit {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("projectstate: Git output exceeds size limit")
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr == nil {
		return output, nil
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		return nil, &gitExitError{code: exit.ExitCode(), stderr: strings.TrimSpace(stderr.String())}
	}
	return nil, waitErr
}

func sanitizedGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+9)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || key == "SSH_ASKPASS" || key == "SSH_ASKPASS_REQUIRE" || key == "GCM_INTERACTIVE" || key == "LC_ALL" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_PROTOCOL_FROM_USER=0",
		"GCM_INTERACTIVE=never",
		"SSH_ASKPASS=/bin/false",
		"SSH_ASKPASS_REQUIRE=never",
		"LC_ALL=C",
	)
}

func trimGitLine(output []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
}
