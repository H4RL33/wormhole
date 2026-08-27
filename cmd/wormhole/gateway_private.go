package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

func callGatewayPrivateMethod(ctx context.Context, socketPath, method string, request, response any) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("gatewayd not running (dial %s: %w)", socketPath, err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	write := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = conn.Write(append(raw, '\n'))
		return err
	}
	read := func(destination any) error {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		return decodeClosedGatewayJSON(bytes.TrimSpace(line), destination)
	}

	initializeParams, err := json.Marshal(map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "wormhole-cli", "version": version}})
	if err != nil {
		return fmt.Errorf("marshal Gateway initialize request: %w", err)
	}
	if err := write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: initializeParams}); err != nil {
		return fmt.Errorf("write Gateway initialize request: %w", err)
	}
	var initialized rpcResponse
	if err := read(&initialized); err != nil {
		return fmt.Errorf("read Gateway initialize response: %w", err)
	}
	if initialized.Error != nil {
		return errors.New(initialized.Error.Message)
	}
	if err := write(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return fmt.Errorf("write Gateway initialized notification: %w", err)
	}
	params, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal Gateway private request: %w", err)
	}
	if err := write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: method, Params: params}); err != nil {
		return fmt.Errorf("write Gateway private request: %w", err)
	}
	var privateResponse rpcResponse
	if err := read(&privateResponse); err != nil {
		return fmt.Errorf("read Gateway private response: %w", err)
	}
	if privateResponse.Error != nil {
		return errors.New(privateResponse.Error.Message)
	}
	if err := decodeClosedGatewayJSON(privateResponse.Result, response); err != nil {
		return fmt.Errorf("decode Gateway private response: %w", err)
	}
	return nil
}

func decodeClosedGatewayJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
