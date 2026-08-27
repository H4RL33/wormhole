//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

var errGatewayAlreadyRunning = errors.New("gatewayd: already running")

type databaseOwnerLock struct {
	fd           int
	databasePath string
	closeOnce    sync.Once
	closeErr     error
}

func acquireDatabaseOwnerLock(path string) (*databaseOwnerLock, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	absolutePath = filepath.Clean(absolutePath)
	parentPath := filepath.Dir(absolutePath)
	if err := os.MkdirAll(parentPath, 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return nil, fmt.Errorf("resolve database directory: %w", err)
	}
	parentFD, err := unix.Open(canonicalParent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open database directory: %w", err)
	}
	defer unix.Close(parentFD)

	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil {
		return nil, fmt.Errorf("stat database directory: %w", err)
	}
	if parentStat.Mode&unix.S_IFMT != unix.S_IFDIR || parentStat.Uid != uint32(os.Geteuid()) || parentStat.Mode&0o7777 != 0o700 {
		return nil, fmt.Errorf("unsafe database directory %s: require effective-user-owned mode 0700 directory", canonicalParent)
	}

	databaseName := filepath.Base(absolutePath)
	if databaseName == "." || databaseName == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid database path %s", absolutePath)
	}
	canonicalDatabasePath := filepath.Join(canonicalParent, databaseName)
	lockName := databaseName + ".lock"
	lockFD, err := unix.Openat(parentFD, lockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database owner lock: %w", err)
	}
	keepLockFD := false
	defer func() {
		if !keepLockFD {
			_ = unix.Close(lockFD)
		}
	}()

	var lockStat unix.Stat_t
	if err := unix.Fstat(lockFD, &lockStat); err != nil {
		return nil, fmt.Errorf("stat database owner lock: %w", err)
	}
	if err := validateDatabaseOwnerFileStat("database owner lock", &lockStat, false); err != nil {
		return nil, err
	}
	if err := validateDatabaseOwnerPathIdentity(parentFD, lockName, &lockStat); err != nil {
		return nil, err
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", errGatewayAlreadyRunning, canonicalDatabasePath)
		}
		return nil, fmt.Errorf("lock database owner file: %w", err)
	}
	ownerFiles := make([]databaseOwnerFile, 0, 3)
	defer func() {
		for _, file := range ownerFiles {
			_ = unix.Close(file.fd)
		}
	}()
	for _, name := range []string{databaseName, databaseName + "-wal", databaseName + "-shm"} {
		file, err := openDatabaseOwnerFile(parentFD, name)
		if err != nil {
			return nil, err
		}
		if file.fd >= 0 {
			ownerFiles = append(ownerFiles, file)
		}
	}

	if err := unix.Fstat(lockFD, &lockStat); err != nil {
		return nil, fmt.Errorf("restat database owner lock before restriction: %w", err)
	}
	if err := validateDatabaseOwnerFileStat("database owner lock", &lockStat, false); err != nil {
		return nil, err
	}
	if err := validateDatabaseOwnerPathIdentity(parentFD, lockName, &lockStat); err != nil {
		return nil, err
	}
	if err := unix.Fchmod(lockFD, 0o600); err != nil {
		return nil, fmt.Errorf("restrict database owner lock: %w", err)
	}
	if err := unix.Fstat(lockFD, &lockStat); err != nil {
		return nil, fmt.Errorf("restat database owner lock: %w", err)
	}
	if err := validateDatabaseOwnerFileStat("database owner lock", &lockStat, true); err != nil {
		return nil, err
	}
	if err := validateDatabaseOwnerPathIdentity(parentFD, lockName, &lockStat); err != nil {
		return nil, err
	}
	for _, file := range ownerFiles {
		if err := normalizeDatabaseOwnerFile(parentFD, file); err != nil {
			return nil, err
		}
	}

	keepLockFD = true
	return &databaseOwnerLock{fd: lockFD, databasePath: canonicalDatabasePath}, nil
}

func validateDatabaseOwnerFileStat(label string, stat *unix.Stat_t, exactMode bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("unsafe %s: require regular, effective-user-owned, single-link file", label)
	}
	if exactMode && stat.Mode&0o7777 != 0o600 {
		return fmt.Errorf("unsafe %s: require mode 0600", label)
	}
	return nil
}

func validateDatabaseOwnerPathIdentity(parentFD int, name string, opened *unix.Stat_t) error {
	var entry unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("inspect database owner path %s: %w", name, err)
	}
	if entry.Dev != opened.Dev || entry.Ino != opened.Ino {
		return fmt.Errorf("unsafe database owner path %s: entry changed during validation", name)
	}
	if err := validateDatabaseOwnerFileStat(name, &entry, false); err != nil {
		return err
	}
	return nil
}

type databaseOwnerFile struct {
	fd   int
	name string
}

func openDatabaseOwnerFile(parentFD int, name string) (databaseOwnerFile, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return databaseOwnerFile{fd: -1}, nil
	}
	if err != nil {
		return databaseOwnerFile{}, fmt.Errorf("open database owner file %s: %w", name, err)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return databaseOwnerFile{}, fmt.Errorf("stat database owner file %s: %w", name, err)
	}
	if err := validateDatabaseOwnerFileStat(name, &stat, false); err != nil {
		_ = unix.Close(fd)
		return databaseOwnerFile{}, err
	}
	if err := validateDatabaseOwnerPathIdentity(parentFD, name, &stat); err != nil {
		_ = unix.Close(fd)
		return databaseOwnerFile{}, err
	}
	return databaseOwnerFile{fd: fd, name: name}, nil
}

func normalizeDatabaseOwnerFile(parentFD int, file databaseOwnerFile) error {
	var stat unix.Stat_t
	if err := unix.Fstat(file.fd, &stat); err != nil {
		return fmt.Errorf("restat database owner file %s before restriction: %w", file.name, err)
	}
	if err := validateDatabaseOwnerFileStat(file.name, &stat, false); err != nil {
		return err
	}
	if err := validateDatabaseOwnerPathIdentity(parentFD, file.name, &stat); err != nil {
		return err
	}
	if err := unix.Fchmod(file.fd, 0o600); err != nil {
		return fmt.Errorf("restrict database owner file %s: %w", file.name, err)
	}
	if err := unix.Fstat(file.fd, &stat); err != nil {
		return fmt.Errorf("restat database owner file %s: %w", file.name, err)
	}
	if err := validateDatabaseOwnerFileStat(file.name, &stat, true); err != nil {
		return err
	}
	return validateDatabaseOwnerPathIdentity(parentFD, file.name, &stat)
}

func (lock *databaseOwnerLock) DatabasePath() string {
	return lock.databasePath
}

func (lock *databaseOwnerLock) Close() error {
	lock.closeOnce.Do(func() {
		lock.closeErr = unix.Close(lock.fd)
	})
	return lock.closeErr
}
