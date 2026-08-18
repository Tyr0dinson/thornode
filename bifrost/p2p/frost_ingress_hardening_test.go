package p2p

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type frostBudgetState struct {
	nodeBytes      uint64
	inFlightFrames uint32
	activeSessions uint32
}

func snapshotFrostBudget(budget *frostIngressBudget) frostBudgetState {
	budget.lock.Lock()
	defer budget.lock.Unlock()
	return frostBudgetState{
		nodeBytes:      budget.nodeBytes,
		inFlightFrames: budget.inFlightFrames,
		activeSessions: budget.activeSessions,
	}
}

func reserveAndAcceptTSSFrame(
	t *testing.T,
	comm *Communication,
	topic messages.THORChainTSSMessageType,
	msgID string,
	remote peer.ID,
	payloadBytes uint32,
) bool {
	t.Helper()
	_, _, reservation, err := comm.preflightTSSFrame(
		remote,
		payloadBytes,
		tssFramePrefix(t, topic, msgID),
	)
	if err != nil {
		return false
	}
	if reservation == nil || !reservation.accept() {
		t.Fatal("authorized preflight reservation could not be accepted")
	}
	return true
}

func TestFrostIngressBudgetAggregatesAcrossTopicsAndCeremonies(t *testing.T) {
	remote := peer.ID("aggregate-peer")
	comm := newTestCommunication()
	comm.frostBudget = newFrostIngressBudget(12, 8, 12, 16, 8, 2)

	channelA := make(chan *Message, 1)
	comm.SetSubscribeWithPeers(
		messages.TSSFrostDKGRound1Msg,
		"ceremony-a",
		channelA,
		[]peer.ID{remote},
		16,
		16,
	)
	comm.SetSubscribeWithPeers(
		messages.TSSFrostDKGRound2Msg,
		"ceremony-a",
		channelA,
		[]peer.ID{remote},
		16,
		16,
	)
	comm.SetSubscribeWithPeers(
		messages.TSSFrostShareMsg,
		"ceremony-b",
		make(chan *Message, 1),
		[]peer.ID{remote},
		16,
		16,
	)

	if !reserveAndAcceptTSSFrame(t, comm, messages.TSSFrostDKGRound1Msg, "ceremony-a", remote, 6) {
		t.Fatal("first topic should fit the ceremony budget")
	}
	if !reserveAndAcceptTSSFrame(t, comm, messages.TSSFrostDKGRound2Msg, "ceremony-a", remote, 2) {
		t.Fatal("second topic should consume the remaining ceremony budget")
	}
	if reserveAndAcceptTSSFrame(t, comm, messages.TSSFrostDKGRound2Msg, "ceremony-a", remote, 1) {
		t.Fatal("a second topic must not receive an independent ceremony byte budget")
	}
	if !reserveAndAcceptTSSFrame(t, comm, messages.TSSFrostShareMsg, "ceremony-b", remote, 4) {
		t.Fatal("second ceremony should consume the remaining node-wide budget")
	}
	if reserveAndAcceptTSSFrame(t, comm, messages.TSSFrostShareMsg, "ceremony-b", remote, 1) {
		t.Fatal("concurrent ceremonies must share one node-wide byte budget")
	}

	if comm.frostBudget.nodeBytes != 12 {
		t.Fatalf("node budget used %d bytes, want 12", comm.frostBudget.nodeBytes)
	}
	if got := comm.frostBudget.sessions["ceremony-a"].bytes; got != 8 {
		t.Fatalf("ceremony A used %d bytes, want 8", got)
	}
	if got := comm.frostBudget.sessions["ceremony-b"].bytes; got != 4 {
		t.Fatalf("ceremony B used %d bytes, want 4", got)
	}

	comm.CancelSubscribe(messages.TSSFrostDKGRound1Msg, "ceremony-a")
	comm.CancelSubscribe(messages.TSSFrostDKGRound2Msg, "ceremony-a")
	comm.CancelSubscribe(messages.TSSFrostShareMsg, "ceremony-b")
	if comm.frostBudget.nodeBytes != 0 ||
		comm.frostBudget.inFlightFrames != 0 ||
		comm.frostBudget.activeSessions != 0 {
		t.Fatalf(
			"session teardown leaked node budget: bytes=%d frames=%d sessions=%d",
			comm.frostBudget.nodeBytes,
			comm.frostBudget.inFlightFrames,
			comm.frostBudget.activeSessions,
		)
	}
}

func TestFrostIngressBudgetBoundsConcurrentPreflight(t *testing.T) {
	const maxInFlight = 8
	remote := peer.ID("concurrent-peer")
	comm := newTestCommunication()
	comm.frostBudget = newFrostIngressBudget(64, 64, 64, maxInFlight, maxInFlight, 2)
	comm.SetSubscribeWithPeers(
		messages.TSSFrostPreprocessMsg,
		"concurrent-a",
		make(chan *Message, 1),
		[]peer.ID{remote},
		16,
		64,
	)
	prefixes := map[messages.THORChainTSSMessageType][]byte{
		messages.TSSFrostPreprocessMsg: tssFramePrefix(t, messages.TSSFrostPreprocessMsg, "concurrent-a"),
		messages.TSSFrostShareMsg:      tssFramePrefix(t, messages.TSSFrostShareMsg, "concurrent-b"),
	}
	comm.SetSubscribeWithPeers(
		messages.TSSFrostShareMsg,
		"concurrent-b",
		make(chan *Message, 1),
		[]peer.ID{remote},
		16,
		64,
	)

	type result struct {
		reservation *messageReservation
	}
	results := make(chan result, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			topic := messages.TSSFrostPreprocessMsg
			if index%2 == 1 {
				topic = messages.TSSFrostShareMsg
			}
			_, _, reservation, _ := comm.preflightTSSFrame(
				remote,
				1,
				prefixes[topic],
			)
			results <- result{reservation: reservation}
		}(i)
	}
	wg.Wait()
	close(results)

	var accepted []*messageReservation
	for result := range results {
		if result.reservation != nil {
			accepted = append(accepted, result.reservation)
		}
	}
	if len(accepted) != maxInFlight {
		t.Fatalf("accepted %d concurrent frames, want hard cap %d", len(accepted), maxInFlight)
	}
	if state := snapshotFrostBudget(comm.frostBudget); state.inFlightFrames != maxInFlight {
		t.Fatalf("in-flight accounting=%d, want %d", state.inFlightFrames, maxInFlight)
	}
	for _, reservation := range accepted {
		reservation.reject()
	}
	if state := snapshotFrostBudget(comm.frostBudget); state.nodeBytes != 0 || state.inFlightFrames != 0 {
		t.Fatalf(
			"concurrent reservation refund leaked capacity: bytes=%d frames=%d",
			state.nodeBytes,
			state.inFlightFrames,
		)
	}
}

func TestFrostIngressBudgetIsolatesStalledCeremonies(t *testing.T) {
	const (
		globalFrames     = 8
		framesPerSession = 4
	)
	remote := peer.ID("stalled-ceremony-peer")
	comm := newTestCommunication()
	comm.frostBudget = newFrostIngressBudget(
		64,
		64,
		64,
		globalFrames,
		framesPerSession,
		2,
	)
	for _, msgID := range []string{"stalled-a", "healthy-b"} {
		comm.SetSubscribeWithPeers(
			messages.TSSFrostShareMsg,
			msgID,
			make(chan *Message, 1),
			[]peer.ID{remote},
			16,
			16,
		)
	}

	var reservations []*messageReservation
	for frame := uint32(0); frame < framesPerSession; frame++ {
		_, _, reservation, err := comm.preflightTSSFrame(
			remote,
			1,
			tssFramePrefix(t, messages.TSSFrostShareMsg, "stalled-a"),
		)
		if err != nil || reservation == nil {
			t.Fatalf("stalled ceremony frame %d rejected below its isolated cap: %v", frame+1, err)
		}
		reservations = append(reservations, reservation)
	}
	if _, _, _, err := comm.preflightTSSFrame(
		remote,
		1,
		tssFramePrefix(t, messages.TSSFrostShareMsg, "stalled-a"),
	); err == nil {
		t.Fatal("stalled ceremony exceeded its isolated in-flight cap")
	}

	for frame := uint32(0); frame < framesPerSession; frame++ {
		_, _, reservation, err := comm.preflightTSSFrame(
			remote,
			1,
			tssFramePrefix(t, messages.TSSFrostShareMsg, "healthy-b"),
		)
		if err != nil || reservation == nil {
			t.Fatalf("healthy ceremony frame %d lost its reserved quota: %v", frame+1, err)
		}
		reservations = append(reservations, reservation)
	}
	for _, reservation := range reservations {
		reservation.reject()
	}
}

