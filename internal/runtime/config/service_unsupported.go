//go:build !linux

package config

import (
	"os/exec"
)

func NewGatewayService(runner CommandRunner) GatewayService {
	return newUnavailableGatewayService(runner)
}

func prepareCommandProcessGroup(*exec.Cmd) {}

func terminateCommandProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
