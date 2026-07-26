package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	cgconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
)

const (
	MaxTrackedGoFiles          = 10_000
	MaxTrackedFileBytes  int64 = 4 << 20
	MaxTrackedTotalBytes int64 = 128 << 20
	maxGitOutputBytes          = 64 << 20
)

var ErrCheckoutRoot = errors.New("codegraph index: checkout is not the canonical Git root")
var ErrRemoteMismatch = errors.New("codegraph index: canonical remote mismatch")
var ErrUnsupportedTrackedFile = errors.New("codegraph index: unsupported tracked file")
var ErrTrackedFileChanged = errors.New("codegraph index: tracked file missing or changed")
var ErrInventoryLimit = errors.New("codegraph index: tracked inventory exceeds limit")

type InventoryLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

var DefaultInventoryLimits = InventoryLimits{
	MaxFiles: MaxTrackedGoFiles, MaxFileBytes: MaxTrackedFileBytes, MaxTotalBytes: MaxTrackedTotalBytes,
}

type TrackedFile struct {
	Path   string
	Mode   string
	Bytes  []byte
	SHA256 string
}

type GitInventory struct {
	Root            string
	CanonicalRemote string
	Commit          string
	Files           []TrackedFile
	TotalBytes      int64
}

func LoadGitInventory(ctx context.Context, checkout, canonicalRemote string) (GitInventory, error) {
	return LoadGitInventoryWithLimits(ctx, checkout, canonicalRemote, DefaultInventoryLimits)
}

func LoadGitInventoryWithLimits(ctx context.Context, checkout, canonicalRemote string, limits InventoryLimits) (GitInventory, error) {
	if err := validateInventoryLimits(limits); err != nil {
		return GitInventory{}, err
	}
	root, err := canonicalCheckout(checkout)
	if err != nil {
		return GitInventory{}, err
	}
	gitRootOutput, err := gitOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitInventory{}, fmt.Errorf("%w: %v", ErrCheckoutRoot, err)
	}
	gitRoot, err := canonicalCheckout(strings.TrimSpace(string(gitRootOutput)))
	if err != nil || gitRoot != root {
		return GitInventory{}, fmt.Errorf("%w: checkout=%q git_root=%q", ErrCheckoutRoot, root, strings.TrimSpace(string(gitRootOutput)))
	}
	remoteOutput, err := gitOutput(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return GitInventory{}, fmt.Errorf("codegraph index: resolve canonical remote: %w", err)
	}
	remote, remoteErr := cgconfig.CanonicalRemote(strings.TrimSpace(string(remoteOutput)))
	configured, configuredErr := cgconfig.CanonicalRemote(canonicalRemote)
	if remoteErr != nil || configuredErr != nil || remote != configured {
		return GitInventory{}, fmt.Errorf("%w: configured repository and checkout origin differ", ErrRemoteMismatch)
	}
	commitOutput, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return GitInventory{}, fmt.Errorf("codegraph index: resolve checkout commit: %w", err)
	}
	commit := strings.TrimSpace(string(commitOutput))
	if !lowerHexOfLength(commit, 40, 64) {
		return GitInventory{}, fmt.Errorf("codegraph index: invalid checkout commit %q", commit)
	}
	stageOutput, err := gitOutput(ctx, root, "ls-files", "-z", "--cached", "--stage", "--", "*.go")
	if err != nil {
		return GitInventory{}, fmt.Errorf("codegraph index: enumerate tracked Go files: %w", err)
	}
	entries, err := parseTrackedStage(stageOutput)
	if err != nil {
		return GitInventory{}, err
	}
	if len(entries) > limits.MaxFiles {
		return GitInventory{}, fmt.Errorf("%w: files=%d maximum=%d", ErrInventoryLimit, len(entries), limits.MaxFiles)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return GitInventory{}, fmt.Errorf("%w: open checkout root", ErrCheckoutRoot)
	}
	defer rootHandle.Close()
	inventory := GitInventory{Root: root, CanonicalRemote: remote, Commit: commit, Files: make([]TrackedFile, 0, len(entries))}
	for _, entry := range entries {
		file, err := readTrackedFile(rootHandle, root, entry, limits.MaxFileBytes)
		if err != nil {
			return GitInventory{}, err
		}
		if inventory.TotalBytes > limits.MaxTotalBytes-int64(len(file.Bytes)) {
			return GitInventory{}, fmt.Errorf("%w: total bytes exceed %d", ErrInventoryLimit, limits.MaxTotalBytes)
		}
		inventory.TotalBytes += int64(len(file.Bytes))
		inventory.Files = append(inventory.Files, file)
	}
	return inventory, nil
}

type trackedStageEntry struct {
	path string
	mode string
}