func TestFrostBroadcastSchedulerIsolatesCeremonies(t *testing.T) {
	scheduler := newFrostBroadcastScheduler(4, 2, FrostMaxPendingBroadcasts)
	const stalledBacklog = 300
	for i := 0; i < stalledBacklog; i++ {
		if !scheduler.enqueue(frostBroadcastTask{msgID: "stalled-a"}) {
			t.Fatalf("enqueue stalled-a task %d", i+1)
		}
	}
	if !scheduler.enqueue(frostBroadcastTask{msgID: "healthy-b"}) {
		t.Fatal("a legitimate >256-message ceremony backlog hid healthy-b")
	}

	first, ok := scheduler.next()
	if !ok || first.msgID != "stalled-a" {
		t.Fatalf("first scheduled task=%q, want stalled-a", first.msgID)
	}
	second, ok := scheduler.next()
	if !ok || second.msgID != "healthy-b" {
		t.Fatalf("round-robin isolation scheduled %q before healthy-b", second.msgID)
	}
	third, ok := scheduler.next()
	if !ok || third.msgID != "stalled-a" {
		t.Fatalf("third scheduled task=%q, want stalled-a", third.msgID)
	}
	if _, hasNext := scheduler.next(); hasNext {
		t.Fatal("stalled ceremony exceeded its per-session active writer cap")
	}

	scheduler.complete("stalled-a", false)
	fourth, ok := scheduler.next()
	if !ok || fourth.msgID != "stalled-a" {
		t.Fatal("stalled ceremony did not resume after one of its writers completed")
	}
}

func TestCancelFrostBroadcastsDropsStaleQueueAndReleasesNextCeremony(t *testing.T) {
	scheduler := newFrostBroadcastScheduler(4, 2, FrostMaxPendingBroadcasts)
	for i := 0; i < 384; i++ {
		if !scheduler.enqueue(frostBroadcastTask{msgID: "timed-out-dkg"}) {
			t.Fatalf("enqueue stale DKG task %d", i+1)
		}
	}
	first, ok := scheduler.next()
	if !ok || first.msgID != "timed-out-dkg" {
		t.Fatal("stale DKG did not start its first bounded writer")
	}
	second, ok := scheduler.next()
	if !ok || second.msgID != "timed-out-dkg" {
		t.Fatal("stale DKG did not start its second bounded writer")
	}
	if !scheduler.enqueue(frostBroadcastTask{msgID: "next-keysign"}) {
		t.Fatal("failed to enqueue next ceremony")
	}

	if dropped := scheduler.cancel("timed-out-dkg"); dropped != 382 {
		t.Fatalf("canceled %d stale tasks, want 382", dropped)
	}
	next, ok := scheduler.next()
	if !ok || next.msgID != "next-keysign" {
		t.Fatalf("canceled stale queue did not release next ceremony: %+v", next)
	}
	if scheduler.pending != 0 {
		t.Fatalf("scheduler retained %d pending tasks after cancellation", scheduler.pending)
	}
	scheduler.complete("timed-out-dkg", false)
	scheduler.complete("timed-out-dkg", false)
	scheduler.complete("next-keysign", false)
	if scheduler.active != 0 {
		t.Fatalf("late completions leaked %d active writers", scheduler.active)
	}
}

func TestCancelFrostBroadcastsPreservesAndPrioritizesTerminalResult(t *testing.T) {
	scheduler := newFrostBroadcastScheduler(4, 2, FrostMaxPendingBroadcasts)
	for i := 0; i < 10; i++ {
		if !scheduler.enqueue(frostBroadcastTask{msgID: "completed-keysign"}) {
			t.Fatalf("enqueue round task %d", i+1)
		}
	}
	if !scheduler.enqueue(frostBroadcastTask{
		msgID:    "completed-keysign",
		terminal: true,
	}) {
		t.Fatal("enqueue terminal result")
	}

	if dropped := scheduler.cancel("completed-keysign"); dropped != 10 {
		t.Fatalf("canceled %d round tasks, want 10", dropped)
	}
	if scheduler.pending != 1 {
		t.Fatalf("scheduler retained %d tasks after cancellation, want terminal result only", scheduler.pending)
	}
	first, ok := scheduler.next()
	if !ok || !first.terminal || first.msgID != "completed-keysign" {
		t.Fatal("terminal result was not preserved and prioritized after cancellation")
	}
	scheduler.complete("completed-keysign", true)
	if scheduler.pending != 0 || scheduler.active != 0 {
		t.Fatalf("terminal completion left pending=%d active=%d", scheduler.pending, scheduler.active)
	}
}

func TestCancelFrostBroadcastsPreservesAllTerminalQueue(t *testing.T) {
	scheduler := newFrostBroadcastScheduler(4, 2, FrostMaxPendingBroadcasts)
	expected := []frostBroadcastTask{
		{
			msgID:    "completed-keysign",
			msg:      []byte("keysign-result"),
			msgType:  messages.TSSFrostResultMsg,
			terminal: true,
		},
		{
			msgID:    "completed-keysign",
			msg:      []byte("dkg-result"),
			msgType:  messages.TSSFrostDKGResultMsg,
			terminal: true,
		},
	}
	for i, task := range expected {
		if !scheduler.enqueue(task) {
			t.Fatalf("enqueue terminal task %d", i+1)
		}
	}

	// Repeated cancellation used to clear the live backing array on the first
	// call while leaving pending and ready bookkeeping unchanged.
	for iteration := 0; iteration < 8; iteration++ {
		if dropped := scheduler.cancel("completed-keysign"); dropped != 0 {
			t.Fatalf("iteration %d canceled %d terminal tasks", iteration, dropped)
		}
		if scheduler.pending != len(expected) {
			t.Fatalf("iteration %d pending=%d, want %d", iteration, scheduler.pending, len(expected))
		}
		queue := scheduler.pendingByID["completed-keysign"]
		if len(queue) != len(expected) {
			t.Fatalf("iteration %d queue length=%d, want %d", iteration, len(queue), len(expected))
		}
		for i := range expected {
			if queue[i].msgID != expected[i].msgID ||
				queue[i].msgType != expected[i].msgType ||
				queue[i].terminal != expected[i].terminal ||
				string(queue[i].msg) != string(expected[i].msg) {
				t.Fatalf("iteration %d terminal task %d changed: %+v", iteration, i, queue[i])
			}
		}
	}

	for i := range expected {
		task, ok := scheduler.next()
		if !ok {
			t.Fatalf("terminal task %d was lost", i)
		}
		if task.msgID != expected[i].msgID || task.msgType != expected[i].msgType ||
			!task.terminal || string(task.msg) != string(expected[i].msg) {
			t.Fatalf("terminal task %d changed during dispatch: %+v", i, task)
		}
		scheduler.complete(task.msgID, task.terminal)
	}
	if scheduler.pending != 0 || scheduler.active != 0 ||
		len(scheduler.pendingByID) != 0 || len(scheduler.activeByMsgID) != 0 {
		t.Fatalf(
			"terminal dispatch leaked scheduler state: pending=%d active=%d pending_ids=%d active_ids=%d",
			scheduler.pending,
			scheduler.active,
			len(scheduler.pendingByID),
			len(scheduler.activeByMsgID),
		)
	}
}

