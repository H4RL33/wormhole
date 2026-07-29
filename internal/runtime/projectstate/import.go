package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrDirectImmutableRecordMutation = fmt.Errorf("projectstate: direct immutable record mutation: %w", state.ErrImmutableRecord)
	ErrDirectPathDeletion            = errors.New("projectstate: direct path deletion")
	ErrDirectEditTombstone           = errors.New("projectstate: direct tombstone edit")
	ErrDirectResurrection            = errors.New("projectstate: direct resurrection")
	ErrDirectImmutableFieldMutation  = errors.New("projectstate: direct immutable field mutation")
)

type ImportRequest struct {
	Scope                     types.WorkspaceScope
	Root                      string
	ExpectedWorkingTreeDigest *state.Digest
	Actor                     types.ActorEnvelope
}

type ImportResult struct {
	PreviousCandidateDigest  *state.Digest
	ImportedCandidateDigest  state.Digest
	ComposedViewDigest       state.Digest
	ImportedChangeCount      int
	RebasedThroughGeneration int64
	Conflicts                []Conflict
}

func (s *Service) Import(ctx context.Context, req ImportRequest) (ImportResult, error) {
	if s == nil || s.repo == nil || s.readWorkingTree == nil || s.now == nil {
		return ImportResult{}, fmt.Errorf("projectstate: service is unavailable")
	}
	if err := req.Actor.ValidateLocalAction(); err != nil {
		return ImportResult{}, err
	}
	if req.ExpectedWorkingTreeDigest != nil && !validImportDigest(*req.ExpectedWorkingTreeDigest) {
		return ImportResult{}, fmt.Errorf("projectstate: invalid expected working-tree digest")
	}
	if !types.CanonicalUUID(req.Scope.ProjectID) || !types.CanonicalUUID(string(req.Scope.WorkspaceID)) {
		return ImportResult{}, localstore.ErrNotFound
	}
	root, err := canonicalNonSymlinkDirectory(req.Root)
	if err != nil || root != req.Root {
		if err == nil {
			err = fmt.Errorf("root is not canonical")
		}
		return ImportResult{}, fmt.Errorf("projectstate: invalid import root: %w", err)
	}
	capturedTree, err := s.readWorkingTree(root)
	if err != nil {
		return ImportResult{}, err
	}
	capturedTree = cloneCheckpointTree(capturedTree)
	capturedDigest, err := state.DigestTree(capturedTree)
	if err != nil {
		return ImportResult{}, err
	}
	if req.ExpectedWorkingTreeDigest != nil && *req.ExpectedWorkingTreeDigest != capturedDigest {
		return ImportResult{}, fmt.Errorf("%w: expected %s, captured %s", ErrWorkingTreeChanged, *req.ExpectedWorkingTreeDigest, capturedDigest)
	}

	var result ImportResult
	err = s.repo.WithImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		workspace, err := tx.Workspace(ctx)
		if err != nil {
			return err
		}
		workspace, err = cloneImportWorkspace(workspace)
		if err != nil {
			return err
		}
		if workspace.Binding.Scope != req.Scope {
			return fmt.Errorf("projectstate: workspace scope differs from import request")
		}
		if err := validateImportCheckout(workspace.Binding, root); err != nil {
			return err
		}
		openConflicts, err := tx.OpenConflictOccurrences(ctx)
		if err != nil {
			return err
		}
		openConflicts = cloneImportOccurrences(openConflicts)
		if _, err := decodeWorkspaceConflictOccurrences(openConflicts); err != nil {
			return err
		}
		if (workspace.State == "conflicted") != (len(openConflicts) != 0) {
			return fmt.Errorf("projectstate: workspace conflict state does not match open conflict evidence")
		}
		candidate, err := tx.Candidate(ctx)
		if err != nil {
			return err
		}
		candidate, err = cloneImportCandidate(candidate)
		if err != nil {
			return err
		}
		priorSurface := workspace.Snapshot
		if candidate != nil {
			priorSurface = candidate.DirectSnapshot
		}
		priorTree, err := state.EncodeTree(priorSurface)
		if err != nil {
			return err
		}

		disposition, err := tx.MaterializationDisposition(ctx)
		if err != nil {
			return err
		}
		disposition = cloneImportDisposition(disposition)
		proof, err := proveMaterializationDisposition(disposition)
		if err != nil {
			return err
		}
		if err := rawDirectDeletionPreflight(priorSurface, capturedTree); err != nil {
			return err
		}
		liveSnapshot, err := state.DecodeTree(capturedTree)
		if err != nil {
			return err
		}
		canonicalLiveTree, err := state.EncodeTree(liveSnapshot)
		if err != nil {
			return err
		}
		if !equalCheckpointTree(canonicalLiveTree, capturedTree) || liveSnapshot.Digest != capturedDigest {
			return fmt.Errorf("projectstate: captured working tree is not exact canonical state")
		}
		directDiff, err := SemanticDiff(priorSurface, liveSnapshot, nil)
		if err != nil {
			return err
		}

		eligible, err := tx.AcceptanceEligibleMaterializationByCandidateDigest(ctx, capturedDigest)
		if err != nil {
			return err
		}
		if eligible != nil {
			cloned := cloneMaterializationRecord(*eligible)
			eligible = &cloned
			if _, err := requireMatchingMaterialization(proof, eligible, workspace.Binding, priorTree, capturedTree, capturedDigest); err != nil {
				return err
			}
		} else if err := ValidateDirectDelta(priorSurface, liveSnapshot); err != nil {
			return err
		}

		start, boundary := selectCandidateStart(workspace.Snapshot, candidate)
		activeRows := make([]localstore.WorkspaceOperation, 0)
		for _, row := range disposition.Operations {
			switch row.State {
			case "active":
				if row.Generation <= boundary {
					return fmt.Errorf("projectstate: active operation does not exceed selected candidate boundary")
				}
				activeRows = append(activeRows, cloneImportOperation(row))
			case "rebased":
				if row.Generation > boundary {
					return fmt.Errorf("projectstate: rebased operation exceeds selected candidate boundary")
				}
			}
		}
		operations, err := decodeStoredOperations(activeRows)
		if err != nil {
			return err
		}
		oldComposed, err := Compose(start, boundary, operations)
		if err != nil {
			return err
		}
		merged, err := ThreeWayRebase(priorSurface, liveSnapshot, oldComposed.Snapshot)
		if err != nil {
			return err
		}
		evidence, err := encodeWorkspaceConflictEvidence(merged.Conflicts)
		if err != nil {
			return err
		}
		mutationTime := s.now().UTC()
		importedBy := req.Actor.PrincipalID()
		rebasedSnapshot, err := cloneImportSnapshot(merged.Snapshot)
		if err != nil {
			return err
		}
		directSnapshot, err := cloneImportSnapshot(liveSnapshot)
		if err != nil {
			return err
		}

		liveTree, err := s.readWorkingTree(root)
		if err != nil {
			return err
		}
		liveTree = cloneCheckpointTree(liveTree)
		liveDigest, err := state.DigestTree(liveTree)
		if err != nil {
			return err
		}
		if !equalCheckpointTree(liveTree, capturedTree) || liveDigest != capturedDigest {
			return fmt.Errorf("%w: second capture differs", ErrWorkingTreeChanged)
		}
		if err := validateImportCheckout(workspace.Binding, root); err != nil {
			return err
		}

		if _, err := tx.ReplaceOpenConflictOccurrences(ctx, evidence, mutationTime); err != nil {
			return err
		}
		if err := tx.UpsertCandidate(ctx, localstore.WorkspaceCandidateRecord{
			AcceptedBaseDigest: state.Digest(workspace.Binding.AcceptedTreeDigest),
			WorkingTreeDigest:  capturedDigest, DirectSnapshot: directSnapshot,
			RebasedSnapshot: &rebasedSnapshot, RebasedThroughGeneration: oldComposed.ThroughGeneration,
			ImportedBy: importedBy, ImportedAt: mutationTime,
		}); err != nil {
			return err
		}
		if err := tx.TransitionOperations(ctx, activeRows, "rebased", nil); err != nil {
			return err
		}
		workspaceState := "pending"
		if len(merged.Conflicts) != 0 {
			workspaceState = "conflicted"
		}
		if err := tx.SetStatus(ctx, workspaceState); err != nil {
			return err
		}

		var previous *state.Digest
		if candidate != nil || len(activeRows) != 0 {
			digest := oldComposed.Snapshot.Digest
			previous = &digest
		}
		result = ImportResult{
			PreviousCandidateDigest: previous, ImportedCandidateDigest: liveSnapshot.Digest,
			ComposedViewDigest: merged.Snapshot.Digest, ImportedChangeCount: len(directDiff.Changes),
			RebasedThroughGeneration: oldComposed.ThroughGeneration, Conflicts: cloneImportConflicts(merged.Conflicts),
		}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

func validImportDigest(value state.Digest) bool {
	raw := string(value)
	if len(raw) != len("sha256:")+64 || !strings.HasPrefix(raw, "sha256:") {
		return false
	}
	for _, char := range raw[len("sha256:"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validateImportCheckout(binding types.WorkspaceBinding, root string) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	canonical, err := canonicalNonSymlinkDirectory(root)
	if err != nil || canonical != root || canonical != binding.Checkout.CanonicalPath {
		if err == nil {
			err = fmt.Errorf("checkout root differs from binding")
		}
		return fmt.Errorf("%w: %w", ErrWorkingTreeChanged, err)
	}
	identity, err := checkoutIdentity(canonical)
	if err != nil || identity != binding.Checkout {
		if err == nil {
			err = fmt.Errorf("checkout identity differs from binding")
		}
		return fmt.Errorf("%w: %w", ErrWorkingTreeChanged, err)
	}
	return nil
}

func rawDirectDeletionPreflight(prior state.Snapshot, captured state.Tree) error {
	paths := make(map[string]struct{}, len(captured))
	for _, file := range captured {
		paths[file.Path] = struct{}{}
	}
	type stablePath struct {
		key  state.RecordKey
		path string
	}
	stable := []stablePath{{key: state.RecordKey{Kind: "project", ID: prior.Project.ID}, path: "state/v1/project.json"}}
	appendRecords := func(kind, prefix, suffix string, ids []string) {
		for _, id := range ids {
			stable = append(stable, stablePath{key: state.RecordKey{Kind: kind, ID: id}, path: prefix + id + suffix})
		}
	}
	appendRecords("actor", "state/v1/actors/", ".json", sortedImportIDs(prior.Actors))
	appendRecords("task", "state/v1/tasks/", ".json", sortedImportIDs(prior.Tasks))
	appendRecords("task_link", "state/v1/tasks/links/", ".json", sortedImportIDs(prior.TaskLinks))
	appendRecords("kb_article", "state/v1/kb/", "/record.json", sortedImportIDs(prior.Articles))
	appendRecords("channel", "state/v1/channels/", ".json", sortedImportIDs(prior.Channels))
	appendRecords("event", "state/v1/events/", ".json", sortedImportIDs(prior.Events))
	appendRecords("git_link", "state/v1/git-links/", ".json", sortedImportIDs(prior.GitLinks))
	sort.Slice(stable, func(i, j int) bool {
		left, right := diffKindRank(stable[i].key.Kind), diffKindRank(stable[j].key.Kind)
		if left != right {
			return left < right
		}
		return stable[i].key.ID < stable[j].key.ID
	})
	for _, record := range stable {
		if _, ok := paths[record.path]; ok {
			continue
		}
		if record.key.Kind == "event" || record.key.Kind == "git_link" {
			return fmt.Errorf("%w: %s %s", ErrDirectImmutableRecordMutation, record.key.Kind, record.key.ID)
		}
		return directPathDeletion(record.key)
	}
	return nil
}

func sortedImportIDs[T any](records map[string]T) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func cloneImportWorkspace(record localstore.WorkspaceRecord) (localstore.WorkspaceRecord, error) {
	cloned := record
	var err error
	cloned.Snapshot, err = cloneImportSnapshot(record.Snapshot)
	return cloned, err
}

func cloneImportCandidate(record *localstore.WorkspaceCandidateRecord) (*localstore.WorkspaceCandidateRecord, error) {
	if record == nil {
		return nil, nil
	}
	cloned := *record
	var err error
	cloned.DirectSnapshot, err = cloneImportSnapshot(record.DirectSnapshot)
	if err != nil {
		return nil, err
	}
	if record.RebasedSnapshot != nil {
		rebased, err := cloneImportSnapshot(*record.RebasedSnapshot)
		if err != nil {
			return nil, err
		}
		cloned.RebasedSnapshot = &rebased
	}
	return &cloned, nil
}

func cloneImportSnapshot(snapshot state.Snapshot) (state.Snapshot, error) {
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		return state.Snapshot{}, err
	}
	return state.DecodeTree(cloneCheckpointTree(tree))
}

func cloneImportDisposition(value localstore.WorkspaceMaterializationDisposition) localstore.WorkspaceMaterializationDisposition {
	cloned := localstore.WorkspaceMaterializationDisposition{}
	if value.Journals != nil {
		cloned.Journals = make([]localstore.WorkspaceMaterializationRecord, len(value.Journals))
		for index, journal := range value.Journals {
			cloned.Journals[index] = cloneMaterializationRecord(journal)
		}
	}
	if value.Operations != nil {
		cloned.Operations = make([]localstore.WorkspaceOperation, len(value.Operations))
		for index, operation := range value.Operations {
			cloned.Operations[index] = cloneImportOperation(operation)
		}
	}
	return cloned
}

func cloneImportOperation(value localstore.WorkspaceOperation) localstore.WorkspaceOperation {
	cloned := value
	cloned.OperationJSON = bytes.Clone(value.OperationJSON)
	if value.StashedByStashID != nil {
		owner := *value.StashedByStashID
		cloned.StashedByStashID = &owner
	}
	return cloned
}

func cloneImportOccurrences(value []localstore.WorkspaceConflictOccurrence) []localstore.WorkspaceConflictOccurrence {
	if value == nil {
		return nil
	}
	cloned := make([]localstore.WorkspaceConflictOccurrence, len(value))
	copy(cloned, value)
	return cloned
}

func cloneImportConflicts(value []Conflict) []Conflict {
	cloned := make([]Conflict, len(value))
	for index, conflict := range value {
		cloned[index] = conflict
		cloned[index].Base.Value = bytes.Clone(conflict.Base.Value)
		cloned[index].Ours.Value = bytes.Clone(conflict.Ours.Value)
		cloned[index].Theirs.Value = bytes.Clone(conflict.Theirs.Value)
	}
	return cloned
}

// ValidateDirectDelta verifies that next is a permitted direct successor to
// prior. It is pure: neither input is modified or retained.
func ValidateDirectDelta(prior, next state.Snapshot) error {
	prior, err := validatedDiffSnapshot(prior)
	if err != nil {
		return fmt.Errorf("projectstate: direct delta prior: %w", err)
	}
	if key, missing := directRawDeletion(prior, next); missing {
		if key.Kind == "event" || key.Kind == "git_link" {
			return fmt.Errorf("%w: %s %s", ErrDirectImmutableRecordMutation, key.Kind, key.ID)
		}
		return directPathDeletion(key)
	}
	if err := directRawTombstoneDigestPreflight(prior, next); err != nil {
		return err
	}
	next, err = validatedDiffSnapshot(next)
	if err != nil {
		return fmt.Errorf("projectstate: direct delta next: %w", err)
	}
	if err := directBindingEqual(prior, next); err != nil {
		return err
	}
	if err := directImmutableRecordsEqual(prior, next); err != nil {
		return err
	}
	return directMutableRecordsAllowed(prior, next)
}

// directRawTombstoneDigestPreflight preserves the tombstone-digest contract
// for the one digest field that snapshot validation otherwise rejects before
// direct lifecycle validation can classify it.
func directRawTombstoneDigestPreflight(prior, next state.Snapshot) error {
	ids := make([]string, 0, len(prior.Articles))
	for id := range prior.Articles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		priorRecord := prior.Articles[id]
		nextRecord, ok := next.Articles[id]
		if !ok || priorRecord.Value == nil || nextRecord.Value != nil || nextRecord.Tombstone == nil ||
			nextRecord.Tombstone.ID != id || nextRecord.Tombstone.EntityKind != "kb_article" {
			continue
		}
		if nextRecord.Tombstone.DeletedBodyDigest == nil && directKBBodyDigestIsSoleDefect(priorRecord, next, id) {
			return fmt.Errorf("%w: kb_article %s body digest", state.ErrTombstoneDigest, id)
		}
	}
	return nil
}

func directKBBodyDigestIsSoleDefect(prior state.KBRecord, snapshot state.Snapshot, id string) bool {
	digest, err := state.DigestCanonicalMarkdown(prior.Body)
	if err != nil {
		return false
	}
	probe := snapshot
	probe.Articles = make(map[string]state.KBRecord, len(snapshot.Articles))
	for articleID, record := range snapshot.Articles {
		probe.Articles[articleID] = record
	}
	record := probe.Articles[id]
	tombstone := *record.Tombstone
	tombstone.DeletedBodyDigest = &digest
	record.Tombstone = &tombstone
	probe.Articles[id] = record
	_, err = validatedDiffSnapshot(probe)
	return err == nil
}

func directRawDeletion(prior, next state.Snapshot) (state.RecordKey, bool) {
	for _, records := range []struct {
		kind    string
		missing func() (string, bool)
	}{
		{kind: "actor", missing: func() (string, bool) { return firstMissingRecordKey(prior.Actors, next.Actors) }},
		{kind: "task", missing: func() (string, bool) { return firstMissingRecordKey(prior.Tasks, next.Tasks) }},
		{kind: "task_link", missing: func() (string, bool) { return firstMissingRecordKey(prior.TaskLinks, next.TaskLinks) }},
		{kind: "kb_article", missing: func() (string, bool) { return firstMissingRecordKey(prior.Articles, next.Articles) }},
		{kind: "channel", missing: func() (string, bool) { return firstMissingRecordKey(prior.Channels, next.Channels) }},
		{kind: "event", missing: func() (string, bool) { return firstMissingRecordKey(prior.Events, next.Events) }},
		{kind: "git_link", missing: func() (string, bool) { return firstMissingRecordKey(prior.GitLinks, next.GitLinks) }},
	} {
		if id, missing := records.missing(); missing {
			return state.RecordKey{Kind: records.kind, ID: id}, true
		}
	}
	return state.RecordKey{}, false
}

func directPathDeletion(key state.RecordKey) error {
	return fmt.Errorf("%w: %s %s", ErrDirectPathDeletion, key.Kind, key.ID)
}

func directBindingEqual(prior, next state.Snapshot) error {
	if prior.Config.SnapshotVersion != next.Config.SnapshotVersion ||
		prior.Config.ProjectID != next.Config.ProjectID ||
		prior.Config.Repository != next.Config.Repository {
		return fmt.Errorf("projectstate: direct delta binding mismatch")
	}
	return nil
}

func directImmutableRecordsEqual(prior, next state.Snapshot) error {
	for _, records := range []struct {
		kind  string
		prior map[string]any
		next  map[string]any
	}{
		{kind: "event", prior: directEventValues(prior.Events), next: directEventValues(next.Events)},
		{kind: "git_link", prior: directGitLinkValues(prior.GitLinks), next: directGitLinkValues(next.GitLinks)},
	} {
		for _, id := range directSharedIDs(records.prior, records.next) {
			equal, err := directCanonicalEqual(records.prior[id], records.next[id])
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("%w: %s %s", ErrDirectImmutableRecordMutation, records.kind, id)
			}
		}
	}
	return nil
}

