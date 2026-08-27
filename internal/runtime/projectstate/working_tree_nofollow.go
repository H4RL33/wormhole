package projectstate

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrUnsafeWorkingTree                = errors.New("projectstate: unsafe working tree")
	ErrWorkingTreeLimit                 = errors.New("projectstate: working tree limit exceeded")
	ErrWorkingTreeChanged               = errors.New("projectstate: working tree changed")
	ErrWorkingTreeFilesystemUnsupported = errors.New("projectstate: secure working-tree reads unsupported")
)

const (
	maxWorkingTreeFiles       = 10_000
	maxWorkingTreeDirectories = 10_016
	maxWorkingTreePathBytes   = 4 << 10
	maxWorkingTreePathDepth   = 5
	maxWorkingTreeFileBytes   = int64(16 << 20)
	maxWorkingTreeTotalBytes  = int64(64 << 20)
)

type workingTreeLimits struct {
	maxFiles       int
	maxDirectories int
	maxPathBytes   int
	maxPathDepth   int
	maxFileBytes   int64
	maxTotalBytes  int64
}

func defaultWorkingTreeLimits() workingTreeLimits {
	return workingTreeLimits{
		maxFiles: maxWorkingTreeFiles, maxDirectories: maxWorkingTreeDirectories,
		maxPathBytes: maxWorkingTreePathBytes, maxPathDepth: maxWorkingTreePathDepth,
		maxFileBytes: maxWorkingTreeFileBytes, maxTotalBytes: maxWorkingTreeTotalBytes,
	}
}

type workingTreeReadStage uint8

const (
	workingTreeAfterEntryStat workingTreeReadStage = iota + 1
	workingTreeAfterFileOpen
	workingTreeAfterFileRead
	workingTreeBeforeDirectoryRecheck
	workingTreeBeforeAbsentRecheck
)

type workingTreeReadHook func(stage workingTreeReadStage, relativePath string) error

// ReadWorkingTreeNoFollow captures exact bytes beneath root/.wormhole without
// following symbolic links. Paths in the returned tree are relative to
// .wormhole and sorted bytewise.
func ReadWorkingTreeNoFollow(root string) (state.Tree, error) {
	return readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), nil)
}

func readWorkingTreeNoFollow(root string, limits workingTreeLimits, hook workingTreeReadHook) (state.Tree, error) {
	if err := validateWorkingTreeLimits(limits); err != nil {
		return nil, err
	}
	return readWorkingTreeNoFollowPlatform(root, limits, hook)
}

func validateWorkingTreeLimits(limits workingTreeLimits) error {
	if limits.maxFiles <= 0 || limits.maxDirectories <= 0 || limits.maxPathBytes <= 0 ||
		limits.maxPathDepth <= 0 || limits.maxFileBytes <= 0 || limits.maxTotalBytes <= 0 {
		return fmt.Errorf("%w: limits must be positive", ErrWorkingTreeLimit)
	}
	return nil
}

func validateWorkingTreeRelativePath(relativePath string, limits workingTreeLimits) error {
	if len(relativePath) > limits.maxPathBytes {
		return fmt.Errorf("%w: path %q exceeds %d bytes", ErrWorkingTreeLimit, relativePath, limits.maxPathBytes)
	}
	if relativePath == "" || !utf8.ValidString(relativePath) ||
		strings.Contains(relativePath, "\\") || strings.HasPrefix(relativePath, "/") ||
		path.Clean(relativePath) != relativePath || relativePath == "." || relativePath == ".." ||
		strings.HasPrefix(relativePath, "../") {
		return fmt.Errorf("%w: invalid path %q", ErrUnsafeWorkingTree, relativePath)
	}
	if strings.Count(relativePath, "/")+1 > limits.maxPathDepth {
		return fmt.Errorf("%w: path depth for %q", ErrWorkingTreeLimit, relativePath)
	}
	for _, character := range relativePath {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: control character in path %q", ErrUnsafeWorkingTree, relativePath)
		}
	}
	return nil
}

func workingTreeChangedIOError(operation, relativePath string, cause error, unsafe bool) error {
	if unsafe {
		return fmt.Errorf("%w: %w: %s %q: %w", ErrWorkingTreeChanged, ErrUnsafeWorkingTree, operation, relativePath, cause)
	}
	return fmt.Errorf("%w: %s %q: %w", ErrWorkingTreeChanged, operation, relativePath, cause)
}