func parseTrackedStage(output []byte) ([]trackedStageEntry, error) {
	records := bytes.Split(output, []byte{0})
	entries := make([]trackedStageEntry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab <= 0 || tab == len(record)-1 {
			return nil, fmt.Errorf("%w: malformed Git stage record", ErrUnsupportedTrackedFile)
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[2] != "0" || !lowerHexOfLength(fields[1], 40, 64) {
			return nil, fmt.Errorf("%w: mode/stage/object is not a regular stage-zero entry", ErrUnsupportedTrackedFile)
		}
		filePath := string(record[tab+1:])
		if !utf8.ValidString(filePath) || strings.Contains(filePath, "\\") || !strings.HasSuffix(filePath, ".go") || path.Clean(filePath) != filePath || !filepath.IsLocal(filepath.FromSlash(filePath)) {
			return nil, fmt.Errorf("%w: unsafe tracked path", ErrUnsupportedTrackedFile)
		}
		if _, exists := seen[filePath]; exists {
			return nil, fmt.Errorf("%w: colliding tracked path", ErrUnsupportedTrackedFile)
		}
		seen[filePath] = struct{}{}
		entries = append(entries, trackedStageEntry{path: filePath, mode: fields[0]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, nil
}

func readTrackedFile(rootHandle *os.Root, root string, entry trackedStageEntry, maximum int64) (TrackedFile, error) {
	relativePath := filepath.FromSlash(entry.path)
	fullPath := filepath.Join(root, filepath.FromSlash(entry.path))
	if !containedPath(root, fullPath) {
		return TrackedFile{}, fmt.Errorf("%w: tracked path escaped checkout", ErrUnsupportedTrackedFile)
	}
	infoBefore, err := rootHandle.Lstat(relativePath)
	if err != nil || !infoBefore.Mode().IsRegular() {
		return TrackedFile{}, fmt.Errorf("%w: %s", ErrTrackedFileChanged, entry.path)
	}
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil || !containedPath(root, resolved) || resolved != fullPath {
		return TrackedFile{}, fmt.Errorf("%w: tracked path is not a contained regular file", ErrUnsupportedTrackedFile)
	}
	if infoBefore.Size() > maximum {
		return TrackedFile{}, fmt.Errorf("%w: file %q bytes=%d maximum=%d", ErrInventoryLimit, entry.path, infoBefore.Size(), maximum)
	}
	file, err := rootHandle.Open(relativePath)
	if err != nil {
		return TrackedFile{}, fmt.Errorf("%w: open %q: %v", ErrTrackedFileChanged, entry.path, err)
	}
	openedBefore, err := file.Stat()
	if err != nil || !openedBefore.Mode().IsRegular() || !os.SameFile(infoBefore, openedBefore) {
		_ = file.Close()
		return TrackedFile{}, fmt.Errorf("%w: %s", ErrTrackedFileChanged, entry.path)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || int64(len(content)) > maximum {
		return TrackedFile{}, fmt.Errorf("%w: read %q", ErrInventoryLimit, entry.path)
	}
	infoAfter, err := rootHandle.Lstat(relativePath)
	resolvedAfter, resolveErr := filepath.EvalSymlinks(fullPath)
	if err != nil || resolveErr != nil || resolvedAfter != fullPath || !infoAfter.Mode().IsRegular() ||
		!openedAfter.Mode().IsRegular() || !os.SameFile(openedBefore, openedAfter) || !os.SameFile(openedAfter, infoAfter) ||
		infoBefore.Size() != infoAfter.Size() || openedBefore.Size() != openedAfter.Size() ||
		!infoBefore.ModTime().Equal(infoAfter.ModTime()) || !openedBefore.ModTime().Equal(openedAfter.ModTime()) {
		return TrackedFile{}, fmt.Errorf("%w: %s", ErrTrackedFileChanged, entry.path)
	}
	digest := sha256.Sum256(content)
	return TrackedFile{Path: entry.path, Mode: entry.mode, Bytes: content, SHA256: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func canonicalCheckout(checkout string) (string, error) {
	absolute, err := filepath.Abs(checkout)
	if err != nil {
		return "", fmt.Errorf("%w: resolve checkout: %v", ErrCheckoutRoot, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: resolve checkout: %v", ErrCheckoutRoot, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: checkout is not a directory", ErrCheckoutRoot)
	}
	return filepath.Clean(resolved), nil
}

func containedPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validateInventoryLimits(limits InventoryLimits) error {
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 {
		return fmt.Errorf("%w: limits must be positive", ErrInventoryLimit)
	}
	return nil
}

func lowerHexOfLength(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		validLength = validLength || len(value) == length
	}
	if !validLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = gitEnvironment()
	stdout := &boundedOutput{limit: maxGitOutputBytes}
	stderr := &boundedOutput{limit: 4096}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, sanitizeDiagnostic(stderr.String()))
	}
	if stdout.truncated {
		return nil, fmt.Errorf("%w: Git output exceeded %d bytes", ErrInventoryLimit, maxGitOutputBytes)
	}
	return stdout.Bytes(), nil
}

func gitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+6)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "GIT_") || name == "LC_ALL" {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
}

type boundedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = output.buffer.Write(value)
	}
	if originalLength > remaining {
		output.truncated = true
	}
	return originalLength, nil
}

func (output *boundedOutput) Bytes() []byte  { return output.buffer.Bytes() }
func (output *boundedOutput) String() string { return output.buffer.String() }

func sanitizeDiagnostic(message string) string {
	message = strings.Map(func(value rune) rune {
		if value == '\n' || value == '\r' || value == '\t' || value < 0x20 || value == 0x7f {
			return ' '
		}
		return value
	}, message)
	return strings.TrimSpace(message)
}
