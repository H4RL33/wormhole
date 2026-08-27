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
	restoreRequestGolden           = "{\"schema_version\":1,\"action\":\"restore\",\"scope\":{\"project_id\":\"00000000-0000-4000-8000-000000000001\",\"workspace_id\":\"00000000-0000-4000-8000-000000000002\"},\"actor\":{\"actor_kind\":\"human\",\"human_principal_id\":\"11111111-1111-4111-8111-111111111111\",\"assurance\":\"local\",\"occurred_at\":\"2026-07-29T12:34:56Z\"},\"stash_id\":\"33333333-3333-4333-8333-333333333333\"}\n"
	restoreCleanResultGolden       = "{\"restored_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"rebased_through_generation\":4,\"conflicts\":[],\"stash_retained\":false}\n"
	restoreCleanReceiptGolden      = "{\"schema_version\":1,\"action\":\"restore\",\"outcome\":\"clean\",\"result\":{\"restored_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"rebased_through_generation\":4,\"conflicts\":[],\"stash_retained\":false},\"conflict_retry_digest\":null}\n"
	restoreConflictedResultGolden  = "{\"restored_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"rebased_through_generation\":4,\"conflicts\":[{\"id\":\"sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92\",\"key\":{\"kind\":\"task\",\"id\":\"22222222-2222-4222-8222-222222222222\"},\"field_path\":\"/title\",\"kind\":\"same_field\",\"base\":{\"present\":true,\"value\":\"base\"},\"ours\":{\"present\":true,\"value\":\"ours\"},\"theirs\":{\"present\":true,\"value\":\"theirs\"}}],\"stash_retained\":true}\n"
	restoreConflictedReceiptGolden = "{\"schema_version\":1,\"action\":\"restore\",\"outcome\":\"conflicted\",\"result\":{\"restored_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"rebased_through_generation\":4,\"conflicts\":[{\"id\":\"sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92\",\"key\":{\"kind\":\"task\",\"id\":\"22222222-2222-4222-8222-222222222222\"},\"field_path\":\"/title\",\"kind\":\"same_field\",\"base\":{\"present\":true,\"value\":\"base\"},\"ours\":{\"present\":true,\"value\":\"ours\"},\"theirs\":{\"present\":true,\"value\":\"theirs\"}}],\"stash_retained\":true},\"conflict_retry_digest\":\"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\"}\n"
	restoreCleanReceiptV2Golden    = "{\"schema_version\":2,\"action\":\"restore\",\"outcome\":\"clean\",\"workspace_revision\":7,\"result\":{\"restored_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"rebased_through_generation\":4,\"conflicts\":[],\"stash_retained\":false},\"conflict_retry_digest\":null}\n"
	restoreConflictReceiptV2Golden = "{\"schema_version\":2,\"action\":\"restore\",\"outcome\":\"conflicted\",\"workspace_revision\":7,\"result\":{\"restored_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"rebased_through_generation\":4,\"conflicts\":[{\"id\":\"sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92\",\"key\":{\"kind\":\"task\",\"id\":\"22222222-2222-4222-8222-222222222222\"},\"field_path\":\"/title\",\"kind\":\"same_field\",\"base\":{\"present\":true,\"value\":\"base\"},\"ours\":{\"present\":true,\"value\":\"ours\"},\"theirs\":{\"present\":true,\"value\":\"theirs\"}}],\"stash_retained\":true},\"conflict_retry_digest\":\"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\"}\n"
)

func TestRestoreRequestDigestGolden(t *testing.T) {
	req := restoreCodecRequest()
	projection := restoreRequestDigestV1{
		SchemaVersion: 1, Action: "restore", Scope: req.Scope, Actor: req.Actor, StashID: req.StashID,
	}
	canonical, err := state.CanonicalJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != restoreRequestGolden {
		t.Fatalf("restore request preimage=%q, want %q", canonical, restoreRequestGolden)
	}
	const want = state.Digest("sha256:c26fbd6793650026aae2f5dfe9695b74e3b626b242c9757f27f220ce09532a7a")
	digest, err := state.DigestCanonicalJSON(projection)
	if err != nil || digest != want {
		t.Fatalf("projection digest=(%q,%v), want (%q,nil)", digest, err, want)
	}
	got, err := restoreRequestDigest(req)
	if err != nil || got != want {
		t.Fatalf("restoreRequestDigest()=(%q,%v), want (%q,nil)", got, err, want)
	}

	retry := req
	retry.RequestID = "99999999-9999-4999-8999-999999999992"
	if got, err := restoreRequestDigest(retry); err != nil || got != want {
		t.Fatalf("request ID changed digest=(%q,%v), want (%q,nil)", got, err, want)
	}
}

