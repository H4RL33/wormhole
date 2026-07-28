package types

import (
	"errors"
	"fmt"
	"time"
)

type ActorKind string

const (
	ActorHuman ActorKind = "human"
	ActorAgent ActorKind = "agent"
)

type Assurance string

const (
	AssuranceLocal                Assurance = "local"
	AssuranceLegacy               Assurance = "legacy"
	AssuranceUnknown              Assurance = "unknown"
	AssurancePublicKeyContinuity  Assurance = "public-key-continuity"
	AssurancePrivateAuthenticated Assurance = "private-authenticated"
)

var ErrInvalidActorEnvelope = errors.New("types: invalid actor envelope")

type ActorEnvelope struct {
	ActorKind          ActorKind `json:"actor_kind"`
	HumanPrincipalID   string    `json:"human_principal_id,omitempty"`
	AgentID            string    `json:"agent_id,omitempty"`
	AccountableHumanID string    `json:"accountable_human_id,omitempty"`
	SessionID          string    `json:"session_id,omitempty"`
	HarnessName        string    `json:"harness_name,omitempty"`
	HarnessVersion     string    `json:"harness_version,omitempty"`
	ModelName          string    `json:"model_name,omitempty"`
	ModelVersion       string    `json:"model_version,omitempty"`
	Assurance          Assurance `json:"assurance"`
	OccurredAt         time.Time `json:"occurred_at"`
}

func (e ActorEnvelope) PrincipalID() string {
	switch e.ActorKind {
	case ActorHuman:
		return e.HumanPrincipalID
	case ActorAgent:
		return e.AgentID
	default:
		return ""
	}
}

func (e ActorEnvelope) Validate() error {
	if e.OccurredAt.IsZero() || !isUTC(e.OccurredAt) {
		return fmt.Errorf("%w: occurred_at must be non-zero UTC", ErrInvalidActorEnvelope)
	}
	switch e.Assurance {
	case AssuranceLocal, AssuranceLegacy, AssuranceUnknown, AssurancePublicKeyContinuity, AssurancePrivateAuthenticated:
	default:
		return fmt.Errorf("%w: unknown assurance %q", ErrInvalidActorEnvelope, e.Assurance)
	}
	switch e.ActorKind {
	case ActorHuman:
		if !CanonicalUUID(e.HumanPrincipalID) || e.AgentID != "" || e.AccountableHumanID != "" {
			return fmt.Errorf("%w: invalid human identity fields", ErrInvalidActorEnvelope)
		}
	case ActorAgent:
		if !CanonicalUUID(e.AgentID) || e.HumanPrincipalID != "" {
			return fmt.Errorf("%w: invalid agent identity fields", ErrInvalidActorEnvelope)
		}
		if e.AccountableHumanID != "" && !CanonicalUUID(e.AccountableHumanID) {
			return fmt.Errorf("%w: invalid accountable human ID", ErrInvalidActorEnvelope)
		}
		if (e.ModelName == "") != (e.ModelVersion == "") {
			return fmt.Errorf("%w: model name and version must be supplied together", ErrInvalidActorEnvelope)
		}
		if e.Assurance == AssuranceLocal || e.Assurance == AssurancePublicKeyContinuity || e.Assurance == AssurancePrivateAuthenticated {
			if !CanonicalUUID(e.AccountableHumanID) || e.SessionID == "" || e.HarnessName == "" || e.HarnessVersion == "" {
				return fmt.Errorf("%w: agent provenance is incomplete", ErrInvalidActorEnvelope)
			}
		}
	default:
		return fmt.Errorf("%w: unknown actor kind %q", ErrInvalidActorEnvelope, e.ActorKind)
	}
	return nil
}

func (e ActorEnvelope) ValidateLocalAction() error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Assurance != AssuranceLocal {
		return fmt.Errorf("%w: local action requires local assurance", ErrInvalidActorEnvelope)
	}
	return nil
}

func (e ActorEnvelope) ValidateHistorical() error {
	return e.Validate()
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

// CanonicalUUID reports whether value is a non-nil lower-case UUID string.
// It is kept in the stdlib-only parent package so shared types need no UUID
// dependency.
func CanonicalUUID(value string) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, char := range value {
		switch i {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
				return false
			}
		}
	}
	return true
}
