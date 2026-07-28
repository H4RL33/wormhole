package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	TrialMetricsSchemaVersion   = 1
	TrialMetricsMaxJSONBytes    = 1 << 20
	TrialMetricsMaxJSONDepth    = 64
	TrialMetricsMaxParticipants = 100
	TrialMetricsMaxItems        = 100
	TrialMetricsMaxStringBytes  = 256
	TrialConsentVersion         = "closed-alpha-v1"
)

const (
	TrialParticipantCompleted  = "completed"
	TrialParticipantIncomplete = "incomplete"
	TrialParticipantWithdrawn  = "withdrawn"

	TrialBaselineGuidanceOff  = "guidance_off"
	TrialBaselineCodeGraphOff = "code_graph_off"

	GateDContinueTowardsBetaPlanning    = "continue towards beta planning"
	GateDContinueWithNarrowedScope      = "continue with narrowed scope"
	GateDRepeatAlphaAfterCorrectiveWork = "repeat alpha after corrective work"
	GateDStopCurrentDirection           = "stop the current direction"

	TrialEvidenceSupports = "supports"
	TrialEvidenceContrary = "contrary"
	TrialEvidenceMissing  = "missing"
)

type TrialOS string
type TrialHarness string
type TrialModelFamily string
type TrialRepositoryProfile string
type TrialSupportCode string
type TrialFailureCode string
type TrialProcedureOmissionCode string
type TrialQueryCategory string
type TrialTaskKind string
type TrialToolSelection string
type TrialLoopAdherence string
type TrialQuality string
type TrialOmissionCode string
type TrialComparisonOmissionCode string
type TrialGateCriterion string

const (
	TrialOSLinux TrialOS = "linux"
	TrialOSWSL   TrialOS = "wsl"
	TrialOSOther TrialOS = "other"

	TrialHarnessClaudeCode TrialHarness = "claude_code"
	TrialHarnessOpenCode   TrialHarness = "opencode"
	TrialHarnessOther      TrialHarness = "other"

	TrialModelAnthropic TrialModelFamily = "anthropic"
	TrialModelOpenAI    TrialModelFamily = "openai"
	TrialModelGoogle    TrialModelFamily = "google"
	TrialModelLocal     TrialModelFamily = "local"
	TrialModelOther     TrialModelFamily = "other"

	TrialRepositorySingle   TrialRepositoryProfile = "single_repository"
	TrialRepositoryMonorepo TrialRepositoryProfile = "monorepo"
	TrialRepositoryMulti    TrialRepositoryProfile = "multi_repository"
	TrialRepositoryOther    TrialRepositoryProfile = "other"

	TrialSupportInstallation TrialSupportCode = "installation"
	TrialSupportEnrolment    TrialSupportCode = "enrolment"
	TrialSupportPermissions  TrialSupportCode = "permissions"
	TrialSupportGuidance     TrialSupportCode = "guidance"
	TrialSupportCodeGraph    TrialSupportCode = "code_graph"
	TrialSupportSyncOutage   TrialSupportCode = "sync_outage"
	TrialSupportTaskWorkflow TrialSupportCode = "task_workflow"
	TrialSupportMeasurement  TrialSupportCode = "measurement"
	TrialSupportPrivacy      TrialSupportCode = "privacy"

	TrialFailureInstallation TrialFailureCode = "installation"
	TrialFailureEnrolment    TrialFailureCode = "enrolment"
	TrialFailureGatewayCall  TrialFailureCode = "gateway_call"
	TrialFailurePermission   TrialFailureCode = "permission"
	TrialFailureGuidance     TrialFailureCode = "guidance"
	TrialFailureCodeGraph    TrialFailureCode = "code_graph"
	TrialFailureSync         TrialFailureCode = "sync"
	TrialFailureHandoff      TrialFailureCode = "handoff"
	TrialFailureTask         TrialFailureCode = "task"
	TrialFailureValidation   TrialFailureCode = "validation"
	TrialFailureOther        TrialFailureCode = "other"

	TrialProcedureOmissionManifest  TrialProcedureOmissionCode = "manifest_approval"
	TrialProcedureOmissionCodeGraph TrialProcedureOmissionCode = "code_graph_enablement"
	TrialProcedureOmissionBenchmark TrialProcedureOmissionCode = "benchmark_arm"
	TrialProcedureOmissionOutage    TrialProcedureOmissionCode = "outage_exercise"
	TrialProcedureOmissionSupport   TrialProcedureOmissionCode = "support_record"
	TrialProcedureOmissionSubmit    TrialProcedureOmissionCode = "participant_submission"

	TrialQueryCategoryNavigation    TrialQueryCategory = "navigation"
	TrialQueryCategorySymbolLookup  TrialQueryCategory = "symbol_lookup"
	TrialQueryCategoryRelationship  TrialQueryCategory = "relationship"
	TrialQueryCategorySourceRequest TrialQueryCategory = "source_request"
	TrialQueryCategoryOther         TrialQueryCategory = "other"

	TrialTaskFeature       TrialTaskKind = "feature"
	TrialTaskBugfix        TrialTaskKind = "bugfix"
	TrialTaskReview        TrialTaskKind = "review"
	TrialTaskRefactor      TrialTaskKind = "refactor"
	TrialTaskDocumentation TrialTaskKind = "documentation"
	TrialTaskOther         TrialTaskKind = "other"

	TrialToolSelectionAppropriate   TrialToolSelection = "appropriate"
	TrialToolSelectionMixed         TrialToolSelection = "mixed"
	TrialToolSelectionInappropriate TrialToolSelection = "inappropriate"
	TrialToolSelectionMissing       TrialToolSelection = "missing"

	TrialLoopComplete TrialLoopAdherence = "complete"
	TrialLoopPartial  TrialLoopAdherence = "partial"
	TrialLoopAbsent   TrialLoopAdherence = "absent"
	TrialLoopMissing  TrialLoopAdherence = "missing"

	TrialQualityMet     TrialQuality = "met"
	TrialQualityPartial TrialQuality = "partial"
	TrialQualityFailed  TrialQuality = "failed"
	TrialQualityMissing TrialQuality = "missing"
)

