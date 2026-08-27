package projectstate

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	checkpointPublicationReviewGolden = "{\"schema_version\":1,\"kind\":\"checkpoint_publication_review\",\"review\":{" +
		"\"schema_version\":1,\"kind\":\"publication_review\",\"scope\":{\"project_id\":\"00000000-0000-4000-8000-000000000001\",\"workspace_id\":\"00000000-0000-4000-8000-000000000002\"}," +
		"\"repository_identity\":{\"provider\":\"github\",\"immutable_id\":\"repository-1\",\"canonical_remote\":\"https://github.com/acme/wormhole\"},\"origin_digest\":\"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"classification\":\"public_git\",\"policy_revision\":2,\"accepted_ref\":\"refs/heads/main\",\"accepted_commit_sha\":\"dddddddddddddddddddddddddddddddddddddddd\",\"accepted_tree_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"candidate_tree_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"semantic_diff_digest\":\"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee\",\"overlay_generation\":0}," +
		"\"review_digest\":\"sha256:0c84b1ee405ef05d66f4f8c8fc096420fc148fa32608c9fb3133c8a833c5105a\",\"checkpointed_by\":{\"actor_kind\":\"human\",\"human_principal_id\":\"22222222-2222-4222-8222-222222222222\",\"assurance\":\"local\",\"occurred_at\":\"2026-08-01T12:00:00Z\"}}\n"
	checkpointPriorCandidateAbsentGolden          = "{\"schema_version\":1,\"kind\":\"checkpoint_prior_candidate\",\"candidate\":null}\n"
	checkpointPriorDirectTreeGolden               = "{\"digest\":\"sha256:8199f0bed46726554cab971fd3feb06b641e05c55baa6678a438880dfd8e63f3\",\"files\":[{\"path\":\"config.toml\",\"data\":\"c25hcHNob3RfdmVyc2lvbiA9IDEKcHJvamVjdF9pZCA9ICIwMDAwMDAwMC0wMDAwLTQwMDAtODAwMC0wMDAwMDAwMDAwMDEiCgpbaGFuZGxlXQpuYW1lc3BhY2UgPSAiYWNtZSIKbmFtZSA9ICJ3b3JtaG9sZSIKCltyZXBvc2l0b3J5XQpwcm92aWRlciA9ICJnaXRodWIiCmltbXV0YWJsZV9pZCA9ICJyZXBvc2l0b3J5LTEiCmNhbm9uaWNhbF9yZW1vdGUgPSAiaHR0cHM6Ly9naXRodWIuY29tL2FjbWUvd29ybWhvbGUiCg==\"},{\"path\":\"state/v1/project.json\",\"data\":\"eyJzY2hlbWFfdmVyc2lvbiI6MSwia2luZCI6InByb2plY3QiLCJpZCI6IjAwMDAwMDAwLTAwMDAtNDAwMC04MDAwLTAwMDAwMDAwMDAwMSIsIm5hbWUiOiJXb3JtaG9sZSIsImFsaWFzZXMiOltdLCJjcmVhdGVkX2F0IjoiMjAyNi0wNy0yOFQxMjowMDowMFoiLCJ1cGRhdGVkX2F0IjoiMjAyNi0wNy0yOFQxMjowMDowMFoiLCJleHRlbnNpb25zIjp7fX0K\"}]}"
	checkpointPriorCandidateDirectGolden          = "{\"schema_version\":1,\"kind\":\"checkpoint_prior_candidate\",\"candidate\":{\"accepted_base_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"working_tree_digest\":\"sha256:8199f0bed46726554cab971fd3feb06b641e05c55baa6678a438880dfd8e63f3\",\"direct_tree\":" + checkpointPriorDirectTreeGolden + ",\"rebased_tree\":null,\"rebased_through_generation\":0,\"imported_by\":\"11111111-1111-4111-8111-111111111111\",\"imported_at\":\"2026-08-01T13:00:00Z\"}}\n"
	checkpointPriorCandidateRebasedZeroGolden     = "{\"schema_version\":1,\"kind\":\"checkpoint_prior_candidate\",\"candidate\":{\"accepted_base_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"working_tree_digest\":\"sha256:8199f0bed46726554cab971fd3feb06b641e05c55baa6678a438880dfd8e63f3\",\"direct_tree\":" + checkpointPriorDirectTreeGolden + ",\"rebased_tree\":" + checkpointPriorDirectTreeGolden + ",\"rebased_through_generation\":0,\"imported_by\":\"system:git-observation-rebase-v1\",\"imported_at\":\"2026-08-01T14:00:00Z\"}}\n"
	checkpointPriorCandidateRebasedPositiveGolden = "{\"schema_version\":1,\"kind\":\"checkpoint_prior_candidate\",\"candidate\":{\"accepted_base_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"working_tree_digest\":\"sha256:8199f0bed46726554cab971fd3feb06b641e05c55baa6678a438880dfd8e63f3\",\"direct_tree\":" + checkpointPriorDirectTreeGolden + ",\"rebased_tree\":" + checkpointPriorDirectTreeGolden + ",\"rebased_through_generation\":7,\"imported_by\":\"11111111-1111-4111-8111-111111111111\",\"imported_at\":\"2026-08-01T15:00:00Z\"}}\n"
)

