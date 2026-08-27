package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	publicationEmptySemanticDiffGolden = "{\"schema_version\":1,\"kind\":\"semantic_diff\",\"accepted_tree_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"candidate_tree_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"changes\":[]}\n"
	publicationEmptySemanticDiffDigest = state.Digest("sha256:69591135301299c7ad70bfece52d1bcd9a0c630998dbb4a930428b7fb1d1256f")
	publicationMixedSemanticDiffGolden = "{\"schema_version\":1,\"kind\":\"semantic_diff\",\"accepted_tree_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"candidate_tree_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"changes\":[{\"key\":{\"kind\":\"task\",\"id\":\"11111111-1111-4111-8111-111111111111\"},\"kind\":\"modify\",\"before_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"after_digest\":\"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\",\"before_body_digest\":null,\"after_body_digest\":null,\"fields\":[{\"path\":\"/priority\",\"before\":{\"present\":true,\"value\":\"low\"},\"after\":{\"present\":true,\"value\":\"high\"},\"actor\":null},{\"path\":\"/title\",\"before\":{\"present\":true,\"value\":\"old\"},\"after\":{\"present\":true,\"value\":\"new\"},\"actor\":{\"actor_kind\":\"human\",\"human_principal_id\":\"22222222-2222-4222-8222-222222222222\",\"assurance\":\"local\",\"occurred_at\":\"2026-08-01T12:00:00Z\"}}],\"actor\":null}]}\n"
	publicationMixedSemanticDiffDigest = state.Digest("sha256:754848a8addbc94f263fbdc4dd5afe9b4db31d8ca466d445f215fff6c552f4bb")
	publicationReviewEnvelopeGolden    = "{\"schema_version\":1,\"kind\":\"publication_review\",\"scope\":{\"project_id\":\"00000000-0000-4000-8000-000000000001\",\"workspace_id\":\"00000000-0000-4000-8000-000000000002\"},\"repository_identity\":{\"provider\":\"github\",\"immutable_id\":\"repository-1\",\"canonical_remote\":\"https://github.com/acme/wormhole\"},\"origin_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"classification\":\"public_git\",\"policy_revision\":2,\"accepted_ref\":\"refs/heads/main\",\"accepted_commit_sha\":\"dddddddddddddddddddddddddddddddddddddddd\",\"accepted_tree_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"candidate_tree_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"semantic_diff_digest\":\"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee\",\"overlay_generation\":0}\n"
	publicationReviewEnvelopeDigest    = state.Digest("sha256:0c84b1ee405ef05d66f4f8c8fc096420fc148fa32608c9fb3133c8a833c5105a")
)

func TestPublicationFieldChangeActorSurface(t *testing.T) {
	actor := diffActorEnvelope()
	field := FieldChange{Actor: &actor}
	if field.Actor == nil || *field.Actor != actor {
		t.Fatalf("FieldChange.Actor = %+v, want %+v", field.Actor, actor)
	}
}

func TestPublicationSemanticDiffEmptyGolden(t *testing.T) {
	diff := Diff{
		BaseDigest: state.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		ViewDigest: state.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		Changes:    make([]Change, 0),
	}
	encoded, digest, err := encodePublicationSemanticDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != publicationEmptySemanticDiffGolden || digest != publicationEmptySemanticDiffDigest {
		t.Fatalf("empty semantic diff = (%s, %q), want (%s, %q)", encoded, digest, publicationEmptySemanticDiffGolden, publicationEmptySemanticDiffDigest)
	}
}

func TestPublicationSemanticDiffMixedAttributionGolden(t *testing.T) {
	accepted, selectedStart, operation, actor := publicationMixedAttributionInputs(t)
	_, attributed, err := publicationAttributedDiff(accepted, selectedStart, 0, []StoredOperation{{Generation: 1, Operation: operation}})
	if err != nil {
		t.Fatal(err)
	}
	attributedChange := publicationOnlyChange(t, attributed)
	if publicationFieldAt(t, attributedChange, "/priority").Actor != nil {
		t.Fatal("real direct attribution unexpectedly supplied a priority actor")
	}
	titleActor := publicationFieldAt(t, attributedChange, "/title").Actor
	if titleActor == nil || *titleActor != actor {
		t.Fatalf("real active attribution actor = %+v, want %+v", titleActor, actor)
	}

	diff := publicationCodecDiff()
	diff.Changes[0].Key.ID = "11111111-1111-4111-8111-111111111111"
	diff.Changes[0].BeforeDigest = publicationDigestPointer('c')
	diff.Changes[0].AfterDigest = publicationDigestPointer('d')
	diff.Changes[0].Fields = []FieldChange{
		{Path: "/priority", Before: publicationPresent(`"low"`), After: publicationPresent(`"high"`)},
		{Path: "/title", Before: publicationPresent(`"old"`), After: publicationPresent(`"new"`), Actor: publicationActorPointer(*titleActor)},
	}
	encoded, digest, err := encodePublicationSemanticDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != publicationMixedSemanticDiffGolden || digest != publicationMixedSemanticDiffDigest {
		t.Fatalf("mixed semantic diff = (%s, %q), want (%s, %q)", encoded, digest, publicationMixedSemanticDiffGolden, publicationMixedSemanticDiffDigest)
	}
}