const (
	TrialOmissionInstallationCompleted         TrialOmissionCode = "installation_completed"
	TrialOmissionTimeToFirstGatewayMCPCall     TrialOmissionCode = "time_to_first_gateway_mcp_call_ms"
	TrialOmissionTimeToProductiveWork          TrialOmissionCode = "time_to_productive_work_ms"
	TrialOmissionToolSuccessCount              TrialOmissionCode = "tool_success_count"
	TrialOmissionToolDenialCount               TrialOmissionCode = "tool_denial_count"
	TrialOmissionToolFailureCount              TrialOmissionCode = "tool_failure_count"
	TrialOmissionContextAtSessionStart         TrialOmissionCode = "context_retrieved_at_session_start"
	TrialOmissionHumanCoaching                 TrialOmissionCode = "human_coaching_interventions"
	TrialOmissionModelHandoff                  TrialOmissionCode = "model_handoff_succeeded"
	TrialOmissionSyncRecovery                  TrialOmissionCode = "sync_recovery_succeeded"
	TrialOmissionKBRelevantResults             TrialOmissionCode = "kb_relevant_results"
	TrialOmissionKBResultsConsidered           TrialOmissionCode = "kb_results_considered"
	TrialOmissionLowValueKBContributions       TrialOmissionCode = "duplicate_or_low_value_kb_contributions"
	TrialOmissionCodeGraphUsefulQueries        TrialOmissionCode = "code_graph_useful_queries"
	TrialOmissionCodeGraphQueries              TrialOmissionCode = "code_graph_queries"
	TrialOmissionFilesBeforeCorrectEdit        TrialOmissionCode = "files_read_before_correct_edit"
	TrialOmissionSourceBytesBeforeCorrectEdit  TrialOmissionCode = "source_bytes_read_before_correct_edit"
	TrialOmissionEventCount                    TrialOmissionCode = "event_count"
	TrialOmissionEventNoiseCount               TrialOmissionCode = "event_noise_count"
	TrialOmissionTaskStateAccurate             TrialOmissionCode = "task_state_accurate"
	TrialOmissionContextReconstructionsAvoided TrialOmissionCode = "context_reconstructions_avoided"
	TrialOmissionTokensBeforeProductiveWork    TrialOmissionCode = "tokens_before_productive_work"

	TrialComparisonOmissionUsefulWrites TrialComparisonOmissionCode = "useful_shared_state_writes"
	TrialComparisonOmissionCorrections  TrialComparisonOmissionCode = "human_corrections"
	TrialComparisonOmissionToolCalls    TrialComparisonOmissionCode = "unnecessary_tool_calls"
	TrialComparisonOmissionSourceFiles  TrialComparisonOmissionCode = "source_files_read"
	TrialComparisonOmissionSourceBytes  TrialComparisonOmissionCode = "source_bytes_read"

	TrialGateManualContextRelay     TrialGateCriterion = "manual_context_relay"
	TrialGateRepeatedReconstruction TrialGateCriterion = "repeated_project_reconstruction"
	TrialGateCrossModelContinuation TrialGateCriterion = "cross_model_continuation"
	TrialGateInterruptionRecovery   TrialGateCriterion = "interruption_recovery"
	TrialGateGuidanceLearnability   TrialGateCriterion = "managed_guidance_learnability"
	TrialGateSourceDiscovery        TrialGateCriterion = "source_discovery_narrowing"
	TrialGateMaintenance            TrialGateCriterion = "maintenance_proportionate"
	TrialGateEventNoise             TrialGateCriterion = "event_noise_proportionate"
	TrialGateConfidence             TrialGateCriterion = "confidence_appropriate"
)

var (
	ErrTrialMetricsInvalid = errors.New("localapi: invalid trial metrics export")
	ErrTrialPrivacy        = errors.New("localapi: trial metrics privacy violation")
)

type TrialMetricsExport struct {
	SchemaVersion    int                  `json:"schema_version"`
	ExportID         string               `json:"export_id"`
	ReleaseCandidate string               `json:"release_candidate"`
	GeneratedAt      string               `json:"generated_at"`
	Participants     []TrialParticipant   `json:"participants"`
	GateDDecisions   []TrialGateDDecision `json:"gate_d_decisions"`
}

type TrialParticipantExport struct {
	SchemaVersion    int              `json:"schema_version"`
	ExportID         string           `json:"export_id"`
	ReleaseCandidate string           `json:"release_candidate"`
	GeneratedAt      string           `json:"generated_at"`
	Participant      TrialParticipant `json:"participant"`
}

type TrialParticipant struct {
	ParticipantID          string                       `json:"participant_id,omitempty"`
	External               bool                         `json:"external,omitempty"`
	Status                 string                       `json:"status"`
	Consent                TrialConsent                 `json:"consent"`
	Environment            *TrialEnvironment            `json:"environment,omitempty"`
	Metrics                *TrialParticipantMetrics     `json:"metrics,omitempty"`
	Comparisons            []TrialComparison            `json:"comparisons,omitempty"`
	SupportInterventions   []TrialSupportCode           `json:"support_interventions,omitempty"`
	Failures               []TrialFailureCode           `json:"failures,omitempty"`
	ProcedureOmissions     []TrialProcedureOmissionCode `json:"procedure_omissions,omitempty"`
	PrivateQueryCategories []TrialQueryCategory         `json:"private_query_categories,omitempty"`
}

