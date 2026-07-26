package localapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIntegrationPreview_DeterministicDiffAndNoWrites(t *testing.T) {
	root := t.TempDir()
	agents := []byte("# Repository instructions\n\nKeep this byte-for-byte.\n")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), agents, 0o640); err != nil {
		t.Fatal(err)
	}
	before := snapshotMaterializationTree(t, root)

	manifest := IntegrationManifest{
		SchemaVersion:   1,
		ManifestID:      "52b860cd-0db7-4ee0-a3fd-672ad9da0c95",
		ManifestVersion: 1,
		ProjectID:       "e724dd25-5bc9-40db-bcad-0b21716d1ca4",
		ManifestDigest:  "sha256:719a185b670128590f3522d2b34e2c213edf0305a114de3ca53661141826e054",
		Entries: []IntegrationManifestEntry{
			{
				Kind:          "agents_bootstrap",
				Target:        "AGENTS.md",
				Content:       "Use approved Wormhole guidance.\n",
				ContentDigest: materializationDigest([]byte("Use approved Wormhole guidance.\n")),
				MergePolicy:   "managed_section",
				Required:      true,
				RoleFilters:   []string{},
			},
			{
				Kind:          "skill",
				Target:        ".agents/skills/wormhole-orientation/SKILL.md",
				Content:       "# Orientation\n",
				ContentDigest: materializationDigest([]byte("# Orientation\n")),
				MergePolicy:   "managed_file",
				Required:      true,
				RoleFilters:   []string{},
			},
		},
	}
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := materializer.Preview(IntegrationMaterializationRequest{
		Operation:    IntegrationApply,
		Manifest:     &manifest,
		ProjectID:    manifest.ProjectID,
		ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "--- a/.agents/skills/wormhole-orientation/SKILL.md\n" +
		"+++ b/.agents/skills/wormhole-orientation/SKILL.md\n" +
		"@@ -0,0 +1,1 @@\n" +
		"+# Orientation\n" +
		"--- a/AGENTS.md\n" +
		"+++ b/AGENTS.md\n" +
		"@@ -1,3 +1,8 @@\n" +
		"-# Repository instructions\n" +
		"-\n" +
		"-Keep this byte-for-byte.\n" +
		"+# Repository instructions\n" +
		"+\n" +
		"+Keep this byte-for-byte.\n" +
		"+\n" +
		"+<!-- wormhole:managed-begin integration-manifest/v1 -->\n" +
		"+<!-- wormhole:manifest id=52b860cd-0db7-4ee0-a3fd-672ad9da0c95 version=1 digest=sha256:719a185b670128590f3522d2b34e2c213edf0305a114de3ca53661141826e054 -->\n" +
		"+Use approved Wormhole guidance.\n" +
		"+<!-- wormhole:managed-end integration-manifest/v1 -->\n"
	if preview.Diff != want {
		t.Fatalf("preview diff mismatch\ngot:\n%s\nwant:\n%s", preview.Diff, want)
	}
	second, err := materializer.Preview(IntegrationMaterializationRequest{
		Operation:    IntegrationApply,
		Manifest:     &manifest,
		ProjectID:    manifest.ProjectID,
		ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Diff != preview.Diff {
		t.Fatal("preview is not deterministic")
	}
	after := snapshotMaterializationTree(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preview changed repository\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestIntegrationApplyAndUpdate_PreserveUserOwnedBytesAndModes(t *testing.T) {
	root := t.TempDir()
	before := []byte("user bytes without a final newline")
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, before, 0o640); err != nil {
		t.Fatal(err)
	}
	manifest := materializationTestManifest("first managed body\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID,
		ResolvedRole: "contributor", VerifiedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesHavePrefix(applied, append(before, '\n', '\n')) || !strings.Contains(string(applied), "first managed body\n") {
		t.Fatalf("AGENTS.md lost user prefix or managed body: %q", applied)
	}
	info, err := os.Stat(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("AGENTS.md mode = %o, want 640", info.Mode().Perm())
	}
	stateInfo, err := os.Stat(filepath.Join(root, integrationStateTarget))
	if err != nil {
		t.Fatal(err)
	}
	if stateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("integration state mode = %o, want 600", stateInfo.Mode().Perm())
	}
	wormholeInfo, err := os.Stat(filepath.Join(root, ".wormhole"))
	if err != nil {
		t.Fatal(err)
	}
	if wormholeInfo.Mode().Perm() != 0o700 {
		t.Fatalf("new .wormhole mode = %o, want 700", wormholeInfo.Mode().Perm())
	}

	outsideEdit := []byte("user bytes changed after approval")
	managedStart := strings.Index(string(applied), managedBeginMarker)
	if managedStart < 0 {
		t.Fatal("managed marker absent")
	}
	if err := os.WriteFile(agentsPath, append(append(outsideEdit, '\n', '\n'), applied[managedStart:]...), 0o640); err != nil {
		t.Fatal(err)
	}
	updated := materializationTestManifest("second managed body\n")
	updated.ManifestVersion = 2
	updated.ManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	updated.Entries[0].Content = "second managed body\n"
	updated.Entries[0].ContentDigest = materializationDigest([]byte(updated.Entries[0].Content))
	state, err = materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationUpdate, Manifest: &updated, State: &state, ProjectID: updated.ProjectID,
		ResolvedRole: "contributor", VerifiedAt: time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	finalAgents, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesHavePrefix(finalAgents, append(outsideEdit, '\n', '\n')) || strings.Contains(string(finalAgents), "first managed body") || !strings.Contains(string(finalAgents), "second managed body") {
		t.Fatalf("update did not preserve outside bytes and replace only block: %q", finalAgents)
	}
	if state.ActiveManifestVersion == nil || *state.ActiveManifestVersion != 2 {
		t.Fatalf("active version = %v, want 2", state.ActiveManifestVersion)
	}
}

func TestIntegrationApply_RejectsUntrackedManagedFileAndUnsafeLinks(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{
			name: "pre-existing file",
			setup: func(t *testing.T, _ string, target string) {
				t.Helper()
				if err := os.WriteFile(target, []byte("user owned\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, root, target string) {
				t.Helper()
				outside := filepath.Join(root, "outside")
				if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hard link",
			setup: func(t *testing.T, root, target string) {
				t.Helper()
				outside := filepath.Join(root, "outside")
				if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, target); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, ".agents", "skills", "wormhole-orientation", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			test.setup(t, root, target)
			manifest := materializationTestManifest("managed\n")
			manifest.Entries = manifest.Entries[1:]
			materializer, err := NewIntegrationMaterializer(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = materializer.Apply(IntegrationMaterializationRequest{
				Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
			})
			if err == nil {
				t.Fatal("unsafe or user-owned target was accepted")
			}
			if _, statErr := os.Stat(filepath.Join(root, integrationStateTarget)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("state created after rejected apply: %v", statErr)
			}
		})
	}
}

func TestIntegrationRemove_DeletesOnlyCleanOwnedBytesAndPreservesDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, ".agents", "skills", "personal", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(unrelated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("personal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := materializationTestManifest("managed\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	managedSkill := filepath.Join(root, filepath.FromSlash(manifest.Entries[1].Target))
	if err := os.WriteFile(managedSkill, []byte("user changed managed file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: manifest.ProjectID,
		ResolvedRole: "contributor", Revoked: true,
	})
	if !errors.Is(err, ErrIntegrationDrift) {
		t.Fatalf("remove error = %v, want ErrIntegrationDrift", err)
	}
	agents, readErr := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if readErr != nil || string(agents) != "user\n" {
		t.Fatalf("AGENTS.md after remove = %q, %v", agents, readErr)
	}
	if skill, readErr := os.ReadFile(managedSkill); readErr != nil || string(skill) != "user changed managed file\n" {
		t.Fatalf("drifted skill was not preserved: %q, %v", skill, readErr)
	}
	if personal, readErr := os.ReadFile(unrelated); readErr != nil || string(personal) != "personal\n" {
		t.Fatalf("unrelated skill changed: %q, %v", personal, readErr)
	}
	if removed.ApprovalState != "revoked" || removed.MaterializationState != "removal_required" ||
		removed.ConnectionState != "attention_required" || !reflect.DeepEqual(removed.PreservedTargets, []string{manifest.Entries[1].Target}) {
		t.Fatalf("revoked drift state = %+v", removed)
	}
}

func TestIntegrationRollback_UsesPriorManifestAndPreservesCurrentOutsideAgentsBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("initial user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := materializationTestManifest("version one\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &first, ProjectID: first.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	second := materializationTestManifest("version two\n")
	second.ManifestVersion = 2
	second.ManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.Entries[0].Content = "version two\n"
	second.Entries[0].ContentDigest = materializationDigest([]byte(second.Entries[0].Content))
	state, err = materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationUpdate, Manifest: &second, State: &state, ProjectID: second.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(root, "AGENTS.md")
	current, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(current), managedBeginMarker)
	if err := os.WriteFile(agentsPath, append([]byte("later user edit\n\n"), current[start:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err = materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRollback, Manifest: &first, State: &state, ProjectID: first.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(rolledBack), "later user edit\n\n") || !strings.Contains(string(rolledBack), "version one\n") || strings.Contains(string(rolledBack), "version two\n") {
		t.Fatalf("rollback output = %q", rolledBack)
	}
	if state.ActiveManifestVersion == nil || *state.ActiveManifestVersion != 1 {
		t.Fatalf("rollback active version = %v", state.ActiveManifestVersion)
	}
}

func TestIntegrationUpdate_RejectsEveryManagedMarkerDriftWithoutWriting(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "duplicate", mutate: func(data []byte) []byte { return append(data, []byte(managedBeginMarker+"\n")...) }},
		{name: "metadata", mutate: func(data []byte) []byte { return bytes.Replace(data, []byte("version=1"), []byte("version=9"), 1) }},
		{name: "body", mutate: func(data []byte) []byte { return bytes.Replace(data, []byte("version one"), []byte("user edited"), 1) }},
		{name: "reordered", mutate: func(data []byte) []byte {
			return bytes.Replace(data, []byte(managedBeginMarker), []byte(managedEndMarker), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			first := materializationTestManifest("version one\n")
			materializer, err := NewIntegrationMaterializer(root)
			if err != nil {
				t.Fatal(err)
			}
			state, err := materializer.Apply(IntegrationMaterializationRequest{
				Operation: IntegrationApply, Manifest: &first, ProjectID: first.ProjectID, ResolvedRole: "contributor",
			})
			if err != nil {
				t.Fatal(err)
			}
			agentsPath := filepath.Join(root, "AGENTS.md")
			current, err := os.ReadFile(agentsPath)
			if err != nil {
				t.Fatal(err)
			}
			drifted := test.mutate(current)
			if err := os.WriteFile(agentsPath, drifted, 0o644); err != nil {
				t.Fatal(err)
			}
			before := snapshotMaterializationTree(t, root)
			second := materializationTestManifest("version two\n")
			second.ManifestVersion = 2
			second.ManifestDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			second.Entries[0].Content = "version two\n"
			second.Entries[0].ContentDigest = materializationDigest([]byte(second.Entries[0].Content))
			_, err = materializer.Apply(IntegrationMaterializationRequest{
				Operation: IntegrationUpdate, Manifest: &second, State: &state, ProjectID: second.ProjectID, ResolvedRole: "contributor",
			})
			if !errors.Is(err, ErrIntegrationDrift) {
				t.Fatalf("marker drift error = %v, want ErrIntegrationDrift", err)
			}
			if after := snapshotMaterializationTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected drift changed repository\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestIntegrationMaterialization_RejectsInvalidTargetMatrix(t *testing.T) {
	for _, target := range []string{
		"../AGENTS.md", "/tmp/SKILL.md", `.agents\skills\wormhole-x\SKILL.md`,
		".agents/skills/personal/SKILL.md", ".agents/skills/wormhole-x/../SKILL.md",
		".agents/skills/wormhole-x/skill.md", ".agents/skills/wormhole-x/references/not_valid.md",
		".agents/skills/wormhole-x/references/a.md/extra",
	} {
		t.Run(target, func(t *testing.T) {
			manifest := materializationTestManifest("managed\n")
			manifest.Entries = []IntegrationManifestEntry{{
				Kind: "skill", Target: target, Content: "managed\n", ContentDigest: materializationDigest([]byte("managed\n")), MergePolicy: "managed_file",
			}}
			materializer, err := NewIntegrationMaterializer(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, err = materializer.Preview(IntegrationMaterializationRequest{
				Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
			})
			if !errors.Is(err, ErrUnsafeIntegrationPath) {
				t.Fatalf("target %q error = %v, want ErrUnsafeIntegrationPath", target, err)
			}
		})
	}
}

func TestIntegrationApply_RollsBackEarlierWritesOnLaterFailure(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := materializationTestManifest("managed\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	materializer.testBeforeMaterializationChange = func(index int, _ IntegrationMaterializationChange) error {
		if index == 1 {
			return errors.New("injected second-write failure")
		}
		return nil
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); err == nil || !strings.Contains(err.Error(), "injected second-write failure") {
		t.Fatalf("Apply error = %v", err)
	}
	agents, err := os.ReadFile(agentsPath)
	if err != nil || string(agents) != "user\n" {
		t.Fatalf("AGENTS.md after rollback = %q, %v", agents, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "wormhole-orientation", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first write survived rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, integrationStateTarget)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state projection exists after rollback: %v", err)
	}
}

func TestIntegrationApply_RollsBackWhenStateProjectionIsUnsafe(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".wormhole"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-state")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, integrationStateTarget)); err != nil {
		t.Fatal(err)
	}
	manifest := materializationTestManifest("managed\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); !errors.Is(err, ErrUnsafeIntegrationPath) {
		t.Fatalf("unsafe state projection error = %v", err)
	}
	agents, err := os.ReadFile(agentsPath)
	if err != nil || string(agents) != "user\n" {
		t.Fatalf("AGENTS.md after projection rollback = %q, %v", agents, err)
	}
	if outsideBytes, err := os.ReadFile(outside); err != nil || string(outsideBytes) != "outside\n" {
		t.Fatalf("outside projection target changed = %q, %v", outsideBytes, err)
	}
}

func TestIntegrationApply_AncestorReplacementFailsClosed(t *testing.T) {
	root := t.TempDir()
	manifest := materializationTestManifest("managed\n")
	manifest.Entries = manifest.Entries[1:]
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	materializer.testBeforeMaterializationChange = func(_ int, _ IntegrationMaterializationChange) error {
		if err := os.Rename(filepath.Join(root, ".agents"), filepath.Join(root, ".agents-moved")); err != nil {
			return err
		}
		return os.Symlink(outside, filepath.Join(root, ".agents"))
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); !errors.Is(err, ErrUnsafeIntegrationPath) {
		t.Fatalf("ancestor replacement error = %v, want ErrUnsafeIntegrationPath", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("ancestor replacement escaped root: entries=%v err=%v", entries, err)
	}
}

func TestIntegrationReferenceEntry_IsManagedWithoutScanningSiblings(t *testing.T) {
	root := t.TempDir()
	manifest := materializationTestManifest("managed\n")
	content := "# Reference\n"
	manifest.Entries = []IntegrationManifestEntry{{
		Kind: "reference", Target: ".agents/skills/wormhole-orientation/references/usage.md",
		Content: content, ContentDigest: materializationDigest([]byte(content)), MergePolicy: "managed_file", Required: true, RoleFilters: []string{},
	}}
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, ".agents", "skills", "wormhole-orientation", "user.md")
	if err := os.WriteFile(sibling, []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sibling); err != nil || string(data) != "user\n" {
		t.Fatalf("unrelated reference sibling changed = %q, %v", data, err)
	}
}

func TestIntegrationRemove_DoesNotDeleteTargetReplacementAfterVerification(t *testing.T) {
	root := t.TempDir()
	manifest := materializationTestManifest("managed\n")
	manifest.Entries = manifest.Entries[1:]
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(manifest.Entries[0].Target))
	replaced := false
	materializer.testBeforeMaterializationUnlink = func() error {
		if replaced {
			return nil
		}
		replaced = true
		if err := os.Rename(target, target+".managed-old"); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("user replacement\n"), 0o644)
	}
	removed, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: manifest.ProjectID, ResolvedRole: "contributor", Revoked: true,
	})
	if !errors.Is(err, ErrIntegrationDrift) {
		t.Fatalf("replacement removal error = %v, want ErrIntegrationDrift", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "user replacement\n" {
		t.Fatalf("replacement was deleted or changed: %q, %v", data, readErr)
	}
	if removed.MaterializationState != "removal_required" {
		t.Fatalf("replacement state = %+v", removed)
	}
}

func TestIntegrationApply_RollsBackMutationWhenPostRenameStepFails(t *testing.T) {
	root := t.TempDir()
	manifest := materializationTestManifest("managed\n")
	manifest.Entries = manifest.Entries[1:]
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	materializer.testAfterMaterializationMutation = func() error {
		if failed {
			return nil
		}
		failed = true
		return errors.New("injected post-rename failure")
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); err == nil || !strings.Contains(err.Error(), "injected post-rename failure") {
		t.Fatalf("post-rename failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(manifest.Entries[0].Target))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-rename failure left target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, integrationStateTarget)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-rename failure wrote state: %v", err)
	}
}

func TestIntegrationRemove_ProjectionFailureRestoresManagedPathBeforeDirectoryCleanup(t *testing.T) {
	root := t.TempDir()
	manifest := materializationTestManifest("managed\n")
	content := "# Reference\n"
	manifest.Entries = []IntegrationManifestEntry{{
		Kind: "reference", Target: ".agents/skills/wormhole-orientation/references/usage.md",
		Content: content, ContentDigest: materializationDigest([]byte(content)), MergePolicy: "managed_file", Required: true, RoleFilters: []string{},
	}}
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, integrationStateTarget)
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-projection")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); !errors.Is(err, ErrUnsafeIntegrationPath) {
		t.Fatalf("projection failure = %v, want ErrUnsafeIntegrationPath", err)
	}
	target := filepath.Join(root, filepath.FromSlash(manifest.Entries[0].Target))
	if data, err := os.ReadFile(target); err != nil || string(data) != content {
		t.Fatalf("projection rollback did not restore managed path: %q, %v", data, err)
	}
}

func TestIntegrationRemove_PreservesPreexistingEmptyAgentsFile(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	manifest := materializationTestManifest("managed\n")
	manifest.Entries = manifest.Entries[:1]
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(agentsPath)
	if err != nil {
		t.Fatalf("empty user AGENTS.md was deleted: %v", err)
	}
	if info.Size() != 0 || info.Mode().Perm() != 0o640 {
		t.Fatalf("empty AGENTS.md size/mode = %d/%o", info.Size(), info.Mode().Perm())
	}
}

func TestIntegrationUpdate_PreservesCreatedOwnershipForLaterRemoval(t *testing.T) {
	root := t.TempDir()
	first := materializationTestManifest("version one\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &first, ProjectID: first.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}

	second := materializationTestManifest("version two\n")
	second.ManifestVersion = 2
	second.ManifestDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	second.Entries[0].Content = "version two\n"
	second.Entries[0].ContentDigest = materializationDigest([]byte(second.Entries[0].Content))
	second.Entries[1].Content = "# Updated orientation\n"
	second.Entries[1].ContentDigest = materializationDigest([]byte(second.Entries[1].Content))
	state, err = materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationUpdate, Manifest: &second, State: &state, ProjectID: second.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}

	tracked := make(map[string]IntegrationTargetState, len(state.Targets))
	for _, target := range state.Targets {
		tracked[target.Target] = target
	}
	if !tracked["AGENTS.md"].CreatedTarget || len(tracked[second.Entries[1].Target].CreatedDirectories) == 0 {
		t.Fatalf("update lost original ownership provenance: %+v", state.Targets)
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: second.ProjectID, ResolvedRole: "contributor",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("originally absent AGENTS.md survived removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("originally created managed directories survived removal: %v", err)
	}
}

func TestIntegrationApply_RollsBackStateProjectionAfterItsRenameFails(t *testing.T) {
	root := t.TempDir()
	manifest := materializationTestManifest("managed\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	materializer.testAfterIntegrationStateMutation = func() error {
		return errors.New("injected post-projection-rename failure")
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &manifest, ProjectID: manifest.ProjectID, ResolvedRole: "contributor",
	}); err == nil || !strings.Contains(err.Error(), "injected post-projection-rename failure") {
		t.Fatalf("projection post-rename failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("projection failure left managed AGENTS.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, integrationStateTarget)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("projection failure left new state projection: %v", err)
	}
}

func TestIntegrationUpdate_RestoresPriorStateProjectionAfterItsRenameFails(t *testing.T) {
	root := t.TempDir()
	first := materializationTestManifest("version one\n")
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationApply, Manifest: &first, ProjectID: first.ProjectID, ResolvedRole: "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(root, integrationStateTarget)
	projectionBefore, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(root, "AGENTS.md")
	agentsBefore, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}

	second := materializationTestManifest("version two\n")
	second.ManifestVersion = 2
	second.ManifestDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	second.Entries[0].Content = "version two\n"
	second.Entries[0].ContentDigest = materializationDigest([]byte(second.Entries[0].Content))
	materializer.testAfterIntegrationStateMutation = func() error {
		return errors.New("injected post-projection-update failure")
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationUpdate, Manifest: &second, State: &state, ProjectID: second.ProjectID, ResolvedRole: "contributor",
	}); err == nil || !strings.Contains(err.Error(), "injected post-projection-update failure") {
		t.Fatalf("projection update failure = %v", err)
	}
	projectionAfter, err := os.ReadFile(projectionPath)
	if err != nil || !bytes.Equal(projectionAfter, projectionBefore) {
		t.Fatalf("prior projection not restored: equal=%t err=%v", bytes.Equal(projectionAfter, projectionBefore), err)
	}
	agentsAfter, err := os.ReadFile(agentsPath)
	if err != nil || !bytes.Equal(agentsAfter, agentsBefore) {
		t.Fatalf("managed target not restored with projection: equal=%t err=%v", bytes.Equal(agentsAfter, agentsBefore), err)
	}
}

func materializationTestManifest(agentsContent string) IntegrationManifest {
	return IntegrationManifest{
		SchemaVersion: 1, ManifestID: "52b860cd-0db7-4ee0-a3fd-672ad9da0c95", ManifestVersion: 1,
		ProjectID:      "e724dd25-5bc9-40db-bcad-0b21716d1ca4",
		ManifestDigest: "sha256:719a185b670128590f3522d2b34e2c213edf0305a114de3ca53661141826e054",
		Entries: []IntegrationManifestEntry{
			{Kind: "agents_bootstrap", Target: "AGENTS.md", Content: agentsContent, ContentDigest: materializationDigest([]byte(agentsContent)), MergePolicy: "managed_section", Required: true, RoleFilters: []string{}},
			{Kind: "skill", Target: ".agents/skills/wormhole-orientation/SKILL.md", Content: "# Orientation\n", ContentDigest: materializationDigest([]byte("# Orientation\n")), MergePolicy: "managed_file", Required: true, RoleFilters: []string{}},
		},
	}
}

func bytesHavePrefix(data, prefix []byte) bool {
	return len(data) >= len(prefix) && reflect.DeepEqual(data[:len(prefix)], prefix)
}

func materializationDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func snapshotMaterializationTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
