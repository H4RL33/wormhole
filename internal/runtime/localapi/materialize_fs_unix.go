//go:build linux || darwin

package localapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type materializationIdentity struct {
	device uint64
	inode  uint64
}

type heldMaterializationDirectory struct {
	fd       int
	identity materializationIdentity
	path     string
	parentFD int
	name     string
}

type materializationRootHandle struct {
	root     heldMaterializationDirectory
	ancestry []heldMaterializationDirectory
}

type materializationParentHandle struct {
	root       *materializationRootHandle
	held       []heldMaterializationDirectory
	created    []string
	missing    bool
	targetName string
}

func openMaterializationRoot(root string) (*materializationRootHandle, error) {
	clean := filepath.Clean(root)
	if clean == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: refusing filesystem root as project root", ErrUnsafeIntegrationPath)
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	rootIdentity, rootMode, _, err := materializationFstat(fd)
	if err != nil || rootMode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: filesystem root is not a directory", ErrUnsafeIntegrationPath)
	}
	ancestry := []heldMaterializationDirectory{{fd: fd, identity: rootIdentity, path: string(filepath.Separator), parentFD: -1}}
	currentPath := ""
	for _, component := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			for index := len(ancestry) - 1; index >= 0; index-- {
				_ = unix.Close(ancestry[index].fd)
			}
			return nil, fmt.Errorf("%w: open project root component: %v", ErrUnsafeIntegrationPath, openErr)
		}
		identity, mode, _, statErr := materializationFstat(next)
		if statErr != nil || mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(next)
			for index := len(ancestry) - 1; index >= 0; index-- {
				_ = unix.Close(ancestry[index].fd)
			}
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("%w: project root component is not a directory", ErrUnsafeIntegrationPath)
		}
		currentPath += "/" + component
		ancestry = append(ancestry, heldMaterializationDirectory{fd: next, identity: identity, path: currentPath, parentFD: fd, name: component})
		fd = next
	}
	return &materializationRootHandle{root: ancestry[len(ancestry)-1], ancestry: ancestry}, nil
}

func (root *materializationRootHandle) close() {
	if root == nil {
		return
	}
	for index := len(root.ancestry) - 1; index >= 0; index-- {
		_ = unix.Close(root.ancestry[index].fd)
	}
	root.ancestry = nil
	root.root.fd = -1
}

func (root *materializationRootHandle) openParent(target string, create bool) (*materializationParentHandle, error) {
	parts := strings.Split(target, "/")
	parent := &materializationParentHandle{root: root, targetName: parts[len(parts)-1]}
	current := root.root.fd
	for index, component := range parts[:len(parts)-1] {
		relative := strings.Join(parts[:index+1], "/")
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				parent.close()
				return nil, fmt.Errorf("create managed directory %q: %w", relative, mkdirErr)
			}
			next, err = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err == nil {
				parent.created = append(parent.created, relative)
			}
		}
		if errors.Is(err, unix.ENOENT) && !create {
			parent.missing = true
			return parent, nil
		}
		if err != nil {
			parent.close()
			return nil, fmt.Errorf("%w: open managed ancestor %q: %v", ErrUnsafeIntegrationPath, relative, err)
		}
		identity, mode, _, statErr := materializationFstat(next)
		if statErr != nil || mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(next)
			parent.close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("%w: managed ancestor %q is not a directory", ErrUnsafeIntegrationPath, relative)
		}
		parent.held = append(parent.held, heldMaterializationDirectory{fd: next, identity: identity, path: relative, parentFD: current, name: component})
		current = next
	}
	return parent, nil
}

func (parent *materializationParentHandle) close() {
	for index := len(parent.held) - 1; index >= 0; index-- {
		_ = unix.Close(parent.held[index].fd)
	}
	parent.held = nil
}

func (parent *materializationParentHandle) fd() int {
	if len(parent.held) == 0 {
		return parent.root.root.fd
	}
	return parent.held[len(parent.held)-1].fd
}

func (parent *materializationParentHandle) revalidate() error {
	for _, held := range parent.root.ancestry {
		if err := revalidateMaterializationDirectory(held); err != nil {
			return err
		}
	}
	for _, held := range parent.held {
		if err := revalidateMaterializationDirectory(held); err != nil {
			return err
		}
	}
	return nil
}

