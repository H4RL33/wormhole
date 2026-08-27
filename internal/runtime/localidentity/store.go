// Package localidentity owns machine-private, owner-only local human identity.
package localidentity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	selectedRecordName          = "selected.json"
	lockRecordName              = ".store.lock"
	maxLocalIdentityRecordBytes = 16 * 1024
	// The identity root only needs a receipt, key, profile, selection, lock,
	// and a bounded number of interrupted-publication temporary files. Refuse
	// oversized directories before examining or changing their contents.
	maxLocalIdentityStoreEntries = 64
)

var (
	ErrNoSelectedIdentity         = errors.New("localidentity: no selected identity")
	ErrSetupIdentityDrift         = errors.New("localidentity: setup identity drift")
	ErrIdentityConflict           = errors.New("localidentity: identity authority conflict")
	ErrInvalidStoreRecord         = errors.New("localidentity: invalid store record")
	ErrUnsafeStore                = errors.New("localidentity: unsafe owner-only store")
	ErrStoreFilesystemUnsupported = errors.New("localidentity: owner-only filesystem operations unsupported")
	ErrInvalidConnectionIdentity  = errors.New("localidentity: invalid connection identity")
)

// PublicHumanProfile contains the only identity information this package
// returns to callers. In particular, it omits the selected email and private
// Ed25519 key.
type PublicHumanProfile struct {
	HumanPrincipalID string    `json:"human_principal_id"`
	DisplayName      string    `json:"display_name"`
	PublicKey        []byte    `json:"public_key"`
	CreatedAt        time.Time `json:"created_at"`
}

// ConnectionIdentity supplies connection-owned action time to actor
// resolution. It intentionally contains no caller-chosen actor or routing
// fields; those authorities are Gateway-owned in the next stage.
type ConnectionIdentity struct {
	OccurredAt time.Time
}

func (c ConnectionIdentity) validate() error {
	_, offset := c.OccurredAt.Zone()
	if c.OccurredAt.IsZero() || offset != 0 {
		return ErrInvalidConnectionIdentity
	}
	return nil
}

type humanRecord struct {
	HumanPrincipalID string    `json:"human_principal_id"`
	DisplayName      string    `json:"display_name"`
	Email            string    `json:"email,omitempty"`
	PublicKey        []byte    `json:"public_key"`
	CreatedAt        time.Time `json:"created_at"`
}

type setupRecord struct {
	HumanPrincipalID string                           `json:"human_principal_id"`
	Selection        types.ConfirmedIdentitySelection `json:"selection"`
	CreatedAt        time.Time                        `json:"created_at"`
}

type selectedRecord struct {
	HumanPrincipalID string `json:"human_principal_id"`
}

// Store has no ambient path or process-global state. Its unexported hooks are
// deliberately injectable by package tests to prove recovery after each
// durable publication boundary.
type Store struct {
	root        string
	clock       func() time.Time
	random      func([]byte) (int, error)
	atomicWrite func(int, string, []byte, fs.FileMode, bool) error
}

// Open creates (when absent) and validates an owner-only local identity root.
func Open(root string) (*Store, error) {
	canonicalRoot, fd, err := openLocalIdentityRoot(root)
	if err != nil {
		return nil, err
	}
	if err := closeLocalIdentityFD(fd); err != nil {
		return nil, err
	}
	store := &Store{root: canonicalRoot, clock: time.Now, random: rand.Read}
	store.atomicWrite = store.atomicWriteFile
	return store, nil
}

// EnsureSelectedForSetup durably records setup intent before key creation. A
// replay of the same journal and selection recovers every durable prefix;
// another selection for that journal is refused without changing state.
func (s *Store) EnsureSelectedForSetup(ctx context.Context, journalID string, selection types.ConfirmedIdentitySelection) (PublicHumanProfile, error) {
	if err := ctx.Err(); err != nil {
		return PublicHumanProfile{}, err
	}
	if !types.CanonicalUUID(journalID) {
		return PublicHumanProfile{}, fmt.Errorf("%w: setup journal UUID", ErrInvalidStoreRecord)
	}
	if err := selection.Validate(); err != nil {
		return PublicHumanProfile{}, err
	}
	fd, err := s.openRoot()
	if err != nil {
		return PublicHumanProfile{}, err
	}
	defer closeLocalIdentityFD(fd)
	unlock, err := lockLocalIdentityStore(ctx, fd)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	defer unlock()
	return s.ensureSelectedLocked(ctx, fd, journalID, selection)
}

