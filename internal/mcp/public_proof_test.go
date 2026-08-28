package mcp

import (
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

func publicProofFixture() *types.PublicRequestProof {
	return &types.PublicRequestProof{
		KeyID: "sha256:key", PublicKey: "public", Timestamp: "2026-08-28T12:00:00Z",
		Nonce: "nonce", Signature: "signature", SessionID: "session",
	}
}

func TestToolsCallParamsGoldenPlacesProofBesideArguments(t *testing.T) {
	got, err := json.Marshal(ToolsCallParams{
		Name: "wormhole.sync.push", Arguments: json.RawMessage(`{"version":2}`), Proof: publicProofFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"wormhole.sync.push","arguments":{"version":2},"proof":{"key_id":"sha256:key","public_key":"public","timestamp":"2026-08-28T12:00:00Z","nonce":"nonce","signature":"signature","session_id":"session"}}`
	if string(got) != want {
		t.Fatalf("ToolsCallParams = %s, want %s", got, want)
	}
	private, err := json.Marshal(ToolsCallParams{Name: "wormhole.agent.whoami", Arguments: json.RawMessage(`{}`)})
	if err != nil || string(private) != `{"name":"wormhole.agent.whoami","arguments":{}}` {
		t.Fatalf("private ToolsCallParams = %s, %v", private, err)
	}
}

func TestProbeToolsCallNameKeepsPreIdentificationFailuresNumeric(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": `{"arguments":{}}`, "duplicate": `{"name":"wormhole.sync.push","name":"wormhole.sync.pull","arguments":{}}`,
		"non-string": `{"name":2,"arguments":{}}`, "malformed": `{"name":"wormhole.sync.push","arguments":`,
		"trailing": `{"name":"wormhole.sync.push","arguments":{}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if operation, err := probeToolsCallName(json.RawMessage(raw)); err == nil || operation != "" {
				t.Fatalf("probe = %q, %v; want unidentified error", operation, err)
			}
		})
	}
	operation, err := probeToolsCallName(json.RawMessage(`{"extra":1,"name":"wormhole.sync.push","arguments":{}}`))
	if err != nil || operation != "wormhole.sync.push" {
		t.Fatalf("identified operation = %q, %v", operation, err)
	}
}

func TestDecodeKnownPublicToolsCallParamsIsStrictAndAuthExclusive(t *testing.T) {
	valid, err := json.Marshal(ToolsCallParams{Name: "wormhole.sync.push", Arguments: json.RawMessage(`{"version":2}`), Proof: publicProofFixture()})
	if err != nil {
		t.Fatal(err)
	}
	got, code := decodeKnownPublicToolsCallParams(valid, "wormhole.sync.push", "")
	if code != "" || got.Name != "wormhole.sync.push" || got.Proof == nil || string(got.Arguments) != `{"version":2}` {
		t.Fatalf("decoded = %+v, code %q", got, code)
	}
	for name, fixture := range map[string]struct {
		raw        string
		authHeader string
		wantCode   string
	}{
		"unknown field":       {raw: `{"name":"wormhole.sync.push","arguments":{"version":2},"proof":{"key_id":"k","public_key":"p","timestamp":"t","nonce":"n","signature":"s"},"extra":1}`, wantCode: "invalid_request"},
		"duplicate arguments": {raw: `{"name":"wormhole.sync.push","arguments":{},"arguments":{},"proof":{"key_id":"k","public_key":"p","timestamp":"t","nonce":"n","signature":"s"}}`, wantCode: "invalid_request"},
		"trailing":            {raw: string(valid) + `{}`, wantCode: "invalid_request"},
		"missing proof":       {raw: `{"name":"wormhole.sync.push","arguments":{"version":2}}`, wantCode: "authentication_failed"},
		"proof and bearer":    {raw: string(valid), authHeader: "Bearer private", wantCode: "authentication_failed"},
		"wrong known name":    {raw: string(valid), wantCode: ""},
	} {
		t.Run(name, func(t *testing.T) {
			expected := "wormhole.sync.push"
			if name == "wrong known name" {
				expected = "wormhole.sync.pull"
				fixture.wantCode = "invalid_request"
			}
			_, code := decodeKnownPublicToolsCallParams(json.RawMessage(fixture.raw), expected, fixture.authHeader)
			if code != fixture.wantCode {
				t.Fatalf("code = %q, want %q", code, fixture.wantCode)
			}
		})
	}
}

func TestDecodePublicArgumentsRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	type args struct {
		Version int `json:"version"`
	}
	for _, raw := range []string{`{"version":2,"extra":1}`, `{"version":2,"version":2}`, `{"version":2}{}`} {
		var destination args
		if err := decodePublicArguments(json.RawMessage(raw), &destination); err == nil {
			t.Fatalf("decodePublicArguments accepted %s", raw)
		}
	}
	var got args
	if err := decodePublicArguments(json.RawMessage(`{"version":2}`), &got); err != nil || got.Version != 2 {
		t.Fatalf("decodePublicArguments = %+v, %v", got, err)
	}
}