func TestRestoreRequestDigestValidatesRequest(t *testing.T) {
	valid := restoreCodecRequest()
	tests := []struct {
		name string
		edit func(*RestoreStashRequest)
	}{
		{"project ID", func(req *RestoreStashRequest) { req.Scope.ProjectID = "BAD" }},
		{"workspace ID", func(req *RestoreStashRequest) { req.Scope.WorkspaceID = "BAD" }},
		{"request ID", func(req *RestoreStashRequest) { req.RequestID = "BAD" }},
		{"stash ID", func(req *RestoreStashRequest) { req.StashID = "BAD" }},
		{"stash ID version", func(req *RestoreStashRequest) { req.StashID = "33333333-3333-1333-8333-333333333333" }},
		{"stash ID variant", func(req *RestoreStashRequest) { req.StashID = "33333333-3333-4333-7333-333333333333" }},
		{"actor", func(req *RestoreStashRequest) { req.Actor.Assurance = types.AssuranceUnknown }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.edit(&req)
			if got, err := restoreRequestDigest(req); err == nil || got != "" {
				t.Fatalf("restoreRequestDigest(invalid)=(%q,%v), want zero and error", got, err)
			}
		})
	}
}

func TestRestoreCleanReceiptGolden(t *testing.T) {
	result := restoreCodecCleanResult()
	private, err := privateRestoreResult(result)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreCodecGolden(t, private, restoreCleanResultGolden,
		"sha256:182ee97778efdbf2241d55c56da86678fe0569ab21cad114b5f6f1dad5657c4c")

	encoded, err := encodeCleanRestoreReceipt(result)
	if err != nil || string(encoded) != restoreCleanReceiptGolden {
		t.Fatalf("encodeCleanRestoreReceipt()=(%q,%v), want golden", encoded, err)
	}
	var retry *state.Digest
	envelope := restoreStashReceiptV1{
		SchemaVersion: 1, Action: "restore", Outcome: "clean", Result: private, ConflictRetryDigest: retry,
	}
	assertRestoreCodecGolden(t, envelope, restoreCleanReceiptGolden,
		"sha256:f319a6f232dd5e16dea58cf458e4c242773e80dfcd6efcd04cf56a1094f51198")
	if !bytes.Contains(encoded, []byte(`"conflict_retry_digest":null`)) {
		t.Fatalf("clean receipt omitted explicit null retry digest: %s", encoded)
	}
}

func TestRestoreConflictedReceiptGolden(t *testing.T) {
	result := restoreCodecConflictedResult(t)
	private, err := privateRestoreResult(result)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreCodecGolden(t, private, restoreConflictedResultGolden,
		"sha256:1f3090baa6437b12030f2361f67ea2347b127a142c14cc010414bbc7e6c850ff")

	retry := restoreCodecRetryDigest()
	encoded, err := encodeConflictedRestoreReceipt(result, retry)
	if err != nil || string(encoded) != restoreConflictedReceiptGolden {
		t.Fatalf("encodeConflictedRestoreReceipt()=(%q,%v), want golden", encoded, err)
	}
	envelope := restoreStashReceiptV1{
		SchemaVersion: 1, Action: "restore", Outcome: "conflicted", Result: private, ConflictRetryDigest: &retry,
	}
	assertRestoreCodecGolden(t, envelope, restoreConflictedReceiptGolden,
		"sha256:76cbbe824c0a4640ae8feaf2f541573ab4c7df7e0f88130bfab3ef2b6cacc672")
}