func TestDrainCanceledFrostIngressRequeuesWhenDeferredFull(t *testing.T) {
	comm := &Communication{
		logger:                zerolog.Nop(),
		stopChan:              make(chan struct{}),
		FrostBroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 4),
	}
	fixtures := []struct {
		msgID   string
		msgType messages.THORChainTSSMessageType
	}{
		{msgID: "cancel-me", msgType: messages.TSSFrostShareMsg},
		{msgID: "keep-one", msgType: messages.TSSFrostShareMsg},
		{msgID: "cancel-me", msgType: messages.TSSFrostResultMsg},
		{msgID: "keep-two", msgType: messages.TSSFrostDKGRound1Msg},
	}
	for _, fixture := range fixtures {
		if err := comm.QueueFrostBroadcast(&messages.BroadcastMsgChan{
			WrappedMessage: messages.WrappedMessage{
				MessageType: fixture.msgType,
				MsgID:       fixture.msgID,
			},
		}); err != nil {
			t.Fatalf("queue %s: %v", fixture.msgID, err)
		}
	}

	dropped := comm.drainCanceledFrostIngress("cancel-me", func(frostBroadcastTask) bool {
		return false // model a full deferred result reserve
	})
	if dropped != 1 {
		t.Fatalf("dropped %d canceled messages, want 1", dropped)
	}
	want := fixtures[1:]
	if len(comm.FrostBroadcastMsgChan) != len(want) {
		t.Fatalf("preserved queue length=%d, want %d", len(comm.FrostBroadcastMsgChan), len(want))
	}
	for i, expected := range want {
		got := <-comm.FrostBroadcastMsgChan
		if got.WrappedMessage.MsgID != expected.msgID || got.WrappedMessage.MessageType != expected.msgType {
			t.Fatalf("preserved message %d=%s/%s, want %s/%s",
				i,
				got.WrappedMessage.MsgID,
				got.WrappedMessage.MessageType,
				expected.msgID,
				expected.msgType,
			)
		}
	}
}

func TestCancelFrostBroadcastsDrainsIngressButDeliversTerminalResult(t *testing.T) {
	stop := make(chan struct{})
	releaseRoundWriters := make(chan struct{})
	roundStarted := make(chan struct{}, FrostMaxConcurrentBroadcastsPerSession)
	terminalSent := make(chan struct{}, 1)
	nextCeremonySent := make(chan struct{}, 1)

	comm := &Communication{
		logger:                   zerolog.Nop(),
		wg:                       &sync.WaitGroup{},
		stopChan:                 stop,
		BroadcastMsgChan:         make(chan *messages.BroadcastMsgChan, 1),
		FrostBroadcastMsgChan:    make(chan *messages.BroadcastMsgChan, FrostMaxPendingBroadcasts+32),
		cancelFrostBroadcastChan: make(chan frostBroadcastCancelRequest, 1),
		broadcastToPeersFn: func(_ []peer.ID, payload []byte, msgID string, closeAfterWrite bool) {
			if !closeAfterWrite {
				return
			}
			var wrapped messages.WrappedMessage
			if err := json.Unmarshal(payload, &wrapped); err != nil {
				return
			}
			switch {
			case msgID == "completed-keysign" && wrapped.MessageType == messages.TSSFrostResultMsg:
				terminalSent <- struct{}{}
			case msgID == "completed-keysign":
				roundStarted <- struct{}{}
				<-releaseRoundWriters
			case msgID == "next-dkg":
				nextCeremonySent <- struct{}{}
			}
		},
	}
	comm.wg.Add(1)
	go comm.ProcessBroadcast()

	roundMessage := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "completed-keysign",
			Payload:     []byte("round"),
		},
		PeersID: []peer.ID{peer.ID("offline-xmr-peer")},
	}
	for i := 0; i < FrostMaxPendingBroadcasts+8; i++ {
		comm.FrostBroadcastMsgChan <- roundMessage
	}
	for i := uint32(0); i < FrostMaxConcurrentBroadcastsPerSession; i++ {
		select {
		case <-roundStarted:
		case <-time.After(time.Second):
			t.Fatal("FROST scheduler did not start bounded round writers")
		}
	}
	comm.FrostBroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostResultMsg,
			MsgID:       "completed-keysign",
			Payload:     []byte("terminal"),
		},
		PeersID: []peer.ID{peer.ID("standby-xmr-peer")},
	}

	// Cancellation is an acknowledgment barrier: all ordinary traffic already
	// queued by this coordinator has been removed when it returns, including
	// traffic not yet admitted by the scheduler. It also waits for old active
	// writers, so a retry cannot reuse this deterministic msgID while a prior
	// attempt can still deliver round traffic.
	cancelReturned := make(chan struct{})
	go func() {
		comm.CancelFrostBroadcasts("completed-keysign")
		close(cancelReturned)
	}()
	select {
	case <-cancelReturned:
		t.Fatal("cancellation acknowledged while old round writers were active")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRoundWriters)
	select {
	case <-cancelReturned:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not acknowledge after old writers quiesced")
	}
	if got := len(comm.FrostBroadcastMsgChan); got != 0 {
		t.Fatalf("cancellation left %d stale ingress messages", got)
	}
	select {
	case <-terminalSent:
	case <-time.After(time.Second):
		t.Fatal("terminal result was canceled with obsolete round traffic")
	}

	comm.FrostBroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostDKGRound1Msg,
			MsgID:       "next-dkg",
			Payload:     []byte("next"),
		},
		PeersID: []peer.ID{peer.ID("healthy-xmr-peer")},
	}
	select {
	case <-nextCeremonySent:
	case <-time.After(time.Second):
		t.Fatal("canceled ingress backlog blocked the next ceremony")
	}

	close(stop)
	comm.wg.Wait()
}

func TestFrostQueueFailsFastUnderRoundSaturationButAdmitsTerminalResult(t *testing.T) {
	comm := &Communication{
		stopChan:                    make(chan struct{}),
		BroadcastMsgChan:            make(chan *messages.BroadcastMsgChan, 1),
		FrostBroadcastMsgChan:       make(chan *messages.BroadcastMsgChan, FrostMaxConcurrentBroadcasts),
		FrostResultBroadcastMsgChan: make(chan *messages.BroadcastMsgChan, FrostMaxPendingResults),
		cancelFrostBroadcastChan:    make(chan frostBroadcastCancelRequest, 1),
	}
	round := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "saturated-round",
		},
	}
	for i := uint32(0); i < FrostMaxConcurrentBroadcasts; i++ {
		if err := comm.QueueFrostBroadcast(round); err != nil {
			t.Fatalf("fill production ingress slot %d: %v", i+1, err)
		}
	}

	start := time.Now()
	if err := comm.QueueFrostBroadcast(round); err == nil {
		t.Fatal("ordinary round traffic exceeded bounded production ingress")
	}
	if elapsed := time.Since(start); elapsed > 2*FrostBroadcastEnqueueTimeout {
		t.Fatalf("saturated ordinary enqueue blocked coordinator for %s", elapsed)
	}

	result := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostResultMsg,
			MsgID:       "saturated-round",
		},
	}
	if err := comm.QueueFrostBroadcast(result); err != nil {
		t.Fatalf("round saturation blocked terminal result admission: %v", err)
	}
	if got := len(comm.FrostResultBroadcastMsgChan); got != 1 {
		t.Fatalf("terminal result queue length=%d, want 1", got)
	}
}

