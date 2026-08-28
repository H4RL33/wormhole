package git

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	observerProjectID = "00000000-0000-4000-8000-000000000001"
	observerCommitA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observerCommitB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	observerTreeA     = "cccccccccccccccccccccccccccccccccccccccc"
	observerTreeB     = "dddddddddddddddddddddddddddddddddddddddd"
)

type observerCredentialFixture struct {
	mu        sync.Mutex
	reference string
	secret    string
	err       error
	calls     []string
}

func (s *observerCredentialFixture) ReadServerCredential(ctx context.Context, reference string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, reference)
	if s.err != nil {
		return "", s.err
	}
	if reference != s.reference {
		return "", errors.New("credential unavailable")
	}
	return s.secret, nil
}

func (s *observerCredentialFixture) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

type githubObserverFixture struct {
	t                 *testing.T
	repositoryID      string
	repositoryReplyID string
	repository        types.RepositoryIdentity
	ref               string
	commit            string
	commitTree        string
	treeSHA           string
	tree              projectstate.Tree
	treeEntries       []map[string]any
	treeEntriesB      []map[string]any
	blobs             map[string]map[string]any
	blobsB            map[string]map[string]any
	requests          []string
	authorization     []string
	onRef             func()
	commitReply       map[string]any
	treeReply         map[string]any
	server            *httptest.Server
	mu                sync.Mutex
}

func newGitHubObserverFixture(t *testing.T) *githubObserverFixture {
	t.Helper()
	repository := types.RepositoryIdentity{
		Provider: "github", ImmutableID: "123456789",
		CanonicalRemote: "https://github.com/wormhole/observer-test",
	}
	tree := streamTestTree(t, observerProjectID, repository)
	fixture := &githubObserverFixture{
		t: t, repositoryID: repository.ImmutableID, repositoryReplyID: repository.ImmutableID, repository: repository,
		ref: "refs/heads/main", commit: observerCommitA, commitTree: observerTreeA,
		treeSHA: observerTreeA, tree: tree, blobs: make(map[string]map[string]any), blobsB: make(map[string]map[string]any),
	}
	for index, file := range tree {
		sha := fmt.Sprintf("%040x", index+1)
		fixture.treeEntries = append(fixture.treeEntries, map[string]any{
			"path": ".wormhole/" + file.Path, "mode": "100644", "type": "blob", "sha": sha, "size": len(file.Data),
		})
		fixture.blobs[sha] = map[string]any{
			"sha": sha, "size": len(file.Data), "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(file.Data),
		}
	}
	snapshotB, err := projectstate.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	snapshotB.Project.Name = "Moved ref tree"
	treeB, err := projectstate.EncodeTree(snapshotB)
	if err != nil {
		t.Fatal(err)
	}
	for index, file := range treeB {
		sha := fmt.Sprintf("%040x", index+1000)
		fixture.treeEntriesB = append(fixture.treeEntriesB, map[string]any{
			"path": ".wormhole/" + file.Path, "mode": "100644", "type": "blob", "sha": sha, "size": len(file.Data),
		})
		fixture.blobsB[sha] = map[string]any{
			"sha": sha, "size": len(file.Data), "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(file.Data),
		}
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *githubObserverFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.RequestURI())
	f.authorization = append(f.authorization, r.Header.Get("Authorization"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/repositories/"+f.repositoryID:
		writeObserverJSON(f.t, w, map[string]any{"id": json.Number(f.repositoryReplyID)})
	case r.URL.Path == "/repositories/"+f.repositoryID+"/git/ref/heads/main":
		fallthrough
	case strings.HasPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/ref/heads/"):
		f.mu.Lock()
		commit := f.commit
		f.mu.Unlock()
		writeObserverJSON(f.t, w, map[string]any{
			"ref": f.ref, "object": map[string]any{"type": "commit", "sha": commit},
		})
		if f.onRef != nil {
			f.onRef()
		}
	case strings.HasPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/commits/"):
		if f.commitReply != nil {
			writeObserverJSON(f.t, w, f.commitReply)
			return
		}
		sha := strings.TrimPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/commits/")
		if sha != observerCommitA && sha != observerCommitB {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		treeSHA := f.commitTree
		f.mu.Unlock()
		if sha == observerCommitB {
			treeSHA = observerTreeB
		}
		writeObserverJSON(f.t, w, map[string]any{"sha": sha, "tree": map[string]any{"sha": treeSHA}})
	case strings.HasPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/trees/"):
		if f.treeReply != nil {
			writeObserverJSON(f.t, w, f.treeReply)
			return
		}
		requestedSHA := strings.TrimPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/trees/")
		if requestedSHA == observerTreeB {
			writeObserverJSON(f.t, w, map[string]any{"sha": observerTreeB, "truncated": false, "tree": f.treeEntriesB})
			return
		}
		writeObserverJSON(f.t, w, map[string]any{"sha": f.treeSHA, "truncated": false, "tree": f.treeEntries})
	case strings.HasPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/blobs/"):
		sha := strings.TrimPrefix(r.URL.Path, "/repositories/"+f.repositoryID+"/git/blobs/")
		blob, ok := f.blobs[sha]
		if !ok {
			blob, ok = f.blobsB[sha]
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeObserverJSON(f.t, w, blob)
	default:
		http.NotFound(w, r)
	}
}

func writeObserverJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fake GitHub response: %v", err)
	}
}

func (f *githubObserverFixture) newObserver(t *testing.T, credentials GitCredentialSource) *GitHubObserver {
	t.Helper()
	observer, err := NewGitHubObserver(f.server.URL, credentials)
	if err != nil {
		t.Fatalf("NewGitHubObserver: %v", err)
	}
	return observer
}

func (f *githubObserverFixture) requestLog() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...), append([]string(nil), f.authorization...)
}

