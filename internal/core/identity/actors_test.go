package identity

import (
	"context"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestRecordActorActionInTxRejectsInvalidInputsBeforeSQL(t *testing.T) {
	var s *Store
	if _, err := s.RecordActorActionInTx(context.Background(), nil, types.ActorScope{}, "x", []byte(`{}`)); err == nil {
		t.Fatal("nil store/transaction accepted")
	}
}
