package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	stashRequestGolden = "{\"schema_version\":1,\"action\":\"stash\",\"scope\":{\"project_id\":\"00000000-0000-4000-8000-000000000001\",\"workspace_id\":\"00000000-0000-4000-8000-000000000002\"},\"actor\":{\"actor_kind\":\"human\",\"human_principal_id\":\"11111111-1111-4111-8111-111111111111\",\"assurance\":\"local\",\"occurred_at\":\"2026-07-29T12:34:56Z\"},\"label\":\"branch work\"}\n"
	stashResultGolden  = "{\"stash_id\":\"22222222-2222-4222-8222-222222222222\",\"source_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"candidate_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"operation_count\":3}\n"
	stashReceiptGolden = "{\"schema_version\":1,\"action\":\"stash\",\"outcome\":\"clean\",\"result\":{\"stash_id\":\"22222222-2222-4222-8222-222222222222\",\"source_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"candidate_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"operation_count\":3}}\n"
)

func TestStashRequestDigestGolden(t *testing.T) {
	req := stashCodecRequest()
	projection := stashRequestDigestV1{
		SchemaVersion: 1, Action: "stash", Scope: req.Scope, Actor: req.Actor, Label: req.Label,
	}
	canonical, err := state.CanonicalJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != stashRequestGolden {
		t.Fatalf("stash request preimage=%q, want %q", canonical, stashRequestGolden)
	}
	digest, err := state.DigestCanonicalJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	const want = state.Digest("sha256:0d8e9cad2b3831420d6637c931c0fbb8bb2ec4a3affe7b56ec9638bd2580f366")
	if digest != want {
		t.Fatalf("stash request projection digest=%q, want %q", digest, want)
	}
	got, err := stashRequestDigest(req)
	if err != nil || got != want {
		t.Fatalf("stashRequestDigest()=(%q,%v), want (%q,nil)", got, err, want)
	}

	retry := req
	retry.RequestID = "33333333-3333-4333-8333-333333333333"
	if got, err := stashRequestDigest(retry); err != nil || got != want {
		t.Fatalf("request ID changed digest=(%q,%v), want (%q,nil)", got, err, want)
	}
}

