//go:build linux || darwin

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openCredentialRootNoFollow(credentialsDir string) (int, error) {
	absolute, err := filepath.Abs(credentialsDir)
	if err != nil {
		return -1, fmt.Errorf("%w: resolve credential root", ErrUnsafeCredentialProfile)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(filepath.Clean(absolute), "/"), "/")
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, unix.ENOENT) {
				return -1, openErr
			}
			return -1, fmt.Errorf("%w: credential root component", ErrUnsafeCredentialProfile)
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%w: credential root ownership or mode", ErrUnsafeCredentialProfile)
	}
	return fd, nil
}

func validateCredentialRoot(credentialsDir string) error {
	fd, err := openCredentialRootNoFollow(credentialsDir)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func readCredentialProfileFile(credentialsDir, filename string) ([]byte, error) {
	rootFD, err := openCredentialRootNoFollow(credentialsDir)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat(rootFD, filename, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: open credential profile", ErrUnsafeCredentialProfile)
	}
	file := os.NewFile(uintptr(fd), filename)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
		return nil, fmt.Errorf("%w: credential file type, ownership, or mode", ErrUnsafeCredentialProfile)
	}
	return io.ReadAll(file)
}