func TestQueueFrostBroadcastRejectsOversizedEncodedFrame(t *testing.T) {
	// Base64 expansion keeps the raw payload below the limit while pushing the
	// encoded outer frame above it.
	payload := bytes.Repeat([]byte{0x42}, 3*FrostMaxSessionPayload/4)
	if len(payload) >= FrostMaxSessionPayload {
		t.Fatalf("raw test payload is %d bytes, want less than %d", len(payload), FrostMaxSessionPayload)
	}

	for _, tc := range []struct {
		name    string
		msgType messages.THORChainTSSMessageType
	}{
		{name: "ordinary", msgType: messages.TSSFrostShareMsg},
		{name: "terminal", msgType: messages.TSSFrostResultMsg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &messages.BroadcastMsgChan{
				WrappedMessage: messages.WrappedMessage{
					MessageType: tc.msgType,
					MsgID:       "oversized-frame",
					Payload:     payload,
				},
			}
			encoded, err := json.Marshal(msg.WrappedMessage)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) <= FrostMaxSessionPayload {
				t.Fatalf("encoded test frame is %d bytes, want more than %d", len(encoded), FrostMaxSessionPayload)
			}
			if _, err = frostBroadcastTaskFromMessage(msg); err == nil {
				t.Fatal("oversized encoded FROST frame was converted")
			}

			comm := &Communication{
				stopChan:                    make(chan struct{}),
				BroadcastMsgChan:            make(chan *messages.BroadcastMsgChan, 1),
				FrostBroadcastMsgChan:       make(chan *messages.BroadcastMsgChan, 1),
				FrostResultBroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
			}
			if err = comm.QueueFrostBroadcast(msg); err == nil {
				t.Fatal("oversized encoded FROST frame was queued")
			}
			if len(comm.BroadcastMsgChan) != 0 || len(comm.FrostBroadcastMsgChan) != 0 ||
				len(comm.FrostResultBroadcastMsgChan) != 0 {
				t.Fatal("rejected FROST frame changed an ingress queue")
			}
		})
	}
}

func TestTerminalResultProgressesAfterGloballySaturatedProductionIngress(t *testing.T) {
	stop := make(chan struct{})
	releaseWriters := make(chan struct{})
	started := make(chan struct{}, FrostMaxConcurrentBroadcasts)
	terminalSent := make(chan struct{}, 1)
	comm := &Communication{
		logger:                      zerolog.Nop(),
		wg:                          &sync.WaitGroup{},
		stopChan:                    stop,
		BroadcastMsgChan:            make(chan *messages.BroadcastMsgChan, 1),
		FrostBroadcastMsgChan:       make(chan *messages.BroadcastMsgChan, FrostMaxConcurrentBroadcasts),
		FrostResultBroadcastMsgChan: make(chan *messages.BroadcastMsgChan, FrostMaxPendingResults),
		cancelFrostBroadcastChan:    make(chan frostBroadcastCancelRequest, 1),
		broadcastToPeersFn: func(_ []peer.ID, payload []byte, _ string, closeAfterWrite bool) {
			if !closeAfterWrite {
				return
			}
			var wrapped messages.WrappedMessage
			if err := json.Unmarshal(payload, &wrapped); err != nil {
				return
			}
			if wrapped.MessageType == messages.TSSFrostResultMsg {
				terminalSent <- struct{}{}
				return
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-releaseWriters
		},
	}
	comm.wg.Add(1)
	go comm.ProcessBroadcast()

	for i := uint32(0); i < FrostMaxConcurrentBroadcasts; i++ {
		msg := &messages.BroadcastMsgChan{
			WrappedMessage: messages.WrappedMessage{
				MessageType: messages.TSSFrostShareMsg,
				MsgID:       fmt.Sprintf("active-session-%d", i/FrostMaxConcurrentBroadcastsPerSession),
			},
		}
		for {
			if err := comm.QueueFrostBroadcast(msg); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	for i := uint32(0); i < FrostMaxConcurrentBroadcasts; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("failed to saturate the global FROST writer pool")
		}
	}

	backlog := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "backlogged-session",
		},
	}
	accepted := 0
	wantAccepted := FrostMaxPendingBroadcasts + int(FrostMaxConcurrentBroadcasts)
	fillDeadline := time.Now().Add(time.Second)
	for accepted < wantAccepted {
		if err := comm.QueueFrostBroadcast(backlog); err == nil {
			accepted++
			continue
		}
		if time.Now().After(fillDeadline) {
			t.Fatalf("filled only %d/%d saturated scheduler+ingress slots", accepted, wantAccepted)
		}
		time.Sleep(time.Millisecond)
	}
	if err := comm.QueueFrostBroadcast(backlog); err == nil {
		t.Fatal("globally saturated ordinary ingress did not fail fast")
	}

	queueStarted := time.Now()
	if err := comm.QueueFrostBroadcast(&messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostResultMsg,
			MsgID:       "active-session-0",
		},
	}); err != nil {
		t.Fatalf("globally saturated round traffic blocked terminal result: %v", err)
	}
	if elapsed := time.Since(queueStarted); elapsed > 2*FrostBroadcastEnqueueTimeout {
		t.Fatalf("terminal result enqueue blocked for %s", elapsed)
	}

	close(releaseWriters)
	select {
	case <-terminalSent:
	case <-time.After(time.Second):
		t.Fatal("terminal result did not progress once a bounded writer slot opened")
	}
	close(stop)
	comm.wg.Wait()
}

func TestTerminalResultBacklogRemainsBoundedWhileWritersAreStalled(t *testing.T) {
	stop := make(chan struct{})
	releaseWriters := make(chan struct{})
	started := make(chan struct{}, FrostMaxConcurrentBroadcasts)
	comm := &Communication{
		logger:                      zerolog.Nop(),
		wg:                          &sync.WaitGroup{},
		stopChan:                    stop,
		BroadcastMsgChan:            make(chan *messages.BroadcastMsgChan, 1),
		FrostBroadcastMsgChan:       make(chan *messages.BroadcastMsgChan, FrostMaxConcurrentBroadcasts),
		FrostResultBroadcastMsgChan: make(chan *messages.BroadcastMsgChan, FrostMaxPendingResults),
		cancelFrostBroadcastChan:    make(chan frostBroadcastCancelRequest, 1),
		broadcastToPeersFn: func(_ []peer.ID, _ []byte, _ string, closeAfterWrite bool) {
			if !closeAfterWrite {
				return
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-releaseWriters
		},
	}
	comm.wg.Add(1)
	go comm.ProcessBroadcast()
	t.Cleanup(func() {
		close(stop)
		close(releaseWriters)
		comm.wg.Wait()
	})

	// Occupy every writer with ordinary work from distinct sessions. Two tasks
	// per session match the production per-ceremony concurrency ceiling.
	for i := uint32(0); i < FrostMaxConcurrentBroadcasts; i++ {
		comm.FrostBroadcastMsgChan <- &messages.BroadcastMsgChan{
			WrappedMessage: messages.WrappedMessage{
				MessageType: messages.TSSFrostShareMsg,
				MsgID:       fmt.Sprintf("stalled-session-%d", i/FrostMaxConcurrentBroadcastsPerSession),
			},
		}
	}
	for i := uint32(0); i < FrostMaxConcurrentBroadcasts; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("failed to saturate the global FROST writer pool")
		}
	}

	// Fill the scheduler and leave the ordinary ingress channel full. No writer
	// can complete, so the processor has exactly its fixed pending capacity.
	ordinary := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "pending-rounds",
		},
	}
	fillDone := make(chan struct{})
	go func() {
		for i := 0; i < FrostMaxPendingBroadcasts+cap(comm.FrostBroadcastMsgChan); i++ {
			select {
			case comm.FrostBroadcastMsgChan <- ordinary:
			case <-stop:
				return
			}
		}
		close(fillDone)
	}()
	select {
	case <-fillDone:
	case <-time.After(time.Second):
		t.Fatal("failed to fill the bounded FROST scheduler and ingress")
	}

	result := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostResultMsg,
			MsgID:       "completed-session",
		},
	}
	fillResultsDone := make(chan struct{})
	go func() {
		for i := 0; i < FrostMaxDeferredBroadcasts+FrostMaxPendingResults; i++ {
			select {
			case comm.FrostResultBroadcastMsgChan <- result:
			case <-stop:
				return
			}
		}
		close(fillResultsDone)
	}()
	select {
	case <-fillResultsDone:
	case <-time.After(time.Second):
		t.Fatal("failed to fill the bounded deferred and ingress result queues")
	}

	// The processor may retain only the fixed local allowance and channel
	// capacity while the scheduler is full. Previously it kept draining into an
	// unbounded deferredFrost, so this extra enqueue always succeeded.
	if err := comm.QueueFrostBroadcast(result); err == nil {
		t.Fatal("stalled writers admitted terminal work beyond the fixed backlog")
	}
}

