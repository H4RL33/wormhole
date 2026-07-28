package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTrialMetricsMarshalProducesStrictStructuredExport(t *testing.T) {
	export := validTrialMetricsExport()
	data, err := MarshalTrialMetricsExport(export)
	if err != nil {
		t.Fatalf("MarshalTrialMetricsExport: %v", err)
	}
	for _, key := range []string{
		`"installation_completed"`, `"time_to_first_gateway_mcp_call_ms"`, `"time_to_productive_work_ms"`,
		`"tool_success_count"`, `"tool_denial_count"`, `"context_retrieved_at_session_start"`,
		`"human_coaching_interventions"`, `"model_handoff_succeeded"`, `"sync_recovery_succeeded"`,
		`"kb_relevant_results"`, `"kb_results_considered"`, `"duplicate_or_low_value_kb_contributions"`,
		`"code_graph_useful_queries"`, `"code_graph_queries"`, `"files_read_before_correct_edit"`,
		`"source_bytes_read_before_correct_edit"`, `"event_count"`, `"event_noise_count"`,
		`"task_state_accurate"`, `"context_reconstructions_avoided"`, `"tokens_before_productive_work"`,
	} {
		if !bytes.Contains(data, []byte(key)) {
			t.Errorf("structured export missing metric key %s", key)
		}
	}
	for _, excluded := range []string{"private_query_text", "supporting evidence text", "package main"} {
		if bytes.Contains(data, []byte(excluded)) {
			t.Fatalf("structured export included excluded text %q", excluded)
		}
	}
	if _, err := DecodeTrialMetricsExport(data); err != nil {
		t.Fatalf("DecodeTrialMetricsExport: %v", err)
	}
}

func TestTrialMetricsParticipantExportPrecedesAggregateDecision(t *testing.T) {
	aggregate := validTrialMetricsExport()
	export := TrialParticipantExport{
		SchemaVersion: TrialMetricsSchemaVersion, ExportID: "participant-a-local-export",
		ReleaseCandidate: aggregate.ReleaseCandidate, GeneratedAt: aggregate.GeneratedAt,
		Participant: aggregate.Participants[0],
	}
	data, err := MarshalTrialParticipantExport(export)
	if err != nil {
		t.Fatalf("MarshalTrialParticipantExport: %v", err)
	}
	if bytes.Contains(data, []byte("gate_d_decisions")) {
		t.Fatalf("participant export included aggregate decision: %s", data)
	}
	if _, err := DecodeTrialParticipantExport(data); err != nil {
		t.Fatalf("DecodeTrialParticipantExport: %v", err)
	}
	export.Participant.Consent.ParticipantSubmission = false
	if err := ValidateTrialParticipantExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("without submission consent error = %v, want ErrTrialMetricsInvalid", err)
	}
}

func TestTrialMetricsRequiresThreeCompletedExternalComparisonsAndOneDecision(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TrialMetricsExport)
	}{
		{"two participants", func(export *TrialMetricsExport) { export.Participants = export.Participants[:2] }},
		{"one internal", func(export *TrialMetricsExport) { export.Participants[2].External = false }},
		{"one incomplete", func(export *TrialMetricsExport) { export.Participants[2].Status = TrialParticipantIncomplete }},
		{"missing comparison", func(export *TrialMetricsExport) { export.Participants[1].Comparisons = nil }},
		{"no decision", func(export *TrialMetricsExport) { export.GateDDecisions = nil }},
		{"two decisions", func(export *TrialMetricsExport) {
			export.GateDDecisions = append(export.GateDDecisions, export.GateDDecisions[0])
		}},
		{"invalid decision", func(export *TrialMetricsExport) { export.GateDDecisions[0].Decision = "keep everything" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			export := validTrialMetricsExport()
			test.mutate(&export)
			if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
				t.Fatalf("error = %v, want ErrTrialMetricsInvalid", err)
			}
		})
	}
}

