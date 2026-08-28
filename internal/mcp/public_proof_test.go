package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
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

func TestDecodePublicArgumentsRejectsPrimitiveNulls(t *testing.T) {
	type dynamicArgs struct {
		Version int               `json:"version" const:"1"`
		Labels  map[string]string `json:"labels"`
	}
	tests := []struct {
		name        string
		raw         string
		destination func() any
	}{
		{
			name:        "top-level required string",
			raw:         `{"version":2,"repository":{"provider":"github","immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":null,"base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			destination: func() any { return &SyncAttachV2Args{} },
		},
		{
			name:        "nested required string",
			raw:         `{"version":2,"repository":{"provider":null,"immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":"refs/heads/main","base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			destination: func() any { return &SyncAttachV2Args{} },
		},
		{
			name:        "dynamic map string value",
			raw:         `{"version":1,"labels":{"arbitrary.key":null}}`,
			destination: func() any { return &dynamicArgs{} },
		},
		{
			name:        "omitempty pointer scalar",
			raw:         syncPushArguments(`{"schema_version":1,"id":"operation","kind":"resurrect","expected_view_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","actor":{"actor_kind":"human","assurance":"public-key-continuity","occurred_at":"2026-08-28T12:00:00Z"},"resurrect":{"key":{"Kind":"task","ID":"record"},"expected_tombstone_digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","record":{},"kb_body":null}}`),
			destination: func() any { return &SyncPushV2Args{} },
		},
		{
			name:        "time string",
			raw:         syncPushArguments(`{"schema_version":1,"id":"operation","kind":"put_record","expected_view_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","actor":{"actor_kind":"human","assurance":"public-key-continuity","occurred_at":null}}`),
			destination: func() any { return &SyncPushV2Args{} },
		},
		{
			name:        "byte string",
			raw:         `{"Path":"records/file","Data":null}`,
			destination: func() any { return &projectstate.File{} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := decodePublicArguments(json.RawMessage(test.raw), test.destination()); err == nil {
				t.Fatal("decodePublicArguments accepted null for a non-nullable primitive schema")
			}
		})
	}
}

func TestDecodePublicArgumentsAcceptsCanonicalRequiredNullablePointers(t *testing.T) {
	t.Run("task record", func(t *testing.T) {
		rawTask, err := os.ReadFile("../types/projectstate/testdata/v1/valid/.wormhole/state/v1/tasks/22222222-2222-4222-8222-222222222222.json")
		if err != nil {
			t.Fatal(err)
		}
		var task projectstate.TaskV1
		if err := json.Unmarshal(rawTask, &task); err != nil {
			t.Fatal(err)
		}
		task.OwnerActorID = nil
		operation := canonicalPublicOperation(projectstate.OperationPutRecord)
		operation.PutRecord = &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Task: &task}}
		if _, err := projectstate.CanonicalOperation(operation); err != nil {
			t.Fatalf("CanonicalOperation rejected valid task: %v", err)
		}
		raw := marshalPublicArguments(t, publicSyncPushArguments(operation))
		for _, member := range [][]byte{[]byte(`"parent_task_id":null`), []byte(`"owner_actor_id":null`), []byte(`"due_by":null`)} {
			if !bytes.Contains(raw, member) {
				t.Fatalf("marshaled task lacks %s: %s", member, raw)
			}
		}
		var destination SyncPushV2Args
		if err := decodePublicArguments(raw, &destination); err != nil {
			t.Fatalf("decodePublicArguments rejected canonical task pointers: %v", err)
		}
	})

	t.Run("tombstone operation", func(t *testing.T) {
		operation := canonicalPublicOperation(projectstate.OperationTombstone)
		operation.Tombstone = &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"},
			ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}
		if _, err := projectstate.CanonicalOperation(operation); err != nil {
			t.Fatalf("CanonicalOperation rejected valid tombstone: %v", err)
		}
		raw := marshalPublicArguments(t, publicSyncPushArguments(operation))
		if !bytes.Contains(raw, []byte(`"expected_body_digest":null`)) {
			t.Fatalf("marshaled tombstone lacks required null digest: %s", raw)
		}
		var destination SyncPushV2Args
		if err := decodePublicArguments(raw, &destination); err != nil {
			t.Fatalf("decodePublicArguments rejected canonical tombstone pointer: %v", err)
		}
	})

	t.Run("activity event", func(t *testing.T) {
		occurredAt := time.Date(2026, 8, 29, 9, 30, 0, 0, time.UTC)
		activity := projectstate.ActivityV1{
			SchemaVersion: 1,
			ID:            "99999999-9999-4999-8999-999999999991",
			Class:         projectstate.ActivityOrdinaryV1,
			Actor: types.ActorEnvelope{
				ActorKind: types.ActorAgent, AgentID: "11111111-1111-4111-8111-111111111111",
				AccountableHumanID: "22222222-2222-4222-8222-222222222222", SessionID: "33333333-3333-4333-8333-333333333333",
				HarnessName: "codex", HarnessVersion: "1.0", ModelName: "gpt", ModelVersion: "5.6",
				Assurance: types.AssurancePublicKeyContinuity, OccurredAt: occurredAt,
			},
			Event: &projectstate.ActivityEventProjectionV1{
				ChannelID: "44444444-4444-4444-8444-444444444444", ActorID: "11111111-1111-4111-8111-111111111111",
				EventType: "task.status_changed",
				Payload:   json.RawMessage(`{"from_status":"wip","task_id":"55555555-5555-4555-8555-555555555555","to_status":"done"}`),
				Note:      nil, CreatedAt: occurredAt,
			},
			CreatedAt: occurredAt,
		}
		if _, err := projectstate.CanonicalActivity(activity); err != nil {
			t.Fatalf("CanonicalActivity rejected valid nil note: %v", err)
		}
		activityDigest, err := projectstate.DigestActivity(activity)
		if err != nil {
			t.Fatal(err)
		}
		raw := marshalPublicArguments(t, ActivityAcceptV1Args{
			Version: 1, AttachmentRef: "attachment", PolicyVersion: 1,
			PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Activity:     activity, ActivityDigest: activityDigest,
		})
		if !bytes.Contains(raw, []byte(`"note":null`)) {
			t.Fatalf("marshaled activity lacks required null note: %s", raw)
		}
		var destination ActivityAcceptV1Args
		if err := decodePublicArguments(raw, &destination); err != nil {
			t.Fatalf("decodePublicArguments rejected canonical activity pointer: %v", err)
		}
	})
}

