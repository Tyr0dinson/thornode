//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"gitlab.com/thorchain/tss/thor-go-frost/gofrost"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const gofrostKeygenProtocol = "keygen"

type taprootDKGStage int

const (
	taprootDKGStageRound1 taprootDKGStage = iota + 1
	taprootDKGStageRound2
	taprootDKGStageRound3
)

// taprootDKGSession is one keygen ceremony of this node.
type taprootDKGSession struct {
	id           string
	participants []string // canonical order: participants[i] holds index i+1
	localIndex   uint32
	threshold    uint32
	stage        taprootDKGStage
	// round1 holds every member's raw round-1 message by participant index;
	// messages the decoded ones. This node's own entry is set in round 1.
	round1   map[uint32][]byte
	messages map[uint32]*round1Message
	// lockKeys are this node's throwaway keys, one per sender index.
	lockKeys   map[uint32]*lockKey
	transcript []byte
	expiry     *time.Timer
	handle     gofrost.Handle
	handleOpen bool
	result     *gofrost.TaprootKeygenResult
	address    string
	groupKey   string
	created    time.Time
}

func (s *taprootDKGSession) local() string {
	return s.participants[s.localIndex-1]
}

func (s *taprootDKGSession) wipeLockKeys() {
	for _, key := range s.lockKeys {
		key.wipe()
	}
	s.lockKeys = nil
}

// release frees the library session and wipes the keyshare and lock keys.
func (s *taprootDKGSession) release() {
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	s.wipeLockKeys()
	if s.handleOpen {
		_ = gofrost.SessionFree(s.handle)
		s.handleOpen = false
	}
	if s.result != nil {
		gofrost.Wipe(s.result.Keyshare)
		s.result = nil
	}
}

type taprootDKGEngine struct {
	chain   common.Chain
	local   string
	nodeKey *ecdsa.PrivateKey
	ttl     time.Duration
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]*taprootDKGSession
}

// NewTaprootDKGEngine returns the FROST DKG engine of this node for a chain
// with Taproot vaults.
func NewTaprootDKGEngine(cfg TaprootDKGConfig) (TaprootDKGEngine, error) {
	if !cfg.Chain.SupportsTaprootVaults() {
		return nil, fmt.Errorf("chain %s has no taproot vaults in this build", cfg.Chain)
	}
	if err := gofrost.CheckRuntimeCompatibility(); err != nil {
		return nil, err
	}
	local := strings.TrimSpace(cfg.NodePubKey.String())
	nodeKey, err := nodePublicKey(local)
	if err != nil {
		return nil, err
	}
	privKey, err := ethcrypto.ToECDSA(cfg.NodePrivKey)
	if err != nil {
		return nil, fmt.Errorf("invalid node private key: %w", err)
	}
	if !privKey.PublicKey.Equal(nodeKey) {
		return nil, errors.New("node private key does not match the node pubkey")
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = DefaultTaprootDKGSessionTTL
	}
	return &taprootDKGEngine{
		chain:    cfg.Chain,
		local:    local,
		nodeKey:  privKey,
		ttl:      ttl,
		now:      time.Now,
		sessions: make(map[string]*taprootDKGSession),
	}, nil
}

