//go:build linux || darwin

package projectstate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"golang.org/x/sys/unix"
)

type workingTreeMetadata struct {
	device     uint64
	inode      uint64
	mode       uint32
	links      uint64
	size       int64
	uid        uint32
	gid        uint32
	modifiedNS int64
	changedNS  int64
}

type heldWorkingTreeDirectory struct {
	fd       int
	parentFD int
	name     string
	path     string
	metadata workingTreeMetadata
}

type workingTreeRootHandle struct {
	ancestry []heldWorkingTreeDirectory
}

type workingTreeWalker struct {
	limits            workingTreeLimits
	hook              workingTreeReadHook
	files             state.Tree
	fileMetadata      map[string]workingTreeMetadata
	directoryMetadata map[string]workingTreeMetadata
	fileCount         int
	directoryCount    int
	totalBytes        int64
}

func readWorkingTreeNoFollowPlatform(root string, limits workingTreeLimits, hook workingTreeReadHook) (state.Tree, error) {
	handle, err := openWorkingTreeRoot(root)
	if err != nil {
		return nil, err
	}
	defer handle.close()

	checkout := handle.ancestry[len(handle.ancestry)-1]
	wormhole, exists, err := openWormholeDirectory(checkout.fd)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := handle.revalidate(); err != nil {
			return nil, err
		}
		if hook != nil {
			if err := hook(workingTreeBeforeAbsentRecheck, ".wormhole"); err != nil {
				return nil, err
			}
		}
		var appeared unix.Stat_t
		if err := unix.Fstatat(checkout.fd, ".wormhole", &appeared, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			if workingTreeStatMetadata(&appeared).mode&unix.S_IFMT != unix.S_IFDIR {
				return nil, fmt.Errorf("%w: %w: .wormhole appeared as a non-directory", ErrWorkingTreeChanged, ErrUnsafeWorkingTree)
			}
			return nil, fmt.Errorf("%w: .wormhole appeared during capture", ErrWorkingTreeChanged)
		} else if !errors.Is(err, unix.ENOENT) {
			return nil, workingTreeChangedIOError("recheck absent directory", ".wormhole", err, false)
		}
		if err := handle.revalidate(); err != nil {
			return nil, err
		}
		return make(state.Tree, 0), nil
	}
	defer unix.Close(wormhole.fd)

	walker := newWorkingTreeWalker(limits, hook)
	if err := walker.walkDirectory(wormhole, "."); err != nil {
		return nil, err
	}
	sort.Slice(walker.files, func(i, j int) bool { return walker.files[i].Path < walker.files[j].Path })
	if err := verifyWorkingTreeCapture(wormhole, limits, walker); err != nil {
		return nil, err
	}
	if err := revalidateWorkingTreeDirectory(wormhole); err != nil {
		return nil, err
	}
	if err := handle.revalidate(); err != nil {
		return nil, err
	}
	return walker.files, nil
}

func newWorkingTreeWalker(limits workingTreeLimits, hook workingTreeReadHook) workingTreeWalker {
	return workingTreeWalker{
		limits: limits, hook: hook, files: make(state.Tree, 0),
		fileMetadata:      make(map[string]workingTreeMetadata),
		directoryMetadata: make(map[string]workingTreeMetadata),
	}
}

func verifyWorkingTreeCapture(wormhole heldWorkingTreeDirectory, limits workingTreeLimits, expected workingTreeWalker) error {
	verification := newWorkingTreeWalker(limits, nil)
	if err := verification.walkDirectory(wormhole, "."); err != nil {
		return fmt.Errorf("%w: final capture verification: %w", ErrWorkingTreeChanged, err)
	}
	sort.Slice(verification.files, func(i, j int) bool { return verification.files[i].Path < verification.files[j].Path })
	if !sameWorkingTreeCapture(expected.files, verification.files) ||
		!sameWorkingTreeFileMetadata(expected.fileMetadata, verification.fileMetadata) ||
		!sameWorkingTreeDirectoryMetadata(expected.directoryMetadata, verification.directoryMetadata) {
		return fmt.Errorf("%w: final capture bytes or metadata differ", ErrWorkingTreeChanged)
	}
	return nil
}

func sameWorkingTreeCapture(left, right state.Tree) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}

