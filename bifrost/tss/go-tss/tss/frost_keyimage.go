package tss

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const frostKeyImageMsgIDDomain = "thorchain-frost-keyimage-msgid-v1"
const frostKeyImageCoordinationHeightDomain = "thorchain-frost-keyimage-coordination-height-v1"

type FrostKeyImageCoordinator struct {
	logger           zerolog.Logger
	p2pCommunication *p2p.Communication
	sidecar          FrostSidecarClient
	localPubKey      string
	msgID            string
	msgChan          chan *p2p.Message
	timeout          time.Duration
}

func NewFrostKeyImageCoordinator(
	comm *p2p.Communication,
	sidecar FrostSidecarClient,
	localPubKey string,
	msgID string,
	msgChan chan *p2p.Message,
	timeout time.Duration,
) *FrostKeyImageCoordinator {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &FrostKeyImageCoordinator{
		logger:           log.With().Str("module", "frost_keyimage").Str("msg_id", msgID).Logger(),
		p2pCommunication: comm,
		sidecar:          sidecar,
		localPubKey:      strings.TrimSpace(localPubKey),
		msgID:            strings.TrimSpace(msgID),
		msgChan:          msgChan,
		timeout:          timeout,
	}
}

func FrostKeyImageMsgID(req FrostKeyImageRequest) (string, error) {
	vault := strings.TrimSpace(req.VaultPubKey)
	txHash := strings.ToLower(strings.TrimSpace(req.TxHash))
	if vault == "" {
		return "", fmt.Errorf("vault pubkey is required")
	}
	if txHash == "" {
		return "", fmt.Errorf("tx hash is required")
	}
	membersHash, err := frostSignerSetHash(req.SignerPubKeys)
	if err != nil {
		return "", fmt.Errorf("fail to hash signer membership: %w", err)
	}
	canonical := strings.Join([]string{
		"domain=" + frostKeyImageMsgIDDomain,
		"vault_pub_key=" + vault,
		"tx_hash=" + txHash,
		"output_index=" + strconv.FormatUint(uint64(req.OutputIndex), 10),
		"members_hash=" + membersHash,
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func frostKeyImageCoordinationHeight(msgID string) int64 {
	sum := sha256.Sum256([]byte(frostKeyImageCoordinationHeightDomain + "\n" + strings.TrimSpace(msgID)))
	height := int64(binary.BigEndian.Uint64(sum[:8]) & uint64(math.MaxInt64))
	if height <= 0 {
		return 1
	}
	return height
}

func (t *TssServer) FrostKeyImage(req FrostKeyImageRequest, sidecar FrostSidecarClient) (FrostKeyImageResponse, error) {
	if sidecar == nil {
		return FrostKeyImageResponse{}, fmt.Errorf("frost sidecar client is nil")
	}
	if len(req.SignerPubKeys) == 0 {
		return FrostKeyImageResponse{}, fmt.Errorf("signer pub keys are empty")
	}
	msgID, err := FrostKeyImageMsgID(req)
	if err != nil {
		return FrostKeyImageResponse{}, fmt.Errorf("fail to compute frost key image msgID: %w", err)
	}
	coordinationHeight := frostKeyImageCoordinationHeight(msgID)
	threshold, err := conversion.GetThreshold(len(req.SignerPubKeys))
	if err != nil {
		return FrostKeyImageResponse{}, fmt.Errorf("fail to compute frost threshold: %w", err)
	}
	predictedLeader := ""
	if peerIDs, peerErr := conversion.GetPeerIDsFromPubKeys(req.SignerPubKeys); peerErr == nil {
		peerIDStrings := make([]string, 0, len(peerIDs))
		for _, peerID := range peerIDs {
			peerIDStrings = append(peerIDStrings, peerID.String())
		}
		if leader, leaderErr := p2p.LeaderNode(msgID, coordinationHeight, peerIDStrings); leaderErr == nil {
			predictedLeader = leader
		}
	}
	t.logger.Info().
		Str("msg_id", msgID).
		Str("vault_pub_key", strings.TrimSpace(req.VaultPubKey)).
		Str("tx_hash", strings.ToLower(strings.TrimSpace(req.TxHash))).
		Uint32("output_index", req.OutputIndex).
		Int64("requested_block_height", req.BlockHeight).
		Int64("coordination_height", coordinationHeight).
		Str("predicted_leader", predictedLeader).
		Strs("signer_pub_keys", req.SignerPubKeys).
		Msg("starting frost key-image ceremony")

	msgChan := make(chan *p2p.Message, 1024)
	sigChan := make(chan string, 2)

	t.p2pCommunication.SetSubscribe(messages.TSSFrostPreprocessMsg, msgID, msgChan)
	t.p2pCommunication.SetSubscribe(messages.TSSFrostShareMsg, msgID, msgChan)
	t.p2pCommunication.SetSubscribe(messages.TSSFrostVerifyMsg, msgID, msgChan)
	defer func() {
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostPreprocessMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostShareMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostVerifyMsg, msgID)
		t.p2pCommunication.ReleaseStream(msgID)
		t.partyCoordinator.ReleaseStream(msgID)
		close(sigChan)
	}()

	onlinePeers, _, errJoinParty := t.joinParty(
		msgID,
		req.Version,
		coordinationHeight,
		req.SignerPubKeys,
		threshold,
		sigChan,
	)
	if errJoinParty != nil {
		t.logger.Warn().
			Err(errJoinParty).
			Str("msg_id", msgID).
			Str("vault_pub_key", strings.TrimSpace(req.VaultPubKey)).
			Str("tx_hash", strings.ToLower(strings.TrimSpace(req.TxHash))).
			Uint32("output_index", req.OutputIndex).
			Int64("requested_block_height", req.BlockHeight).
			Int64("coordination_height", coordinationHeight).
			Str("predicted_leader", predictedLeader).
			Strs("signer_pub_keys", req.SignerPubKeys).
			Msg("frost key-image join party failed")
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(errJoinParty.Error(), nil),
		}, nil
	}

	activeParticipants, err := conversion.GetPubKeysFromPeerIDs(peerIDsToStrings(onlinePeers))
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("fail to resolve active participants", nil),
		}, nil
	}

	isActive := false
	ourPeerID := t.p2pCommunication.GetLocalPeerID()
	for _, p := range onlinePeers {
		if p.String() == ourPeerID {
			isActive = true
			break
		}
	}
	t.logger.Info().
		Str("msg_id", msgID).
		Str("vault_pub_key", strings.TrimSpace(req.VaultPubKey)).
		Str("tx_hash", strings.ToLower(strings.TrimSpace(req.TxHash))).
		Uint32("output_index", req.OutputIndex).
		Str("local_peer_id", ourPeerID).
		Interface("online_peer_ids", peerIDsToStrings(onlinePeers)).
		Strs("active_participants", activeParticipants).
		Bool("local_is_active", isActive).
		Msg("frost key-image join party succeeded")
	if !isActive {
		t.logger.Info().
			Str("msg_id", msgID).
			Str("vault_pub_key", strings.TrimSpace(req.VaultPubKey)).
			Str("tx_hash", strings.ToLower(strings.TrimSpace(req.TxHash))).
			Uint32("output_index", req.OutputIndex).
			Msg("local node will not execute frost key-image ceremony because it is not in the active signer set")
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in active signer set", nil),
		}, nil
	}

	coordinator := NewFrostKeyImageCoordinator(
		t.p2pCommunication,
		sidecar,
		t.localNodePubKey,
		msgID,
		msgChan,
		t.conf.KeySignTimeout,
	)
	resp := coordinator.Execute(req, activeParticipants, onlinePeers)
	t.logger.Info().
		Str("msg_id", msgID).
		Str("vault_pub_key", strings.TrimSpace(req.VaultPubKey)).
		Str("tx_hash", strings.ToLower(strings.TrimSpace(req.TxHash))).
		Uint32("output_index", req.OutputIndex).
		Int("status", int(resp.Status)).
		Str("fail_reason", resp.Blame.FailReason).
		Msg("completed frost key-image ceremony")
	return resp, nil
}

