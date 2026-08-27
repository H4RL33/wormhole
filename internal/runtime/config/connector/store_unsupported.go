//go:build !linux

package connector

import "context"

func openConnectorStoreRoot(string) (string, int, error) {
	return "", -1, ErrConnectorFilesystemUnsupported
}
func closeConnectorFD(int) error { return nil }

func (s *Store) WithOperationLock(context.Context, AdapterName, string, func(context.Context) error) error {
	return ErrConnectorFilesystemUnsupported
}

func lockConnectorFile(context.Context, int, string) (func(), error) {
	return nil, ErrConnectorFilesystemUnsupported
}

func listConnectorStoreNames(int) ([]string, error) {
	return nil, ErrConnectorFilesystemUnsupported
}

func readConnectorFile(int, string) ([]byte, bool, error) {
	return nil, false, ErrConnectorFilesystemUnsupported
}

func atomicConnectorWrite(int, string, []byte, []byte, bool, func([]byte) (int, error), func(string) error) error {
	return ErrConnectorFilesystemUnsupported
}

func retireConnectorTemps(int, []string) error {
	return ErrConnectorFilesystemUnsupported
}