func sameWorkingTreeFileMetadata(left, right map[string]workingTreeMetadata) bool {
	if len(left) != len(right) {
		return false
	}
	for relativePath, metadata := range left {
		if right[relativePath] != metadata {
			return false
		}
	}
	return true
}

func sameWorkingTreeDirectoryMetadata(left, right map[string]workingTreeMetadata) bool {
	if len(left) != len(right) {
		return false
	}
	for relativePath, metadata := range left {
		other, exists := right[relativePath]
		if !exists || !sameWorkingTreeDirectory(metadata, other) {
			return false
		}
	}
	return true
}

func openWorkingTreeRoot(root string) (*workingTreeRootHandle, error) {
	if root == "" || strings.ContainsAny(root, "\x00\r\n") {
		return nil, fmt.Errorf("%w: invalid checkout root", ErrUnsafeWorkingTree)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize checkout root: %w", ErrUnsafeWorkingTree, err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: filesystem root is not a checkout", ErrUnsafeWorkingTree)
	}

	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("projectstate: open filesystem root: %w", err)
	}
	metadata, err := workingTreeFstat(fd)
	if err != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: filesystem root is not a directory", ErrUnsafeWorkingTree)
	}
	handle := &workingTreeRootHandle{ancestry: []heldWorkingTreeDirectory{{
		fd: fd, parentFD: -1, path: string(filepath.Separator), metadata: metadata,
	}}}
	currentPath := ""
	for _, component := range strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		parent := handle.ancestry[len(handle.ancestry)-1]
		var linked unix.Stat_t
		if err := unix.Fstatat(parent.fd, component, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			handle.close()
			return nil, fmt.Errorf("projectstate: inspect checkout root component %q: %w", component, err)
		}
		linkedMetadata := workingTreeStatMetadata(&linked)
		if linkedMetadata.mode&unix.S_IFMT != unix.S_IFDIR {
			handle.close()
			return nil, fmt.Errorf("%w: checkout root component %q is not a directory", ErrUnsafeWorkingTree, component)
		}
		next, openErr := unix.Openat(parent.fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			handle.close()
			return nil, workingTreeChangedIOError("open checkout root component", component, openErr, unsafeWorkingTreeOpenFailure(openErr))
		}
		openedMetadata, statErr := workingTreeFstat(next)
		if statErr != nil || !sameWorkingTreeDirectoryIdentity(openedMetadata, linkedMetadata) {
			_ = unix.Close(next)
			handle.close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("%w: checkout root component %q changed while opening", ErrWorkingTreeChanged, component)
		}
		currentPath = filepath.Join(currentPath, component)
		handle.ancestry = append(handle.ancestry, heldWorkingTreeDirectory{
			fd: next, parentFD: parent.fd, name: component, path: currentPath, metadata: openedMetadata,
		})
	}
	return handle, nil
}

func (handle *workingTreeRootHandle) close() {
	if handle == nil {
		return
	}
	for index := len(handle.ancestry) - 1; index >= 0; index-- {
		_ = unix.Close(handle.ancestry[index].fd)
	}
	handle.ancestry = nil
}

func (handle *workingTreeRootHandle) revalidate() error {
	for _, directory := range handle.ancestry {
		if err := revalidateWorkingTreeDirectoryIdentity(directory); err != nil {
			return err
		}
	}
	return nil
}

func openWormholeDirectory(checkoutFD int) (heldWorkingTreeDirectory, bool, error) {
	var linked unix.Stat_t
	if err := unix.Fstatat(checkoutFD, ".wormhole", &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return heldWorkingTreeDirectory{}, false, nil
		}
		return heldWorkingTreeDirectory{}, false, fmt.Errorf("projectstate: inspect .wormhole: %w", err)
	}
	linkedMetadata := workingTreeStatMetadata(&linked)
	if linkedMetadata.mode&unix.S_IFMT != unix.S_IFDIR {
		return heldWorkingTreeDirectory{}, false, fmt.Errorf("%w: .wormhole is not a directory", ErrUnsafeWorkingTree)
	}
	fd, err := unix.Openat(checkoutFD, ".wormhole", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return heldWorkingTreeDirectory{}, false, workingTreeChangedIOError("open directory", ".wormhole", err, unsafeWorkingTreeOpenFailure(err))
	}
	openedMetadata, err := workingTreeFstat(fd)
	if err != nil || !sameWorkingTreeDirectory(openedMetadata, linkedMetadata) {
		_ = unix.Close(fd)
		if err != nil {
			return heldWorkingTreeDirectory{}, false, err
		}
		return heldWorkingTreeDirectory{}, false, fmt.Errorf("%w: .wormhole changed while opening", ErrWorkingTreeChanged)
	}
	return heldWorkingTreeDirectory{
		fd: fd, parentFD: checkoutFD, name: ".wormhole", path: ".wormhole", metadata: openedMetadata,
	}, true, nil
}

