package store

import (
	"errors"
	"testing"
)

func TestReadRevisionIDsReturnsTerminalIteratorError(t *testing.T) {
	wantErr := errors.New("injected iterator failure")
	rows := &failingRevisionIDRows{err: wantErr}

	_, err := readRevisionIDs(rows)
	if !errors.Is(err, wantErr) {
		t.Fatalf("readRevisionIDs() error = %v, want injected iterator failure", err)
	}
}

type failingRevisionIDRows struct {
	err error
}

func (*failingRevisionIDRows) Next() bool        { return false }
func (*failingRevisionIDRows) Scan(...any) error { return nil }
func (rows *failingRevisionIDRows) Err() error   { return rows.err }
