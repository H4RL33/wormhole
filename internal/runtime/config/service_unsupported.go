//go:build !linux

package config

func NewGatewayService(runner CommandRunner) GatewayService {
	return newUnavailableGatewayService(runner)
}