func TestTrialPrivacyRejectsArbitraryTextInSanctionedFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TrialMetricsExport)
	}{
		{"environment", func(export *TrialMetricsExport) {
			export.Participants[0].Environment.RepositoryProfile = "private/repository/path"
		}},
		{"support", func(export *TrialMetricsExport) {
			export.Participants[0].SupportInterventions = []TrialSupportCode{"package main"}
		}},
		{"failure", func(export *TrialMetricsExport) {
			export.Participants[0].Failures = []TrialFailureCode{"select * from private_table"}
		}},
		{"procedure omission", func(export *TrialMetricsExport) {
			export.Participants[0].ProcedureOmissions = []TrialProcedureOmissionCode{"private query text"}
		}},
		{"comparison task", func(export *TrialMetricsExport) {
			export.Participants[0].Comparisons[0].TaskKind = "secret issue title"
		}},
		{"comparison assessment", func(export *TrialMetricsExport) {
			export.Participants[0].Comparisons[0].Alpha.TaskQuality = "source excerpt"
		}},
		{"gate evidence", func(export *TrialMetricsExport) {
			export.GateDDecisions[0].SupportingEvidence = []TrialGateCriterion{"repository content"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			export := validTrialMetricsExport()
			test.mutate(&export)
			if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
				t.Fatalf("error = %v, want ErrTrialMetricsInvalid", err)
			}
		})
	}
}

func TestTrialPrivacyRejectsExcludedKeysAndCredentialValues(t *testing.T) {
	data, err := MarshalTrialMetricsExport(validTrialMetricsExport())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"source body":        addTrialJSONField(data, `"source_body":"package secret"`),
		"bearer token":       addTrialJSONField(data, `"token":"`+"Bearer "+`secret-value"`),
		"private query":      addTrialJSONField(data, `"private_query_text":"where is secret"`),
		"repository content": addTrialJSONField(data, `"repository_content":"unrelated"`),
		"project id":         addTrialJSONField(data, `"project_id":"project-b"`),
		"basic auth":         []byte(strings.Replace(string(data), `"export_id": "closed-alpha-test"`, `"export_id": "`+"Basic "+`QWxhZGRpbjpvcGVuIHNlc2FtZQ=="`, 1)),
		"github pat":         []byte(strings.Replace(string(data), `"export_id": "closed-alpha-test"`, `"export_id": "`+"ghp_"+`1234567890abcdefghijklmnop"`, 1)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTrialMetricsExport(raw); !errors.Is(err, ErrTrialPrivacy) {
				t.Fatalf("error = %v, want ErrTrialPrivacy", err)
			}
		})
	}
}

func TestTrialPrivacyUsesQueryCategoriesAndSeparateConsentTimestamp(t *testing.T) {
	export := validTrialMetricsExport()
	export.Participants[0].PrivateQueryCategories = []TrialQueryCategory{TrialQueryCategorySymbolLookup}
	export.Participants[0].Consent.PrivateQueryCollection = true
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("missing separate timestamp error = %v, want ErrTrialMetricsInvalid", err)
	}
	export.Participants[0].Consent.PrivateQueryConsentAt = "2026-07-28T10:01:00Z"
	if err := ValidateTrialMetricsExport(export); err != nil {
		t.Fatalf("separately consented query category: %v", err)
	}
	export.Participants[0].PrivateQueryCategories[0] = "actual private query"
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("arbitrary query error = %v, want ErrTrialMetricsInvalid", err)
	}
}