// FrostDKGRound1 opens the keygen session and returns this node's round-1
// commitment package.
func (e *taprootDKGEngine) FrostDKGRound1(req tssp.FrostSidecarDKGRound1Req) (tssp.FrostSidecarDKGRound1Resp, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return tssp.FrostSidecarDKGRound1Resp{}, errors.New("frost dkg session id is empty")
	}
	if previous, exists := e.sessions[sessionID]; exists {
		// Only this node's coordinator starts sessions. Starting one again
		// means the earlier attempt is dead: release it so a retry is not
		// blocked, and never carry its key material over.
		e.dropLocked(previous)
	}
	n := len(req.Participants)
	if n < 2 || uint32(n) != req.N {
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("frost dkg needs the full participant list (%d listed, n=%d, minimum 2)", n, req.N)
	}
	if req.ParticipantIndex == 0 || req.ParticipantIndex > uint32(n) {
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("frost dkg participant index %d is out of range", req.ParticipantIndex)
	}
	if req.Threshold == 0 || req.Threshold > uint32(n) || req.Threshold > math.MaxUint16 {
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("frost dkg threshold %d is out of range for %d participants", req.Threshold, n)
	}
	participants := make([]string, n)
	seen := make(map[string]struct{}, n)
	for i, participant := range req.Participants {
		participant = strings.TrimSpace(participant)
		// Every participant must be a node key shares can be locked to.
		if _, err := nodePublicKey(participant); err != nil {
			return tssp.FrostSidecarDKGRound1Resp{}, err
		}
		if _, dup := seen[participant]; dup {
			return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("duplicate frost dkg participant %s", participant)
		}
		seen[participant] = struct{}{}
		participants[i] = participant
	}
	if participants[req.ParticipantIndex-1] != e.local {
		return tssp.FrostSidecarDKGRound1Resp{}, errors.New("frost dkg participant index does not point at this node")
	}

	handle, err := gofrost.KeygenSessionNew(sessionID, participants, e.local, uint16(req.Threshold))
	if err != nil {
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("open frost dkg session: %w", err)
	}
	session := &taprootDKGSession{
		id:           sessionID,
		participants: participants,
		localIndex:   req.ParticipantIndex,
		threshold:    req.Threshold,
		stage:        taprootDKGStageRound1,
		round1:       make(map[uint32][]byte, n),
		messages:     make(map[uint32]*round1Message, n),
		lockKeys:     make(map[uint32]*lockKey, n-1),
		handle:       handle,
		handleOpen:   true,
		created:      e.now(),
	}
	outputs, err := gofrost.DrainSessionOutputMessages(handle)
	if err != nil {
		session.release()
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("read frost dkg round1 output: %w", err)
	}
	if len(outputs) != 1 || !outputs[0].RequiresSecureBroadcast || outputs[0].RequiresConfidentialTransport {
		session.release()
		return tssp.FrostSidecarDKGRound1Resp{}, errors.New("unexpected frost dkg round1 output")
	}
	// One fresh lock key per other member, announced in ascending index order.
	announced := make([]*ecdsa.PublicKey, 0, n-1)
	for index := uint32(1); index <= uint32(n); index++ {
		if index == session.localIndex {
			continue
		}
		key, keyErr := newLockKey()
		if keyErr != nil {
			session.release()
			return tssp.FrostSidecarDKGRound1Resp{}, keyErr
		}
		session.lockKeys[index] = key
		announced = append(announced, key.pub)
	}
	message, err := encodeRound1Message(e.nodeKey, session.round1Context(session.localIndex), outputs[0].Message, announced)
	if err != nil {
		session.release()
		return tssp.FrostSidecarDKGRound1Resp{}, err
	}
	session.round1[session.localIndex] = message
	e.sessions[sessionID] = session
	// Release the session and its key material after the TTL even if no other
	// call ever reaches the engine.
	session.expiry = time.AfterFunc(e.ttl, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if current, ok := e.sessions[sessionID]; ok && current == session {
			e.dropLocked(current)
		}
	})
	return tssp.FrostSidecarDKGRound1Resp{Commitments: hex.EncodeToString(message)}, nil
}

