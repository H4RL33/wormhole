package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	discardRequestGolden = "{\"schema_version\":1,\"action\":\"discard\",\"scope\":{\"project_id\":\"00000000-0000-4000-8000-000000000001\",\"workspace_id\":\"00000000-0000-4000-8000-000000000002\"},\"actor\":{\"actor_kind\":\"human\",\"human_principal_id\":\"11111111-1111-4111-8111-111111111111\",\"assurance\":\"local\",\"occurred_at\":\"2026-07-29T12:34:56Z\"},\"expected_binding\":{\"scope\":{\"project_id\":\"00000000-0000-4000-8000-000000000001\",\"workspace_id\":\"00000000-0000-4000-8000-000000000002\"},\"checkout\":{\"canonical_path\":\"/checkout\",\"device\":1,\"inode\":2},\"repository\":{\"provider\":\"github\",\"immutable_id\":\"wormhole-id\",\"canonical_remote\":\"https://github.com/H4RL33/wormhole\"},\"accepted_ref\":\"refs/heads/main\",\"accepted_commit_sha\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"accepted_tree_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"},\"canonical_root\":\"/checkout\",\"expected_commit\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"}\n"
	discardResultGolden  = "{\"previous_commit\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"observed_commit\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"previous_ref\":\"refs/heads/main\",\"observed_ref\":\"refs/heads/next\",\"previous_base_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"observed_base_digest\":\"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\",\"candidate_accepted\":false,\"accepted_journal_id\":null,\"rebased\":false,\"conflicts\":[]}\n"
	discardReceiptGolden = "{\"schema_version\":1,\"action\":\"discard\",\"outcome\":\"clean\",\"result\":{\"previous_commit\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"observed_commit\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"previous_ref\":\"refs/heads/main\",\"observed_ref\":\"refs/heads/next\",\"previous_base_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"observed_base_digest\":\"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\",\"candidate_accepted\":false,\"accepted_journal_id\":null,\"rebased\":false,\"conflicts\":[]}}\n"
)

func TestDiscardRequestDigestGolden(t *testing.T) {
	req := discardCodecRequest()
	projection, err := discardRequestDigestProjection(req)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := state.CanonicalJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != discardRequestGolden {
		t.Fatalf("discard request preimage=%q, want %q", canonical, discardRequestGolden)
	}
	const want = state.Digest("sha256:78a41316a2fb1c586f591565e86d1599aac429a9ea24f4a0bc8998cb766d9dd5")
	digest, err := state.DigestCanonicalJSON(projection)
	if err != nil || digest != want {
		t.Fatalf("projection digest=(%q,%v), want (%q,nil)", digest, err, want)
	}
	got, err := discardRequestDigest(req)
	if err != nil || got != want {
		t.Fatalf("discardRequestDigest()=(%q,%v), want (%q,nil)", got, err, want)
	}

	retry := req
	retry.RequestID = "99999999-9999-4999-8999-999999999992"
	if got, err := discardRequestDigest(retry); err != nil || got != want {
		t.Fatalf("request ID changed digest=(%q,%v), want (%q,nil)", got, err, want)
	}
}