func TestTrialPrivacyWithdrawnReceiptIsMinimalAndUnlinked(t *testing.T) {
	export := validTrialMetricsExport()
	withdrawn := export.Participants[0]
	withdrawn.Status = TrialParticipantWithdrawn
	withdrawn.Consent.WithdrawnAt = "2026-07-28T11:00:00Z"
	withdrawn.Consent.WithdrawnDataDeletedAt = "2026-07-28T11:30:00Z"
	export.Participants = append(export.Participants, withdrawn)
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialPrivacy) {
		t.Fatalf("linked full withdrawal error = %v, want ErrTrialPrivacy", err)
	}

	export.Participants[3] = TrialParticipant{
		Status: TrialParticipantWithdrawn,
		Consent: TrialConsent{
			Version: TrialConsentVersion, Collection: true, ParticipantSubmission: true,
			RecordedAt: "2026-07-28T10:00:00Z", WithdrawnAt: "2026-07-28T11:00:00Z",
			WithdrawnDataDeletedAt: "2026-07-28T11:30:00Z",
		},
	}
	if err := ValidateTrialMetricsExport(export); err != nil {
		t.Fatalf("minimal unlinked withdrawal: %v", err)
	}
}

func TestTrialMetricsNullCountsRequireTypedOmissionCodes(t *testing.T) {
	export := validTrialMetricsExport()
	export.Participants[0].Metrics.ToolSuccessCount = nil
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("missing omission error = %v, want ErrTrialMetricsInvalid", err)
	}
	export.Participants[0].Metrics.Omissions = append(export.Participants[0].Metrics.Omissions, TrialOmissionToolSuccessCount)
	if err := ValidateTrialMetricsExport(export); err != nil {
		t.Fatalf("null count with omission: %v", err)
	}
	export.Participants[0].Metrics.ToolSuccessCount = trialIntPointer(1)
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("contradictory omission error = %v, want ErrTrialMetricsInvalid", err)
	}
}

func TestTrialConsentTimestampsAreChronological(t *testing.T) {
	base := validTrialMetricsExport()
	tests := []struct {
		name   string
		mutate func(*TrialParticipantExport)
	}{
		{"consent after export", func(export *TrialParticipantExport) {
			export.Participant.Consent.RecordedAt = "2026-07-28T13:00:00Z"
		}},
		{"private query consent before trial consent", func(export *TrialParticipantExport) {
			export.Participant.Consent.PrivateQueryCollection = true
			export.Participant.Consent.PrivateQueryConsentAt = "2026-07-28T09:59:59Z"
			export.Participant.PrivateQueryCategories = []TrialQueryCategory{TrialQueryCategorySymbolLookup}
		}},
		{"private query consent after export", func(export *TrialParticipantExport) {
			export.Participant.Consent.PrivateQueryCollection = true
			export.Participant.Consent.PrivateQueryConsentAt = "2026-07-28T13:00:00Z"
			export.Participant.PrivateQueryCategories = []TrialQueryCategory{TrialQueryCategorySymbolLookup}
		}},
		{"withdrawal before consent", func(export *TrialParticipantExport) {
			export.Participant = validTrialWithdrawalReceipt()
			export.Participant.Consent.WithdrawnAt = "2026-07-28T09:59:59Z"
		}},
		{"deletion before withdrawal", func(export *TrialParticipantExport) {
			export.Participant = validTrialWithdrawalReceipt()
			export.Participant.Consent.WithdrawnDataDeletedAt = "2026-07-28T10:59:59Z"
		}},
		{"deletion after export", func(export *TrialParticipantExport) {
			export.Participant = validTrialWithdrawalReceipt()
			export.Participant.Consent.WithdrawnDataDeletedAt = "2026-07-28T13:00:00Z"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			export := TrialParticipantExport{
				SchemaVersion: TrialMetricsSchemaVersion, ExportID: "participant-local-export",
				ReleaseCandidate: base.ReleaseCandidate, GeneratedAt: base.GeneratedAt,
				Participant: base.Participants[0],
			}
			test.mutate(&export)
			if err := ValidateTrialParticipantExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
				t.Fatalf("error = %v, want ErrTrialMetricsInvalid", err)
			}
		})
	}
}