// FrostDKGRound2 feeds every other participant's round-1 package to the session
// and returns this node's round-2 shares, each locked to its receiver.
func (e *taprootDKGEngine) FrostDKGRound2(req tssp.FrostSidecarDKGRound2Req) (resp tssp.FrostSidecarDKGRound2Resp, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	session, err := e.sessionLocked(req.SessionID, taprootDKGStageRound1)
	if err != nil {
		return tssp.FrostSidecarDKGRound2Resp{}, err
	}
	defer func() {
		if err != nil {
			e.dropLocked(session)
		}
	}()

	for index := uint32(1); index <= uint32(len(session.participants)); index++ {
		if index == session.localIndex {
			continue
		}
		sender := session.participants[index-1]
		raw, decodeErr := hex.DecodeString(strings.TrimSpace(req.AllCommitments[indexKey(index)]))
		if decodeErr != nil || len(raw) == 0 {
			return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("missing or malformed round1 message from %s", sender)
		}
		senderKey, keyErr := nodePublicKey(sender)
		if keyErr != nil {
			return tssp.FrostSidecarDKGRound2Resp{}, keyErr
		}
		message, msgErr := decodeRound1Message(session.round1Context(index), senderKey, raw)
		if msgErr != nil {
			return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("round1 message from %s rejected: %w", sender, msgErr)
		}
		if _, err = gofrost.InputBoundSessionMessage(session.handle, gofrostKeygenProtocol, session.id, sender, message.frost); err != nil {
			// The library checks the packages once the last one is in; its
			// error names the participant at fault.
			return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("frost dkg round1 packages rejected: %w", err)
		}
		session.round1[index] = raw
		session.messages[index] = message
	}
	session.transcript = session.transcriptHash()

	outputs, err := gofrost.DrainSessionOutputMessages(session.handle)
	defer func() {
		for i := range outputs {
			gofrost.Wipe(outputs[i].Message)
		}
	}()
	if err != nil {
		return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("read frost dkg round2 output: %w", err)
	}
	if len(outputs) != len(session.participants)-1 {
		return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("frost dkg round2 produced %d shares for %d receivers", len(outputs), len(session.participants)-1)
	}
	shares := make(map[string]string, len(outputs))
	for _, output := range outputs {
		// A round-2 share must go to exactly one other participant and must
		// never leave this process unlocked.
		if !output.RequiresConfidentialTransport || output.RequiresSecureBroadcast || len(output.Receivers) != 1 {
			return tssp.FrostSidecarDKGRound2Resp{}, errors.New("unexpected frost dkg round2 output")
		}
		receiverIndex := session.indexOf(output.Receivers[0])
		if receiverIndex == 0 || receiverIndex == session.localIndex {
			return tssp.FrostSidecarDKGRound2Resp{}, errors.New("frost dkg round2 share has an invalid receiver")
		}
		if _, dup := shares[indexKey(receiverIndex)]; dup {
			return tssp.FrostSidecarDKGRound2Resp{}, errors.New("frost dkg round2 produced two shares for one receiver")
		}
		lockPub, keyErr := session.messages[receiverIndex].lockKeyFor(receiverIndex, session.localIndex)
		if keyErr != nil {
			return tssp.FrostSidecarDKGRound2Resp{}, keyErr
		}
		label, labelErr := session.label(session.localIndex, receiverIndex, lockPub)
		if labelErr != nil {
			return tssp.FrostSidecarDKGRound2Resp{}, labelErr
		}
		sealed, sealErr := sealShare(e.nodeKey, label, output.Message)
		if sealErr != nil {
			return tssp.FrostSidecarDKGRound2Resp{}, sealErr
		}
		shares[indexKey(receiverIndex)] = hex.EncodeToString(sealed)
	}
	session.stage = taprootDKGStageRound2
	return tssp.FrostSidecarDKGRound2Resp{EncryptedShares: shares}, nil
}