func TestCheckpointPublicationReviewCodecGolden(t *testing.T) {
	want := checkpointPublicationReviewV1{
		SchemaVersion: 1,
		Kind:          "checkpoint_publication_review",
		Review:        publicationReviewFixture(),
		ReviewDigest:  publicationReviewEnvelopeDigest,
		CheckpointedBy: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "22222222-2222-4222-8222-222222222222",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	raw, err := encodeCheckpointPublicationReview(want)
	if err != nil {
		t.Fatal(err)
	}
	if raw != checkpointPublicationReviewGolden {
		t.Fatalf("publication review = %q, want %q", raw, checkpointPublicationReviewGolden)
	}
	got, err := decodeCheckpointPublicationReview(raw)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("decode publication review = (%+v, %v), want %+v", got, err, want)
	}
}

func TestCheckpointPublicationReviewCodecActorsAndValidation(t *testing.T) {
	t.Run("complete local agent", func(t *testing.T) {
		value := checkpointPublicationReviewFixture()
		value.CheckpointedBy = checkpointPublicationAgentActor()
		raw, err := encodeCheckpointPublicationReview(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeCheckpointPublicationReview(raw)
		if err != nil || !reflect.DeepEqual(got, value) {
			t.Fatalf("agent round trip = (%+v, %v), want %+v", got, err, value)
		}
	})

	reviewTests := []struct {
		name   string
		mutate func(*checkpointPublicationReviewV1)
	}{
		{"wrapper schema", func(value *checkpointPublicationReviewV1) { value.SchemaVersion = 2 }},
		{"wrapper kind", func(value *checkpointPublicationReviewV1) { value.Kind = "publication_review" }},
		{"review schema", func(value *checkpointPublicationReviewV1) { value.Review.SchemaVersion = 2 }},
		{"review kind", func(value *checkpointPublicationReviewV1) { value.Review.Kind = "checkpoint_publication_review" }},
		{"review project", func(value *checkpointPublicationReviewV1) { value.Review.Scope.ProjectID = "bad" }},
		{"review workspace", func(value *checkpointPublicationReviewV1) { value.Review.Scope.WorkspaceID = "bad" }},
		{"review repository", func(value *checkpointPublicationReviewV1) {
			value.Review.Repository.CanonicalRemote = "https://user@example.com/acme/wormhole"
		}},
		{"review origin", func(value *checkpointPublicationReviewV1) { value.Review.OriginDigest = "bad" }},
		{"review classification", func(value *checkpointPublicationReviewV1) { value.Review.Classification = "secret" }},
		{"review policy", func(value *checkpointPublicationReviewV1) { value.Review.PolicyRevision = 0 }},
		{"review ref", func(value *checkpointPublicationReviewV1) { value.Review.AcceptedRef = "main" }},
		{"review commit", func(value *checkpointPublicationReviewV1) { value.Review.AcceptedCommitSHA = "bad" }},
		{"review accepted digest", func(value *checkpointPublicationReviewV1) { value.Review.AcceptedTreeDigest = "bad" }},
		{"review candidate digest", func(value *checkpointPublicationReviewV1) { value.Review.CandidateTreeDigest = "bad" }},
		{"review semantic digest", func(value *checkpointPublicationReviewV1) { value.Review.SemanticDiffDigest = "bad" }},
		{"review generation", func(value *checkpointPublicationReviewV1) { value.Review.OverlayGeneration = -1 }},
		{"malformed review digest", func(value *checkpointPublicationReviewV1) { value.ReviewDigest = "bad" }},
		{"mismatched review digest", func(value *checkpointPublicationReviewV1) { value.ReviewDigest = publicationRepeatedDigest('f') }},
	}
	for _, test := range reviewTests {
		t.Run(test.name, func(t *testing.T) {
			value := checkpointPublicationReviewFixture()
			test.mutate(&value)
			assertCheckpointPublicationRejected(t, value)
		})
	}

	actorTests := []struct {
		name   string
		mutate func(*types.ActorEnvelope)
	}{
		{"actor kind", func(actor *types.ActorEnvelope) { actor.ActorKind = "service" }},
		{"human identity", func(actor *types.ActorEnvelope) { actor.HumanPrincipalID = "bad" }},
		{"agent identity", func(actor *types.ActorEnvelope) { *actor = checkpointPublicationAgentActor(); actor.AgentID = "bad" }},
		{"agent accountable human", func(actor *types.ActorEnvelope) {
			*actor = checkpointPublicationAgentActor()
			actor.AccountableHumanID = "bad"
		}},
		{"agent session provenance", func(actor *types.ActorEnvelope) { *actor = checkpointPublicationAgentActor(); actor.SessionID = "" }},
		{"agent harness provenance", func(actor *types.ActorEnvelope) { *actor = checkpointPublicationAgentActor(); actor.HarnessName = "" }},
		{"agent harness version", func(actor *types.ActorEnvelope) {
			*actor = checkpointPublicationAgentActor()
			actor.HarnessVersion = ""
		}},
		{"agent model pair", func(actor *types.ActorEnvelope) { *actor = checkpointPublicationAgentActor(); actor.ModelVersion = "" }},
		{"zero time", func(actor *types.ActorEnvelope) { actor.OccurredAt = time.Time{} }},
		{"nonzero offset time", func(actor *types.ActorEnvelope) {
			actor.OccurredAt = time.Date(2026, 8, 1, 13, 0, 0, 0, time.FixedZone("plus-one", 3600))
		}},
	}
	for _, assurance := range []types.Assurance{types.AssuranceLegacy, types.AssuranceUnknown, types.AssurancePublicKeyContinuity, types.AssurancePrivateAuthenticated} {
		assurance := assurance
		actorTests = append(actorTests, struct {
			name   string
			mutate func(*types.ActorEnvelope)
		}{"non-local assurance " + string(assurance), func(actor *types.ActorEnvelope) { actor.Assurance = assurance }})
	}
	for _, test := range actorTests {
		t.Run(test.name, func(t *testing.T) {
			value := checkpointPublicationReviewFixture()
			test.mutate(&value.CheckpointedBy)
			assertCheckpointPublicationRejected(t, value)
		})
	}
}

func TestCheckpointPublicationReviewCodecStrictJSON(t *testing.T) {
	tests := map[string]string{
		"unknown wrapper":    strings.Replace(checkpointPublicationReviewGolden, `"schema_version":1`, `"unknown":0,"schema_version":1`, 1),
		"unknown review":     strings.Replace(checkpointPublicationReviewGolden, `"overlay_generation":0}`, `"overlay_generation":0,"unknown":0}`, 1),
		"unknown scope":      strings.Replace(checkpointPublicationReviewGolden, `"workspace_id":"00000000-0000-4000-8000-000000000002"`, `"workspace_id":"00000000-0000-4000-8000-000000000002","unknown":0`, 1),
		"unknown repository": strings.Replace(checkpointPublicationReviewGolden, `"canonical_remote":"https://github.com/acme/wormhole"`, `"canonical_remote":"https://github.com/acme/wormhole","unknown":0`, 1),
		"unknown actor":      strings.Replace(checkpointPublicationReviewGolden, `"occurred_at":"2026-08-01T12:00:00Z"`, `"occurred_at":"2026-08-01T12:00:00Z","unknown":0`, 1),
		"trailing value":     checkpointPublicationReviewGolden + `{}`,
		"leading whitespace": " " + checkpointPublicationReviewGolden,
		"reordered members":  strings.Replace(checkpointPublicationReviewGolden, `{"schema_version":1,"kind":"checkpoint_publication_review"`, `{"kind":"checkpoint_publication_review","schema_version":1`, 1),
		"missing LF":         strings.TrimSuffix(checkpointPublicationReviewGolden, "\n"),
		"doubled LF":         checkpointPublicationReviewGolden + "\n",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) { assertCheckpointPublicationDecodeRejected(t, raw) })
	}
}