func TestRestoreReceiptV2GoldenStoresProjectedCommittedRevision(t *testing.T) {
	const revision int64 = 7
	clean, err := encodeCleanRestoreReceiptV2(restoreCodecCleanResult(), revision)
	if err != nil || string(clean) != restoreCleanReceiptV2Golden {
		t.Fatalf("encodeCleanRestoreReceiptV2()=(%q,%v), want golden", clean, err)
	}
	retry := restoreCodecRetryDigest()
	conflicted, err := encodeConflictedRestoreReceiptV2(restoreCodecConflictedResult(t), revision, retry)
	if err != nil || string(conflicted) != restoreConflictReceiptV2Golden {
		t.Fatalf("encodeConflictedRestoreReceiptV2()=(%q,%v), want golden", conflicted, err)
	}

	req := restoreCodecRequest()
	requestDigest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, outcome string
		raw           json.RawMessage
		wantRetry     bool
	}{
		{name: "clean", outcome: "clean", raw: clean},
		{name: "conflicted", outcome: "conflicted", raw: conflicted, wantRetry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := restoreCodecReceipt(t, req, test.outcome)
			row.ResultJSON = bytes.Clone(test.raw)
			decoded, err := decodeRestoreReceipt(row, req, requestDigest)
			if err != nil || decoded.SchemaVersion != 2 || decoded.WorkspaceRevision != revision ||
				(decoded.ConflictRetryDigest != nil) != test.wantRetry {
				t.Fatalf("decode v2 receipt=(%+v,%v)", decoded, err)
			}
		})
	}

	if got, err := encodeCleanRestoreReceiptV2(restoreCodecCleanResult(), 0); err == nil || got != nil {
		t.Fatalf("zero revision v2 receipt=(%q,%v), want nil,error", got, err)
	}
}

func TestEncodeRestoreReceiptInvariantMatrix(t *testing.T) {
	clean := restoreCodecCleanResult()
	conflicted := restoreCodecConflictedResult(t)
	retry := restoreCodecRetryDigest()
	if got, err := encodeCleanRestoreReceipt(clean); err != nil || got == nil {
		t.Fatalf("valid clean receipt=(%q,%v)", got, err)
	}
	if got, err := encodeConflictedRestoreReceipt(conflicted, retry); err != nil || got == nil {
		t.Fatalf("valid conflicted receipt=(%q,%v)", got, err)
	}

	tests := []struct {
		name       string
		conflicted bool
		result     RestoreStashResult
		retry      state.Digest
	}{
		{name: "clean nil conflicts", result: func() RestoreStashResult { value := clean; value.Conflicts = nil; return value }()},
		{name: "clean conflict", result: func() RestoreStashResult { value := clean; value.Conflicts = conflicted.Conflicts; return value }()},
		{name: "clean retained", result: func() RestoreStashResult { value := clean; value.StashRetained = true; return value }()},
		{name: "conflicted nil conflicts", conflicted: true, result: func() RestoreStashResult { value := conflicted; value.Conflicts = nil; return value }(), retry: retry},
		{name: "conflicted empty conflicts", conflicted: true, result: func() RestoreStashResult { value := conflicted; value.Conflicts = []Conflict{}; return value }(), retry: retry},
		{name: "conflicted not retained", conflicted: true, result: func() RestoreStashResult { value := conflicted; value.StashRetained = false; return value }(), retry: retry},
		{name: "conflicted retry digest", conflicted: true, result: conflicted, retry: "BAD"},
		{name: "restored digest", result: func() RestoreStashResult { value := clean; value.RestoredDigest = "BAD"; return value }()},
		{name: "generation", result: func() RestoreStashResult { value := clean; value.RebasedThroughGeneration = -1; return value }()},
		{name: "wrong conflict ID", conflicted: true, result: func() RestoreStashResult {
			value := cloneRestoreCodecResult(conflicted)
			value.Conflicts[0].ID = string(restoreCodecRetryDigest())
			return value
		}(), retry: retry},
		{name: "noncanonical conflict value", conflicted: true, result: func() RestoreStashResult {
			value := cloneRestoreCodecResult(conflicted)
			value.Conflicts[0].Base.Value = []byte(" \"base\"")
			return value
		}(), retry: retry},
		{name: "unsorted conflicts", conflicted: true, result: restoreCodecUnsortedResult(t), retry: retry},
		{name: "duplicate conflicts", conflicted: true, result: func() RestoreStashResult {
			value := conflicted
			value.Conflicts = []Conflict{value.Conflicts[0], value.Conflicts[0]}
			return value
		}(), retry: retry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got json.RawMessage
			var err error
			if test.conflicted {
				got, err = encodeConflictedRestoreReceipt(test.result, test.retry)
			} else {
				got, err = encodeCleanRestoreReceipt(test.result)
			}
			if err == nil || got != nil {
				t.Fatalf("encode invalid receipt=(%q,%v), want nil and error", got, err)
			}
		})
	}
}