// FrostDKGRound3 opens the shares addressed to this node, completes the keygen
// and returns the vault address and group key.
func (e *taprootDKGEngine) FrostDKGRound3(req tssp.FrostSidecarDKGRound3Req) (resp tssp.FrostSidecarDKGRound3Resp, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	session, err := e.sessionLocked(req.SessionID, taprootDKGStageRound2)
	if err != nil {
		return tssp.FrostSidecarDKGRound3Resp{}, err
	}
	defer func() {
		if err != nil {
			e.dropLocked(session)
		}
	}()

	for index := uint32(1); index <= uint32(len(session.participants)); index++ {
		if index == session.localIndex {
			continue
		}
		sender := session.participants[index-1]
		// A share that is missing, malformed or not validly signed is the
		// sender's fault on its face: every member can see that from the
		// broadcast, so no lock key is revealed for it.
		sealed, decodeErr := hex.DecodeString(strings.TrimSpace(req.AllShares[indexKey(index)]))
		if decodeErr != nil || len(sealed) == 0 {
			return tssp.FrostSidecarDKGRound3Resp{}, session.dispute(index, false, "it is missing or malformed")
		}
		key := session.lockKeys[index]
		label, labelErr := session.label(index, session.localIndex, key.pub)
		if labelErr != nil {
			return tssp.FrostSidecarDKGRound3Resp{}, labelErr
		}
		locked, sigErr := verifySealedShare(label, sealed)
		if sigErr != nil {
			return tssp.FrostSidecarDKGRound3Resp{}, session.dispute(index, false, "it is not validly signed")
		}
		// Only now does a secret matter: the signature is good but the lock
		// does not open. The others can only see that with the lock key.
		share, openErr := unlockShare(label, key.priv, locked)
		if openErr != nil {
			return tssp.FrostSidecarDKGRound3Resp{}, session.dispute(index, true, "its lock does not open")
		}
		_, inputErr := gofrost.InputBoundSessionMessage(session.handle, gofrostKeygenProtocol, session.id, sender, share)
		gofrost.Wipe(share)
		if inputErr != nil {
			// The library checks every share once the last one is in, so the
			// sender whose input surfaced the error is not necessarily the
			// one whose share is bad. Dispute only a sender the library names,
			// and reveal nothing when it names none.
			culprit := session.culpritIndex(inputErr)
			if culprit == 0 {
				return tssp.FrostSidecarDKGRound3Resp{}, fmt.Errorf("frost dkg round3 rejected a share: %w", inputErr)
			}
			return tssp.FrostSidecarDKGRound3Resp{}, session.dispute(culprit, true, "the keygen library rejected its content")
		}
	}

	progress, err := gofrost.GetSessionProgress(session.handle)
	if err != nil {
		return tssp.FrostSidecarDKGRound3Resp{}, fmt.Errorf("read frost dkg progress: %w", err)
	}
	if !progress.ReadyToFinish {
		return tssp.FrostSidecarDKGRound3Resp{}, fmt.Errorf("frost dkg is not complete after round3 (round %s, missing %v)", progress.Round, progress.MissingParticipants)
	}
	// On failure the deferred drop frees the handle; freeing one the finish
	// call already consumed is rejected by the library and ignored here.
	result, err := gofrost.FinishTaprootKeygenAndFree(session.handle, session.local())
	if err != nil {
		return tssp.FrostSidecarDKGRound3Resp{}, fmt.Errorf("finish frost dkg: %w", err)
	}
	session.handleOpen = false
	session.result = &result
	// Every share is in, so the lock keys have done their job.
	session.wipeLockKeys()

	address, err := e.vaultAddress(result.PublicKeyCompressed)
	if err != nil {
		return tssp.FrostSidecarDKGRound3Resp{}, err
	}
	session.address = address
	session.groupKey = hex.EncodeToString(result.PublicKeyCompressed)
	session.stage = taprootDKGStageRound3
	return tssp.FrostSidecarDKGRound3Resp{Address: session.address, GroupKey: session.groupKey}, nil
}

// FrostDKGFinalize hands out this node's keyshare for exactly the vault the
// session produced, then forgets the session.
func (e *taprootDKGEngine) FrostDKGFinalize(req tssp.FrostSidecarDKGFinalizeReq) (tssp.FrostSidecarDKGFinalizeResp, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	session, err := e.sessionLocked(req.SessionID, taprootDKGStageRound3)
	if err != nil {
		return tssp.FrostSidecarDKGFinalizeResp{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(req.GroupKey), session.groupKey) || strings.TrimSpace(req.Address) != session.address {
		// Fail closed: a result for another vault must never be paired with
		// this node's keyshare. The session stays so the right result can
		// still finalize it.
		return tssp.FrostSidecarDKGFinalizeResp{}, errors.New("frost dkg result does not match this node's keygen session")
	}
	if _, err = gofrost.ValidateKeyshareForTaproot(session.result.Keyshare, session.local(), session.result.OutputKeyXOnly); err != nil {
		e.dropLocked(session)
		return tssp.FrostSidecarDKGFinalizeResp{}, fmt.Errorf("frost dkg keyshare failed validation: %w", err)
	}
	keyBytesHex := hex.EncodeToString(session.result.Keyshare)
	e.dropLocked(session)
	return tssp.FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: keyBytesHex}, nil
}

