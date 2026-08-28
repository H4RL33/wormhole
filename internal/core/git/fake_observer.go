package git

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

// ObserverCall is one immutable FakeObserver invocation.
type ObserverCall struct {
	Repository            types.RepositoryIdentity
	RefName               string
	ObserverCredentialRef string
}

type fakeObservation struct {
	commitSHA string
	tree      projectstate.Tree
}

// FakeObserver is a deterministic, concurrency-safe observer for local tests.
type FakeObserver struct {
	mu       sync.Mutex
	fixtures map[string]fakeObservation
	calls    []ObserverCall
}

// SetRef replaces one repository/ref fixture with deep-cloned canonical bytes.
func (f *FakeObserver) SetRef(repository types.RepositoryIdentity, refName, commitSHA string, tree projectstate.Tree) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fixtures == nil {
		f.fixtures = make(map[string]fakeObservation)
	}
	f.fixtures[fakeObserverKey(repository, refName)] = fakeObservation{commitSHA: commitSHA, tree: cloneObservedTree(tree)}
}

// ObserveRef returns a deep clone and a fixed timestamp so repeated fixtures
// remain byte-for-byte deterministic.
func (f *FakeObserver) ObserveRef(ctx context.Context, repository types.RepositoryIdentity, refName, observerCredentialRef string) (RefObservation, projectstate.Tree, error) {
	if err := ctx.Err(); err != nil {
		return RefObservation{}, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ObserverCall{Repository: repository, RefName: refName, ObserverCredentialRef: observerCredentialRef})
	fixture, ok := f.fixtures[fakeObserverKey(repository, refName)]
	if !ok {
		return RefObservation{}, nil, fmt.Errorf("git: fake observe ref: %w", ErrGitObservation)
	}
	return RefObservation{
		Repository: repository, RefName: refName, CommitSHA: fixture.commitSHA,
		ObservedAt: time.Unix(0, 0).UTC(),
	}, cloneObservedTree(fixture.tree), nil
}

// Calls returns a deep copy of the ordered invocation log.
func (f *FakeObserver) Calls() []ObserverCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ObserverCall(nil), f.calls...)
}

func fakeObserverKey(repository types.RepositoryIdentity, refName string) string {
	return repository.Provider + "\x00" + repository.ImmutableID + "\x00" + repository.CanonicalRemote + "\x00" + refName
}

func cloneObservedTree(tree projectstate.Tree) projectstate.Tree {
	cloned := make(projectstate.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = projectstate.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Path < cloned[j].Path })
	return cloned
}
