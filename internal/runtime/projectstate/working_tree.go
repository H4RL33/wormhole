package projectstate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	maxCommittedTreeBytes        = 64 << 20
	maxCommittedTreeFiles        = 10_000
	maxCommittedTreePathBytes    = 4 << 10
	maxCommittedObjectBytes      = 16 << 20
	maxCommittedTreeListingBytes = 8 << 20
	maxGitStderrBytes            = 64 << 10
	maxGitBatchHeaderBytes       = 256
)

type committedWorkspace struct {
	root        string
	checkout    types.CheckoutIdentity
	acceptedRef string
	commit      string
	tree        state.Tree
	snapshot    state.Snapshot
}

type committedWorkspaceFinalReaders struct {
	headCommit       func(context.Context, string) (string, error)
	checkoutIdentity func(string) (types.CheckoutIdentity, error)
}

func inspectCommittedWorkspace(ctx context.Context, requestedRoot, expectedCommit string) (committedWorkspace, error) {
	return inspectCommittedWorkspaceWithRaceSentinel(ctx, requestedRoot, expectedCommit, false)
}

func inspectCommittedWorkspaceForGitBase(ctx context.Context, requestedRoot, expectedCommit string) (committedWorkspace, error) {
	return inspectCommittedWorkspaceWithRaceSentinel(ctx, requestedRoot, expectedCommit, true)
}

func inspectCommittedWorkspaceWithRaceSentinel(ctx context.Context, requestedRoot, expectedCommit string, observation bool) (committedWorkspace, error) {
	return inspectCommittedWorkspaceWithFinalReaders(ctx, requestedRoot, expectedCommit, observation, committedWorkspaceFinalReaders{
		headCommit: readCommittedWorkspaceHead, checkoutIdentity: checkoutIdentity,
	})
}

func inspectCommittedWorkspaceWithFinalReaders(
	ctx context.Context,
	requestedRoot, expectedCommit string,
	observation bool,
	finalReaders committedWorkspaceFinalReaders,
) (committedWorkspace, error) {
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
		if observation {
			return committedWorkspace{}, fmt.Errorf("%w: Git HEAD differs from expected commit", ErrGitObservationChanged)
		}
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
	finalHead, err := finalReaders.headCommit(ctx, root)
	if err != nil {
		if observation {
			return committedWorkspace{}, fmt.Errorf("%w: re-read Git HEAD: %w", ErrGitObservationChanged, err)
		}
		return committedWorkspace{}, fmt.Errorf("projectstate: Git HEAD changed during registration")
	}
	if finalHead != expectedCommit {
		if observation {
			return committedWorkspace{}, fmt.Errorf("%w: Git HEAD changed during committed-tree read", ErrGitObservationChanged)
		}
		return committedWorkspace{}, fmt.Errorf("projectstate: Git HEAD changed during registration")
	}
	revalidated, err := finalReaders.checkoutIdentity(root)
	if err != nil {
		if observation {
			return committedWorkspace{}, fmt.Errorf("%w: revalidate checkout identity: %w", ErrGitObservationChanged, err)
		}
		return committedWorkspace{}, err
	}
	if revalidated != checkout {
		if observation {
			return committedWorkspace{}, fmt.Errorf("%w: checkout identity changed during committed-tree read", ErrGitObservationChanged)
		}
		return committedWorkspace{}, fmt.Errorf("projectstate: checkout identity changed during registration")
	}
	return committedWorkspace{root: root, checkout: checkout, acceptedRef: acceptedRef, commit: head, tree: tree, snapshot: snapshot}, nil
}

func readCommittedWorkspaceHead(ctx context.Context, root string) (string, error) {
	output, err := readOnlyGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return trimGitLine(output), nil
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
	listing, err := readOnlyGitLimited(ctx, root, maxCommittedTreeListingBytes, "ls-tree", "-r", "-z", "--full-tree", commit, "--", ".wormhole")
	if err != nil {
		return nil, fmt.Errorf("projectstate: list committed .wormhole tree: %w", err)
	}
	objects, err := parseCommittedTreeListing(listing)
	if err != nil {
		return nil, err
	}
	pathBytes := 0
	for _, object := range objects {
		pathBytes += len(object.path)
		if pathBytes > maxCommittedTreeBytes {
			return nil, fmt.Errorf("projectstate: committed .wormhole tree exceeds aggregate size limit")
		}
	}
	tree, err := readCommittedBlobs(ctx, root, objects, maxCommittedTreeBytes-pathBytes)
	if err != nil {
		return nil, err
	}
	sort.Slice(tree, func(i, j int) bool { return tree[i].Path < tree[j].Path })
	return tree, nil
}