func TestDiscardRequestDigestValidationAndBindingCoverage(t *testing.T) {
	valid := discardCodecRequest()
	base, err := discardRequestDigest(valid)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*ObserveGitBaseRequest)
	}{
		{"branch action", func(req *ObserveGitBaseRequest) { req.BranchAction = BranchSwitchReject }},
		{"project ID", func(req *ObserveGitBaseRequest) { req.Scope.ProjectID = "BAD" }},
		{"workspace ID", func(req *ObserveGitBaseRequest) { req.Scope.WorkspaceID = "BAD" }},
		{"request ID", func(req *ObserveGitBaseRequest) { req.RequestID = "BAD" }},
		{"actor", func(req *ObserveGitBaseRequest) { req.Actor.Assurance = types.AssuranceLegacy }},
		{"scope mismatch", func(req *ObserveGitBaseRequest) { req.Scope.WorkspaceID = "00000000-0000-4000-8000-000000000009" }},
		{"root mismatch", func(req *ObserveGitBaseRequest) { req.Root = "/other" }},
		{"commit", func(req *ObserveGitBaseRequest) { req.ExpectedCommit = "BAD" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.edit(&req)
			if got, err := discardRequestDigest(req); err == nil || got != "" {
				t.Fatalf("discardRequestDigest(invalid)=(%q,%v), want zero and error", got, err)
			}
		})
	}

	drift := []struct {
		name string
		edit func(*ObserveGitBaseRequest)
	}{
		{"actor kind", func(req *ObserveGitBaseRequest) {
			req.Actor.ActorKind = types.ActorAgent
			req.Actor.HumanPrincipalID = ""
			req.Actor.AgentID = "11111111-1111-4111-8111-111111111112"
			req.Actor.AccountableHumanID = "11111111-1111-4111-8111-111111111111"
			req.Actor.SessionID = "s"
			req.Actor.HarnessName = "h"
			req.Actor.HarnessVersion = "1"
		}},
		{"actor time", func(req *ObserveGitBaseRequest) { req.Actor.OccurredAt = req.Actor.OccurredAt.Add(time.Second) }},
		{"binding project", func(req *ObserveGitBaseRequest) {
			req.ExpectedBinding.Scope.ProjectID = "00000000-0000-4000-8000-000000000009"
			req.Scope.ProjectID = req.ExpectedBinding.Scope.ProjectID
		}},
		{"binding workspace", func(req *ObserveGitBaseRequest) {
			req.ExpectedBinding.Scope.WorkspaceID = "00000000-0000-4000-8000-000000000009"
			req.Scope.WorkspaceID = req.ExpectedBinding.Scope.WorkspaceID
		}},
		{"checkout path", func(req *ObserveGitBaseRequest) {
			req.ExpectedBinding.Checkout.CanonicalPath = "/changed"
			req.Root = "/changed"
		}},
		{"checkout device", func(req *ObserveGitBaseRequest) { req.ExpectedBinding.Checkout.Device++ }},
		{"checkout inode", func(req *ObserveGitBaseRequest) { req.ExpectedBinding.Checkout.Inode++ }},
		{"repository provider", func(req *ObserveGitBaseRequest) { req.ExpectedBinding.Repository.Provider = "gitlab" }},
		{"repository immutable ID", func(req *ObserveGitBaseRequest) { req.ExpectedBinding.Repository.ImmutableID = "changed" }},
		{"repository remote", func(req *ObserveGitBaseRequest) {
			req.ExpectedBinding.Repository.CanonicalRemote = "https://gitlab.com/h4rl33/wormhole"
		}},
		{"accepted ref", func(req *ObserveGitBaseRequest) { req.ExpectedBinding.AcceptedRef = "refs/heads/changed" }},
		{"accepted commit", func(req *ObserveGitBaseRequest) { req.ExpectedBinding.AcceptedCommitSHA = strings.Repeat("b", 40) }},
		{"accepted tree", func(req *ObserveGitBaseRequest) {
			req.ExpectedBinding.AcceptedTreeDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{"root", func(req *ObserveGitBaseRequest) {
			req.Root = "/changed"
			req.ExpectedBinding.Checkout.CanonicalPath = "/changed"
		}},
		{"expected commit", func(req *ObserveGitBaseRequest) { req.ExpectedCommit = strings.Repeat("e", 40) }},
	}
	for _, test := range drift {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.edit(&req)
			got, err := discardRequestDigest(req)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatal("field drift did not change request digest")
			}
		})
	}
}

