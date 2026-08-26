package connector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

type memoryBackupStore struct {
	puts      int
	backup    ConnectorBackup
	reference config.BackupReference
}

func (s *memoryBackupStore) Put(_ context.Context, backup ConnectorBackup) (config.BackupReference, error) {
	s.puts++
	s.backup = backup
	if s.reference == "" {
		s.reference = config.BackupReference("connector-backup:v1:" + string(backup.Adapter) + ":11111111-1111-4111-8111-111111111111")
	}
	return s.reference, nil
}

func (s *memoryBackupStore) Get(_ context.Context, reference config.BackupReference) (ConnectorBackup, error) {
	if reference != s.reference {
		return ConnectorBackup{}, ErrConnectorBackupNotFound
	}
	return s.backup, nil
}

type memoryJournal struct {
	record OperationRecord
	active bool
	fault  map[OperationStage]error
}

func (j *memoryJournal) Prepare(_ context.Context, operation PrepareOperation) (OperationRecord, error) {
	j.record = OperationRecord{SchemaVersion: 1, OperationID: "22222222-2222-4222-8222-222222222222", Adapter: operation.Change.Adapter, Name: operation.Change.Name, Action: operation.Change.Action, PlanDigest: operation.Change.PlanDigest, ExpectedPriorDigest: operation.Change.ExpectedPriorDigest, DesiredDigest: operation.Change.DesiredDigest, BackupReference: operation.BackupReference, Stage: StagePrepared}
	j.active = true
	if err := j.fault[StagePrepared]; err != nil {
		return OperationRecord{}, err
	}
	return j.record, nil
}

func (j *memoryJournal) Active(_ context.Context, adapter AdapterName, name string) (OperationRecord, bool, error) {
	if !j.active || j.record.Adapter != adapter || j.record.Name != name {
		return OperationRecord{}, false, nil
	}
	return j.record, true, nil
}

func (j *memoryJournal) Advance(_ context.Context, id string, stage OperationStage) error {
	if !j.active || j.record.OperationID != id {
		return ErrConnectorOperationNotFound
	}
	j.record.Stage = stage
	if stage == StageComplete {
		j.active = false
	}
	return j.fault[stage]
}

type memoryCoordinator struct{ mu sync.Mutex }

func (c *memoryCoordinator) WithOperationLock(ctx context.Context, _ AdapterName, _ string, operation func(context.Context) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return operation(ctx)
}

type stateAdapter struct {
	name          AdapterName
	connectorName string
	current       ConnectorEntry
	applyErr      error
	verifyErr     error
	rollbackErr   error
	thirdOnApply  *ConnectorEntry
}

func (a *stateAdapter) AdapterName() AdapterName { return a.name }
func (a *stateAdapter) Discover(context.Context) (Availability, error) {
	return Availability{Available: true, Version: "test"}, nil
}
func (a *stateAdapter) Inspect(context.Context) (ConnectorEntry, error) {
	return cloneConnectorEntry(a.current), nil
}
func (a *stateAdapter) Plan(_ context.Context, prior, desired ConnectorEntry) (ChangePlan, error) {
	action := OperationInstall
	if desired.State == EntryAbsent {
		action = OperationRemove
	}
	return BuildChangePlan(a.name, a.connectorName, action, prior, desired)
}
func (a *stateAdapter) Apply(_ context.Context, plan ChangePlan) error {
	if a.thirdOnApply != nil {
		a.current = cloneConnectorEntry(*a.thirdOnApply)
	} else {
		a.current = cloneConnectorEntry(plan.Desired)
	}
	return a.applyErr
}
func (a *stateAdapter) Verify(_ context.Context, desired ConnectorEntry) error {
	if a.verifyErr != nil {
		err := a.verifyErr
		a.verifyErr = nil
		return err
	}
	if !EqualConnectorEntry(a.current, desired) {
		return ErrConnectorStateDrift
	}
	return nil
}
func (a *stateAdapter) Rollback(_ context.Context, plan ChangePlan) error {
	if a.rollbackErr != nil {
		return a.rollbackErr
	}
	if EqualConnectorEntry(a.current, plan.Prior) {
		return nil
	}
	if !EqualConnectorEntry(a.current, plan.Desired) {
		return ErrConnectorStateDrift
	}
	a.current = cloneConnectorEntry(plan.Prior)
	return nil
}
func (a *stateAdapter) Remove(_ context.Context, prior ConnectorEntry) error {
	if !EqualConnectorEntry(a.current, prior) {
		return ErrConnectorStateDrift
	}
	a.current = ConnectorEntry{State: EntryAbsent}
	return nil
}

