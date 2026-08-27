package projectstate

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestReferenceClasses(t *testing.T) {
	if _, err := DecodeTree(readFixtureTree(t, "testdata/v1/live-reference-tombstone/.wormhole")); !errors.Is(err, ErrBrokenReference) {
		t.Fatalf("live reference error = %v, want ErrBrokenReference", err)
	}
	if _, err := DecodeTree(readFixtureTree(t, "testdata/v1/historical-reference-tombstone/.wormhole")); err != nil {
		t.Fatalf("historical reference: %v", err)
	}
}

func TestRejectsInvalidFixtureTrees(t *testing.T) {
	tests := []struct {
		name string
		want error
	}{
		{"duplicate-id", ErrInvalidSnapshot},
		{"path-id-mismatch", ErrInvalidSnapshot},
		{"secret-field", ErrTrackedSecret},
		{"unknown-version", ErrUnknownVersion},
		{"dangling-live-reference", ErrBrokenReference},
		{"kb-tombstone-body", ErrInvalidSnapshot},
		{"event-id-collision", ErrInvalidSnapshot},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeTree(readFixtureTree(t, "testdata/v1/"+test.name+"/.wormhole"))
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeTree error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRejectsStructuralSchemaViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   error
	}{
		{"config version", func(snapshot *Snapshot) { snapshot.Config.SnapshotVersion = 2 }, ErrUnknownVersion},
		{"project aliases", func(snapshot *Snapshot) { snapshot.Project.Aliases = nil }, ErrInvalidSnapshot},
		{"project timestamp order", func(snapshot *Snapshot) { snapshot.Project.UpdatedAt = snapshot.Project.CreatedAt.Add(-time.Second) }, ErrInvalidSnapshot},
		{"actor kind", func(snapshot *Snapshot) { snapshot.Actors[actorID].Value.ActorKind = "service" }, ErrInvalidSnapshot},
		{"actor key algorithm", func(snapshot *Snapshot) { snapshot.Actors[actorID].Value.PublicKeys[0].Algorithm = "rsa" }, ErrInvalidSnapshot},
		{"task status", func(snapshot *Snapshot) { snapshot.Tasks[taskID].Value.Status = "unknown" }, ErrInvalidSnapshot},
		{"task parent ID", func(snapshot *Snapshot) { value := "BAD"; snapshot.Tasks[taskID].Value.ParentTaskID = &value }, ErrInvalidSnapshot},
		{"task due timezone", func(snapshot *Snapshot) {
			value := time.Date(2026, 7, 28, 10, 0, 0, 0, time.FixedZone("plus-one", 3600))
			snapshot.Tasks[taskID].Value.DueBy = &value
		}, ErrInvalidSnapshot},
		{"task link type", func(snapshot *Snapshot) {
			snapshot.TaskLinks["33333333-3333-4333-8333-333333333333"].Value.LinkType = "url"
		}, ErrInvalidSnapshot},
		{"article frontmatter secret", func(snapshot *Snapshot) {
			snapshot.Articles[articleID].Value.Frontmatter["token"] = json.RawMessage(`"secret"`)
		}, ErrTrackedSecret},
		{"article related duplicate", func(snapshot *Snapshot) {
			snapshot.Articles[articleID].Value.RelatedArticleIDs = []string{articleID, articleID}
		}, ErrInvalidSnapshot},
		{"channel name", func(snapshot *Snapshot) { snapshot.Channels["55555555-5555-4555-8555-555555555555"].Value.Name = "" }, ErrInvalidSnapshot},
		{"event payload", func(snapshot *Snapshot) {
			event := snapshot.Events[eventID]
			event.Payload = json.RawMessage(`{`)
			snapshot.Events[eventID] = event
		}, ErrInvalidSnapshot},
		{"event payload secret", func(snapshot *Snapshot) {
			event := snapshot.Events[eventID]
			event.Payload = json.RawMessage(`{"password":"secret"}`)
			snapshot.Events[eventID] = event
		}, ErrTrackedSecret},
		{"git commit", func(snapshot *Snapshot) {
			value := "ABC"
			snapshot.GitLinks["77777777-7777-4777-8777-777777777777"].Value.CommitSHA = &value
		}, ErrInvalidSnapshot},
		{"GitLink tombstone", func(snapshot *Snapshot) {
			snapshot.GitLinks[gitLinkID] = Record[GitLinkV1]{Tombstone: &TombstoneV1{
				SchemaVersion: 1, Kind: "tombstone", ID: gitLinkID, EntityKind: "git_link",
				DeletedContentDigest: "sha256:46922078bd9d327fb4179236b47a8c77f05ddca8bd701b09b8e446a07c9590a3",
				DeletedBy:            operationActor(), DeletedAt: operationActor().OccurredAt, Extensions: ExtensionsV1{},
			}}
		}, ErrInvalidSnapshot},
		{"extension key", func(snapshot *Snapshot) {
			snapshot.Project.Extensions["invalid"] = ExtensionV1{SchemaVersion: 1, Data: json.RawMessage(`{}`)}
		}, ErrInvalidSnapshot},
		{"extension data object", func(snapshot *Snapshot) {
			snapshot.Project.Extensions["com.acme.test"] = ExtensionV1{SchemaVersion: 1, Data: json.RawMessage(`[]`)}
		}, ErrInvalidSnapshot},
		{"nil map", func(snapshot *Snapshot) { snapshot.Events = nil }, ErrInvalidSnapshot},
		{"remotes duplicate alias", func(snapshot *Snapshot) {
			snapshot.Remotes.Fabrics = append(snapshot.Remotes.Fabrics, snapshot.Remotes.Fabrics[0])
		}, ErrInvalidSnapshot},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := operationSnapshot(t)
			test.mutate(&snapshot)
			if err := Validate(snapshot); !errors.Is(err, test.want) {
				t.Fatalf("Validate error = %v, want %v", err, test.want)
			}
		})
	}
}