type committedTreeObject struct {
	path string
	oid  string
}

func parseCommittedTreeListing(listing []byte) ([]committedTreeObject, error) {
	records := bytes.Split(listing, []byte{0})
	objects := make([]committedTreeObject, 0, min(len(records), maxCommittedTreeFiles))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		if len(objects) >= maxCommittedTreeFiles {
			return nil, fmt.Errorf("projectstate: committed .wormhole tree exceeds file count limit")
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
		if len(relative) > maxCommittedTreePathBytes {
			return nil, fmt.Errorf("projectstate: committed .wormhole path exceeds size limit")
		}
		if relative == "" || filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))) != relative || strings.ContainsRune(relative, 0) {
			return nil, fmt.Errorf("projectstate: invalid committed .wormhole path %q", fullPath)
		}
		if !validCommit(fields[2]) {
			return nil, fmt.Errorf("projectstate: invalid committed .wormhole object ID")
		}
		objects = append(objects, committedTreeObject{path: relative, oid: fields[2]})
	}
	return objects, nil
}

func readCommittedBlobs(ctx context.Context, root string, objects []committedTreeObject, aggregateLimit int) (state.Tree, error) {
	command := newReadOnlyGitCommand(ctx, root, "cat-file", "--batch")
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("projectstate: open Git batch input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("projectstate: open Git batch output: %w", err)
	}
	stderr := newBoundedGitStderr()
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("projectstate: start Git batch reader: %w", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		var writeErr error
		for _, object := range objects {
			if _, err := io.WriteString(stdin, object.oid+"\n"); err != nil {
				writeErr = err
				break
			}
		}
		if err := stdin.Close(); writeErr == nil {
			writeErr = err
		}
		writeDone <- writeErr
	}()

	tree, decodeErr := decodeBatchObjects(bufio.NewReader(stdout), objects, maxCommittedObjectBytes, aggregateLimit)
	if decodeErr != nil {
		_ = command.Process.Kill()
	}
	writeErr := <-writeDone
	waitErr := command.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("projectstate: decode Git batch output: %w", decodeErr)
	}
	if writeErr != nil {
		return nil, fmt.Errorf("projectstate: write Git batch input: %w", writeErr)
	}
	if waitErr != nil {
		return nil, gitWaitError(waitErr, stderr)
	}
	if stderr.truncated {
		return nil, fmt.Errorf("projectstate: Git stderr exceeded size limit: %s", stderr.text())
	}
	return tree, nil
}

func decodeBatchObjects(reader *bufio.Reader, objects []committedTreeObject, objectLimit, aggregateLimit int) (state.Tree, error) {
	if objectLimit < 0 || aggregateLimit < 0 {
		return nil, fmt.Errorf("invalid Git batch size limit")
	}
	tree := make(state.Tree, 0, len(objects))
	total := 0
	for _, object := range objects {
		header, err := readBoundedBatchHeader(reader)
		if err != nil {
			return nil, fmt.Errorf("read object %q header: %w", object.path, err)
		}
		fields := strings.Fields(header)
		if len(fields) == 2 && fields[0] == object.oid && fields[1] == "missing" {
			return nil, fmt.Errorf("committed object %s is missing", object.oid)
		}
		if len(fields) != 3 || fields[0] != object.oid || fields[1] != "blob" {
			return nil, fmt.Errorf("unexpected Git batch header for %q", object.path)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid object size for %q", object.path)
		}
		if size > int64(objectLimit) {
			return nil, fmt.Errorf("committed object size exceeds limit for %q", object.path)
		}
		if size > int64(aggregateLimit-total) {
			return nil, fmt.Errorf("committed object aggregate exceeds size limit")
		}
		contents := make([]byte, int(size))
		if _, err := io.ReadFull(reader, contents); err != nil {
			return nil, fmt.Errorf("read object %q contents: %w", object.path, err)
		}
		terminator, err := reader.ReadByte()
		if err != nil || terminator != '\n' {
			return nil, fmt.Errorf("invalid Git batch terminator for %q", object.path)
		}
		total += len(contents)
		tree = append(tree, state.File{Path: object.path, Data: contents})
	}
	if extra, err := reader.ReadByte(); err == nil {
		return nil, fmt.Errorf("unexpected trailing Git batch output byte %q", extra)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("finish Git batch output: %w", err)
	}
	return tree, nil
}