func TestMalformedTerminalResultDoesNotWedgeFrostScheduler(t *testing.T) {
	stop := make(chan struct{})
	sent := make(chan struct{}, 1)
	comm := &Communication{
		logger:                      zerolog.Nop(),
		wg:                          &sync.WaitGroup{},
		stopChan:                    stop,
		BroadcastMsgChan:            make(chan *messages.BroadcastMsgChan, 1),
		FrostBroadcastMsgChan:       make(chan *messages.BroadcastMsgChan, 1),
		FrostResultBroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
		cancelFrostBroadcastChan:    make(chan frostBroadcastCancelRequest, 1),
		broadcastToPeersFn: func(_ []peer.ID, _ []byte, msgID string, closeAfterWrite bool) {
			if closeAfterWrite && msgID == "healthy-session" {
				sent <- struct{}{}
			}
		},
	}
	comm.wg.Add(1)
	go comm.ProcessBroadcast()

	comm.FrostResultBroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostResultMsg,
		},
	}
	comm.FrostBroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "healthy-session",
		},
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("malformed terminal result wedged later FROST traffic")
	}

	close(stop)
	comm.wg.Wait()
}

func TestFrostBroadcastSaturationDoesNotBlockLegacyTSS(t *testing.T) {
	stop := make(chan struct{})
	releaseFrost := make(chan struct{})
	frostStarted := make(chan struct{}, FrostMaxConcurrentBroadcastsPerSession)
	legacySent := make(chan struct{}, 1)
	comm := &Communication{
		logger:                   zerolog.Nop(),
		wg:                       &sync.WaitGroup{},
		stopChan:                 stop,
		BroadcastMsgChan:         make(chan *messages.BroadcastMsgChan, 1),
		FrostBroadcastMsgChan:    make(chan *messages.BroadcastMsgChan, FrostMaxPendingBroadcasts+1),
		cancelFrostBroadcastChan: make(chan frostBroadcastCancelRequest, 1),
		broadcastToPeersFn: func(_ []peer.ID, _ []byte, _ string, closeAfterWrite bool) {
			if closeAfterWrite {
				frostStarted <- struct{}{}
				<-releaseFrost
				return
			}
			legacySent <- struct{}{}
		},
	}
	comm.wg.Add(1)
	go comm.ProcessBroadcast()

	frostMessage := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "offline-xmr-session",
			Payload:     []byte("frost"),
		},
		PeersID: []peer.ID{peer.ID("offline-xmr-peer")},
	}
	// One session may run two writers. Fill every remaining bounded scheduler
	// slot, then leave one additional FROST task backpressured at its isolated
	// channel.
	for i := uint32(0); i < FrostMaxConcurrentBroadcastsPerSession; i++ {
		comm.FrostBroadcastMsgChan <- frostMessage
	}
	for i := 0; i < FrostMaxPendingBroadcasts; i++ {
		comm.FrostBroadcastMsgChan <- frostMessage
	}
	for i := uint32(0); i < FrostMaxConcurrentBroadcastsPerSession; i++ {
		select {
		case <-frostStarted:
		case <-time.After(time.Second):
			t.Fatal("FROST scheduler did not start its bounded active writers")
		}
	}
	deadline := time.Now().Add(time.Second)
	for len(comm.FrostBroadcastMsgChan) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(comm.FrostBroadcastMsgChan) != 0 {
		t.Fatalf("FROST scheduler did not consume its exact pending allowance: %d queued", len(comm.FrostBroadcastMsgChan))
	}
	comm.FrostBroadcastMsgChan <- frostMessage
	if len(comm.FrostBroadcastMsgChan) != 1 {
		t.Fatal("test did not establish saturated FROST backpressure")
	}

	comm.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSKeySignMsg,
			MsgID:       "btc-keysign",
			Payload:     []byte("legacy"),
		},
		PeersID: []peer.ID{peer.ID("btc-peer")},
	}
	select {
	case <-legacySent:
	case <-time.After(time.Second):
		t.Fatal("saturated offline XMR ceremony blocked legacy TSS dispatch")
	}

	close(stop)
	close(releaseFrost)
	comm.wg.Wait()
}

func TestTSSReaderConcurrencyBoundFitsMaxPartyBurst(t *testing.T) {
	if TSSMaxConcurrentReaders < maxFrostParty-1 {
		t.Fatalf(
			"reader bound %d cannot admit one frame from every remote member of a %d-member party",
			TSSMaxConcurrentReaders,
			maxFrostParty,
		)
	}
	comm := &Communication{
		tssReaders: newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer),
	}
	peers := make([]peer.ID, TSSMaxConcurrentReaders)
	for i := uint32(0); i < TSSMaxConcurrentReaders; i++ {
		peers[i] = peer.ID(fmt.Sprintf("reader-peer-%03d", i))
		if !comm.acquireTSSReadSlot(peers[i]) {
			t.Fatalf("reader slot %d rejected below concurrency bound", i)
		}
	}
	if comm.acquireTSSReadSlot(peer.ID("one-peer-too-many")) {
		t.Fatal("reader concurrency bound admitted an extra inbound stream")
	}
	for _, id := range peers {
		comm.releaseTSSReadSlot(id)
	}
	if total, perPeer := comm.tssReaders.snapshot(); total != 0 || len(perPeer) != 0 {
		t.Fatalf("reader slot release leaked total=%d peers=%v", total, perPeer)
	}
}

func TestTSSReaderConcurrencyIsPeerFair(t *testing.T) {
	comm := &Communication{
		tssReaders: newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer),
	}
	attacker := peer.ID("slow-header-attacker")
	for i := uint32(0); i < TSSMaxReadersPerPeer; i++ {
		if !comm.acquireTSSReadSlot(attacker) {
			t.Fatalf("attacker slot %d rejected below per-peer bound", i)
		}
	}
	if comm.acquireTSSReadSlot(attacker) {
		t.Fatal("one peer occupied more than its fair reader allowance")
	}

	// A maximum-size party has 127 remote members. Even if one member uses its
	// full signer-concurrency headroom, every other member still gets a reader.
	honestPeers := make([]peer.ID, maxFrostParty-2)
	for i := range honestPeers {
		honestPeers[i] = peer.ID(fmt.Sprintf("honest-ceremony-member-%03d", i))
		if !comm.acquireTSSReadSlot(honestPeers[i]) {
			t.Fatalf("one slow peer consumed honest member %d's reader capacity", i)
		}
	}
	if total, _ := comm.tssReaders.snapshot(); total >= TSSMaxConcurrentReaders {
		t.Fatalf("one peer plus max-party honest readers exhausted global capacity: %d", total)
	}
	for _, id := range honestPeers {
		comm.releaseTSSReadSlot(id)
	}
	for i := uint32(0); i < TSSMaxReadersPerPeer; i++ {
		comm.releaseTSSReadSlot(attacker)
	}
}

func TestTSSReaderAllowsDefaultSignerConcurrencyFromSamePeer(t *testing.T) {
	const defaultSignerConcurrency = 10
	comm := &Communication{
		tssReaders: newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer),
	}
	remote := peer.ID("shared-vault-member")
	for i := 0; i < defaultSignerConcurrency; i++ {
		if !comm.acquireTSSReadSlot(remote) {
			t.Fatalf("same peer's honest concurrent ceremony %d was rejected", i+1)
		}
	}
	for i := 0; i < defaultSignerConcurrency; i++ {
		comm.releaseTSSReadSlot(remote)
	}
}

