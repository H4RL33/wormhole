package types

import (
	"errors"
	"testing"
	"time"
)

const (
	testHumanID = "11111111-1111-4111-8111-111111111111"
	testAgentID = "22222222-2222-4222-8222-222222222222"
)

func testActorTime() time.Time {
	return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
}

func TestActorEnvelopeValidateHuman(t *testing.T) {
	envelope := ActorEnvelope{ActorKind: ActorHuman, HumanPrincipalID: testHumanID, Assurance: AssuranceLocal, OccurredAt: testActorTime()}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	invalid := envelope
	invalid.AgentID = testAgentID
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidActorEnvelope) {
		t.Fatalf("Validate error = %v, want ErrInvalidActorEnvelope", err)
	}
}

func TestActorEnvelopeValidateLocalAgentRequiresAccountabilitySessionHarness(t *testing.T) {
	envelope := ActorEnvelope{
		ActorKind: ActorAgent, AgentID: testAgentID, AccountableHumanID: testHumanID,
		SessionID: "session-1", HarnessName: "codex", HarnessVersion: "1.0",
		ModelName: "gpt", ModelVersion: "5", Assurance: AssuranceLocal, OccurredAt: testActorTime(),
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ActorEnvelope)
	}{
		{"accountable human", func(e *ActorEnvelope) { e.AccountableHumanID = "" }},
		{"session", func(e *ActorEnvelope) { e.SessionID = "" }},
		{"harness name", func(e *ActorEnvelope) { e.HarnessName = "" }},
		{"harness version", func(e *ActorEnvelope) { e.HarnessVersion = "" }},
		{"model pair", func(e *ActorEnvelope) { e.ModelVersion = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := envelope
			test.mutate(&invalid)
			if err := invalid.Validate(); !errors.Is(err, ErrInvalidActorEnvelope) {
				t.Fatalf("Validate error = %v, want ErrInvalidActorEnvelope", err)
			}
		})
	}
}

func TestActorEnvelopeValidatePublicAndPrivateAgents(t *testing.T) {
	for _, assurance := range []Assurance{AssurancePublicKeyContinuity, AssurancePrivateAuthenticated} {
		envelope := ActorEnvelope{
			ActorKind: ActorAgent, AgentID: testAgentID, AccountableHumanID: testHumanID,
			SessionID: "session-1", HarnessName: "codex", HarnessVersion: "1.0",
			Assurance: assurance, OccurredAt: testActorTime(),
		}
		if err := envelope.Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", assurance, err)
		}
	}
}

func TestActorEnvelopeValidateHistoricalLegacyAllowsMissingProvenance(t *testing.T) {
	envelope := ActorEnvelope{ActorKind: ActorAgent, AgentID: testAgentID, Assurance: AssuranceLegacy, OccurredAt: testActorTime()}
	if err := envelope.ValidateHistorical(); err != nil {
		t.Fatalf("ValidateHistorical: %v", err)
	}
	envelope.AccountableHumanID = "not-a-canonical-id"
	if err := envelope.ValidateHistorical(); !errors.Is(err, ErrInvalidActorEnvelope) {
		t.Fatalf("ValidateHistorical invalid accountable ID error = %v, want ErrInvalidActorEnvelope", err)
	}
}

func TestActorEnvelopeValidateHistoricalUnknownAllowsMissingProvenance(t *testing.T) {
	envelope := ActorEnvelope{ActorKind: ActorAgent, AgentID: testAgentID, Assurance: AssuranceUnknown, OccurredAt: testActorTime()}
	if err := envelope.ValidateHistorical(); err != nil {
		t.Fatalf("ValidateHistorical: %v", err)
	}
}

func TestActorEnvelopeValidateLocalActionRejectsLegacyUnknownPublicPrivate(t *testing.T) {
	for _, assurance := range []Assurance{AssuranceLegacy, AssuranceUnknown, AssurancePublicKeyContinuity, AssurancePrivateAuthenticated} {
		envelope := ActorEnvelope{
			ActorKind: ActorAgent, AgentID: testAgentID, AccountableHumanID: testHumanID,
			SessionID: "session-1", HarnessName: "codex", HarnessVersion: "1.0",
			Assurance: assurance, OccurredAt: testActorTime(),
		}
		if err := envelope.ValidateLocalAction(); !errors.Is(err, ErrInvalidActorEnvelope) {
			t.Fatalf("ValidateLocalAction(%q) error = %v, want ErrInvalidActorEnvelope", assurance, err)
		}
	}
}

func TestActorEnvelopePrincipalID(t *testing.T) {
	if got := (ActorEnvelope{ActorKind: ActorHuman, HumanPrincipalID: testHumanID}).PrincipalID(); got != testHumanID {
		t.Fatalf("human PrincipalID = %q", got)
	}
	if got := (ActorEnvelope{ActorKind: ActorAgent, AgentID: testAgentID}).PrincipalID(); got != testAgentID {
		t.Fatalf("agent PrincipalID = %q", got)
	}
}

func TestActorEnvelopeHistoricalNeverUpgrades(t *testing.T) {
	envelope := ActorEnvelope{ActorKind: ActorAgent, AgentID: testAgentID, Assurance: AssuranceLegacy, OccurredAt: testActorTime()}
	if err := envelope.ValidateHistorical(); err != nil {
		t.Fatal(err)
	}
	if envelope.Assurance != AssuranceLegacy || envelope.AccountableHumanID != "" {
		t.Fatalf("historical validation mutated envelope: %+v", envelope)
	}
}

func TestActorEnvelopeRejectsUnknownKindAssuranceAndNonUTC(t *testing.T) {
	valid := ActorEnvelope{ActorKind: ActorHuman, HumanPrincipalID: testHumanID, Assurance: AssuranceLocal, OccurredAt: testActorTime()}
	if err := valid.ValidateLocalAction(); err != nil {
		t.Fatalf("ValidateLocalAction: %v", err)
	}
	for _, invalid := range []ActorEnvelope{
		{ActorKind: "service", HumanPrincipalID: testHumanID, Assurance: AssuranceLocal, OccurredAt: testActorTime()},
		{ActorKind: ActorHuman, HumanPrincipalID: testHumanID, Assurance: "future", OccurredAt: testActorTime()},
		{ActorKind: ActorHuman, HumanPrincipalID: testHumanID, Assurance: AssuranceLocal, OccurredAt: time.Date(2026, 7, 28, 13, 0, 0, 0, time.FixedZone("plus-one", 3600))},
	} {
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidActorEnvelope) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidActorEnvelope", invalid, err)
		}
	}
	if got := (ActorEnvelope{ActorKind: "service"}).PrincipalID(); got != "" {
		t.Fatalf("unknown-kind PrincipalID = %q, want empty", got)
	}
}

func TestValidCandidateImportOriginAcceptsExactUnion(t *testing.T) {
	for _, value := range []string{
		testHumanID,
		CandidateImportOriginGitObservationRebaseV1,
	} {
		if !ValidCandidateImportOrigin(value) {
			t.Errorf("ValidCandidateImportOrigin(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"",
		"SYSTEM:git-observation-rebase-v1",
		"system:git-observation-rebase-v1 ",
		"system:git-observation-rebase-v2",
	} {
		if ValidCandidateImportOrigin(value) {
			t.Errorf("ValidCandidateImportOrigin(%q) = true, want false", value)
		}
	}
	if CanonicalUUID(CandidateImportOriginGitObservationRebaseV1) {
		t.Fatal("fixed candidate import origin unexpectedly broadened CanonicalUUID")
	}
}
