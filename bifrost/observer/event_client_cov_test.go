package observer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

// streamCov delivers events from a channel and fails when the subscription
// context ends.
type streamCov struct {
	grpc.ClientStream
	ctx    context.Context
	events chan *ebifrost.EventNotification
}

func (s *streamCov) Recv() (*ebifrost.EventNotification, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case ev := <-s.events:
		return ev, nil
	}
}

type bifrostClientCov struct {
	ebifrost.LocalhostBifrostClient

	mu         sync.Mutex
	subscribed [][]string
	err        error
	streams    []chan *ebifrost.EventNotification // one per subscription
	attempt    chan struct{}
}

func (c *bifrostClientCov) SubscribeToEvents(ctx context.Context, in *ebifrost.SubscribeRequest, _ ...grpc.CallOption) (ebifrost.LocalhostBifrost_SubscribeToEventsClient, error) {
	c.mu.Lock()
	c.subscribed = append(c.subscribed, append([]string(nil), in.EventTypes...))
	err := c.err
	var events chan *ebifrost.EventNotification
	if err == nil {
		events = make(chan *ebifrost.EventNotification)
		c.streams = append(c.streams, events)
	}
	c.mu.Unlock()
	select {
	case c.attempt <- struct{}{}:
	default:
	}
	if err != nil {
		return nil, err
	}
	return &streamCov{ctx: ctx, events: events}, nil
}

// send delivers an event on the newest subscription's stream only. Each
// subscription has its own channel, so a Recv goroutine left over from a
// cancelled subscription can never take an event meant for the current one.
func (c *bifrostClientCov) send(ev *ebifrost.EventNotification) {
	c.mu.Lock()
	events := c.streams[len(c.streams)-1]
	c.mu.Unlock()
	events <- ev
}

func (c *bifrostClientCov) subscriptions() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.subscribed...)
}

func waitCov(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestEventClientResubscribesOnChangesCov(t *testing.T) {
	client := &bifrostClientCov{
		attempt: make(chan struct{}, 10),
	}
	ec := NewEventClient(client)

	gotA := make(chan *ebifrost.EventNotification, 1)
	ec.RegisterHandler("a", func(ev *ebifrost.EventNotification) { gotA <- ev })
	ec.RegisterHandler("a", func(ev *ebifrost.EventNotification) { gotA <- ev }) // duplicate type is not re-added
	ec.Start()
	waitCov(t, client.attempt)

	// an event of a registered type reaches its handler
	client.send(&ebifrost.EventNotification{EventType: "a", Payload: []byte("1")})
	select {
	case ev := <-gotA:
		if string(ev.Payload) != "1" {
			t.Fatalf("unexpected payload %q", ev.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler not called")
	}
	// an event without a handler is dropped
	client.send(&ebifrost.EventNotification{EventType: "unknown"})

	// registering while active cancels the subscription; Start resubscribes
	// with the new event set
	gotB := make(chan struct{}, 1)
	ec.RegisterHandler("b", func(*ebifrost.EventNotification) { gotB <- struct{}{} })
	ec.Start()
	waitCov(t, client.attempt)

	// Start while active restarts the subscription
	ec.Start()
	waitCov(t, client.attempt)
	client.send(&ebifrost.EventNotification{EventType: "b"})
	waitCov(t, gotB)

	ec.CleanShutdown()
	ec.CleanShutdown() // idempotent

	subs := client.subscriptions()
	if len(subs) != 3 {
		t.Fatalf("expected 3 subscriptions, got %v", subs)
	}
	if len(subs[0]) != 1 || subs[0][0] != "a" {
		t.Fatalf("first subscription %v", subs[0])
	}
	for _, s := range subs[1:] {
		if len(s) != 2 || s[0] != "a" || s[1] != "b" {
			t.Fatalf("resubscription %v", s)
		}
	}
}

func TestEventClientStopDuringBackoffCov(t *testing.T) {
	client := &bifrostClientCov{
		err:     errors.New("unavailable"),
		attempt: make(chan struct{}, 10),
	}
	ec := NewEventClient(client)
	// "Subscription error" is logged after the post-subscribe cancellation
	// check, so once it fires the goroutine can only leave via the backoff wait
	backoff := make(chan struct{}, 10)
	ec.logger = ec.logger.Hook(zerolog.HookFunc(func(_ *zerolog.Event, _ zerolog.Level, msg string) {
		if msg == "Subscription error" {
			backoff <- struct{}{}
		}
	}))
	ec.RegisterHandler("a", func(*ebifrost.EventNotification) {})
	ec.Start()
	waitCov(t, backoff)

	// the first failure enters a >=1s backoff; Stop must interrupt it promptly
	start := time.Now()
	ec.Stop()
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("stop took %s", elapsed)
	}
	if n := len(client.subscriptions()); n != 1 {
		t.Fatalf("expected a single attempt, got %d", n)
	}

	// shutdown before any start is safe, and a canceled context is reported
	idle := NewEventClient(client)
	idle.CleanShutdown()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := idle.subscribe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