func (e *taprootDKGEngine) AbortSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if session, ok := e.sessions[strings.TrimSpace(sessionID)]; ok {
		e.dropLocked(session)
	}
}

func (e *taprootDKGEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, session := range e.sessions {
		session.release()
	}
	e.sessions = make(map[string]*taprootDKGSession)
}

func (e *taprootDKGEngine) sessionLocked(sessionID string, stage taprootDKGStage) (*taprootDKGSession, error) {
	session, ok := e.sessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, fmt.Errorf("frost dkg session %s not found", strings.TrimSpace(sessionID))
	}
	if session.stage != stage {
		return nil, fmt.Errorf("frost dkg session %s is not ready for this step", session.id)
	}
	return session, nil
}

func (e *taprootDKGEngine) dropLocked(session *taprootDKGSession) {
	session.release()
	delete(e.sessions, session.id)
}

// expireLocked releases sessions that were never finalized.
func (e *taprootDKGEngine) expireLocked() {
	now := e.now()
	for _, session := range e.sessions {
		if now.Sub(session.created) > e.ttl {
			e.dropLocked(session)
		}
	}
}

// round1Context is the signing context of one member's round-1 message.
func (s *taprootDKGSession) round1Context(senderIndex uint32) round1Context {
	return round1Context{
		sessionID:    s.id,
		threshold:    s.threshold,
		participants: s.participants,
		senderIndex:  senderIndex,
	}
}

// transcriptHash binds the session parameters and every round-1 message.
func (s *taprootDKGSession) transcriptHash() []byte {
	round1 := make([][]byte, len(s.participants))
	for i := range s.participants {
		round1[i] = s.round1[uint32(i+1)]
	}
	return transcriptHash(s.id, s.threshold, s.participants, round1)
}

// label builds a share label from the session's own state, never from a
// received share.
func (s *taprootDKGSession) label(senderIndex, receiverIndex uint32, lockPub *ecdsa.PublicKey) (shareLabel, error) {
	sender, err := nodePublicKey(s.participants[senderIndex-1])
	if err != nil {
		return shareLabel{}, err
	}
	receiver, err := nodePublicKey(s.participants[receiverIndex-1])
	if err != nil {
		return shareLabel{}, err
	}
	return shareLabel{
		sessionID:     s.id,
		transcript:    s.transcript,
		senderIndex:   senderIndex,
		receiverIndex: receiverIndex,
		sender:        sender,
		receiver:      receiver,
		lockKey:       lockPub,
	}, nil
}

// dispute builds the error that ends round 3 over one sender's share. With
// reveal it carries the throwaway lock key this node announced for that sender,
// which opens that sender's share and nothing else.
func (s *taprootDKGSession) dispute(senderIndex uint32, reveal bool, reason string) *ShareDisputeError {
	out := &ShareDisputeError{
		SessionID:   s.id,
		Sender:      s.participants[senderIndex-1],
		SenderIndex: senderIndex,
		Reason:      reason,
	}
	if reveal {
		out.RevealedLockKeyHex = hex.EncodeToString(s.lockKeys[senderIndex].priv)
	}
	return out
}

// culpritIndex returns the one other participant the library holds responsible
// for err, or zero when it names none or more than one.
func (s *taprootDKGSession) culpritIndex(err error) uint32 {
	var ffiErr *gofrost.FFIError
	if !errors.As(err, &ffiErr) {
		return 0
	}
	culprits := ffiErr.BlameParticipants()
	if len(culprits) != 1 {
		return 0
	}
	index := s.indexOf(culprits[0])
	if index == s.localIndex {
		return 0
	}
	return index
}

func (s *taprootDKGSession) indexOf(participant string) uint32 {
	for i, candidate := range s.participants {
		if candidate == participant {
			return uint32(i + 1)
		}
	}
	return 0
}