func (walker *workingTreeWalker) walkDirectory(directory heldWorkingTreeDirectory, relativePath string) error {
	names, overflow, err := workingTreeDirectoryNames(directory.fd, walker.remainingEntryCapacity())
	if err != nil {
		return fmt.Errorf("projectstate: enumerate working-tree directory %q: %w", relativePath, err)
	}
	if overflow {
		return fmt.Errorf("%w: directory inventory exceeds remaining entry capacity at %q", ErrWorkingTreeLimit, relativePath)
	}
	for _, name := range names {
		childPath := name
		if relativePath != "." {
			childPath = relativePath + "/" + name
		}
		if err := validateWorkingTreeRelativePath(childPath, walker.limits); err != nil {
			return err
		}
		var linked unix.Stat_t
		if err := unix.Fstatat(directory.fd, name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return workingTreeChangedIOError("inspect entry", childPath, err, false)
		}
		linkedMetadata := workingTreeStatMetadata(&linked)
		if walker.hook != nil {
			if err := walker.hook(workingTreeAfterEntryStat, childPath); err != nil {
				return err
			}
		}
		switch linkedMetadata.mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := walker.walkChildDirectory(directory.fd, name, childPath, linkedMetadata); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := walker.readFile(directory.fd, name, childPath, linkedMetadata); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %q is not a regular file or directory", ErrUnsafeWorkingTree, childPath)
		}
	}
	if walker.hook != nil {
		if err := walker.hook(workingTreeBeforeDirectoryRecheck, relativePath); err != nil {
			return err
		}
	}
	after, overflow, err := workingTreeDirectoryNames(directory.fd, len(names))
	if err != nil {
		return workingTreeChangedIOError("re-enumerate directory", relativePath, err, false)
	}
	if overflow {
		return fmt.Errorf("%w: directory inventory grew at %q", ErrWorkingTreeChanged, relativePath)
	}
	if !reflect.DeepEqual(names, after) {
		return fmt.Errorf("%w: directory inventory changed at %q", ErrWorkingTreeChanged, relativePath)
	}
	if err := revalidateWorkingTreeDirectory(directory); err != nil {
		return err
	}
	metadata, err := workingTreeFstat(directory.fd)
	if err != nil {
		return workingTreeChangedIOError("record directory metadata", relativePath, err, false)
	}
	walker.directoryMetadata[relativePath] = metadata
	return nil
}

func (walker *workingTreeWalker) remainingEntryCapacity() int {
	remaining := walker.limits.maxFiles - walker.fileCount + walker.limits.maxDirectories - walker.directoryCount
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (walker *workingTreeWalker) walkChildDirectory(parentFD int, name, relativePath string, linked workingTreeMetadata) error {
	walker.directoryCount++
	if walker.directoryCount > walker.limits.maxDirectories {
		return fmt.Errorf("%w: directory count exceeds %d", ErrWorkingTreeLimit, walker.limits.maxDirectories)
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return workingTreeChangedIOError("open directory", relativePath, err, unsafeWorkingTreeOpenFailure(err))
	}
	child := heldWorkingTreeDirectory{fd: fd, parentFD: parentFD, name: name, path: relativePath, metadata: linked}
	defer unix.Close(fd)
	opened, err := workingTreeFstat(fd)
	if err != nil {
		return err
	}
	if !sameWorkingTreeDirectory(opened, linked) || opened.mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: directory %q changed while opening", ErrWorkingTreeChanged, relativePath)
	}
	if err := walker.walkDirectory(child, relativePath); err != nil {
		return err
	}
	return revalidateWorkingTreeDirectory(child)
}