func revalidateMaterializationDirectory(held heldMaterializationDirectory) error {
	identity, mode, _, err := materializationFstat(held.fd)
	if err != nil {
		return err
	}
	if identity != held.identity || mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: managed directory identity changed at %q", ErrUnsafeIntegrationPath, held.path)
	}
	if held.parentFD >= 0 {
		var linked unix.Stat_t
		if err := unix.Fstatat(held.parentFD, held.name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%w: managed directory link changed at %q: %v", ErrUnsafeIntegrationPath, held.path, err)
		}
		linkedIdentity := materializationIdentity{device: uint64(linked.Dev), inode: uint64(linked.Ino)}
		if linkedIdentity != held.identity || linked.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("%w: managed directory link identity changed at %q", ErrUnsafeIntegrationPath, held.path)
		}
	}
	return nil
}

func (parent *materializationParentHandle) read() ([]byte, fs.FileMode, IntegrationFileIdentity, bool, error) {
	if parent.missing {
		return nil, 0, IntegrationFileIdentity{}, false, nil
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(parent.fd(), parent.targetName, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, 0, IntegrationFileIdentity{}, false, nil
		}
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	if pathStat.Mode&unix.S_IFMT != unix.S_IFREG || pathStat.Nlink != 1 {
		return nil, 0, IntegrationFileIdentity{}, false, fmt.Errorf("%w: target %q must be a single-link regular file", ErrUnsafeIntegrationPath, parent.targetName)
	}
	fd, err := unix.Openat(parent.fd(), parent.targetName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, fmt.Errorf("%w: open target %q: %v", ErrUnsafeIntegrationPath, parent.targetName, err)
	}
	file := os.NewFile(uintptr(fd), parent.targetName)
	if file == nil {
		_ = unix.Close(fd)
		return nil, 0, IntegrationFileIdentity{}, false, errors.New("open managed target file handle")
	}
	defer file.Close()
	openedIdentity, openedMode, openedLinks, err := materializationFstat(fd)
	if err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	pathIdentity := materializationIdentity{device: uint64(pathStat.Dev), inode: uint64(pathStat.Ino)}
	if openedIdentity != pathIdentity || openedMode&unix.S_IFMT != unix.S_IFREG || openedLinks != 1 {
		return nil, 0, IntegrationFileIdentity{}, false, fmt.Errorf("%w: target identity changed before read", ErrUnsafeIntegrationPath)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	afterIdentity, afterMode, afterLinks, err := materializationFstat(fd)
	if err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	if afterIdentity != openedIdentity || afterMode != openedMode || afterLinks != 1 {
		return nil, 0, IntegrationFileIdentity{}, false, fmt.Errorf("%w: target identity changed during read", ErrUnsafeIntegrationPath)
	}
	if err := parent.revalidate(); err != nil {
		return nil, 0, IntegrationFileIdentity{}, false, err
	}
	return data, fs.FileMode(openedMode & 0o777), IntegrationFileIdentity{Device: openedIdentity.device, Inode: openedIdentity.inode}, true, nil
}

func (parent *materializationParentHandle) atomicWrite(expected []byte, expectedExists bool, content []byte, mode fs.FileMode, afterMutation func() error) (IntegrationFileIdentity, error) {
	current, _, _, exists, err := parent.read()
	if err != nil {
		return IntegrationFileIdentity{}, err
	}
	if exists != expectedExists || !bytes.Equal(current, expected) {
		return IntegrationFileIdentity{}, fmt.Errorf("%w: target changed before commit", ErrIntegrationDrift)
	}
	temporary, err := randomMaterializationTemporaryName()
	if err != nil {
		return IntegrationFileIdentity{}, err
	}
	fd, err := unix.Openat(parent.fd(), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return IntegrationFileIdentity{}, err
	}
	cleanup := true
	defer func() {
		_ = unix.Close(fd)
		if cleanup {
			_ = unix.Unlinkat(parent.fd(), temporary, 0)
		}
	}()
	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		return IntegrationFileIdentity{}, err
	}
	remaining := content
	for len(remaining) > 0 {
		written, writeErr := unix.Write(fd, remaining)
		if writeErr != nil {
			return IntegrationFileIdentity{}, writeErr
		}
		remaining = remaining[written:]
	}
	if err := unix.Fsync(fd); err != nil {
		return IntegrationFileIdentity{}, err
	}
	if err := parent.revalidate(); err != nil {
		return IntegrationFileIdentity{}, err
	}
	current, _, _, exists, err = parent.read()
	if err != nil {
		return IntegrationFileIdentity{}, err
	}
	if exists != expectedExists || !bytes.Equal(current, expected) {
		return IntegrationFileIdentity{}, fmt.Errorf("%w: target changed before rename", ErrIntegrationDrift)
	}
	if expectedExists {
		if err := materializationExchange(parent.fd(), temporary, parent.targetName); err != nil {
			return IntegrationFileIdentity{}, err
		}
		temporaryTarget := *parent
		temporaryTarget.targetName = temporary
		swapped, _, _, swappedExists, readErr := temporaryTarget.read()
		if readErr != nil || !swappedExists || !bytes.Equal(swapped, expected) {
			restoreErr := materializationExchange(parent.fd(), temporary, parent.targetName)
			if restoreErr != nil {
				return IntegrationFileIdentity{}, fmt.Errorf("%w: target exchange validation failed and restore failed: %v", ErrUnsafeIntegrationPath, restoreErr)
			}
			if readErr != nil {
				return IntegrationFileIdentity{}, readErr
			}
			return IntegrationFileIdentity{}, fmt.Errorf("%w: target changed during exchange", ErrIntegrationDrift)
		}
		if err := unix.Unlinkat(parent.fd(), temporary, 0); err != nil {
			_ = materializationExchange(parent.fd(), temporary, parent.targetName)
			return IntegrationFileIdentity{}, err
		}
	} else if err := materializationRenameNoReplace(parent.fd(), temporary, parent.targetName); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return IntegrationFileIdentity{}, fmt.Errorf("%w: target appeared during commit", ErrIntegrationDrift)
		}
		return IntegrationFileIdentity{}, err
	}
	cleanup = false
	if afterMutation != nil {
		if err := afterMutation(); err != nil {
			return IntegrationFileIdentity{}, err
		}
	}
	if err := unix.Fsync(parent.fd()); err != nil {
		return IntegrationFileIdentity{}, err
	}
	got, _, identity, exists, err := parent.read()
	if err != nil {
		return IntegrationFileIdentity{}, err
	}
	if !exists || !bytes.Equal(got, content) {
		return IntegrationFileIdentity{}, fmt.Errorf("%w: post-write validation failed", ErrUnsafeIntegrationPath)
	}
	return identity, nil
}

