package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

func TestRunTrialMetricsValidatesStdinAndAggregateFile(t *testing.T) {
	preview := trialMetricsCLIParticipantExport()
	preview.Participant.Consent.ParticipantSubmission = false
	previewData, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runTrialMetrics([]string{"validate", "--kind", "participant-preview"}, bytes.NewReader(previewData), &stdout, &stderr); code != 0 {
		t.Fatalf("preview stdin exit = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "valid\n" {
		t.Fatalf("validation stdout = %q, want content-free confirmation", stdout.String())
	}

	aggregateData, err := localapi.MarshalTrialMetricsExport(trialMetricsCLIExport())
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "aggregate.json")
	if err := os.WriteFile(inputPath, aggregateData, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"validate", "--kind", "aggregate", inputPath}, strings.NewReader("ignored"), &stdout, &stderr); code != 0 {
		t.Fatalf("aggregate file exit = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "valid\n" {
		t.Fatalf("aggregate validation stdout = %q, want content-free confirmation", stdout.String())
	}
}

func TestRunTrialMetricsFormatValidatesBeforeWriting(t *testing.T) {
	preview := trialMetricsCLIParticipantExport()
	preview.Participant.Consent.ParticipantSubmission = false
	compact, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	want, err := localapi.MarshalTrialParticipantPreview(preview)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runTrialMetrics([]string{"format", "--kind", "participant-preview", "-"}, bytes.NewReader(compact), &stdout, &stderr); code != 0 {
		t.Fatalf("format exit = %d, stderr = %q", code, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("formatted output mismatch\ngot:  %s\nwant: %s", stdout.Bytes(), want)
	}

	private := []byte(strings.Replace(string(compact), "{", "{\"private_query_text\":\"participant-secret\",", 1))
	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"format", "--kind", "participant-preview"}, bytes.NewReader(private), &stdout, &stderr); code != 1 {
		t.Fatalf("private format exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("private format wrote participant content: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "participant-secret") {
		t.Fatalf("private format error leaked participant content: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "privacy violation") {
		t.Fatalf("private format stderr = %q, want privacy violation", stderr.String())
	}

	unknown := []byte(strings.Replace(string(compact), "{", "{\"participant-secret-key\":true,", 1))
	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"validate", "--kind", "participant-preview"}, bytes.NewReader(unknown), &stdout, &stderr); code != 1 {
		t.Fatalf("unknown-field validation exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unknown-field validation wrote participant content: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "participant-secret-key") {
		t.Fatalf("unknown-field validation leaked participant content: %q", stderr.String())
	}
}