type TrialConsent struct {
	Version                string `json:"version"`
	Collection             bool   `json:"collection"`
	ParticipantSubmission  bool   `json:"participant_submission"`
	PrivateQueryCollection bool   `json:"private_query_collection"`
	PrivateQueryConsentAt  string `json:"private_query_consent_at,omitempty"`
	RecordedAt             string `json:"recorded_at"`
	WithdrawnAt            string `json:"withdrawn_at,omitempty"`
	WithdrawnDataDeletedAt string `json:"withdrawn_data_deleted_at,omitempty"`
}

type TrialEnvironment struct {
	OSFamily          TrialOS                `json:"os_family"`
	Harnesses         []TrialHarness         `json:"harnesses"`
	ModelFamilies     []TrialModelFamily     `json:"model_families"`
	RepositoryProfile TrialRepositoryProfile `json:"repository_profile"`
}

type TrialParticipantMetrics struct {
	InstallationCompleted              *bool               `json:"installation_completed"`
	TimeToFirstGatewayMCPCallMS        *int64              `json:"time_to_first_gateway_mcp_call_ms"`
	TimeToProductiveWorkMS             *int64              `json:"time_to_productive_work_ms"`
	ToolSuccessCount                   *int                `json:"tool_success_count"`
	ToolDenialCount                    *int                `json:"tool_denial_count"`
	ToolFailureCount                   *int                `json:"tool_failure_count"`
	ContextRetrievedAtSessionStart     *bool               `json:"context_retrieved_at_session_start"`
	HumanCoachingInterventions         *int                `json:"human_coaching_interventions"`
	ModelHandoffSucceeded              *bool               `json:"model_handoff_succeeded"`
	SyncRecoverySucceeded              *bool               `json:"sync_recovery_succeeded"`
	KBRelevantResults                  *int                `json:"kb_relevant_results"`
	KBResultsConsidered                *int                `json:"kb_results_considered"`
	DuplicateOrLowValueKBContributions *int                `json:"duplicate_or_low_value_kb_contributions"`
	CodeGraphUsefulQueries             *int                `json:"code_graph_useful_queries"`
	CodeGraphQueries                   *int                `json:"code_graph_queries"`
	FilesReadBeforeCorrectEdit         *int                `json:"files_read_before_correct_edit"`
	SourceBytesReadBeforeCorrectEdit   *int64              `json:"source_bytes_read_before_correct_edit"`
	EventCount                         *int                `json:"event_count"`
	EventNoiseCount                    *int                `json:"event_noise_count"`
	TaskStateAccurate                  *bool               `json:"task_state_accurate"`
	ContextReconstructionsAvoided      *int                `json:"context_reconstructions_avoided"`
	TokensBeforeProductiveWork         *int64              `json:"tokens_before_productive_work"`
	Omissions                          []TrialOmissionCode `json:"omissions,omitempty"`
}

type TrialComparison struct {
	TaskKind              TrialTaskKind      `json:"task_kind"`
	BaselineKind          string             `json:"baseline_kind"`
	SameCheckoutRevision  bool               `json:"same_checkout_revision"`
	SamePermissions       bool               `json:"same_permissions"`
	SameSuccessCriteria   bool               `json:"same_success_criteria"`
	SameMeasurementMethod bool               `json:"same_measurement_method"`
	Baseline              TrialComparisonArm `json:"baseline"`
	Alpha                 TrialComparisonArm `json:"alpha"`
}

type TrialComparisonArm struct {
	ToolSelection           TrialToolSelection            `json:"tool_selection"`
	OperatingLoopAdherence  TrialLoopAdherence            `json:"operating_loop_adherence"`
	UsefulSharedStateWrites *int                          `json:"useful_shared_state_writes"`
	HumanCorrections        *int                          `json:"human_corrections"`
	TaskQuality             TrialQuality                  `json:"task_quality"`
	UnnecessaryToolCalls    *int                          `json:"unnecessary_tool_calls"`
	SourceFilesRead         *int                          `json:"source_files_read"`
	SourceBytesRead         *int64                        `json:"source_bytes_read"`
	ReviewQuality           TrialQuality                  `json:"review_quality"`
	Omissions               []TrialComparisonOmissionCode `json:"omissions,omitempty"`
}

type TrialGateDDecision struct {
	Decision           string               `json:"decision"`
	Evaluation         TrialGateDEvaluation `json:"evaluation"`
	SupportingEvidence []TrialGateCriterion `json:"supporting_evidence"`
	ContraryEvidence   []TrialGateCriterion `json:"contrary_evidence"`
}

type TrialGateDEvaluation struct {
	ManualContextRelay            string `json:"manual_context_relay"`
	RepeatedProjectReconstruction string `json:"repeated_project_reconstruction"`
	CrossModelContinuation        string `json:"cross_model_continuation"`
	InterruptionRecovery          string `json:"interruption_recovery"`
	ManagedGuidanceLearnability   string `json:"managed_guidance_learnability"`
	SourceDiscoveryNarrowing      string `json:"source_discovery_narrowing"`
	MaintenanceProportionate      string `json:"maintenance_proportionate"`
	EventNoiseProportionate       string `json:"event_noise_proportionate"`
	ConfidenceAppropriate         string `json:"confidence_appropriate"`
}

func MarshalTrialMetricsExport(export TrialMetricsExport) ([]byte, error) {
	if err := ValidateTrialMetricsExport(export); err != nil {
		return nil, err
	}
	return marshalTrialJSON(export)
}

func MarshalTrialParticipantExport(export TrialParticipantExport) ([]byte, error) {
	if err := ValidateTrialParticipantExport(export); err != nil {
		return nil, err
	}
	return marshalTrialJSON(export)
}

