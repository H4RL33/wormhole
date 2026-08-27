package types

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type ActorKind string

const (
	ActorHuman ActorKind = "human"
	ActorAgent ActorKind = "agent"
)

const CandidateImportOriginGitObservationRebaseV1 = "system:git-observation-rebase-v1"

type Assurance string

const (
	AssuranceLocal                Assurance = "local"
	AssuranceLegacy               Assurance = "legacy"
	AssuranceUnknown              Assurance = "unknown"
	AssurancePublicKeyContinuity  Assurance = "public-key-continuity"
	AssurancePrivateAuthenticated Assurance = "private-authenticated"
)

var ErrInvalidActorEnvelope = errors.New("types: invalid actor envelope")

const (
	MaxConfirmedIdentityDisplayNameBytes = 128
	MaxConfirmedIdentityEmailBytes       = 254
)

var ErrInvalidConfirmedIdentitySelection = errors.New("types: invalid confirmed identity selection")

// ConfirmedIdentitySelection is the exact human-visible identity data approved
// during setup. It is deliberately a small value type so private setup state
// can carry no unbounded Git configuration or credential-shaped values.
type ConfirmedIdentitySelection struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
}

func (s ConfirmedIdentitySelection) Validate() error {
	if !validConfirmedDisplayName(s.DisplayName) {
		return fmt.Errorf("%w: display name", ErrInvalidConfirmedIdentitySelection)
	}
	if s.Email == "" {
		return nil
	}
	if len(s.Email) < 3 || len(s.Email) > MaxConfirmedIdentityEmailBytes || strings.ContainsFunc(s.Email, unicode.IsSpace) || strings.ContainsFunc(s.Email, unicode.IsControl) {
		return fmt.Errorf("%w: email", ErrInvalidConfirmedIdentitySelection)
	}
	parsed, err := mail.ParseAddress(s.Email)
	if err != nil || parsed.Address != s.Email || parsed.Name != "" {
		return fmt.Errorf("%w: email", ErrInvalidConfirmedIdentitySelection)
	}
	return nil
}

func validConfirmedDisplayName(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > MaxConfirmedIdentityDisplayNameBytes || strings.TrimSpace(value) != value || strings.Contains(value, "  ") {
		return false
	}
	for _, sentinel := range []string{"private key", "token", "password", "secret", "credential", "authorization", "bearer", "-----begin", "-----end"} {
		if strings.Contains(strings.ToLower(value), sentinel) {
			return false
		}
	}
	for _, runeValue := range value {
		if unicode.IsLetter(runeValue) || unicode.IsMark(runeValue) || unicode.IsNumber(runeValue) {
			continue
		}
		switch runeValue {
		case ' ', '\'', '’', '-', '.', ',', '(', ')':
		default:
			return false
		}
	}
	return true
}

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

// ActorScope carries a structurally valid actor and its issuer-derived scope.
// It does not establish issuer authority; issuers derive fresh scopes from their
// own private records before dispatching an action.
type ActorScope struct {
	Actor        ActorEnvelope
	ProjectID    string
	MembershipID string
	PassportID   string
	CredentialID string
	Roles        []string
	Permissions  []string
}

func (s ActorScope) Validate() error {
	if err := s.Actor.Validate(); err != nil {
		return err
	}
	if !CanonicalUUID(s.ProjectID) {
		return fmt.Errorf("%w: invalid scope project ID", ErrInvalidActorEnvelope)
	}
	for _, value := range []string{s.MembershipID, s.PassportID, s.CredentialID} {
		if value != "" && !CanonicalUUID(value) {
			return fmt.Errorf("%w: invalid scope identifier", ErrInvalidActorEnvelope)
		}
	}
	return nil
}

func (s ActorScope) HasPermission(name string) bool {
	if name == "" {
		return false
	}
	for _, permission := range s.Permissions {
		if permission == name {
			return true
		}
	}
	return false
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

// ValidCandidateImportOrigin reports whether value is valid persisted candidate
// provenance: either a canonical principal UUID or the fixed Git observer token.
func ValidCandidateImportOrigin(value string) bool {
	return CanonicalUUID(value) || value == CandidateImportOriginGitObservationRebaseV1
}
