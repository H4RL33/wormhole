package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

const (
	connectorSchemaVersion = 1
	maxConnectorNameBytes  = 64
	maxConnectorValueBytes = 4096
	maxConnectorArgs       = 64
	maxConnectorEnv        = 64
)

var (
	ErrInvalidConnectorEntry          = errors.New("connector: invalid canonical entry")
	ErrUnsupportedConnectorEntry      = errors.New("connector: unsupported native entry")
	ErrConnectorUnavailable           = errors.New("connector: native client unavailable")
	ErrConnectorStateDrift            = errors.New("connector: observed state drift")
	ErrInvalidConnectorPlan           = errors.New("connector: invalid change plan")
	ErrUnsafeConnectorStore           = errors.New("connector: unsafe owner-only store")
	ErrInvalidConnectorStore          = errors.New("connector: invalid durable store")
	ErrConnectorFilesystemUnsupported = errors.New("connector: owner-only filesystem unsupported")
	ErrConnectorBackupNotFound        = errors.New("connector: backup not found")
	ErrConnectorOperationNotFound     = errors.New("connector: operation not found")
	ErrAmbiguousConnectorOperation    = errors.New("connector: ambiguous active operation")
	ErrConnectorCommandFailed         = errors.New("connector: native client command failed")
)

type AdapterName string

const (
	AdapterCodex  AdapterName = "codex"
	AdapterClaude AdapterName = "claude"
)

type EntryState string

const (
	EntryAbsent  EntryState = "absent"
	EntryPresent EntryState = "present"
)

type Scope string

const ScopeUser Scope = "user"

type Transport string

const TransportStdio Transport = "stdio"

type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ConnectorEntry struct {
	State     EntryState            `json:"state"`
	Scope     Scope                 `json:"scope,omitempty"`
	Transport Transport             `json:"transport,omitempty"`
	Command   string                `json:"command,omitempty"`
	Args      []string              `json:"args"`
	Env       []EnvironmentVariable `json:"env"`
}

type Availability struct {
	Available bool
	Version   string
}

type OperationAction string

const (
	OperationInstall OperationAction = "install"
	OperationRemove  OperationAction = "remove"
)

type ChangePlan struct {
	Adapter AdapterName        `json:"adapter"`
	Name    string             `json:"name"`
	Action  OperationAction    `json:"action"`
	Prior   ConnectorEntry     `json:"prior"`
	Desired ConnectorEntry     `json:"desired"`
	Digest  config.StateDigest `json:"-"`
}

type ConfirmedConnectorChange struct {
	Adapter             AdapterName
	Name                string
	Action              OperationAction
	PlanDigest          config.StateDigest
	ExpectedPriorDigest config.StateDigest
	DesiredDigest       config.StateDigest
}

type OperationStage string

const (
	StagePrepared   OperationStage = "prepared"
	StageApplied    OperationStage = "applied"
	StageVerified   OperationStage = "verified"
	StageRolledBack OperationStage = "rolled_back"
	StageComplete   OperationStage = "complete"
)

type ConnectorBackup struct {
	SchemaVersion int                `json:"schema_version"`
	Adapter       AdapterName        `json:"adapter"`
	Name          string             `json:"name"`
	Prior         ConnectorEntry     `json:"prior"`
	Desired       ConnectorEntry     `json:"desired"`
	PlanDigest    config.StateDigest `json:"plan_digest"`
	CreatedAt     time.Time          `json:"created_at,omitempty"`
}

type PrepareOperation struct {
	Change          ConfirmedConnectorChange
	BackupReference config.BackupReference
}

type OperationRecord struct {
	SchemaVersion       int                    `json:"schema_version"`
	OperationID         string                 `json:"operation_id"`
	Adapter             AdapterName            `json:"adapter"`
	Name                string                 `json:"name"`
	Action              OperationAction        `json:"action"`
	PlanDigest          config.StateDigest     `json:"plan_digest"`
	ExpectedPriorDigest config.StateDigest     `json:"expected_prior_digest"`
	DesiredDigest       config.StateDigest     `json:"desired_digest"`
	BackupReference     config.BackupReference `json:"backup_reference"`
	Stage               OperationStage         `json:"stage"`
	CreatedAt           time.Time              `json:"created_at,omitempty"`
	UpdatedAt           time.Time              `json:"updated_at,omitempty"`
}

type TransactionResult struct {
	OperationID     string
	Stage           OperationStage
	BackupReference config.BackupReference
}

type Adapter interface {
	AdapterName() AdapterName
	Discover(context.Context) (Availability, error)
	Inspect(context.Context) (ConnectorEntry, error)
	Plan(context.Context, ConnectorEntry, ConnectorEntry) (ChangePlan, error)
	Apply(context.Context, ChangePlan) error
	Verify(context.Context, ConnectorEntry) error
	Rollback(context.Context, ChangePlan) error
	Remove(context.Context, ConnectorEntry) error
}

type BackupStore interface {
	Put(context.Context, ConnectorBackup) (config.BackupReference, error)
	Get(context.Context, config.BackupReference) (ConnectorBackup, error)
}

type OperationJournal interface {
	Prepare(context.Context, PrepareOperation) (OperationRecord, error)
	Active(context.Context, AdapterName, string) (OperationRecord, bool, error)
	Advance(context.Context, string, OperationStage) error
}

type CompletedOperationJournal interface {
	Completed(context.Context, ConfirmedConnectorChange) (OperationRecord, bool, error)
}