func MarshalTrialParticipantPreview(export TrialParticipantExport) ([]byte, error) {
	if err := ValidateTrialParticipantPreview(export); err != nil {
		return nil, err
	}
	return marshalTrialJSON(export)
}

func marshalTrialJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrTrialMetricsInvalid, err)
	}
	data = append(data, '\n')
	if len(data) > TrialMetricsMaxJSONBytes {
		return nil, invalidTrial("encoded JSON exceeds %d bytes", TrialMetricsMaxJSONBytes)
	}
	return data, nil
}

func DecodeTrialMetricsExport(data []byte) (TrialMetricsExport, error) {
	if err := inspectTrialJSON(data); err != nil {
		return TrialMetricsExport{}, err
	}
	var export TrialMetricsExport
	if err := decodeStrictTrialJSON(data, &export); err != nil {
		return TrialMetricsExport{}, err
	}
	if err := ValidateTrialMetricsExport(export); err != nil {
		return TrialMetricsExport{}, err
	}
	return export, nil
}

func DecodeTrialParticipantExport(data []byte) (TrialParticipantExport, error) {
	if err := inspectTrialJSON(data); err != nil {
		return TrialParticipantExport{}, err
	}
	var export TrialParticipantExport
	if err := decodeStrictTrialJSON(data, &export); err != nil {
		return TrialParticipantExport{}, err
	}
	if err := ValidateTrialParticipantExport(export); err != nil {
		return TrialParticipantExport{}, err
	}
	return export, nil
}

func DecodeTrialParticipantPreview(data []byte) (TrialParticipantExport, error) {
	if err := inspectTrialJSON(data); err != nil {
		return TrialParticipantExport{}, err
	}
	var export TrialParticipantExport
	if err := decodeStrictTrialJSON(data, &export); err != nil {
		return TrialParticipantExport{}, err
	}
	if err := ValidateTrialParticipantPreview(export); err != nil {
		return TrialParticipantExport{}, err
	}
	return export, nil
}

func decodeStrictTrialJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalidTrial("decode failed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalidTrial("trailing JSON")
	}
	return nil
}

func ValidateTrialMetricsExport(export TrialMetricsExport) error {
	if err := inspectTrialValue(export); err != nil {
		return err
	}
	if err := validateTrialEnvelope(export.SchemaVersion, export.ExportID, export.ReleaseCandidate, export.GeneratedAt); err != nil {
		return err
	}
	if len(export.Participants) > TrialMetricsMaxParticipants {
		return invalidTrial("participants exceed limit")
	}
	if len(export.GateDDecisions) != 1 {
		return invalidTrial("exactly one Gate D decision is required")
	}
	if err := validateTrialGateDDecision(export.GateDDecisions[0]); err != nil {
		return err
	}
	completedExternal := 0
	ids := make(map[string]struct{}, len(export.Participants))
	for i, participant := range export.Participants {
		if err := validateTrialParticipant(participant, export.GeneratedAt, true); err != nil {
			if errors.Is(err, ErrTrialPrivacy) {
				return err
			}
			return invalidTrial("participant %d: %v", i, err)
		}
		if participant.Status != TrialParticipantWithdrawn {
			if _, exists := ids[participant.ParticipantID]; exists {
				return invalidTrial("participant %d: duplicate participant_id", i)
			}
			ids[participant.ParticipantID] = struct{}{}
		}
		if participant.External && participant.Status == TrialParticipantCompleted {
			completedExternal++
		}
	}
	if completedExternal < 3 {
		return invalidTrial("at least three completed external participants are required")
	}
	return nil
}

func ValidateTrialParticipantExport(export TrialParticipantExport) error {
	return validateTrialParticipantEnvelope(export, true)
}

func ValidateTrialParticipantPreview(export TrialParticipantExport) error {
	return validateTrialParticipantEnvelope(export, false)
}

func validateTrialParticipantEnvelope(export TrialParticipantExport, requireSubmission bool) error {
	if err := inspectTrialValue(export); err != nil {
		return err
	}
	if err := validateTrialEnvelope(export.SchemaVersion, export.ExportID, export.ReleaseCandidate, export.GeneratedAt); err != nil {
		return err
	}
	if err := validateTrialParticipant(export.Participant, export.GeneratedAt, requireSubmission); err != nil {
		if errors.Is(err, ErrTrialPrivacy) {
			return err
		}
		return invalidTrial("participant: %v", err)
	}
	return nil
}

func inspectTrialValue(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return invalidTrial("encode for validation: %v", err)
	}
	return inspectTrialJSON(data)
}

func inspectTrialJSON(data []byte) error {
	if len(data) > TrialMetricsMaxJSONBytes {
		return invalidTrial("JSON exceeds %d bytes", TrialMetricsMaxJSONBytes)
	}
	if err := rejectDuplicateTrialJSONKeys(data); err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return invalidTrial("malformed JSON: %v", err)
	}
	return inspectTrialJSONValue(value)
}

func rejectDuplicateTrialJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanTrialJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalidTrial("trailing JSON")
	}
	return nil
}

func scanTrialJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return invalidTrial("malformed JSON: %v", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= TrialMetricsMaxJSONDepth {
		return invalidTrial("JSON nesting exceeds depth limit of %d", TrialMetricsMaxJSONDepth)
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return invalidTrial("malformed object: %v", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return invalidTrial("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return invalidTrial("duplicate JSON key")
			}
			seen[key] = struct{}{}
			if err := scanTrialJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return invalidTrial("malformed object: %v", err)
		}
	case '[':
		for decoder.More() {
			if err := scanTrialJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return invalidTrial("malformed array: %v", err)
		}
	default:
		return invalidTrial("unexpected JSON delimiter")
	}
	return nil
}

func inspectTrialJSONValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if len(key) > TrialMetricsMaxStringBytes {
				return invalidTrial("JSON key exceeds string limit")
			}
			lowerKey := strings.ToLower(key)
			switch lowerKey {
			case "source_body", "source_bodies", "private_query", "private_query_text", "query_text",
				"token", "access_token", "bearer_token", "authorization", "authorization_header",
				"password", "secret", "api_key", "private_key", "credential", "credentials", "credential_profile",
				"repository_content", "unrelated_repository_content", "unrelated_repo_content", "repo_content", "repository_path", "repository_remote":
				return privacyTrial("excluded field %q", key)
			}
			if strings.HasSuffix(lowerKey, "_id") && lowerKey != "export_id" && lowerKey != "participant_id" {
				return privacyTrial("cross-project identifier field %q", key)
			}
			if err := inspectTrialJSONValue(child); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > TrialMetricsMaxItems {
			return invalidTrial("JSON array exceeds item limit")
		}
		for _, child := range typed {
			if err := inspectTrialJSONValue(child); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > TrialMetricsMaxStringBytes {
			return invalidTrial("JSON string exceeds limit")
		}
		if containsTrialCredential(typed) {
			return privacyTrial("credential-shaped value")
		}
	}
	return nil
}

func containsTrialCredential(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_", "glpat-", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return containsAuthSchemeValue(lower, "bearer ") || containsAuthSchemeValue(lower, "basic ")
}

func containsAuthSchemeValue(lower, scheme string) bool {
	index := strings.Index(lower, scheme)
	if index < 0 {
		return false
	}
	fields := strings.Fields(lower[index+len(scheme):])
	if len(fields) == 0 {
		return false
	}
	candidate := strings.Trim(fields[0], "\"'`.,;:()[]{}")
	return len(candidate) >= 8
}

func validateTrialEnvelope(version int, exportID, releaseCandidate, generatedAt string) error {
	if version != TrialMetricsSchemaVersion {
		return invalidTrial("schema_version must be %d", TrialMetricsSchemaVersion)
	}
	if !validTrialSlug(exportID) {
		return invalidTrial("export_id must be a lowercase trial-local slug")
	}
	if canonicalTrialUUID(exportID) {
		return privacyTrial("export_id must not be UUID-shaped")
	}
	if !validTrialCommit(releaseCandidate) {
		return invalidTrial("release_candidate must be a full lowercase Git commit")
	}
	if !validTrialTimestamp(generatedAt) {
		return invalidTrial("generated_at must be RFC3339")
	}
	return nil
}

func validateTrialParticipant(participant TrialParticipant, generatedAt string, requireSubmission bool) error {
	if participant.Consent.Version != TrialConsentVersion || !participant.Consent.Collection || !validTrialTimestamp(participant.Consent.RecordedAt) {
		return errors.New("versioned collection consent is required")
	}
	if participant.Status != TrialParticipantWithdrawn && requireSubmission && !participant.Consent.ParticipantSubmission {
		return errors.New("participant-submission consent is required")
	}
	if participant.Consent.PrivateQueryCollection {
		if !validTrialTimestamp(participant.Consent.PrivateQueryConsentAt) {
			return errors.New("private-query collection requires a separate consent timestamp")
		}
	} else if participant.Consent.PrivateQueryConsentAt != "" || len(participant.PrivateQueryCategories) > 0 {
		return errors.New("private-query category lacks separate consent")
	}
	if err := validateTrialConsentChronology(participant.Consent, generatedAt); err != nil {
		return err
	}
	if participant.Status == TrialParticipantWithdrawn {
		if !validTrialTimestamp(participant.Consent.WithdrawnAt) || !validTrialTimestamp(participant.Consent.WithdrawnDataDeletedAt) {
			return errors.New("withdrawal and deletion timestamps are required")
		}
		if participant.ParticipantID != "" || participant.External || participant.Environment != nil || participant.Metrics != nil || len(participant.Comparisons) > 0 || len(participant.SupportInterventions) > 0 || len(participant.Failures) > 0 || len(participant.ProcedureOmissions) > 0 || len(participant.PrivateQueryCategories) > 0 {
			return privacyTrial("withdrawal receipt must be minimal and unlinked")
		}
		return nil
	}
	if participant.Status != TrialParticipantCompleted && participant.Status != TrialParticipantIncomplete {
		return errors.New("status is invalid")
	}
	if participant.Consent.WithdrawnAt != "" || participant.Consent.WithdrawnDataDeletedAt != "" {
		return errors.New("non-withdrawn participant contains withdrawal metadata")
	}
	if !validTrialSlug(participant.ParticipantID) {
		return errors.New("participant_id must be a trial-local slug")
	}
	if canonicalTrialUUID(participant.ParticipantID) {
		return privacyTrial("participant_id must not be UUID-shaped")
	}
	if participant.Environment == nil || participant.Metrics == nil {
		return errors.New("environment and metrics are required")
	}
	if err := validateTrialEnvironment(*participant.Environment); err != nil {
		return err
	}
	if err := validateTrialMetrics(*participant.Metrics); err != nil {
		return err
	}
	if err := validateTrialCodes(participant); err != nil {
		return err
	}
	if participant.Status == TrialParticipantCompleted && len(participant.Comparisons) == 0 {
		return errors.New("completed participant requires a comparison")
	}
	for i, comparison := range participant.Comparisons {
		if err := validateTrialComparison(comparison); err != nil {
			return fmt.Errorf("comparison %d: %w", i, err)
		}
	}
	return nil
}

