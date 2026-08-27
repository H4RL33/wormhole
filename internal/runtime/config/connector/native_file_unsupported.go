//go:build !linux

package connector

import "os"

func nativeConnectorFileOwner(os.FileInfo) bool { return false }