func TestGitHubObserverUsesProviderRepositoryID(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	observer := fixture.newObserver(t, nil)
	observation, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if observation.Repository != fixture.repository || observation.RefName != fixture.ref || observation.CommitSHA != observerCommitA || observation.ObservedAt.IsZero() {
		t.Fatalf("observation = %+v", observation)
	}
	requests, _ := fixture.requestLog()
	wantPrefix := []string{
		"/repositories/123456789",
		"/repositories/123456789/git/ref/heads/main",
		"/repositories/123456789/git/commits/" + observerCommitA,
		"/repositories/123456789/git/trees/" + observerTreeA + "?recursive=1",
	}
	if len(requests) < len(wantPrefix) {
		t.Fatalf("requests = %v, want prefix %v", requests, wantPrefix)
	}
	for index := range wantPrefix {
		if requests[index] != wantPrefix[index] {
			t.Fatalf("request %d = %q, want %q (all requests %v)", index, requests[index], wantPrefix[index], requests)
		}
	}
	if requests[0] != "/repositories/123456789" {
		t.Fatalf("first request = %v, want provider repository ID endpoint", requests)
	}
	for _, request := range requests {
		if strings.Contains(request, "wormhole/observer-test") || strings.Contains(request, "/repos/") {
			t.Fatalf("observer used mutable repository name endpoint %q", request)
		}
	}
}

func TestGitHubObserverPathEscapesNestedRef(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	fixture.ref = "refs/heads/feature/one"
	observer := fixture.newObserver(t, nil)
	if _, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, ""); err != nil {
		t.Fatal(err)
	}
	requests, _ := fixture.requestLog()
	if len(requests) < 2 || requests[1] != "/repositories/123456789/git/ref/heads/feature%2Fone" {
		t.Fatalf("ref request = %v, want one path-escaped branch segment", requests)
	}
}