func validateTrialConsentChronology(consent TrialConsent, generatedAt string) error {
	recordedAt, err := time.Parse(time.RFC3339Nano, consent.RecordedAt)
	if err != nil {
		return errors.New("consent timestamp is invalid")
	}
	exportAt, err := time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil {
		return errors.New("export timestamp is invalid")
	}
	if recordedAt.After(exportAt) {
		return errors.New("consent must not occur after export")
	}
	if consent.PrivateQueryConsentAt != "" {
		privateQueryAt, err := time.Parse(time.RFC3339Nano, consent.PrivateQueryConsentAt)
		if err != nil {
			return errors.New("private-query consent timestamp is invalid")
		}
		if privateQueryAt.Before(recordedAt) || privateQueryAt.After(exportAt) {
			return errors.New("private-query consent must occur between trial consent and export")
		}
	}
	var withdrawnAt time.Time
	if consent.WithdrawnAt != "" {
		withdrawnAt, err = time.Parse(time.RFC3339Nano, consent.WithdrawnAt)
		if err != nil {
			return errors.New("withdrawal timestamp is invalid")
		}
		if withdrawnAt.Before(recordedAt) || withdrawnAt.After(exportAt) {
			return errors.New("withdrawal must occur between trial consent and export")
		}
	}
	if consent.WithdrawnDataDeletedAt != "" {
		deletedAt, err := time.Parse(time.RFC3339Nano, consent.WithdrawnDataDeletedAt)
		if err != nil {
			return errors.New("withdrawal deletion timestamp is invalid")
		}
		if withdrawnAt.IsZero() || deletedAt.Before(withdrawnAt) || deletedAt.After(exportAt) {
			return errors.New("withdrawn data deletion must occur between withdrawal and export")
		}
	}
	return nil
}

func validateTrialEnvironment(environment TrialEnvironment) error {
	if environment.OSFamily != TrialOSLinux && environment.OSFamily != TrialOSWSL && environment.OSFamily != TrialOSOther {
		return errors.New("OS category is invalid")
	}
	if len(environment.Harnesses) == 0 || len(environment.ModelFamilies) == 0 {
		return errors.New("harness and model-family categories are required")
	}
	if hasDuplicateTrialValues(environment.Harnesses) || hasDuplicateTrialValues(environment.ModelFamilies) {
		return errors.New("environment categories must not be duplicated")
	}
	for _, harness := range environment.Harnesses {
		if harness != TrialHarnessClaudeCode && harness != TrialHarnessOpenCode && harness != TrialHarnessOther {
			return errors.New("harness category is invalid")
		}
	}
	for _, model := range environment.ModelFamilies {
		if model != TrialModelAnthropic && model != TrialModelOpenAI && model != TrialModelGoogle && model != TrialModelLocal && model != TrialModelOther {
			return errors.New("model-family category is invalid")
		}
	}
	if environment.RepositoryProfile != TrialRepositorySingle && environment.RepositoryProfile != TrialRepositoryMonorepo && environment.RepositoryProfile != TrialRepositoryMulti && environment.RepositoryProfile != TrialRepositoryOther {
		return errors.New("repository profile is invalid")
	}
	return nil
}

func validateTrialCodes(participant TrialParticipant) error {
	if hasDuplicateTrialValues(participant.SupportInterventions) || hasDuplicateTrialValues(participant.Failures) ||
		hasDuplicateTrialValues(participant.ProcedureOmissions) || hasDuplicateTrialValues(participant.PrivateQueryCategories) {
		return errors.New("participant observation codes must not be duplicated")
	}
	for _, code := range participant.SupportInterventions {
		switch code {
		case TrialSupportInstallation, TrialSupportEnrolment, TrialSupportPermissions, TrialSupportGuidance, TrialSupportCodeGraph, TrialSupportSyncOutage, TrialSupportTaskWorkflow, TrialSupportMeasurement, TrialSupportPrivacy:
		default:
			return errors.New("support intervention code is invalid")
		}
	}
	for _, code := range participant.Failures {
		switch code {
		case TrialFailureInstallation, TrialFailureEnrolment, TrialFailureGatewayCall, TrialFailurePermission, TrialFailureGuidance, TrialFailureCodeGraph, TrialFailureSync, TrialFailureHandoff, TrialFailureTask, TrialFailureValidation, TrialFailureOther:
		default:
			return errors.New("failure code is invalid")
		}
	}
	for _, code := range participant.ProcedureOmissions {
		switch code {
		case TrialProcedureOmissionManifest, TrialProcedureOmissionCodeGraph, TrialProcedureOmissionBenchmark, TrialProcedureOmissionOutage, TrialProcedureOmissionSupport, TrialProcedureOmissionSubmit:
		default:
			return errors.New("procedure omission code is invalid")
		}
	}
	for _, code := range participant.PrivateQueryCategories {
		switch code {
		case TrialQueryCategoryNavigation, TrialQueryCategorySymbolLookup, TrialQueryCategoryRelationship, TrialQueryCategorySourceRequest, TrialQueryCategoryOther:
		default:
			return errors.New("private-query category is invalid")
		}
	}
	return nil
}

