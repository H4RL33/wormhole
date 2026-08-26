//go:build linux || darwin

package localidentity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func openLocalIdentityRoot(root string) (string, int, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return "", -1, fmt.Errorf("%w: root must be a non-root absolute path", ErrUnsafeStore)
	}
	canonical := filepath.Clean(root)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", -1, err
	}
	components := strings.Split(strings.TrimPrefix(canonical, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" || component == "." {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return "", -1, fmt.Errorf("localidentity: create root component %q: %w", component, mkdirErr)
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return "", -1, fmt.Errorf("%w: root component %q", ErrUnsafeStore, component)
		}
		var directory unix.Stat_t
		if statErr := unix.Fstat(next, &directory); statErr != nil || directory.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(next)
			if statErr != nil {
				return "", -1, statErr
			}
			return "", -1, fmt.Errorf("%w: root component %q is not a directory", ErrUnsafeStore, component)
		}
		if index == len(components)-1 && (directory.Uid != uint32(os.Geteuid()) || directory.Mode&0o7777 != 0o700) {
			_ = unix.Close(next)
			return "", -1, fmt.Errorf("%w: root ownership or mode", ErrUnsafeStore)
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return "", -1, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o700 {
		_ = unix.Close(fd)
		return "", -1, fmt.Errorf("%w: root ownership or mode", ErrUnsafeStore)
	}
	return canonical, fd, nil
}

func closeLocalIdentityFD(fd int) error { return unix.Close(fd) }

func lockLocalIdentityStore(ctx context.Context, rootFD int) (func(), error) {
	fd, err := unix.Openat(rootFD, lockRecordName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("localidentity: open store lock: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: store lock", ErrUnsafeStore)
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		if err := revalidateLocalIdentityLock(rootFD, fd); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if err := revalidateLocalIdentityLock(rootFD, fd); err != nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
				return nil, err
			}
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("localidentity: lock store: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func revalidateLocalIdentityLock(rootFD, lockFD int) error {
	var opened, linked unix.Stat_t
	if err := unix.Fstat(lockFD, &opened); err != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Uid != uint32(os.Geteuid()) || opened.Mode&0o7777 != 0o600 || opened.Nlink != 1 {
		return fmt.Errorf("%w: store lock descriptor", ErrUnsafeStore)
	}
	if err := unix.Fstatat(rootFD, lockRecordName, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil || linked.Mode&unix.S_IFMT != unix.S_IFREG || linked.Uid != uint32(os.Geteuid()) || linked.Mode&0o7777 != 0o600 || linked.Nlink != 1 || opened.Dev != linked.Dev || opened.Ino != linked.Ino {
		return fmt.Errorf("%w: store lock entry changed", ErrUnsafeStore)
	}
	return nil
}

func readLocalIdentityFile(rootFD int, name string) ([]byte, bool, error) {
	if !validLocalIdentityName(name) {
		return nil, false, fmt.Errorf("%w: invalid record name", ErrUnsafeStore)
	}
	var linked unix.Stat_t
	if err := unix.Fstatat(rootFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if linked.Mode&unix.S_IFMT != unix.S_IFREG || linked.Uid != uint32(os.Geteuid()) || linked.Mode&0o7777 != 0o600 || linked.Nlink != 1 {
		return nil, false, fmt.Errorf("%w: record type, ownership, or mode", ErrUnsafeStore)
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, fmt.Errorf("%w: open record", ErrUnsafeStore)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, errors.New("localidentity: open record handle")
	}
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, false, err
	}
	if opened.Dev != linked.Dev || opened.Ino != linked.Ino || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Uid != uint32(os.Geteuid()) || opened.Mode&0o7777 != 0o600 || opened.Nlink != 1 {
		return nil, false, fmt.Errorf("%w: record changed before read", ErrUnsafeStore)
	}
	if opened.Size < 0 || opened.Size > maxLocalIdentityRecordBytes {
		return nil, false, fmt.Errorf("%w: record exceeds size limit", ErrInvalidStoreRecord)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLocalIdentityRecordBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxLocalIdentityRecordBytes {
		return nil, false, fmt.Errorf("%w: record exceeds size limit", ErrInvalidStoreRecord)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, false, err
	}
	if after.Dev != opened.Dev || after.Ino != opened.Ino || after.Size != opened.Size || after.Nlink != 1 {
		return nil, false, fmt.Errorf("%w: record changed during read", ErrUnsafeStore)
	}
	return data, true, nil
}

func atomicLocalIdentityWrite(rootFD int, name string, content []byte, mode fs.FileMode, replace bool, random func([]byte) (int, error)) error {
	if !validLocalIdentityName(name) || mode.Perm() != 0o600 {
		return fmt.Errorf("%w: unsafe record publication", ErrUnsafeStore)
	}
	temporary, err := localIdentityTemporaryName(random)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(rootFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = unix.Close(fd)
		if cleanup {
			_ = unix.Unlinkat(rootFD, temporary, 0)
		}
	}()
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return err
	}
	for remaining := content; len(remaining) > 0; {
		written, writeErr := unix.Write(fd, remaining)
		if writeErr != nil {
			return writeErr
		}
		remaining = remaining[written:]
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	if err := unix.Close(fd); err != nil {
		return err
	}
	fd = -1
	if replace {
		if err := unix.Renameat(rootFD, temporary, rootFD, name); err != nil {
			return err
		}
	} else if err := commitLocalIdentityNoReplace(rootFD, temporary, rootFD, name); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%w: record already exists", ErrSetupIdentityDrift)
		}
		return err
	}
	cleanup = false
	if err := unix.Fsync(rootFD); err != nil {
		return err
	}
	return nil
}

func localIdentityTemporaryName(random func([]byte) (int, error)) (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(randomReader{read: random}, bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf(".tmp-%x", bytes), nil
}

func validLocalIdentityName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}

func listLocalIdentityRecordNames(rootFD int) ([]string, error) {
	duplicated, err := unix.Dup(rootFD)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicated), "localidentity-root")
	if directory == nil {
		_ = unix.Close(duplicated)
		return nil, errors.New("localidentity: open root directory handle")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maxLocalIdentityStoreEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxLocalIdentityStoreEntries {
		return nil, fmt.Errorf("%w: root entry limit", ErrInvalidStoreRecord)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validLocalIdentityName(entry.Name()) {
			return nil, fmt.Errorf("%w: invalid root entry", ErrUnsafeStore)
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
