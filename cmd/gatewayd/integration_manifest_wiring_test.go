package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/sync"
)

type manifestConfigurableTestEngine struct {
	receiver sync.IntegrationManifestReceiver
}

func (*manifestConfigurableTestEngine) Bootstrap(context.Context) error { return nil }
func (*manifestConfigurableTestEngine) Start(context.Context)           {}
func (*manifestConfigurableTestEngine) Stop()                           {}
func (e *manifestConfigurableTestEngine) ConfigureIntegrationManifestReceiver(receiver sync.IntegrationManifestReceiver) {
	e.receiver = receiver
}

type noopIntegrationManifestReceiver struct{}

func (noopIntegrationManifestReceiver) ReceiveIntegrationManifest(context.Context, string, string, []string, json.RawMessage) error {
	return nil
}

func TestWireIntegrationManifestReceiversConfiguresEveryNormalEngine(t *testing.T) {
	first := &manifestConfigurableTestEngine{}
	second := &manifestConfigurableTestEngine{}
	group := &syncGroup{engines: []syncEngine{first, second}}
	receiver := noopIntegrationManifestReceiver{}
	wireIntegrationManifestReceivers(group, receiver)
	if first.receiver == nil || second.receiver == nil {
		t.Fatal("manifest receiver was not configured on every engine")
	}
}
