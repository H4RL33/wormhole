package connector

import (
	"context"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

func ApplyTransactional(ctx context.Context, adapter Adapter, desired ConnectorEntry, change ConfirmedConnectorChange, backups BackupStore, journal OperationJournal, coordinator OperationCoordinator) (TransactionResult, error) {
	if change.Action != OperationInstall || desired.State != EntryPresent {
		return TransactionResult{}, config.ErrConfirmedPlanDrift
	}
	return executeTransactional(ctx, adapter, desired, change, backups, journal, coordinator)
}

func RemoveTransactional(ctx context.Context, adapter Adapter, change ConfirmedConnectorChange, backups BackupStore, journal OperationJournal, coordinator OperationCoordinator) (TransactionResult, error) {
	if change.Action != OperationRemove {
		return TransactionResult{}, config.ErrConfirmedPlanDrift
	}
	return executeTransactional(ctx, adapter, ConnectorEntry{State: EntryAbsent}, change, backups, journal, coordinator)
}

func executeTransactional(ctx context.Context, adapter Adapter, desired ConnectorEntry, change ConfirmedConnectorChange, backups BackupStore, journal OperationJournal, coordinator OperationCoordinator) (TransactionResult, error) {
	if adapter == nil || backups == nil || journal == nil || coordinator == nil || ValidateConfirmedConnectorChange(change) != nil || adapter.AdapterName() != change.Adapter {
		return TransactionResult{}, config.ErrConfirmedPlanDrift
	}
	if desiredDigest, err := DigestConnectorEntry(desired); err != nil || desiredDigest != change.DesiredDigest {
		return TransactionResult{}, config.ErrConfirmedPlanDrift
	}
	var result TransactionResult
	err := coordinator.WithOperationLock(ctx, change.Adapter, change.Name, func(locked context.Context) error {
		if err := requireExactAdapterDiscovery(locked, adapter); err != nil {
			return err
		}
		if err := recoverTransactionsLocked(locked, adapter, change.Name, backups, journal); err != nil {
			return err
		}
		prior, err := adapter.Inspect(locked)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		priorDigest, err := DigestConnectorEntry(prior)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		if priorDigest == change.DesiredDigest {
			if err := adapter.Verify(locked, desired); err != nil {
				return sanitizeConnectorError(err)
			}
			result = TransactionResult{Stage: StageComplete}
			return nil
		}
		if priorDigest != change.ExpectedPriorDigest {
			return config.ErrConfirmedPlanDrift
		}
		plan, err := adapter.Plan(locked, prior, desired)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		if plan.Digest != change.PlanDigest || plan.Action != change.Action || plan.Adapter != change.Adapter || plan.Name != change.Name {
			return config.ErrConfirmedPlanDrift
		}
		backup := ConnectorBackup{SchemaVersion: connectorSchemaVersion, Adapter: change.Adapter, Name: change.Name, Prior: cloneConnectorEntry(prior), Desired: cloneConnectorEntry(desired), PlanDigest: plan.Digest}
		reference, err := backups.Put(locked, backup)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		record, err := journal.Prepare(locked, PrepareOperation{Change: change, BackupReference: reference})
		if err != nil {
			return sanitizeConnectorError(err)
		}
		result = TransactionResult{OperationID: record.OperationID, Stage: record.Stage, BackupReference: reference}

		if change.Action == OperationInstall {
			err = adapter.Apply(locked, plan)
		} else {
			err = adapter.Remove(locked, prior)
		}
		if err != nil {
			return rollbackFailedTransaction(locked, adapter, plan, record, journal, err)
		}
		if err := journal.Advance(locked, record.OperationID, StageApplied); err != nil {
			return sanitizeConnectorError(err)
		}
		result.Stage = StageApplied
		if err := adapter.Verify(locked, desired); err != nil {
			return rollbackFailedTransaction(locked, adapter, plan, record, journal, err)
		}
		if err := journal.Advance(locked, record.OperationID, StageVerified); err != nil {
			return sanitizeConnectorError(err)
		}
		result.Stage = StageVerified
		if err := journal.Advance(locked, record.OperationID, StageComplete); err != nil {
			return sanitizeConnectorError(err)
		}
		result.Stage = StageComplete
		return nil
	})
	if err != nil {
		return TransactionResult{}, err
	}
	return result, nil
}

func rollbackFailedTransaction(ctx context.Context, adapter Adapter, plan ChangePlan, record OperationRecord, journal OperationJournal, cause error) error {
	if err := adapter.Rollback(ctx, plan); err != nil {
		return sanitizeConnectorError(err)
	}
	if err := adapter.Verify(ctx, plan.Prior); err != nil {
		return sanitizeConnectorError(err)
	}
	if err := journal.Advance(ctx, record.OperationID, StageRolledBack); err != nil {
		return sanitizeConnectorError(err)
	}
	if err := journal.Advance(ctx, record.OperationID, StageComplete); err != nil {
		return sanitizeConnectorError(err)
	}
	return sanitizeConnectorError(cause)
}

func RecoverTransactions(ctx context.Context, adapter Adapter, name string, backups BackupStore, journal OperationJournal, coordinator OperationCoordinator) error {
	if adapter == nil || backups == nil || journal == nil || coordinator == nil || !validConnectorName(name) {
		return ErrInvalidConnectorPlan
	}
	return coordinator.WithOperationLock(ctx, adapter.AdapterName(), name, func(locked context.Context) error {
		if err := requireExactAdapterDiscovery(locked, adapter); err != nil {
			return err
		}
		return recoverTransactionsLocked(locked, adapter, name, backups, journal)
	})
}

// RollbackCompletedTransactional compensates an exact completed install using
// only the raw prior retained by the connector subsystem's durable journal.
func RollbackCompletedTransactional(ctx context.Context, adapter Adapter, change ConfirmedConnectorChange, backups BackupStore, completed CompletedOperationJournal, coordinator OperationCoordinator) error {
	if adapter == nil || backups == nil || completed == nil || coordinator == nil || ValidateConfirmedConnectorChange(change) != nil ||
		change.Action != OperationInstall || adapter.AdapterName() != change.Adapter {
		return config.ErrConfirmedPlanDrift
	}
	return coordinator.WithOperationLock(ctx, change.Adapter, change.Name, func(locked context.Context) error {
		if err := requireExactAdapterDiscovery(locked, adapter); err != nil {
			return err
		}
		record, exists, err := completed.Completed(locked, change)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		if !exists {
			return config.ErrConfirmedPlanDrift
		}
		backup, err := backups.Get(locked, record.BackupReference)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		plan, err := adapter.Plan(locked, backup.Prior, backup.Desired)
		if err != nil || backup.SchemaVersion != connectorSchemaVersion || backup.Adapter != change.Adapter || backup.Name != change.Name ||
			backup.PlanDigest != change.PlanDigest || plan.Digest != change.PlanDigest || plan.Action != change.Action {
			return ErrInvalidConnectorStore
		}
		priorDigest, priorErr := DigestConnectorEntry(backup.Prior)
		desiredDigest, desiredErr := DigestConnectorEntry(backup.Desired)
		if priorErr != nil || desiredErr != nil || priorDigest != change.ExpectedPriorDigest || desiredDigest != change.DesiredDigest {
			return ErrInvalidConnectorStore
		}
		current, err := adapter.Inspect(locked)
		if err != nil {
			return sanitizeConnectorError(err)
		}
		if EqualConnectorEntry(current, backup.Prior) {
			return sanitizeConnectorError(adapter.Verify(locked, backup.Prior))
		}
		if !EqualConnectorEntry(current, backup.Desired) {
			return config.ErrConfirmedPlanDrift
		}
		if err := adapter.Rollback(locked, plan); err != nil {
			return sanitizeConnectorError(err)
		}
		return sanitizeConnectorError(adapter.Verify(locked, backup.Prior))
	})
}

func requireExactAdapterDiscovery(ctx context.Context, adapter Adapter) error {
	availability, err := adapter.Discover(ctx)
	if err != nil {
		return sanitizeConnectorError(err)
	}
	want := ""
	switch adapter.AdapterName() {
	case AdapterCodex:
		want = "0.149.0"
	case AdapterClaude:
		want = "2.1.220"
	default:
		return ErrConnectorUnavailable
	}
	if !availability.Available || availability.Version != want {
		return ErrConnectorUnavailable
	}
	return nil
}

func recoverTransactionsLocked(ctx context.Context, adapter Adapter, name string, backups BackupStore, journal OperationJournal) error {
	record, active, err := journal.Active(ctx, adapter.AdapterName(), name)
	if err != nil || !active {
		return sanitizeConnectorError(err)
	}
	backup, err := backups.Get(ctx, record.BackupReference)
	if err != nil {
		return sanitizeConnectorError(err)
	}
	plan, err := adapter.Plan(ctx, backup.Prior, backup.Desired)
	if err != nil || backup.SchemaVersion != connectorSchemaVersion || backup.Adapter != record.Adapter || backup.Name != record.Name || backup.PlanDigest != record.PlanDigest || plan.Digest != record.PlanDigest || plan.Adapter != record.Adapter || plan.Name != record.Name || plan.Action != record.Action {
		return ErrInvalidConnectorStore
	}
	priorDigest, priorErr := DigestConnectorEntry(backup.Prior)
	desiredDigest, desiredErr := DigestConnectorEntry(backup.Desired)
	if priorErr != nil || desiredErr != nil || priorDigest != record.ExpectedPriorDigest || desiredDigest != record.DesiredDigest {
		return ErrInvalidConnectorStore
	}
	current, err := adapter.Inspect(ctx)
	if err != nil {
		return sanitizeConnectorError(err)
	}
	isPrior := EqualConnectorEntry(current, backup.Prior)
	isDesired := EqualConnectorEntry(current, backup.Desired)
	if !isPrior && !isDesired {
		return ErrConnectorStateDrift
	}
	switch record.Stage {
	case StagePrepared:
		if isPrior {
			return sanitizeConnectorError(journal.Advance(ctx, record.OperationID, StageComplete))
		}
		return recoverRollbackDesired(ctx, adapter, plan, record, journal)
	case StageApplied:
		if isDesired {
			return recoverRollbackDesired(ctx, adapter, plan, record, journal)
		}
		if err := adapter.Verify(ctx, backup.Prior); err != nil {
			return sanitizeConnectorError(err)
		}
		if err := journal.Advance(ctx, record.OperationID, StageRolledBack); err != nil {
			return sanitizeConnectorError(err)
		}
		return sanitizeConnectorError(journal.Advance(ctx, record.OperationID, StageComplete))
	case StageVerified:
		if !isDesired {
			return ErrConnectorStateDrift
		}
		if err := adapter.Verify(ctx, backup.Desired); err != nil {
			return sanitizeConnectorError(err)
		}
		return sanitizeConnectorError(journal.Advance(ctx, record.OperationID, StageComplete))
	case StageRolledBack:
		if !isPrior {
			return ErrConnectorStateDrift
		}
		return sanitizeConnectorError(journal.Advance(ctx, record.OperationID, StageComplete))
	case StageComplete:
		return ErrInvalidConnectorStore
	default:
		return ErrInvalidConnectorStore
	}
}

func recoverRollbackDesired(ctx context.Context, adapter Adapter, plan ChangePlan, record OperationRecord, journal OperationJournal) error {
	if err := adapter.Rollback(ctx, plan); err != nil {
		return sanitizeConnectorError(err)
	}
	if err := adapter.Verify(ctx, plan.Prior); err != nil {
		return sanitizeConnectorError(err)
	}
	if err := journal.Advance(ctx, record.OperationID, StageRolledBack); err != nil {
		return sanitizeConnectorError(err)
	}
	return sanitizeConnectorError(journal.Advance(ctx, record.OperationID, StageComplete))
}

func sanitizeConnectorError(err error) error {
	if err == nil {
		return nil
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, config.ErrConfirmedPlanDrift, ErrInvalidConnectorEntry, ErrUnsupportedConnectorEntry, ErrConnectorUnavailable, ErrConnectorStateDrift, ErrInvalidConnectorPlan, ErrUnsafeConnectorStore, ErrInvalidConnectorStore, ErrConnectorFilesystemUnsupported, ErrConnectorBackupNotFound, ErrConnectorOperationNotFound, ErrAmbiguousConnectorOperation, ErrConnectorCommandFailed} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return fmt.Errorf("%w", ErrConnectorCommandFailed)
}