func TestCheckpointPriorCandidateCodecGoldens(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		want := checkpointPriorCandidateV1{SchemaVersion: 1, Kind: "checkpoint_prior_candidate"}
		raw, err := encodeCheckpointPriorCandidate(want)
		if err != nil || raw != checkpointPriorCandidateAbsentGolden {
			t.Fatalf("encode absent = (%q, %v), want (%q, nil)", raw, err, checkpointPriorCandidateAbsentGolden)
		}
		got, err := decodeCheckpointPriorCandidate(raw)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("decode absent = (%+v, %v), want %+v", got, err, want)
		}
	})

	t.Run("direct only", func(t *testing.T) {
		want := checkpointPriorCandidateFixture(t, false, 0, "11111111-1111-4111-8111-111111111111", 13)
		raw, err := encodeCheckpointPriorCandidate(want)
		if err != nil || raw != checkpointPriorCandidateDirectGolden {
			t.Fatalf("encode direct = (%q, %v), want (%q, nil)", raw, err, checkpointPriorCandidateDirectGolden)
		}
		got, err := decodeCheckpointPriorCandidate(raw)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("decode direct = (%+v, %v), want %+v", got, err, want)
		}
	})

	for _, test := range []struct {
		name       string
		value      checkpointPriorCandidateV1
		wantGolden string
	}{
		{"rebased at zero", checkpointPriorCandidateFixture(t, true, 0, types.CandidateImportOriginGitObservationRebaseV1, 14), checkpointPriorCandidateRebasedZeroGolden},
		{"rebased positive", checkpointPriorCandidateFixture(t, true, 7, "11111111-1111-4111-8111-111111111111", 15), checkpointPriorCandidateRebasedPositiveGolden},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := encodeCheckpointPriorCandidate(test.value)
			if err != nil || raw != test.wantGolden {
				t.Fatalf("encode = (%q, %v), want (%q, nil)", raw, err, test.wantGolden)
			}
			got, err := decodeCheckpointPriorCandidate(raw)
			if err != nil || !reflect.DeepEqual(got, test.value) {
				t.Fatalf("decode = (%+v, %v), want %+v", got, err, test.value)
			}
		})
	}
}