func (walker *workingTreeWalker) readFile(parentFD int, name, relativePath string, linked workingTreeMetadata) error {
	if linked.links != 1 {
		return fmt.Errorf("%w: file %q must have exactly one link", ErrUnsafeWorkingTree, relativePath)
	}
	walker.fileCount++
	if walker.fileCount > walker.limits.maxFiles {
		return fmt.Errorf("%w: file count exceeds %d", ErrWorkingTreeLimit, walker.limits.maxFiles)
	}
	if linked.size < 0 || linked.size > walker.limits.maxFileBytes {
		return fmt.Errorf("%w: file %q exceeds per-file limit", ErrWorkingTreeLimit, relativePath)
	}
	remaining := walker.limits.maxTotalBytes - walker.totalBytes - int64(len(relativePath))
	if remaining < 0 || linked.size > remaining {
		return fmt.Errorf("%w: aggregate bytes exceed %d", ErrWorkingTreeLimit, walker.limits.maxTotalBytes)
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return workingTreeChangedIOError("open file", relativePath, err, unsafeWorkingTreeOpenFailure(err))
	}
	file := os.NewFile(uintptr(fd), relativePath)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("projectstate: create file handle for %q", relativePath)
	}
	defer file.Close()
	opened, err := workingTreeFstat(fd)
	if err != nil {
		return err
	}
	if opened.mode&unix.S_IFMT != unix.S_IFREG || opened.links != 1 {
		return fmt.Errorf("%w: %w: file %q became non-regular or multiply linked", ErrWorkingTreeChanged, ErrUnsafeWorkingTree, relativePath)
	}
	if opened != linked {
		return fmt.Errorf("%w: file %q changed while opening", ErrWorkingTreeChanged, relativePath)
	}
	infoBefore, err := file.Stat()
	if err != nil {
		return err
	}
	if walker.hook != nil {
		if err := walker.hook(workingTreeAfterFileOpen, relativePath); err != nil {
			return err
		}
	}
	data, err := readWorkingTreeFile(file, walker.limits.maxFileBytes)
	if err != nil {
		return fmt.Errorf("projectstate: read working-tree file %q: %w", relativePath, err)
	}
	if int64(len(data)) > remaining {
		return fmt.Errorf("%w: aggregate bytes exceed %d", ErrWorkingTreeLimit, walker.limits.maxTotalBytes)
	}
	if walker.hook != nil {
		if err := walker.hook(workingTreeAfterFileRead, relativePath); err != nil {
			return err
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	confirmation, err := readWorkingTreeFile(file, walker.limits.maxFileBytes)
	if err != nil {
		return workingTreeChangedIOError("confirm file", relativePath, err, false)
	}
	if !bytes.Equal(data, confirmation) {
		return fmt.Errorf("%w: file contents changed at %q", ErrWorkingTreeChanged, relativePath)
	}
	infoAfter, err := file.Stat()
	if err != nil {
		return err
	}
	after, err := workingTreeFstat(fd)
	if err != nil {
		return err
	}
	if after.mode&unix.S_IFMT != unix.S_IFREG || after.links != 1 {
		return fmt.Errorf("%w: %w: file %q became non-regular or multiply linked during read", ErrWorkingTreeChanged, ErrUnsafeWorkingTree, relativePath)
	}
	if opened != after || !os.SameFile(infoBefore, infoAfter) || infoBefore.Mode() != infoAfter.Mode() ||
		infoBefore.Size() != infoAfter.Size() || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		return fmt.Errorf("%w: file metadata changed at %q", ErrWorkingTreeChanged, relativePath)
	}
	var linkedAfter unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &linkedAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return workingTreeChangedIOError("recheck file", relativePath, err, false)
	}
	pathAfter := workingTreeStatMetadata(&linkedAfter)
	if pathAfter != linked {
		if pathAfter.mode&unix.S_IFMT != unix.S_IFREG || pathAfter.links != 1 {
			return fmt.Errorf("%w: %w: file path became unsafe at %q", ErrWorkingTreeChanged, ErrUnsafeWorkingTree, relativePath)
		}
		return fmt.Errorf("%w: file link changed at %q", ErrWorkingTreeChanged, relativePath)
	}
	walker.totalBytes += int64(len(relativePath)) + int64(len(data))
	walker.files = append(walker.files, state.File{Path: relativePath, Data: data})
	walker.fileMetadata[relativePath] = after
	return nil
}