func (parent *materializationParentHandle) unlink(expected []byte, beforeUnlink func() error) error {
	current, _, _, exists, err := parent.read()
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(current, expected) {
		return fmt.Errorf("%w: target changed before removal", ErrIntegrationDrift)
	}
	if beforeUnlink != nil {
		if err := beforeUnlink(); err != nil {
			return err
		}
	}
	temporary, err := randomMaterializationTemporaryName()
	if err != nil {
		return err
	}
	if err := materializationRenameNoReplace(parent.fd(), parent.targetName, temporary); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%w: target changed before removal commit", ErrIntegrationDrift)
		}
		return err
	}
	moved := true
	defer func() {
		if moved {
			_ = materializationRenameNoReplace(parent.fd(), temporary, parent.targetName)
		}
	}()
	temporaryTarget := *parent
	temporaryTarget.targetName = temporary
	movedBytes, _, _, movedExists, err := temporaryTarget.read()
	if err != nil || !movedExists || !bytes.Equal(movedBytes, expected) {
		restoreErr := materializationRenameNoReplace(parent.fd(), temporary, parent.targetName)
		if restoreErr != nil {
			return fmt.Errorf("%w: removed target changed and restore failed: %v", ErrUnsafeIntegrationPath, restoreErr)
		}
		moved = false
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: target changed during removal commit", ErrIntegrationDrift)
	}
	if err := unix.Unlinkat(parent.fd(), temporary, 0); err != nil {
		return err
	}
	moved = false
	if err := unix.Fsync(parent.fd()); err != nil {
		return err
	}
	_, _, _, exists, err = parent.read()
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: target still exists after removal", ErrUnsafeIntegrationPath)
	}
	return parent.revalidate()
}

func (root *materializationRootHandle) removeCreatedDirectories(directories []string) {
	unique := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		if directory != "" {
			unique[directory] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(unique))
	for directory := range unique {
		ordered = append(ordered, directory)
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftDepth, rightDepth := strings.Count(ordered[i], "/"), strings.Count(ordered[j], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return ordered[i] > ordered[j]
	})
	for _, directory := range ordered {
		parent, err := root.openParent(directory, false)
		if err != nil || parent.missing {
			if parent != nil {
				parent.close()
			}
			continue
		}
		err = unix.Unlinkat(parent.fd(), parent.targetName, unix.AT_REMOVEDIR)
		if err == nil {
			_ = unix.Fsync(parent.fd())
		}
		parent.close()
	}
}

func materializationFstat(fd int) (materializationIdentity, uint32, uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return materializationIdentity{}, 0, 0, err
	}
	return materializationIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, uint32(stat.Mode), uint64(stat.Nlink), nil
}

func randomMaterializationTemporaryName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".wormhole-tmp-" + hex.EncodeToString(value[:]), nil
}
