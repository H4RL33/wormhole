package connector

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/types"
)

const (
	connectorStoreLockName   = ".store.lock"
	connectorTemporaryPrefix = ".tmp-"
	maxConnectorRecordBytes  = 64 * 1024
	maxConnectorStoreRecords = 512
	maxConnectorStoreLocks   = 512
)

type Store struct {
	root   string
	clock  func() time.Time
	random func([]byte) (int, error)
	fault  func(string) error
}

func OpenStore() (*Store, error) {
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil, ErrUnsafeConnectorStore
		}
		dataRoot = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(dataRoot) {
		return nil, ErrUnsafeConnectorStore
	}
	return OpenStoreAt(filepath.Join(filepath.Clean(dataRoot), "wormhole", "connectors"))
}

func OpenStoreAt(root string) (*Store, error) {
	canonical, fd, err := openConnectorStoreRoot(root)
	if err != nil {
		return nil, err
	}
	if err := closeConnectorFD(fd); err != nil {
		return nil, err
	}
	return &Store{root: canonical, clock: time.Now, random: rand.Read}, nil
}

func (s *Store) Put(ctx context.Context, backup ConnectorBackup) (config.BackupReference, error) {
	if s == nil || validateConnectorBackup(backup, false) != nil {
		return "", ErrInvalidConnectorStore
	}
	var reference config.BackupReference
	err := s.withStoreLock(ctx, func(fd int, state connectorStoreState) error {
		if len(state.backups)+len(state.operations) >= maxConnectorStoreRecords {
			return ErrInvalidConnectorStore
		}
		identifier, err := s.newUUID(state.identifiers)
		if err != nil {
			return err
		}
		backup.SchemaVersion = connectorSchemaVersion
		backup.CreatedAt = s.clock().UTC()
		if validateConnectorBackup(backup, true) != nil {
			return ErrInvalidConnectorStore
		}
		data, err := marshalCanonicalConnectorJSON(backup)
		if err != nil {
			return err
		}
		if err := atomicConnectorWrite(fd, backupRecordName(identifier), data, nil, false, s.random, s.fault); err != nil {
			return err
		}
		reference = config.BackupReference("connector-backup:v1:" + string(backup.Adapter) + ":" + identifier)
		return nil
	})
	return reference, err
}

func (s *Store) Get(ctx context.Context, reference config.BackupReference) (ConnectorBackup, error) {
	adapter, identifier, ok := parseConnectorBackupReference(reference)
	if !ok {
		return ConnectorBackup{}, ErrConnectorBackupNotFound
	}
	var backup ConnectorBackup
	err := s.withStoreLock(ctx, func(_ int, state connectorStoreState) error {
		value, exists := state.backups[identifier]
		if !exists || value.Adapter != adapter {
			return ErrConnectorBackupNotFound
		}
		backup = cloneConnectorBackup(value)
		return nil
	})
	return backup, err
}