func workingTreeDirectoryNames(fd, maximum int) ([]string, bool, error) {
	if maximum < 0 {
		return nil, false, fmt.Errorf("%w: invalid directory-entry limit", ErrWorkingTreeLimit)
	}
	duplicate, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(duplicate), ".")
	if file == nil {
		_ = unix.Close(duplicate)
		return nil, false, errors.New("create directory handle")
	}
	names, readErr := file.Readdirnames(maximum + 1)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	if len(names) > maximum {
		return names, true, nil
	}
	if names == nil {
		names = make([]string, 0)
	}
	sort.Strings(names)
	return names, false, nil
}

func readWorkingTreeFile(file *os.File, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, ErrWorkingTreeLimit
	}
	return data, nil
}

func revalidateWorkingTreeDirectory(directory heldWorkingTreeDirectory) error {
	opened, err := workingTreeFstat(directory.fd)
	if err != nil {
		return workingTreeChangedIOError("stat directory", directory.path, err, false)
	}
	if !sameWorkingTreeDirectory(opened, directory.metadata) || opened.mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: directory metadata changed at %q", ErrWorkingTreeChanged, directory.path)
	}
	if directory.parentFD < 0 {
		return nil
	}
	var linked unix.Stat_t
	if err := unix.Fstatat(directory.parentFD, directory.name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return workingTreeChangedIOError("recheck directory", directory.path, err, false)
	}
	linkedMetadata := workingTreeStatMetadata(&linked)
	if !sameWorkingTreeDirectory(linkedMetadata, directory.metadata) {
		if linkedMetadata.mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("%w: %w: directory path became unsafe at %q", ErrWorkingTreeChanged, ErrUnsafeWorkingTree, directory.path)
		}
		return fmt.Errorf("%w: directory link changed at %q", ErrWorkingTreeChanged, directory.path)
	}
	return nil
}

func workingTreeFstat(fd int) (workingTreeMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return workingTreeMetadata{}, err
	}
	return workingTreeStatMetadata(&stat), nil
}

func workingTreeStatMetadata(stat *unix.Stat_t) workingTreeMetadata {
	return workingTreeMetadata{
		device: uint64(stat.Dev), inode: uint64(stat.Ino), mode: uint32(stat.Mode),
		links: uint64(stat.Nlink), size: stat.Size, uid: stat.Uid, gid: stat.Gid,
		modifiedNS: unix.TimespecToNsec(stat.Mtim), changedNS: unix.TimespecToNsec(stat.Ctim),
	}
}

func sameWorkingTreeDirectory(left, right workingTreeMetadata) bool {
	return left.device == right.device && left.inode == right.inode && left.mode == right.mode &&
		left.uid == right.uid && left.gid == right.gid && left.modifiedNS == right.modifiedNS &&
		left.changedNS == right.changedNS
}

func sameWorkingTreeDirectoryIdentity(left, right workingTreeMetadata) bool {
	return left.device == right.device && left.inode == right.inode &&
		left.mode&unix.S_IFMT == unix.S_IFDIR && right.mode&unix.S_IFMT == unix.S_IFDIR
}

func unsafeWorkingTreeOpenFailure(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EISDIR)
}

func revalidateWorkingTreeDirectoryIdentity(directory heldWorkingTreeDirectory) error {
	opened, err := workingTreeFstat(directory.fd)
	if err != nil {
		return workingTreeChangedIOError("stat checkout ancestor", directory.path, err, false)
	}
	if !sameWorkingTreeDirectoryIdentity(opened, directory.metadata) {
		return fmt.Errorf("%w: checkout ancestor identity changed at %q", ErrWorkingTreeChanged, directory.path)
	}
	if directory.parentFD < 0 {
		return nil
	}
	var linked unix.Stat_t
	if err := unix.Fstatat(directory.parentFD, directory.name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return workingTreeChangedIOError("recheck checkout ancestor", directory.path, err, false)
	}
	linkedMetadata := workingTreeStatMetadata(&linked)
	if !sameWorkingTreeDirectoryIdentity(linkedMetadata, directory.metadata) {
		if linkedMetadata.mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("%w: %w: checkout ancestor became unsafe at %q", ErrWorkingTreeChanged, ErrUnsafeWorkingTree, directory.path)
		}
		return fmt.Errorf("%w: checkout ancestor link changed at %q", ErrWorkingTreeChanged, directory.path)
	}
	return nil
}