func TestDecodePublicArgumentsRejectsNestedSyncPushDuplicates(t *testing.T) {
	for name, raw := range map[string]string{
		"repository": `{"version":2,"attachment_ref":"attachment","repository":{"provider":"github","provider":"gitlab","immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":"refs/heads/main","base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_stream_version":1,"expected_live_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","operation":{"schema_version":1,"id":"operation","kind":"put_record","expected_view_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","actor":{"actor_kind":"human","assurance":"public-key-continuity","occurred_at":"2026-08-28T12:00:00Z"}}}`,
		"operation":  `{"version":2,"attachment_ref":"attachment","repository":{"provider":"github","immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":"refs/heads/main","base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_stream_version":1,"expected_live_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","operation":{"schema_version":1,"id":"operation-1","id":"operation-2","kind":"put_record","expected_view_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","actor":{"actor_kind":"human","assurance":"public-key-continuity","occurred_at":"2026-08-28T12:00:00Z"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var destination SyncPushV2Args
			if err := decodePublicArguments(json.RawMessage(raw), &destination); err == nil {
				t.Fatal("decodePublicArguments accepted a nested duplicate member")
			}
		})
	}
}

func TestDecodePublicArgumentsRejectsMissingRequiredMember(t *testing.T) {
	for name, raw := range map[string]string{
		"top-level canonical ref": `{"version":2,"repository":{"provider":"github","immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		"nested immutable id":     `{"version":2,"repository":{"provider":"github","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":"refs/heads/main","base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var destination SyncAttachV2Args
			if err := decodePublicArguments(json.RawMessage(raw), &destination); err == nil {
				t.Fatal("decodePublicArguments accepted a missing required member")
			}
		})
	}
}

func TestDecodePublicArgumentsRejectsWrongSyncVersion(t *testing.T) {
	raw := `{"version":1,"repository":{"provider":"github","immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":"refs/heads/main","base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	var destination SyncAttachV2Args
	if err := decodePublicArguments(json.RawMessage(raw), &destination); err == nil {
		t.Fatal("decodePublicArguments accepted sync version 1")
	}
}

func TestDecodePublicArgumentsRejectsWrongActivityVersion(t *testing.T) {
	raw := `{"version":2,"attachment_ref":"attachment","after_sequence":0,"limit":10}`
	var destination ActivityPullV1Args
	if err := decodePublicArguments(json.RawMessage(raw), &destination); err == nil {
		t.Fatal("decodePublicArguments accepted Activity version 2")
	}
}

func TestDecodePublicArgumentsAllowsDynamicKeysButRejectsDuplicates(t *testing.T) {
	type dynamicArgs struct {
		Version int               `json:"version" const:"1"`
		Labels  map[string]string `json:"labels"`
	}
	var valid dynamicArgs
	if err := decodePublicArguments(json.RawMessage(`{"version":1,"labels":{"arbitrary.key":"value"}}`), &valid); err != nil {
		t.Fatalf("decodePublicArguments rejected an arbitrary dynamic key: %v", err)
	}
	var duplicate dynamicArgs
	if err := decodePublicArguments(json.RawMessage(`{"version":1,"labels":{"arbitrary.key":"one","arbitrary.key":"two"}}`), &duplicate); err == nil {
		t.Fatal("decodePublicArguments accepted a duplicate dynamic key")
	}
}