type OperationCoordinator interface {
	WithOperationLock(context.Context, AdapterName, string, func(context.Context) error) error
}

func DigestConnectorEntry(entry ConnectorEntry) (config.StateDigest, error) {
	if err := validateConnectorEntry(entry); err != nil {
		return "", err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", ErrInvalidConnectorEntry
	}
	return config.SHA256StateDigest(data), nil
}

func BuildChangePlan(adapter AdapterName, name string, action OperationAction, prior, desired ConnectorEntry) (ChangePlan, error) {
	plan := ChangePlan{Adapter: adapter, Name: name, Action: action, Prior: cloneConnectorEntry(prior), Desired: cloneConnectorEntry(desired)}
	if err := validateChangePlan(plan); err != nil {
		return ChangePlan{}, err
	}
	wire := struct {
		Adapter AdapterName     `json:"adapter"`
		Name    string          `json:"name"`
		Action  OperationAction `json:"action"`
		Prior   ConnectorEntry  `json:"prior"`
		Desired ConnectorEntry  `json:"desired"`
	}{plan.Adapter, plan.Name, plan.Action, plan.Prior, plan.Desired}
	data, err := json.Marshal(wire)
	if err != nil {
		return ChangePlan{}, ErrInvalidConnectorPlan
	}
	plan.Digest = config.SHA256StateDigest(data)
	return plan, nil
}

func ValidateConfirmedConnectorChange(change ConfirmedConnectorChange) error {
	if !validAdapter(change.Adapter) || !validConnectorName(change.Name) || !validOperationAction(change.Action) {
		return config.ErrConfirmedPlanDrift
	}
	for _, digest := range []config.StateDigest{change.PlanDigest, change.ExpectedPriorDigest, change.DesiredDigest} {
		if _, err := config.ParseStateDigest(string(digest)); err != nil {
			return config.ErrConfirmedPlanDrift
		}
	}
	return nil
}

func EqualConnectorEntry(first, second ConnectorEntry) bool {
	firstDigest, firstErr := DigestConnectorEntry(first)
	secondDigest, secondErr := DigestConnectorEntry(second)
	return firstErr == nil && secondErr == nil && firstDigest == secondDigest
}

func validateChangePlan(plan ChangePlan) error {
	if !validAdapter(plan.Adapter) || !validConnectorName(plan.Name) || !validOperationAction(plan.Action) || validateConnectorEntry(plan.Prior) != nil || validateConnectorEntry(plan.Desired) != nil {
		return ErrInvalidConnectorPlan
	}
	if EqualConnectorEntry(plan.Prior, plan.Desired) {
		return ErrInvalidConnectorPlan
	}
	if plan.Action == OperationInstall && plan.Desired.State != EntryPresent {
		return ErrInvalidConnectorPlan
	}
	if plan.Action == OperationRemove && (plan.Prior.State != EntryPresent || plan.Desired.State != EntryAbsent) {
		return ErrInvalidConnectorPlan
	}
	return nil
}

func validateDesiredWormholeEntry(entry ConnectorEntry) error {
	if validateConnectorEntry(entry) != nil || entry.State != EntryPresent || !filepath.IsAbs(entry.Command) || filepath.Clean(entry.Command) != entry.Command || entry.Command == string(filepath.Separator) || len(entry.Args) != 1 || entry.Args[0] != "mcp" || len(entry.Env) != 0 {
		return ErrUnsupportedConnectorEntry
	}
	return nil
}

func validateConnectorEntry(entry ConnectorEntry) error {
	if entry.State == EntryAbsent {
		if entry.Scope != "" || entry.Transport != "" || entry.Command != "" || entry.Args != nil || entry.Env != nil {
			return ErrInvalidConnectorEntry
		}
		return nil
	}
	if entry.State != EntryPresent || entry.Scope != ScopeUser || entry.Transport != TransportStdio || !validConnectorValue(entry.Command) || entry.Args == nil || entry.Env == nil || len(entry.Args) > maxConnectorArgs || len(entry.Env) > maxConnectorEnv {
		return ErrInvalidConnectorEntry
	}
	for _, argument := range entry.Args {
		if !validConnectorValueAllowEmpty(argument) {
			return ErrInvalidConnectorEntry
		}
	}
	priorName := ""
	for _, variable := range entry.Env {
		if !validEnvironmentName(variable.Name) || variable.Name <= priorName || !validConnectorValueAllowEmpty(variable.Value) {
			return ErrInvalidConnectorEntry
		}
		priorName = variable.Name
	}
	return nil
}

func validConnectorValue(value string) bool {
	return value != "" && validConnectorValueAllowEmpty(value)
}

func validConnectorValueAllowEmpty(value string) bool {
	if len(value) > maxConnectorValueBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validConnectorName(value string) bool {
	return value == "wormhole"
}

func validAdapter(value AdapterName) bool { return value == AdapterCodex || value == AdapterClaude }
func validOperationAction(value OperationAction) bool {
	return value == OperationInstall || value == OperationRemove
}

func cloneConnectorEntry(entry ConnectorEntry) ConnectorEntry {
	if entry.Args != nil {
		entry.Args = append([]string{}, entry.Args...)
	}
	if entry.Env != nil {
		entry.Env = append([]EnvironmentVariable{}, entry.Env...)
	}
	return entry
}

func redactedCommandError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w", ErrConnectorCommandFailed)
}
