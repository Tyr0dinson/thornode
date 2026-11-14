package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

var errBusy = errors.New("admission unavailable")

type admission struct {
	active, waiting chan struct{}
	queue           time.Duration
	rejected        atomic.Uint64
}

func (a *admission) acquire(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case a.active <- struct{}{}:
		if ctx.Err() != nil {
			a.release()
			return ctx.Err()
		}
		return nil
	default:
	}
	select {
	case a.waiting <- struct{}{}:
		defer func() { <-a.waiting }()
	default:
		a.rejected.Add(1)
		return errBusy
	}
	t := time.NewTimer(a.queue)
	defer t.Stop()
	select {
	case a.active <- struct{}{}:
		if ctx.Err() != nil {
			a.release()
			return ctx.Err()
		}
		return nil
	case <-ctx.Done():
		a.rejected.Add(1)
		return ctx.Err()
	case <-t.C:
		a.rejected.Add(1)
		return errBusy
	}
}

func (a *admission) release() { <-a.active }