func (s *Store) ensureSelectedLocked(ctx context.Context, fd int, journalID string, selection types.ConfirmedIdentitySelection) (PublicHumanProfile, error) {
	setupName := "setup-" + journalID + ".json"
	setup, exists, err := readSetupRecord(fd, setupName)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	if exists && setup.Selection != selection {
		return PublicHumanProfile{}, ErrSetupIdentityDrift
	}
	selected, selectedExists, err := readSelectedRecord(fd)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	topology, topologyErr := inspectIdentityTopology(fd, selection)
	if topologyErr != nil {
		return PublicHumanProfile{}, topologyErr
	}
	if selectedExists && (!topology.found || !topology.complete || topology.setup.HumanPrincipalID != selected.HumanPrincipalID) {
		return PublicHumanProfile{}, ErrIdentityConflict
	}
	if exists && (!topology.found || topology.setup.HumanPrincipalID != setup.HumanPrincipalID || !topology.setup.CreatedAt.Equal(setup.CreatedAt)) {
		return PublicHumanProfile{}, ErrIdentityConflict
	}
	if !exists {
		if err := ctx.Err(); err != nil {
			return PublicHumanProfile{}, err
		}
		if topology.found {
			setup = topology.setup
		} else {
			if selectedExists {
				return PublicHumanProfile{}, ErrIdentityConflict
			}
			identifier, identifierErr := s.newUUID()
			if identifierErr != nil {
				return PublicHumanProfile{}, identifierErr
			}
			now := s.clock().UTC()
			if now.IsZero() {
				return PublicHumanProfile{}, fmt.Errorf("%w: zero clock", ErrInvalidStoreRecord)
			}
			setup = setupRecord{HumanPrincipalID: identifier, Selection: selection, CreatedAt: now}
		}
		encoded, encodeErr := marshalCanonical(setup)
		if encodeErr != nil {
			return PublicHumanProfile{}, encodeErr
		}
		if writeErr := s.atomicWrite(fd, setupName, encoded, 0o600, false); writeErr != nil {
			return PublicHumanProfile{}, writeErr
		}
	}
	if err := ctx.Err(); err != nil {
		return PublicHumanProfile{}, err
	}
	privateKey, err := s.ensurePrivateKey(fd, setup.HumanPrincipalID)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	human, exists, err := readHumanRecord(fd, setup.HumanPrincipalID)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	if !exists {
		human = humanRecord{HumanPrincipalID: setup.HumanPrincipalID, DisplayName: setup.Selection.DisplayName, Email: setup.Selection.Email, PublicKey: append([]byte(nil), publicKey...), CreatedAt: setup.CreatedAt}
		encoded, encodeErr := marshalCanonical(human)
		if encodeErr != nil {
			return PublicHumanProfile{}, encodeErr
		}
		if writeErr := s.atomicWrite(fd, humanRecordName(setup.HumanPrincipalID), encoded, 0o600, false); writeErr != nil {
			return PublicHumanProfile{}, writeErr
		}
	} else if human.DisplayName != setup.Selection.DisplayName || human.Email != setup.Selection.Email || string(human.PublicKey) != string(publicKey) || !human.CreatedAt.Equal(setup.CreatedAt) {
		return PublicHumanProfile{}, fmt.Errorf("%w: human record differs from setup intent", ErrInvalidStoreRecord)
	}
	if err := s.ensureSelectedPointer(fd, setup.HumanPrincipalID); err != nil {
		return PublicHumanProfile{}, err
	}
	return publicProfile(human), nil
}

func (s *Store) ensureSelectedPointer(fd int, humanID string) error {
	selected, exists, err := readSelectedRecord(fd)
	if err != nil {
		return err
	}
	if exists {
		if selected.HumanPrincipalID == humanID {
			return nil
		}
		return ErrIdentityConflict
	}
	encoded, err := marshalCanonical(selectedRecord{HumanPrincipalID: humanID})
	if err != nil {
		return err
	}
	writeErr := s.atomicWrite(fd, selectedRecordName, encoded, 0o600, false)
	if writeErr == nil {
		return nil
	}
	selected, exists, rereadErr := readSelectedRecord(fd)
	if rereadErr != nil {
		return rereadErr
	}
	if exists {
		if selected.HumanPrincipalID == humanID {
			return nil
		}
		return ErrIdentityConflict
	}
	return writeErr
}