func (c *FrostKeyImageCoordinator) Execute(
	req FrostKeyImageRequest,
	activeParticipants []string,
	activePeerIDs []peer.ID,
) FrostKeyImageResponse {
	if c.sidecar == nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost sidecar client is nil", nil),
		}
	}
	if c.msgID == "" {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost msgID is empty", nil),
		}
	}
	_, participantIndexMap, originalByNormalized, err := buildParticipantIndex(req.SignerPubKeys)
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}
	activeSet, err := buildActiveSet(activeParticipants, originalByNormalized)
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}
	localNorm := normalizePubKey(c.localPubKey)
	localIndex, ok := participantIndexMap[localNorm]
	if !ok {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in participant set", nil),
		}
	}
	if _, ok := activeSet.originalByNormalized[localNorm]; !ok {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in active signer set", nil),
		}
	}

	prepareResp, err := c.sidecar.FrostKeyImagePrepare(FrostSidecarKeyImagePrepareReq{
		MsgID:       c.msgID,
		VaultPubKey: req.VaultPubKey,
		TxHash:      req.TxHash,
		OutputIndex: req.OutputIndex,
	})
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost key image prepare failed: %v", err), nil),
		}
	}

	preprocesses, failResp := c.exchangePreprocess(
		activeSet,
		participantIndexMap,
		activePeerIDs,
		FrostPreprocessPayload{
			SenderPubKey:     c.localPubKey,
			ParticipantIndex: localIndex,
			PreprocessHex:    prepareResp.Preprocess,
		},
	)
	if failResp != nil {
		return *failResp
	}

	partialResp, err := c.sidecar.FrostKeyImagePartial(FrostSidecarKeyImagePartialReq{
		MsgID:           c.msgID,
		SessionID:       prepareResp.SessionID,
		AllPreprocesses: preprocesses,
	})
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost key image partial failed: %v", err), nil),
		}
	}

	shares, failResp := c.exchangeShares(
		activeSet,
		participantIndexMap,
		activePeerIDs,
		FrostSharePayload{
			SenderPubKey:     c.localPubKey,
			ParticipantIndex: localIndex,
			ShareHex:         partialResp.SignatureShare,
		},
	)
	if failResp != nil {
		return *failResp
	}

	combineResp, err := c.sidecar.FrostKeyImageCombine(FrostSidecarKeyImageCombineReq{
		MsgID:     c.msgID,
		SessionID: prepareResp.SessionID,
		AllShares: shares,
	})
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost key image combine failed: %v", err), nil),
		}
	}
	validateResp, err := c.sidecar.FrostKeyImageValidate(FrostSidecarKeyImageValidateReq{
		VaultPubKey: req.VaultPubKey,
		TxHash:      req.TxHash,
		OutputIndex: req.OutputIndex,
		KeyImage:    combineResp.KeyImage,
		ProofC:      combineResp.ProofC,
		ProofS:      combineResp.ProofS,
	})
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost key image validate failed: %v", err), nil),
		}
	}
	if !validateResp.Valid {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost key image validate returned invalid=false", nil),
		}
	}

	return FrostKeyImageResponse{
		MsgID:       c.msgID,
		TxHash:      strings.ToLower(strings.TrimSpace(combineResp.TxHash)),
		OutputIndex: combineResp.OutputIndex,
		KeyImage:    strings.ToLower(strings.TrimSpace(combineResp.KeyImage)),
		ProofC:      strings.ToLower(strings.TrimSpace(combineResp.ProofC)),
		ProofS:      strings.ToLower(strings.TrimSpace(combineResp.ProofS)),
		Status:      tcommon.Success,
		Blame:       blame.Blame{},
	}
}

