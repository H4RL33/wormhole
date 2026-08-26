//go:build linux

package config

import (
	"bytes"
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

func openSetupJournalRoot(root string) (string, int, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return "", -1, ErrUnsafeSetupJournalStore
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", -1, err
	}
	components := strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(current)
			return "", -1, ErrUnsafeSetupJournalStore
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return "", -1, fmt.Errorf("config: create setup journal root: %w", mkdirErr)
			}
			if syncErr := unix.Fsync(current); syncErr != nil {
				_ = unix.Close(current)
				return "", -1, syncErr
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			return "", -1, ErrUnsafeSetupJournalStore
		}
		_ = unix.Close(current)
		current = next
		var stat unix.Stat_t
		if err := unix.Fstat(current, &stat); err != nil {
			_ = unix.Close(current)
			return "", -1, err
		}
		final := index == len(components)-1
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || !trustedSetupJournalDirectory(stat, final) {
			_ = unix.Close(current)
			return "", -1, ErrUnsafeSetupJournalStore
		}
	}
	return root, current, nil
}

func trustedSetupJournalDirectory(stat unix.Stat_t, final bool) bool {
	uid := uint32(os.Geteuid())
	if final {
		return stat.Uid == uid && stat.Mode&0o7777 == 0o700
	}
	if stat.Uid != 0 && stat.Uid != uid {
		return false
	}
	if stat.Mode&0o022 == 0 {
		return true
	}
	return stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0
}

func closeSetupJournalFD(fd int) error { return unix.Close(fd) }

func lockSetupJournalStore(ctx context.Context, rootFD int) (func(), error) {
	fd, err := unix.Openat(rootFD, setupJournalLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, ErrUnsafeSetupJournalStore
	}
	if err := revalidateSetupJournalLock(rootFD, fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			if err := revalidateSetupJournalLock(rootFD, fd); err != nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
				return nil, err
			}
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
			}, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func revalidateSetupJournalLock(rootFD, lockFD int) error {
	var opened, linked unix.Stat_t
	if err := unix.Fstat(lockFD, &opened); err != nil || !validSetupJournalRegular(opened, 0) {
		return ErrUnsafeSetupJournalStore
	}
	if err := unix.Fstatat(rootFD, setupJournalLockName, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil || !validSetupJournalRegular(linked, 0) || opened.Dev != linked.Dev || opened.Ino != linked.Ino {
		return ErrUnsafeSetupJournalStore
	}
	return nil
}

func readSetupJournalFile(rootFD int, name string) ([]byte, bool, error) {
	if !validSetupJournalName(name) {
		return nil, false, ErrUnsafeSetupJournalStore
	}
	var linked unix.Stat_t
	if err := unix.Fstatat(rootFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !validSetupJournalRegular(linked, maxSetupJournalRecordBytes) {
		return nil, false, ErrUnsafeSetupJournalStore
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, ErrUnsafeSetupJournalStore
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, ErrUnsafeSetupJournalStore
	}
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !validSetupJournalRegular(opened, maxSetupJournalRecordBytes) || opened.Dev != linked.Dev || opened.Ino != linked.Ino || opened.Size != linked.Size {
		return nil, false, ErrUnsafeSetupJournalStore
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSetupJournalRecordBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxSetupJournalRecordBytes {
		return nil, false, ErrInvalidSetupJournal
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || after.Dev != opened.Dev || after.Ino != opened.Ino || after.Size != opened.Size || after.Nlink != 1 {
		return nil, false, ErrUnsafeSetupJournalStore
	}
	return data, true, nil
}

func validSetupJournalRegular(stat unix.Stat_t, maxSize int64) bool {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || stat.Size < 0 {
		return false
	}
	return maxSize == 0 || stat.Size <= maxSize
}

func atomicSetupJournalWrite(rootFD int, name string, content, expected []byte, mode fs.FileMode, replace bool, random func([]byte) (int, error), fault func(string) error) error {
	if !validSetupJournalName(name) || mode.Perm() != 0o600 || len(content) > maxSetupJournalRecordBytes {
		return ErrUnsafeSetupJournalStore
	}
	if replace && len(expected) == 0 {
		return ErrConfirmedPlanDrift
	}
	temporary, err := setupJournalTemporaryName(random)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(rootFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		if cleanup {
			_ = unix.Unlinkat(rootFD, temporary, 0)
			_ = unix.Fsync(rootFD)
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
		if written == 0 {
			return io.ErrShortWrite
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
	if fault != nil {
		if err := fault("write_before_publish"); err != nil {
			return err
		}
	}
	if replace {
		current, exists, readErr := readSetupJournalFile(rootFD, name)
		if readErr != nil {
			return readErr
		}
		if !exists || !bytes.Equal(current, expected) {
			return ErrConfirmedPlanDrift
		}
		if err := unix.Renameat(rootFD, temporary, rootFD, name); err != nil {
			return err
		}
	} else if err := unix.Renameat2(rootFD, temporary, rootFD, name, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrConfirmedPlanDrift
		}
		return err
	}
	cleanup = false
	if fault != nil {
		if err := fault("write_after_publish"); err != nil {
			return err
		}
	}
	published, exists, err := readSetupJournalFile(rootFD, name)
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(published, content) {
		return ErrConfirmedPlanDrift
	}
	if err := unix.Fsync(rootFD); err != nil {
		return err
	}
	if fault != nil {
		if err := fault("write_after_directory_sync"); err != nil {
			return err
		}
	}
	return nil
}

func setupJournalTemporaryName(random func([]byte) (int, error)) (string, error) {
	data := make([]byte, 16)
	if _, err := io.ReadFull(setupRandomReader{random}, data); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%x", setupJournalTemporaryPrefix, data), nil
}

func listSetupJournalNames(rootFD int) ([]string, error) {
	duplicated, err := unix.Dup(rootFD)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicated), "setup-journal-root")
	if directory == nil {
		_ = unix.Close(duplicated)
		return nil, ErrUnsafeSetupJournalStore
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maxSetupJournalStoreEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxSetupJournalStoreEntries {
		return nil, ErrInvalidSetupJournal
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validSetupJournalName(entry.Name()) {
			return nil, ErrUnsafeSetupJournalStore
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func validSetupJournalName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}