func (s *Store) Prepare(ctx context.Context, operation PrepareOperation) (OperationRecord, error) {
	if s == nil || ValidateConfirmedConnectorChange(operation.Change) != nil {
		return OperationRecord{}, config.ErrConfirmedPlanDrift
	}
	if operation.OwnerDigest != "" {
		if _, err := config.ParseStateDigest(string(operation.OwnerDigest)); err != nil {
			return OperationRecord{}, config.ErrConfirmedPlanDrift
		}
	}
	adapter, backupID, ok := parseConnectorBackupReference(operation.BackupReference)
	if !ok || adapter != operation.Change.Adapter {
		return OperationRecord{}, config.ErrConfirmedPlanDrift
	}
	var record OperationRecord
	err := s.withStoreLock(ctx, func(fd int, state connectorStoreState) error {
		backup, exists := state.backups[backupID]
		if !exists || backup.Adapter != operation.Change.Adapter || backup.Name != operation.Change.Name || backup.PlanDigest != operation.Change.PlanDigest || actionForDesired(backup.Desired) != operation.Change.Action {
			return config.ErrConfirmedPlanDrift
		}
		priorDigest, priorErr := DigestConnectorEntry(backup.Prior)
		desiredDigest, desiredErr := DigestConnectorEntry(backup.Desired)
		if priorErr != nil || desiredErr != nil || priorDigest != operation.Change.ExpectedPriorDigest || desiredDigest != operation.Change.DesiredDigest {
			return config.ErrConfirmedPlanDrift
		}
		for _, candidate := range state.operations {
			if !terminalOperationStage(candidate.Stage) && candidate.Adapter == operation.Change.Adapter && candidate.Name == operation.Change.Name {
				return ErrAmbiguousConnectorOperation
			}
		}
		if len(state.backups)+len(state.operations) >= maxConnectorStoreRecords {
			return ErrInvalidConnectorStore
		}
		identifier, err := s.newUUID(state.identifiers)
		if err != nil {
			return err
		}
		now := s.clock().UTC()
		record = OperationRecord{SchemaVersion: connectorSchemaVersion, OperationID: identifier, Adapter: operation.Change.Adapter, Name: operation.Change.Name, Action: operation.Change.Action, PlanDigest: operation.Change.PlanDigest, ExpectedPriorDigest: operation.Change.ExpectedPriorDigest, DesiredDigest: operation.Change.DesiredDigest, BackupReference: operation.BackupReference, OwnerDigest: operation.OwnerDigest, Stage: StagePrepared, CreatedAt: now, UpdatedAt: now}
		if validateOperationRecord(record) != nil {
			return ErrInvalidConnectorStore
		}
		data, err := marshalCanonicalConnectorJSON(record)
		if err != nil {
			return err
		}
		return atomicConnectorWrite(fd, operationRecordName(identifier), data, nil, false, s.random, s.fault)
	})
	return record, err
}