func TestTrialMetricsRejectsDuplicateSetCodes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TrialMetricsExport)
	}{
		{"harness", func(export *TrialMetricsExport) {
			export.Participants[0].Environment.Harnesses = []TrialHarness{TrialHarnessOpenCode, TrialHarnessOpenCode}
		}},
		{"model family", func(export *TrialMetricsExport) {
			export.Participants[0].Environment.ModelFamilies = []TrialModelFamily{TrialModelOpenAI, TrialModelOpenAI}
		}},
		{"support intervention", func(export *TrialMetricsExport) {
			export.Participants[0].SupportInterventions = []TrialSupportCode{TrialSupportGuidance, TrialSupportGuidance}
		}},
		{"failure", func(export *TrialMetricsExport) {
			export.Participants[0].Failures = []TrialFailureCode{TrialFailureSync, TrialFailureSync}
		}},
		{"procedure omission", func(export *TrialMetricsExport) {
			export.Participants[0].ProcedureOmissions = []TrialProcedureOmissionCode{TrialProcedureOmissionOutage, TrialProcedureOmissionOutage}
		}},
		{"private query category", func(export *TrialMetricsExport) {
			export.Participants[0].Consent.PrivateQueryCollection = true
			export.Participants[0].Consent.PrivateQueryConsentAt = "2026-07-28T10:01:00Z"
			export.Participants[0].PrivateQueryCategories = []TrialQueryCategory{TrialQueryCategorySymbolLookup, TrialQueryCategorySymbolLookup}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			export := validTrialMetricsExport()
			test.mutate(&export)
			if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
				t.Fatalf("error = %v, want ErrTrialMetricsInvalid", err)
			}
		})
	}
}

func TestTrialMetricsRejectsDuplicateJSONKeys(t *testing.T) {
	data, err := MarshalTrialMetricsExport(validTrialMetricsExport())
	if err != nil {
		t.Fatal(err)
	}
	duplicate := []byte(strings.Replace(string(data), `"collection": true`, `"collection": true, "collection": false`, 1))
	if _, err := DecodeTrialMetricsExport(duplicate); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("duplicate key error = %v, want ErrTrialMetricsInvalid", err)
	}
}

func TestTrialMetricsRejectsOversizedInputCollectionsAndStrings(t *testing.T) {
	if _, err := DecodeTrialMetricsExport(bytes.Repeat([]byte("x"), TrialMetricsMaxJSONBytes+1)); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("oversized input error = %v, want ErrTrialMetricsInvalid", err)
	}

	export := validTrialMetricsExport()
	export.ExportID = strings.Repeat("a", TrialMetricsMaxStringBytes+1)
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("oversized string error = %v, want ErrTrialMetricsInvalid", err)
	}

	export = validTrialMetricsExport()
	for len(export.Participants) <= TrialMetricsMaxParticipants {
		participant := export.Participants[0]
		participant.ParticipantID = "participant-" + strings.Repeat("x", len(export.Participants))
		export.Participants = append(export.Participants, participant)
	}
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("oversized participant collection error = %v, want ErrTrialMetricsInvalid", err)
	}

	export = validTrialMetricsExport()
	export.Participants[0].SupportInterventions = make([]TrialSupportCode, TrialMetricsMaxItems+1)
	for i := range export.Participants[0].SupportInterventions {
		export.Participants[0].SupportInterventions[i] = TrialSupportInstallation
	}
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("oversized slice error = %v, want ErrTrialMetricsInvalid", err)
	}
}

func TestTrialPrivacyRejectsUUIDShapedEnvelopeAndParticipantIDs(t *testing.T) {
	const crossProjectID = "550e8400-e29b-41d4-a716-446655440000"

	export := validTrialMetricsExport()
	export.ExportID = crossProjectID
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialPrivacy) {
		t.Fatalf("UUID-shaped export ID error = %v, want ErrTrialPrivacy", err)
	}

	export = validTrialMetricsExport()
	export.Participants[0].ParticipantID = crossProjectID
	if err := ValidateTrialMetricsExport(export); !errors.Is(err, ErrTrialPrivacy) {
		t.Fatalf("UUID-shaped participant ID error = %v, want ErrTrialPrivacy", err)
	}
}