type identityTopology struct {
	setup    setupRecord
	found    bool
	complete bool
}

func inspectIdentityTopology(fd int, selection types.ConfirmedIdentitySelection) (identityTopology, error) {
	names, err := listLocalIdentityRecordNames(fd)
	if err != nil {
		return identityTopology{}, err
	}
	receipts := map[string]setupRecord{}
	humans := map[string]humanRecord{}
	keys := map[string]ed25519.PrivateKey{}
	for _, name := range names {
		switch {
		case name == lockRecordName || name == selectedRecordName:
			continue
		case isLocalIdentityTemporaryName(name):
			if _, exists, readErr := readLocalIdentityFile(fd, name); readErr != nil || !exists {
				return identityTopology{}, ErrInvalidStoreRecord
			}
			continue
		case parseSetupRecordName(name) != "":
			record, exists, readErr := readSetupRecord(fd, name)
			if readErr != nil || !exists {
				return identityTopology{}, ErrInvalidStoreRecord
			}
			if record.Selection != selection {
				return identityTopology{}, ErrIdentityConflict
			}
			if prior, duplicate := receipts[record.HumanPrincipalID]; duplicate && !prior.CreatedAt.Equal(record.CreatedAt) {
				return identityTopology{}, ErrIdentityConflict
			}
			receipts[record.HumanPrincipalID] = record
		case parseHumanRecordName(name) != "":
			identifier := parseHumanRecordName(name)
			record, exists, readErr := readHumanRecord(fd, identifier)
			if readErr != nil || !exists {
				return identityTopology{}, ErrInvalidStoreRecord
			}
			humans[identifier] = record
		case parsePrivateKeyRecordName(name) != "":
			identifier := parsePrivateKeyRecordName(name)
			data, exists, readErr := readLocalIdentityFile(fd, name)
			if readErr != nil || !exists {
				return identityTopology{}, ErrInvalidStoreRecord
			}
			key, keyErr := parsePrivateKey(data)
			if keyErr != nil {
				return identityTopology{}, keyErr
			}
			keys[identifier] = key
		default:
			return identityTopology{}, fmt.Errorf("%w: unknown identity store entry", ErrInvalidStoreRecord)
		}
	}
	if len(receipts) == 0 {
		if len(humans) != 0 || len(keys) != 0 {
			return identityTopology{}, ErrIdentityConflict
		}
		return identityTopology{}, nil
	}
	if len(receipts) != 1 {
		return identityTopology{}, ErrIdentityConflict
	}
	var candidate setupRecord
	for _, candidate = range receipts {
		break
	}
	for identifier, human := range humans {
		if identifier != candidate.HumanPrincipalID || human.DisplayName != candidate.Selection.DisplayName || human.Email != candidate.Selection.Email || !human.CreatedAt.Equal(candidate.CreatedAt) {
			return identityTopology{}, ErrIdentityConflict
		}
	}
	for identifier := range keys {
		if identifier != candidate.HumanPrincipalID {
			return identityTopology{}, ErrIdentityConflict
		}
	}
	human, hasHuman := humans[candidate.HumanPrincipalID]
	key, hasKey := keys[candidate.HumanPrincipalID]
	// The durable order is receipt, key, profile, selected pointer. A key
	// without a profile is therefore an interrupted but repairable prefix only
	// while no selected pointer exists; a profile without a key is unreachable.
	if hasHuman && !hasKey {
		return identityTopology{}, ErrIdentityConflict
	}
	if hasHuman && string(human.PublicKey) != string(key.Public().(ed25519.PublicKey)) {
		return identityTopology{}, ErrIdentityConflict
	}
	return identityTopology{setup: candidate, found: true, complete: hasHuman && hasKey}, nil
}

func parseSetupRecordName(name string) string {
	return parseIdentityRecordName(name, "setup-", ".json")
}