func TestGitHubObserverReadsTreeAtResolvedCommitNotMovingRef(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	fixture.onRef = func() {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.commit = observerCommitB
	}
	observer := fixture.newObserver(t, nil)
	observation, got, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if observation.CommitSHA != observerCommitA {
		t.Fatalf("observed commit = %q, want resolved commit A", observation.CommitSHA)
	}
	assertObserverTreeEqual(t, fixture.tree, got)
	requests, _ := fixture.requestLog()
	refRequests := 0
	for _, request := range requests {
		if strings.Contains(request, "/git/ref/") {
			refRequests++
		}
	}
	if refRequests != 1 || !containsObserverRequest(requests, "/git/commits/"+observerCommitA) ||
		!containsObserverRequest(requests, "/git/trees/"+observerTreeA) || containsObserverRequest(requests, "/git/trees/"+observerTreeB) {
		t.Fatalf("requests = %v, want one ref read followed by commit A", requests)
	}
}

func TestGitHubObserverRejectsCommitMismatch(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	fixture.commitReply = map[string]any{"sha": observerCommitB, "tree": map[string]any{"sha": observerTreeB}}
	observer := fixture.newObserver(t, nil)
	if _, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, ""); !errors.Is(err, ErrGitObservation) {
		t.Fatalf("ObserveRef error = %v, want ErrGitObservation", err)
	}
	requests, _ := fixture.requestLog()
	if containsObserverRequest(requests, "/git/trees/") || containsObserverRequest(requests, "/git/blobs/") {
		t.Fatalf("observer continued after commit mismatch: %v", requests)
	}
}

func TestGitHubObserverFetchesOnlyWormholeBlobs(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	fixture.treeEntries = append(fixture.treeEntries,
		map[string]any{"path": "README.md", "mode": "100644", "type": "blob", "sha": strings.Repeat("e", 40), "size": 12},
		map[string]any{"path": "cmd/fabric/main.go", "mode": "100644", "type": "blob", "sha": strings.Repeat("f", 40), "size": 100},
	)
	observer := fixture.newObserver(t, nil)
	_, got, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, "")
	if err != nil {
		t.Fatal(err)
	}
	assertObserverTreeEqual(t, fixture.tree, got)
	requests, _ := fixture.requestLog()
	if containsObserverRequest(requests, strings.Repeat("e", 40)) || containsObserverRequest(requests, strings.Repeat("f", 40)) {
		t.Fatalf("observer fetched repository source: %v", requests)
	}
}

func TestGitHubObserverPublicSendsNoAuthorization(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	credentials := &observerCredentialFixture{reference: "server:private", secret: "private-token"}
	observer := fixture.newObserver(t, credentials)
	if _, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, ""); err != nil {
		t.Fatal(err)
	}
	_, authorizations := fixture.requestLog()
	for _, authorization := range authorizations {
		if authorization != "" {
			t.Fatal("public request sent an Authorization header")
		}
	}
	if calls := credentials.Calls(); len(calls) != 0 {
		t.Fatal("public observation read a server credential")
	}
}

func TestGitHubObserverPrivateUsesServerCredentialOnly(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	credentials := &observerCredentialFixture{reference: "server:private", secret: "private-token"}
	observer := fixture.newObserver(t, credentials)
	if _, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, "server:private"); err != nil {
		t.Fatal(err)
	}
	if calls := credentials.Calls(); len(calls) != 1 || calls[0] != "server:private" {
		t.Fatal("private observation did not resolve exactly the configured server credential reference")
	}
	_, authorizations := fixture.requestLog()
	for _, authorization := range authorizations {
		if authorization != "Bearer private-token" {
			t.Fatal("private request did not use the resolved server credential")
		}
	}
}

func TestGitHubObserverCredentialFailureDoesNotExposeSourceDetail(t *testing.T) {
	fixture := newGitHubObserverFixture(t)
	credentials := &observerCredentialFixture{
		err: errors.New("vault server:private/path exposed github-secret"),
	}
	observer := fixture.newObserver(t, credentials)
	_, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, "server:private/path")
	if !errors.Is(err, ErrGitObservation) {
		t.Fatal("credential source failure did not return ErrGitObservation")
	}
	for _, forbidden := range []string{"server:private/path", "github-secret", "vault"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("credential error exposed forbidden source detail category %q", forbidden)
		}
	}
	requests, _ := fixture.requestLog()
	if len(requests) != 0 {
		t.Fatalf("credential failure performed network requests: %v", requests)
	}
}