func TestPublicationReviewEnvelopeGolden(t *testing.T) {
	envelope := publicationReviewFixture()
	encoded, digest, err := encodePublicationReviewEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != publicationReviewEnvelopeGolden || digest != publicationReviewEnvelopeDigest {
		t.Fatalf("review envelope = (%s, %q), want (%s, %q)", encoded, digest, publicationReviewEnvelopeGolden, publicationReviewEnvelopeDigest)
	}
}

func TestPublicationSemanticDiffStrictFieldValuesAndCollections(t *testing.T) {
	t.Run("empty fields array", func(t *testing.T) {
		diff := publicationCodecDiff()
		diff.Changes[0].Fields = make([]FieldChange, 0)
		encoded, _, err := encodePublicationSemanticDiff(diff)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte(`"fields":[],"actor":null`)) {
			t.Fatalf("empty fields encoding = %s", encoded)
		}
	})

	t.Run("absent empty and present null", func(t *testing.T) {
		diff := publicationCodecDiff()
		diff.Changes[0].Fields[0].Before = FieldValue{Value: json.RawMessage{}}
		diff.Changes[0].Fields[0].After = publicationPresent(`null`)
		encoded, _, err := encodePublicationSemanticDiff(diff)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte(`"before":{"present":false},"after":{"present":true,"value":null}`)) {
			t.Fatalf("absent/null encoding = %s", encoded)
		}
	})

	tests := []struct {
		name   string
		mutate func(*Diff)
	}{
		{name: "nil changes", mutate: func(diff *Diff) { diff.Changes = nil }},
		{name: "nil fields", mutate: func(diff *Diff) { diff.Changes[0].Fields = nil }},
		{name: "absent with value", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before = FieldValue{Value: json.RawMessage(`null`)} }},
		{name: "present empty", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before = FieldValue{Present: true} }},
		{name: "unknown literal", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before.Value = json.RawMessage(`undefined`) }},
		{name: "trailing value", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before.Value = json.RawMessage(`"old" null`) }},
		{name: "leading whitespace", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before.Value = json.RawMessage(` "old"`) }},
		{name: "final newline", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before.Value = json.RawMessage("\"old\"\n") }},
		{name: "noncanonical object order", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Before.Value = json.RawMessage(`{"z":1,"a":2}`) }},
		{name: "invalid digest", mutate: func(diff *Diff) { diff.BaseDigest = "sha256:nope" }},
		{name: "invalid key", mutate: func(diff *Diff) { diff.Changes[0].Key.ID = "not-a-uuid" }},
		{name: "invalid kind", mutate: func(diff *Diff) { diff.Changes[0].Kind = "rewrite" }},
		{name: "invalid path", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Path = "title" }},
		{name: "duplicate path", mutate: func(diff *Diff) {
			field := diff.Changes[0].Fields[0]
			diff.Changes[0].Fields = []FieldChange{field, field}
		}},
		{name: "unordered fields", mutate: func(diff *Diff) {
			second := diff.Changes[0].Fields[0]
			second.Path = "/alpha"
			diff.Changes[0].Fields = []FieldChange{diff.Changes[0].Fields[0], second}
		}},
		{name: "invalid field actor", mutate: func(diff *Diff) { diff.Changes[0].Fields[0].Actor = &types.ActorEnvelope{} }},
		{name: "stale change actor", mutate: func(diff *Diff) { diff.Changes[0].Actor = publicationActorPointer(diffActorEnvelope()) }},
		{name: "change actor differs from field", mutate: func(diff *Diff) {
			fieldActor := publicationHumanActor(publicationActorOneID, 0)
			changeActor := publicationHumanActor(publicationActorTwoID, 0)
			diff.Changes[0].Fields[0].Actor = &fieldActor
			diff.Changes[0].Actor = &changeActor
		}},
		{name: "unordered changes", mutate: func(diff *Diff) {
			first := diff.Changes[0]
			first.Key.ID = "22222222-2222-4222-8222-222222222222"
			second := publicationCodecDiff().Changes[0]
			second.Key.ID = "11111111-1111-4111-8111-111111111111"
			diff.Changes = []Change{first, second}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diff := publicationCodecDiff()
			test.mutate(&diff)
			encoded, digest, err := encodePublicationSemanticDiff(diff)
			if err == nil || encoded != nil || digest != "" {
				t.Fatalf("invalid semantic diff = (%s, %q, %v), want nil, empty, error", encoded, digest, err)
			}
		})
	}
}