func TestCheckpointPriorCandidateCodecStrictJSON(t *testing.T) {
	firstData := `"data":"c25hcHNob3RfdmVyc2lvbiA9IDEK`
	tests := map[string]string{
		"unknown wrapper":    strings.Replace(checkpointPriorCandidateDirectGolden, `"schema_version":1`, `"unknown":0,"schema_version":1`, 1),
		"unknown candidate":  strings.Replace(checkpointPriorCandidateDirectGolden, `"imported_at":"2026-08-01T13:00:00Z"`, `"imported_at":"2026-08-01T13:00:00Z","unknown":0`, 1),
		"unknown tree":       strings.Replace(checkpointPriorCandidateDirectGolden, `"files":[`, `"unknown":0,"files":[`, 1),
		"unknown file":       strings.Replace(checkpointPriorCandidateDirectGolden, firstData, `"unknown":0,`+firstData, 1),
		"trailing value":     checkpointPriorCandidateDirectGolden + `{}`,
		"leading whitespace": " " + checkpointPriorCandidateDirectGolden,
		"reordered members":  strings.Replace(checkpointPriorCandidateDirectGolden, `{"schema_version":1,"kind":"checkpoint_prior_candidate"`, `{"kind":"checkpoint_prior_candidate","schema_version":1`, 1),
		"missing LF":         strings.TrimSuffix(checkpointPriorCandidateDirectGolden, "\n"),
		"doubled LF":         checkpointPriorCandidateDirectGolden + "\n",
		"malformed base64":   strings.Replace(checkpointPriorCandidateDirectGolden, "c25hcHNob3RfdmVyc2lvbiA9IDEK", "***", 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) { assertCheckpointPriorDecodeRejected(t, raw) })
	}
}

func TestCheckpointPriorCandidateCodecProofValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*checkpointPriorCandidateV1)
	}{
		{"absent schema", func(value *checkpointPriorCandidateV1) { value.SchemaVersion = 2 }},
		{"absent kind", func(value *checkpointPriorCandidateV1) { value.Kind = "prior_candidate" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := checkpointPriorCandidateV1{SchemaVersion: 1, Kind: "checkpoint_prior_candidate"}
			test.mutate(&value)
			assertCheckpointPriorRejected(t, value)
		})
	}

	tests := []struct {
		name   string
		mutate func(*checkpointPriorCandidateV1)
	}{
		{"schema", func(value *checkpointPriorCandidateV1) { value.SchemaVersion = 2 }},
		{"kind", func(value *checkpointPriorCandidateV1) { value.Kind = "prior_candidate" }},
		{"accepted digest malformed", func(value *checkpointPriorCandidateV1) { value.Candidate.AcceptedBaseDigest = "bad" }},
		{"accepted digest uppercase", func(value *checkpointPriorCandidateV1) {
			value.Candidate.AcceptedBaseDigest = state.Digest("sha256:" + strings.Repeat("A", 64))
		}},
		{"working digest malformed", func(value *checkpointPriorCandidateV1) { value.Candidate.WorkingTreeDigest = "bad" }},
		{"direct files nil", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files = nil }},
		{"rebased files nil", func(value *checkpointPriorCandidateV1) { value.Candidate.RebasedTree.Files = nil }},
		{"empty path", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Path = "" }},
		{"NUL path", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Path = "config\x00.toml" }},
		{"backslash path", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Path = `state\v1` }},
		{"absolute path", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Path = "/config.toml" }},
		{"dot path", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Path = "." }},
		{"escape path", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Path = "../config.toml" }},
		{"unclean path", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Files[0].Path = "state//v1/project.json"
		}},
		{"unsorted paths", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Files[0], value.Candidate.DirectTree.Files[1] = value.Candidate.DirectTree.Files[1], value.Candidate.DirectTree.Files[0]
		}},
		{"duplicate paths", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Files[1].Path = value.Candidate.DirectTree.Files[0].Path
		}},
		{"unknown snapshot file", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Files = append(value.Candidate.DirectTree.Files, checkpointPriorFileV1{Path: "unknown", Data: []byte("x")})
		}},
		{"missing snapshot file", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Files = value.Candidate.DirectTree.Files[1:]
		}},
		{"noncanonical snapshot file", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Files[1].Data = append(value.Candidate.DirectTree.Files[1].Data, '\n')
		}},
		{"changed bytes", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Files[0].Data[0] ^= 1 }},
		{"malformed tree digest", func(value *checkpointPriorCandidateV1) { value.Candidate.DirectTree.Digest = "bad" }},
		{"incorrect tree digest", func(value *checkpointPriorCandidateV1) {
			value.Candidate.DirectTree.Digest = publicationRepeatedDigest('b')
		}},
		{"working direct mismatch", func(value *checkpointPriorCandidateV1) {
			value.Candidate.WorkingTreeDigest = publicationRepeatedDigest('b')
		}},
		{"negative boundary", func(value *checkpointPriorCandidateV1) { value.Candidate.RebasedThroughGeneration = -1 }},
		{"nil rebased positive boundary", func(value *checkpointPriorCandidateV1) {
			value.Candidate.RebasedTree = nil
			value.Candidate.RebasedThroughGeneration = 1
		}},
		{"invalid provenance", func(value *checkpointPriorCandidateV1) { value.Candidate.ImportedBy = "system:other" }},
		{"zero imported time", func(value *checkpointPriorCandidateV1) { value.Candidate.ImportedAt = time.Time{} }},
		{"nonzero offset imported time", func(value *checkpointPriorCandidateV1) {
			value.Candidate.ImportedAt = time.Date(2026, 8, 1, 14, 0, 0, 0, time.FixedZone("plus-one", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := checkpointPriorCandidateFixture(t, true, 7, "11111111-1111-4111-8111-111111111111", 15)
			test.mutate(&value)
			assertCheckpointPriorRejected(t, value)
		})
	}

	t.Run("invalid UTF-8 path", func(t *testing.T) {
		raw := strings.Replace(checkpointPriorCandidateDirectGolden, "config.toml", string([]byte{0xff}), 1)
		assertCheckpointPriorDecodeRejected(t, raw)
	})

	t.Run("rebased project mismatch", func(t *testing.T) {
		value := checkpointPriorCandidateFixture(t, true, 0, types.CandidateImportOriginGitObservationRebaseV1, 14)
		other := checkpointPriorTreeFixture(t, "00000000-0000-4000-8000-000000000003", types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-1", CanonicalRemote: "https://github.com/acme/wormhole"})
		value.Candidate.RebasedTree = &other
		assertCheckpointPriorRejected(t, value)
	})
	t.Run("rebased repository mismatch", func(t *testing.T) {
		value := checkpointPriorCandidateFixture(t, true, 0, types.CandidateImportOriginGitObservationRebaseV1, 14)
		other := checkpointPriorTreeFixture(t, "00000000-0000-4000-8000-000000000001", types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"})
		value.Candidate.RebasedTree = &other
		assertCheckpointPriorRejected(t, value)
	})
}

func TestCheckpointPriorCandidateCodecLimits(t *testing.T) {
	if maxCheckpointPriorTreeFiles != 10_000 || maxCheckpointPriorTreePathBytes != 4<<10 ||
		maxCheckpointPriorTreeFileBytes != 16<<20 || maxCheckpointPriorTreeTotalBytes != 64<<20 {
		t.Fatalf("production limits = (%d, %d, %d, %d), want (10000, 4096, 16777216, 67108864)",
			maxCheckpointPriorTreeFiles, maxCheckpointPriorTreePathBytes, maxCheckpointPriorTreeFileBytes, maxCheckpointPriorTreeTotalBytes)
	}
	value := checkpointPriorCandidateFixture(t, false, 0, "11111111-1111-4111-8111-111111111111", 13)
	raw := mustCanonicalCheckpointPrior(t, value)
	tree := value.Candidate.DirectTree
	longestPath, largestFile, total := 0, 0, 0
	for _, file := range tree.Files {
		if len(file.Path) > longestPath {
			longestPath = len(file.Path)
		}
		if len(file.Data) > largestFile {
			largestFile = len(file.Data)
		}
		total += len(file.Path) + len(file.Data)
	}
	exact := checkpointPriorTreeLimits{maxFiles: len(tree.Files), maxPathBytes: longestPath, maxFileBytes: int64(largestFile), maxTotalBytes: int64(total)}
	if got, err := decodeCheckpointPriorCandidateWithLimits(raw, exact); err != nil || !reflect.DeepEqual(got, value) {
		t.Fatalf("exact limits = (%+v, %v), want valid", got, err)
	}
	tests := []struct {
		name   string
		mutate func(*checkpointPriorTreeLimits)
	}{
		{"file count one over", func(limits *checkpointPriorTreeLimits) { limits.maxFiles-- }},
		{"path bytes one over", func(limits *checkpointPriorTreeLimits) { limits.maxPathBytes-- }},
		{"file bytes one over", func(limits *checkpointPriorTreeLimits) { limits.maxFileBytes-- }},
		{"aggregate bytes one over", func(limits *checkpointPriorTreeLimits) { limits.maxTotalBytes-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := exact
			test.mutate(&limits)
			if got, err := decodeCheckpointPriorCandidateWithLimits(raw, limits); err == nil || !reflect.DeepEqual(got, checkpointPriorCandidateV1{}) {
				t.Fatalf("over limit = (%+v, %v), want zero and error", got, err)
			}
		})
	}

	t.Run("rebased tree independently bounded", func(t *testing.T) {
		value := checkpointPriorCandidateFixture(t, true, 0, types.CandidateImportOriginGitObservationRebaseV1, 14)
		larger := checkpointPriorTreeWithActorFixture(t)
		value.Candidate.RebasedTree = &larger
		raw := mustCanonicalCheckpointPrior(t, value)
		limits := checkpointPriorTreeProductionLimits()
		limits.maxFiles = len(value.Candidate.DirectTree.Files)
		if got, err := decodeCheckpointPriorCandidateWithLimits(raw, limits); err == nil || !reflect.DeepEqual(got, checkpointPriorCandidateV1{}) {
			t.Fatalf("rebased over independent file limit = (%+v, %v), want zero and error", got, err)
		}
	})
}

func TestCheckpointPriorCandidateCodecDeterminismOwnershipAndNoMutation(t *testing.T) {
	value := checkpointPriorCandidateFixture(t, true, 7, "11111111-1111-4111-8111-111111111111", 15)
	wantInput := checkpointPriorCandidateFixture(t, true, 7, "11111111-1111-4111-8111-111111111111", 15)
	wantRaw, err := encodeCheckpointPriorCandidate(value)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 20; iteration++ {
		gotRaw, gotErr := encodeCheckpointPriorCandidate(value)
		if gotErr != nil || gotRaw != wantRaw {
			t.Fatalf("iteration %d = (%q, %v), want deterministic %q", iteration, gotRaw, gotErr, wantRaw)
		}
	}
	if !reflect.DeepEqual(value, wantInput) {
		t.Fatal("encoder mutated or aliased input")
	}
	left, err := decodeCheckpointPriorCandidate(wantRaw)
	if err != nil {
		t.Fatal(err)
	}
	right, err := decodeCheckpointPriorCandidate(wantRaw)
	if err != nil {
		t.Fatal(err)
	}
	left.Candidate.DirectTree.Files[0].Data[0] ^= 1
	left.Candidate.RebasedTree.Files[0].Data[0] ^= 1
	if bytes.Equal(left.Candidate.DirectTree.Files[0].Data, right.Candidate.DirectTree.Files[0].Data) ||
		bytes.Equal(left.Candidate.RebasedTree.Files[0].Data, right.Candidate.RebasedTree.Files[0].Data) || wantRaw != checkpointPriorCandidateRebasedPositiveGolden {
		t.Fatal("decoded file data aliases another result or raw input")
	}
}

func checkpointPriorCandidateFixture(t *testing.T, rebased bool, boundary int64, importedBy string, hour int) checkpointPriorCandidateV1 {
	t.Helper()
	direct := checkpointPriorTreeFixture(t, "00000000-0000-4000-8000-000000000001", types.RepositoryIdentity{
		Provider: "github", ImmutableID: "repository-1", CanonicalRemote: "https://github.com/acme/wormhole",
	})
	var rebasedTree *checkpointPriorTreeV1
	if rebased {
		copy := direct
		copy.Files = cloneCheckpointPriorFiles(direct.Files)
		rebasedTree = &copy
	}
	return checkpointPriorCandidateV1{
		SchemaVersion: 1, Kind: "checkpoint_prior_candidate",
		Candidate: &checkpointPriorCandidateStateV1{
			AcceptedBaseDigest: publicationRepeatedDigest('a'), WorkingTreeDigest: direct.Digest,
			DirectTree: direct, RebasedTree: rebasedTree, RebasedThroughGeneration: boundary,
			ImportedBy: importedBy, ImportedAt: time.Date(2026, 8, 1, hour, 0, 0, 0, time.UTC),
		},
	}
}

func checkpointPriorTreeFixture(t *testing.T, projectID string, repository types.RepositoryIdentity) checkpointPriorTreeV1 {
	t.Helper()
	tree := testSnapshotTree(t, projectID, repository)
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]checkpointPriorFileV1, len(tree))
	for index, file := range tree {
		files[index] = checkpointPriorFileV1{Path: file.Path, Data: append([]byte(nil), file.Data...)}
	}
	return checkpointPriorTreeV1{Digest: digest, Files: files}
}