func TestDiscardRequestRejectsInvalidUTF8WithoutCanonicalCollisions(t *testing.T) {
	invalid := []string{string([]byte{0xff}), string([]byte{0xfe})}
	for index, suffix := range invalid {
		req := discardCodecRequest()
		req.Root = "/checkout/" + suffix
		req.ExpectedBinding.Checkout.CanonicalPath = req.Root
		if got, err := discardRequestDigest(req); err == nil || got != "" {
			t.Fatalf("invalid root %d digest=(%q,%v), want zero and error", index, got, err)
		}
	}

	fields := []struct {
		name string
		set  func(*types.ActorEnvelope, string)
	}{
		{"session", func(actor *types.ActorEnvelope, value string) { actor.SessionID = value }},
		{"harness name", func(actor *types.ActorEnvelope, value string) { actor.HarnessName = value }},
		{"harness version", func(actor *types.ActorEnvelope, value string) { actor.HarnessVersion = value }},
		{"model name", func(actor *types.ActorEnvelope, value string) { actor.ModelName = value }},
		{"model version", func(actor *types.ActorEnvelope, value string) { actor.ModelVersion = value }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			for index, value := range invalid {
				req := discardCodecAgentRequest()
				field.set(&req.Actor, value)
				if got, err := discardRequestDigest(req); err == nil || got != "" {
					t.Fatalf("invalid value %d digest=(%q,%v), want zero and error", index, got, err)
				}
			}
		})
	}
}

func TestDiscardRequestRejectsComponentInvalidBranchRefs(t *testing.T) {
	refs := []string{
		"refs/heads/foo/.bar",
		"refs/heads/foo/bar.lock/baz",
		"refs/heads/foo/" + string([]byte{0xff}),
	}
	for _, ref := range refs {
		t.Run(strings.ToValidUTF8(ref, "invalid"), func(t *testing.T) {
			req := discardCodecRequest()
			req.ExpectedBinding.AcceptedRef = ref
			if got, err := discardRequestDigest(req); err == nil || got != "" {
				t.Fatalf("discardRequestDigest(ref %q)=(%q,%v), want zero and error", ref, got, err)
			}
		})
	}
}

func TestDiscardAcceptsInteriorDotSuffixBranchRef(t *testing.T) {
	const ref = "refs/heads/foo./bar"
	req := discardCodecRequest()
	req.ExpectedBinding.AcceptedRef = ref
	digest, err := discardRequestDigest(req)
	if err != nil || digest == "" {
		t.Fatalf("discardRequestDigest(valid Git ref)=(%q,%v)", digest, err)
	}

	result := discardCodecResult()
	result.PreviousRef = ref
	encoded, err := encodeDiscardReceipt(result)
	if err != nil || encoded == nil {
		t.Fatalf("encodeDiscardReceipt(valid Git ref)=(%q,%v)", encoded, err)
	}
	row := discardCodecReceipt(t, req, encoded)
	decoded, err := decodeDiscardReceipt(row, req, digest)
	if err != nil || !reflect.DeepEqual(decoded, result) {
		t.Fatalf("decodeDiscardReceipt(valid Git ref)=(%+v,%v), want %+v", decoded, err, result)
	}
}

func TestDiscardReceiptGoldenAndDeepOwnership(t *testing.T) {
	result := discardCodecResult()
	private, err := privateDiscardResult(result)
	if err != nil {
		t.Fatal(err)
	}
	assertDiscardCodecGolden(t, private, discardResultGolden, "sha256:2e7fa9171ce57fefa114227637308348d101f9e30c747913ccd0dae8bdf5c806")
	encoded, err := encodeDiscardReceipt(result)
	if err != nil || string(encoded) != discardReceiptGolden {
		t.Fatalf("encodeDiscardReceipt()=(%q,%v), want golden", encoded, err)
	}
	envelope := discardReceiptV1{SchemaVersion: 1, Action: "discard", Outcome: "clean", Result: private}
	assertDiscardCodecGolden(t, envelope, discardReceiptGolden, "sha256:0af212a9312ba0f7d74e82f469e3dae8f11850e37c73f692437dd7fceb1c6fce")
	if !bytes.Contains(encoded, []byte(`"accepted_journal_id":null`)) || !bytes.Contains(encoded, []byte(`"conflicts":[]`)) {
		t.Fatalf("discard receipt omitted explicit null or empty conflicts: %s", encoded)
	}

	req := discardCodecRequest()
	digest, err := discardRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	row := discardCodecReceipt(t, req, encoded)
	decoded, err := decodeDiscardReceipt(row, req, digest)
	if err != nil || !reflect.DeepEqual(decoded, result) || decoded.Conflicts == nil {
		t.Fatalf("decodeDiscardReceipt()=(%+v,%v)", decoded, err)
	}
	decoded.Conflicts = append(decoded.Conflicts, Conflict{})
	if len(result.Conflicts) != 0 {
		t.Fatal("decoded result aliases source result")
	}
}