func TestDecodeRestoreReceiptStrictlyMatchesNeutralRecord(t *testing.T) {
	req := restoreCodecRequest()
	valid := restoreCodecReceipt(t, req, "clean")
	wantDigest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRestoreReceipt(valid, req, wantDigest)
	if err != nil || decoded.Outcome != "clean" || decoded.ConflictRetryDigest != nil ||
		!reflect.DeepEqual(decoded.Result, restoreCodecCleanResult()) || decoded.Result.Conflicts == nil {
		t.Fatalf("decodeRestoreReceipt()=(%+v,%v)", decoded, err)
	}
	conflicted := restoreCodecReceipt(t, req, "conflicted")
	decodedConflict, err := decodeRestoreReceipt(conflicted, req, wantDigest)
	if err != nil || decodedConflict.Outcome != "conflicted" || decodedConflict.ConflictRetryDigest == nil ||
		*decodedConflict.ConflictRetryDigest != restoreCodecRetryDigest() ||
		!reflect.DeepEqual(decodedConflict.Result, restoreCodecConflictedResult(t)) {
		t.Fatalf("decode conflicted restore receipt=(%+v,%v)", decodedConflict, err)
	}

	otherActor := req.Actor
	otherActor.HumanPrincipalID = "44444444-4444-4444-8444-444444444444"
	tests := []struct {
		name string
		edit func(*localstore.WorkspaceTransitionReceiptRecord, *RestoreStashRequest, *state.Digest)
	}{
		{"request ID", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.RequestID = "99999999-9999-4999-8999-999999999992"
		}},
		{"noncanonical request ID", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.RequestID = "BAD"
		}},
		{"action", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.Action = "stash"
		}},
		{"row digest", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.RequestDigest = state.Digest("sha256:" + strings.Repeat("e", 64))
		}},
		{"malformed row digest", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.RequestDigest = "BAD"
		}},
		{"supplied digest", func(_ *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, digest *state.Digest) {
			*digest = state.Digest("sha256:" + strings.Repeat("e", 64))
		}},
		{"malformed supplied digest", func(_ *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, digest *state.Digest) {
			*digest = "BAD"
		}},
		{"actor mismatch", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.Actor = otherActor
		}},
		{"invalid actor", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.Actor = types.ActorEnvelope{}
		}},
		{"row outcome", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.Outcome = "conflicted"
		}},
		{"unknown row outcome", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.Outcome = "unknown"
		}},
		{"zero timestamp", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.CreatedAt = time.Time{}
		}},
		{"non-UTC timestamp", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *RestoreStashRequest, _ *state.Digest) {
			row.CreatedAt = time.Date(2026, 7, 29, 13, 0, 0, 0, time.FixedZone("offset", 60))
		}},
		{"changed request", func(_ *localstore.WorkspaceTransitionReceiptRecord, request *RestoreStashRequest, _ *state.Digest) {
			request.StashID = "55555555-5555-4555-8555-555555555555"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := cloneRestoreCodecReceipt(valid)
			request, digest := req, wantDigest
			test.edit(row, &request, &digest)
			if got, err := decodeRestoreReceipt(row, request, digest); err == nil || !reflect.DeepEqual(got, decodedRestoreReceipt{}) {
				t.Fatalf("decode mismatch=(%+v,%v), want zero and error", got, err)
			}
		})
	}
	if got, err := decodeRestoreReceipt(nil, req, wantDigest); err == nil || !reflect.DeepEqual(got, decodedRestoreReceipt{}) {
		t.Fatalf("decode nil=(%+v,%v), want zero and error", got, err)
	}
}

