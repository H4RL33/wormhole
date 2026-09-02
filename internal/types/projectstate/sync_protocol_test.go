package projectstate

import (
	"encoding/json"
	"errors"
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
func TestPublicProofMessageGolden(t *testing.T) {
	var nonce [32]byte
	copy(nonce[:], []byte("01234567890123456789012345678901"))
	got, err := PublicProofMessage(
		"11111111-1111-4111-8111-111111111111",
		"wormhole.sync.push",
		"attachment:44444444-4444-4444-8444-444444444444:session:55555555-5555-4555-8555-555555555555",
		[]byte("{\"version\":2}\n"),
		time.Date(2026, 8, 29, 12, 0, 0, 123456789, time.UTC),
		nonce,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "wormhole-public-v1\n" +
		"11111111-1111-4111-8111-111111111111\n" +
		"wormhole.sync.push\n" +
		"attachment:44444444-4444-4444-8444-444444444444:session:55555555-5555-4555-8555-555555555555\n" +
		"aab41f219a4fbdfdfc305d8b58700f569a96ed6112a6b62a95a7929dc3da3471\n" +
		"2026-08-29T12:00:00.123456789Z\n" +
		"MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE"
	if string(got) != want {
		t.Fatalf("proof message = %q, want %q", got, want)
	}
}

func TestPublicProofMessageRejectsNonCanonicalAuthorityInputs(t *testing.T) {
	var nonce [32]byte
	validTime := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, fabric, tool, scope string
		arguments                 []byte
		at                        time.Time
	}{
		{name: "fabric", tool: "wormhole.sync.pull", scope: "attachment:x", arguments: []byte("{}\n"), at: validTime},
		{name: "tool newline", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull\nother", scope: "attachment:x", arguments: []byte("{}\n"), at: validTime},
		{name: "scope newline", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull", scope: "attachment:x\nother", arguments: []byte("{}\n"), at: validTime},
		{name: "arguments", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull", scope: "attachment:x", at: validTime},
		{name: "time", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull", scope: "attachment:x", arguments: []byte("{}\n"), at: validTime.In(time.FixedZone("offset", 3600))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PublicProofMessage(test.fabric, test.tool, test.scope, test.arguments, test.at, nonce); !errors.Is(err, ErrInvalidPublicProofMessage) {
				t.Fatalf("error=%v, want ErrInvalidPublicProofMessage", err)
			}
		})
	}
}

func TestCanonicalJSONObjectRecursivelySortsStructKeysWithoutRewritingCanonicalWireBytes(t *testing.T) {
	type nested struct {
		Zulu  int `json:"zulu"`
		Alpha int `json:"alpha"`
	}
	type arguments struct {
		Version       int    `json:"version"`
		Nested        nested `json:"nested"`
		AttachmentRef string `json:"attachment_ref"`
	}
	want := []byte(`{"attachment_ref":"attachment","nested":{"alpha":1,"zulu":2},"version":1}`)
	got, err := CanonicalJSONObject(arguments{
		Version: 1, Nested: nested{Zulu: 2, Alpha: 1}, AttachmentRef: "attachment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CanonicalJSONObject = %s, want %s", got, want)
	}
	reencoded, err := CanonicalJSONObject(json.RawMessage(want))
	if err != nil || !reflect.DeepEqual(reencoded, want) {
		t.Fatalf("canonical wire round trip = (%s,%v), want (%s,nil)", reencoded, err, want)
	}
	normalized, err := CanonicalJSONObject(json.RawMessage(`{"version":1e0}`))
	if err != nil || string(normalized) != `{"version":1}` {
		t.Fatalf("canonical number = (%s,%v), want ({\"version\":1},nil)", normalized, err)
	}
	for _, value := range []any{json.RawMessage(`[1]`), json.RawMessage(`null`), func() {}} {
		if got, err := CanonicalJSONObject(value); got != nil || err == nil {
			t.Fatalf("CanonicalJSONObject(%T) = (%s,%v), want (nil,error)", value, got, err)
		}
	}
}
