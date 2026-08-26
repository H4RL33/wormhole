package config

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	gatewayServiceUnit  = "wormhole-gatewayd.service"
	gatewayMCPProtocol  = "2025-11-25"
	gatewayServerName   = "gatewayd"
	gatewayReadyMaxLine = 64 << 10
)

var (
	ErrServiceManagerUnavailable = errors.New("gatewayd service manager unavailable; start gatewayd manually")
	ErrServiceStateUnknown       = errors.New("config: gatewayd service state is not recognized")
	ErrServiceNotInstalled       = errors.New("config: gatewayd service is not installed")
	ErrServiceNotReady           = errors.New("config: gatewayd service is not ready")
	ErrServiceChangeDrift        = errors.New("config: gatewayd service changed after confirmation")
	ErrUnsafeServicePath         = errors.New("config: unsafe gatewayd service path")
	ErrRepositoryContent         = errors.New("config: refusing repository-supplied executable content")
)

type ServiceState struct {
	Installed  bool
	Enabled    bool
	Active     bool
	Ready      bool
	Diagnostic string
}

// ConfirmedServiceChange carries only the confirmed executable and prior
// readback. Unit, socket, and runtime locations remain service-owned and are
// always derived from the effective user environment.
type ConfirmedServiceChange struct {
	Executable    string
	ExpectedPrior ServiceState
}

type GatewayService interface {
	Inspect(context.Context) (ServiceState, error)
	Install(context.Context, ConfirmedServiceChange) error
	Start(context.Context) error
	WaitReady(context.Context) error
}

type unavailableGatewayService struct {
	runner CommandRunner
}

func newUnavailableGatewayService(runner CommandRunner) GatewayService {
	return unavailableGatewayService{runner: runner}
}

func (unavailableGatewayService) Inspect(context.Context) (ServiceState, error) {
	return ServiceState{}, ErrServiceManagerUnavailable
}

func (unavailableGatewayService) Install(context.Context, ConfirmedServiceChange) error {
	return ErrServiceManagerUnavailable
}

func (unavailableGatewayService) Start(context.Context) error { return ErrServiceManagerUnavailable }
func (unavailableGatewayService) WaitReady(context.Context) error {
	return ErrServiceManagerUnavailable
}

type gatewayInitializeResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	} `json:"result"`
	Error json.RawMessage `json:"error"`
}

func probeGatewayReady(ctx context.Context, socketPath string, dial func(context.Context, string, string) (net.Conn, error)) error {
	connection, err := dial(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("%w: connect", ErrServiceNotReady)
	}
	defer connection.Close()
	deadline := time.Now().Add(250 * time.Millisecond)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("%w: set readiness deadline", ErrServiceNotReady)
	}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"wormhole-setup","version":"1"}}}` + "\n")
	if _, err := connection.Write(request); err != nil {
		return fmt.Errorf("%w: write initialize", ErrServiceNotReady)
	}
	line, err := bufio.NewReaderSize(connection, gatewayReadyMaxLine+1).ReadBytes('\n')
	if err != nil || len(line) > gatewayReadyMaxLine {
		return fmt.Errorf("%w: read initialize", ErrServiceNotReady)
	}
	var response gatewayInitializeResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("%w: decode initialize", ErrServiceNotReady)
	}
	if response.JSONRPC != "2.0" || string(response.ID) != "1" || len(response.Error) != 0 ||
		response.Result.ProtocolVersion != gatewayMCPProtocol || response.Result.ServerInfo.Name != gatewayServerName {
		return fmt.Errorf("%w: unexpected initialize response", ErrServiceNotReady)
	}
	return nil
}