func TestStashRequestDigestValidatesRequest(t *testing.T) {
	valid := stashCodecRequest()
	boundary := valid
	boundary.Label = strings.Repeat("x", 256)
	if got, err := stashRequestDigest(boundary); err != nil || got == "" {
		t.Fatalf("256-byte label digest=(%q,%v), want non-zero nil", got, err)
	}

	tests := []struct {
		name string
		edit func(*StashRequest)
	}{
		{"project ID", func(req *StashRequest) { req.Scope.ProjectID = "BAD" }},
		{"workspace ID", func(req *StashRequest) { req.Scope.WorkspaceID = "BAD" }},
		{"request ID", func(req *StashRequest) { req.RequestID = "BAD" }},
		{"actor", func(req *StashRequest) { req.Actor.Assurance = types.AssuranceUnknown }},
		{"empty label", func(req *StashRequest) { req.Label = "" }},
		{"oversize label", func(req *StashRequest) { req.Label = strings.Repeat("x", 257) }},
		{"NUL label", func(req *StashRequest) { req.Label = "a\x00b" }},
		{"CR label", func(req *StashRequest) { req.Label = "a\rb" }},
		{"LF label", func(req *StashRequest) { req.Label = "a\nb" }},
		{"invalid UTF-8 label", func(req *StashRequest) { req.Label = string([]byte{0xff}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.edit(&req)
			if got, err := stashRequestDigest(req); err == nil || got != "" {
				t.Fatalf("stashRequestDigest(invalid)=(%q,%v), want zero and error", got, err)
			}
		})
	}
}

func TestStashResultAndReceiptGoldens(t *testing.T) {
	result := stashCodecResult()
	privateResult := stashResultV1{
		StashID: result.StashID, SourceDigest: result.SourceDigest,
		CandidateDigest: result.CandidateDigest, OperationCount: result.OperationCount,
	}
	resultJSON, err := state.CanonicalJSON(privateResult)
	if err != nil {
		t.Fatal(err)
	}
	if string(resultJSON) != stashResultGolden {
		t.Fatalf("stash result=%q, want %q", resultJSON, stashResultGolden)
	}
	resultDigest, err := state.DigestCanonicalJSON(privateResult)
	if err != nil {
		t.Fatal(err)
	}
	const wantResultDigest = state.Digest("sha256:5cb50fd889109d6a84784f7c13bec414fa2ac98fa36f621a0313af8c8eddbe93")
	if resultDigest != wantResultDigest {
		t.Fatalf("stash result digest=%q, want %q", resultDigest, wantResultDigest)
	}

	receiptJSON, err := encodeStashReceipt(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(receiptJSON) != stashReceiptGolden {
		t.Fatalf("stash receipt=%q, want %q", receiptJSON, stashReceiptGolden)
	}
	receiptDigest, err := state.DigestCanonicalJSON(stashReceiptV1{
		SchemaVersion: 1, Action: "stash", Outcome: "clean", Result: privateResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantReceiptDigest = state.Digest("sha256:8c8db87d3cd9ffe837919d47704290ac22d8343d7a237bf592b5f6131f3861e2")
	if receiptDigest != wantReceiptDigest {
		t.Fatalf("stash receipt digest=%q, want %q", receiptDigest, wantReceiptDigest)
	}

	req := stashCodecRequest()
	receipt := stashCodecReceipt(t, req)
	got, err := decodeStashReceipt(receipt, req)
	if err != nil || !reflect.DeepEqual(got, result) {
		t.Fatalf("decodeStashReceipt()=(%+v,%v), want (%+v,nil)", got, err, result)
	}
	receipt.ResultJSON[0] = '['
	if !bytes.Equal(receiptJSON, []byte(stashReceiptGolden)) {
		t.Fatal("encoded receipt aliases decoded or caller-owned bytes")
	}
}

func TestEncodeStashReceiptValidatesResult(t *testing.T) {
	valid := stashCodecResult()
	zeroOperations := valid
	zeroOperations.OperationCount = 0
	if got, err := encodeStashReceipt(zeroOperations); err != nil || len(got) == 0 {
		t.Fatalf("zero-operation receipt=(%q,%v), want canonical receipt", got, err)
	}

	tests := []struct {
		name string
		edit func(*StashResult)
	}{
		{"stash ID", func(result *StashResult) { result.StashID = "BAD" }},
		{"stash ID version", func(result *StashResult) { result.StashID = "22222222-2222-1222-8222-222222222222" }},
		{"source digest", func(result *StashResult) { result.SourceDigest = "sha256:ABC" }},
		{"candidate digest", func(result *StashResult) { result.CandidateDigest = "sha256:ABC" }},
		{"operation count", func(result *StashResult) { result.OperationCount = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			test.edit(&result)
			if got, err := encodeStashReceipt(result); err == nil || got != nil {
				t.Fatalf("encodeStashReceipt(invalid)=(%q,%v), want nil and error", got, err)
			}
		})
	}
}

func TestDecodeStashReceiptRejectsNeutralRecordMismatch(t *testing.T) {
	req := stashCodecRequest()
	valid := stashCodecReceipt(t, req)
	otherActor := req.Actor
	otherActor.HumanPrincipalID = "44444444-4444-4444-8444-444444444444"

	tests := []struct {
		name string
		edit func(*localstore.WorkspaceTransitionReceiptRecord, *StashRequest)
	}{
		{"request ID", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.RequestID = "33333333-3333-4333-8333-333333333333"
		}},
		{"malformed request ID", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) { receipt.RequestID = "BAD" }},
		{"action", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.Action = "restore"
		}},
		{"request digest", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.RequestDigest = state.Digest("sha256:" + strings.Repeat("c", 64))
		}},
		{"malformed request digest", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.RequestDigest = "BAD"
		}},
		{"actor mismatch", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.Actor = otherActor
		}},
		{"invalid actor", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.Actor = types.ActorEnvelope{}
		}},
		{"outcome", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.Outcome = "conflicted"
		}},
		{"zero timestamp", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.CreatedAt = time.Time{}
		}},
		{"non-UTC timestamp", func(receipt *localstore.WorkspaceTransitionReceiptRecord, _ *StashRequest) {
			receipt.CreatedAt = time.Date(2026, 7, 29, 12, 0, 0, 0, time.FixedZone("offset", 60))
		}},
		{"request changed", func(_ *localstore.WorkspaceTransitionReceiptRecord, req *StashRequest) { req.Label = "different" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt := cloneStashCodecReceipt(valid)
			request := req
			test.edit(receipt, &request)
			if got, err := decodeStashReceipt(receipt, request); err == nil || !reflect.DeepEqual(got, StashResult{}) {
				t.Fatalf("decodeStashReceipt(invalid)=(%+v,%v), want zero and error", got, err)
			}
		})
	}
	if got, err := decodeStashReceipt(nil, req); err == nil || !reflect.DeepEqual(got, StashResult{}) {
		t.Fatalf("decodeStashReceipt(nil)=(%+v,%v), want zero and error", got, err)
	}
}

