package localidentity

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	connectionRecordsName         = "connections.json"
	connectionRecordVersion       = 1
	UnknownClientMetadata         = "unknown"
	ConnectionSessionRetention    = 30 * 24 * time.Hour
	maxTerminalConnectionSessions = 10_000
	maxDurableAgents              = 1_024
	maxConnectionMetadataBytes    = 128
)

var (
	ErrInvalidClientInfo          = errors.New("localidentity: invalid MCP client info")
	ErrConnectionSessionNotFound  = errors.New("localidentity: connection session not found")
	ErrConnectionSessionExhausted = errors.New("localidentity: connection session capacity exhausted")
)

// MCPClientInfo is the bounded, non-secret harness metadata accepted from an
// MCP initialize request. Identity, ownership, session, and assurance are not
// representable here and remain Gateway-owned.
type MCPClientInfo struct {
	Name         string
	Version      string
	ModelName    string
	ModelVersion string
}

// AgentProfile is a durable machine-private agent principal. Harness version
// and model belong to individual connection sessions, not this stable identity.
type AgentProfile struct {
	SchemaVersion      int       `json:"schema_version"`
	AgentID            string    `json:"agent_id"`
	AccountableHumanID string    `json:"accountable_human_id"`
	HarnessName        string    `json:"harness_name"`
	CreatedAt          time.Time `json:"created_at"`
}