func parseHumanRecordName(name string) string {
	return parseIdentityRecordName(name, "human-", ".json")
}

func parsePrivateKeyRecordName(name string) string {
	return parseIdentityRecordName(name, "key-", ".ed25519")
}

func parseIdentityRecordName(name, prefix, suffix string) string {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return ""
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if !types.CanonicalUUID(identifier) {
		return ""
	}
	return identifier
}

func isLocalIdentityTemporaryName(name string) bool {
	const prefix = ".tmp-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	for _, character := range name[len(prefix):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func (s *Store) ensurePrivateKey(fd int, humanID string) (ed25519.PrivateKey, error) {
	name := privateKeyRecordName(humanID)
	data, exists, err := readLocalIdentityFile(fd, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return parsePrivateKey(data)
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(readerFromRandom(s.random), seed); err != nil {
		return nil, fmt.Errorf("localidentity: generate Ed25519 seed: %w", err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	if err := s.atomicWrite(fd, name, privateKey, 0o600, false); err != nil {
		return nil, err
	}
	return privateKey, nil
}

// Selected returns the selected identity's bounded public profile.
func (s *Store) Selected(ctx context.Context) (PublicHumanProfile, error) {
	if err := ctx.Err(); err != nil {
		return PublicHumanProfile{}, err
	}
	fd, err := s.openRoot()
	if err != nil {
		return PublicHumanProfile{}, err
	}
	defer closeLocalIdentityFD(fd)
	selected, exists, err := readSelectedRecord(fd)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	if !exists {
		return PublicHumanProfile{}, ErrNoSelectedIdentity
	}
	human, exists, err := readHumanRecord(fd, selected.HumanPrincipalID)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	if !exists {
		return PublicHumanProfile{}, fmt.Errorf("%w: selected human is missing", ErrInvalidStoreRecord)
	}
	privateKeyBytes, exists, err := readLocalIdentityFile(fd, privateKeyRecordName(human.HumanPrincipalID))
	if err != nil {
		return PublicHumanProfile{}, err
	}
	if !exists {
		return PublicHumanProfile{}, fmt.Errorf("%w: selected human key is missing or mismatched", ErrInvalidStoreRecord)
	}
	privateKey, err := parsePrivateKey(privateKeyBytes)
	if err != nil {
		return PublicHumanProfile{}, err
	}
	if string(privateKey.Public().(ed25519.PublicKey)) != string(human.PublicKey) {
		return PublicHumanProfile{}, fmt.Errorf("%w: selected human key is missing or mismatched", ErrInvalidStoreRecord)
	}
	return publicProfile(human), nil
}

// SelectedMatchesSetup compares setup's private confirmed fields inside the
// owner-private identity boundary and returns only the bounded public profile.
func (s *Store) SelectedMatchesSetup(ctx context.Context, selection types.ConfirmedIdentitySelection) (PublicHumanProfile, bool, error) {
	if err := selection.Validate(); err != nil {
		return PublicHumanProfile{}, false, err
	}
	profile, err := s.Selected(ctx)
	if err != nil {
		return PublicHumanProfile{}, false, err
	}
	fd, err := s.openRoot()
	if err != nil {
		return PublicHumanProfile{}, false, err
	}
	defer closeLocalIdentityFD(fd)
	human, exists, err := readHumanRecord(fd, profile.HumanPrincipalID)
	if err != nil || !exists {
		if err != nil {
			return PublicHumanProfile{}, false, err
		}
		return PublicHumanProfile{}, false, ErrInvalidStoreRecord
	}
	return profile, human.DisplayName == selection.DisplayName && human.Email == selection.Email, nil
}

// ResolveLocalActor derives a local human actor from the selected owner-only
// identity. Callers cannot supply a human principal ID or assurance.
func (s *Store) ResolveLocalActor(ctx context.Context, connection ConnectionIdentity) (types.ActorEnvelope, error) {
	if err := connection.validate(); err != nil {
		return types.ActorEnvelope{}, err
	}
	profile, err := s.Selected(ctx)
	if err != nil {
		return types.ActorEnvelope{}, err
	}
	actor := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: profile.HumanPrincipalID, Assurance: types.AssuranceLocal, OccurredAt: connection.OccurredAt}
	if err := actor.ValidateLocalAction(); err != nil {
		return types.ActorEnvelope{}, err
	}
	return actor, nil
}

func (s *Store) openRoot() (int, error) {
	_, fd, err := openLocalIdentityRoot(s.root)
	return fd, err
}

func (s *Store) newUUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(readerFromRandom(s.random), bytes); err != nil {
		return "", fmt.Errorf("localidentity: generate human UUID: %w", err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func (s *Store) atomicWriteFile(fd int, name string, data []byte, mode fs.FileMode, replace bool) error {
	return atomicLocalIdentityWrite(fd, name, data, mode, replace, s.random)
}

func humanRecordName(identifier string) string      { return "human-" + identifier + ".json" }
func privateKeyRecordName(identifier string) string { return "key-" + identifier + ".ed25519" }

func parsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	if len(data) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid Ed25519 private key length", ErrInvalidStoreRecord)
	}
	privateKey := ed25519.PrivateKey(append([]byte(nil), data...))
	if string(ed25519.NewKeyFromSeed(privateKey.Seed())) != string(privateKey) {
		return nil, fmt.Errorf("%w: invalid Ed25519 private key", ErrInvalidStoreRecord)
	}
	return privateKey, nil
}

func readSetupRecord(fd int, name string) (setupRecord, bool, error) {
	var record setupRecord
	exists, err := readStrictLocalIdentityRecord(fd, name, &record)
	if err != nil || !exists {
		return setupRecord{}, exists, err
	}
	if !types.CanonicalUUID(record.HumanPrincipalID) || record.Selection.Validate() != nil || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC {
		return setupRecord{}, false, ErrInvalidStoreRecord
	}
	return record, true, nil
}

func readSelectedRecord(fd int) (selectedRecord, bool, error) {
	var record selectedRecord
	exists, err := readStrictLocalIdentityRecord(fd, selectedRecordName, &record)
	if err != nil || !exists {
		return selectedRecord{}, exists, err
	}
	if !types.CanonicalUUID(record.HumanPrincipalID) {
		return selectedRecord{}, false, ErrInvalidStoreRecord
	}
	return record, true, nil
}

func readHumanRecord(fd int, identifier string) (humanRecord, bool, error) {
	if !types.CanonicalUUID(identifier) {
		return humanRecord{}, false, ErrInvalidStoreRecord
	}
	var record humanRecord
	exists, err := readStrictLocalIdentityRecord(fd, humanRecordName(identifier), &record)
	if err != nil || !exists {
		return humanRecord{}, exists, err
	}
	selection := types.ConfirmedIdentitySelection{DisplayName: record.DisplayName, Email: record.Email}
	if record.HumanPrincipalID != identifier || selection.Validate() != nil || len(record.PublicKey) != ed25519.PublicKeySize || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC {
		return humanRecord{}, false, ErrInvalidStoreRecord
	}
	return record, true, nil
}

func publicProfile(record humanRecord) PublicHumanProfile {
	return PublicHumanProfile{HumanPrincipalID: record.HumanPrincipalID, DisplayName: record.DisplayName, PublicKey: append([]byte(nil), record.PublicKey...), CreatedAt: record.CreatedAt}
}

func marshalCanonical(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("localidentity: encode record: %w", err)
	}
	return append(encoded, '\n'), nil
}

func readStrictLocalIdentityRecord(fd int, name string, target any) (bool, error) {
	data, exists, err := readLocalIdentityFile(fd, name)
	if err != nil || !exists {
		return exists, err
	}
	if err := strictJSONDecode(data, target); err != nil {
		return false, fmt.Errorf("%w: %s", ErrInvalidStoreRecord, name)
	}
	return true, nil
}

func strictJSONDecode(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := rejectDuplicateJSONKeys(decoder); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON")
	}
	decoder = json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	return scanJSONToken(decoder, token)
}

func scanJSONToken(decoder *json.Decoder, token json.Token) error {
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := scanJSONToken(decoder, value); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := scanJSONToken(decoder, value); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

type randomReader struct{ read func([]byte) (int, error) }

func (reader randomReader) Read(data []byte) (int, error) { return reader.read(data) }

func readerFromRandom(read func([]byte) (int, error)) io.Reader { return randomReader{read: read} }
