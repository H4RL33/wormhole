package sync

import (
	"context"
	"errors"
	"sync"
)

type V2Engine struct {
	statusMu sync.RWMutex
	status   Status
}

func NewV2Engine() *V2Engine {
	return &V2Engine{status: Status{State: StateOffline, PendingWrites: 0}}
}

func (e *V2Engine) Status(context.Context) (Status, error) {
	if e == nil {
		return Status{}, errors.New("sync: status unavailable")
	}
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	return e.status, nil
}