func TestGitHubObserverRejectsCredentialedRedirectBeforeFollow(t *testing.T) {
	redirected := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected++
		if r.Header.Get("Authorization") != "" {
			t.Error("credential followed redirect")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	credentials := &observerCredentialFixture{reference: "server:private", secret: "private-token"}
	observer, err := NewGitHubObserver(origin.URL, credentials)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = observer.ObserveRef(context.Background(), observerRepository(), "refs/heads/main", "server:private")
	if !errors.Is(err, ErrGitObservation) {
		t.Fatalf("redirect error = %v, want ErrGitObservation", err)
	}
	if redirected != 0 {
		t.Fatalf("redirect destination received %d requests", redirected)
	}
}

func TestGitHubObserverRejectsCrossOriginRedirect(t *testing.T) {
	redirected := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/other", http.StatusFound)
	}))
	defer origin.Close()
	observer, err := NewGitHubObserver(origin.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = observer.ObserveRef(context.Background(), observerRepository(), "refs/heads/main", "")
	if !errors.Is(err, ErrGitObservation) {
		t.Fatalf("redirect error = %v, want ErrGitObservation", err)
	}
	if redirected != 0 {
		t.Fatalf("cross-origin redirect destination received %d requests", redirected)
	}
}

func TestGitHubObserverBoundsTree(t *testing.T) {
	t.Run("more than ten thousand entries", func(t *testing.T) {
		fixture := newGitHubObserverFixture(t)
		entries := make([]map[string]any, 10_001)
		for index := range entries {
			entries[index] = map[string]any{"path": fmt.Sprintf("source/%05d.go", index), "mode": "100644", "type": "blob", "sha": fmt.Sprintf("%040x", index+1), "size": 1}
		}
		fixture.treeEntries = entries
		assertObserverRejected(t, fixture)
	})
	t.Run("truncated tree", func(t *testing.T) {
		fixture := newGitHubObserverFixture(t)
		fixture.treeReply = map[string]any{"sha": observerTreeA, "truncated": true, "tree": fixture.treeEntries}
		assertObserverRejected(t, fixture)
	})
	t.Run("blob over one MiB", func(t *testing.T) {
		fixture := newGitHubObserverFixture(t)
		fixture.treeEntries[0]["size"] = 1<<20 + 1
		assertObserverRejected(t, fixture)
	})
	t.Run("aggregate over sixteen MiB", func(t *testing.T) {
		fixture := newGitHubObserverFixture(t)
		fixture.treeEntries = nil
		fixture.blobs = make(map[string]map[string]any)
		data := bytes.Repeat([]byte("x"), 1<<20)
		for index := 0; index < 17; index++ {
			sha := fmt.Sprintf("%040x", index+100)
			fixture.treeEntries = append(fixture.treeEntries, map[string]any{"path": fmt.Sprintf(".wormhole/state/v1/events/%02d.json", index), "mode": "100644", "type": "blob", "sha": sha, "size": len(data)})
			fixture.blobs[sha] = map[string]any{"sha": sha, "size": len(data), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)}
		}
		assertObserverRejected(t, fixture)
	})
	for _, entry := range []map[string]any{
		{"path": ".wormhole/config.toml", "mode": "120000", "type": "blob", "sha": strings.Repeat("1", 40), "size": 5},
		{"path": ".wormhole/vendor", "mode": "160000", "type": "commit", "sha": strings.Repeat("2", 40)},
		{"path": ".wormhole/../secret", "mode": "100644", "type": "blob", "sha": strings.Repeat("3", 40), "size": 5},
	} {
		name := fmt.Sprint(entry["mode"])
		t.Run(name, func(t *testing.T) {
			fixture := newGitHubObserverFixture(t)
			fixture.treeEntries = []map[string]any{entry}
			assertObserverRejected(t, fixture)
		})
	}
}