func TestDecodeRestoreReceiptRejectsStrictJSONAndInvariantMatrix(t *testing.T) {
	req := restoreCodecRequest()
	digest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	cleanPrivate, err := privateRestoreResult(restoreCodecCleanResult())
	if err != nil {
		t.Fatal(err)
	}
	conflictedPrivate, err := privateRestoreResult(restoreCodecConflictedResult(t))
	if err != nil {
		t.Fatal(err)
	}
	retry := restoreCodecRetryDigest()
	cleanEnvelope := restoreStashReceiptV1{SchemaVersion: 1, Action: "restore", Outcome: "clean", Result: cleanPrivate}
	conflictedEnvelope := restoreStashReceiptV1{SchemaVersion: 1, Action: "restore", Outcome: "conflicted", Result: conflictedPrivate, ConflictRetryDigest: &retry}

	tests := []struct {
		name    string
		outcome string
		raw     func() []byte
	}{
		{"nil", "clean", func() []byte { return nil }},
		{"unknown field", "clean", func() []byte {
			return []byte(strings.TrimSuffix(restoreCleanReceiptGolden, "}\n") + ",\"unknown\":true}\n")
		}},
		{"inner unknown field", "clean", func() []byte {
			return []byte(strings.Replace(restoreCleanReceiptGolden, "\"stash_retained\":false", "\"stash_retained\":false,\"unknown\":true", 1))
		}},
		{"trailing JSON", "clean", func() []byte { return []byte(restoreCleanReceiptGolden + "{}\n") }},
		{"noncanonical bytes", "clean", func() []byte { return []byte(strings.TrimSuffix(restoreCleanReceiptGolden, "\n")) }},
		{"missing explicit retry field", "clean", func() []byte {
			return []byte(strings.Replace(restoreCleanReceiptGolden, ",\"conflict_retry_digest\":null", "", 1))
		}},
		{"schema", "clean", func() []byte { value := cleanEnvelope; value.SchemaVersion = 2; return restoreCodecCanonical(t, value) }},
		{"action", "clean", func() []byte { value := cleanEnvelope; value.Action = "stash"; return restoreCodecCanonical(t, value) }},
		{"envelope outcome", "clean", func() []byte {
			value := cleanEnvelope
			value.Outcome = "conflicted"
			return restoreCodecCanonical(t, value)
		}},
		{"clean retry digest", "clean", func() []byte {
			value := cleanEnvelope
			value.ConflictRetryDigest = &retry
			return restoreCodecCanonical(t, value)
		}},
		{"clean null conflicts", "clean", func() []byte {
			value := cleanEnvelope
			value.Result.Conflicts = nil
			return restoreCodecCanonical(t, value)
		}},
		{"clean conflict", "clean", func() []byte {
			value := cleanEnvelope
			value.Result.Conflicts = cloneRestoreCodecPrivateResult(conflictedPrivate).Conflicts
			return restoreCodecCanonical(t, value)
		}},
		{"clean retained", "clean", func() []byte {
			value := cleanEnvelope
			value.Result.StashRetained = true
			return restoreCodecCanonical(t, value)
		}},
		{"conflicted retry null", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.ConflictRetryDigest = nil
			return restoreCodecCanonical(t, value)
		}},
		{"conflicted retry malformed", "conflicted", func() []byte {
			value, malformed := conflictedEnvelope, state.Digest("BAD")
			value.ConflictRetryDigest = &malformed
			return restoreCodecCanonical(t, value)
		}},
		{"conflicted null conflicts", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.Result.Conflicts = nil
			return restoreCodecCanonical(t, value)
		}},
		{"conflicted empty conflicts", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.Result.Conflicts = []transitionConflictV1{}
			return restoreCodecCanonical(t, value)
		}},
		{"conflicted not retained", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.Result.StashRetained = false
			return restoreCodecCanonical(t, value)
		}},
		{"negative generation", "clean", func() []byte {
			value := cleanEnvelope
			value.Result.RebasedThroughGeneration = -1
			return restoreCodecCanonical(t, value)
		}},
		{"bad restored digest", "clean", func() []byte {
			value := cleanEnvelope
			value.Result.RestoredDigest = "BAD"
			return restoreCodecCanonical(t, value)
		}},
		{"bad conflict ID", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.Result = cloneRestoreCodecPrivateResult(value.Result)
			value.Result.Conflicts[0].ID = string(restoreCodecRetryDigest())
			return restoreCodecCanonical(t, value)
		}},
		{"noncanonical conflict value", "conflicted", func() []byte {
			return []byte(strings.Replace(restoreConflictedReceiptGolden,
				`"base":{"present":true,"value":"base"}`,
				`"base":{"present":true,"value": "base"}`, 1))
		}},
		{"unsorted conflicts", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.Result = restoreCodecPrivateUnsortedResult(t)
			return restoreCodecCanonical(t, value)
		}},
		{"duplicate conflicts", "conflicted", func() []byte {
			value := conflictedEnvelope
			value.Result.Conflicts = append(value.Result.Conflicts, value.Result.Conflicts[0])
			return restoreCodecCanonical(t, value)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := restoreCodecReceipt(t, req, test.outcome)
			row.ResultJSON = test.raw()
			if got, err := decodeRestoreReceipt(row, req, digest); err == nil || !reflect.DeepEqual(got, decodedRestoreReceipt{}) {
				t.Fatalf("decode corrupt=(%+v,%v), want zero and error", got, err)
			}
		})
	}
}

