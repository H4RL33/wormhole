//go:build linux

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSystemdExchangeRecoveryPreservesDisplacedAndDesiredEvidence(t *testing.T) {
	tests := []struct {
		name          string
		faultPoint    string
		secondReplace bool
	}{
		{name: "crash immediately after exchange", faultPoint: "publish_after_exchange"},
		{name: "crash during displaced validation", faultPoint: "publish_during_validation"},
		{name: "second replacement before restore", faultPoint: "publish_before_restore", secondReplace: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, change := installedUpgradeFixture(t)
			service := fixture.service.(*systemdGatewayService)
			unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
			firstCompetitor := []byte("first-third-party-unit")
			secondCompetitor := []byte("second-third-party-unit")
			swapped := false
			service.hooks.beforeConditionalPublish = func() {
				if swapped {
					return
				}
				swapped = true
				if err := os.WriteFile(unitPath, firstCompetitor, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			service.hooks.fault = func(point string) error {
				if point != test.faultPoint {
					return nil
				}
				if test.secondReplace {
					replacement := filepath.Join(filepath.Dir(unitPath), ".second-competitor")
					if err := os.WriteFile(replacement, secondCompetitor, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := unix.Renameat2(unix.AT_FDCWD, replacement, unix.AT_FDCWD, unitPath, unix.RENAME_EXCHANGE); err != nil {
						t.Fatal(err)
					}
					return nil
				}
				return errors.New("injected publish crash")
			}

			firstErr := fixture.service.Install(t.Context(), change)
			if firstErr == nil {
				t.Fatal("Install succeeded despite injected exchange fault")
			}
			if test.secondReplace {
				assertFileBytes(t, unitPath, secondCompetitor)
			} else {
				assertFileDigest(t, unitPath, change.DesiredUnitDigest)
			}

			restarted := NewGatewayService(fixture.runner)
			reconstructed := reconstructConfirmedChange(change)
			if err := restarted.Install(t.Context(), reconstructed); !errors.Is(err, ErrServiceChangeDrift) {
				t.Fatalf("restart Install error = %v, want conservative drift", err)
			}
			if test.secondReplace {
				assertFileBytes(t, unitPath, secondCompetitor)
			} else {
				assertFileBytes(t, unitPath, firstCompetitor)
			}
			assertDirectoryEvidence(t, filepath.Dir(unitPath), firstCompetitor, change.DesiredUnitDigest)
		})
	}
}

func TestSystemdExchangeRecoveryResumesCrashAfterRescue(t *testing.T) {
	fixture, change := installedUpgradeFixture(t)
	service := fixture.service.(*systemdGatewayService)
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	competitor := []byte("displaced-third-party-unit")
	service.hooks.beforeConditionalPublish = func() {
		if err := os.WriteFile(unitPath, competitor, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service.hooks.fault = func(point string) error {
		if point == "publish_after_rescue" {
			return errors.New("injected crash after rescue")
		}
		return nil
	}
	if err := fixture.service.Install(t.Context(), change); err == nil {
		t.Fatal("Install succeeded despite rescue crash")
	}
	if _, err := os.Lstat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit exists during interrupted rescue: %v", err)
	}
	restarted := NewGatewayService(fixture.runner)
	if err := restarted.Install(t.Context(), reconstructConfirmedChange(change)); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("restart Install error = %v, want drift after restore", err)
	}
	assertFileBytes(t, unitPath, competitor)
	assertDirectoryEvidence(t, filepath.Dir(unitPath), competitor, change.DesiredUnitDigest)
}

func TestSystemdUnitReferencesOwnerManagedDigestExecutable(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(unit, []byte(fixture.executable)) {
		t.Fatalf("unit references mutable source executable: %s", unit)
	}
	managed, ok := parseGatewayUnit(unit, fixture.runtimeRoot)
	if !ok || !strings.HasPrefix(managed, filepath.Join(fixture.configRoot, "wormhole", "service-bin")+string(os.PathSeparator)) {
		t.Fatalf("managed executable path = %q, parsed=%v", managed, ok)
	}
	assertFileBytes(t, managed, []byte("gateway"))
	info, err := os.Stat(managed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("managed executable mode = %#o, want 0500", info.Mode().Perm())
	}
}

func TestSystemdInstallRejectsSourceExecutableChangedAfterConfirmation(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := os.WriteFile(fixture.executable, []byte("changed-after-confirmation"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want executable drift", err)
	}
	if mutating := mutatingSystemdCalls(fixture.runner.calls); len(mutating) != 0 {
		t.Fatalf("manager mutated after executable drift: %v", mutating)
	}
}

func TestSystemdStartRejectsManagedExecutableSwapAfterValidation(t *testing.T) {
	fixture := newSystemdFixture(t)
	service := fixture.service.(*systemdGatewayService)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	fixture.runner.active = false
	unit, err := os.ReadFile(filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit))
	if err != nil {
		t.Fatal(err)
	}
	managed, ok := parseGatewayUnit(unit, fixture.runtimeRoot)
	if !ok {
		t.Fatal("installed unit did not parse")
	}
	swapped := false
	service.hooks.fault = func(point string) error {
		if point != "before_systemd_start" || swapped {
			return nil
		}
		swapped = true
		replacement := managed + ".replacement"
		if err := os.WriteFile(replacement, []byte("unvalidated"), 0o500); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, managed); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	before := fixture.runner.Count("start")
	if err := fixture.service.Start(t.Context()); !errors.Is(err, ErrServiceChangeDrift) && !errors.Is(err, ErrUnsafeServicePath) {
		t.Fatalf("Start error = %v, want executable identity rejection", err)
	}
	if got := fixture.runner.Count("start"); got != before {
		t.Fatalf("start executed swapped path: before=%d after=%d", before, got)
	}
}

func TestSystemdFaultMatrixResumesAfterProcessRestart(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (systemdFixture, ConfirmedServiceChange)
	}{
		{
			name: "after exchange",
			setup: func(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
				fixture := newSystemdFixture(t)
				change := confirmedServiceChange(t, fixture.service, fixture.executable)
				service := fixture.service.(*systemdGatewayService)
				failed := false
				service.hooks.fault = func(point string) error {
					if point == "publish_after_exchange" && !failed {
						failed = true
						return errors.New("injected post-exchange crash")
					}
					return nil
				}
				return fixture, change
			},
		},
		{
			name: "during exchange validation",
			setup: func(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
				fixture := newSystemdFixture(t)
				change := confirmedServiceChange(t, fixture.service, fixture.executable)
				service := fixture.service.(*systemdGatewayService)
				failed := false
				service.hooks.fault = func(point string) error {
					if point == "publish_during_validation" && !failed {
						failed = true
						return errors.New("injected validation crash")
					}
					return nil
				}
				return fixture, change
			},
		},
		{
			name: "unit directory sync",
			setup: func(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
				fixture := newSystemdFixture(t)
				change := confirmedServiceChange(t, fixture.service, fixture.executable)
				service := fixture.service.(*systemdGatewayService)
				failed := false
				service.hooks.fault = func(point string) error {
					if point == "unit_directory_sync" && !failed {
						failed = true
						return errors.New("injected directory sync crash")
					}
					return nil
				}
				return fixture, change
			},
		},
		{
			name: "daemon reload",
			setup: func(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
				fixture := newSystemdFixture(t)
				fixture.runner.failDaemonReload = 1
				return fixture, confirmedServiceChange(t, fixture.service, fixture.executable)
			},
		},
		{
			name: "enable",
			setup: func(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
				fixture := newSystemdFixture(t)
				fixture.runner.failEnable = 1
				return fixture, confirmedServiceChange(t, fixture.service, fixture.executable)
			},
		},
		{
			name: "start",
			setup: func(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
				fixture := newSystemdFixture(t)
				first := confirmedServiceChange(t, fixture.service, fixture.executable)
				if err := fixture.service.Install(t.Context(), first); err != nil {
					t.Fatal(err)
				}
				fixture.runner.active = false
				change := confirmedServiceChange(t, fixture.service, fixture.executable)
				fixture.runner.failStart = 1
				return fixture, change
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, change := test.setup(t)
			if err := fixture.service.Install(t.Context(), change); err == nil {
				t.Fatal("first Install succeeded despite fault")
			}
			restarted := NewGatewayService(fixture.runner)
			if err := restarted.Install(t.Context(), reconstructConfirmedChange(change)); err != nil {
				t.Fatalf("restart Install: %v", err)
			}
			state, err := restarted.Inspect(t.Context())
			if err != nil || !isDesiredServiceState(state, change.DesiredUnitDigest, fixture.runner.unitPath) {
				t.Fatalf("restarted state = %+v, err=%v", state, err)
			}
		})
	}
}

func TestSystemdUnitPublishedJournalAdvancesReloadedStateWithoutReload(t *testing.T) {
	fixture, change := installedUpgradeFixture(t)
	fixture.runner.active = false
	change = confirmedServiceChange(t, fixture.service, change.Executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	fixture.runner.active = false
	writeServiceJournal(t, fixture, installRecord{
		SchemaVersion: 2, Prior: serviceMutationStateFrom(change.ExpectedPrior),
		DesiredDigest: change.DesiredUnitDigest, Phase: installUnitPublished,
		Publish: dummyValidatedPublish(change),
	})
	beforeReload := fixture.runner.Count("daemon-reload")
	if err := fixture.service.Install(t.Context(), reconstructConfirmedChange(change)); err != nil {
		t.Fatal(err)
	}
	if got := fixture.runner.Count("daemon-reload"); got != beforeReload {
		t.Fatalf("ahead state was redundantly reloaded: before=%d after=%d", beforeReload, got)
	}
}

func TestSystemdDesiredNoOpInspectsConflictingJournal(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	writeRawServiceJournal(t, fixture, `{"schema_version":1,"prior_digest":"absent","desired_digest":"sha256:`+strings.Repeat("0", 64)+`","phase":"prepared"}`+"\n")
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want conflicting-journal drift", err)
	}
}

func TestSystemdDesiredNoOpCleansMatchingTerminalJournal(t *testing.T) {
	fixture := newSystemdFixture(t)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	writeServiceJournal(t, fixture, installRecord{
		SchemaVersion: 2,
		Prior:         serviceMutationStateFrom(change.ExpectedPrior),
		DesiredDigest: change.DesiredUnitDigest,
		Phase:         installManagerApplied,
		Publish:       dummyValidatedPublish(change),
	})
	if err := fixture.service.Install(t.Context(), change); err != nil {
		t.Fatalf("terminal recovery Install: %v", err)
	}
	if _, err := os.Lstat(serviceJournalPath(fixture)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal journal remains: %v", err)
	}
}

func TestSystemdUnitPublishedJournalRejectsPriorUnitBeforeReload(t *testing.T) {
	fixture, change := installedUpgradeFixture(t)
	writeServiceJournal(t, fixture, installRecord{
		SchemaVersion: 2,
		Prior:         serviceMutationStateFrom(change.ExpectedPrior),
		DesiredDigest: change.DesiredUnitDigest,
		Phase:         installUnitPublished,
		Publish:       dummyValidatedPublish(change),
	})
	before := fixture.runner.Count("daemon-reload")
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want behind-journal drift", err)
	}
	if got := fixture.runner.Count("daemon-reload"); got != before {
		t.Fatalf("daemon-reload mutated behind state: before=%d after=%d", before, got)
	}
}