func TestGitHubObserverRejectsInconsistentProviderObjects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*githubObserverFixture)
	}{
		{"repository ID", func(f *githubObserverFixture) { f.repositoryReplyID = "987654321" }},
		{"tree SHA", func(f *githubObserverFixture) { f.treeSHA = observerTreeB }},
		{"blob SHA", func(f *githubObserverFixture) {
			for _, blob := range f.blobs {
				blob["sha"] = strings.Repeat("9", 40)
				break
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitHubObserverFixture(t)
			test.mutate(fixture)
			assertObserverRejected(t, fixture)
		})
	}
}

func TestGitHubObserverRejectsUnsafeAPIBaseURL(t *testing.T) {
	for _, test := range []struct {
		name, baseURL string
	}{
		{"userinfo", "https://user:secret@api.github.com"},
		{"query", "https://api.github.com?token=secret"},
		{"fragment", "https://api.github.com#fragment"},
		{"scheme", "file:///tmp/github"},
		{"dot path", "https://api.github.com/../other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewGitHubObserver(test.baseURL, nil); !errors.Is(err, ErrGitObservation) {
				t.Fatalf("NewGitHubObserver error = %v, want ErrGitObservation", err)
			}
		})
	}
}

func TestFakeObserverIsDeterministic(t *testing.T) {
	repository := observerRepository()
	tree := streamTestTree(t, observerProjectID, repository)
	fake := &FakeObserver{}
	fake.SetRef(repository, "refs/heads/main", observerCommitA, tree)

	firstObservation, firstTree, err := fake.ObserveRef(context.Background(), repository, "refs/heads/main", "server:unused")
	if err != nil {
		t.Fatal(err)
	}
	tree[0].Data[0] ^= 0xff
	firstTree[0].Data[0] ^= 0xff
	secondObservation, secondTree, err := fake.ObserveRef(context.Background(), repository, "refs/heads/main", "server:unused")
	if err != nil {
		t.Fatal(err)
	}
	if firstObservation != secondObservation || secondObservation.CommitSHA != observerCommitA {
		t.Fatalf("fake observations differ: first=%+v second=%+v", firstObservation, secondObservation)
	}
	if bytes.Equal(firstTree[0].Data, secondTree[0].Data) || secondTree[0].Data[0] == tree[0].Data[0] {
		t.Fatal("fake returned aliased tree bytes")
	}
	calls := fake.Calls()
	if len(calls) != 2 || calls[0] != calls[1] || calls[0].ObserverCredentialRef != "server:unused" {
		t.Fatal("fake observer call log was not deterministic")
	}
}

func assertObserverRejected(t *testing.T, fixture *githubObserverFixture) {
	t.Helper()
	observer := fixture.newObserver(t, nil)
	if _, _, err := observer.ObserveRef(context.Background(), fixture.repository, fixture.ref, ""); !errors.Is(err, ErrGitObservation) {
		t.Fatalf("ObserveRef error = %v, want ErrGitObservation", err)
	}
}

func observerRepository() types.RepositoryIdentity {
	return types.RepositoryIdentity{Provider: "github", ImmutableID: "123456789", CanonicalRemote: "https://github.com/wormhole/observer-test"}
}

func containsObserverRequest(requests []string, part string) bool {
	for _, request := range requests {
		if strings.Contains(request, part) {
			return true
		}
	}
	return false
}

func assertObserverTreeEqual(t *testing.T, want, got projectstate.Tree) {
	t.Helper()
	want = cloneObserverTree(want)
	got = cloneObserverTree(got)
	sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
	sort.Slice(got, func(i, j int) bool { return got[i].Path < got[j].Path })
	if len(want) != len(got) {
		t.Fatalf("tree length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if want[index].Path != got[index].Path || !bytes.Equal(want[index].Data, got[index].Data) {
			t.Fatalf("tree file %d differs: got %q=%q want %q=%q", index, got[index].Path, got[index].Data, want[index].Path, want[index].Data)
		}
	}
}

func cloneObserverTree(tree projectstate.Tree) projectstate.Tree {
	cloned := make(projectstate.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = projectstate.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return cloned
}