func TestDiscardReceiptRejectsInvalidResultAndRecord(t *testing.T) {
	result := discardCodecResult()
	invalid := []struct {
		name string
		edit func(*ObserveGitBaseResult)
	}{
		{"previous commit", func(value *ObserveGitBaseResult) { value.PreviousCommit = "BAD" }},
		{"observed commit", func(value *ObserveGitBaseResult) { value.ObservedCommit = "BAD" }},
		{"previous ref", func(value *ObserveGitBaseResult) { value.PreviousRef = "bad" }},
		{"observed ref", func(value *ObserveGitBaseResult) { value.ObservedRef = "bad" }},
		{"previous digest", func(value *ObserveGitBaseResult) { value.PreviousBaseDigest = "BAD" }},
		{"observed digest", func(value *ObserveGitBaseResult) { value.ObservedBaseDigest = "BAD" }},
		{"candidate accepted", func(value *ObserveGitBaseResult) { value.CandidateAccepted = true }},
		{"accepted journal", func(value *ObserveGitBaseResult) {
			id := "11111111-1111-4111-8111-111111111111"
			value.AcceptedJournalID = &id
		}},
		{"rebased", func(value *ObserveGitBaseResult) { value.Rebased = true }},
		{"nil conflicts", func(value *ObserveGitBaseResult) { value.Conflicts = nil }},
		{"nonempty conflicts", func(value *ObserveGitBaseResult) { value.Conflicts = []Conflict{{}} }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			value := result
			test.edit(&value)
			if got, err := encodeDiscardReceipt(value); err == nil || got != nil {
				t.Fatalf("encodeDiscardReceipt(invalid)=(%q,%v), want nil and error", got, err)
			}
		})
	}

	req := discardCodecRequest()
	digest, err := discardRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := encodeDiscardReceipt(result)
	if err != nil {
		t.Fatal(err)
	}
	otherActor := req.Actor
	otherActor.HumanPrincipalID = "44444444-4444-4444-8444-444444444444"
	cases := []struct {
		name string
		edit func(*localstore.WorkspaceTransitionReceiptRecord, *ObserveGitBaseRequest, *state.Digest)
	}{
		{"nil receipt", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			*row = localstore.WorkspaceTransitionReceiptRecord{}
		}},
		{"request ID", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.RequestID = "99999999-9999-4999-8999-999999999992"
		}},
		{"noncanonical request ID", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.RequestID = "BAD"
		}},
		{"action", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.Action = "stash"
		}},
		{"digest", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.RequestDigest = state.Digest("sha256:" + strings.Repeat("e", 64))
		}},
		{"supplied digest", func(_ *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, value *state.Digest) {
			*value = state.Digest("sha256:" + strings.Repeat("e", 64))
		}},
		{"actor", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.Actor = otherActor
		}},
		{"invalid actor", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.Actor = types.ActorEnvelope{}
		}},
		{"outcome", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.Outcome = "conflicted"
		}},
		{"timestamp", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.CreatedAt = time.Time{}
		}},
		{"non-UTC timestamp", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.CreatedAt = time.Date(2026, 7, 29, 13, 0, 0, 0, time.FixedZone("offset", 60))
		}},
		{"unknown field", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.ResultJSON = json.RawMessage(strings.Replace(string(valid), "}\n", ",\"extra\":true}\n", 1))
		}},
		{"trailing value", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.ResultJSON = append(bytes.Clone(valid), []byte("{}\n")...)
		}},
		{"noncanonical", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.ResultJSON = json.RawMessage(strings.Replace(string(valid), "{", "{ ", 1))
		}},
		{"envelope action", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.ResultJSON = json.RawMessage(bytes.Replace(valid, []byte(`"discard"`), []byte(`"restore"`), 1))
		}},
		{"envelope outcome", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.ResultJSON = json.RawMessage(bytes.Replace(valid, []byte(`"clean"`), []byte(`"conflicted"`), 1))
		}},
		{"nil conflicts", func(row *localstore.WorkspaceTransitionReceiptRecord, _ *ObserveGitBaseRequest, _ *state.Digest) {
			row.ResultJSON = json.RawMessage(bytes.Replace(valid, []byte(`"conflicts":[]`), []byte(`"conflicts":null`), 1))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			row := discardCodecReceipt(t, req, valid)
			request, wantDigest := req, digest
			test.edit(row, &request, &wantDigest)
			if test.name == "nil receipt" {
				if got, err := decodeDiscardReceipt(nil, request, wantDigest); err == nil || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
					t.Fatalf("nil receipt=(%+v,%v)", got, err)
				}
				return
			}
			if got, err := decodeDiscardReceipt(row, request, wantDigest); err == nil || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
				t.Fatalf("decode invalid receipt=(%+v,%v)", got, err)
			}
		})
	}
}