func TestRestoreReceiptCodecOwnsConflictBytes(t *testing.T) {
	result := restoreCodecConflictedResult(t)
	retry := restoreCodecRetryDigest()
	encoded, err := encodeConflictedRestoreReceipt(result, retry)
	if err != nil {
		t.Fatal(err)
	}
	wantEncoded := bytes.Clone(encoded)
	result.Conflicts[0].Base.Value[0] ^= 1
	if !bytes.Equal(encoded, wantEncoded) {
		t.Fatal("encoded receipt aliases caller conflict bytes")
	}

	req := restoreCodecRequest()
	row := restoreCodecReceipt(t, req, "conflicted")
	digest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRestoreReceipt(row, req, digest)
	if err != nil {
		t.Fatal(err)
	}
	wantRow := bytes.Clone(row.ResultJSON)
	decoded.Result.Conflicts[0].Base.Value[0] ^= 1
	*decoded.ConflictRetryDigest = state.Digest("sha256:" + strings.Repeat("e", 64))
	if !bytes.Equal(row.ResultJSON, wantRow) {
		t.Fatal("decoded receipt aliases persisted JSON")
	}
}

func restoreCodecRequest() RestoreStashRequest {
	return RestoreStashRequest{
		Scope: types.WorkspaceScope{
			ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000002",
		},
		RequestID: "99999999-9999-4999-8999-999999999991",
		StashID:   "33333333-3333-4333-8333-333333333333",
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
		},
	}
}

func restoreCodecCleanResult() RestoreStashResult {
	return RestoreStashResult{
		RestoredDigest:           state.Digest("sha256:" + strings.Repeat("c", 64)),
		RebasedThroughGeneration: 4, Conflicts: []Conflict{}, StashRetained: false,
	}
}

func restoreCodecConflictedResult(t *testing.T) RestoreStashResult {
	t.Helper()
	conflict, err := newConflict(state.RecordKey{Kind: "task", ID: composeTaskID}, "/title", ConflictSameField,
		codecField(`"base"`), codecField(`"ours"`), codecField(`"theirs"`))
	if err != nil {
		t.Fatal(err)
	}
	return RestoreStashResult{
		RestoredDigest:           state.Digest("sha256:" + strings.Repeat("c", 64)),
		RebasedThroughGeneration: 4, Conflicts: []Conflict{conflict}, StashRetained: true,
	}
}

func restoreCodecUnsortedResult(t *testing.T) RestoreStashResult {
	t.Helper()
	result := restoreCodecConflictedResult(t)
	second, err := newConflict(state.RecordKey{Kind: "task", ID: diffSecondTaskID}, "/title", ConflictSameField,
		codecField(`"base"`), codecField(`"ours"`), codecField(`"theirs"`))
	if err != nil {
		t.Fatal(err)
	}
	result.Conflicts = []Conflict{second, result.Conflicts[0]}
	return result
}