func checkpointPriorTreeWithActorFixture(t *testing.T) checkpointPriorTreeV1 {
	t.Helper()
	repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-1", CanonicalRemote: "https://github.com/acme/wormhole"}
	snapshot, err := state.DecodeTree(testSnapshotTree(t, "00000000-0000-4000-8000-000000000001", repository))
	if err != nil {
		t.Fatal(err)
	}
	actorID := "44444444-4444-4444-8444-444444444444"
	snapshot.Actors[actorID] = state.Record[state.ActorV1]{Value: &state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: actorID, ActorKind: types.ActorHuman,
		DisplayName: "Checkpoint Actor", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]checkpointPriorFileV1, len(tree))
	for index, file := range tree {
		files[index] = checkpointPriorFileV1{Path: file.Path, Data: append([]byte(nil), file.Data...)}
	}
	return checkpointPriorTreeV1{Digest: digest, Files: files}
}

func cloneCheckpointPriorFiles(files []checkpointPriorFileV1) []checkpointPriorFileV1 {
	clone := make([]checkpointPriorFileV1, len(files))
	for index, file := range files {
		clone[index] = checkpointPriorFileV1{Path: file.Path, Data: append([]byte(nil), file.Data...)}
	}
	return clone
}

func checkpointPublicationReviewFixture() checkpointPublicationReviewV1 {
	return checkpointPublicationReviewV1{
		SchemaVersion: 1,
		Kind:          "checkpoint_publication_review",
		Review:        publicationReviewFixture(),
		ReviewDigest:  publicationReviewEnvelopeDigest,
		CheckpointedBy: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "22222222-2222-4222-8222-222222222222",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		},
	}
}