// ConnectionSession is bounded runtime attribution evidence. EndedAt is nil
// while the owning Gateway connection is live and is set only by close or the
// explicit single-owner startup recovery path.
type ConnectionSession struct {
	SchemaVersion      int        `json:"schema_version"`
	SessionID          string     `json:"session_id"`
	AgentID            string     `json:"agent_id,omitempty"`
	HumanPrincipalID   string     `json:"human_principal_id,omitempty"`
	AccountableHumanID string     `json:"accountable_human_id"`
	HarnessName        string     `json:"harness_name"`
	HarnessVersion     string     `json:"harness_version"`
	ModelName          string     `json:"model_name,omitempty"`
	ModelVersion       string     `json:"model_version,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	EndedAt            *time.Time `json:"ended_at,omitempty"`
}

type connectionRecords struct {
	SchemaVersion int                 `json:"schema_version"`
	Agents        []AgentProfile      `json:"agents"`
	Sessions      []ConnectionSession `json:"sessions"`
}

// OpenMCP establishes a fresh server-owned agent session, reusing the one
// durable agent selected by accountable human and normalized harness name.
func (s *Store) OpenMCP(ctx context.Context, info MCPClientInfo) (ConnectionIdentity, error) {
	return s.openConnection(ctx, info, false)
}

// OpenHuman establishes a bounded connection record for a same-user private
// CLI/setup caller. Its actor envelope intentionally carries no session or
// harness provenance because human actions are attributed directly.
func (s *Store) OpenHuman(ctx context.Context, info MCPClientInfo) (ConnectionIdentity, error) {
	return s.openConnection(ctx, info, true)
}

func (s *Store) openConnection(ctx context.Context, supplied MCPClientInfo, human bool) (ConnectionIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionIdentity{}, err
	}
	info, err := canonicalClientInfo(supplied)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	fd, err := s.openRoot()
	if err != nil {
		return ConnectionIdentity{}, err
	}
	defer closeLocalIdentityFD(fd)
	unlock, err := lockLocalIdentityStore(ctx, fd)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	defer unlock()
	selected, exists, err := readSelectedRecord(fd)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	if !exists {
		return ConnectionIdentity{}, ErrNoSelectedIdentity
	}
	if _, exists, err := readHumanRecord(fd, selected.HumanPrincipalID); err != nil || !exists {
		if err != nil {
			return ConnectionIdentity{}, err
		}
		return ConnectionIdentity{}, ErrInvalidStoreRecord
	}
	records, exists, err := readConnectionRecords(fd)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	if !exists {
		records = connectionRecords{SchemaVersion: connectionRecordVersion, Agents: []AgentProfile{}, Sessions: []ConnectionSession{}}
	}
	now := s.clock().UTC()
	if now.IsZero() {
		return ConnectionIdentity{}, fmt.Errorf("%w: zero clock", ErrInvalidStoreRecord)
	}
	records.Sessions = retainedConnectionSessions(records.Sessions, now)
	sessionID, err := s.newUUID()
	if err != nil {
		return ConnectionIdentity{}, err
	}
	session := ConnectionSession{
		SchemaVersion: connectionRecordVersion, SessionID: sessionID,
		AccountableHumanID: selected.HumanPrincipalID, HarnessName: info.Name,
		HarnessVersion: info.Version, ModelName: info.ModelName,
		ModelVersion: info.ModelVersion, StartedAt: now,
	}
	if human {
		session.HumanPrincipalID = selected.HumanPrincipalID
	} else {
		agentIndex := -1
		for index := range records.Agents {
			if records.Agents[index].AccountableHumanID == selected.HumanPrincipalID && records.Agents[index].HarnessName == info.Name {
				agentIndex = index
				break
			}
		}
		if agentIndex < 0 {
			if len(records.Agents) >= maxDurableAgents {
				return ConnectionIdentity{}, ErrConnectionSessionExhausted
			}
			agentID, idErr := s.newUUID()
			if idErr != nil {
				return ConnectionIdentity{}, idErr
			}
			records.Agents = append(records.Agents, AgentProfile{SchemaVersion: connectionRecordVersion, AgentID: agentID, AccountableHumanID: selected.HumanPrincipalID, HarnessName: info.Name, CreatedAt: now})
			sort.Slice(records.Agents, func(left, right int) bool { return records.Agents[left].AgentID < records.Agents[right].AgentID })
			session.AgentID = agentID
		} else {
			session.AgentID = records.Agents[agentIndex].AgentID
		}
	}
	records.Sessions = append(records.Sessions, session)
	sort.Slice(records.Sessions, func(left, right int) bool {
		return records.Sessions[left].SessionID < records.Sessions[right].SessionID
	})
	if err := writeConnectionRecords(s, fd, records); err != nil {
		return ConnectionIdentity{}, err
	}
	return ConnectionIdentity{SessionID: sessionID, OccurredAt: now}, nil
}

// ResolveLocalActor reconstructs an actor solely from the server-generated
// connection session and the durable selected-human accountability boundary.
func (s *Store) ResolveLocalActor(ctx context.Context, connection ConnectionIdentity) (types.ActorEnvelope, error) {
	if err := connection.validate(); err != nil {
		return types.ActorEnvelope{}, err
	}
	session, err := s.Session(ctx, connection.SessionID)
	if err != nil {
		return types.ActorEnvelope{}, err
	}
	if session.EndedAt != nil {
		return types.ActorEnvelope{}, ErrInvalidConnectionIdentity
	}
	var actor types.ActorEnvelope
	if session.AgentID != "" {
		actor = types.ActorEnvelope{
			ActorKind: types.ActorAgent, AgentID: session.AgentID,
			AccountableHumanID: session.AccountableHumanID,
			SessionID:          session.SessionID, HarnessName: session.HarnessName,
			HarnessVersion: session.HarnessVersion, ModelName: session.ModelName,
			ModelVersion: session.ModelVersion, Assurance: types.AssuranceLocal,
			OccurredAt: connection.OccurredAt,
		}
	} else {
		actor = types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: session.HumanPrincipalID, Assurance: types.AssuranceLocal, OccurredAt: connection.OccurredAt}
	}
	if err := actor.ValidateLocalAction(); err != nil {
		return types.ActorEnvelope{}, err
	}
	return actor, nil
}

// ResolveHumanActor is the explicit authority used by same-user private CLI
// and setup operations. It never copies connection/harness metadata.
func (s *Store) ResolveHumanActor(ctx context.Context, occurredAt time.Time) (types.ActorEnvelope, error) {
	_, offset := occurredAt.Zone()
	if occurredAt.IsZero() || offset != 0 {
		return types.ActorEnvelope{}, ErrInvalidConnectionIdentity
	}
	profile, err := s.Selected(ctx)
	if err != nil {
		return types.ActorEnvelope{}, err
	}
	actor := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: profile.HumanPrincipalID, Assurance: types.AssuranceLocal, OccurredAt: occurredAt}
	if err := actor.ValidateLocalAction(); err != nil {
		return types.ActorEnvelope{}, err
	}
	return actor, nil
}

func (s *Store) Session(ctx context.Context, sessionID string) (ConnectionSession, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionSession{}, err
	}
	if !types.CanonicalUUID(sessionID) {
		return ConnectionSession{}, ErrInvalidConnectionIdentity
	}
	fd, err := s.openRoot()
	if err != nil {
		return ConnectionSession{}, err
	}
	defer closeLocalIdentityFD(fd)
	unlock, err := lockLocalIdentityStore(ctx, fd)
	if err != nil {
		return ConnectionSession{}, err
	}
	defer unlock()
	records, exists, err := readConnectionRecords(fd)
	if err != nil {
		return ConnectionSession{}, err
	}
	if exists {
		for _, session := range records.Sessions {
			if session.SessionID == sessionID {
				return session, nil
			}
		}
	}
	return ConnectionSession{}, ErrConnectionSessionNotFound
}

func (s *Store) CloseConnection(ctx context.Context, connection ConnectionIdentity) error {
	if err := connection.validate(); err != nil {
		return err
	}
	return s.updateConnectionSessions(ctx, func(records *connectionRecords, now time.Time) (bool, error) {
		for index := range records.Sessions {
			if records.Sessions[index].SessionID != connection.SessionID {
				continue
			}
			if records.Sessions[index].EndedAt != nil {
				return false, nil
			}
			ended := now
			records.Sessions[index].EndedAt = &ended
			return true, nil
		}
		return false, ErrConnectionSessionNotFound
	})
}

// RecoverConnectionSessions is intentionally explicit: Gateway may call it
// only after its authoritative single-daemon owner lock is held. Store.Open
// never terminates a possibly concurrent live session.
func (s *Store) RecoverConnectionSessions(ctx context.Context) error {
	return s.updateConnectionSessions(ctx, func(records *connectionRecords, now time.Time) (bool, error) {
		changed := false
		for index := range records.Sessions {
			if records.Sessions[index].EndedAt == nil {
				ended := now
				records.Sessions[index].EndedAt = &ended
				changed = true
			}
		}
		return changed, nil
	})
}

func (s *Store) updateConnectionSessions(ctx context.Context, update func(*connectionRecords, time.Time) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := s.openRoot()
	if err != nil {
		return err
	}
	defer closeLocalIdentityFD(fd)
	unlock, err := lockLocalIdentityStore(ctx, fd)
	if err != nil {
		return err
	}
	defer unlock()
	records, exists, err := readConnectionRecords(fd)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	now := s.clock().UTC()
	if now.IsZero() {
		return fmt.Errorf("%w: zero clock", ErrInvalidStoreRecord)
	}
	changed, err := update(&records, now)
	if err != nil {
		return err
	}
	retained := retainedConnectionSessions(records.Sessions, now)
	if len(retained) != len(records.Sessions) {
		changed = true
	}
	records.Sessions = retained
	if !changed {
		return nil
	}
	return writeConnectionRecords(s, fd, records)
}

func canonicalClientInfo(info MCPClientInfo) (MCPClientInfo, error) {
	canonical := MCPClientInfo{
		Name: strings.ToLower(strings.TrimSpace(info.Name)), Version: strings.TrimSpace(info.Version),
		ModelName: strings.TrimSpace(info.ModelName), ModelVersion: strings.TrimSpace(info.ModelVersion),
	}
	if canonical.Name == "" {
		canonical.Name = UnknownClientMetadata
	}
	if canonical.Version == "" {
		canonical.Version = UnknownClientMetadata
	}
	if (canonical.ModelName == "") != (canonical.ModelVersion == "") {
		return MCPClientInfo{}, ErrInvalidClientInfo
	}
	for _, value := range []string{canonical.Name, canonical.Version, canonical.ModelName, canonical.ModelVersion} {
		if value == "" {
			continue
		}
		if !utf8.ValidString(value) || len(value) > maxConnectionMetadataBytes || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
			return MCPClientInfo{}, ErrInvalidClientInfo
		}
	}
	return canonical, nil
}

func retainedConnectionSessions(sessions []ConnectionSession, now time.Time) []ConnectionSession {
	cutoff := now.Add(-ConnectionSessionRetention)
	terminal := make([]ConnectionSession, 0, len(sessions))
	active := make([]ConnectionSession, 0, len(sessions))
	for _, session := range sessions {
		if session.EndedAt == nil {
			active = append(active, session)
		} else {
			terminal = append(terminal, session)
		}
	}
	sort.Slice(terminal, func(left, right int) bool {
		if terminal[left].EndedAt.Equal(*terminal[right].EndedAt) {
			return terminal[left].SessionID > terminal[right].SessionID
		}
		return terminal[left].EndedAt.After(*terminal[right].EndedAt)
	})
	if len(terminal) > maxTerminalConnectionSessions {
		terminal = terminal[:maxTerminalConnectionSessions]
	}
	retained := append([]ConnectionSession{}, active...)
	for _, session := range terminal {
		if session.EndedAt.After(cutoff) {
			retained = append(retained, session)
		}
	}
	sort.Slice(retained, func(left, right int) bool { return retained[left].SessionID < retained[right].SessionID })
	return retained
}

func readConnectionRecords(fd int) (connectionRecords, bool, error) {
	var records connectionRecords
	exists, err := readStrictLocalIdentityRecord(fd, connectionRecordsName, &records)
	if err != nil || !exists {
		return connectionRecords{}, exists, err
	}
	if err := records.validate(); err != nil {
		return connectionRecords{}, false, err
	}
	return records, true, nil
}

func writeConnectionRecords(store *Store, fd int, records connectionRecords) error {
	if err := records.validate(); err != nil {
		return err
	}
	encoded, err := marshalCanonical(records)
	if err != nil {
		return err
	}
	if len(encoded) > maxLocalIdentityRecordBytes {
		return ErrConnectionSessionExhausted
	}
	return store.atomicWrite(fd, connectionRecordsName, encoded, fs.FileMode(0o600), true)
}

func (records connectionRecords) validate() error {
	if records.SchemaVersion != connectionRecordVersion || records.Agents == nil || records.Sessions == nil || len(records.Agents) > maxDurableAgents {
		return ErrInvalidStoreRecord
	}
	agents := make(map[string]AgentProfile, len(records.Agents))
	keys := make(map[string]struct{}, len(records.Agents))
	for index, agent := range records.Agents {
		if agent.SchemaVersion != connectionRecordVersion || !types.CanonicalUUID(agent.AgentID) || !types.CanonicalUUID(agent.AccountableHumanID) || agent.CreatedAt.IsZero() || agent.CreatedAt.Location() != time.UTC {
			return ErrInvalidStoreRecord
		}
		info, err := canonicalClientInfo(MCPClientInfo{Name: agent.HarnessName, Version: UnknownClientMetadata})
		if err != nil || info.Name != agent.HarnessName {
			return ErrInvalidStoreRecord
		}
		key := agent.AccountableHumanID + "\x00" + agent.HarnessName
		if _, duplicate := agents[agent.AgentID]; duplicate {
			return ErrInvalidStoreRecord
		}
		if _, duplicate := keys[key]; duplicate {
			return ErrInvalidStoreRecord
		}
		agents[agent.AgentID] = agent
		keys[key] = struct{}{}
		if index > 0 && records.Agents[index-1].AgentID >= agent.AgentID {
			return ErrInvalidStoreRecord
		}
	}
	sessionIDs := make(map[string]struct{}, len(records.Sessions))
	activeCount := 0
	terminalCount := 0
	for index, session := range records.Sessions {
		if session.SchemaVersion != connectionRecordVersion || !types.CanonicalUUID(session.SessionID) || !types.CanonicalUUID(session.AccountableHumanID) || session.StartedAt.IsZero() || session.StartedAt.Location() != time.UTC || (session.EndedAt != nil && (session.EndedAt.IsZero() || session.EndedAt.Location() != time.UTC || session.EndedAt.Before(session.StartedAt))) {
			return ErrInvalidStoreRecord
		}
		info, err := canonicalClientInfo(MCPClientInfo{Name: session.HarnessName, Version: session.HarnessVersion, ModelName: session.ModelName, ModelVersion: session.ModelVersion})
		if err != nil || info.Name != session.HarnessName || info.Version != session.HarnessVersion || info.ModelName != session.ModelName || info.ModelVersion != session.ModelVersion {
			return ErrInvalidStoreRecord
		}
		if (session.AgentID == "") == (session.HumanPrincipalID == "") {
			return ErrInvalidStoreRecord
		}
		if session.AgentID != "" {
			agent, ok := agents[session.AgentID]
			if !ok || agent.AccountableHumanID != session.AccountableHumanID || agent.HarnessName != session.HarnessName {
				return ErrInvalidStoreRecord
			}
		} else if !types.CanonicalUUID(session.HumanPrincipalID) || session.HumanPrincipalID != session.AccountableHumanID {
			return ErrInvalidStoreRecord
		}
		if _, duplicate := sessionIDs[session.SessionID]; duplicate {
			return ErrInvalidStoreRecord
		}
		sessionIDs[session.SessionID] = struct{}{}
		if index > 0 && records.Sessions[index-1].SessionID >= session.SessionID {
			return ErrInvalidStoreRecord
		}
		if session.EndedAt == nil {
			activeCount++
		} else {
			terminalCount++
		}
	}
	if terminalCount > maxTerminalConnectionSessions || activeCount > maxTerminalConnectionSessions {
		return ErrInvalidStoreRecord
	}
	return nil
}