func hasDuplicateTrialValues[T comparable](values []T) bool {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validateTrialMetrics(metrics TrialParticipantMetrics) error {
	omissions := make(map[TrialOmissionCode]struct{}, len(metrics.Omissions))
	for _, code := range metrics.Omissions {
		if !validTrialOmissionCode(code) {
			return errors.New("measurement omission code is invalid")
		}
		if _, duplicate := omissions[code]; duplicate {
			return errors.New("measurement omission code is duplicated")
		}
		omissions[code] = struct{}{}
	}
	checks := []struct {
		present bool
		code    TrialOmissionCode
	}{
		{metrics.InstallationCompleted != nil, TrialOmissionInstallationCompleted},
		{metrics.TimeToFirstGatewayMCPCallMS != nil, TrialOmissionTimeToFirstGatewayMCPCall},
		{metrics.TimeToProductiveWorkMS != nil, TrialOmissionTimeToProductiveWork},
		{metrics.ToolSuccessCount != nil, TrialOmissionToolSuccessCount},
		{metrics.ToolDenialCount != nil, TrialOmissionToolDenialCount},
		{metrics.ToolFailureCount != nil, TrialOmissionToolFailureCount},
		{metrics.ContextRetrievedAtSessionStart != nil, TrialOmissionContextAtSessionStart},
		{metrics.HumanCoachingInterventions != nil, TrialOmissionHumanCoaching},
		{metrics.ModelHandoffSucceeded != nil, TrialOmissionModelHandoff},
		{metrics.SyncRecoverySucceeded != nil, TrialOmissionSyncRecovery},
		{metrics.KBRelevantResults != nil, TrialOmissionKBRelevantResults},
		{metrics.KBResultsConsidered != nil, TrialOmissionKBResultsConsidered},
		{metrics.DuplicateOrLowValueKBContributions != nil, TrialOmissionLowValueKBContributions},
		{metrics.CodeGraphUsefulQueries != nil, TrialOmissionCodeGraphUsefulQueries},
		{metrics.CodeGraphQueries != nil, TrialOmissionCodeGraphQueries},
		{metrics.FilesReadBeforeCorrectEdit != nil, TrialOmissionFilesBeforeCorrectEdit},
		{metrics.SourceBytesReadBeforeCorrectEdit != nil, TrialOmissionSourceBytesBeforeCorrectEdit},
		{metrics.EventCount != nil, TrialOmissionEventCount},
		{metrics.EventNoiseCount != nil, TrialOmissionEventNoiseCount},
		{metrics.TaskStateAccurate != nil, TrialOmissionTaskStateAccurate},
		{metrics.ContextReconstructionsAvoided != nil, TrialOmissionContextReconstructionsAvoided},
		{metrics.TokensBeforeProductiveWork != nil, TrialOmissionTokensBeforeProductiveWork},
	}
	for _, check := range checks {
		_, omitted := omissions[check.code]
		if check.present == omitted {
			return fmt.Errorf("measurement %s must have exactly one value or omission", check.code)
		}
	}
	for _, value := range []*int{metrics.ToolSuccessCount, metrics.ToolDenialCount, metrics.ToolFailureCount, metrics.HumanCoachingInterventions, metrics.KBRelevantResults, metrics.KBResultsConsidered, metrics.DuplicateOrLowValueKBContributions, metrics.CodeGraphUsefulQueries, metrics.CodeGraphQueries, metrics.FilesReadBeforeCorrectEdit, metrics.EventCount, metrics.EventNoiseCount, metrics.ContextReconstructionsAvoided} {
		if value != nil && *value < 0 {
			return errors.New("measurement count cannot be negative")
		}
	}
	for _, value := range []*int64{metrics.TimeToFirstGatewayMCPCallMS, metrics.TimeToProductiveWorkMS, metrics.SourceBytesReadBeforeCorrectEdit, metrics.TokensBeforeProductiveWork} {
		if value != nil && *value < 0 {
			return errors.New("measurement value cannot be negative")
		}
	}
	if exceedsTrialTotal(metrics.KBRelevantResults, metrics.KBResultsConsidered) || exceedsTrialTotal(metrics.CodeGraphUsefulQueries, metrics.CodeGraphQueries) || exceedsTrialTotal(metrics.EventNoiseCount, metrics.EventCount) {
		return errors.New("subset count exceeds total")
	}
	return nil
}

func validTrialOmissionCode(code TrialOmissionCode) bool {
	switch code {
	case TrialOmissionInstallationCompleted, TrialOmissionTimeToFirstGatewayMCPCall, TrialOmissionTimeToProductiveWork,
		TrialOmissionToolSuccessCount, TrialOmissionToolDenialCount, TrialOmissionToolFailureCount, TrialOmissionContextAtSessionStart,
		TrialOmissionHumanCoaching, TrialOmissionModelHandoff, TrialOmissionSyncRecovery,
		TrialOmissionKBRelevantResults, TrialOmissionKBResultsConsidered, TrialOmissionLowValueKBContributions,
		TrialOmissionCodeGraphUsefulQueries, TrialOmissionCodeGraphQueries, TrialOmissionFilesBeforeCorrectEdit,
		TrialOmissionSourceBytesBeforeCorrectEdit, TrialOmissionEventCount, TrialOmissionEventNoiseCount,
		TrialOmissionTaskStateAccurate, TrialOmissionContextReconstructionsAvoided, TrialOmissionTokensBeforeProductiveWork:
		return true
	default:
		return false
	}
}

func exceedsTrialTotal(part, total *int) bool {
	return part != nil && total != nil && *part > *total
}

func validateTrialComparison(comparison TrialComparison) error {
	switch comparison.TaskKind {
	case TrialTaskFeature, TrialTaskBugfix, TrialTaskReview, TrialTaskRefactor, TrialTaskDocumentation, TrialTaskOther:
	default:
		return errors.New("task kind is invalid")
	}
	if comparison.BaselineKind != TrialBaselineGuidanceOff && comparison.BaselineKind != TrialBaselineCodeGraphOff {
		return errors.New("baseline kind is invalid")
	}
	if !comparison.SameCheckoutRevision || !comparison.SamePermissions || !comparison.SameSuccessCriteria || !comparison.SameMeasurementMethod {
		return errors.New("comparison controls must match")
	}
	if err := validateTrialComparisonArm(comparison.Baseline); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := validateTrialComparisonArm(comparison.Alpha); err != nil {
		return fmt.Errorf("alpha: %w", err)
	}
	return nil
}

func validateTrialComparisonArm(arm TrialComparisonArm) error {
	if arm.ToolSelection != TrialToolSelectionAppropriate && arm.ToolSelection != TrialToolSelectionMixed && arm.ToolSelection != TrialToolSelectionInappropriate && arm.ToolSelection != TrialToolSelectionMissing {
		return errors.New("tool selection is invalid")
	}
	if arm.OperatingLoopAdherence != TrialLoopComplete && arm.OperatingLoopAdherence != TrialLoopPartial && arm.OperatingLoopAdherence != TrialLoopAbsent && arm.OperatingLoopAdherence != TrialLoopMissing {
		return errors.New("operating-loop adherence is invalid")
	}
	if !validTrialQuality(arm.TaskQuality) || !validTrialQuality(arm.ReviewQuality) {
		return errors.New("quality code is invalid")
	}
	omissions := map[TrialComparisonOmissionCode]struct{}{}
	for _, code := range arm.Omissions {
		if !validTrialComparisonOmission(code) {
			return errors.New("comparison omission code is invalid")
		}
		if _, duplicate := omissions[code]; duplicate {
			return errors.New("comparison omission code is duplicated")
		}
		omissions[code] = struct{}{}
	}
	checks := []struct {
		present bool
		code    TrialComparisonOmissionCode
	}{
		{arm.UsefulSharedStateWrites != nil, TrialComparisonOmissionUsefulWrites},
		{arm.HumanCorrections != nil, TrialComparisonOmissionCorrections},
		{arm.UnnecessaryToolCalls != nil, TrialComparisonOmissionToolCalls},
		{arm.SourceFilesRead != nil, TrialComparisonOmissionSourceFiles},
		{arm.SourceBytesRead != nil, TrialComparisonOmissionSourceBytes},
	}
	for _, check := range checks {
		_, omitted := omissions[check.code]
		if check.present == omitted {
			return fmt.Errorf("comparison metric %s must have exactly one value or omission", check.code)
		}
	}
	for _, value := range []*int{arm.UsefulSharedStateWrites, arm.HumanCorrections, arm.UnnecessaryToolCalls, arm.SourceFilesRead} {
		if value != nil && *value < 0 {
			return errors.New("comparison count cannot be negative")
		}
	}
	if arm.SourceBytesRead != nil && *arm.SourceBytesRead < 0 {
		return errors.New("comparison byte count cannot be negative")
	}
	return nil
}

func validTrialComparisonOmission(code TrialComparisonOmissionCode) bool {
	switch code {
	case TrialComparisonOmissionUsefulWrites, TrialComparisonOmissionCorrections, TrialComparisonOmissionToolCalls, TrialComparisonOmissionSourceFiles, TrialComparisonOmissionSourceBytes:
		return true
	default:
		return false
	}
}

func validTrialQuality(quality TrialQuality) bool {
	return quality == TrialQualityMet || quality == TrialQualityPartial || quality == TrialQualityFailed || quality == TrialQualityMissing
}

func validateTrialGateDDecision(decision TrialGateDDecision) error {
	switch decision.Decision {
	case GateDContinueTowardsBetaPlanning, GateDContinueWithNarrowedScope, GateDRepeatAlphaAfterCorrectiveWork, GateDStopCurrentDirection:
	default:
		return invalidTrial("Gate D decision is invalid")
	}
	ratings := map[TrialGateCriterion]string{
		TrialGateManualContextRelay:     decision.Evaluation.ManualContextRelay,
		TrialGateRepeatedReconstruction: decision.Evaluation.RepeatedProjectReconstruction,
		TrialGateCrossModelContinuation: decision.Evaluation.CrossModelContinuation,
		TrialGateInterruptionRecovery:   decision.Evaluation.InterruptionRecovery,
		TrialGateGuidanceLearnability:   decision.Evaluation.ManagedGuidanceLearnability,
		TrialGateSourceDiscovery:        decision.Evaluation.SourceDiscoveryNarrowing,
		TrialGateMaintenance:            decision.Evaluation.MaintenanceProportionate,
		TrialGateEventNoise:             decision.Evaluation.EventNoiseProportionate,
		TrialGateConfidence:             decision.Evaluation.ConfidenceAppropriate,
	}
	for _, rating := range ratings {
		if rating != TrialEvidenceSupports && rating != TrialEvidenceContrary && rating != TrialEvidenceMissing {
			return invalidTrial("Gate D rating is invalid")
		}
	}
	if len(decision.SupportingEvidence) == 0 || len(decision.ContraryEvidence) == 0 {
		return invalidTrial("Gate D requires supporting and contrary evidence codes")
	}
	if err := validateGateEvidence(decision.SupportingEvidence, ratings, TrialEvidenceSupports); err != nil {
		return err
	}
	return validateGateEvidence(decision.ContraryEvidence, ratings, TrialEvidenceContrary)
}

func validateGateEvidence(codes []TrialGateCriterion, ratings map[TrialGateCriterion]string, want string) error {
	seen := map[TrialGateCriterion]struct{}{}
	for _, code := range codes {
		rating, valid := ratings[code]
		if !valid || rating != want {
			return invalidTrial("Gate D evidence code does not match its rating")
		}
		if _, duplicate := seen[code]; duplicate {
			return invalidTrial("Gate D evidence code is duplicated")
		}
		seen[code] = struct{}{}
	}
	return nil
}

func validTrialSlug(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func canonicalTrialUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validTrialCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validTrialTimestamp(value string) bool {
	if value == "" || len(value) > TrialMetricsMaxStringBytes {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func invalidTrial(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTrialMetricsInvalid, fmt.Sprintf(format, args...))
}

func privacyTrial(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTrialPrivacy, fmt.Sprintf(format, args...))
}
