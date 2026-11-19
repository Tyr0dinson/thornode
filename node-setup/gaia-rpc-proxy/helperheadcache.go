package main

import (
	"context"
	"encoding/json"
	"time"
)

// One immutable response, at most gaiaMaxResponse bytes, shared for at most one
// second from fetch start. The gate also serializes cache fills without workers.
type gaiaHeadCache struct {
	gate   chan struct{}
	fields map[string]json.RawMessage
	until  time.Time
}

func gaiaHeadForID(fields map[string]json.RawMessage, id json.RawMessage) ([]byte, error) {
	response := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		response[key] = value
	}
	response["id"] = id
	data, err := json.Marshal(response)
	if err != nil {
		return nil, gaiaFailed("response_encode", 0)
	}
	if len(data) > gaiaMaxResponse {
		return nil, gaiaFailed("body_size", 0)
	}
	return data, nil
}

func (g *GaiaProxy) primaryHead(ctx context.Context, q gaiaRequest) ([]byte, error) {
	select {
	case g.head.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, gaiaContextFailure(ctx)
	}
	defer func() { <-g.head.gate }()
	if ctx.Err() != nil {
		return nil, gaiaContextFailure(ctx)
	}
	if g.head.fields != nil && g.now().Before(g.head.until) {
		g.headHits.Add(1)
		return gaiaHeadForID(g.head.fields, q.ID)
	}
	// A failed refresh must not leave an old response available to another caller.
	g.head.fields = nil
	g.head.until = time.Time{}
	g.headMisses.Add(1)
	g.primaryReads.Add(1)
	until := g.now().Add(time.Second)
	data, e, err := g.call(ctx, g.primary, q)
	if err != nil {
		return nil, err
	}
	if e.Error != nil {
		g.logFailure(q, gaiaAt("primary_head", nil))
		return data, nil
	}
	if ctx.Err() != nil {
		return nil, gaiaContextFailure(ctx)
	}
	if g.now().Before(until) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return nil, gaiaFailed("decode", 0)
		}
		g.head.fields, g.head.until = fields, until
	}
	return data, nil
}