func (s *Store) Completed(ctx context.Context, change ConfirmedConnectorChange) (OperationRecord, bool, error) {
	if ValidateConfirmedConnectorChange(change) != nil {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	var found OperationRecord
	var exists bool
	err := s.withStoreLock(ctx, func(_ int, state connectorStoreState) error {
		for _, record := range state.operations {
			if record.Stage != StageComplete || record.OwnerDigest != "" || record.Adapter != change.Adapter || record.Name != change.Name || record.Action != change.Action || record.PlanDigest != change.PlanDigest || record.ExpectedPriorDigest != change.ExpectedPriorDigest || record.DesiredDigest != change.DesiredDigest {
				continue
			}
			if exists {
				return ErrAmbiguousConnectorOperation
			}
			found, exists = record, true
		}
		return nil
	})
	return found, exists, err
}

func (s *Store) Active(ctx context.Context, adapter AdapterName, name string) (OperationRecord, bool, error) {
	if !validAdapter(adapter) || !validConnectorName(name) {
		return OperationRecord{}, false, ErrInvalidConnectorStore
	}
	var found OperationRecord
	var exists bool
	err := s.withStoreLock(ctx, func(_ int, state connectorStoreState) error {
		for _, record := range state.operations {
			if record.Adapter == adapter && record.Name == name && !terminalOperationStage(record.Stage) {
				if exists {
					return ErrAmbiguousConnectorOperation
				}
				found, exists = record, true
			}
		}
		return nil
	})
	return found, exists, err
}

// Completed returns the unique durable completion for the exact confirmed
// transition. Callers cannot use it to recover a merely similar operation.
func (s *Store) CompletedFor(ctx context.Context, change ConfirmedConnectorChange, owner config.StateDigest) (OperationRecord, bool, error) {
	if ValidateConfirmedConnectorChange(change) != nil {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	if _, err := config.ParseStateDigest(string(owner)); err != nil {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	var found OperationRecord
	var exists bool
	err := s.withStoreLock(ctx, func(_ int, state connectorStoreState) error {
		for _, record := range state.operations {
			if record.Stage != StageComplete || record.Adapter != change.Adapter || record.Name != change.Name ||
				record.Action != change.Action || record.PlanDigest != change.PlanDigest ||
				record.ExpectedPriorDigest != change.ExpectedPriorDigest || record.DesiredDigest != change.DesiredDigest || record.OwnerDigest != owner {
				continue
			}
			if exists {
				return ErrAmbiguousConnectorOperation
			}
			found, exists = record, true
		}
		return nil
	})
	return found, exists, err
}

// CompletedTransition finds the unique completed operation matching the
// coordinator's exact high-level prior and desired predicates.
func (s *Store) CompletedTransition(ctx context.Context, adapter AdapterName, name string, action OperationAction, prior, desired, owner config.StateDigest) (OperationRecord, bool, error) {
	if !validAdapter(adapter) || !validConnectorName(name) || (action != OperationInstall && action != OperationRemove) ||
		prior == desired {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	if _, err := config.ParseStateDigest(string(prior)); err != nil {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	if _, err := config.ParseStateDigest(string(desired)); err != nil {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	if _, err := config.ParseStateDigest(string(owner)); err != nil {
		return OperationRecord{}, false, config.ErrConfirmedPlanDrift
	}
	var found OperationRecord
	var exists bool
	err := s.withStoreLock(ctx, func(_ int, state connectorStoreState) error {
		for _, record := range state.operations {
			if record.Stage != StageComplete || record.Adapter != adapter || record.Name != name || record.Action != action ||
				record.ExpectedPriorDigest != prior || record.DesiredDigest != desired || record.OwnerDigest != owner {
				continue
			}
			if exists {
				return ErrAmbiguousConnectorOperation
			}
			found, exists = record, true
		}
		return nil
	})
	return found, exists, err
}

func (s *Store) Advance(ctx context.Context, operationID string, stage OperationStage) error {
	if !types.CanonicalUUID(operationID) {
		return ErrConnectorOperationNotFound
	}
	return s.withStoreLock(ctx, func(fd int, state connectorStoreState) error {
		record, exists := state.operations[operationID]
		if !exists {
			return ErrConnectorOperationNotFound
		}
		if record.Stage == stage {
			return nil
		}
		if !validOperationTransition(record.Stage, stage) {
			return ErrInvalidConnectorStore
		}
		prior, err := marshalCanonicalConnectorJSON(record)
		if err != nil {
			return err
		}
		now := s.clock().UTC()
		if now.Before(record.UpdatedAt) {
			return ErrInvalidConnectorStore
		}
		record.Stage, record.UpdatedAt = stage, now
		if validateOperationRecord(record) != nil {
			return ErrInvalidConnectorStore
		}
		next, err := marshalCanonicalConnectorJSON(record)
		if err != nil {
			return err
		}
		return atomicConnectorWrite(fd, operationRecordName(operationID), next, prior, true, s.random, s.fault)
	})
}

func (s *Store) withStoreLock(ctx context.Context, operation func(int, connectorStoreState) error) error {
	if s == nil || s.root == "" || s.clock == nil || s.random == nil {
		return ErrUnsafeConnectorStore
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	canonical, fd, err := openConnectorStoreRoot(s.root)
	if err != nil {
		return err
	}
	defer closeConnectorFD(fd)
	if canonical != s.root {
		return ErrUnsafeConnectorStore
	}
	unlock, err := lockConnectorFile(ctx, fd, connectorStoreLockName)
	if err != nil {
		return err
	}
	defer unlock()
	state, temps, err := loadConnectorStore(fd)
	if err != nil {
		return err
	}
	if err := retireConnectorTemps(fd, temps); err != nil {
		return err
	}
	return operation(fd, state)
}

type connectorStoreState struct {
	backups     map[string]ConnectorBackup
	operations  map[string]OperationRecord
	identifiers map[string]bool
}

func loadConnectorStore(fd int) (connectorStoreState, []string, error) {
	names, err := listConnectorStoreNames(fd)
	if err != nil {
		return connectorStoreState{}, nil, err
	}
	sort.Strings(names)
	state := connectorStoreState{backups: map[string]ConnectorBackup{}, operations: map[string]OperationRecord{}, identifiers: map[string]bool{}}
	temps := []string{}
	locks := 0
	for _, name := range names {
		switch {
		case name == connectorStoreLockName:
			content, exists, readErr := readConnectorFile(fd, name)
			if readErr != nil || !exists || len(content) != 0 {
				return state, nil, ErrInvalidConnectorStore
			}
			locks++
		case validConnectorPairLockName(name):
			content, exists, readErr := readConnectorFile(fd, name)
			if readErr != nil || !exists || len(content) != 0 {
				return state, nil, ErrInvalidConnectorStore
			}
			locks++
		case isConnectorTemporaryName(name):
			if _, exists, readErr := readConnectorFile(fd, name); readErr != nil || !exists {
				if readErr != nil {
					return state, nil, readErr
				}
				return state, nil, ErrInvalidConnectorStore
			}
			temps = append(temps, name)
		case strings.HasPrefix(name, "backup-"):
			identifier := parseRecordIdentifier(name, "backup-")
			if identifier == "" {
				return state, nil, ErrInvalidConnectorStore
			}
			var value ConnectorBackup
			if err := readCanonicalConnectorRecord(fd, name, &value); err != nil || validateConnectorBackup(value, true) != nil {
				return state, nil, ErrInvalidConnectorStore
			}
			state.backups[identifier], state.identifiers[identifier] = value, true
		case strings.HasPrefix(name, "operation-"):
			identifier := parseRecordIdentifier(name, "operation-")
			if identifier == "" {
				return state, nil, ErrInvalidConnectorStore
			}
			var value OperationRecord
			if err := readCanonicalConnectorRecord(fd, name, &value); err != nil || value.OperationID != identifier || validateOperationRecord(value) != nil {
				return state, nil, ErrInvalidConnectorStore
			}
			state.operations[identifier], state.identifiers[identifier] = value, true
		default:
			return state, nil, ErrInvalidConnectorStore
		}
	}
	if len(state.backups)+len(state.operations) > maxConnectorStoreRecords || locks > maxConnectorStoreLocks || len(temps) > maxConnectorStoreRecords {
		return state, nil, ErrInvalidConnectorStore
	}
	activePairs := map[string]bool{}
	for _, record := range state.operations {
		adapter, identifier, ok := parseConnectorBackupReference(record.BackupReference)
		backup, exists := state.backups[identifier]
		if !ok || !exists || adapter != record.Adapter || backup.Adapter != record.Adapter || backup.Name != record.Name || backup.PlanDigest != record.PlanDigest || record.Action != actionForDesired(backup.Desired) {
			return state, nil, ErrInvalidConnectorStore
		}
		priorDigest, priorErr := DigestConnectorEntry(backup.Prior)
		desiredDigest, desiredErr := DigestConnectorEntry(backup.Desired)
		if priorErr != nil || desiredErr != nil || priorDigest != record.ExpectedPriorDigest || desiredDigest != record.DesiredDigest {
			return state, nil, ErrInvalidConnectorStore
		}
		if !terminalOperationStage(record.Stage) {
			pair := string(record.Adapter) + "\x00" + record.Name
			if activePairs[pair] {
				return state, nil, ErrAmbiguousConnectorOperation
			}
			activePairs[pair] = true
		}
	}
	return state, temps, nil
}

func readCanonicalConnectorRecord(fd int, name string, target any) error {
	data, exists, err := readConnectorFile(fd, name)
	if err != nil || !exists {
		return ErrInvalidConnectorStore
	}
	if err := strictConnectorJSONDecode(data, target); err != nil {
		return ErrInvalidConnectorStore
	}
	canonical, err := marshalCanonicalConnectorJSON(target)
	if err != nil || !bytes.Equal(data, canonical) {
		return ErrInvalidConnectorStore
	}
	return nil
}

func marshalCanonicalConnectorJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidConnectorStore
	}
	return append(data, '\n'), nil
}

func strictConnectorJSONDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanConnectorJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func scanConnectorJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
			if err := scanConnectorJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanConnectorJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func validateConnectorBackup(backup ConnectorBackup, durable bool) error {
	if backup.SchemaVersion != connectorSchemaVersion || !validAdapter(backup.Adapter) || !validConnectorName(backup.Name) {
		return ErrInvalidConnectorStore
	}
	if !durable && backup.CreatedAt.IsZero() == false {
		return ErrInvalidConnectorStore
	}
	if durable && (backup.CreatedAt.IsZero() || !backup.CreatedAt.Equal(backup.CreatedAt.UTC())) {
		return ErrInvalidConnectorStore
	}
	plan, err := BuildChangePlan(backup.Adapter, backup.Name, actionForDesired(backup.Desired), backup.Prior, backup.Desired)
	if err != nil || plan.Digest != backup.PlanDigest {
		return ErrInvalidConnectorStore
	}
	return nil
}

func validateOperationRecord(record OperationRecord) error {
	if record.SchemaVersion != connectorSchemaVersion || !types.CanonicalUUID(record.OperationID) || ValidateConfirmedConnectorChange(ConfirmedConnectorChange{Adapter: record.Adapter, Name: record.Name, Action: record.Action, PlanDigest: record.PlanDigest, ExpectedPriorDigest: record.ExpectedPriorDigest, DesiredDigest: record.DesiredDigest}) != nil || !validOperationStage(record.Stage) || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) || !record.CreatedAt.Equal(record.CreatedAt.UTC()) || !record.UpdatedAt.Equal(record.UpdatedAt.UTC()) {
		return ErrInvalidConnectorStore
	}
	if record.OwnerDigest != "" {
		if _, err := config.ParseStateDigest(string(record.OwnerDigest)); err != nil {
			return ErrInvalidConnectorStore
		}
	}
	adapter, _, ok := parseConnectorBackupReference(record.BackupReference)
	if !ok || adapter != record.Adapter {
		return ErrInvalidConnectorStore
	}
	return nil
}

func validOperationStage(stage OperationStage) bool {
	return stage == StagePrepared || stage == StageApplied || stage == StageVerified || stage == StageRolledBack || stage == StageComplete || stage == StageCompensated
}
func validOperationTransition(from, to OperationStage) bool {
	if to == StageRolledBack {
		return from == StagePrepared || from == StageApplied
	}
	if to == StageComplete {
		return from == StageVerified || from == StageRolledBack
	}
	if to == StageCompensated {
		return from == StageComplete
	}
	return (from == StagePrepared && to == StageApplied) || (from == StageApplied && to == StageVerified)
}
func terminalOperationStage(stage OperationStage) bool {
	return stage == StageComplete || stage == StageCompensated
}
func actionForDesired(entry ConnectorEntry) OperationAction {
	if entry.State == EntryAbsent {
		return OperationRemove
	}
	return OperationInstall
}

func parseConnectorBackupReference(reference config.BackupReference) (AdapterName, string, bool) {
	parts := strings.Split(string(reference), ":")
	if len(parts) != 4 || parts[0] != "connector-backup" || parts[1] != "v1" || !types.CanonicalUUID(parts[3]) {
		return "", "", false
	}
	adapter := AdapterName(parts[2])
	if !validAdapter(adapter) {
		return "", "", false
	}
	return adapter, parts[3], true
}

func backupRecordName(id string) string    { return "backup-" + id + ".json" }
func operationRecordName(id string) string { return "operation-" + id + ".json" }
func parseRecordIdentifier(name, prefix string) string {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
	if !types.CanonicalUUID(id) {
		return ""
	}
	return id
}

func validConnectorPairLockName(name string) bool {
	if !strings.HasPrefix(name, ".pair-") || !strings.HasSuffix(name, ".lock") {
		return false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, ".pair-"), ".lock")
	separator := strings.IndexByte(value, '-')
	if separator < 1 || !validAdapter(AdapterName(value[:separator])) {
		return false
	}
	digest := value[separator+1:]
	if len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isConnectorTemporaryName(name string) bool {
	if !strings.HasPrefix(name, connectorTemporaryPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(name, connectorTemporaryPrefix)
	if len(suffix) != 32 {
		return false
	}
	for _, character := range suffix {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Store) newUUID(existing map[string]bool) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		data := make([]byte, 16)
		if _, err := io.ReadFull(connectorRandomReader{s.random}, data); err != nil {
			return "", err
		}
		data[6] = data[6]&0x0f | 0x40
		data[8] = data[8]&0x3f | 0x80
		id := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
		if !existing[id] {
			return id, nil
		}
	}
	return "", ErrAmbiguousConnectorOperation
}

type connectorRandomReader struct{ read func([]byte) (int, error) }

func (r connectorRandomReader) Read(data []byte) (int, error) { return r.read(data) }
func cloneConnectorBackup(value ConnectorBackup) ConnectorBackup {
	value.Prior = cloneConnectorEntry(value.Prior)
	value.Desired = cloneConnectorEntry(value.Desired)
	return value
}
