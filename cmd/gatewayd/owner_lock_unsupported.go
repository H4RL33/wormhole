//go:build !linux

package main

import "errors"

var errGatewayAlreadyRunning = errors.New("gatewayd: already running")

type databaseOwnerLock struct{}

func acquireDatabaseOwnerLock(string) (*databaseOwnerLock, error) {
	return nil, ensureSupportedPlatform()
}

func (*databaseOwnerLock) DatabasePath() string {
	return ""
}

func (*databaseOwnerLock) Close() error {
	return nil
}
