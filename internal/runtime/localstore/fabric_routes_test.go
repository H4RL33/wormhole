package localstore

import (
	"context"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestFabricProfileIsSoleCredentialAuthority(t *testing.T) {
	store, workspaceRepo := openWorkspaceStore(t)
	binding := createBinding(t, workspaceRepo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	routes := NewFabricRouteRepo(store.DB())
	profile := types.FabricProfile{
		ProfileID: "10000000-0000-4000-8000-000000000001", Alias: "primary",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		BaseURL:          "https://fabric.example.test", Mode: types.FabricModePrivate,
		CredentialRef: "keyring:primary",
	}
	if err := routes.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	fabricBinding := types.FabricBinding{
		Workspace: binding, ProfileID: profile.ProfileID, FabricInstanceID: profile.FabricInstanceID,
		RemoteProjectID: "30000000-0000-4000-8000-000000000001",
		StreamID:        "40000000-0000-4000-8000-000000000001",
		AttachmentRef:   "50000000-0000-4000-8000-000000000001",
		CanonicalRef:    binding.AcceptedRef, Writable: true,
	}
	if err := routes.AttachWorkspace(context.Background(), fabricBinding); err != nil {
		t.Fatal(err)
	}
	gotBinding, gotProfile, err := routes.GetRoute(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if gotBinding.RemoteKey() != fabricBinding.RemoteKey() || gotProfile != profile {
		t.Fatalf("route=(%+v,%+v), want (%+v,%+v)", gotBinding, gotProfile, fabricBinding, profile)
	}
	if err := routes.UpdateProfile(context.Background(), profile.ProfileID, profile.FabricInstanceID,
		"https://rotated.example.test", "keyring:rotated"); err != nil {
		t.Fatal(err)
	}
	rotated, err := routes.GetProfile(context.Background(), profile.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CredentialRef != "keyring:rotated" || rotated.BaseURL != "https://rotated.example.test" {
		t.Fatalf("rotated profile=%+v", rotated)
	}
}

func TestFabricBindingRejectsWorkspaceMismatchByDirectSQL(t *testing.T) {
	store, workspaceRepo := openWorkspaceStore(t)
	binding := createBinding(t, workspaceRepo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	profile := types.FabricProfile{ProfileID: "10000000-0000-4000-8000-000000000001", Alias: "primary",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001", BaseURL: "https://fabric.example.test", Mode: types.FabricModePrivate}
	if err := NewFabricRouteRepo(store.DB()).CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	_, err := store.DB().Exec(`INSERT INTO workspace_fabric_bindings
		(project_id,workspace_id,profile_id,fabric_instance_id,remote_project_id,stream_id,attachment_ref,
		 repository_provider,repository_immutable_id,canonical_ref,writable,state)
		VALUES (?,?,?,?,?,?,?,'github','repo','refs/heads/main',1,'active')`,
		binding.Scope.ProjectID, "00000000-0000-4000-8000-000000000099", profile.ProfileID,
		profile.FabricInstanceID, "30000000-0000-4000-8000-000000000001",
		"40000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000001")
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		t.Fatalf("mismatched direct binding insert error=%v code=%v, want SQLITE_CONSTRAINT_FOREIGNKEY",
			err, sqliteErrorCode(sqliteErr))
	}
}

func TestCursorRejectsBindingMismatchByDirectSQL(t *testing.T) {
	store, workspaceRepo := openWorkspaceStore(t)
	binding := createBinding(t, workspaceRepo,
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	routes := NewFabricRouteRepo(store.DB())
	profile := types.FabricProfile{ProfileID: "10000000-0000-4000-8000-000000000001", Alias: "primary",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001", BaseURL: "https://fabric.example.test", Mode: types.FabricModePrivate}
	if err := routes.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	fabricBinding := types.FabricBinding{Workspace: binding, ProfileID: profile.ProfileID,
		FabricInstanceID: profile.FabricInstanceID, RemoteProjectID: "30000000-0000-4000-8000-000000000001",
		StreamID: "40000000-0000-4000-8000-000000000001", AttachmentRef: "50000000-0000-4000-8000-000000000001",
		CanonicalRef: binding.AcceptedRef, Writable: true}
	if err := routes.AttachWorkspace(context.Background(), fabricBinding); err != nil {
		t.Fatal(err)
	}
	_, err := store.DB().Exec(`INSERT INTO fabric_cursors
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,stream_version)
		VALUES (?,?,?,?,?,0)`, binding.Scope.ProjectID, binding.Scope.WorkspaceID,
		profile.FabricInstanceID, fabricBinding.RemoteProjectID, "40000000-0000-4000-8000-000000000099")
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		t.Fatalf("mismatched direct cursor insert error=%v code=%v, want SQLITE_CONSTRAINT_FOREIGNKEY",
			err, sqliteErrorCode(sqliteErr))
	}
	if err := routes.UpdateCursor(context.Background(), fabricBinding.RemoteKey(), 12, "cursor-12"); err != nil {
		t.Fatal(err)
	}
	var version int64
	var cursor string
	if err := store.DB().QueryRow(`SELECT stream_version,pull_cursor FROM fabric_cursors
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?`,
		fabricBinding.RemoteKey().ProjectID, fabricBinding.RemoteKey().WorkspaceID,
		fabricBinding.RemoteKey().FabricInstanceID, fabricBinding.RemoteKey().RemoteProjectID,
		fabricBinding.RemoteKey().StreamID).Scan(&version, &cursor); err != nil {
		t.Fatal(err)
	}
	if version != 12 || cursor != "cursor-12" {
		t.Fatalf("cursor=(%d,%q), want (12,cursor-12)", version, cursor)
	}
}

func sqliteErrorCode(err *sqlite.Error) any {
	if err == nil {
		return nil
	}
	return err.Code()
}