func checkpointPublicationAgentActor() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: "33333333-3333-4333-8333-333333333333",
		AccountableHumanID: "22222222-2222-4222-8222-222222222222", SessionID: "session-1",
		HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
}

func assertCheckpointPublicationRejected(t *testing.T, value checkpointPublicationReviewV1) {
	t.Helper()
	if raw, err := encodeCheckpointPublicationReview(value); err == nil || raw != "" {
		t.Fatalf("encode invalid publication proof = (%q, %v), want empty and error", raw, err)
	}
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpointPublicationDecodeRejected(t, string(canonical))
}

func assertCheckpointPublicationDecodeRejected(t *testing.T, raw string) {
	t.Helper()
	if got, err := decodeCheckpointPublicationReview(raw); err == nil || !reflect.DeepEqual(got, checkpointPublicationReviewV1{}) {
		t.Fatalf("decode invalid publication proof = (%+v, %v), want zero and error", got, err)
	}
}

func assertCheckpointPriorRejected(t *testing.T, value checkpointPriorCandidateV1) {
	t.Helper()
	if raw, err := encodeCheckpointPriorCandidate(value); err == nil || raw != "" {
		t.Fatalf("encode invalid prior proof = (%q, %v), want empty and error", raw, err)
	}
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpointPriorDecodeRejected(t, string(canonical))
}

func assertCheckpointPriorDecodeRejected(t *testing.T, raw string) {
	t.Helper()
	if got, err := decodeCheckpointPriorCandidate(raw); err == nil || !reflect.DeepEqual(got, checkpointPriorCandidateV1{}) {
		t.Fatalf("decode invalid prior proof = (%+v, %v), want zero and error", got, err)
	}
}

func mustCanonicalCheckpointPrior(t *testing.T, value checkpointPriorCandidateV1) string {
	t.Helper()
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
