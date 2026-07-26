//go:build !linux && !darwin

package localapi

import (
	"fmt"
	"io/fs"
)

type materializationRootHandle struct{}
type materializationParentHandle struct {
	created []string
	missing bool
}

func openMaterializationRoot(string) (*materializationRootHandle, error) {
	return nil, fmt.Errorf("%w: descriptor-relative filesystem operations are unavailable on this platform", ErrIntegrationUnsupported)
}

func (*materializationRootHandle) close() {}

func (*materializationRootHandle) openParent(string, bool) (*materializationParentHandle, error) {
	return nil, ErrIntegrationUnsupported
}

func (*materializationParentHandle) close() {}

func (*materializationParentHandle) revalidate() error { return ErrIntegrationUnsupported }

func (*materializationParentHandle) read() ([]byte, fs.FileMode, IntegrationFileIdentity, bool, error) {
	return nil, 0, IntegrationFileIdentity{}, false, ErrIntegrationUnsupported
}

func (*materializationParentHandle) atomicWrite([]byte, bool, []byte, fs.FileMode, func() error) (IntegrationFileIdentity, error) {
	return IntegrationFileIdentity{}, ErrIntegrationUnsupported
}

func (*materializationParentHandle) unlink([]byte, func() error) error {
	return ErrIntegrationUnsupported
}

func (*materializationRootHandle) removeCreatedDirectories([]string) {}
