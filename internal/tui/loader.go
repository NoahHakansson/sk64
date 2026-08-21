package tui

import (
	"context"
	"sync/atomic"
)

type loader struct {
	reqID   int
	pending bool
	cancel  context.CancelFunc
}

var lastReqID atomic.Int64

func (l *loader) start(parent context.Context) (context.Context, int) {
	if l.cancel != nil {
		l.cancel()
	}
	l.reqID = int(lastReqID.Add(1))
	l.pending = true
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	return ctx, l.reqID
}

func (l *loader) finish(reqID int) bool {
	if reqID != l.reqID || !l.pending {
		return false
	}
	l.pending = false
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	return true
}

func (l *loader) stop() bool {
	if !l.pending {
		return false
	}
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.pending = false
	return true
}

func stopLoadingScreens(screens []screen) {
	for _, current := range screens {
		if current, ok := current.(interface{ stop() bool }); ok {
			current.stop()
		}
	}
}
