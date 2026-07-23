package sync

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type alphaSyncContract struct {
	Mode         string `json:"mode"`
	SyncProtocol struct {
		Version   int               `json:"version"`
		Methods   []alphaSyncMethod `json:"methods"`
		WireTypes []alphaWireType   `json:"wire_types"`
	} `json:"sync_protocol"`
}

type alphaSyncMethod struct {
	Name                  string   `json:"name"`
	RequestFields         []string `json:"request_fields"`
	OptionalRequestFields []string `json:"optional_request_fields"`
}

type alphaWireType struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

func TestAlphaContractSyncProtocol(t *testing.T) {
	manifest := readAlphaSyncContract(t)
	if manifest.Mode != "alpha-inventory" {
		t.Fatalf("mode = %q, want alpha-inventory", manifest.Mode)
	}
	if SyncProtocolVersion != manifest.SyncProtocol.Version {
		t.Fatalf("Gateway SyncProtocolVersion = %d, manifest = %d", SyncProtocolVersion, manifest.SyncProtocol.Version)
	}

	queueRepo, auditRepo := setupTestRepos(t)
	defer queueRepo.db.Close()
	engine := mustNewEngine(t, "http://unused.invalid", queueRepo, auditRepo, nil, nil, DefaultConfig())

	calls := map[string][]map[string]interface{}{}
	engine.testCallSyncToolWithResultFn = func(_ context.Context, name string, args map[string]interface{}) (interface{}, error) {
		calls[name] = append(calls[name], args)
		switch name {
		case "wormhole.sync.bootstrap":
			return map[string]interface{}{
				"org_config":   map[string]interface{}{},
				"project_list": []interface{}{},
				"task_list":    []interface{}{},
				"kb_list":      []interface{}{},
				"timestamp":    "2026-07-23T00:00:00Z",
				"version":      manifest.SyncProtocol.Version,
			}, nil
		case "wormhole.sync.incremental_pull":
			return map[string]interface{}{"updates": []interface{}{}, "timestamp": "2026-07-23T00:00:00Z", "version": manifest.SyncProtocol.Version}, nil
		case "wormhole.sync.incremental_push":
			return map[string]interface{}{
				"items_received": 1,
				"applied":        []map[string]interface{}{{"id": "task-contract", "type": "task"}},
				"timestamp":      "2026-07-23T00:00:00Z",
				"version":        manifest.SyncProtocol.Version,
			}, nil
		case "wormhole.sync.conflict_report":
			return map[string]interface{}{"resolved_value": "server", "resolution_method": "last_write_wins", "version": manifest.SyncProtocol.Version}, nil
		default:
			t.Fatalf("unexpected sync method %q", name)
			return nil, nil
		}
	}

	ctx := context.Background()
	if err := engine.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := engine.PullIncremental(ctx); err != nil {
		t.Fatalf("PullIncremental: %v", err)
	}
	if err := engine.PullIncremental(ctx); err != nil {
		t.Fatalf("PullIncremental with cursor: %v", err)
	}
	if _, err := queueRepo.Enqueue(ctx, "ns-1", "task", "task-contract", "create", json.RawMessage(`{"title":"contract"}`), 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := engine.pushBatch(ctx); err != nil {
		t.Fatalf("pushBatch: %v", err)
	}
	if err := engine.ReportConflict(ctx, "task", "task-contract", "concurrent_update", "server", "local"); err != nil {
		t.Fatalf("ReportConflict: %v", err)
	}

	actualMethods := make([]alphaSyncMethod, 0, len(calls))
	for name, argumentSets := range calls {
		fieldCounts := map[string]int{}
		for _, args := range argumentSets {
			version, ok := args["version"].(int)
			if !ok {
				t.Fatalf("%s version = %#v, want int", name, args["version"])
			}
			if version != manifest.SyncProtocol.Version {
				t.Fatalf("%s version = %d, manifest = %d", name, version, manifest.SyncProtocol.Version)
			}
			for field := range args {
				fieldCounts[field]++
			}
		}
		fields := make([]string, 0, len(fieldCounts))
		optionalFields := []string{}
		for field, count := range fieldCounts {
			fields = append(fields, field)
			if count != len(argumentSets) {
				optionalFields = append(optionalFields, field)
			}
		}
		sort.Strings(fields)
		sort.Strings(optionalFields)
		actualMethods = append(actualMethods, alphaSyncMethod{
			Name:                  name,
			RequestFields:         fields,
			OptionalRequestFields: optionalFields,
		})
	}
	sort.Slice(actualMethods, func(i, j int) bool { return actualMethods[i].Name < actualMethods[j].Name })
	if !reflect.DeepEqual(actualMethods, manifest.SyncProtocol.Methods) {
		t.Fatalf("sync methods = %#v, manifest = %#v", actualMethods, manifest.SyncProtocol.Methods)
	}

	gatewayWireTypes := []alphaWireType{
		{Name: "applied_item", Fields: jsonFieldNames(t, appliedItemWire{})},
		{Name: "article_summary", Fields: jsonFieldNames(t, articleSummaryWire{})},
		{Name: "bootstrap_response", Fields: jsonFieldNames(t, bootstrapResultWire{})},
		{Name: "incremental_pull_response", Fields: jsonFieldNames(t, incrementalPullResultWire{})},
		{Name: "incremental_pull_update", Fields: jsonFieldNames(t, syncUpdateEnvelopeWire{})},
		{Name: "incremental_push_response", Fields: jsonFieldNames(t, incrementalPushResultWire{})},
		{Name: "task_summary", Fields: jsonFieldNames(t, taskSummaryWire{})},
	}
	manifestWireTypes := make(map[string][]string, len(manifest.SyncProtocol.WireTypes))
	for _, wireType := range manifest.SyncProtocol.WireTypes {
		manifestWireTypes[wireType.Name] = wireType.Fields
	}
	for _, wireType := range gatewayWireTypes {
		if !reflect.DeepEqual(wireType.Fields, manifestWireTypes[wireType.Name]) {
			t.Errorf("Gateway %s fields = %v, manifest = %v", wireType.Name, wireType.Fields, manifestWireTypes[wireType.Name])
		}
	}
}

func readAlphaSyncContract(t *testing.T) alphaSyncContract {
	t.Helper()
	data, err := os.ReadFile("../../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatalf("read alpha contract: %v", err)
	}
	var manifest alphaSyncContract
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode alpha contract: %v", err)
	}
	return manifest
}

func jsonFieldNames(t *testing.T, value any) []string {
	t.Helper()
	valueType := reflect.TypeOf(value)
	fields := make([]string, 0, valueType.NumField())
	for i := 0; i < valueType.NumField(); i++ {
		name := strings.Split(valueType.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("%s field %s has no JSON name", valueType, valueType.Field(i).Name)
		}
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}