func TestFrostIngressBudgetAllowsDefaultSignerConcurrency(t *testing.T) {
	const defaultSignerConcurrency = 10
	remote := peer.ID("concurrent-signer-peer")
	comm := newTestCommunication()
	msgIDs := make([]string, defaultSignerConcurrency)
	reservations := make([]*messageReservation, 0, defaultSignerConcurrency*int(FrostMaxConcurrentFramesPerSession))
	for i := range msgIDs {
		msgIDs[i] = fmt.Sprintf("honest-concurrent-sign-%02d", i)
		comm.SetSubscribeWithPeers(
			messages.TSSFrostShareMsg,
			msgIDs[i],
			make(chan *Message, 1),
			[]peer.ID{remote},
			FrostMaxSessionPayload,
			FrostMaxMessagesPerPeer,
		)
		for frame := uint32(0); frame < FrostMaxConcurrentFramesPerSession; frame++ {
			_, _, reservation, err := comm.preflightTSSFrame(
				remote,
				1,
				tssFramePrefix(t, messages.TSSFrostShareMsg, msgIDs[i]),
			)
			if err != nil || reservation == nil {
				t.Fatalf("honest concurrent ceremony %d frame %d rejected: %v", i+1, frame+1, err)
			}
			reservations = append(reservations, reservation)
		}
	}
	if state := snapshotFrostBudget(comm.frostBudget); state.activeSessions != defaultSignerConcurrency {
		t.Fatalf(
			"active FROST ceremonies=%d, want default signer concurrency %d",
			state.activeSessions,
			defaultSignerConcurrency,
		)
	}
	for _, reservation := range reservations {
		reservation.reject()
	}
	for _, msgID := range msgIDs {
		comm.CancelSubscribe(messages.TSSFrostShareMsg, msgID)
	}
}

func TestFrostBudgetReplacementAndCancelRefundExactly(t *testing.T) {
	remote := peer.ID("replacement-peer")
	comm := newTestCommunication()
	comm.frostBudget = newFrostIngressBudget(32, 32, 32, 8, 8, 2)
	topic := messages.TSSFrostShareMsg
	msgID := "replacement-session"

	comm.SetSubscribeWithPeers(
		topic,
		msgID,
		make(chan *Message, 1),
		[]peer.ID{remote},
		16,
		8,
	)
	_, _, active, err := comm.preflightTSSFrame(
		remote,
		6,
		tssFramePrefix(t, topic, msgID),
	)
	if err != nil || active == nil {
		t.Fatalf("reserve active frame: %v", err)
	}

	// Replacing the type+msgID subscription must cancel and refund its
	// preflight reservation without deleting the newly attached ceremony.
	comm.SetSubscribeWithPeers(
		topic,
		msgID,
		make(chan *Message, 1),
		[]peer.ID{remote},
		16,
		8,
	)
	if state := snapshotFrostBudget(comm.frostBudget); state.nodeBytes != 0 || state.inFlightFrames != 0 {
		t.Fatalf(
			"replacement leaked active reservation: bytes=%d frames=%d",
			state.nodeBytes,
			state.inFlightFrames,
		)
	}

	_, _, committed, err := comm.preflightTSSFrame(
		remote,
		4,
		tssFramePrefix(t, topic, msgID),
	)
	if err != nil || committed == nil || !committed.accept() {
		t.Fatalf("commit replacement frame: %v", err)
	}
	comm.CancelSubscribe(topic, msgID)
	if state := snapshotFrostBudget(comm.frostBudget); state.nodeBytes != 0 ||
		state.inFlightFrames != 0 ||
		state.activeSessions != 0 {
		t.Fatalf(
			"cancel leaked committed accounting: bytes=%d frames=%d sessions=%d",
			state.nodeBytes,
			state.inFlightFrames,
			state.activeSessions,
		)
	}
}

func TestFrostMaxPartyFitsAggregateCeremonyBudget(t *testing.T) {
	remotePeers := make([]peer.ID, maxFrostParty-1)
	for i := range remotePeers {
		remotePeers[i] = peer.ID(fmt.Sprintf("aggregate-member-%03d", i))
	}

	msgID := "aggregate-full-party"
	comm := newTestCommunication()
	shared := make(chan *Message, FrostSessionQueueSize)
	verificationLimit := FrostVerificationMessagesPerPeer(maxFrostParty, 3)
	topics := []messages.THORChainTSSMessageType{
		messages.TSSFrostDKGRound1Msg,
		messages.TSSFrostDKGRound2Msg,
		messages.TSSFrostDKGRound3Msg,
		messages.TSSFrostVerifyMsg,
		messages.TSSFrostDKGResultMsg,
	}
	for _, topic := range topics {
		limit := FrostMaxMessagesPerPeer
		if topic == messages.TSSFrostVerifyMsg {
			limit = verificationLimit
		}
		comm.SetSubscribeWithPeers(
			topic,
			msgID,
			shared,
			remotePeers,
			FrostMaxSessionPayload,
			limit,
		)
	}

	// Verification is the dominant keygen topic: every peer may echo every
	// participant transcript in all three rounds. Give each other topic six
	// generous 8 KiB frames per peer to model round/result headroom.
	for _, remote := range remotePeers {
		verifySub := comm.getSubscriber(messages.TSSFrostVerifyMsg, msgID)
		for frame := uint32(0); frame < verificationLimit; frame++ {
			reservation := verifySub.reserve(remote, 1280)
			if reservation == nil || !reservation.accept() {
				t.Fatalf("verification frame %d rejected for %s", frame, remote)
			}
		}
		for _, topic := range topics {
			if topic == messages.TSSFrostVerifyMsg {
				continue
			}
			sub := comm.getSubscriber(topic, msgID)
			for frame := 0; frame < 6; frame++ {
				reservation := sub.reserve(remote, 8*1024)
				if reservation == nil || !reservation.accept() {
					t.Fatalf("%s frame %d rejected for %s", topic, frame, remote)
				}
			}
		}
	}

	session := comm.frostBudget.sessions[msgID]
	if session == nil {
		t.Fatal("full-party ceremony budget was not registered")
	}
	if session.bytes >= FrostMaxSessionBytes {
		t.Fatalf(
			"honest max-party model exhausted aggregate ceremony budget: used=%d max=%d",
			session.bytes,
			FrostMaxSessionBytes,
		)
	}
	if comm.frostBudget.nodeBytes != session.bytes {
		t.Fatalf(
			"single ceremony node accounting mismatch: node=%d ceremony=%d",
			comm.frostBudget.nodeBytes,
			session.bytes,
		)
	}
}

func rawHandlerStream(remote peer.ID, raw []byte) *handlerStream {
	base := NewMockNetworkStream()
	_, _ = base.Write(raw)
	return &handlerStream{
		MockNetworkStream: base,
		conn:              fakeTSSConn{remote: remote},
	}
}

func declaredHandlerStream(remote peer.ID, declared uint32, body []byte) *handlerStream {
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, declared)
	return rawHandlerStream(remote, append(header, body...))
}