func directEventValues(records map[string]state.EventV1) map[string]any {
	values := make(map[string]any, len(records))
	for id, value := range records {
		values[id] = value
	}
	return values
}

func directGitLinkValues(records map[string]state.Record[state.GitLinkV1]) map[string]any {
	values := make(map[string]any, len(records))
	for id, record := range records {
		if record.Value != nil {
			values[id] = *record.Value
		}
	}
	return values
}

func directSharedIDs(left, right map[string]any) []string {
	ids := make([]string, 0)
	for id := range left {
		if _, ok := right[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func directCanonicalEqual(left, right any) (bool, error) {
	leftJSON, err := state.CanonicalJSON(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := state.CanonicalJSON(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func directMutableRecordsAllowed(prior, next state.Snapshot) error {
	pairs := []directMutablePair{{
		key:   state.RecordKey{Kind: "project", ID: prior.Project.ID},
		prior: directRecord{live: prior.Project, createdAt: prior.Project.CreatedAt},
		next:  directRecord{live: next.Project, createdAt: next.Project.CreatedAt},
	}}
	for _, records := range []directMutableRecordSet{
		directActorRecordSet(prior.Actors, next.Actors),
		directTaskRecordSet(prior.Tasks, next.Tasks),
		directTaskLinkRecordSet(prior.TaskLinks, next.TaskLinks),
		directArticleRecordSet(prior.Articles, next.Articles),
		directChannelRecordSet(prior.Channels, next.Channels),
	} {
		for _, id := range records.sharedIDs() {
			pairs = append(pairs, directMutablePair{
				key: state.RecordKey{Kind: records.kind, ID: id}, prior: records.prior(id), next: records.next(id),
			})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		leftRank, rightRank := diffKindRank(pairs[i].key.Kind), diffKindRank(pairs[j].key.Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if pairs[i].key.ID != pairs[j].key.ID {
			return pairs[i].key.ID < pairs[j].key.ID
		}
		return pairs[i].key.Kind < pairs[j].key.Kind
	})
	for _, pair := range pairs {
		if pair.prior.tombstone == nil && pair.next.tombstone != nil {
			if err := directValidateTombstone(pair.key.Kind, pair.key.ID, pair.prior, *pair.next.tombstone); err != nil {
				return err
			}
		}
	}
	for _, pair := range pairs {
		if pair.prior.tombstone != nil && pair.next.tombstone != nil {
			equal, err := directCanonicalEqual(*pair.prior.tombstone, *pair.next.tombstone)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("%w: %s %s", ErrDirectEditTombstone, pair.key.Kind, pair.key.ID)
			}
		}
	}
	for _, pair := range pairs {
		if pair.prior.tombstone != nil && pair.next.tombstone == nil {
			return fmt.Errorf("%w: %s %s", ErrDirectResurrection, pair.key.Kind, pair.key.ID)
		}
	}
	for _, pair := range pairs {
		if pair.prior.tombstone != nil || pair.next.tombstone != nil || pair.prior.createdAt == nil || pair.next.createdAt == nil {
			continue
		}
		equal, err := directCanonicalEqual(pair.prior.createdAt, pair.next.createdAt)
		if err != nil {
			return err
		}
		if !equal {
			return fmt.Errorf("%w: %s %s changed created_at", ErrDirectImmutableFieldMutation, pair.key.Kind, pair.key.ID)
		}
	}
	return nil
}

type directMutablePair struct {
	key         state.RecordKey
	prior, next directRecord
}

type directMutableRecordSet struct {
	kind     string
	priorIDs map[string]struct{}
	nextIDs  map[string]struct{}
	prior    func(string) directRecord
	next     func(string) directRecord
}

func (records directMutableRecordSet) sharedIDs() []string {
	ids := make([]string, 0)
	for id := range records.priorIDs {
		if _, ok := records.nextIDs[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

type directRecord struct {
	live      any
	tombstone *state.TombstoneV1
	body      []byte
	createdAt any
}

func directValidateTombstone(kind, id string, prior directRecord, tombstone state.TombstoneV1) error {
	contentDigest, err := state.DigestCanonicalJSON(prior.live)
	if err != nil {
		return err
	}
	if tombstone.DeletedContentDigest != contentDigest {
		return fmt.Errorf("%w: %s %s content digest", state.ErrTombstoneDigest, kind, id)
	}
	if kind != "kb_article" {
		return nil
	}
	bodyDigest, err := state.DigestCanonicalMarkdown(prior.body)
	if err != nil {
		return err
	}
	if tombstone.DeletedBodyDigest == nil || *tombstone.DeletedBodyDigest != bodyDigest {
		return fmt.Errorf("%w: %s %s body digest", state.ErrTombstoneDigest, kind, id)
	}
	return nil
}

func directActorRecordSet(prior, next map[string]state.Record[state.ActorV1]) directMutableRecordSet {
	return directTypedRecordSet("actor", prior, next, func(value state.ActorV1) any { return value }, func(state.ActorV1) any { return nil })
}

func directTaskRecordSet(prior, next map[string]state.Record[state.TaskV1]) directMutableRecordSet {
	return directTypedRecordSet("task", prior, next, func(value state.TaskV1) any { return value }, func(value state.TaskV1) any { return value.CreatedAt })
}

func directTaskLinkRecordSet(prior, next map[string]state.Record[state.TaskLinkV1]) directMutableRecordSet {
	return directTypedRecordSet("task_link", prior, next, func(value state.TaskLinkV1) any { return value }, func(state.TaskLinkV1) any { return nil })
}

func directChannelRecordSet(prior, next map[string]state.Record[state.ChannelV1]) directMutableRecordSet {
	return directTypedRecordSet("channel", prior, next, func(value state.ChannelV1) any { return value }, func(value state.ChannelV1) any { return value.CreatedAt })
}

func directTypedRecordSet[T any](kind string, prior, next map[string]state.Record[T], live func(T) any, createdAt func(T) any) directMutableRecordSet {
	return directMutableRecordSet{
		kind: kind, priorIDs: directRecordIDs(prior), nextIDs: directRecordIDs(next),
		prior: func(id string) directRecord { return directTypedRecord(prior[id], live, createdAt) },
		next:  func(id string) directRecord { return directTypedRecord(next[id], live, createdAt) },
	}
}

func directRecordIDs[T any](records map[string]T) map[string]struct{} {
	ids := make(map[string]struct{}, len(records))
	for id := range records {
		ids[id] = struct{}{}
	}
	return ids
}

func directTypedRecord[T any](record state.Record[T], live func(T) any, createdAt func(T) any) directRecord {
	if record.Tombstone != nil {
		return directRecord{tombstone: record.Tombstone}
	}
	return directRecord{live: live(*record.Value), createdAt: createdAt(*record.Value)}
}

func directArticleRecordSet(prior, next map[string]state.KBRecord) directMutableRecordSet {
	return directMutableRecordSet{
		kind: "kb_article", priorIDs: directRecordIDs(prior), nextIDs: directRecordIDs(next),
		prior: func(id string) directRecord { return directArticleRecord(prior[id]) },
		next:  func(id string) directRecord { return directArticleRecord(next[id]) },
	}
}

func directArticleRecord(record state.KBRecord) directRecord {
	if record.Tombstone != nil {
		return directRecord{tombstone: record.Tombstone}
	}
	return directRecord{live: *record.Value, body: bytes.Clone(record.Body), createdAt: record.Value.CreatedAt}
}