func TestPublicationSemanticDiffOwnsInputsAndIsDeterministic(t *testing.T) {
	diff := publicationCodecDiff()
	actor := publicationHumanActor(publicationActorOneID, 0)
	diff.Changes[0].Fields[0].Actor = &actor
	diff.Changes[0].Actor = publicationActorPointer(actor)
	first, firstDigest, err := encodePublicationSemanticDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	diff.Changes[0].Fields[0].Before.Value[0] = 'X'
	diff.Changes[0].Fields[0].Actor.HumanPrincipalID = publicationActorTwoID
	if bytes.Contains(first, []byte(publicationActorTwoID)) || first[0] != '{' {
		t.Fatalf("encoded diff aliases input: %s", first)
	}

	fresh := publicationCodecDiff()
	freshActor := publicationHumanActor(publicationActorOneID, 0)
	fresh.Changes[0].Fields[0].Actor = &freshActor
	fresh.Changes[0].Actor = publicationActorPointer(freshActor)
	for iteration := 0; iteration < 10; iteration++ {
		encoded, digest, err := encodePublicationSemanticDiff(fresh)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, first) || digest != firstDigest {
			t.Fatalf("iteration %d = (%s, %q), want (%s, %q)", iteration, encoded, digest, first, firstDigest)
		}
	}
}

func TestPublicationReviewDigestChangesForEveryBoundField(t *testing.T) {
	base := publicationReviewFixture()
	_, baseDigest, err := encodePublicationReviewEnvelope(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*publicationReviewEnvelopeV1)
	}{
		{name: "project ID", mutate: func(value *publicationReviewEnvelopeV1) {
			value.Scope.ProjectID = "00000000-0000-4000-8000-000000000003"
		}},
		{name: "workspace ID", mutate: func(value *publicationReviewEnvelopeV1) {
			value.Scope.WorkspaceID = "00000000-0000-4000-8000-000000000004"
		}},
		{name: "repository provider", mutate: func(value *publicationReviewEnvelopeV1) { value.Repository.Provider = "gitlab" }},
		{name: "repository immutable ID", mutate: func(value *publicationReviewEnvelopeV1) { value.Repository.ImmutableID = "repository-2" }},
		{name: "repository canonical remote", mutate: func(value *publicationReviewEnvelopeV1) {
			value.Repository.CanonicalRemote = "https://github.com/acme/wormhole-fork"
		}},
		{name: "origin digest", mutate: func(value *publicationReviewEnvelopeV1) { value.OriginDigest = publicationRepeatedDigest('f') }},
		{name: "classification", mutate: func(value *publicationReviewEnvelopeV1) { value.Classification = types.PublicationPrivateGit }},
		{name: "policy revision", mutate: func(value *publicationReviewEnvelopeV1) { value.PolicyRevision++ }},
		{name: "accepted ref", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedRef = "" }},
		{name: "accepted commit", mutate: func(value *publicationReviewEnvelopeV1) {
			value.AcceptedCommitSHA = "ffffffffffffffffffffffffffffffffffffffff"
		}},
		{name: "accepted tree", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedTreeDigest = publicationRepeatedDigest('f') }},
		{name: "candidate tree", mutate: func(value *publicationReviewEnvelopeV1) { value.CandidateTreeDigest = publicationRepeatedDigest('f') }},
		{name: "semantic diff", mutate: func(value *publicationReviewEnvelopeV1) { value.SemanticDiffDigest = publicationRepeatedDigest('f') }},
		{name: "overlay generation", mutate: func(value *publicationReviewEnvelopeV1) { value.OverlayGeneration++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			_, digest, err := encodePublicationReviewEnvelope(value)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseDigest {
				t.Fatalf("mutating %s retained digest %q", test.name, digest)
			}
		})
	}
}

func TestPublicationReviewEnvelopeRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*publicationReviewEnvelopeV1)
	}{
		{name: "schema", mutate: func(value *publicationReviewEnvelopeV1) { value.SchemaVersion = 2 }},
		{name: "kind", mutate: func(value *publicationReviewEnvelopeV1) { value.Kind = "semantic_diff" }},
		{name: "project scope", mutate: func(value *publicationReviewEnvelopeV1) { value.Scope.ProjectID = "bad" }},
		{name: "workspace scope", mutate: func(value *publicationReviewEnvelopeV1) { value.Scope.WorkspaceID = "bad" }},
		{name: "repository", mutate: func(value *publicationReviewEnvelopeV1) {
			value.Repository.CanonicalRemote = "https://user@example.com/acme/wormhole"
		}},
		{name: "origin digest", mutate: func(value *publicationReviewEnvelopeV1) { value.OriginDigest = "bad" }},
		{name: "classification", mutate: func(value *publicationReviewEnvelopeV1) { value.Classification = "secret" }},
		{name: "policy revision", mutate: func(value *publicationReviewEnvelopeV1) { value.PolicyRevision = 0 }},
		{name: "accepted ref", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedRef = "main" }},
		{name: "accepted ref invalid UTF-8", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedRef = "refs/heads/" + string([]byte{0xff}) }},
		{name: "accepted ref dot component", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedRef = "refs/heads/foo/.bar" }},
		{name: "accepted ref lock component", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedRef = "refs/heads/foo.lock/bar" }},
		{name: "accepted commit", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedCommitSHA = "bad" }},
		{name: "accepted tree", mutate: func(value *publicationReviewEnvelopeV1) { value.AcceptedTreeDigest = "bad" }},
		{name: "candidate tree", mutate: func(value *publicationReviewEnvelopeV1) { value.CandidateTreeDigest = "bad" }},
		{name: "semantic diff", mutate: func(value *publicationReviewEnvelopeV1) { value.SemanticDiffDigest = "bad" }},
		{name: "overlay generation", mutate: func(value *publicationReviewEnvelopeV1) { value.OverlayGeneration = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := publicationReviewFixture()
			test.mutate(&value)
			encoded, digest, err := encodePublicationReviewEnvelope(value)
			if err == nil || encoded != nil || digest != "" {
				t.Fatalf("invalid review envelope = (%s, %q, %v), want nil, empty, error", encoded, digest, err)
			}
		})
	}
}

func TestPublicationReviewEnvelopeAcceptsGitValidDotSuffixComponent(t *testing.T) {
	value := publicationReviewFixture()
	value.AcceptedRef = "refs/heads/foo./bar"
	encoded, digest, err := encodePublicationReviewEnvelope(value)
	if err != nil || len(encoded) == 0 || digest == "" {
		t.Fatalf("valid dot-suffix ref = (%s, %q, %v), want encoded review", encoded, digest, err)
	}
}

func publicationCodecDiff() Diff {
	return Diff{
		BaseDigest: publicationRepeatedDigest('a'),
		ViewDigest: publicationRepeatedDigest('b'),
		Changes: []Change{{
			Key:  state.RecordKey{Kind: "task", ID: "11111111-1111-4111-8111-111111111111"},
			Kind: ChangeModify, BeforeDigest: publicationDigestPointer('c'), AfterDigest: publicationDigestPointer('d'),
			Fields: []FieldChange{{Path: "/title", Before: publicationPresent(`"old"`), After: publicationPresent(`"new"`)}},
		}},
	}
}

func publicationReviewFixture() publicationReviewEnvelopeV1 {
	return publicationReviewEnvelopeV1{
		SchemaVersion: 1,
		Kind:          "publication_review",
		Scope: types.WorkspaceScope{
			ProjectID:   "00000000-0000-4000-8000-000000000001",
			WorkspaceID: "00000000-0000-4000-8000-000000000002",
		},
		Repository: types.RepositoryIdentity{
			Provider:        "github",
			ImmutableID:     "repository-1",
			CanonicalRemote: "https://github.com/acme/wormhole",
		},
		OriginDigest:        publicationRepeatedDigest('c'),
		Classification:      types.PublicationPublicGit,
		PolicyRevision:      2,
		AcceptedRef:         "refs/heads/main",
		AcceptedCommitSHA:   "dddddddddddddddddddddddddddddddddddddddd",
		AcceptedTreeDigest:  publicationRepeatedDigest('a'),
		CandidateTreeDigest: publicationRepeatedDigest('b'),
		SemanticDiffDigest:  publicationRepeatedDigest('e'),
		OverlayGeneration:   0,
	}
}

func publicationPresent(value string) FieldValue {
	return FieldValue{Present: true, Value: json.RawMessage(value)}
}

func publicationRepeatedDigest(char byte) state.Digest {
	return state.Digest("sha256:" + string(bytes.Repeat([]byte{char}, 64)))
}

func publicationDigestPointer(char byte) *state.Digest {
	value := publicationRepeatedDigest(char)
	return &value
}

func publicationActorPointer(actor types.ActorEnvelope) *types.ActorEnvelope {
	copy := actor
	return &copy
}

func TestPublicationCodecFixturesAreIndependent(t *testing.T) {
	left, right := publicationCodecDiff(), publicationCodecDiff()
	left.Changes[0].Fields[0].Before.Value[0] = 'X'
	if reflect.DeepEqual(left, right) || string(right.Changes[0].Fields[0].Before.Value) != `"old"` {
		t.Fatal("publication codec fixture aliases another fixture")
	}
}
