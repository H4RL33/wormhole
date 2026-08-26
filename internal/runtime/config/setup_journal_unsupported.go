//go:build !linux

package config

import (
	"context"
	"io/fs"
)

func openSetupJournalRoot(string) (string, int, error) {
	// Stage 2 deliberately supports this store only on Linux. Darwin and
	// Windows fail before root creation until the complete setup/service
	// security and durability contract exists on those platforms.
	return "", -1, ErrSetupJournalFilesystemUnsupported
}

func closeSetupJournalFD(int) error { return nil }

func lockSetupJournalStore(context.Context, int) (func(), error) {
	return nil, ErrSetupJournalFilesystemUnsupported
}

func revalidateSetupJournalLock(int, int) error { return ErrSetupJournalFilesystemUnsupported }

func readSetupJournalFile(int, string) ([]byte, bool, error) {
	return nil, false, ErrSetupJournalFilesystemUnsupported
}

func atomicSetupJournalWrite(int, string, []byte, []byte, fs.FileMode, bool, func([]byte) (int, error), func(string) error) error {
	return ErrSetupJournalFilesystemUnsupported
}

func listSetupJournalNames(int) ([]string, error) {
	return nil, ErrSetupJournalFilesystemUnsupported
}
