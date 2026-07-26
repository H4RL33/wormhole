package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestResponseSchemaReflectionCoversIgnoredOptionalEnumAndFallbackFields(t *testing.T) {
	type response struct {
		Ignored  string   `json:"-"`
		Optional []string `json:"optional,omitempty"`
		Mode     string   `json:"mode" enum:"fast,safe"`
	}
	properties, required := reflectResponseStructSchema(reflect.TypeOf(response{}))
	if _, ok := properties["Ignored"]; ok {
		t.Fatal("ignored response field appeared in schema")
	}
	if _, ok := properties["optional"]; !ok {
		t.Fatal("optional response field missing from schema")
	}
	mode, ok := properties["mode"].(map[string]any)
	if !ok || len(mode["enum"].([]any)) != 2 {
		t.Fatalf("enum response schema = %#v", properties["mode"])
	}
	if len(required) != 1 || required[0] != "mode" {
		t.Fatalf("required response fields = %#v", required)
	}
	if got := jsonPresentResponseSchemaForType(reflect.TypeOf(float64(0))); got["type"] != "object" {
		t.Fatalf("response fallback schema = %#v", got)
	}
	if got := jsonSchemaForType(reflect.TypeOf(float64(0))); got["type"] != "object" {
		t.Fatalf("request fallback schema = %#v", got)
	}
}

func TestHandleToolsCallRejectsMalformedArgumentsAndUnencodableResults(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name: "test.boundary", RequiresAuth: false,
		Handler: func(context.Context, *identity.AuthenticatedScope, string, json.RawMessage) (any, error) {
			return make(chan int), nil
		},
	})
	malformed, _ := json.Marshal(toolsCallParams{Name: "test.boundary", Arguments: json.RawMessage(`{`)})
	if result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", malformed); result != nil || rpcErr == nil || rpcErr.Code != RPCInvalidParams {
		t.Fatalf("malformed arguments result=%v rpcErr=%+v", result, rpcErr)
	}
	params, _ := json.Marshal(toolsCallParams{Name: "test.boundary", Arguments: json.RawMessage(`{"project_id":"project"}`)})
	if result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", params); result != nil || rpcErr == nil || rpcErr.Code != RPCInternalError {
		t.Fatalf("unencodable result=%v rpcErr=%+v", result, rpcErr)
	}
}
