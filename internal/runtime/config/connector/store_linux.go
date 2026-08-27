//go:build linux

package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func openConnectorStoreRoot(root string) (string, int, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return "", -1, ErrUnsafeConnectorStore
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", -1, err
	}
	components := strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			unix.Close(current)
			return "", -1, ErrUnsafeConnectorStore
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				unix.Close(current)
				return "", -1, ErrUnsafeConnectorStore
			}
			if syncErr := unix.Fsync(current); syncErr != nil {
				unix.Close(current)
				return "", -1, syncErr
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			unix.Close(current)
			return "", -1, ErrUnsafeConnectorStore
		}
		unix.Close(current)
		current = next
		var stat unix.Stat_t
		if unix.Fstat(current, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || !trustedConnectorDirectory(stat, index == len(components)-1) {
			unix.Close(current)
			return "", -1, ErrUnsafeConnectorStore
		}
	}
	return root, current, nil
}

func trustedConnectorDirectory(stat unix.Stat_t, final bool) bool {
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

func closeConnectorFD(fd int) error { return unix.Close(fd) }

func (s *Store) WithOperationLock(ctx context.Context, adapter AdapterName, name string, operation func(context.Context) error) error {
	if s == nil || operation == nil || !validAdapter(adapter) || !validConnectorName(name) {
		return ErrUnsafeConnectorStore
	}
	canonical, fd, err := openConnectorStoreRoot(s.root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if canonical != s.root {
		return ErrUnsafeConnectorStore
	}
	lockName := connectorPairLockName(adapter, name)
	unlock, err := lockConnectorFile(ctx, fd, lockName)
	if err != nil {
		return err
	}
	defer unlock()
	return operation(ctx)
}

func connectorPairLockName(adapter AdapterName, name string) string {
	digest := sha256.Sum256([]byte(string(adapter) + "\x00" + name))
	return ".pair-" + string(adapter) + "-" + hex.EncodeToString(digest[:]) + ".lock"
}

func lockConnectorFile(ctx context.Context, rootFD int, name string) (func(), error) {
	if name != connectorStoreLockName && !(strings.HasPrefix(name, ".pair-") && strings.HasSuffix(name, ".lock")) {
		return nil, ErrUnsafeConnectorStore
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, ErrUnsafeConnectorStore
	}
	if err := revalidateConnectorFile(rootFD, name, fd, 0); err != nil {
		unix.Close(fd)
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			unix.Close(fd)
			return nil, err
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			if err := revalidateConnectorFile(rootFD, name, fd, 0); err != nil {
				unix.Flock(fd, unix.LOCK_UN)
				unix.Close(fd)
				return nil, err
			}
			return func() { unix.Flock(fd, unix.LOCK_UN); unix.Close(fd) }, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func revalidateConnectorFile(rootFD int, name string, fd int, max int64) error {
	var opened, linked unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || !validConnectorRegular(opened, max) {
		return ErrUnsafeConnectorStore
	}
	if unix.Fstatat(rootFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW) != nil || !validConnectorRegular(linked, max) || opened.Dev != linked.Dev || opened.Ino != linked.Ino {
		return ErrUnsafeConnectorStore
	}
	return nil
}

func validConnectorRegular(stat unix.Stat_t, max int64) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Mode&0o7777 == 0o600 && stat.Nlink == 1 && stat.Size >= 0 && (max == 0 || stat.Size <= max)
}

func listConnectorStoreNames(rootFD int) ([]string, error) {
	dup, err := unix.Dup(rootFD)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(dup), "connector-store")
	if file == nil {
		unix.Close(dup)
		return nil, ErrUnsafeConnectorStore
	}
	defer file.Close()
	entries, err := file.ReadDir(maxConnectorStoreRecords + maxConnectorStoreLocks + maxConnectorStoreRecords + 2)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxConnectorStoreRecords+maxConnectorStoreLocks+maxConnectorStoreRecords+1 {
		return nil, ErrInvalidConnectorStore
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func readConnectorFile(rootFD int, name string) ([]byte, bool, error) {
	if strings.Contains(name, "/") || name == "" {
		return nil, false, ErrUnsafeConnectorStore
	}
	var linked unix.Stat_t
	if err := unix.Fstatat(rootFD, name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !validConnectorRegular(linked, maxConnectorRecordBytes) {
		return nil, false, ErrUnsafeConnectorStore
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, ErrUnsafeConnectorStore
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, false, ErrUnsafeConnectorStore
	}
	defer file.Close()
	if err := revalidateConnectorFile(rootFD, name, fd, maxConnectorRecordBytes); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConnectorRecordBytes+1))
	if err != nil || len(data) > maxConnectorRecordBytes {
		return nil, false, ErrInvalidConnectorStore
	}
	return data, true, nil
}

func atomicConnectorWrite(rootFD int, name string, content, expected []byte, replace bool, random func([]byte) (int, error), fault func(string) error) error {
	if len(content) > maxConnectorRecordBytes || strings.Contains(name, "/") || name == "" {
		return ErrInvalidConnectorStore
	}
	temporary, err := connectorTemporaryName(random)
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
			unix.Close(fd)
		}
		if cleanup {
			unix.Unlinkat(rootFD, temporary, 0)
			unix.Fsync(rootFD)
		}
	}()
	if unix.Fchmod(fd, 0o600) != nil {
		return ErrUnsafeConnectorStore
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
		current, exists, err := readConnectorFile(rootFD, name)
		if err != nil {
			return err
		}
		if !exists || !bytes.Equal(current, expected) {
			return configDriftError()
		}
		if err := unix.Renameat(rootFD, temporary, rootFD, name); err != nil {
			return err
		}
	} else if err := unix.Renameat2(rootFD, temporary, rootFD, name, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return configDriftError()
		}
		return err
	}
	cleanup = false
	if fault != nil {
		if err := fault("write_after_publish"); err != nil {
			return err
		}
	}
	if err := unix.Fsync(rootFD); err != nil {
		return err
	}
	if fault != nil {
		if err := fault("write_after_sync"); err != nil {
			return err
		}
	}
	readback, exists, err := readConnectorFile(rootFD, name)
	if err != nil || !exists || !bytes.Equal(readback, content) {
		return ErrInvalidConnectorStore
	}
	return nil
}

func configDriftError() error { return ErrInvalidConnectorStore }

func connectorTemporaryName(random func([]byte) (int, error)) (string, error) {
	data := make([]byte, 16)
	if _, err := io.ReadFull(connectorRandomReader{random}, data); err != nil {
		return "", err
	}
	return connectorTemporaryPrefix + hex.EncodeToString(data), nil
}

func retireConnectorTemps(rootFD int, names []string) error {
	for _, name := range names {
		if !isConnectorTemporaryName(name) {
			return ErrInvalidConnectorStore
		}
		if err := unix.Unlinkat(rootFD, name, 0); err != nil {
			return err
		}
	}
	if len(names) > 0 {
		return unix.Fsync(rootFD)
	}
	return nil
}

var _ fs.FileMode