func TestDecodeDiscardReceiptBindsResultToDigestBoundRequest(t *testing.T) {
	req := discardCodecRequest()
	digest, err := discardRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeDiscardReceipt(discardCodecResult())
	if err != nil {
		t.Fatal(err)
	}
	private, err := decodeDiscardReceiptJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*discardResultV1)
	}{
		{"previous commit", func(result *discardResultV1) { result.PreviousCommit = strings.Repeat("e", 40) }},
		{"previous ref", func(result *discardResultV1) { result.PreviousRef = "refs/heads/other" }},
		{"previous digest", func(result *discardResultV1) {
			result.PreviousBaseDigest = state.Digest("sha256:" + strings.Repeat("e", 64))
		}},
		{"observed commit", func(result *discardResultV1) { result.ObservedCommit = strings.Repeat("e", 40) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := private
			test.edit(&tampered.Result)
			resultJSON, err := state.CanonicalJSON(tampered)
			if err != nil {
				t.Fatal(err)
			}
			row := discardCodecReceipt(t, req, resultJSON)
			if got, err := decodeDiscardReceipt(row, req, digest); err == nil || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
				t.Fatalf("decode tampered receipt=(%+v,%v), want zero and error", got, err)
			}
		})
	}
}

func TestDiscardReceiptRejectsComponentInvalidBranchRefs(t *testing.T) {
	refs := []string{
		"refs/heads/foo/.bar",
		"refs/heads/foo/bar.lock/baz",
		"refs/heads/foo/" + string([]byte{0xff}),
	}
	fields := []struct {
		name       string
		golden     string
		setPublic  func(*ObserveGitBaseResult, string)
		setPrivate func(*discardResultV1, string)
	}{
		{"previous", "refs/heads/main", func(result *ObserveGitBaseResult, ref string) { result.PreviousRef = ref }, func(result *discardResultV1, ref string) { result.PreviousRef = ref }},
		{"observed", "refs/heads/next", func(result *ObserveGitBaseResult, ref string) { result.ObservedRef = ref }, func(result *discardResultV1, ref string) { result.ObservedRef = ref }},
	}
	for _, ref := range refs {
		for _, field := range fields {
			t.Run(field.name+"/"+strings.ToValidUTF8(ref, "invalid"), func(t *testing.T) {
				result := discardCodecResult()
				field.setPublic(&result, ref)
				if got, err := encodeDiscardReceipt(result); err == nil || got != nil {
					t.Fatalf("encodeDiscardReceipt(ref %q)=(%q,%v), want nil and error", ref, got, err)
				}

				if !utf8.ValidString(ref) {
					raw := bytes.Replace([]byte(discardReceiptGolden), []byte(field.golden), []byte(ref), 1)
					if _, err := decodeDiscardReceiptJSON(raw); err == nil {
						t.Fatalf("decoded invalid UTF-8 ref %q", ref)
					}
					return
				}
				private, err := privateDiscardResult(discardCodecResult())
				if err != nil {
					t.Fatal(err)
				}
				field.setPrivate(&private, ref)
				resultJSON, err := state.CanonicalJSON(discardReceiptV1{SchemaVersion: 1, Action: "discard", Outcome: "clean", Result: private})
				if err != nil {
					t.Fatal(err)
				}
				req := discardCodecRequest()
				row := discardCodecReceipt(t, req, resultJSON)
				digest, err := discardRequestDigest(req)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := decodeDiscardReceipt(row, req, digest); err == nil || !reflect.DeepEqual(got, ObserveGitBaseResult{}) {
					t.Fatalf("decode invalid ref=(%+v,%v), want zero and error", got, err)
				}
			})
		}
	}
}

