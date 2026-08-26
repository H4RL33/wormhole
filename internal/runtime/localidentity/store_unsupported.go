//go:build !linux && !darwin

package localidentity

import (
	"context"
	"fmt"
	"io/fs"
)

func openLocalIdentityRoot(string) (string, int, error) {
	return "", -1, ErrStoreFilesystemUnsupported
}

func closeLocalIdentityFD(int) error { return nil }

func lockLocalIdentityStore(context.Context, int) (func(), error) {
	return nil, ErrStoreFilesystemUnsupported
}

func readLocalIdentityFile(int, string) ([]byte, bool, error) {
	return nil, false, ErrStoreFilesystemUnsupported
}

func atomicLocalIdentityWrite(int, string, []byte, fs.FileMode, bool, func([]byte) (int, error)) error {
	return fmt.Errorf("%w", ErrStoreFilesystemUnsupported)
}