// vaultAddress derives the chain's Taproot address of the DKG output key. The
// key is already the final Taproot output key, so it is used as is.
func (e *taprootDKGEngine) vaultAddress(compressedKey []byte) (string, error) {
	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &secp256k1.PubKey{Key: compressedKey})
	if err != nil {
		return "", fmt.Errorf("encode frost group key: %w", err)
	}
	vaultKey, err := common.NewPubKey(bech)
	if err != nil {
		return "", fmt.Errorf("encode frost group key: %w", err)
	}
	address, err := vaultKey.GetAddressForAlgo(e.chain, common.SigningAlgoSecp256k1Taproot)
	if err != nil {
		return "", fmt.Errorf("derive %s taproot address: %w", e.chain, err)
	}
	return address.String(), nil
}

func indexKey(index uint32) string {
	return strconv.FormatUint(uint64(index), 10)
}

// JudgeShareDispute lets any keygen member judge a disputed round-2 share from
// public material: the keygen parameters, the round-1 messages and the sealed
// share it received in the broadcasts, and the lock key the receiver revealed.
// An error means the case itself is unusable (for example a round-1 message
// that does not verify).
//
// Honest members reach the same verdict only if they judge the same material.
// The round-1 messages must be the set the keygen coordinator confirmed with
// every member before round 2: a member that signs two different round-1
// messages could otherwise make an honest sender's share fail verification for
// those who hold the other one. The sealed share must be the one the member
// received in the round-2 broadcast.
func JudgeShareDispute(c ShareDisputeCase) (ShareDisputeVerdict, []byte, error) {
	n := uint32(len(c.Participants))
	if n < 2 || c.SenderIndex == 0 || c.ReceiverIndex == 0 || c.SenderIndex > n || c.ReceiverIndex > n || c.SenderIndex == c.ReceiverIndex {
		return 0, nil, errors.New("share dispute has invalid participants")
	}
	if c.Threshold == 0 || c.Threshold > n || strings.TrimSpace(c.SessionID) == "" {
		return 0, nil, errors.New("share dispute has an invalid session or threshold")
	}
	seen := make(map[string]struct{}, n)
	for _, participant := range c.Participants {
		if participant == "" || strings.TrimSpace(participant) != participant {
			return 0, nil, errors.New("share dispute participants must be trimmed and non-empty")
		}
		if _, dup := seen[participant]; dup {
			return 0, nil, errors.New("share dispute has a duplicate participant")
		}
		seen[participant] = struct{}{}
	}
	session := &taprootDKGSession{
		id:           strings.TrimSpace(c.SessionID),
		participants: c.Participants,
		threshold:    c.Threshold,
		round1:       make(map[uint32][]byte, n),
		messages:     make(map[uint32]*round1Message, n),
	}
	for index := uint32(1); index <= n; index++ {
		raw, err := hex.DecodeString(strings.TrimSpace(c.Round1[indexKey(index)]))
		if err != nil || len(raw) == 0 {
			return 0, nil, fmt.Errorf("share dispute is missing the round1 message of participant %d", index)
		}
		key, err := nodePublicKey(c.Participants[index-1])
		if err != nil {
			return 0, nil, err
		}
		message, err := decodeRound1Message(session.round1Context(index), key, raw)
		if err != nil {
			return 0, nil, fmt.Errorf("share dispute round1 message of participant %d: %w", index, err)
		}
		session.round1[index] = raw
		session.messages[index] = message
	}
	session.transcript = session.transcriptHash()
	lockPub, err := session.messages[c.ReceiverIndex].lockKeyFor(c.ReceiverIndex, c.SenderIndex)
	if err != nil {
		return 0, nil, err
	}
	label, err := session.label(c.SenderIndex, c.ReceiverIndex, lockPub)
	if err != nil {
		return 0, nil, err
	}
	sealed, err := hex.DecodeString(strings.TrimSpace(c.SealedShareHex))
	if err != nil || len(sealed) == 0 {
		// The sender broadcast nothing usable for this receiver.
		return ShareDisputeSenderAtFault, nil, nil //nolint:nilerr // a verdict, not a failure to judge
	}
	revealed, err := hex.DecodeString(strings.TrimSpace(c.RevealedLockKeyHex))
	if err != nil {
		return ShareDisputeReceiverAtFault, nil, nil //nolint:nilerr // as above
	}
	return judgeSealedShare(label, sealed, revealed)
}
