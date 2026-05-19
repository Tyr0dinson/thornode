package tss

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	tmsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	coskey "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cosmos "github.com/cosmos/cosmos-sdk/types"
	sdk "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"
	"github.com/libp2p/go-libp2p-core/peer"
	dklssession "gitlab.com/thorchain/thornode/v3/dkls/go-wrappers/go-dkls/sessions"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
)

const (
	dklsKeygenAbortRound  = "DKLSKeygenAbort"
	dklsKeysignAbortRound = "DKLSKeysignAbort"

	dklsMaxConcurrentSignSessions = 4
)

const (
	dklsAbortPhaseKeygen  = "keygen"
	dklsAbortPhaseKeysign = "keysign"
)

type dklsWireMessage struct {
	Kind    string `json:"kind,omitempty"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	Index   int    `json:"index,omitempty"`
	Message []byte `json:"message"`
	Sig     []byte `json:"sig,omitempty"`
}

type dklsSignResultMessage struct {
	Signatures []keysign.Signature `json:"signatures"`
}

type dklsSessionOps struct {
	output       func(dklssession.Handle) ([]byte, error)
	receiver     func(dklssession.Handle, []byte, int) (string, error)
	input        func(dklssession.Handle, []byte) (bool, error)
	finish       func(dklssession.Handle) ([]byte, error)
	abortMessage func(dklssession.Handle) ([]byte, error)
	free         func(dklssession.Handle) error
}

func dklsIDs(participants []string) ([]string, []byte) {
	ids := append([]string(nil), participants...)
	sort.Strings(ids)
	return ids, []byte(strings.Join(ids, "\x00"))
}

func dklsThreshold(participantCount int) (int, error) {
	threshold, err := conversion.GetThreshold(participantCount)
	if err != nil {
		return 0, err
	}
	return threshold + 1, nil
}

func dklsAbortConfigValue(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	data, err := os.ReadFile("/var/data/bifrost/dkls-abort.env")
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func dklsAbortMode() string {
	return strings.ToLower(dklsAbortConfigValue("BIFROST_DKLS_ABORT_MODE"))
}

func dklsPhaseForMessageType(msgType messages.THORChainTSSMessageType) string {
	if msgType == messages.TSSDKLSKeyGenMsg {
		return dklsAbortPhaseKeygen
	}
	return dklsAbortPhaseKeysign
}

func (t *TssServer) shouldTriggerDKLSTestFault(phase string, produced, input uint64) bool {
	return t.shouldTriggerDKLSTestFaultForTarget(phase, produced, input, "")
}

func (t *TssServer) shouldTriggerDKLSTestFaultForTarget(phase string, produced, input uint64, defaultTarget string) bool {
	if dklsAbortConfigValue("BIFROST_DKLS_ABORT_PHASE") != phase {
		return false
	}
	target := dklsAbortConfigValue("BIFROST_DKLS_ABORT_LOCAL_PUBKEY")
	if target == "" {
		target = defaultTarget
	}
	if target != "" && target != t.localNodePubKey {
		return false
	}
	return t.shouldTriggerDKLSTestHook(phase, produced, input)
}

func (t *TssServer) maybeAbortDKLSJoinTestHook(phase, msgID string) {
	if dklsAbortMode() != "join" {
		return
	}
	if dklsAbortConfigValue("BIFROST_DKLS_ABORT_PHASE") != phase {
		return
	}
	if target := dklsAbortConfigValue("BIFROST_DKLS_ABORT_LOCAL_PUBKEY"); target != "" && target != t.localNodePubKey {
		return
	}
	t.logger.Error().
		Str("phase", phase).
		Str("msg_id", msgID).
		Str("local_pubkey", t.localNodePubKey).
		Msg("DKLS test abort hook exiting before join party")
	os.Exit(42)
}

func (t *TssServer) maybeAbortDKLSTestHook(phase, msgID string, participants []string, produced, input uint64, defaultTarget string) error {
	switch dklsAbortMode() {
	case "corrupt", "malformed", "auth", "result", "join":
		return nil
	}
	if !t.shouldTriggerDKLSTestFaultForTarget(phase, produced, input, defaultTarget) {
		return nil
	}

	idx := 0
	for i, participant := range participants {
		if participant == t.localNodePubKey {
			idx = i + 1
			break
		}
	}
	if idx == 0 {
		return nil
	}

	t.logger.Error().
		Str("phase", phase).
		Str("msg_id", msgID).
		Str("local_pubkey", t.localNodePubKey).
		Int("party_index", idx).
		Uint64("produced", produced).
		Uint64("input", input).
		Msg("DKLS test abort hook triggered")

	if dklsAbortMode() == "error" {
		return fmt.Errorf("DKLS test identifiable abort: protocol abort %d", idx)
	}
	os.Exit(42)
	return nil
}

func (t *TssServer) shouldTriggerDKLSTestHook(phase string, produced, input uint64) bool {
	if dklsAbortConfigValue("BIFROST_DKLS_ABORT_PHASE") != phase {
		return false
	}
	if target := dklsAbortConfigValue("BIFROST_DKLS_ABORT_LOCAL_PUBKEY"); target != "" && target != t.localNodePubKey {
		return false
	}
	afterProduced := uint64(1)
	if raw := dklsAbortConfigValue("BIFROST_DKLS_ABORT_AFTER_OUTPUTS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			afterProduced = uint64(parsed)
		}
	}
	afterInput := int64(-1)
	if raw := dklsAbortConfigValue("BIFROST_DKLS_ABORT_AFTER_INPUTS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			afterInput = int64(parsed)
		}
	}
	return produced >= afterProduced && (afterInput < 0 || input >= uint64(afterInput))
}

func (t *TssServer) maybeCorruptDKLSTestMessage(phase, msgID, receiver string, produced, input uint64, msg []byte) []byte {
	if dklsAbortMode() != "corrupt" {
		return msg
	}
	if !t.shouldTriggerDKLSTestFault(phase, produced, input) {
		return msg
	}
	corrupted := append([]byte(nil), msg...)
	const dklsMessageHeaderSize = 36
	if len(corrupted) > dklsMessageHeaderSize {
		corrupted[dklsMessageHeaderSize] ^= 0x01
	} else if len(corrupted) > 0 {
		corrupted[len(corrupted)-1] ^= 0x01
	}
	t.logger.Error().
		Str("phase", phase).
		Str("msg_id", msgID).
		Str("local_pubkey", t.localNodePubKey).
		Str("receiver", receiver).
		Uint64("produced", produced).
		Uint64("input", input).
		Msg("DKLS test abort hook corrupted outgoing protocol message")
	return corrupted
}

func (t *TssServer) maybeFaultDKLSWireMessage(phase, msgID, receiver string, produced, input uint64, wire *dklsWireMessage, encoded []byte) []byte {
	if !t.shouldTriggerDKLSTestFault(phase, produced, input) {
		return encoded
	}
	switch dklsAbortMode() {
	case "auth":
		if len(wire.Sig) > 0 {
			wire.Sig[len(wire.Sig)-1] ^= 0x01
		} else {
			wire.Sig = []byte{1}
		}
		faulted, err := json.Marshal(wire)
		if err != nil {
			return encoded
		}
		t.logger.Error().
			Str("phase", phase).
			Str("msg_id", msgID).
			Str("local_pubkey", t.localNodePubKey).
			Str("receiver", receiver).
			Uint64("produced", produced).
			Uint64("input", input).
			Msg("DKLS test abort hook corrupted outgoing wire signature")
		return faulted
	case "malformed":
		t.logger.Error().
			Str("phase", phase).
			Str("msg_id", msgID).
			Str("local_pubkey", t.localNodePubKey).
			Str("receiver", receiver).
			Uint64("produced", produced).
			Uint64("input", input).
			Msg("DKLS test abort hook corrupted outgoing wire payload")
		return []byte("{")
	default:
		return encoded
	}
}

func (t *TssServer) maybeFaultDKLSSignResult(phase, msgID string, signatures []keysign.Signature) ([]keysign.Signature, bool) {
	if dklsAbortMode() != "result" || !t.shouldTriggerDKLSTestFault(phase, 1, 0) || len(signatures) == 0 {
		return signatures, false
	}
	faulted := append([]keysign.Signature(nil), signatures...)
	faulted[0].S = base64.StdEncoding.EncodeToString([]byte{1})
	t.logger.Error().
		Str("phase", phase).
		Str("msg_id", msgID).
		Str("local_pubkey", t.localNodePubKey).
		Msg("DKLS test abort hook corrupted keysign result")
	return faulted, true
}

func (t *TssServer) maybeForwardDKLSAbortAfterCorruptInput(
	phase string,
	msgID string,
	msgType messages.THORChainTSSMessageType,
	index int,
	session dklssession.Handle,
	localID string,
	corruptFrom string,
	participants map[string]struct{},
	produced uint64,
	input uint64,
	ops dklsSessionOps,
) (bool, error) {
	if dklsAbortConfigValue("BIFROST_DKLS_ABORT_LOCAL_PUBKEY") != corruptFrom || ops.abortMessage == nil {
		return false, nil
	}
	out, err := ops.output(session)
	if err != nil {
		return false, err
	}
	if len(out) == 0 {
		return false, nil
	}
	abortMsg, err := ops.abortMessage(session)
	if err != nil {
		return false, err
	}
	isAbort := bytes.Equal(out, abortMsg)
	for idx := 0; idx < len(participants); idx++ {
		receiver, err := ops.receiver(session, out, idx)
		if err != nil {
			return false, err
		}
		if receiver == "" {
			break
		}
		if receiver == localID {
			continue
		}
		if _, ok := participants[receiver]; !ok {
			return false, fmt.Errorf("DKLS receiver %q is not a participant", receiver)
		}
		peerID, err := conversion.GetPeerIDFromPubKey(receiver)
		if err != nil {
			return false, err
		}
		if err := t.sendDKLSMessage(msgID, msgType, index, localID, receiver, out, []peer.ID{peerID}, phase, produced, input); err != nil {
			return false, err
		}
	}
	if !isAbort {
		return false, nil
	}
	t.logger.Error().
		Str("phase", phase).
		Str("msg_id", msgID).
		Str("local_pubkey", localID).
		Str("corrupt_pubkey", corruptFrom).
		Msg("DKLS corrupt input caused native abort message")
	return true, nil
}

func dklsPubKeyFromCompressed(pk []byte) (string, error) {
	if len(pk) != 33 {
		return "", fmt.Errorf("invalid DKLS compressed public key length: %d", len(pk))
	}
	if _, err := btcec.ParsePubKey(pk, btcec.S256()); err != nil {
		return "", fmt.Errorf("invalid DKLS public key: %w", err)
	}
	compressedPubkey := coskey.PubKey{Key: pk}
	return sdk.MarshalPubKey(sdk.AccPK, &compressedPubkey) //nolint:staticcheck
}

func dklsBlameNodesFromCSV(csv string) []blame.Node {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	nodes := make([]blame.Node, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		pubkey := strings.TrimSpace(part)
		if pubkey == "" {
			continue
		}
		if _, ok := seen[pubkey]; ok {
			continue
		}
		seen[pubkey] = struct{}{}
		nodes = append(nodes, blame.Node{Pubkey: pubkey})
	}
	return nodes
}

func dklsBlameNodesFromTimeoutFroms(reason string, participants []string) []blame.Node {
	const fromsMarker = " froms="
	if !strings.Contains(reason, fromsMarker) {
		return nil
	}
	froms := reason[strings.LastIndex(reason, fromsMarker)+len(fromsMarker):]
	if idx := strings.Index(froms, " missing="); idx >= 0 {
		froms = froms[:idx]
	}
	if strings.TrimSpace(froms) == "" {
		return nil
	}

	participantSet := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		participantSet[participant] = struct{}{}
	}

	maxPubkey := ""
	maxCount := 0
	secondCount := 0
	tiedMax := false
	for _, part := range strings.Split(froms, ",") {
		pair := strings.Split(strings.TrimSpace(part), "=")
		if len(pair) != 2 {
			continue
		}
		pubkey := strings.TrimSpace(pair[0])
		if _, ok := participantSet[pubkey]; !ok {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(pair[1]))
		if err != nil {
			continue
		}
		switch {
		case count > maxCount:
			secondCount = maxCount
			maxCount = count
			maxPubkey = pubkey
			tiedMax = false
		case count == maxCount:
			tiedMax = true
		case count > secondCount:
			secondCount = count
		}
	}
	if maxPubkey == "" || tiedMax || maxCount <= secondCount || maxCount <= 1 {
		return nil
	}
	return []blame.Node{{Pubkey: maxPubkey}}
}

func dklsBlameError(reason string, pubkeys ...string) error {
	nodes := make([]string, 0, len(pubkeys))
	seen := make(map[string]struct{}, len(pubkeys))
	for _, pubkey := range pubkeys {
		pubkey = strings.TrimSpace(pubkey)
		if pubkey == "" {
			continue
		}
		if _, ok := seen[pubkey]; ok {
			continue
		}
		seen[pubkey] = struct{}{}
		nodes = append(nodes, pubkey)
	}
	if len(nodes) == 0 {
		return errors.New(reason)
	}
	sort.Strings(nodes)
	return fmt.Errorf("%s blame=%s", reason, strings.Join(nodes, ","))
}

func dklsPubKeyFromMessagePeer(raw *p2p.Message) (string, bool) {
	if raw == nil {
		return "", false
	}
	from, err := conversion.GetPubKeyFromPeerID(raw.PeerID.String())
	if err != nil || from == "" {
		return "", false
	}
	return from, true
}

func dklsNodeSyncBlame(keys []string, onlinePeers []peer.ID, round string, cause error) blame.Blame {
	nodes := make([]blame.Node, 0)
	for _, key := range keys {
		peerID, err := conversion.GetPeerIDFromPubKey(key)
		if err != nil {
			return dklsBlameFromError(cause, keys, round)
		}
		found := false
		for _, online := range onlinePeers {
			if online == peerID {
				found = true
				break
			}
		}
		if !found {
			nodes = append(nodes, blame.Node{Pubkey: key})
		}
	}
	if len(nodes) == 0 {
		return dklsBlameFromError(cause, keys, round)
	}
	reason := blame.TssSyncFail
	if cause != nil {
		reason = cause.Error() + ": dkls node sync abort"
	}
	return blame.Blame{
		FailReason: reason,
		Round:      round,
		BlameNodes: nodes,
	}
}

func dklsMissingParticipants(participants []string, localID string, inputFrom map[string]int) string {
	missing := make([]string, 0)
	for _, participant := range participants {
		if participant == localID {
			continue
		}
		if inputFrom[participant] == 0 {
			missing = append(missing, participant)
		}
	}
	sort.Strings(missing)
	return strings.Join(missing, ",")
}

func dklsWireSignBytes(msgID string, wire dklsWireMessage) ([]byte, error) {
	wire.Sig = nil
	return json.Marshal(wire)
}

func (t *TssServer) signDKLSWireMessage(msgID string, wire *dklsWireMessage) error {
	payload, err := dklsWireSignBytes(msgID, *wire)
	if err != nil {
		return err
	}
	sig, err := common.GenerateSignature(payload, msgID, t.privateKey)
	if err != nil {
		return err
	}
	wire.Sig = sig
	return nil
}

func verifyDKLSWireMessage(msgID string, wire dklsWireMessage) error {
	if len(wire.Sig) == 0 {
		return errors.New("missing DKLS wire signature")
	}
	pubKey, err := tcommon.NewPubKey(wire.From)
	if err != nil {
		return err
	}
	secpPubKey, err := pubKey.Secp256K1()
	if err != nil {
		return err
	}
	tmPubKey := tmsecp256k1.PubKey(secpPubKey.SerializeCompressed())
	payload, err := dklsWireSignBytes(msgID, wire)
	if err != nil {
		return err
	}
	if !common.VerifySignature(tmPubKey, payload, wire.Sig, msgID) {
		return fmt.Errorf("invalid DKLS wire signature from %q", wire.From)
	}
	return nil
}

func (t *TssServer) sendDKLSMessage(
	msgID string,
	msgType messages.THORChainTSSMessageType,
	index int,
	from, to string,
	msg []byte,
	peers []peer.ID,
	phase string,
	produced, input uint64,
) error {
	wireMsg := dklsWireMessage{Kind: "session", From: from, To: to, Index: index, Message: msg}
	if err := t.signDKLSWireMessage(msgID, &wireMsg); err != nil {
		return err
	}
	wire, err := json.Marshal(wireMsg)
	if err != nil {
		return err
	}
	wire = t.maybeFaultDKLSWireMessage(phase, msgID, to, produced, input, &wireMsg, wire)
	wrapped := messages.WrappedMessage{
		MsgID:       msgID,
		MessageType: msgType,
		Payload:     wire,
	}
	t.p2pCommunication.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: wrapped,
		PeersID:        peers,
	}
	return nil
}

func (t *TssServer) sendDKLSSetup(msgID string, msgType messages.THORChainTSSMessageType, index int, from, to string, setup []byte) error {
	wireMsg := dklsWireMessage{Kind: "setup", From: from, To: to, Index: index, Message: setup}
	if err := t.signDKLSWireMessage(msgID, &wireMsg); err != nil {
		return err
	}
	wire, err := json.Marshal(wireMsg)
	if err != nil {
		return err
	}
	wire = t.maybeFaultDKLSWireMessage(dklsPhaseForMessageType(msgType), msgID, to, 1, 0, &wireMsg, wire)
	peerID, err := conversion.GetPeerIDFromPubKey(to)
	if err != nil {
		return err
	}
	wrapped := messages.WrappedMessage{
		MsgID:       msgID,
		MessageType: msgType,
		Payload:     wire,
	}
	t.p2pCommunication.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: wrapped,
		PeersID:        []peer.ID{peerID},
	}
	return nil
}

func (t *TssServer) receiveDKLSSetup(ch chan *p2p.Message, localID, leaderID string, timeout time.Duration) ([]byte, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case raw := <-ch:
			from, hasFrom := dklsPubKeyFromMessagePeer(raw)
			var wrapped messages.WrappedMessage
			if err := json.Unmarshal(raw.Payload, &wrapped); err != nil {
				if hasFrom {
					return nil, dklsBlameError(fmt.Sprintf("invalid DKLS setup wrapper from %q: %v", from, err), from)
				}
				return nil, err
			}
			var wire dklsWireMessage
			if err := json.Unmarshal(wrapped.Payload, &wire); err != nil {
				if hasFrom {
					return nil, dklsBlameError(fmt.Sprintf("invalid DKLS setup wire from %q: %v", from, err), from)
				}
				return nil, err
			}
			if !hasFrom {
				return nil, fmt.Errorf("fail to get DKLS setup pubkey from peer %q", raw.PeerID)
			}
			if from != wire.From {
				return nil, dklsBlameError(fmt.Sprintf("DKLS setup sender mismatch: peer %q is %q but payload says %q", raw.PeerID, from, wire.From), from)
			}
			if err := verifyDKLSWireMessage(wrapped.MsgID, wire); err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS setup signature from %q: %v", from, err), from)
			}
			if wire.Kind == "setup" && wire.From == leaderID && (wire.To == "" || wire.To == localID) {
				return wire.Message, nil
			}
			if wire.From == leaderID {
				return nil, dklsBlameError(fmt.Sprintf("unexpected DKLS setup message from %q: kind=%q to=%q", from, wire.Kind, wire.To), from)
			}
		case <-timer.C:
			return nil, errors.New("timeout waiting for DKLS setup")
		case <-t.stopChan:
			return nil, errors.New("received exit signal")
		}
	}
}

func (t *TssServer) demuxDKLSKeySignMessages(in <-chan *p2p.Message, setupChans, sessionChans []chan *p2p.Message, done <-chan struct{}) {
	fanout := func(raw *p2p.Message) {
		for _, ch := range setupChans {
			select {
			case ch <- raw:
			default:
			}
		}
		for _, ch := range sessionChans {
			select {
			case ch <- raw:
			default:
			}
		}
	}
	for {
		select {
		case raw := <-in:
			var wrapped messages.WrappedMessage
			if err := json.Unmarshal(raw.Payload, &wrapped); err != nil {
				t.logger.Err(err).Msg("fail to decode DKLS wrapped message")
				fanout(raw)
				continue
			}
			var wire dklsWireMessage
			if err := json.Unmarshal(wrapped.Payload, &wire); err != nil {
				t.logger.Err(err).Msg("fail to decode DKLS wire message")
				fanout(raw)
				continue
			}
			if wire.Index < 0 || wire.Index >= len(sessionChans) {
				t.logger.Error().Int("index", wire.Index).Msg("DKLS message index out of range")
				fanout(raw)
				continue
			}
			switch wire.Kind {
			case "setup":
				setupChans[wire.Index] <- raw
			case "session":
				sessionChans[wire.Index] <- raw
			default:
				t.logger.Error().Str("kind", wire.Kind).Msg("unexpected DKLS keysign message kind")
				select {
				case sessionChans[wire.Index] <- raw:
				default:
				}
			}
		case <-done:
			return
		case <-t.stopChan:
			return
		}
	}
}

func (t *TssServer) broadcastDKLSSignResult(msgID, from string, signatures []keysign.Signature, participants []string) error {
	signatures, faultedResult := t.maybeFaultDKLSSignResult(dklsAbortPhaseKeysign, msgID, signatures)
	result, err := json.Marshal(dklsSignResultMessage{Signatures: signatures})
	if err != nil {
		return err
	}
	wireMsg := dklsWireMessage{Kind: "result", From: from, Message: result}
	if err := t.signDKLSWireMessage(msgID, &wireMsg); err != nil {
		return err
	}
	wire, err := json.Marshal(wireMsg)
	if err != nil {
		return err
	}
	peers, err := conversion.GetPeerIDsFromPubKeys(participants)
	if err != nil {
		return err
	}
	wrapped := messages.WrappedMessage{
		MsgID:       msgID,
		MessageType: messages.TSSDKLSKeySignResultMsg,
		Payload:     wire,
	}
	t.p2pCommunication.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: wrapped,
		PeersID:        peers,
	}
	if faultedResult {
		return dklsBlameError("DKLS test bad keysign result broadcaster", from)
	}
	return nil
}

func (t *TssServer) receiveDKLSSignResult(ch chan *p2p.Message, participants []string, poolPubKey string, encodedMsgs []string, done <-chan struct{}) ([]keysign.Signature, error) {
	timer := time.NewTimer(t.conf.KeySignTimeout)
	defer timer.Stop()
	participantSet := make(map[string]struct{}, len(participants))
	for _, p := range participants {
		participantSet[p] = struct{}{}
	}
	pk, err := tcommon.NewPubKey(poolPubKey)
	if err != nil {
		return nil, err
	}
	for {
		select {
		case raw := <-ch:
			from, hasFrom := dklsPubKeyFromMessagePeer(raw)
			var wrapped messages.WrappedMessage
			if err := json.Unmarshal(raw.Payload, &wrapped); err != nil {
				if hasFrom {
					return nil, dklsBlameError(fmt.Sprintf("invalid DKLS keysign result wrapper from %q: %v", from, err), from)
				}
				return nil, err
			}
			if wrapped.MessageType != messages.TSSDKLSKeySignResultMsg {
				if hasFrom {
					return nil, dklsBlameError(fmt.Sprintf("unexpected DKLS keysign result message type from %q: %s", from, wrapped.MessageType), from)
				}
				return nil, fmt.Errorf("unexpected DKLS keysign result message type: %s", wrapped.MessageType)
			}
			if !hasFrom {
				return nil, fmt.Errorf("fail to get DKLS keysign result pubkey from peer %q", raw.PeerID)
			}
			if _, ok := participantSet[from]; !ok {
				return nil, dklsBlameError(fmt.Sprintf("DKLS keysign result sender %q is not a participant", from), from)
			}
			var wire dklsWireMessage
			if err := json.Unmarshal(wrapped.Payload, &wire); err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS keysign result wire from %q: %v", from, err), from)
			}
			if wire.Kind != "result" {
				return nil, dklsBlameError(fmt.Sprintf("unexpected DKLS keysign result kind from %q: %q", from, wire.Kind), from)
			}
			if from != wire.From {
				return nil, dklsBlameError(fmt.Sprintf("DKLS keysign result sender mismatch: peer %q is %q but payload says %q", raw.PeerID, from, wire.From), from)
			}
			if _, ok := participantSet[wire.From]; !ok {
				return nil, dklsBlameError(fmt.Sprintf("DKLS keysign result sender %q is not a participant", wire.From), from)
			}
			if err := verifyDKLSWireMessage(wrapped.MsgID, wire); err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS keysign result signature from %q: %v", from, err), from)
			}
			var result dklsSignResultMessage
			if err := json.Unmarshal(wire.Message, &result); err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS keysign result payload from %q: %v", from, err), from)
			}
			if err := validateDKLSSignatures(pk, result.Signatures, encodedMsgs); err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS keysign result signatures from %q: %v", from, err), from)
			}
			return result.Signatures, nil
		case <-timer.C:
			return nil, errors.New("timeout waiting for DKLS keysign result")
		case <-done:
			return nil, errors.New("DKLS keysign result wait cancelled")
		case <-t.stopChan:
			return nil, errors.New("received exit signal")
		}
	}
}

func validateDKLSSignatures(pk tcommon.PubKey, signatures []keysign.Signature, encodedMsgs []string) error {
	if len(signatures) != len(encodedMsgs) {
		return fmt.Errorf("DKLS keysign result count mismatch: got %d want %d", len(signatures), len(encodedMsgs))
	}
	secpPubKey, err := pk.Secp256K1()
	if err != nil {
		return err
	}
	msgs := make(map[string][]byte, len(encodedMsgs))
	remaining := make(map[string]int, len(encodedMsgs))
	for _, encoded := range encodedMsgs {
		msg, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		msgToSign := msg
		if len(msgToSign) != 32 {
			digest := sha256.Sum256(msgToSign)
			msgToSign = digest[:]
		}
		msgs[encoded] = msgToSign
		remaining[encoded]++
	}
	for _, sig := range signatures {
		msgToSign, ok := msgs[sig.Msg]
		if !ok {
			return fmt.Errorf("DKLS keysign result contains unknown message")
		}
		if remaining[sig.Msg] == 0 {
			return fmt.Errorf("DKLS keysign result contains too many signatures for message")
		}
		remaining[sig.Msg]--
		rBytes, err := base64.StdEncoding.DecodeString(sig.R)
		if err != nil {
			return err
		}
		sBytes, err := base64.StdEncoding.DecodeString(sig.S)
		if err != nil {
			return err
		}
		btcecSig := &btcec.Signature{
			R: new(big.Int).SetBytes(rBytes),
			S: new(big.Int).SetBytes(sBytes),
		}
		if !btcecSig.Verify(msgToSign, secpPubKey) {
			return fmt.Errorf("DKLS keysign result signature verification failed")
		}
	}
	for encoded, count := range remaining {
		if count != 0 {
			return fmt.Errorf("DKLS keysign result missing %d signature(s) for message %q", count, encoded)
		}
	}
	return nil
}

func (t *TssServer) runDKLSSession(
	msgID string,
	msgType messages.THORChainTSSMessageType,
	index int,
	ch chan *p2p.Message,
	session dklssession.Handle,
	localID string,
	participants []string,
	timeout time.Duration,
	phase string,
	defaultAbortPubKey string,
	ops dklsSessionOps,
) ([]byte, error) {
	defer ops.free(session)

	participantSet := make(map[string]struct{}, len(participants))
	for _, p := range participants {
		participantSet[p] = struct{}{}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	sessionFinished := false
	var produced, sent, received, input, selfInput uint64
	inputFrom := make(map[string]int, len(participants))

	var drainOutput func() ([]byte, error)
	drainOutput = func() ([]byte, error) {
		for {
			out, err := ops.output(session)
			if err != nil {
				return nil, err
			}
			if len(out) == 0 {
				if sessionFinished {
					return ops.finish(session)
				}
				return nil, nil
			}
			produced++
			for idx := 0; idx < len(participants); idx++ {
				receiver, err := ops.receiver(session, out, idx)
				if err != nil {
					return nil, err
				}
				if receiver == "" {
					break
				}
				if _, ok := participantSet[receiver]; !ok {
					return nil, fmt.Errorf("DKLS receiver %q is not a participant", receiver)
				}
				if receiver == localID {
					if sessionFinished {
						continue
					}
					finished, err := ops.input(session, out)
					if err != nil {
						return nil, err
					}
					input++
					selfInput++
					if err := t.maybeAbortDKLSTestHook(phase, msgID, participants, produced, input, defaultAbortPubKey); err != nil {
						return nil, err
					}
					if finished {
						sessionFinished = true
					}
					if result, err := drainOutput(); err != nil || result != nil {
						return result, err
					}
					continue
				}
				peerID, err := conversion.GetPeerIDFromPubKey(receiver)
				if err != nil {
					return nil, err
				}
				outgoing := t.maybeCorruptDKLSTestMessage(phase, msgID, receiver, produced, input, out)
				if err := t.sendDKLSMessage(msgID, msgType, index, localID, receiver, outgoing, []peer.ID{peerID}, phase, produced, input); err != nil {
					return nil, err
				}
				sent++
				if err := t.maybeAbortDKLSTestHook(phase, msgID, participants, produced, input, defaultAbortPubKey); err != nil {
					return nil, err
				}
			}
		}
	}
	if result, err := drainOutput(); err != nil || result != nil {
		return result, err
	}

	for {
		select {
		case <-ticker.C:
			if result, err := drainOutput(); err != nil || result != nil {
				return result, err
			}
		case raw := <-ch:
			received++
			from, hasFrom := dklsPubKeyFromMessagePeer(raw)
			var wrapped messages.WrappedMessage
			if err := json.Unmarshal(raw.Payload, &wrapped); err != nil {
				if hasFrom {
					return nil, dklsBlameError(fmt.Sprintf("invalid DKLS session wrapper from %q: %v", from, err), from)
				}
				return nil, err
			}
			var wire dklsWireMessage
			if err := json.Unmarshal(wrapped.Payload, &wire); err != nil {
				if hasFrom {
					return nil, dklsBlameError(fmt.Sprintf("invalid DKLS session wire from %q: %v", from, err), from)
				}
				return nil, err
			}
			if wire.To != "" && wire.To != localID {
				continue
			}
			if sessionFinished {
				continue
			}
			if !hasFrom {
				return nil, fmt.Errorf("fail to get DKLS session pubkey from peer %q", raw.PeerID)
			}
			if from != wire.From {
				return nil, dklsBlameError(fmt.Sprintf("DKLS sender mismatch: peer %q is %q but payload says %q", raw.PeerID, from, wire.From), from)
			}
			if _, ok := participantSet[wire.From]; !ok {
				return nil, dklsBlameError(fmt.Sprintf("DKLS sender %q is not a participant", wire.From), from)
			}
			if wire.Kind != "session" {
				return nil, dklsBlameError(fmt.Sprintf("unexpected DKLS session message kind from %q: %q", from, wire.Kind), from)
			}
			if wire.Index != index {
				return nil, dklsBlameError(fmt.Sprintf("unexpected DKLS session message index from %q: got %d want %d", from, wire.Index, index), from)
			}
			if err := verifyDKLSWireMessage(wrapped.MsgID, wire); err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS session signature from %q: %v", from, err), from)
			}
			finished, err := ops.input(session, wire.Message)
			if err != nil {
				return nil, dklsBlameError(fmt.Sprintf("invalid DKLS protocol message from %q: %v", from, err), from)
			}
			input++
			inputFrom[wire.From]++
			if aborted, err := t.maybeForwardDKLSAbortAfterCorruptInput(phase, msgID, msgType, index, session, localID, wire.From, participantSet, produced, input, ops); err != nil {
				return nil, err
			} else if aborted {
				return nil, dklsBlameError(fmt.Sprintf("DKLS corrupt protocol message from %q caused native abort", wire.From), wire.From)
			}
			if err := t.maybeAbortDKLSTestHook(phase, msgID, participants, produced, input, defaultAbortPubKey); err != nil {
				return nil, err
			}
			if finished {
				sessionFinished = true
			}
			if result, err := drainOutput(); err != nil || result != nil {
				return result, err
			}
		case <-timer.C:
			froms := make([]string, 0, len(inputFrom))
			for from, count := range inputFrom {
				froms = append(froms, fmt.Sprintf("%s=%d", from, count))
			}
			sort.Strings(froms)
			missing := dklsMissingParticipants(participants, localID, inputFrom)
			return nil, fmt.Errorf("DKLS session timeout: produced=%d sent=%d received=%d input=%d self_input=%d finished=%t queued=%d froms=%s missing=%s", produced, sent, received, input, selfInput, sessionFinished, len(ch), strings.Join(froms, ","), missing)
		case <-t.stopChan:
			return nil, errors.New("received exit signal")
		}
	}
}

func dklsBlameFromError(err error, participants []string, round string) blame.Blame {
	if err == nil {
		return blame.Blame{}
	}
	reason := err.Error()
	if marker := " blame="; strings.Contains(reason, marker) {
		pubkeys := strings.TrimSpace(reason[strings.LastIndex(reason, marker)+len(marker):])
		if nodes := dklsBlameNodesFromCSV(pubkeys); len(nodes) > 0 {
			return blame.Blame{
				FailReason: reason + ": dkls attributable abort",
				Round:      round,
				BlameNodes: nodes,
			}
		}
	}
	if nodes := dklsBlameNodesFromTimeoutFroms(reason, participants); len(nodes) > 0 {
		return blame.Blame{
			FailReason: reason + ": dkls timeout sender-skew abort",
			Round:      round,
			BlameNodes: nodes,
		}
	}
	if marker := " missing="; strings.Contains(reason, marker) {
		missing := strings.TrimSpace(reason[strings.LastIndex(reason, marker)+len(marker):])
		if nodes := dklsBlameNodesFromCSV(missing); len(nodes) > 0 {
			return blame.Blame{
				FailReason: reason + ": dkls missing participant abort",
				Round:      round,
				BlameNodes: nodes,
			}
		}
	}
	lower := strings.ToLower(reason)
	idx := 0
	if strings.Contains(lower, "protocol abort") || strings.Contains(lower, "abort_protocol") {
		fields := strings.Fields(reason)
		if len(fields) > 0 {
			if parsed, parseErr := strconv.Atoi(fields[len(fields)-1]); parseErr == nil {
				idx = parsed
			}
		}
		if idx == 0 {
			trimmed := strings.TrimRight(reason, "0123456789")
			if len(trimmed) < len(reason) {
				if parsed, parseErr := strconv.Atoi(reason[len(trimmed):]); parseErr == nil {
					idx = parsed
				}
			}
		}
	}
	if idx < 1 || idx > len(participants) {
		return blame.Blame{
			FailReason: reason,
			Round:      round,
		}
	}
	return blame.Blame{
		FailReason: reason + ": dkls identifiable abort",
		Round:      round,
		BlameNodes: []blame.Node{{
			Pubkey: participants[idx-1],
		}},
	}
}

func dklsBlameFromErrorWithDefault(err error, participants []string, round, defaultPubKey, defaultReason string) blame.Blame {
	b := dklsBlameFromError(err, participants, round)
	if err == nil || len(b.BlameNodes) > 0 || defaultPubKey == "" {
		return b
	}
	if b.FailReason == "" {
		b.FailReason = err.Error()
	}
	if defaultReason != "" {
		b.FailReason += ": " + defaultReason
	}
	if b.Round == "" {
		b.Round = round
	}
	b.BlameNodes = []blame.Node{{Pubkey: defaultPubKey}}
	return b
}

func (t *TssServer) DKLSKeygen(req keygen.Request) (keygen.Response, error) {
	ids, idsBytes := dklsIDs(req.Keys)
	threshold, err := dklsThreshold(len(ids))
	if err != nil {
		return keygen.Response{}, err
	}
	msgID, err := t.requestToMsgId(req)
	if err != nil {
		return keygen.Response{}, err
	}
	msgID += ":" + storage.SigningEngineDKLS
	ch := make(chan *p2p.Message, len(ids)*1024)
	setupMsgID := msgID + ":setup"
	setupCh := make(chan *p2p.Message, len(ids))
	t.p2pCommunication.SetSubscribe(messages.TSSDKLSKeyGenMsg, msgID, ch)
	t.p2pCommunication.SetSubscribe(messages.TSSDKLSKeyGenMsg, setupMsgID, setupCh)
	defer func() {
		t.p2pCommunication.CancelSubscribe(messages.TSSDKLSKeyGenMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSDKLSKeyGenMsg, setupMsgID)
		t.p2pCommunication.ReleaseStream(msgID)
		t.p2pCommunication.ReleaseStream(setupMsgID)
		t.partyCoordinator.ReleaseStream(msgID)
	}()
	sigChan := make(chan string)
	t.maybeAbortDKLSJoinTestHook(dklsAbortPhaseKeygen, msgID)
	onlinePeers, leader, err := t.joinParty(msgID, req.Version, req.BlockHeight, ids, len(ids)-1, sigChan)
	if err != nil {
		t.logger.Error().Err(err).Interface("online_peers", onlinePeers).Msg("failed to form DKLS keygen party")
		return keygen.Response{
			Status: common.Fail,
			Blame:  dklsNodeSyncBlame(ids, onlinePeers, dklsKeygenAbortRound, err),
		}, nil
	}
	t.logger.Info().Int("participants", len(ids)).Msg("DKLS keygen party formed")

	leaderID := t.localNodePubKey
	if leader != "NONE" {
		leaderID, err = conversion.GetPubKeyFromPeerID(leader)
		if err != nil {
			return keygen.Response{
				Status: common.Fail,
				Blame:  dklsBlameFromError(err, ids, dklsKeygenAbortRound),
			}, err
		}
	}
	var setup []byte
	if t.p2pCommunication.GetLocalPeerID() == leader || leader == "NONE" {
		keyID := sha256.Sum256([]byte(msgID))
		setup, err = dklssession.DklsKeygenSetupMsgNew(threshold, keyID[:], idsBytes)
		if err != nil {
			return keygen.Response{
				Status: common.Fail,
				Blame:  dklsBlameFromError(err, ids, dklsKeygenAbortRound),
			}, err
		}
		for _, id := range ids {
			if id == t.localNodePubKey {
				continue
			}
			if err := t.sendDKLSSetup(setupMsgID, messages.TSSDKLSKeyGenMsg, 0, t.localNodePubKey, id, setup); err != nil {
				return keygen.Response{
					Status: common.Fail,
					Blame:  dklsBlameFromError(err, ids, dklsKeygenAbortRound),
				}, err
			}
		}
	} else {
		setup, err = t.receiveDKLSSetup(setupCh, t.localNodePubKey, leaderID, t.conf.KeyGenTimeout)
		if err != nil {
			return keygen.Response{
				Status: common.Fail,
				Blame:  dklsBlameFromError(err, ids, dklsKeygenAbortRound),
			}, err
		}
	}
	session, err := dklssession.DklsKeygenSessionFromSetup(setup, []byte(t.localNodePubKey))
	if err != nil {
		return keygen.Response{
			Status: common.Fail,
			Blame:  dklsBlameFromError(err, ids, dklsKeygenAbortRound),
		}, err
	}

	if t.p2pCommunication.GetLocalPeerID() == leader || leader == "NONE" {
		t.logger.Info().
			Str("msg_id", msgID).
			Str("leader_pubkey", leaderID).
			Int("participants", len(ids)).
			Int("threshold", threshold).
			Int64("block_height", req.BlockHeight).
			Msg("DKLS keygen ceremony leader started")
	}
	keyshareHandleBytes, err := t.runDKLSSession(msgID, messages.TSSDKLSKeyGenMsg, 0, ch, session, t.localNodePubKey, ids, t.conf.KeyGenTimeout, dklsAbortPhaseKeygen, "", dklsSessionOps{
		output: dklssession.DklsKeygenSessionOutputMessage,
		receiver: func(h dklssession.Handle, msg []byte, idx int) (string, error) {
			return dklssession.DklsKeygenSessionMessageReceiver(h, msg, idx)
		},
		input: dklssession.DklsKeygenSessionInputMessage,
		finish: func(h dklssession.Handle) ([]byte, error) {
			share, err := dklssession.DklsKeygenSessionFinish(h)
			if err != nil {
				return nil, err
			}
			defer dklssession.DklsKeyshareFree(share)
			return dklssession.DklsKeyshareToBytes(share)
		},
		abortMessage: dklssession.DklsKeygenSessionAbortMessage,
		free:         dklssession.DklsKeygenSessionFree,
	})
	if err != nil {
		return keygen.Response{
			Status: common.Fail,
			Blame:  dklsBlameFromError(err, ids, dklsKeygenAbortRound),
		}, err
	}
	t.logger.Info().
		Str("msg_id", msgID).
		Str("local_pubkey", t.localNodePubKey).
		Msg("DKLS keygen ceremony party produced")
	share, err := dklssession.DklsKeyshareFromBytes(keyshareHandleBytes)
	if err != nil {
		return keygen.Response{}, err
	}
	defer dklssession.DklsKeyshareFree(share)
	pkBytes, err := dklssession.DklsKeysharePublicKey(share)
	if err != nil {
		return keygen.Response{}, err
	}
	pubKey, err := dklsPubKeyFromCompressed(pkBytes)
	if err != nil {
		return keygen.Response{}, err
	}
	state := storage.KeygenLocalState{
		PubKey:          pubKey,
		LocalData:       keyshareHandleBytes,
		ParticipantKeys: ids,
		LocalPartyKey:   t.localNodePubKey,
		SigningEngine:   storage.SigningEngineDKLS,
	}
	if err := t.stateManager.SaveLocalState(state); err != nil {
		return keygen.Response{}, err
	}
	cmtPubKey := coskey.PubKey{Key: pkBytes}
	addr := cosmos.AccAddress(cmtPubKey.Address().Bytes())
	return keygen.NewResponse(tcommon.SigningAlgoSecp256k1, pubKey, addr.String(), common.Success, blame.Blame{}), nil
}

func (t *TssServer) DKLSKeySign(req keysign.Request, localStateItem storage.KeygenLocalState) (keysign.Response, error) {
	msgID, err := t.requestToMsgId(req)
	if err != nil {
		return keysign.Response{}, err
	}
	msgID += ":" + storage.SigningEngineDKLS
	allParticipants := append([]string(nil), localStateItem.ParticipantKeys...)
	sort.Strings(allParticipants)
	threshold, err := conversion.GetThreshold(len(allParticipants))
	if err != nil {
		return keysign.Response{}, err
	}
	resultCh := make(chan *p2p.Message, 1)
	keySignCh := make(chan *p2p.Message, len(allParticipants)*len(req.Messages)*1024)
	sessionChans := make([]chan *p2p.Message, len(req.Messages))
	setupChans := make([]chan *p2p.Message, len(req.Messages))
	demuxDone := make(chan struct{})
	t.p2pCommunication.SetSubscribe(messages.TSSDKLSKeySignMsg, msgID, keySignCh)
	t.p2pCommunication.SetSubscribe(messages.TSSDKLSKeySignResultMsg, msgID, resultCh)
	for i := range req.Messages {
		sessionChans[i] = make(chan *p2p.Message, len(allParticipants)*1024)
		setupChans[i] = make(chan *p2p.Message, len(allParticipants))
	}
	go t.demuxDKLSKeySignMessages(keySignCh, setupChans, sessionChans, demuxDone)
	defer func() {
		close(demuxDone)
		t.p2pCommunication.CancelSubscribe(messages.TSSDKLSKeySignMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSDKLSKeySignResultMsg, msgID)
		t.p2pCommunication.ReleaseStream(msgID)
		t.partyCoordinator.ReleaseStream(msgID)
	}()
	sigChan := make(chan string, 1)
	t.maybeAbortDKLSJoinTestHook(dklsAbortPhaseKeysign, msgID)
	onlinePeers, leader, err := t.joinParty(msgID, req.Version, req.BlockHeight, allParticipants, threshold, sigChan)
	if err != nil {
		return keysign.Response{
			Status: common.Fail,
			Blame:  dklsNodeSyncBlame(allParticipants, onlinePeers, dklsKeysignAbortRound, err),
		}, nil
	}
	signers, err := conversion.GetPubKeysFromPeerIDs(peerIDsToStrings(onlinePeers))
	if err != nil {
		return keysign.Response{}, err
	}
	sort.Strings(signers)
	leaderID := localStateItem.LocalPartyKey
	if leader != "NONE" {
		leaderID, err = conversion.GetPubKeyFromPeerID(leader)
		if err != nil {
			return keysign.Response{}, err
		}
	}
	resultWaitDone := make(chan struct{})
	defer close(resultWaitDone)
	type dklsResult struct {
		signatures []keysign.Signature
		err        error
	}
	resultDone := make(chan dklsResult, 1)
	go func() {
		sigs, err := t.receiveDKLSSignResult(resultCh, signers, req.PoolPubKey, req.Messages, resultWaitDone)
		resultDone <- dklsResult{signatures: sigs, err: err}
	}()
	if !containsString(signers, localStateItem.LocalPartyKey) {
		result := <-resultDone
		if result.err != nil {
			return keysign.Response{Status: common.Fail, Blame: dklsBlameFromErrorWithDefault(result.err, signers, dklsKeysignAbortRound, leaderID, "dkls default leader abort")}, nil
		}
		return keysign.NewResponse(result.signatures, common.Success, blame.Blame{}), nil
	}

	share, err := dklssession.DklsKeyshareFromBytes(localStateItem.LocalData)
	if err != nil {
		return keysign.Response{}, err
	}
	keyID, err := dklssession.DklsKeyshareKeyID(share)
	_ = dklssession.DklsKeyshareFree(share)
	if err != nil {
		return keysign.Response{}, err
	}
	_, idsBytes := dklsIDs(signers)

	generateDone := make(chan dklsResult, 1)
	go func() {
		signatures, err := t.generateDKLSSignatures(msgID, req, localStateItem, signers, leader, leaderID, idsBytes, keyID, setupChans, sessionChans)
		generateDone <- dklsResult{signatures: signatures, err: err}
	}()

	var generated dklsResult
	var received dklsResult
	var generatedDone bool
	var receivedDone bool
	for {
		select {
		case result := <-resultDone:
			received = result
			receivedDone = true
			if generatedDone && generated.err != nil && received.err == nil {
				return keysign.NewResponse(received.signatures, common.Success, blame.Blame{}), nil
			}
			if generatedDone && generated.err != nil {
				return keysign.Response{Status: common.Fail, Blame: dklsBlameFromErrorWithDefault(generated.err, signers, dklsKeysignAbortRound, leaderID, "dkls default leader abort")}, nil
			}
		case result := <-generateDone:
			generated = result
			generatedDone = true
			if generated.err == nil {
				if err := t.broadcastDKLSSignResult(msgID, localStateItem.LocalPartyKey, generated.signatures, allParticipants); err != nil {
					if b := dklsBlameFromError(err, signers, dklsKeysignAbortRound); len(b.BlameNodes) > 0 {
						return keysign.Response{Status: common.Fail, Blame: b}, nil
					}
					t.logger.Err(err).Msg("fail to broadcast DKLS keysign result")
				}
				return keysign.NewResponse(generated.signatures, common.Success, blame.Blame{}), nil
			}
			if receivedDone {
				if received.err == nil {
					return keysign.NewResponse(received.signatures, common.Success, blame.Blame{}), nil
				}
				return keysign.Response{Status: common.Fail, Blame: dklsBlameFromErrorWithDefault(generated.err, signers, dklsKeysignAbortRound, leaderID, "dkls default leader abort")}, nil
			}
		}
	}
}

func (t *TssServer) generateDKLSSignatures(
	msgID string,
	req keysign.Request,
	localStateItem storage.KeygenLocalState,
	signers []string,
	leader string,
	leaderID string,
	idsBytes []byte,
	keyID []byte,
	setupChans []chan *p2p.Message,
	sessionChans []chan *p2p.Message,
) ([]keysign.Signature, error) {
	signatures := make([]keysign.Signature, len(req.Messages))
	signSessionLimit := dklsMaxConcurrentSignSessions
	if signSessionLimit > len(req.Messages) {
		signSessionLimit = len(req.Messages)
	}
	if signSessionLimit == 0 {
		return signatures, nil
	}
	signSessionSem := make(chan struct{}, signSessionLimit)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	var firstErr error
	for idx, encodedMsg := range req.Messages {
		idx := idx
		encodedMsg := encodedMsg
		wg.Add(1)
		go func() {
			defer wg.Done()
			signSessionSem <- struct{}{}
			defer func() { <-signSessionSem }()
			msg, err := base64.StdEncoding.DecodeString(encodedMsg)
			if err != nil {
				resultMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				resultMu.Unlock()
				return
			}
			msgToSign := msg
			if len(msgToSign) != 32 {
				digest := sha256.Sum256(msgToSign)
				msgToSign = digest[:]
			}
			sessionMsgID := fmt.Sprintf("%s:sign:%d", msgID, idx)
			var setup []byte
			if t.p2pCommunication.GetLocalPeerID() == leader || leader == "NONE" {
				setup, err = dklssession.DklsSignSetupMsgNew(keyID, nil, msgToSign, idsBytes)
				if err != nil {
					resultMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					resultMu.Unlock()
					return
				}
				for _, id := range signers {
					if id == localStateItem.LocalPartyKey {
						continue
					}
					if err := t.sendDKLSSetup(msgID, messages.TSSDKLSKeySignMsg, idx, localStateItem.LocalPartyKey, id, setup); err != nil {
						resultMu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						resultMu.Unlock()
						return
					}
				}
			} else {
				setup, err = t.receiveDKLSSetup(setupChans[idx], localStateItem.LocalPartyKey, leaderID, t.conf.KeySignTimeout)
				if err != nil {
					resultMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					resultMu.Unlock()
					return
				}
			}
			share, err := dklssession.DklsKeyshareFromBytes(localStateItem.LocalData)
			if err != nil {
				resultMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				resultMu.Unlock()
				return
			}
			defer dklssession.DklsKeyshareFree(share)
			session, err := dklssession.DklsSignSessionFromSetup(setup, []byte(localStateItem.LocalPartyKey), share)
			if err != nil {
				resultMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				resultMu.Unlock()
				return
			}
			if t.p2pCommunication.GetLocalPeerID() == leader || leader == "NONE" {
				t.logger.Info().
					Str("msg_id", sessionMsgID).
					Str("pool_pub_key", req.PoolPubKey).
					Str("leader_pubkey", leaderID).
					Int("message_index", idx).
					Int("participants", len(signers)).
					Int64("block_height", req.BlockHeight).
					Msg("DKLS keysign ceremony leader started")
			}
			out, err := t.runDKLSSession(msgID, messages.TSSDKLSKeySignMsg, idx, sessionChans[idx], session, localStateItem.LocalPartyKey, signers, t.conf.KeySignTimeout, dklsAbortPhaseKeysign, leaderID, dklsSessionOps{
				output: dklssession.DklsSignSessionOutputMessage,
				receiver: func(h dklssession.Handle, msg []byte, idx int) (string, error) {
					receiver, err := dklssession.DklsSignSessionMessageReceiver(h, msg, idx)
					return string(receiver), err
				},
				input:        dklssession.DklsSignSessionInputMessage,
				finish:       dklssession.DklsSignSessionFinish,
				abortMessage: dklssession.DklsSignSessionAbortMessage,
				free:         dklssession.DklsSignSessionFree,
			})
			if err != nil {
				resultMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				resultMu.Unlock()
				return
			}
			if len(out) < 64 {
				resultMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("invalid DKLS signature length: %d", len(out))
				}
				resultMu.Unlock()
				return
			}
			t.logger.Info().
				Str("msg_id", sessionMsgID).
				Str("pool_pub_key", req.PoolPubKey).
				Str("local_pubkey", localStateItem.LocalPartyKey).
				Int("message_index", idx).
				Msg("DKLS keysign ceremony party produced")
			recoveryID := []byte{0}
			if len(out) > 64 {
				recoveryID[0] = out[64]
			}
			signatures[idx] = keysign.NewSignature(
				encodedMsg,
				base64.StdEncoding.EncodeToString(out[:32]),
				base64.StdEncoding.EncodeToString(out[32:64]),
				base64.StdEncoding.EncodeToString(recoveryID),
			)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	pk, err := tcommon.NewPubKey(req.PoolPubKey)
	if err != nil {
		return nil, err
	}
	if err := validateDKLSSignatures(pk, signatures, req.Messages); err != nil {
		return nil, err
	}
	return signatures, nil
}

func containsString(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}

func dklsSignaturesEqual(a, b []keysign.Signature) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func dklsBytesEqual(a, b []byte) bool {
	return bytes.Equal(a, b)
}

func peerIDsToStrings(pIDs []peer.ID) []string {
	out := make([]string, 0, len(pIDs))
	for _, id := range pIDs {
		out = append(out, id.String())
	}
	return out
}
