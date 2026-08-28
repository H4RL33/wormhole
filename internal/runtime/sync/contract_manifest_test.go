package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type alphaSyncContract struct {
	SyncProtocol struct {
		Version              int      `json:"version"`
		ActivityVersion      int      `json:"activity_version"`
		PublicDescriptorOnly bool     `json:"public_descriptor_only"`
		ToolsCallFields      []string `json:"tools_call_fields"`
		SafeErrorFields      []string `json:"safe_error_fields"`
	} `json:"sync_protocol"`
}

func TestAlphaContractOwnsSharedV2RecordBoundary(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest alphaSyncContract
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SyncProtocol.Version != projectstate.SyncProtocolVersionV2 ||
		manifest.SyncProtocol.ActivityVersion != 1 ||
		!manifest.SyncProtocol.PublicDescriptorOnly {
		t.Fatalf("sync protocol boundary = %+v", manifest.SyncProtocol)
	}
	wantCall := []string{"name", "arguments", "proof"}
	wantError := []string{"code", "operation"}
	if !equalStrings(manifest.SyncProtocol.ToolsCallFields, wantCall) ||
		!equalStrings(manifest.SyncProtocol.SafeErrorFields, wantError) {
		t.Fatalf("envelope=%q error=%q", manifest.SyncProtocol.ToolsCallFields, manifest.SyncProtocol.SafeErrorFields)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func retainedOperation(sequence int) projectstate.OperationV1 {
	return queueOperation(fmt.Sprintf("90000000-0000-4000-8000-%012d", sequence))
}