func canonicalPublicOperation(kind projectstate.OperationKind) projectstate.OperationV1 {
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: kind,
		ExpectedViewDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 29, 9, 30, 0, 0, time.UTC),
		},
	}
}

func publicSyncPushArguments(operation projectstate.OperationV1) SyncPushV2Args {
	return SyncPushV2Args{
		SyncV2Scope: SyncV2Scope{
			Version: 2, AttachmentRef: "attachment",
			Repository:   types.RepositoryIdentity{Provider: "github", ImmutableID: "123", CanonicalRemote: "https://github.com/H4RL33/wormhole"},
			CanonicalRef: "refs/heads/main", BaseCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			BaseTreeDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ExpectedStreamVersion:  1,
			ExpectedLiveTreeDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		Operation: operation,
	}
}

func marshalPublicArguments(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestValidatePublicInputSchemaRejectsPrimitiveKindMismatches(t *testing.T) {
	for name, test := range map[string]struct {
		value  any
		schema map[string]any
	}{
		"string":  {value: json.Number("1"), schema: map[string]any{"type": "string"}},
		"integer": {value: "1", schema: map[string]any{"type": "integer"}},
		"number":  {value: true, schema: map[string]any{"type": "number"}},
		"boolean": {value: json.Number("1"), schema: map[string]any{"type": "boolean"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePublicInputSchema(test.value, test.schema); err == nil {
				t.Fatal("validatePublicInputSchema accepted an incompatible primitive kind")
			}
		})
	}
}

func TestValidatePublicInputSchemaLeavesUnconstrainedValuesOpen(t *testing.T) {
	for _, value := range []any{nil, "value", json.Number("1"), true, []any{}, map[string]any{}} {
		if err := validatePublicInputSchema(value, map[string]any{}); err != nil {
			t.Fatalf("validatePublicInputSchema rejected unconstrained value %#v: %v", value, err)
		}
	}
}

func syncPushArguments(operation string) string {
	return `{"version":2,"attachment_ref":"attachment","repository":{"provider":"github","immutable_id":"123","canonical_remote":"https://github.com/H4RL33/wormhole"},"canonical_ref":"refs/heads/main","base_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_stream_version":1,"expected_live_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","operation":` + operation + `}`
}