func TestTrialMetricsRejectsExcessiveJSONNesting(t *testing.T) {
	const excessiveDepth = 65
	raw := []byte(strings.Repeat("[", excessiveDepth) + strings.Repeat("]", excessiveDepth))
	err := rejectDuplicateTrialJSONKeys(raw)
	if !errors.Is(err, ErrTrialMetricsInvalid) || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("nesting error = %v, want depth-limited ErrTrialMetricsInvalid", err)
	}
}

func TestTrialMetricsMarshalRejectsFormattedOutputOverLimit(t *testing.T) {
	var candidate TrialMetricsExport
	found := false
	for low, high := 3, TrialMetricsMaxParticipants*TrialMetricsMaxItems; low <= high; {
		mid := low + (high-low)/2
		export := trialMetricsExportWithComparisons(mid)
		formatted, err := json.MarshalIndent(export, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if len(formatted)+1 > TrialMetricsMaxJSONBytes {
			candidate = export
			found = true
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	if !found {
		t.Fatal("fixture did not produce formatted JSON over the limit")
	}
	compact, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) > TrialMetricsMaxJSONBytes {
		t.Fatalf("fixture compact JSON = %d bytes, want at most %d", len(compact), TrialMetricsMaxJSONBytes)
	}
	if _, err := MarshalTrialMetricsExport(candidate); !errors.Is(err, ErrTrialMetricsInvalid) {
		t.Fatalf("formatted output error = %v, want ErrTrialMetricsInvalid", err)
	}
}

func trialMetricsExportWithComparisons(total int) TrialMetricsExport {
	export := validTrialMetricsExport()
	base := export.Participants[0]
	participantCount := (total + TrialMetricsMaxItems - 1) / TrialMetricsMaxItems
	if participantCount < 3 {
		participantCount = 3
	}
	export.Participants = make([]TrialParticipant, participantCount)
	remaining := total
	for i := range export.Participants {
		participant := base
		participant.ParticipantID = fmt.Sprintf("participant-%03d", i)
		comparisonsLeft := participantCount - i - 1
		comparisonCount := remaining - comparisonsLeft
		if comparisonCount > TrialMetricsMaxItems {
			comparisonCount = TrialMetricsMaxItems
		}
		participant.Comparisons = make([]TrialComparison, comparisonCount)
		for j := range participant.Comparisons {
			participant.Comparisons[j] = base.Comparisons[0]
		}
		export.Participants[i] = participant
		remaining -= comparisonCount
	}
	return export
}

func validTrialWithdrawalReceipt() TrialParticipant {
	return TrialParticipant{
		Status: TrialParticipantWithdrawn,
		Consent: TrialConsent{
			Version: TrialConsentVersion, Collection: true, ParticipantSubmission: true,
			RecordedAt: "2026-07-28T10:00:00Z", WithdrawnAt: "2026-07-28T11:00:00Z",
			WithdrawnDataDeletedAt: "2026-07-28T11:30:00Z",
		},
	}
}

func validTrialMetricsExport() TrialMetricsExport {
	// Synthetic validator input only; never copy these values into trial evidence.
	participants := make([]TrialParticipant, 3)
	for i := range participants {
		participants[i] = TrialParticipant{
			ParticipantID: "participant-" + string(rune('a'+i)), External: true, Status: TrialParticipantCompleted,
			Consent: TrialConsent{
				Version: TrialConsentVersion, Collection: true, ParticipantSubmission: true,
				RecordedAt: "2026-07-28T10:00:00Z",
			},
			Environment: &TrialEnvironment{
				OSFamily: TrialOSLinux, Harnesses: []TrialHarness{TrialHarnessOther},
				ModelFamilies: []TrialModelFamily{TrialModelOther}, RepositoryProfile: TrialRepositorySingle,
			},
			Metrics: &TrialParticipantMetrics{
				InstallationCompleted: trialBoolPointer(true), TimeToFirstGatewayMCPCallMS: trialInt64Pointer(1200),
				TimeToProductiveWorkMS: trialInt64Pointer(5000), ToolSuccessCount: trialIntPointer(10),
				ToolDenialCount: trialIntPointer(1), ContextRetrievedAtSessionStart: trialBoolPointer(true),
				HumanCoachingInterventions: trialIntPointer(1), ModelHandoffSucceeded: trialBoolPointer(true),
				SyncRecoverySucceeded: trialBoolPointer(true), KBRelevantResults: trialIntPointer(3),
				KBResultsConsidered: trialIntPointer(4), DuplicateOrLowValueKBContributions: trialIntPointer(0),
				CodeGraphUsefulQueries: trialIntPointer(2), CodeGraphQueries: trialIntPointer(3),
				FilesReadBeforeCorrectEdit: trialIntPointer(5), SourceBytesReadBeforeCorrectEdit: trialInt64Pointer(4096),
				EventCount: trialIntPointer(8), EventNoiseCount: trialIntPointer(1), TaskStateAccurate: trialBoolPointer(true),
				ContextReconstructionsAvoided: trialIntPointer(1), TokensBeforeProductiveWork: trialInt64Pointer(2400),
			},
			Comparisons: []TrialComparison{{
				TaskKind: TrialTaskFeature, BaselineKind: TrialBaselineGuidanceOff,
				SameCheckoutRevision: true, SamePermissions: true, SameSuccessCriteria: true, SameMeasurementMethod: true,
				Baseline: validTrialComparisonArm(), Alpha: validTrialComparisonArm(),
			}},
			SupportInterventions: []TrialSupportCode{}, Failures: []TrialFailureCode{},
			ProcedureOmissions: []TrialProcedureOmissionCode{}, PrivateQueryCategories: []TrialQueryCategory{},
		}
	}
	return TrialMetricsExport{
		SchemaVersion: TrialMetricsSchemaVersion, ExportID: "closed-alpha-test",
		ReleaseCandidate: strings.Repeat("a", 40), GeneratedAt: "2026-07-28T12:00:00Z",
		Participants: participants, GateDDecisions: []TrialGateDDecision{validTrialGateDDecision()},
	}
}

func validTrialComparisonArm() TrialComparisonArm {
	return TrialComparisonArm{
		ToolSelection: TrialToolSelectionAppropriate, OperatingLoopAdherence: TrialLoopComplete,
		UsefulSharedStateWrites: trialIntPointer(1), HumanCorrections: trialIntPointer(1),
		TaskQuality: TrialQualityMet, UnnecessaryToolCalls: trialIntPointer(1),
		SourceFilesRead: trialIntPointer(5), SourceBytesRead: trialInt64Pointer(4096), ReviewQuality: TrialQualityMet,
	}
}

func validTrialGateDDecision() TrialGateDDecision {
	return TrialGateDDecision{
		Decision: GateDContinueTowardsBetaPlanning,
		Evaluation: TrialGateDEvaluation{
			ManualContextRelay: TrialEvidenceSupports, RepeatedProjectReconstruction: TrialEvidenceSupports,
			CrossModelContinuation: TrialEvidenceSupports, InterruptionRecovery: TrialEvidenceSupports,
			ManagedGuidanceLearnability: TrialEvidenceSupports, SourceDiscoveryNarrowing: TrialEvidenceSupports,
			MaintenanceProportionate: TrialEvidenceSupports, EventNoiseProportionate: TrialEvidenceContrary,
			ConfidenceAppropriate: TrialEvidenceSupports,
		},
		SupportingEvidence: []TrialGateCriterion{TrialGateManualContextRelay},
		ContraryEvidence:   []TrialGateCriterion{TrialGateEventNoise},
	}
}

func addTrialJSONField(data []byte, field string) []byte {
	return []byte(strings.Replace(string(data), "{", "{"+field+",", 1))
}

func trialBoolPointer(value bool) *bool    { return &value }
func trialIntPointer(value int) *int       { return &value }
func trialInt64Pointer(value int64) *int64 { return &value }
