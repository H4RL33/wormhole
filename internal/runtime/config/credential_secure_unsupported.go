//go:build !linux && !darwin

package config

import (
	"fmt"
)

func validateCredentialRoot(credentialsDir string) error {
	return fmt.Errorf("%w: %s", ErrCredentialFilesystemUnsupported, credentialsDir)
}

func readCredentialProfileFile(credentialsDir, filename string) ([]byte, error) {
	return nil, fmt.Errorf("%w: %s", ErrCredentialFilesystemUnsupported, credentialsDir)
}

func commitCredentialNoReplace(temporaryPath, finalPath string) error {
	return fmt.Errorf("%w: %s", ErrCredentialFilesystemUnsupported, finalPath)
}