func TestRunTrialMetricsValidateDoesNotRequireFormattedOutputToFit(t *testing.T) {
	var compact []byte
	for low, high := 3, localapi.TrialMetricsMaxParticipants*localapi.TrialMetricsMaxItems; low <= high; {
		mid := low + (high-low)/2
		export := trialMetricsCLIExportWithComparisons(mid)
		formatted, err := json.MarshalIndent(export, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if len(formatted)+1 > localapi.TrialMetricsMaxJSONBytes {
			compact, err = json.Marshal(export)
			if err != nil {
				t.Fatal(err)
			}
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	if len(compact) == 0 || len(compact) > localapi.TrialMetricsMaxJSONBytes {
		t.Fatalf("fixture compact size = %d, want valid input within limit", len(compact))
	}
	if _, err := localapi.DecodeTrialMetricsExport(compact); err != nil {
		t.Fatalf("fixture must be a valid compact aggregate: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := runTrialMetrics([]string{"validate", "--kind", "aggregate"}, bytes.NewReader(compact), &stdout, &stderr); code != 0 {
		t.Fatalf("valid compact aggregate exit = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"format", "--kind", "aggregate"}, bytes.NewReader(compact), &stdout, &stderr); code != 1 {
		t.Fatalf("oversized formatted aggregate exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("oversized formatted aggregate wrote partial output")
	}
}

func TestRunTrialMetricsPreservesStrictSubmittedValidationAndExitCodes(t *testing.T) {
	preview := trialMetricsCLIParticipantExport()
	preview.Participant.Consent.ParticipantSubmission = false
	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runTrialMetrics([]string{"validate", "--kind", "participant"}, bytes.NewReader(data), &stdout, &stderr); code != 1 {
		t.Fatalf("unsubmitted final export exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("invalid submitted export wrote participant content: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"validate"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("missing kind exit = %d, want 2", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"validate", "--kind", "aggregate", "one", "two"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("extra operand exit = %d, want 2", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrialMetrics([]string{"validate", "--kind", "aggregate", filepath.Join(t.TempDir(), "missing.json")}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("read failure exit = %d, want 1", code)
	}
}

func trialMetricsCLIParticipantExport() localapi.TrialParticipantExport {
	aggregate := trialMetricsCLIExport()
	return localapi.TrialParticipantExport{
		SchemaVersion: aggregate.SchemaVersion, ExportID: "participant-a-local-export",
		ReleaseCandidate: aggregate.ReleaseCandidate, GeneratedAt: aggregate.GeneratedAt,
		Participant: aggregate.Participants[0],
	}
}

func trialMetricsCLIExport() localapi.TrialMetricsExport {
	participants := make([]localapi.TrialParticipant, 3)
	for i := range participants {
		participants[i] = localapi.TrialParticipant{
			ParticipantID: "participant-" + string(rune('a'+i)), External: true, Status: localapi.TrialParticipantCompleted,
			Consent: localapi.TrialConsent{
				Version: localapi.TrialConsentVersion, Collection: true, ParticipantSubmission: true,
				RecordedAt: "2026-07-28T10:00:00Z",
			},
			Environment: &localapi.TrialEnvironment{
				OSFamily: localapi.TrialOSLinux, Harnesses: []localapi.TrialHarness{localapi.TrialHarnessOther},
				ModelFamilies: []localapi.TrialModelFamily{localapi.TrialModelOther}, RepositoryProfile: localapi.TrialRepositorySingle,
			},
			Metrics: &localapi.TrialParticipantMetrics{
				InstallationCompleted: trialMetricsCLIBool(true), TimeToFirstGatewayMCPCallMS: trialMetricsCLIInt64(1200),
				TimeToProductiveWorkMS: trialMetricsCLIInt64(5000), ToolSuccessCount: trialMetricsCLIInt(10),
				ToolDenialCount: trialMetricsCLIInt(1), ToolFailureCount: trialMetricsCLIInt(2),
				ContextRetrievedAtSessionStart: trialMetricsCLIBool(true), HumanCoachingInterventions: trialMetricsCLIInt(1),
				ModelHandoffSucceeded: trialMetricsCLIBool(true), SyncRecoverySucceeded: trialMetricsCLIBool(true),
				KBRelevantResults: trialMetricsCLIInt(3), KBResultsConsidered: trialMetricsCLIInt(4),
				DuplicateOrLowValueKBContributions: trialMetricsCLIInt(0), CodeGraphUsefulQueries: trialMetricsCLIInt(2),
				CodeGraphQueries: trialMetricsCLIInt(3), FilesReadBeforeCorrectEdit: trialMetricsCLIInt(5),
				SourceBytesReadBeforeCorrectEdit: trialMetricsCLIInt64(4096), EventCount: trialMetricsCLIInt(8),
				EventNoiseCount: trialMetricsCLIInt(1), TaskStateAccurate: trialMetricsCLIBool(true),
				ContextReconstructionsAvoided: trialMetricsCLIInt(1), TokensBeforeProductiveWork: trialMetricsCLIInt64(2400),
			},
			Comparisons: []localapi.TrialComparison{{
				TaskKind: localapi.TrialTaskFeature, BaselineKind: localapi.TrialBaselineGuidanceOff,
				SameCheckoutRevision: true, SamePermissions: true, SameSuccessCriteria: true, SameMeasurementMethod: true,
				Baseline: trialMetricsCLIComparisonArm(), Alpha: trialMetricsCLIComparisonArm(),
			}},
		}
	}
	return localapi.TrialMetricsExport{
		SchemaVersion: localapi.TrialMetricsSchemaVersion, ExportID: "closed-alpha-test",
		ReleaseCandidate: strings.Repeat("a", 40), GeneratedAt: "2026-07-28T12:00:00Z", Participants: participants,
		GateDDecisions: []localapi.TrialGateDDecision{{
			Decision: localapi.GateDContinueTowardsBetaPlanning,
			Evaluation: localapi.TrialGateDEvaluation{
				ManualContextRelay: localapi.TrialEvidenceSupports, RepeatedProjectReconstruction: localapi.TrialEvidenceSupports,
				CrossModelContinuation: localapi.TrialEvidenceSupports, InterruptionRecovery: localapi.TrialEvidenceSupports,
				ManagedGuidanceLearnability: localapi.TrialEvidenceSupports, SourceDiscoveryNarrowing: localapi.TrialEvidenceSupports,
				MaintenanceProportionate: localapi.TrialEvidenceSupports, EventNoiseProportionate: localapi.TrialEvidenceContrary,
				ConfidenceAppropriate: localapi.TrialEvidenceSupports,
			},
			SupportingEvidence: []localapi.TrialGateCriterion{localapi.TrialGateManualContextRelay},
			ContraryEvidence:   []localapi.TrialGateCriterion{localapi.TrialGateEventNoise},
		}},
	}
}

func trialMetricsCLIExportWithComparisons(total int) localapi.TrialMetricsExport {
	export := trialMetricsCLIExport()
	base := export.Participants[0]
	participantCount := (total + localapi.TrialMetricsMaxItems - 1) / localapi.TrialMetricsMaxItems
	if participantCount < 3 {
		participantCount = 3
	}
	export.Participants = make([]localapi.TrialParticipant, participantCount)
	remaining := total
	for i := range export.Participants {
		participant := base
		participant.ParticipantID = "participant-" + strings.Repeat("x", i+1)
		comparisonsLeft := participantCount - i - 1
		comparisonCount := remaining - comparisonsLeft
		if comparisonCount > localapi.TrialMetricsMaxItems {
			comparisonCount = localapi.TrialMetricsMaxItems
		}
		participant.Comparisons = make([]localapi.TrialComparison, comparisonCount)
		for j := range participant.Comparisons {
			participant.Comparisons[j] = base.Comparisons[0]
		}
		export.Participants[i] = participant
		remaining -= comparisonCount
	}
	return export
}

func trialMetricsCLIComparisonArm() localapi.TrialComparisonArm {
	return localapi.TrialComparisonArm{
		ToolSelection: localapi.TrialToolSelectionAppropriate, OperatingLoopAdherence: localapi.TrialLoopComplete,
		UsefulSharedStateWrites: trialMetricsCLIInt(1), HumanCorrections: trialMetricsCLIInt(1),
		TaskQuality: localapi.TrialQualityMet, UnnecessaryToolCalls: trialMetricsCLIInt(1),
		SourceFilesRead: trialMetricsCLIInt(5), SourceBytesRead: trialMetricsCLIInt64(4096), ReviewQuality: localapi.TrialQualityMet,
	}
}

func trialMetricsCLIBool(value bool) *bool    { return &value }
func trialMetricsCLIInt(value int) *int       { return &value }
func trialMetricsCLIInt64(value int64) *int64 { return &value }