func TestDecodeStashReceiptRejectsStrictJSONAndResultCorruption(t *testing.T) {
	req := stashCodecRequest()
	valid := stashCodecReceipt(t, req)
	canonicalEnvelope := stashReceiptV1{
		SchemaVersion: 1, Action: "stash", Outcome: "clean",
		Result: stashResultV1{
			StashID: stashCodecResult().StashID, SourceDigest: stashCodecResult().SourceDigest,
			CandidateDigest: stashCodecResult().CandidateDigest, OperationCount: stashCodecResult().OperationCount,
		},
	}

	tests := []struct {
		name string
		raw  func() []byte
	}{
		{"nil", func() []byte { return nil }},
		{"unknown field", func() []byte { return []byte(strings.TrimSuffix(stashReceiptGolden, "}\n") + ",\"unknown\":true}\n") }},
		{"inner unknown field", func() []byte {
			return []byte(strings.Replace(stashReceiptGolden, "\"operation_count\":3", "\"operation_count\":3,\"unknown\":true", 1))
		}},
		{"trailing JSON", func() []byte { return []byte(stashReceiptGolden + "{}\n") }},
		{"noncanonical bytes", func() []byte { return []byte(strings.TrimSuffix(stashReceiptGolden, "\n")) }},
		{"schema version", func() []byte {
			value := canonicalEnvelope
			value.SchemaVersion = 2
			return stashCodecCanonical(t, value)
		}},
		{"envelope action", func() []byte {
			value := canonicalEnvelope
			value.Action = "restore"
			return stashCodecCanonical(t, value)
		}},
		{"envelope outcome", func() []byte {
			value := canonicalEnvelope
			value.Outcome = "conflicted"
			return stashCodecCanonical(t, value)
		}},
		{"stash ID", func() []byte {
			value := canonicalEnvelope
			value.Result.StashID = "BAD"
			return stashCodecCanonical(t, value)
		}},
		{"source digest", func() []byte {
			value := canonicalEnvelope
			value.Result.SourceDigest = "BAD"
			return stashCodecCanonical(t, value)
		}},
		{"candidate digest", func() []byte {
			value := canonicalEnvelope
			value.Result.CandidateDigest = "BAD"
			return stashCodecCanonical(t, value)
		}},
		{"operation count", func() []byte {
			value := canonicalEnvelope
			value.Result.OperationCount = -1
			return stashCodecCanonical(t, value)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt := cloneStashCodecReceipt(valid)
			receipt.ResultJSON = test.raw()
			if got, err := decodeStashReceipt(receipt, req); err == nil || !reflect.DeepEqual(got, StashResult{}) {
				t.Fatalf("decodeStashReceipt(corrupt)=(%+v,%v), want zero and error", got, err)
			}
		})
	}
}

func stashCodecRequest() StashRequest {
	return StashRequest{
		Scope: types.WorkspaceScope{
			ProjectID:   "00000000-0000-4000-8000-000000000001",
			WorkspaceID: "00000000-0000-4000-8000-000000000002",
		},
		RequestID: "99999999-9999-4999-8999-999999999991",
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
		},
		Label: "branch work",
	}
}

func stashCodecResult() StashResult {
	return StashResult{
		StashID:         "22222222-2222-4222-8222-222222222222",
		SourceDigest:    state.Digest("sha256:" + strings.Repeat("a", 64)),
		CandidateDigest: state.Digest("sha256:" + strings.Repeat("b", 64)),
		OperationCount:  3,
	}
}

func stashCodecReceipt(t *testing.T, req StashRequest) *localstore.WorkspaceTransitionReceiptRecord {
	t.Helper()
	digest, err := stashRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	return &localstore.WorkspaceTransitionReceiptRecord{
		WorkspaceTransitionReceiptInsert: localstore.WorkspaceTransitionReceiptInsert{
			RequestID: req.RequestID, Action: "stash", RequestDigest: digest, Actor: req.Actor,
			ResultJSON: json.RawMessage(stashReceiptGolden), Outcome: "clean",
		},
		CreatedAt: time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC),
	}
}

func cloneStashCodecReceipt(value *localstore.WorkspaceTransitionReceiptRecord) *localstore.WorkspaceTransitionReceiptRecord {
	clone := *value
	clone.ResultJSON = bytes.Clone(value.ResultJSON)
	return &clone
}

func stashCodecCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