func TestSystemdReloadedJournalRejectsPriorUnitBeforeManagerMutation(t *testing.T) {
	fixture, change := installedUpgradeFixture(t)
	fixture.runner.active = false
	change = confirmedServiceChange(t, fixture.service, change.Executable)
	writeServiceJournal(t, fixture, installRecord{
		SchemaVersion: 2,
		Prior:         serviceMutationStateFrom(change.ExpectedPrior),
		DesiredDigest: change.DesiredUnitDigest,
		Phase:         installReloaded,
		Publish:       dummyValidatedPublish(change),
	})
	before := len(mutatingSystemdCalls(fixture.runner.calls))
	if err := fixture.service.Install(t.Context(), change); !errors.Is(err, ErrServiceChangeDrift) {
		t.Fatalf("Install error = %v, want behind-journal drift", err)
	}
	if got := len(mutatingSystemdCalls(fixture.runner.calls)); got != before {
		t.Fatalf("manager mutated behind state: before=%d after=%d", before, got)
	}
}

func TestSystemdJournalRejectsDuplicateKeysAndNonCanonicalDigest(t *testing.T) {
	fixture := newSystemdFixture(t)
	unitDirectory := filepath.Join(fixture.configRoot, "systemd", "user")
	if err := os.MkdirAll(unitDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryFD, err := openSecureDirectory(unitDirectory, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFD(directoryFD)

	valid := installRecord{
		SchemaVersion: 2,
		Prior:         serviceMutationState{UnitDigest: AbsentServiceUnitDigest},
		DesiredDigest: ServiceUnitDigest("sha256:" + strings.Repeat("0", 64)),
		Phase:         installPrepared,
	}
	canonical, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(canonical), "{", `{"schema_version":2,`, 1) + "\n"
	invalidDigest := valid
	invalidDigest.DesiredDigest = ServiceUnitDigest("sha256:" + strings.Repeat("A", 64))
	nonCanonicalDigest, err := json.Marshal(invalidDigest)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{duplicate, string(nonCanonicalDigest) + "\n"}
	for _, data := range invalid {
		if err := os.WriteFile(serviceJournalPath(fixture), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readInstallRecord(directoryFD); !errors.Is(err, ErrServiceChangeDrift) {
			t.Fatalf("readInstallRecord(%q) error = %v, want drift", data, err)
		}
	}
}

func TestSystemdInspectRejectsLoadedManagerOrphanWithoutOwnedUnit(t *testing.T) {
	tests := []struct {
		name            string
		enabled, active bool
		loaded          bool
	}{
		{name: "enabled", enabled: true},
		{name: "active", active: true},
		{name: "loaded", loaded: true},
		{name: "all", enabled: true, active: true, loaded: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSystemdFixture(t)
			fixture.runner.enabled, fixture.runner.active, fixture.runner.loaded = test.enabled, test.active, test.loaded
			fixture.runner.unitPath = filepath.Join(fixture.configRoot, "systemd", "user", gatewayServiceUnit)
			state, err := fixture.service.Inspect(t.Context())
			if !errors.Is(err, ErrServiceStateUnknown) {
				t.Fatalf("Inspect error = %v, want unknown orphan", err)
			}
			if state.Installed || state.Enabled != test.enabled || state.Active != test.active || state.Loaded != test.loaded {
				t.Fatalf("orphan state not represented: %+v", state)
			}
		})
	}
}

func TestSystemdFreshTreeRetriesParentSyncFailure(t *testing.T) {
	fixture := newSystemdFixture(t)
	service := fixture.service.(*systemdGatewayService)
	change := confirmedServiceChange(t, fixture.service, fixture.executable)
	failed := false
	service.hooks.fault = func(point string) error {
		if point == "directory_parent_sync" && !failed {
			failed = true
			return errors.New("injected parent sync failure")
		}
		return nil
	}
	if err := fixture.service.Install(t.Context(), change); err == nil {
		t.Fatal("Install succeeded despite parent sync failure")
	}
	restarted := NewGatewayService(fixture.runner)
	if err := restarted.Install(t.Context(), reconstructConfirmedChange(change)); err != nil {
		t.Fatalf("retry fresh-tree Install: %v", err)
	}
}

func installedUpgradeFixture(t *testing.T) (systemdFixture, ConfirmedServiceChange) {
	t.Helper()
	fixture := newSystemdFixture(t)
	first := confirmedServiceChange(t, fixture.service, fixture.executable)
	if err := fixture.service.Install(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "gatewayd")
	if err := os.WriteFile(second, []byte("gateway-v2"), 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture, confirmedServiceChange(t, fixture.service, second)
}

func writeServiceJournal(t *testing.T, fixture systemdFixture, record installRecord) {
	t.Helper()
	unitDirectory := filepath.Join(fixture.configRoot, "systemd", "user")
	directoryFD, err := openSecureDirectory(unitDirectory, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFD(directoryFD)
	if err := writeInstallRecord(directoryFD, record); err != nil {
		t.Fatal(err)
	}
}

func writeRawServiceJournal(t *testing.T, fixture systemdFixture, data string) {
	t.Helper()
	if err := os.WriteFile(serviceJournalPath(fixture), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func serviceJournalPath(fixture systemdFixture) string {
	return filepath.Join(fixture.configRoot, "systemd", "user", servicePhaseName)
}

func reconstructConfirmedChange(change ConfirmedServiceChange) ConfirmedServiceChange {
	return ConfirmedServiceChange{
		Executable: change.Executable, ExecutableDigest: change.ExecutableDigest, ExpectedPrior: change.ExpectedPrior,
		DesiredUnitDigest: change.DesiredUnitDigest,
	}
}

func dummyValidatedPublish(change ConfirmedServiceChange) *unitPublishRecord {
	expected := serviceFileIdentity{Device: 1, Inode: 1, Digest: change.ExpectedPrior.UnitDigest}
	wasAbsent := change.ExpectedPrior.UnitDigest == AbsentServiceUnitDigest
	if wasAbsent {
		expected = serviceFileIdentity{Digest: AbsentServiceUnitDigest}
	}
	return &unitPublishRecord{
		Stage: publishValidated, Temporary: ".wormhole-gatewayd-unit-test",
		Rescue: ".wormhole-gatewayd-rescue-test", Expected: expected,
		Desired:   serviceFileIdentity{Device: 2, Inode: 2, Digest: change.DesiredUnitDigest},
		WasAbsent: wasAbsent,
	}
}

func assertFileBytes(t *testing.T, path string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, expected) {
		t.Fatalf("%s = %q, want %q", path, data, expected)
	}
}

func assertFileDigest(t *testing.T, path string, expected ServiceUnitDigest) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := serviceUnitDigest(data); got != expected {
		t.Fatalf("%s digest = %s, want %s", path, got, expected)
	}
}

func assertDirectoryEvidence(t *testing.T, directory string, competitor []byte, desired ServiceUnitDigest) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var foundCompetitor, foundDesired bool
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			continue
		}
		foundCompetitor = foundCompetitor || bytes.Equal(data, competitor)
		foundDesired = foundDesired || serviceUnitDigest(data) == desired
	}
	if !foundCompetitor || !foundDesired {
		t.Fatalf("evidence missing: competitor=%v desired=%v", foundCompetitor, foundDesired)
	}
}

func closeFD(fd int) {
	_ = unix.Close(fd)
}