func TestHandleStreamTssResetsAndRefundsEveryTruncationPoint(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "truncation-session"
	remote := peer.ID("allowed-peer")
	validPrefix := tssFramePrefix(t, topic, msgID)

	tests := []struct {
		name   string
		stream func() *handlerStream
	}{
		{
			name: "truncated length header",
			stream: func() *handlerStream {
				return rawHandlerStream(remote, []byte{1, 2})
			},
		},
		{
			name: "truncated bounded prefix",
			stream: func() *handlerStream {
				return declaredHandlerStream(remote, TSSFramePreflightBytes, validPrefix[:32])
			},
		},
		{
			name: "truncated body after reservation",
			stream: func() *handlerStream {
				return declaredHandlerStream(remote, uint32(len(validPrefix)+1), validPrefix)
			},
		},
		{
			name: "invalid full JSON after reservation",
			stream: func() *handlerStream {
				return declaredHandlerStream(remote, uint32(len(validPrefix)), validPrefix)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comm := newHandlerCommunication(topic, msgID, []peer.ID{remote})
			stream := tc.stream()
			comm.handleStreamTss(stream)

			if !stream.resetCalled.Load() {
				t.Fatal("failed inbound frame was not reset")
			}
			if got := parkedStreamCount(comm.streamMgr); got != 0 {
				t.Fatalf("failed inbound frame remained parked: %d streams", got)
			}
			if comm.frostBudget.nodeBytes != 0 || comm.frostBudget.inFlightFrames != 0 {
				t.Fatalf(
					"failed inbound frame leaked node budget: bytes=%d frames=%d",
					comm.frostBudget.nodeBytes,
					comm.frostBudget.inFlightFrames,
				)
			}
			sub := comm.getSubscriber(topic, msgID)
			if sub.sessionBytes != 0 || len(sub.peerBytes) != 0 || len(sub.peerCounts) != 0 {
				t.Fatalf(
					"failed inbound frame leaked subscription budget: bytes=%d peers=%v counts=%v",
					sub.sessionBytes,
					sub.peerBytes,
					sub.peerCounts,
				)
			}
		})
	}
}