func readBoundedBatchHeader(reader *bufio.Reader) (string, error) {
	header := make([]byte, 0, maxGitBatchHeaderBytes)
	for {
		fragment, prefix, err := reader.ReadLine()
		if err != nil {
			return "", err
		}
		if len(fragment) > maxGitBatchHeaderBytes-len(header) {
			return "", fmt.Errorf("Git batch header exceeds size limit")
		}
		header = append(header, fragment...)
		if !prefix {
			return string(header), nil
		}
	}
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
	command := newReadOnlyGitCommand(ctx, root, arguments...)
	command.Stdin = bytes.NewReader(nil)
	stderr := newBoundedGitStderr()
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if len(output) > limit || readErr != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(output) > limit {
		return nil, fmt.Errorf("projectstate: Git output exceeds size limit")
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, gitWaitError(waitErr, stderr)
	}
	if stderr.truncated {
		return nil, fmt.Errorf("projectstate: Git stderr exceeded size limit: %s", stderr.text())
	}
	return output, nil
}

func newReadOnlyGitCommand(ctx context.Context, root string, arguments ...string) *exec.Cmd {
	global := []string{
		"--no-pager", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.sshCommand=/bin/false",
		"-c", "credential.helper=",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.git.allow=never",
		"-c", "protocol.http.allow=never",
		"-c", "protocol.https.allow=never",
		"-c", "protocol.ssh.allow=never",
		"-c", "submodule.recurse=false",
		"-C", root,
	}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = sanitizedGitEnvironment()
	return command
}

type boundedGitStderr struct {
	buffer    bytes.Buffer
	truncated bool
}

func newBoundedGitStderr() *boundedGitStderr {
	stderr := &boundedGitStderr{}
	stderr.buffer.Grow(maxGitStderrBytes)
	return stderr
}

func (stderr *boundedGitStderr) Write(data []byte) (int, error) {
	remaining := maxGitStderrBytes - stderr.buffer.Len()
	if remaining > 0 {
		retained := min(remaining, len(data))
		_, _ = stderr.buffer.Write(data[:retained])
	}
	if len(data) > remaining {
		stderr.truncated = true
	}
	return len(data), nil
}

func (stderr *boundedGitStderr) text() string {
	text := strings.TrimSpace(stderr.buffer.String())
	if stderr.truncated {
		if text != "" {
			text += " "
		}
		text += "[stderr truncated]"
	}
	return text
}

func gitWaitError(waitErr error, stderr *boundedGitStderr) error {
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		return &gitExitError{code: exit.ExitCode(), stderr: stderr.text()}
	}
	return waitErr
}

func sanitizedGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+13)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if strings.HasPrefix(upperKey, "GIT_") || strings.HasPrefix(upperKey, "SSH_") ||
			strings.HasPrefix(upperKey, "GCM_") || strings.HasSuffix(upperKey, "_PROXY") || upperKey == "LC_ALL" {
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
		"GIT_NO_LAZY_FETCH=1",
		"GIT_ALLOW_PROTOCOL=",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_SSH=/bin/false",
		"GIT_SSH_COMMAND=/bin/false",
		"GIT_ASKPASS=/bin/false",
		"GCM_INTERACTIVE=never",
		"SSH_ASKPASS=/bin/false",
		"SSH_ASKPASS_REQUIRE=never",
		"LC_ALL=C",
	)
}

func trimGitLine(output []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
}
