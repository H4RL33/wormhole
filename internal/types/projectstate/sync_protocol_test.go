package projectstate

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestRepositoryScopeProjectionGolden(t *testing.T) {
	repository := types.RepositoryIdentity{
		Provider: "github", ImmutableID: "123456",
		CanonicalRemote: "https://github.com/H4RL33/wormhole",
	}
	projection, err := RepositoryScopeProjection(repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	wantProjection := "{\"provider\":\"github\",\"immutable_id\":\"123456\",\"canonical_ref\":\"refs/heads/main\"}\n"
	if string(projection) != wantProjection {
		t.Fatalf("projection = %q, want %q", projection, wantProjection)
	}
	scope, err := RepositoryScopeKey(repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	wantScope := "repository:sha256:6affdf263e9aac6b09e1196d401d97b4a17fcebf1df54551d99b39c465ccf349"
	if scope != wantScope {
		t.Fatalf("scope = %q, want %q", scope, wantScope)
	}
	changedRemote := repository
	changedRemote.CanonicalRemote = "https://mirror.example.test/H4RL33/wormhole"
	changedScope, err := RepositoryScopeKey(changedRemote, "refs/heads/main")
	if err != nil || changedScope != scope {
		t.Fatalf("canonical remote changed public projection: scope=%q err=%v", changedScope, err)
	}
}

func TestRepositoryScopeRejectsIncompleteInputs(t *testing.T) {
	valid := types.RepositoryIdentity{Provider: "github", ImmutableID: "123456", CanonicalRemote: "https://github.com/H4RL33/wormhole"}
	for name, input := range map[string]struct {
		repository types.RepositoryIdentity
		ref        string
	}{
		"local-only repository": {repository: types.RepositoryIdentity{}, ref: "refs/heads/main"},
		"invalid repository":    {repository: types.RepositoryIdentity{Provider: "github"}, ref: "refs/heads/main"},
		"empty ref":             {repository: valid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RepositoryScopeKey(input.repository, input.ref); err == nil {
				t.Fatal("RepositoryScopeKey accepted incomplete input")
			}
		})
	}
}

func TestSyncProtocolRecordsRoundTripWithExactEmptyLists(t *testing.T) {
	state := SyncPullV2Result{
		Version: SyncProtocolVersionV2,
		State: SyncStateV2{
			StreamVersion: 3, AcceptedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AcceptedTreeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			LiveTreeDigest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			AcceptedTree:       Tree{}, LiveTree: Tree{}, OpenConflictIDs: []string{},
		},
	}
	got, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"version":2,"changed":false,"state":{"stream_version":3,"accepted_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","accepted_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","live_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","accepted_tree":[],"live_tree":[],"open_conflict_ids":[]}}` {
		t.Fatalf("state JSON = %s", got)
	}
	var decoded SyncPullV2Result
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, state) {
		t.Fatalf("round trip = %#v, want %#v", decoded, state)
	}
}

func TestPublicAgentSessionIssueCarriesSharedAssuranceAndTime(t *testing.T) {
	want := PublicAgentSessionIssueV2Result{
		Version: 2, SessionID: "session", AgentID: "agent", AccountableHumanID: "human",
		HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5",
		Assurance: types.AssurancePublicKeyContinuity,
		ExpiresAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got PublicAgentSessionIssueV2Result
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("session round trip = %#v, %v; want %#v", got, err, want)
	}
}