func TestPartyNarrowingRevokesQueuedAndBlockedFrames(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "party-narrowing"
	retained := peer.ID("retained-peer")
	removed := peer.ID("removed-peer")
	channel := make(chan *Message, 1)
	comm := newTestCommunication()
	comm.streamMgr = NewStreamMgr()
	comm.stopChan = make(chan struct{})
	comm.frostBudget = newFrostIngressBudget(1024, 1024, 1024, 8, 8, 1)
	comm.SetSubscribeWithPeers(
		topic,
		msgID,
		channel,
		[]peer.ID{retained, removed},
		FrostMaxSessionPayload,
		8,
	)

	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("queued-before-party-formation"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	queued := newHandlerStream(removed, frame)
	comm.handleStreamTss(queued)
	if len(channel) != 1 {
		t.Fatal("first removed-peer frame was not queued")
	}

	blocked := newHandlerStream(removed, frame)
	blockedDone := make(chan struct{})
	go func() {
		defer close(blockedDone)
		comm.handleStreamTss(blocked)
	}()
	deadline := time.Now().Add(time.Second)
	for snapshotFrostBudget(comm.frostBudget).inFlightFrames != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := snapshotFrostBudget(comm.frostBudget); state.inFlightFrames != 2 {
		t.Fatalf("second frame did not reach blocked delivery: frames=%d", state.inFlightFrames)
	}

	if !comm.UpdateSubscribePeers(topic, msgID, []peer.ID{retained}) {
		t.Fatal("failed to narrow party")
	}
	select {
	case <-blockedDone:
	case <-time.After(time.Second):
		t.Fatal("party narrowing did not wake blocked delivery")
	}
	if len(channel) != 1 {
		t.Fatalf("expected one bounded queued frame after revocation: queue=%d", len(channel))
	}
	revokedMessage := <-channel
	if revokedMessage.Accept() {
		t.Fatal("revoked queued frame passed the coordinator acceptance check")
	}
	if !queued.resetCalled.Load() || !blocked.resetCalled.Load() {
		t.Fatal("party narrowing did not reset every revoked stream")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("revoked streams remained parked after purge: %d", got)
	}
	if comm.frostBudget.nodeBytes != 0 || comm.frostBudget.inFlightFrames != 0 {
		t.Fatalf(
			"revoked frames leaked aggregate budget: bytes=%d frames=%d",
			comm.frostBudget.nodeBytes,
			comm.frostBudget.inFlightFrames,
		)
	}

	fresh := newHandlerStream(retained, frame)
	comm.handleStreamTss(fresh)
	select {
	case message := <-channel:
		if !message.Accept() {
			t.Fatal("fresh retained-peer frame was rejected under the new authorization epoch")
		}
	default:
		t.Fatal("fresh retained-peer frame was not delivered")
	}
	if fresh.resetCalled.Load() {
		t.Fatal("fresh retained-peer frame was reset")
	}
	if comm.frostBudget.inFlightFrames != 0 ||
		comm.frostBudget.nodeBytes != uint64(len(frame)) {
		t.Fatalf(
			"accepted frame accounting mismatch: bytes=%d frames=%d",
			comm.frostBudget.nodeBytes,
			comm.frostBudget.inFlightFrames,
		)
	}
}

func TestPartyNarrowingDoesNotWaitForBlockedRetainedPeer(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "party-narrowing-blocked-retained"
	retained := peer.ID("retained-peer")
	removed := peer.ID("removed-peer")
	channel := make(chan *Message, 1)
	comm := newTestCommunication()
	comm.streamMgr = NewStreamMgr()
	comm.stopChan = make(chan struct{})
	comm.frostBudget = newFrostIngressBudget(4096, 4096, 4096, 8, 8, 1)
	comm.SetSubscribeWithPeers(
		topic,
		msgID,
		channel,
		[]peer.ID{retained, removed},
		FrostMaxSessionPayload,
		8,
	)

	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("retained-before-final-party"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	first := newHandlerStream(retained, frame)
	comm.handleStreamTss(first)
	if len(channel) != 1 {
		t.Fatal("first retained-peer frame was not queued")
	}

	second := newHandlerStream(retained, frame)
	blockedDone := make(chan struct{})
	var stopOnce sync.Once
	t.Cleanup(func() {
		stopOnce.Do(func() { close(comm.stopChan) })
		comm.CancelSubscribe(topic, msgID)
		select {
		case <-blockedDone:
		case <-time.After(time.Second):
		}
	})
	go func() {
		defer close(blockedDone)
		comm.handleStreamTss(second)
	}()
	deadline := time.Now().Add(time.Second)
	for snapshotFrostBudget(comm.frostBudget).inFlightFrames != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := snapshotFrostBudget(comm.frostBudget); state.inFlightFrames != 2 {
		t.Fatalf("second frame did not reach blocked delivery: frames=%d", state.inFlightFrames)
	}
	select {
	case <-blockedDone:
		t.Fatal("second retained-peer frame did not block on the full queue")
	default:
	}

	updated := make(chan bool, 1)
	go func() {
		updated <- comm.UpdateSubscribePeers(topic, msgID, []peer.ID{retained})
	}()
	returnedBeforeDrain := false
	select {
	case ok := <-updated:
		if !ok {
			t.Fatal("failed to narrow party")
		}
		returnedBeforeDrain = true
	case <-time.After(time.Second):
	}

	firstMessage := <-channel
	if !returnedBeforeDrain {
		select {
		case ok := <-updated:
			if !ok {
				t.Fatal("failed to narrow party after queue capacity became available")
			}
		case <-time.After(time.Second):
			stopOnce.Do(func() { close(comm.stopChan) })
			t.Fatal("party narrowing remained blocked after queue capacity became available")
		}
	}
	select {
	case <-blockedDone:
	case <-time.After(time.Second):
		stopOnce.Do(func() { close(comm.stopChan) })
		t.Fatal("second retained-peer delivery did not finish")
	}
	var secondMessage *Message
	select {
	case secondMessage = <-channel:
	case <-time.After(time.Second):
		t.Fatal("second retained-peer frame was not queued")
	}

	if !firstMessage.Accept() || !secondMessage.Accept() {
		t.Fatal("retained-peer frame was rejected after party narrowing")
	}
	if first.resetCalled.Load() || second.resetCalled.Load() ||
		!first.closeCalled.Load() || !second.closeCalled.Load() {
		t.Fatal("retained-peer streams were not accepted and gracefully closed")
	}
	state := snapshotFrostBudget(comm.frostBudget)
	if state.inFlightFrames != 0 || state.nodeBytes != 2*uint64(len(frame)) {
		t.Fatalf(
			"retained frame accounting mismatch: bytes=%d frames=%d",
			state.nodeBytes,
			state.inFlightFrames,
		)
	}
	if !returnedBeforeDrain {
		t.Fatal("party narrowing waited for retained-peer queue capacity")
	}
}

func TestPartyNarrowingPreservesQueuedFramesFromRetainedPeers(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "party-narrowing-retains-queued"
	retained := peer.ID("retained-peer")
	removed := peer.ID("removed-peer")
	channel := make(chan *Message, 2)
	comm := newTestCommunication()
	comm.streamMgr = NewStreamMgr()
	comm.stopChan = make(chan struct{})
	comm.frostBudget = newFrostIngressBudget(4096, 4096, 4096, 8, 8, 1)
	comm.SetSubscribeWithPeers(
		topic,
		msgID,
		channel,
		[]peer.ID{retained, removed},
		FrostMaxSessionPayload,
		8,
	)

	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("queued-before-final-party"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	retainedStream := newHandlerStream(retained, frame)
	removedStream := newHandlerStream(removed, frame)
	comm.handleStreamTss(retainedStream)
	comm.handleStreamTss(removedStream)
	if len(channel) != 2 {
		t.Fatalf("expected both pre-formation frames to be queued, got %d", len(channel))
	}

	if !comm.UpdateSubscribePeers(topic, msgID, []peer.ID{retained}) {
		t.Fatal("failed to narrow party")
	}
	if len(channel) != 2 {
		t.Fatalf("party narrowing changed the bounded queue: queue=%d", len(channel))
	}
	retainedMessage := <-channel
	if retainedMessage.PeerID != retained {
		t.Fatalf("first queued frame came from the wrong peer: %s", retainedMessage.PeerID)
	}
	if !retainedMessage.Accept() {
		t.Fatal("queued retained-peer reservation did not advance to the final-party epoch")
	}
	removedMessage := <-channel
	if removedMessage.PeerID != removed {
		t.Fatalf("second queued frame came from the wrong peer: %s", removedMessage.PeerID)
	}
	if removedMessage.Accept() {
		t.Fatal("queued removed-peer frame passed the coordinator acceptance check")
	}
	if retainedStream.resetCalled.Load() || !retainedStream.closeCalled.Load() {
		t.Fatal("retained peer stream was not accepted and gracefully closed")
	}
	if !removedStream.resetCalled.Load() {
		t.Fatal("removed peer stream was not revoked")
	}
	state := snapshotFrostBudget(comm.frostBudget)
	if state.inFlightFrames != 0 || state.nodeBytes != uint64(len(frame)) {
		t.Fatalf(
			"party narrowing accounting mismatch: bytes=%d frames=%d",
			state.nodeBytes,
			state.inFlightFrames,
		)
	}
}

func TestRevocationBeforeParkingCannotRetainDeadStream(t *testing.T) {
	remote := peer.ID("revoked-before-park")
	comm := newTestCommunication()
	comm.streamMgr = NewStreamMgr()
	topic := messages.TSSFrostShareMsg
	msgID := "revoked-before-park-session"
	comm.SetSubscribeWithPeers(
		topic,
		msgID,
		make(chan *Message, 1),
		[]peer.ID{remote},
		FrostMaxSessionPayload,
		8,
	)

	_, _, reservation, err := comm.preflightTSSFrame(
		remote,
		1,
		tssFramePrefix(t, topic, msgID),
	)
	if err != nil || reservation == nil {
		t.Fatalf("reserve frame: %v", err)
	}
	stream := rawHandlerStream(remote, nil)
	reservation.attachStream(stream, comm.streamMgr, msgID)

	if !comm.UpdateSubscribePeers(topic, msgID, nil) {
		t.Fatal("failed to revoke party")
	}
	if reservation.parkStream() {
		t.Fatal("settled reservation parked a stream after party revocation")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("revoked stream was retained after rejected park: %d", got)
	}
	if !stream.resetCalled.Load() {
		t.Fatal("revoked pre-park stream was not reset")
	}
	if state := snapshotFrostBudget(comm.frostBudget); state.nodeBytes != 0 ||
		state.inFlightFrames != 0 {
		t.Fatalf(
			"pre-park revocation leaked aggregate budget: bytes=%d frames=%d",
			state.nodeBytes,
			state.inFlightFrames,
		)
	}
}

func TestPartyRevocationAndStreamParkingRaceIsSafe(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		remote := peer.ID("parking-race-peer")
		comm := newTestCommunication()
		comm.streamMgr = NewStreamMgr()
		topic := messages.TSSFrostShareMsg
		msgID := fmt.Sprintf("parking-race-%d", iteration)
		comm.SetSubscribeWithPeers(
			topic,
			msgID,
			make(chan *Message, 1),
			[]peer.ID{remote},
			FrostMaxSessionPayload,
			8,
		)
		_, _, reservation, err := comm.preflightTSSFrame(
			remote,
			1,
			tssFramePrefix(t, topic, msgID),
		)
		if err != nil || reservation == nil {
			t.Fatalf("iteration %d: reserve frame: %v", iteration, err)
		}
		stream := rawHandlerStream(remote, nil)
		reservation.attachStream(stream, comm.streamMgr, msgID)

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = reservation.parkStream()
		}()
		go func() {
			defer wg.Done()
			<-start
			if !comm.UpdateSubscribePeers(topic, msgID, nil) {
				t.Errorf("iteration %d: failed to revoke party", iteration)
			}
		}()
		close(start)
		wg.Wait()

		if got := parkedStreamCount(comm.streamMgr); got != 0 {
			t.Fatalf("iteration %d: revoked stream remained parked: %d", iteration, got)
		}
		if !stream.resetCalled.Load() {
			t.Fatalf("iteration %d: revoked stream was not reset", iteration)
		}
		state := snapshotFrostBudget(comm.frostBudget)
		if state.nodeBytes != 0 || state.inFlightFrames != 0 {
			t.Fatalf(
				"iteration %d: revocation leaked budget: bytes=%d frames=%d",
				iteration,
				state.nodeBytes,
				state.inFlightFrames,
			)
		}
	}
}

func TestStoppedSubscriptionRejectsPartyUpdates(t *testing.T) {
	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")
	ms := NewMessageIDSubscriber()
	ms.SubscribeRestricted(
		"stopped-session",
		make(chan *Message, 1),
		[]peer.ID{peerA},
		32,
		4,
	)
	sub := ms.GetSubscriber("stopped-session")
	sub.stop()

	if sub.setAllowedPeers([]peer.ID{peerA}) {
		t.Fatal("same-peer update reported success after subscription stop")
	}
	if sub.setAllowedPeers([]peer.ID{peerB}) {
		t.Fatal("changed-peer update reported success after subscription stop")
	}
}

func TestCancelAndPartyUpdateRaceIsSafe(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		ms := NewMessageIDSubscriber()
		ms.SubscribeRestricted(
			"racing-session",
			make(chan *Message, 1),
			[]peer.ID{peer.ID("peer-a"), peer.ID("peer-b")},
			32,
			4,
		)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			ms.UnSubscribe("racing-session")
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = ms.UpdateAllowedPeers("racing-session", []peer.ID{peer.ID("peer-a")})
		}()
		close(start)
		wg.Wait()

		if ms.UpdateAllowedPeers("racing-session", []peer.ID{peer.ID("peer-a")}) {
			t.Fatalf("iteration %d: update succeeded after cancellation", iteration)
		}
	}
}

func TestInvalidJSONFixtureReallyPassesBoundedHeaderPreflight(t *testing.T) {
	// Guard the truncation fixture: it must reserve against a valid bounded
	// header before full JSON parsing fails, otherwise that test would not prove
	// the post-preflight refund path.
	topic := messages.TSSFrostShareMsg
	msgID := "fixture-session"
	prefix := tssFramePrefix(t, topic, msgID)
	if _, err := decodeTSSFrameHeader(prefix); err != nil {
		t.Fatalf("fixture does not pass bounded header decoding: %v", err)
	}
	if json.Valid(prefix) {
		t.Fatal("fixture unexpectedly became valid full JSON")
	}
	if !bytes.Contains(prefix, []byte(`"payload":"`)) {
		t.Fatal("fixture no longer reaches the payload field")
	}
}