func (c *FrostKeyImageCoordinator) exchangePreprocess(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostPreprocessPayload,
) (map[string]string, *FrostKeyImageResponse) {
	payload, err := json.Marshal(local)
	if err != nil {
		resp := FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to marshal preprocess payload: %v", err), nil),
		}
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostPreprocessMsg, payload, activePeerIDs); err != nil {
		resp := FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to broadcast preprocess payload: %v", err), nil),
		}
		return nil, &resp
	}
	localHash, err := FrostTranscriptPreprocessHash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local preprocess payload: %v", err), frostRoundPreprocess, []string{local.SenderPubKey})
		return nil, &resp
	}
	verifier, err := newFrostTranscriptVerifier(frostRoundPreprocess, activeSet, participantIndexMap)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to initialize preprocess transcript verifier: %v", err), frostRoundPreprocess, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordPayload(local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, frostRoundPreprocess, failure.blamePubKey)
		return nil, &resp
	}
	if err := c.broadcastTranscriptVerification(frostRoundPreprocess, local.SenderPubKey, local.ParticipantIndex, localHash, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast preprocess verification: %v", err), frostRoundPreprocess, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordVerification(c.localPubKey, local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, frostRoundPreprocess, failure.blamePubKey)
		return nil, &resp
	}

	values := map[string]string{
		strconv.FormatUint(uint64(local.ParticipantIndex), 10): local.PreprocessHex,
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.msgChan:
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok || strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostPreprocessMsg:
				var incoming FrostPreprocessPayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid preprocess payload", frostRoundPreprocess)
					return nil, &resp
				}
				senderNorm := normalizePubKey(incoming.SenderPubKey)
				if senderNorm == "" {
					resp := c.failForMalformed(msg.PeerID, "empty sender pubkey in preprocess payload", frostRoundPreprocess)
					return nil, &resp
				}
				if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
					resp := c.failForPubKeys("preprocess sender is not in active participant set", frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					resp := c.failForPubKeys(err.Error(), frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				expectedIndex := participantIndexMap[senderNorm]
				if incoming.ParticipantIndex != expectedIndex {
					resp := c.failForPubKeys("participant index mismatch in preprocess payload", frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if strings.TrimSpace(incoming.PreprocessHex) == "" {
					resp := c.failForPubKeys("empty preprocess payload", frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				payloadHash, err := FrostTranscriptPreprocessHash(c.msgID, incoming)
				if err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to hash preprocess payload: %v", err), frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				isNewPayload, failure := verifier.recordPayload(incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash)
				if failure != nil {
					resp := c.failForPubKeys(failure.reason, frostRoundPreprocess, failure.blamePubKey)
					return nil, &resp
				}
				if !isNewPayload {
					continue
				}
				values[strconv.FormatUint(uint64(incoming.ParticipantIndex), 10)] = incoming.PreprocessHex
				if err := c.broadcastTranscriptVerification(frostRoundPreprocess, incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash, activePeerIDs); err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast preprocess verification: %v", err), frostRoundPreprocess, nil)
					return nil, &resp
				}
				if _, failure := verifier.recordVerification(c.localPubKey, incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash); failure != nil {
					resp := c.failForPubKeys(failure.reason, frostRoundPreprocess, failure.blamePubKey)
					return nil, &resp
				}
			case messages.TSSFrostVerifyMsg:
				var incoming FrostVerifyPayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid transcript verification payload", frostRoundPreprocess)
					return nil, &resp
				}
				if strings.TrimSpace(incoming.Round) != frostRoundPreprocess {
					continue
				}
				attesterNorm := normalizePubKey(incoming.SenderPubKey)
				if attesterNorm == "" {
					resp := c.failForMalformed(msg.PeerID, "empty sender pubkey in transcript verification payload", frostRoundPreprocess)
					return nil, &resp
				}
				if _, ok := activeSet.originalByNormalized[attesterNorm]; !ok {
					resp := c.failForPubKeys("verification attester is not in active participant set", frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					resp := c.failForPubKeys(err.Error(), frostRoundPreprocess, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if _, failure := verifier.recordVerification(
					incoming.SenderPubKey,
					incoming.TargetSenderPubKey,
					incoming.TargetParticipantIndex,
					incoming.PayloadHash,
				); failure != nil {
					resp := c.failForPubKeys(failure.reason, frostRoundPreprocess, failure.blamePubKey)
					return nil, &resp
				}
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			missingVerifiers := verifier.missingVerifierPubKeys()
			missing := mergePubKeyLists(missingPayloads, missingVerifiers)
			resp := c.failForPubKeys(
				transcriptTimeoutReason("preprocess payloads", missingPayloads, missingVerifiers),
				frostRoundPreprocess,
				missing,
			)
			return nil, &resp
		}
	}

	return values, nil
}

func (c *FrostKeyImageCoordinator) exchangeShares(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostSharePayload,
) (map[string]string, *FrostKeyImageResponse) {
	payload, err := json.Marshal(local)
	if err != nil {
		resp := FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to marshal share payload: %v", err), nil),
		}
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostShareMsg, payload, activePeerIDs); err != nil {
		resp := FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to broadcast share payload: %v", err), nil),
		}
		return nil, &resp
	}
	localHash, err := FrostTranscriptShareHash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local share payload: %v", err), frostRoundShare, []string{local.SenderPubKey})
		return nil, &resp
	}
	verifier, err := newFrostTranscriptVerifier(frostRoundShare, activeSet, participantIndexMap)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to initialize share transcript verifier: %v", err), frostRoundShare, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordPayload(local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, frostRoundShare, failure.blamePubKey)
		return nil, &resp
	}
	if err := c.broadcastTranscriptVerification(frostRoundShare, local.SenderPubKey, local.ParticipantIndex, localHash, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast share verification: %v", err), frostRoundShare, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordVerification(c.localPubKey, local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, frostRoundShare, failure.blamePubKey)
		return nil, &resp
	}

	values := map[string]string{
		strconv.FormatUint(uint64(local.ParticipantIndex), 10): local.ShareHex,
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.msgChan:
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok || strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostShareMsg:
				var incoming FrostSharePayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid share payload", frostRoundShare)
					return nil, &resp
				}
				senderNorm := normalizePubKey(incoming.SenderPubKey)
				if senderNorm == "" {
					resp := c.failForMalformed(msg.PeerID, "empty sender pubkey in share payload", frostRoundShare)
					return nil, &resp
				}
				if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
					resp := c.failForPubKeys("share sender is not in active participant set", frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					resp := c.failForPubKeys(err.Error(), frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				expectedIndex := participantIndexMap[senderNorm]
				if incoming.ParticipantIndex != expectedIndex {
					resp := c.failForPubKeys("participant index mismatch in share payload", frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if strings.TrimSpace(incoming.ShareHex) == "" {
					resp := c.failForPubKeys("empty share payload", frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				payloadHash, err := FrostTranscriptShareHash(c.msgID, incoming)
				if err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to hash share payload: %v", err), frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				isNewPayload, failure := verifier.recordPayload(incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash)
				if failure != nil {
					resp := c.failForPubKeys(failure.reason, frostRoundShare, failure.blamePubKey)
					return nil, &resp
				}
				if !isNewPayload {
					continue
				}
				values[strconv.FormatUint(uint64(incoming.ParticipantIndex), 10)] = incoming.ShareHex
				if err := c.broadcastTranscriptVerification(frostRoundShare, incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash, activePeerIDs); err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast share verification: %v", err), frostRoundShare, nil)
					return nil, &resp
				}
				if _, failure := verifier.recordVerification(c.localPubKey, incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash); failure != nil {
					resp := c.failForPubKeys(failure.reason, frostRoundShare, failure.blamePubKey)
					return nil, &resp
				}
			case messages.TSSFrostVerifyMsg:
				var incoming FrostVerifyPayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid transcript verification payload", frostRoundShare)
					return nil, &resp
				}
				if strings.TrimSpace(incoming.Round) != frostRoundShare {
					continue
				}
				attesterNorm := normalizePubKey(incoming.SenderPubKey)
				if attesterNorm == "" {
					resp := c.failForMalformed(msg.PeerID, "empty sender pubkey in transcript verification payload", frostRoundShare)
					return nil, &resp
				}
				if _, ok := activeSet.originalByNormalized[attesterNorm]; !ok {
					resp := c.failForPubKeys("verification attester is not in active participant set", frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					resp := c.failForPubKeys(err.Error(), frostRoundShare, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				if _, failure := verifier.recordVerification(
					incoming.SenderPubKey,
					incoming.TargetSenderPubKey,
					incoming.TargetParticipantIndex,
					incoming.PayloadHash,
				); failure != nil {
					resp := c.failForPubKeys(failure.reason, frostRoundShare, failure.blamePubKey)
					return nil, &resp
				}
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			missingVerifiers := verifier.missingVerifierPubKeys()
			missing := mergePubKeyLists(missingPayloads, missingVerifiers)
			resp := c.failForPubKeys(
				transcriptTimeoutReason("share payloads", missingPayloads, missingVerifiers),
				frostRoundShare,
				missing,
			)
			return nil, &resp
		}
	}

	return values, nil
}

func (c *FrostKeyImageCoordinator) broadcast(
	msgType messages.THORChainTSSMessageType,
	payload []byte,
	peers []peer.ID,
) error {
	c.p2pCommunication.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: msgType,
			MsgID:       c.msgID,
			Payload:     payload,
		},
		PeersID: peers,
	}
	return nil
}

func (c *FrostKeyImageCoordinator) broadcastTranscriptVerification(
	round string,
	targetSenderPubKey string,
	targetParticipantIndex uint32,
	payloadHash string,
	peers []peer.ID,
) error {
	payload := FrostVerifyPayload{
		SenderPubKey:           c.localPubKey,
		Round:                  round,
		TargetSenderPubKey:     targetSenderPubKey,
		TargetParticipantIndex: targetParticipantIndex,
		PayloadHash:            payloadHash,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal transcript verification payload: %w", err)
	}
	return c.broadcast(messages.TSSFrostVerifyMsg, buf, peers)
}

func (c *FrostKeyImageCoordinator) validateSenderPeerBinding(msgPeer peer.ID, senderPubKey string) error {
	expectedPeerID, err := conversion.GetPeerIDFromPubKey(senderPubKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender pubkey")
	}
	if expectedPeerID != msgPeer {
		return fmt.Errorf("sender peer binding mismatch")
	}
	return nil
}

func (c *FrostKeyImageCoordinator) failForMalformed(msgPeer peer.ID, reason, round string) FrostKeyImageResponse {
	pubKey, err := conversion.GetPubKeyFromPeerID(msgPeer.String())
	if err != nil {
		return FrostKeyImageResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame(reason, round, nil),
		}
	}
	return c.failForPubKeys(reason, round, []string{pubKey})
}

func (c *FrostKeyImageCoordinator) failForPubKeys(reason, round string, pubKeys []string) FrostKeyImageResponse {
	return FrostKeyImageResponse{
		Status: tcommon.Fail,
		Blame:  buildBlame(reason, round, pubKeys),
	}
}
