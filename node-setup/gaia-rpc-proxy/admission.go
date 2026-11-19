package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

const (
	maxActiveRequests = 8
	maxQueuedRequests = 16
	// Leave time for the small request body within the server's 10s read deadline.
	requestAdmissionTimeout = 5 * time.Second
)

var (
	errAdmissionFull    = errors.New("request waiting room full")
	errAdmissionTimeout = errors.New("request admission timed out")
)

type requestAdmission struct {
	active, waiting              chan struct{}
	waitTimeout                  time.Duration
	rejected, timedOut, canceled atomic.Uint64
}

func newRequestAdmission() *requestAdmission {
	return &requestAdmission{
		active:      make(chan struct{}, maxActiveRequests),
		waiting:     make(chan struct{}, maxQueuedRequests),
		waitTimeout: requestAdmissionTimeout,
	}
}

func (a *requestAdmission) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		a.canceled.Add(1)
		return err
	}
	select {
	case a.active <- struct{}{}:
		if err := ctx.Err(); err != nil {
			a.release()
			a.canceled.Add(1)
			return err
		}
		return nil
	default:
	}
	// Waiting requests retain no parsed body or upstream response buffers.
	select {
	case a.waiting <- struct{}{}:
		defer func() { <-a.waiting }()
	default:
		a.rejected.Add(1)
		return errAdmissionFull
	}
	waitCtx, cancel := context.WithTimeout(ctx, a.waitTimeout)
	defer cancel()
	select {
	case a.active <- struct{}{}:
		// A permit and cancellation may become ready together; cancellation wins.
		if waitCtx.Err() == nil {
			return nil
		}
		a.release()
	case <-waitCtx.Done():
	}
	if err := ctx.Err(); err != nil {
		a.canceled.Add(1)
		return err
	}
	a.timedOut.Add(1)
	return errAdmissionTimeout
}

func (a *requestAdmission) release() { <-a.active }