func restoreCodecPrivateUnsortedResult(t *testing.T) restoreStashResultV1 {
	t.Helper()
	result := restoreCodecUnsortedResult(t)
	private := restoreStashResultV1{
		RestoredDigest: result.RestoredDigest, RebasedThroughGeneration: result.RebasedThroughGeneration,
		Conflicts: make([]transitionConflictV1, len(result.Conflicts)), StashRetained: true,
	}
	for index, conflict := range result.Conflicts {
		private.Conflicts[index] = transitionConflictV1{
			ID: conflict.ID, Key: transitionRecordKeyV1{Kind: conflict.Key.Kind, ID: conflict.Key.ID},
			FieldPath: conflict.FieldPath, Kind: conflict.Kind,
			Base: conflict.Base, Ours: conflict.Ours, Theirs: conflict.Theirs,
		}
	}
	return private
}

func restoreCodecRetryDigest() state.Digest {
	return state.Digest("sha256:" + strings.Repeat("d", 64))
}

func cloneRestoreCodecResult(value RestoreStashResult) RestoreStashResult {
	cloned := value
	if value.Conflicts == nil {
		return cloned
	}
	cloned.Conflicts = make([]Conflict, len(value.Conflicts))
	for index, conflict := range value.Conflicts {
		cloned.Conflicts[index] = conflict
		cloned.Conflicts[index].Base.Value = bytes.Clone(conflict.Base.Value)
		cloned.Conflicts[index].Ours.Value = bytes.Clone(conflict.Ours.Value)
		cloned.Conflicts[index].Theirs.Value = bytes.Clone(conflict.Theirs.Value)
	}
	return cloned
}

func cloneRestoreCodecPrivateResult(value restoreStashResultV1) restoreStashResultV1 {
	cloned := value
	if value.Conflicts == nil {
		return cloned
	}
	cloned.Conflicts = make([]transitionConflictV1, len(value.Conflicts))
	for index, conflict := range value.Conflicts {
		cloned.Conflicts[index] = conflict
		cloned.Conflicts[index].Base.Value = bytes.Clone(conflict.Base.Value)
		cloned.Conflicts[index].Ours.Value = bytes.Clone(conflict.Ours.Value)
		cloned.Conflicts[index].Theirs.Value = bytes.Clone(conflict.Theirs.Value)
	}
	return cloned
}

func restoreCodecReceipt(t *testing.T, req RestoreStashRequest, outcome string) *localstore.WorkspaceTransitionReceiptRecord {
	t.Helper()
	digest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	switch outcome {
	case "clean":
		raw = json.RawMessage(restoreCleanReceiptGolden)
	case "conflicted":
		raw = json.RawMessage(restoreConflictedReceiptGolden)
	default:
		t.Fatalf("unsupported test outcome %q", outcome)
	}
	return &localstore.WorkspaceTransitionReceiptRecord{
		WorkspaceTransitionReceiptInsert: localstore.WorkspaceTransitionReceiptInsert{
			RequestID: req.RequestID, Action: "restore", RequestDigest: digest,
			Actor: req.Actor, ResultJSON: bytes.Clone(raw), Outcome: outcome,
		},
		CreatedAt: time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC),
	}
}

func cloneRestoreCodecReceipt(value *localstore.WorkspaceTransitionReceiptRecord) *localstore.WorkspaceTransitionReceiptRecord {
	clone := *value
	clone.ResultJSON = bytes.Clone(value.ResultJSON)
	return &clone
}

func restoreCodecCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertRestoreCodecGolden(t *testing.T, value any, wantJSON string, wantDigest state.Digest) {
	t.Helper()
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != wantJSON {
		t.Fatalf("canonical JSON=%q, want %q", raw, wantJSON)
	}
	digest, err := state.DigestCanonicalJSON(value)
	if err != nil || digest != wantDigest {
		t.Fatalf("canonical digest=(%q,%v), want (%q,nil)", digest, err, wantDigest)
	}
}