func assertDiscardCodecGolden(t *testing.T, value any, wantJSON, wantDigest string) {
	t.Helper()
	canonical, err := state.CanonicalJSON(value)
	if err != nil || string(canonical) != wantJSON {
		t.Fatalf("canonical=(%q,%v), want %q", canonical, err, wantJSON)
	}
	digest, err := state.DigestCanonicalJSON(value)
	if err != nil || string(digest) != wantDigest {
		t.Fatalf("digest=(%q,%v), want %q", digest, err, wantDigest)
	}
}

func discardCodecRequest() ObserveGitBaseRequest {
	binding := types.WorkspaceBinding{
		Scope:       types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000002"},
		Checkout:    types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
		Repository:  types.RepositoryIdentity{Provider: "github", ImmutableID: "wormhole-id", CanonicalRemote: "https://github.com/H4RL33/wormhole"},
		AcceptedRef: "refs/heads/main", AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: "sha256:" + strings.Repeat("c", 64),
	}
	return ObserveGitBaseRequest{
		Scope: binding.Scope, ExpectedBinding: binding, Root: "/checkout", ExpectedCommit: strings.Repeat("b", 40),
		BranchAction: BranchSwitchDiscard, RequestID: "33333333-3333-4333-8333-333333333333",
		Actor: types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111", Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC)},
	}
}

func discardCodecAgentRequest() ObserveGitBaseRequest {
	req := discardCodecRequest()
	req.Actor = types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: "11111111-1111-4111-8111-111111111112",
		AccountableHumanID: "11111111-1111-4111-8111-111111111111",
		SessionID:          "session", HarnessName: "codex", HarnessVersion: "1",
		ModelName: "model", ModelVersion: "1", Assurance: types.AssuranceLocal,
		OccurredAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
	}
	return req
}

func discardCodecResult() ObserveGitBaseResult {
	return ObserveGitBaseResult{
		PreviousCommit: strings.Repeat("a", 40), ObservedCommit: strings.Repeat("b", 40),
		PreviousRef: "refs/heads/main", ObservedRef: "refs/heads/next",
		PreviousBaseDigest: state.Digest("sha256:" + strings.Repeat("c", 64)), ObservedBaseDigest: state.Digest("sha256:" + strings.Repeat("d", 64)),
		Conflicts: []Conflict{},
	}
}

func discardCodecReceipt(t *testing.T, req ObserveGitBaseRequest, result json.RawMessage) *localstore.WorkspaceTransitionReceiptRecord {
	t.Helper()
	digest, err := discardRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	return &localstore.WorkspaceTransitionReceiptRecord{WorkspaceTransitionReceiptInsert: localstore.WorkspaceTransitionReceiptInsert{
		RequestID: req.RequestID, Action: "discard", RequestDigest: digest, Actor: req.Actor, ResultJSON: bytes.Clone(result), Outcome: "clean",
	}, CreatedAt: time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)}
}