func transactionalFixture(t *testing.T, action OperationAction) (*stateAdapter, ConnectorEntry, ConfirmedConnectorChange, *memoryBackupStore, *memoryJournal, *memoryCoordinator) {
	t.Helper()
	absent := ConnectorEntry{State: EntryAbsent}
	present := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	prior, desired := absent, present
	if action == OperationRemove {
		prior, desired = present, absent
	}
	adapter := &stateAdapter{name: AdapterCodex, connectorName: "wormhole", current: prior}
	plan, err := adapter.Plan(t.Context(), prior, desired)
	if err != nil {
		t.Fatal(err)
	}
	priorDigest, _ := DigestConnectorEntry(prior)
	desiredDigest, _ := DigestConnectorEntry(desired)
	change := ConfirmedConnectorChange{Adapter: AdapterCodex, Name: "wormhole", Action: action, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
	return adapter, desired, change, &memoryBackupStore{}, &memoryJournal{fault: map[OperationStage]error{}}, &memoryCoordinator{}
}

func TestApplyTransactionalCASBackupMutationVerifyAndCompletion(t *testing.T) {
	adapter, desired, change, backups, journal, locks := transactionalFixture(t, OperationInstall)
	result, err := ApplyTransactional(t.Context(), adapter, desired, change, backups, journal, locks)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stage != StageComplete || result.BackupReference == "" || backups.puts != 1 || journal.active {
		t.Fatalf("result=%+v puts=%d active=%v", result, backups.puts, journal.active)
	}
	if !EqualConnectorEntry(adapter.current, desired) {
		t.Fatalf("current=%#v", adapter.current)
	}
}

func TestRemoveTransactionalUsesConfirmedPriorAndCompletes(t *testing.T) {
	adapter, _, change, backups, journal, locks := transactionalFixture(t, OperationRemove)
	result, err := RemoveTransactional(t.Context(), adapter, change, backups, journal, locks)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stage != StageComplete || adapter.current.State != EntryAbsent {
		t.Fatalf("result=%+v current=%#v", result, adapter.current)
	}
}

func TestTransactionalUnsupportedAndCASDriftRejectBeforeBackup(t *testing.T) {
	adapter, desired, change, backups, journal, locks := transactionalFixture(t, OperationInstall)
	third := desired
	third.Command = "/other"
	adapter.current = third
	if _, err := ApplyTransactional(t.Context(), adapter, desired, change, backups, journal, locks); !errors.Is(err, config.ErrConfirmedPlanDrift) {
		t.Fatalf("CAS error=%v", err)
	}
	if backups.puts != 0 {
		t.Fatalf("backups=%d", backups.puts)
	}

	unsupported := &inspectErrorAdapter{Adapter: adapter, err: ErrUnsupportedConnectorEntry}
	if _, err := ApplyTransactional(t.Context(), unsupported, desired, change, backups, journal, locks); !errors.Is(err, ErrUnsupportedConnectorEntry) {
		t.Fatalf("unsupported error=%v", err)
	}
	if backups.puts != 0 {
		t.Fatalf("unsupported created backup")
	}
}

type inspectErrorAdapter struct {
	Adapter
	err error
}

func (a *inspectErrorAdapter) Inspect(context.Context) (ConnectorEntry, error) {
	return ConnectorEntry{}, a.err
}

func TestTransactionalVerifyFailureRollsBackExactPrior(t *testing.T) {
	adapter, desired, change, backups, journal, locks := transactionalFixture(t, OperationInstall)
	adapter.verifyErr = errors.New("verify failed with token top-secret")
	_, err := ApplyTransactional(t.Context(), adapter, desired, change, backups, journal, locks)
	if err == nil {
		t.Fatal("expected failure")
	}
	if stringsContainsAny(err.Error(), "top-secret", "/opt/wormhole") {
		t.Fatalf("unredacted error: %v", err)
	}
	if adapter.current.State != EntryAbsent || journal.active {
		t.Fatalf("current=%#v active=%v", adapter.current, journal.active)
	}
}

func TestTransactionalConcurrentThirdStateIsPreserved(t *testing.T) {
	adapter, desired, change, backups, journal, locks := transactionalFixture(t, OperationInstall)
	third := desired
	third.Command = "/competitor"
	adapter.thirdOnApply = &third
	adapter.applyErr = errors.New("indeterminate")
	_, err := ApplyTransactional(t.Context(), adapter, desired, change, backups, journal, locks)
	if !errors.Is(err, ErrConnectorStateDrift) {
		t.Fatalf("error=%v", err)
	}
	if !EqualConnectorEntry(adapter.current, third) {
		t.Fatalf("third state overwritten: %#v", adapter.current)
	}
	if !journal.active {
		t.Fatal("journal evidence was terminalized")
	}
}

func TestTransactionalExactDesiredRetryCompletesWithoutBackup(t *testing.T) {
	for _, action := range []OperationAction{OperationInstall, OperationRemove} {
		t.Run(string(action), func(t *testing.T) {
			adapter, desired, change, backups, journal, locks := transactionalFixture(t, action)
			adapter.current = cloneConnectorEntry(desired)
			var result TransactionResult
			var err error
			if action == OperationInstall {
				result, err = ApplyTransactional(t.Context(), adapter, desired, change, backups, journal, locks)
			} else {
				result, err = RemoveTransactional(t.Context(), adapter, change, backups, journal, locks)
			}
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if result.Stage != StageComplete || result.BackupReference != "" || backups.puts != 0 {
				t.Fatalf("result=%+v puts=%d", result, backups.puts)
			}
		})
	}
}

func TestRecoveryExactPriorDesiredThirdMatrixAtEveryStage(t *testing.T) {
	stages := []OperationStage{StagePrepared, StageApplied, StageVerified, StageRolledBack}
	for _, stage := range stages {
		for _, observed := range []string{"prior", "desired", "third"} {
			t.Run(string(stage)+"/"+observed, func(t *testing.T) {
				adapter, desired, change, backups, journal, locks := transactionalFixture(t, OperationInstall)
				prior := adapter.current
				plan, _ := adapter.Plan(t.Context(), prior, desired)
				backups.backup = ConnectorBackup{SchemaVersion: 1, Adapter: AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest}
				backups.reference = config.BackupReference("connector-backup:v1:codex:11111111-1111-4111-8111-111111111111")
				journal.record = OperationRecord{SchemaVersion: 1, OperationID: "22222222-2222-4222-8222-222222222222", Adapter: AdapterCodex, Name: "wormhole", Action: OperationInstall, PlanDigest: change.PlanDigest, ExpectedPriorDigest: change.ExpectedPriorDigest, DesiredDigest: change.DesiredDigest, BackupReference: backups.reference, Stage: stage}
				journal.active = true
				switch observed {
				case "desired":
					adapter.current = desired
				case "third":
					adapter.current = desired
					adapter.current.Command = "/competitor"
				}
				err := RecoverTransactions(t.Context(), adapter, "wormhole", backups, journal, locks)
				if observed == "third" || (stage == StageRolledBack && observed == "desired") || (stage == StageVerified && observed == "prior") {
					if !errors.Is(err, ErrConnectorStateDrift) || !journal.active {
						t.Fatalf("error=%v active=%v", err, journal.active)
					}
					return
				}
				if err != nil {
					t.Fatalf("RecoverTransactions: %v", err)
				}
				if journal.active {
					t.Fatal("journal remains active")
				}
				if stage != StageVerified {
					if !EqualConnectorEntry(adapter.current, prior) {
						t.Fatalf("current=%#v want prior", adapter.current)
					}
				} else if !EqualConnectorEntry(adapter.current, desired) {
					t.Fatalf("current=%#v want desired", adapter.current)
				}
			})
		}
	}
}

func TestTransactionalApplyAndRemoveRecoverEveryDurableStage(t *testing.T) {
	for _, action := range []OperationAction{OperationInstall, OperationRemove} {
		for _, faultStage := range []OperationStage{StagePrepared, StageApplied, StageVerified, StageRolledBack, StageComplete} {
			t.Run(string(action)+"/"+string(faultStage), func(t *testing.T) {
				adapter, desired, change, backups, journal, locks := transactionalFixture(t, action)
				prior := cloneConnectorEntry(adapter.current)
				journal.fault[faultStage] = errors.New("simulated crash with secret-token")
				if faultStage == StageRolledBack {
					adapter.verifyErr = errors.New("force rollback")
				}
				if action == OperationInstall {
					_, _ = ApplyTransactional(t.Context(), adapter, desired, change, backups, journal, locks)
				} else {
					_, _ = RemoveTransactional(t.Context(), adapter, change, backups, journal, locks)
				}
				journal.fault = map[OperationStage]error{}
				adapter.verifyErr = nil
				if err := RecoverTransactions(t.Context(), adapter, "wormhole", backups, journal, locks); err != nil {
					t.Fatalf("RecoverTransactions: %v", err)
				}
				if journal.active {
					t.Fatal("active operation remains")
				}
				want := desired
				if faultStage == StagePrepared || faultStage == StageApplied || faultStage == StageRolledBack {
					want = prior
				}
				if !EqualConnectorEntry(adapter.current, want) {
					t.Fatalf("current=%#v want=%#v", adapter.current, want)
				}
			})
		}
	}
}

func stringsContainsAny(value string, forbidden ...string) bool {
	for _, item := range forbidden {
		if strings.Contains(value, item) {
			return true
		}
	}
	return false
}
