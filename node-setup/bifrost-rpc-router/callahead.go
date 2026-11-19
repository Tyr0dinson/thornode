package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Call fetch-ahead (opt-in per chain with call_ahead; needs head_cache_ms;
// adapter layer only).
//
// Every solvency_blocks an EVM scanner reports vault solvency, asking for one
// router allowance per token per vault plus each vault's gas balance, all pinned
// to that block. That is on the order of a hundred requests, strictly one after
// another, which takes tens of seconds against a remote provider, and the
// scanner hands over the block's transactions only when the report is done.
//
// With this on, the router remembers which state queries the last such run
// made. When the first query of a known run arrives for a new block it sends
// the rest of the remembered queries for that block at once, a few at a time,
// and answers the scanner's own requests from those replies for callKeep.
//
// Rules:
//   - Only eth_call and eth_getBalance with exactly two params, the second a
//     canonical block height ("0x..."), are learned or answered. Queries at
//     "latest", "pending" or by hash are never touched.
//   - Every answer is the provider's own result for that exact call: the call
//     object or address is resent byte for byte as the scanner sent it last
//     time, with only the height replaced.
//   - Early calls start only when an arriving query matches one in the last
//     run; an unrelated query at some other height is forwarded and starts
//     nothing. A run replaces the remembered one only when it is at least half
//     as long, so a stray query cannot erase it.
//   - A failed, null or error reply is never handed out; the scanner's request
//     is forwarded and gets the provider's reply directly.
//   - Results are pinned to a height, not a hash. Above the safe head a height
//     can be replaced; an early result is then at most callKeep older than a
//     direct answer would be, the same exposure the scanner already has while
//     its serial run is in progress.

// Atomic so tests can shorten it.
var callKeepNS atomic.Int64 // How long an early result is answerable.

func init() { callKeepNS.Store(int64(30 * time.Second)) }

const (
	callMaxRun     = 512     // Queries remembered per run.
	callMinRun     = 4       // Shorter runs are not worth starting early calls for.
	callMaxObject  = 2 << 10 // Largest call object or address remembered, in bytes.
	callWorkers    = 12      // Early calls in flight at once.
	callEntries    = 2048
	callReplyLimit = 1 << 20
)

type callTemplate struct {
	method string
	first  string // params[0] exactly as the scanner sent it
}

func (c callTemplate) key() string { return c.method + " " + c.first }

func (c callTemplate) params(tag string) string { return "[" + c.first + `,"` + tag + `"]` }

type callAhead struct {
	mu       sync.Mutex
	tag      string // Height of the run now passing through.
	run      []callTemplate
	runSet   map[string]bool
	known    []callTemplate // The last complete run.
	knownSet map[string]bool
	entries  map[string]*aheadEntry // template key + " " + tag
}

// pinned reports a state query made at a canonical block height.
func (q rpcQuery) pinned() (t callTemplate, tag string, ok bool) {
	if q.Method != "eth_call" && q.Method != "eth_getBalance" || len(q.params) != 2 {
		return t, "", false
	}
	tag, isString := q.params[1].(string)
	if _, canonical := parseTag(tag); !isString || !canonical {
		return t, "", false
	}
	var raw []json.RawMessage
	if json.Unmarshal(q.raw, &raw) != nil || len(raw) != 2 || len(raw[0]) > callMaxObject {
		return t, "", false
	}
	return callTemplate{q.Method, string(raw[0])}, tag, true
}

// note records the query in the run for tag and, when it opens a run the
// router knows, returns the early calls to make. It also returns the entry
// already held or in flight for this exact query, if any.
func (a *callAhead) note(t callTemplate, tag string) (e *aheadEntry, start []callTemplate, entries []*aheadEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := t.key()
	if tag != a.tag {
		if len(a.run) >= callMinRun && 2*len(a.run) >= len(a.known) {
			a.known, a.knownSet = a.run, a.runSet
		}
		a.tag, a.run, a.runSet = tag, nil, map[string]bool{}
		keep := time.Duration(callKeepNS.Load())
		for k, old := range a.entries {
			select {
			case <-old.done:
				if time.Since(old.at) > keep {
					delete(a.entries, k)
				}
			default:
			}
		}
		if a.knownSet[key] && len(a.known) >= callMinRun {
			if a.entries == nil {
				a.entries = map[string]*aheadEntry{}
			}
			for _, k := range a.known {
				id := k.key() + " " + tag
				if a.entries[id] != nil || len(a.entries) >= callEntries {
					continue
				}
				fresh := &aheadEntry{done: make(chan struct{})}
				a.entries[id] = fresh
				start, entries = append(start, k), append(entries, fresh)
			}
		}
	}
	if !a.runSet[key] && len(a.run) < callMaxRun {
		a.runSet[key] = true
		a.run = append(a.run, t)
	}
	return a.entries[key+" "+tag], start, entries
}

// callFetch makes the early calls for one run, callWorkers at a time.
func (u *upstream) callFetch(path, tag string, calls []callTemplate, entries []*aheadEntry) {
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < callWorkers && w < len(calls); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				u.ahead.fetches.Add(1)
				result, ok := u.fetchRPC(&url.URL{Path: path}, nil, calls[i].method, calls[i].params(tag), callReplyLimit)
				finish(entries[i], result, ok)
			}
		}()
	}
	for i := range calls {
		next <- i
	}
	close(next)
	wg.Wait()
}

// callTry answers a pinned state query from an early call when call_ahead is on.
func (u *upstream) callTry(ch Chain, req *http.Request, q rpcQuery) (json.RawMessage, bool) {
	if !ch.CallAhead {
		return nil, false
	}
	t, tag, ok := q.pinned()
	if !ok {
		return nil, false
	}
	e, start, entries := u.calls.note(t, tag)
	if len(start) > 0 {
		go u.callFetch(req.URL.Path, tag, start, entries)
	}
	if e == nil {
		return nil, false
	}
	return u.calls.wait(req.Context(), e, &u.ahead.hits)
}

func (a *callAhead) wait(ctx context.Context, e *aheadEntry, hits *atomic.Uint64) (json.RawMessage, bool) {
	select {
	case <-e.done:
	case <-ctx.Done():
		return nil, false
	}
	if !e.ok || time.Since(e.at) > time.Duration(callKeepNS.Load()) {
		return nil, false
	}
	hits.Add(1)
	return e.result, true
}
