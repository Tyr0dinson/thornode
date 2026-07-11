package tss

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/common"
)

const (
	frostWireProtocolVersion     = messages.FrostProtocolVersion
	frostMsgIDDomain             = "thorchain-frost-keysign-msgid-v6"
	frostDecoyContextDomain      = "thorchain-frost-decoy-context-v1"
	frostPlanHashDomain          = "thorchain-frost-eventuality-plan-v2"
	frostEventualityIDDomain     = "thorchain-frost-eventuality-id-v2"
	frostEventualityVersion      = uint8(2)
	frostRoundPreprocess         = "frost_preprocess"
	frostRoundShare              = "frost_share"
	frostRoundResult             = "frost_result"
	frostEventualityHashHexBytes = 64
	frostMaxResultTxHexChars     = 2 * 1024 * 1024
	frostMaxResultSpentRefs      = 4096
	frostMaxResultBlameNodes     = 128
	frostMaxResultReasonBytes    = 512
	frostMaxResultEvidenceBytes  = 64 * 1024
	frostEquivocationFailReason  = "frost signed envelope equivocation"
)

var errFrostResultTimeout = errors.New("timeout waiting for frost result")

type FrostSignCoordinator struct {
	logger            zerolog.Logger
	p2pCommunication  *p2p.Communication
	sidecar           FrostSidecarClient
	localPubKey       string
	msgID             string
	vaultPubKey       string
	network           string
	expectedPlanHash  string
	msgChan           chan *p2p.Message
	timeout           time.Duration
	privateKey        tcrypto.PrivKey
	strictTranscript  bool
	evidenceActivated bool
}

func NewFrostSignCoordinator(
	comm *p2p.Communication,
	sidecar FrostSidecarClient,
	localPubKey string,
	msgID string,
	expectedPlanHash string,
	msgChan chan *p2p.Message,
	timeout time.Duration,
	privateKey tcrypto.PrivKey,
	evidenceActivated bool,
) *FrostSignCoordinator {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &FrostSignCoordinator{
		logger:            log.With().Str("module", "frost_keysign").Str("msg_id", msgID).Logger(),
		p2pCommunication:  comm,
		sidecar:           sidecar,
		localPubKey:       strings.TrimSpace(localPubKey),
		msgID:             strings.TrimSpace(msgID),
		expectedPlanHash:  strings.TrimSpace(expectedPlanHash),
		msgChan:           msgChan,
		timeout:           timeout,
		privateKey:        privateKey,
		strictTranscript:  privateKey != nil,
		evidenceActivated: evidenceActivated,
	}
}

func FrostKeySignMsgID(req FrostKeySignRequest) (string, error) {
	vault := strings.TrimSpace(req.VaultPubKey)
	toAddr := strings.TrimSpace(req.ToAddress)
	if vault == "" {
		return "", fmt.Errorf("vault pubkey is required")
	}
	if toAddr == "" {
		return "", fmt.Errorf("to address is required")
	}
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = common.ChainNetworkName(common.CurrentChainNetwork)
	}

	requiredInputsHash, err := frostRequiredInputsHash(req.RequiredInputs)
	if err != nil {
		return "", fmt.Errorf("fail to hash required inputs: %w", err)
	}

	canonical := strings.Join([]string{
		"domain=" + frostMsgIDDomain,
		"protocol_version=" + strconv.FormatUint(uint64(frostWireProtocolVersion), 10),
		"vault_pub_key=" + vault,
		"network=" + network,
		"to_address=" + toAddr,
		"amount_atomic=" + strconv.FormatUint(req.AmountAtomic, 10),
		"memo=" + req.Memo,
		"fee_rate=" + strconv.FormatUint(req.FeeRate, 10),
		"required_inputs_hash=" + requiredInputsHash,
		"max_inputs=" + strconv.FormatInt(req.MaxInputs, 10),
		"dust_threshold=" + strconv.FormatUint(req.DustThreshold, 10),
		"max_selected_input_value_atomic=" + strconv.FormatUint(req.MaxSelectedInputValueAtomic, 10),
		"max_gas_atomic=" + strconv.FormatUint(req.MaxGasAtomic, 10),
		"tx_in_hash=" + strings.ToLower(strings.TrimSpace(req.TxInHash)),
		"request_id=" + strings.TrimSpace(req.RequestID),
		"block_height=" + strconv.FormatInt(req.BlockHeight, 10),
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func FrostDecoyContext(msgID string) (string, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return "", fmt.Errorf("msgID is required")
	}
	return tcommon.MsgToHashString([]byte(frostDecoyContextDomain + "\n" + msgID))
}

func frostSignerSetHash(signerPubKeys []string) (string, error) {
	members, _, _, err := buildParticipantIndex(signerPubKeys)
	if err != nil {
		return "", err
	}
	return tcommon.MsgToHashString([]byte(strings.Join(members, ",")))
}

// frostActiveSignerPubKeys returns the filtered FROST signing party when one
// is supplied, otherwise it falls back to full vault membership.
func frostActiveSignerPubKeys(req FrostKeySignRequest) []string {
	if len(req.ActiveSignerPubKeys) > 0 {
		return req.ActiveSignerPubKeys
	}
	return req.SignerPubKeys
}

func frostRequiredInputsHash(inputs []FrostSpentOutputRef) (string, error) {
	normalized := make([]FrostSpentOutputRef, 0, len(inputs))
	for _, input := range inputs {
		txHash := strings.ToLower(strings.TrimSpace(input.TxHash))
		if txHash == "" {
			return "", fmt.Errorf("required input tx_hash is empty")
		}
		normalized = append(normalized, FrostSpentOutputRef{
			TxHash:      txHash,
			OutputIndex: input.OutputIndex,
			KeyImage:    strings.ToLower(strings.TrimSpace(input.KeyImage)),
			SpendTxHash: strings.ToLower(strings.TrimSpace(input.SpendTxHash)),
		})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].TxHash == normalized[j].TxHash {
			if normalized[i].OutputIndex == normalized[j].OutputIndex {
				if normalized[i].KeyImage == normalized[j].KeyImage {
					return normalized[i].SpendTxHash < normalized[j].SpendTxHash
				}
				return normalized[i].KeyImage < normalized[j].KeyImage
			}
			return normalized[i].OutputIndex < normalized[j].OutputIndex
		}
		return normalized[i].TxHash < normalized[j].TxHash
	})
	buf, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return tcommon.MsgToHashString(buf)
}

func FrostPlanHash(req FrostKeySignRequest, msgID string) (string, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return "", fmt.Errorf("msgID is required")
	}
	vault := strings.TrimSpace(req.VaultPubKey)
	toAddress := strings.TrimSpace(req.ToAddress)
	if vault == "" {
		return "", fmt.Errorf("vault pubkey is required")
	}
	if toAddress == "" {
		return "", fmt.Errorf("to address is required")
	}
	signerSetHash, err := frostSignerSetHash(req.SignerPubKeys)
	if err != nil {
		return "", fmt.Errorf("fail to hash signer membership: %w", err)
	}
	activeSignerSetHash, err := frostSignerSetHash(frostActiveSignerPubKeys(req))
	if err != nil {
		return "", fmt.Errorf("fail to hash active signer membership: %w", err)
	}
	requiredInputsHash, err := frostRequiredInputsHash(req.RequiredInputs)
	if err != nil {
		return "", fmt.Errorf("fail to hash required inputs: %w", err)
	}
	canonical := strings.Join([]string{
		"domain=" + frostPlanHashDomain,
		"protocol_version=" + strconv.FormatUint(uint64(frostWireProtocolVersion), 10),
		"msg_id=" + msgID,
		"vault_pub_key=" + vault,
		"to_address=" + toAddress,
		"amount_atomic=" + strconv.FormatUint(req.AmountAtomic, 10),
		"memo=" + req.Memo,
		"fee_rate=" + strconv.FormatUint(req.FeeRate, 10),
		"required_inputs_hash=" + requiredInputsHash,
		"max_inputs=" + strconv.FormatInt(req.MaxInputs, 10),
		"dust_threshold=" + strconv.FormatUint(req.DustThreshold, 10),
		"max_selected_input_value_atomic=" + strconv.FormatUint(req.MaxSelectedInputValueAtomic, 10),
		"max_gas_atomic=" + strconv.FormatUint(req.MaxGasAtomic, 10),
		"signer_set_hash=" + signerSetHash,
		"active_signer_set_hash=" + activeSignerSetHash,
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func FrostTxHexHash(txHex string) (string, error) {
	txHex = strings.TrimSpace(txHex)
	if txHex == "" {
		return "", fmt.Errorf("tx_hex is required")
	}
	raw, err := hex.DecodeString(txHex)
	if err != nil {
		return "", fmt.Errorf("tx_hex is not valid hex")
	}
	return tcommon.MsgToHashString(raw)
}

func FrostEventualityID(planHash, txHash, txHexHash, txKeyHash string, feeAtomic uint64) (string, error) {
	planHash = strings.ToLower(strings.TrimSpace(planHash))
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	txHexHash = strings.ToLower(strings.TrimSpace(txHexHash))
	txKeyHash = strings.ToLower(strings.TrimSpace(txKeyHash))
	if err := validateHashHex("plan_hash", planHash); err != nil {
		return "", err
	}
	if err := validateHashHex("tx_hash", txHash); err != nil {
		return "", err
	}
	if err := validateHashHex("tx_hex_hash", txHexHash); err != nil {
		return "", err
	}
	if err := validateHashHex("tx_key_hash", txKeyHash); err != nil {
		return "", err
	}
	canonical := strings.Join([]string{
		"domain=" + frostEventualityIDDomain,
		"plan_hash=" + planHash,
		"tx_hash=" + txHash,
		"tx_hex_hash=" + txHexHash,
		"tx_key_hash=" + txKeyHash,
		"fee_atomic=" + strconv.FormatUint(feeAtomic, 10),
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func BuildFrostEventuality(planHash, txHash, txHex, txKey string, feeAtomic uint64) (FrostEventuality, error) {
	txHexHash, err := FrostTxHexHash(txHex)
	if err != nil {
		return FrostEventuality{}, err
	}
	txKeyHash, err := FrostTxKeyHash(txKey)
	if err != nil {
		return FrostEventuality{}, err
	}
	eventualityID, err := FrostEventualityID(planHash, txHash, txHexHash, txKeyHash, feeAtomic)
	if err != nil {
		return FrostEventuality{}, err
	}
	return FrostEventuality{
		Version:       frostEventualityVersion,
		PlanHash:      strings.ToLower(strings.TrimSpace(planHash)),
		TxHexHash:     strings.ToLower(strings.TrimSpace(txHexHash)),
		TxKeyHash:     txKeyHash,
		EventualityID: strings.ToLower(strings.TrimSpace(eventualityID)),
	}, nil
}

func ValidateFrostEventuality(
	req FrostKeySignRequest,
	msgID string,
	txHash string,
	txHex string,
	txKey string,
	feeAtomic uint64,
	eventuality FrostEventuality,
) error {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return fmt.Errorf("msg_id is required")
	}
	expectedPlanHash, err := FrostPlanHash(req, msgID)
	if err != nil {
		return fmt.Errorf("fail to derive plan hash: %w", err)
	}
	if eventuality.Version != frostEventualityVersion {
		return fmt.Errorf("unexpected eventuality version %d", eventuality.Version)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.PlanHash), expectedPlanHash) {
		return fmt.Errorf("eventuality plan_hash mismatch")
	}
	expectedTxHexHash, err := FrostTxHexHash(txHex)
	if err != nil {
		return fmt.Errorf("fail to derive tx_hex_hash: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.TxHexHash), expectedTxHexHash) {
		return fmt.Errorf("eventuality tx_hex_hash mismatch")
	}
	expectedTxKeyHash, err := FrostTxKeyHash(txKey)
	if err != nil {
		return fmt.Errorf("fail to derive tx_key_hash: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.TxKeyHash), expectedTxKeyHash) {
		return fmt.Errorf("eventuality tx_key_hash mismatch")
	}
	expectedEventualityID, err := FrostEventualityID(expectedPlanHash, txHash, expectedTxHexHash, expectedTxKeyHash, feeAtomic)
	if err != nil {
		return fmt.Errorf("fail to derive eventuality_id: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.EventualityID), expectedEventualityID) {
		return fmt.Errorf("eventuality_id mismatch")
	}
	return nil
}

// FrostTxKeyHash validates the Rust-returned Monero transaction key as a
// canonical lowercase 32-byte hex value and commits to its raw bytes without
// exposing it inside the eventuality identifier.
func FrostTxKeyHash(txKey string) (string, error) {
	if txKey != strings.TrimSpace(txKey) || txKey != strings.ToLower(txKey) || len(txKey) != 64 {
		return "", fmt.Errorf("tx_key must be canonical lowercase 32-byte hex")
	}
	raw, err := hex.DecodeString(txKey)
	if err != nil {
		return "", fmt.Errorf("tx_key must be canonical lowercase 32-byte hex")
	}
	allZero := true
	for _, b := range raw {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "", fmt.Errorf("tx_key must not be zero")
	}
	return tcommon.MsgToHashString(raw)
}

func validateHashHex(label, value string) error {
	if len(value) != frostEventualityHashHexBytes {
		return fmt.Errorf("%s must be %d hex chars", label, frostEventualityHashHexBytes)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s is not valid hex", label)
	}
	return nil
}

func (t *TssServer) FrostKeySign(req FrostKeySignRequest, sidecar FrostSidecarClient) (FrostKeySignResponse, error) {
	if sidecar == nil {
		return FrostKeySignResponse{}, fmt.Errorf("frost sidecar client is nil")
	}
	if len(req.SignerPubKeys) == 0 {
		return FrostKeySignResponse{}, fmt.Errorf("signer pub keys are empty")
	}
	expectedNetwork := common.ChainNetworkName(common.CurrentChainNetwork)
	req.Network = strings.ToLower(strings.TrimSpace(req.Network))
	if req.Network == "" {
		req.Network = expectedNetwork
	}
	if req.Network != expectedNetwork {
		return FrostKeySignResponse{}, fmt.Errorf("frost deployment network %q does not match current network %q", req.Network, expectedNetwork)
	}
	activeSignerPubKeys := frostActiveSignerPubKeys(req)
	if len(activeSignerPubKeys) == 0 {
		return FrostKeySignResponse{}, fmt.Errorf("active signer pub keys are empty")
	}
	msgID, err := FrostKeySignMsgID(req)
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to compute frost msgID: %w", err)
	}
	planHash, err := FrostPlanHash(req, msgID)
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to compute frost plan hash: %w", err)
	}
	threshold, err := conversion.GetThreshold(len(req.SignerPubKeys))
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to compute frost threshold: %w", err)
	}
	if len(activeSignerPubKeys) <= threshold {
		return FrostKeySignResponse{}, fmt.Errorf(
			"not enough eligible frost signers: need %d of %d, got %d",
			threshold+1,
			len(req.SignerPubKeys),
			len(activeSignerPubKeys),
		)
	}

	allowedPeerIDs, err := conversion.GetPeerIDsFromPubKeys(activeSignerPubKeys)
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to resolve FROST session peers: %w", err)
	}
	msgChan := make(chan *p2p.Message, p2p.FrostSessionQueueSize)
	sigChan := make(chan string, 2)

	t.p2pCommunication.SetSubscribeWithPeers(messages.TSSFrostPreprocessMsg, msgID, msgChan, allowedPeerIDs, p2p.FrostMaxSessionPayload, p2p.FrostMaxMessagesPerPeer)
	t.p2pCommunication.SetSubscribeWithPeers(messages.TSSFrostShareMsg, msgID, msgChan, allowedPeerIDs, p2p.FrostMaxSessionPayload, p2p.FrostMaxMessagesPerPeer)
	t.p2pCommunication.SetSubscribeWithPeers(messages.TSSFrostVerifyMsg, msgID, msgChan, allowedPeerIDs, p2p.FrostMaxSessionPayload, p2p.FrostMaxMessagesPerPeer)
	t.p2pCommunication.SetSubscribeWithPeers(messages.TSSFrostResultMsg, msgID, msgChan, allowedPeerIDs, p2p.FrostMaxSessionPayload, p2p.FrostMaxMessagesPerPeer)
	defer func() {
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostPreprocessMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostShareMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostVerifyMsg, msgID)
		t.p2pCommunication.CancelSubscribe(messages.TSSFrostResultMsg, msgID)
		t.p2pCommunication.ReleaseStream(msgID)
		t.partyCoordinator.ReleaseStream(msgID)
		close(sigChan)
	}()

	coordinator := NewFrostSignCoordinator(
		t.p2pCommunication,
		sidecar,
		t.localNodePubKey,
		msgID,
		planHash,
		msgChan,
		t.conf.KeySignTimeout,
		t.privateKey,
		frostEvidenceBlameActivated(req.Version),
	)

	// Nodes outside the filtered active set must not join the FROST ceremony,
	// but they can still import a quorum-verified result broadcast by signers.
	if !containsNormalizedPubKey(activeSignerPubKeys, t.localNodePubKey) {
		t.logger.Info().Str("msg_id", msgID).Msg("not eligible FROST signer, waiting for result")
		quorum := threshold + 1
		waitResp, waitErr := coordinator.WaitForResult(activeSignerPubKeys, quorum, t.conf.KeySignTimeout+5*time.Second)
		if waitErr != nil {
			if !errors.Is(waitErr, errFrostResultTimeout) {
				return FrostKeySignResponse{}, fmt.Errorf("fail to wait for FROST result as non-eligible signer: %w", waitErr)
			}
			t.logger.Info().Err(waitErr).Str("msg_id", msgID).Msg("timed out waiting for FROST result as non-eligible signer; standing down")
			return FrostKeySignResponse{
				Status: tcommon.NA,
				MsgID:  msgID,
			}, nil
		}
		if waitResp.Status == tcommon.Success {
			t.persistPeerAddressBook()
		}
		return waitResp, nil
	}

	onlinePeers, _, errJoinParty := t.joinParty(
		msgID,
		req.Version,
		req.BlockHeight,
		activeSignerPubKeys,
		threshold,
		sigChan,
	)
	if errJoinParty != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(errJoinParty.Error(), nil),
		}, nil
	}

	activeParticipants, err := conversion.GetPubKeysFromPeerIDs(peerIDsToStrings(onlinePeers))
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("fail to resolve active participants", nil),
		}, nil
	}
	// The pre-join subscription is restricted to the eligible signer set. Once
	// the authenticated join response forms a smaller party, narrow all round
	// traffic to that exact set. Result gossip stays on the eligible set so
	// stand-by members can adopt a quorum result.
	t.p2pCommunication.UpdateSubscribePeers(messages.TSSFrostPreprocessMsg, msgID, onlinePeers)
	t.p2pCommunication.UpdateSubscribePeers(messages.TSSFrostShareMsg, msgID, onlinePeers)
	t.p2pCommunication.UpdateSubscribePeers(messages.TSSFrostVerifyMsg, msgID, onlinePeers)
	allPeerIDs, peerErr := conversion.GetPeerIDsFromPubKeys(req.SignerPubKeys)
	if peerErr != nil {
		t.logger.Warn().Err(peerErr).Msg("fail to resolve all peer IDs for frost result broadcast, fallback to active peers")
		allPeerIDs = onlinePeers
	}

	isActive := false
	ourPeerID := t.p2pCommunication.GetLocalPeerID()
	for _, p := range onlinePeers {
		if p.String() == ourPeerID {
			isActive = true
			break
		}
	}

	if !isActive {
		t.logger.Info().Str("msg_id", msgID).Msg("not active FROST signer, waiting for result")
		quorum := threshold + 1
		waitResp, waitErr := coordinator.WaitForResult(activeParticipants, quorum, t.conf.KeySignTimeout+5*time.Second)
		if waitErr != nil {
			return FrostKeySignResponse{
				Status: tcommon.Fail,
				Blame:  blame.NewBlame(waitErr.Error(), nil),
			}, nil
		}
		if waitResp.Status == tcommon.Success {
			t.persistPeerAddressBook()
		}
		return waitResp, nil
	}

	resp := coordinator.Execute(req, activeParticipants, onlinePeers)
	if resp.Status == tcommon.Success {
		t.persistPeerAddressBook()
		if err := coordinator.BroadcastResult(resp, allPeerIDs); err != nil {
			t.logger.Warn().Err(err).Msg("fail to broadcast frost result")
		}
		return resp, nil
	}

	quorum := threshold + 1
	waitResp, waitErr := coordinator.WaitForResult(activeParticipants, quorum, t.conf.KeySignTimeout+5*time.Second)
	if waitErr == nil && waitResp.Status == tcommon.Success {
		t.persistPeerAddressBook()
		return waitResp, nil
	}

	if err := coordinator.BroadcastResult(resp, allPeerIDs); err != nil {
		t.logger.Warn().Err(err).Msg("fail to broadcast local frost failure result")
	}
	if waitErr == nil && waitResp.Status == tcommon.Fail && len(waitResp.Blame.BlameNodes) > 0 && len(resp.Blame.BlameNodes) == 0 {
		return waitResp, nil
	}
	return resp, nil
}

func (c *FrostSignCoordinator) Execute(
	req FrostKeySignRequest,
	activeParticipants []string,
	activePeerIDs []peer.ID,
) FrostKeySignResponse {
	if c.sidecar == nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost sidecar client is nil", nil),
		}
	}
	if c.msgID == "" {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost msgID is empty", nil),
		}
	}
	c.vaultPubKey = strings.TrimSpace(req.VaultPubKey)
	if c.vaultPubKey == "" {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost vault pubkey is empty", nil),
		}
	}
	c.network = strings.ToLower(strings.TrimSpace(req.Network))
	if c.network == "" {
		c.network = common.ChainNetworkName(common.CurrentChainNetwork)
	}
	_, participantIndexMap, originalByNormalized, err := buildParticipantIndex(req.SignerPubKeys)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}
	activeSet, err := buildActiveSet(activeParticipants, originalByNormalized)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}
	localNorm := normalizePubKey(c.localPubKey)
	localIndex, ok := participantIndexMap[localNorm]
	if !ok {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in participant set", nil),
		}
	}
	if _, ok := activeSet.originalByNormalized[localNorm]; !ok {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in active signer set", nil),
		}
	}
	decoyContext, err := FrostDecoyContext(c.msgID)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to derive decoy context: %v", err), nil),
		}
	}

	prepareResp, err := c.sidecar.FrostSignPrepare(FrostSidecarPrepareReq{
		MsgID:                       c.msgID,
		VaultPubKey:                 req.VaultPubKey,
		ToAddress:                   req.ToAddress,
		AmountAtomic:                req.AmountAtomic,
		Memo:                        req.Memo,
		FeeRate:                     req.FeeRate,
		DecoyContext:                decoyContext,
		RequiredInputs:              req.RequiredInputs,
		MaxInputs:                   req.MaxInputs,
		DustThreshold:               req.DustThreshold,
		MaxSelectedInputValueAtomic: req.MaxSelectedInputValueAtomic,
		MaxGasAtomic:                req.MaxGasAtomic,
	})
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost prepare failed: %v", err), nil),
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

	partialResp, err := c.sidecar.FrostSignPartial(FrostSidecarPartialReq{
		MsgID:           c.msgID,
		SessionID:       prepareResp.SessionID,
		AllPreprocesses: preprocesses,
	})
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost partial failed: %v", err), nil),
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

	combineResp, err := c.sidecar.FrostSignCombine(FrostSidecarCombineReq{
		MsgID:     c.msgID,
		SessionID: prepareResp.SessionID,
		AllShares: shares,
	})
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost combine failed: %v", err), nil),
		}
	}
	// The sidecar enforces MaxGas during transaction construction, but keep the
	// same cap check here so a buggy or mismatched signer cannot return an
	// over-cap transaction that Bifrost would later observe.
	if req.MaxGasAtomic > 0 && combineResp.FeeAtomic > req.MaxGasAtomic {
		return FrostKeySignResponse{
			FeeAtomic: combineResp.FeeAtomic,
			Status:    tcommon.Fail,
			Blame: blame.NewBlame(
				fmt.Sprintf("%s actual_fee_atomic=%d max_gas_atomic=%d", FrostFailReasonXMRMaxGasExceeded, combineResp.FeeAtomic, req.MaxGasAtomic),
				nil,
			),
		}
	}
	txKey := combineResp.TxKey
	if _, err := FrostTxKeyHash(txKey); err != nil {
		return FrostKeySignResponse{Status: tcommon.Fail, Blame: blame.NewBlame(fmt.Sprintf("invalid frost combine tx_key: %v", err), nil)}
	}
	eventuality, err := BuildFrostEventuality(c.expectedPlanHash, combineResp.TxHash, combineResp.TxHex, txKey, combineResp.FeeAtomic)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("failed to derive eventuality: %v", err), nil),
		}
	}

	return FrostKeySignResponse{
		TxHash:          combineResp.TxHash,
		TxHex:           combineResp.TxHex,
		TxKey:           txKey,
		FeeAtomic:       combineResp.FeeAtomic,
		MsgID:           c.msgID,
		Eventuality:     eventuality,
		SpentOutputRefs: combineResp.SpentOutputRefs,
		Status:          tcommon.Success,
		Blame:           blame.Blame{},
	}
}

func (c *FrostSignCoordinator) exchangePreprocess(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostPreprocessPayload,
) (map[string]string, *FrostKeySignResponse) {
	localHash, err := FrostTranscriptPreprocessHash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local preprocess payload: %v", err), frostRoundPreprocess, nil)
		return nil, &resp
	}
	localEnvelope := frostEnvelopeFromPreprocess(c.msgID, c.vaultPubKey, c.network, local, localHash)
	if c.strictTranscript {
		localEnvelope, err = newFrostSignedEnvelope(c.privateKey, c.msgID, c.vaultPubKey, c.network, frostRoundPreprocess, local.SenderPubKey, local.ParticipantIndex, localHash)
		if err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundPreprocess, nil)
			return nil, &resp
		}
		local.ProtocolVersion = localEnvelope.ProtocolVersion
		local.Signature = localEnvelope.Signature
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to marshal preprocess payload: %v", err), nil),
		}
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostPreprocessMsg, payload, activePeerIDs); err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to broadcast preprocess payload: %v", err), nil),
		}
		return nil, &resp
	}
	verifier, err := newFrostTranscriptVerifier(frostRoundPreprocess, activeSet, participantIndexMap, c.vaultPubKey, c.network)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to initialize preprocess transcript verifier: %v", err), frostRoundPreprocess, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordPayload(local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, frostRoundPreprocess, failure.blamePubKey)
		return nil, &resp
	}
	if c.strictTranscript {
		if _, err := verifier.recordSignedEnvelope(localEnvelope); err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundPreprocess, nil)
			return nil, &resp
		}
	}
	if err := c.broadcastSignedTranscriptVerification(localEnvelope, activePeerIDs); err != nil {
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
			if !ok {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostPreprocessMsg:
				var incoming FrostPreprocessPayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring malformed preprocess payload")
					continue
				}
				senderNorm := normalizePubKey(incoming.SenderPubKey)
				if senderNorm == "" {
					continue
				}
				if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
					continue
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring spoofed preprocess sender")
					continue
				}
				expectedIndex := participantIndexMap[senderNorm]
				if incoming.ParticipantIndex != expectedIndex {
					continue
				}
				if strings.TrimSpace(incoming.PreprocessHex) == "" {
					continue
				}
				payloadHash, err := FrostTranscriptPreprocessHash(c.msgID, incoming)
				if err != nil {
					continue
				}
				incomingEnvelope := frostEnvelopeFromPreprocess(c.msgID, c.vaultPubKey, c.network, incoming, payloadHash)
				if c.strictTranscript {
					if err := verifyFrostSignedEnvelope(incomingEnvelope); err != nil {
						c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring unsigned preprocess payload")
						continue
					}
					if evidence, err := verifier.recordSignedEnvelope(incomingEnvelope); err != nil {
						continue
					} else if evidence != nil {
						resp := c.failForEquivocation(*evidence)
						return nil, &resp
					}
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
				if err := c.broadcastSignedTranscriptVerification(incomingEnvelope, activePeerIDs); err != nil {
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
					continue
				}
				if strings.TrimSpace(incoming.Round) != frostRoundPreprocess {
					continue
				}
				attesterNorm := normalizePubKey(incoming.SenderPubKey)
				if attesterNorm == "" {
					continue
				}
				if _, ok := activeSet.originalByNormalized[attesterNorm]; !ok {
					continue
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					continue
				}
				if c.strictTranscript {
					echo := frostEchoFromVerify(c.msgID, c.vaultPubKey, c.network, incoming)
					if err := verifyFrostSignedEcho(echo); err != nil {
						c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring invalid signed preprocess echo")
						continue
					}
					if evidence, err := verifier.recordSignedEnvelope(echo.Target); err != nil {
						continue
					} else if evidence != nil {
						resp := c.failForEquivocation(*evidence)
						return nil, &resp
					}
				}
				if _, failure := verifier.recordVerification(
					incoming.SenderPubKey,
					incoming.TargetSenderPubKey,
					incoming.TargetParticipantIndex,
					incoming.PayloadHash,
				); failure != nil {
					c.logger.Warn().Err(failure).Str("peer", msg.PeerID.String()).Msg("ignoring invalid preprocess verification")
					continue
				}
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			missingVerifiers := verifier.missingVerifierPubKeys()
			resp := c.failForPubKeys(
				transcriptTimeoutReason("preprocess payloads", missingPayloads, missingVerifiers),
				frostRoundPreprocess,
				nil,
			)
			return nil, &resp
		}
	}

	return values, nil
}

func (c *FrostSignCoordinator) exchangeShares(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostSharePayload,
) (map[string]string, *FrostKeySignResponse) {
	localHash, err := FrostTranscriptShareHash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local share payload: %v", err), frostRoundShare, nil)
		return nil, &resp
	}
	localEnvelope := frostEnvelopeFromShare(c.msgID, c.vaultPubKey, c.network, local, localHash)
	if c.strictTranscript {
		localEnvelope, err = newFrostSignedEnvelope(c.privateKey, c.msgID, c.vaultPubKey, c.network, frostRoundShare, local.SenderPubKey, local.ParticipantIndex, localHash)
		if err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundShare, nil)
			return nil, &resp
		}
		local.ProtocolVersion = localEnvelope.ProtocolVersion
		local.Signature = localEnvelope.Signature
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to marshal share payload: %v", err), nil),
		}
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostShareMsg, payload, activePeerIDs); err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to broadcast share payload: %v", err), nil),
		}
		return nil, &resp
	}
	verifier, err := newFrostTranscriptVerifier(frostRoundShare, activeSet, participantIndexMap, c.vaultPubKey, c.network)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to initialize share transcript verifier: %v", err), frostRoundShare, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordPayload(local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, frostRoundShare, failure.blamePubKey)
		return nil, &resp
	}
	if c.strictTranscript {
		if _, err := verifier.recordSignedEnvelope(localEnvelope); err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundShare, nil)
			return nil, &resp
		}
	}
	if err := c.broadcastSignedTranscriptVerification(localEnvelope, activePeerIDs); err != nil {
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
			if !ok {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostShareMsg:
				var incoming FrostSharePayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					continue
				}
				senderNorm := normalizePubKey(incoming.SenderPubKey)
				if senderNorm == "" {
					continue
				}
				if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
					continue
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					continue
				}
				expectedIndex := participantIndexMap[senderNorm]
				if incoming.ParticipantIndex != expectedIndex {
					continue
				}
				if strings.TrimSpace(incoming.ShareHex) == "" {
					continue
				}
				payloadHash, err := FrostTranscriptShareHash(c.msgID, incoming)
				if err != nil {
					continue
				}
				incomingEnvelope := frostEnvelopeFromShare(c.msgID, c.vaultPubKey, c.network, incoming, payloadHash)
				if c.strictTranscript {
					if err := verifyFrostSignedEnvelope(incomingEnvelope); err != nil {
						c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring unsigned share payload")
						continue
					}
					if evidence, err := verifier.recordSignedEnvelope(incomingEnvelope); err != nil {
						continue
					} else if evidence != nil {
						resp := c.failForEquivocation(*evidence)
						return nil, &resp
					}
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
				if err := c.broadcastSignedTranscriptVerification(incomingEnvelope, activePeerIDs); err != nil {
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
					continue
				}
				if strings.TrimSpace(incoming.Round) != frostRoundShare {
					continue
				}
				attesterNorm := normalizePubKey(incoming.SenderPubKey)
				if attesterNorm == "" {
					continue
				}
				if _, ok := activeSet.originalByNormalized[attesterNorm]; !ok {
					continue
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					continue
				}
				if c.strictTranscript {
					echo := frostEchoFromVerify(c.msgID, c.vaultPubKey, c.network, incoming)
					if err := verifyFrostSignedEcho(echo); err != nil {
						c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring invalid signed share echo")
						continue
					}
					if evidence, err := verifier.recordSignedEnvelope(echo.Target); err != nil {
						continue
					} else if evidence != nil {
						resp := c.failForEquivocation(*evidence)
						return nil, &resp
					}
				}
				if _, failure := verifier.recordVerification(
					incoming.SenderPubKey,
					incoming.TargetSenderPubKey,
					incoming.TargetParticipantIndex,
					incoming.PayloadHash,
				); failure != nil {
					c.logger.Warn().Err(failure).Str("peer", msg.PeerID.String()).Msg("ignoring invalid share verification")
					continue
				}
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			missingVerifiers := verifier.missingVerifierPubKeys()
			resp := c.failForPubKeys(
				transcriptTimeoutReason("share payloads", missingPayloads, missingVerifiers),
				frostRoundShare,
				nil,
			)
			return nil, &resp
		}
	}

	return values, nil
}

func (c *FrostSignCoordinator) WaitForResult(activeParticipants []string, quorum int, timeout time.Duration) (FrostKeySignResponse, error) {
	_, _, activeByNormalized, err := buildParticipantIndex(activeParticipants)
	if err != nil {
		return FrostKeySignResponse{}, err
	}
	if quorum <= 0 {
		threshold, thresholdErr := conversion.GetThreshold(len(activeByNormalized))
		if thresholdErr != nil {
			return FrostKeySignResponse{}, thresholdErr
		}
		quorum = threshold + 1
	}
	if quorum <= 0 {
		return FrostKeySignResponse{}, fmt.Errorf("invalid quorum %d", quorum)
	}
	if quorum > len(activeByNormalized) {
		return FrostKeySignResponse{}, fmt.Errorf(
			"invalid quorum %d: only %d active participants",
			quorum,
			len(activeByNormalized),
		)
	}
	if timeout <= 0 {
		timeout = c.timeout
	}

	buckets := make(map[string]*resultVoteBucket, len(activeByNormalized))
	malformedBuckets := make(map[string]*malformedResultVoteBucket, len(activeByNormalized))
	senderVotes := make(map[string]senderResultVote, len(activeByNormalized))
	equivocated := make(map[string]struct{}, len(activeByNormalized))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg := <-c.msgChan:
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok || wrapped.MessageType != messages.TSSFrostResultMsg {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			var payload FrostResultPayload
			if err := json.Unmarshal(wrapped.Payload, &payload); err != nil {
				c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("skipping malformed frost result payload")
				continue
			}
			senderNorm := normalizePubKey(payload.SenderPubKey)
			if senderNorm == "" {
				c.logger.Warn().Str("peer", msg.PeerID.String()).Msg("skipping frost result with empty sender pubkey")
				continue
			}
			if _, ok := activeByNormalized[senderNorm]; !ok {
				c.logger.Warn().Str("sender", payload.SenderPubKey).Msg("skipping frost result from non-active signer")
				continue
			}
			if err := c.validateSenderPeerBinding(msg.PeerID, payload.SenderPubKey); err != nil {
				c.logger.Warn().Err(err).Str("sender", payload.SenderPubKey).Msg("skipping frost result with peer binding mismatch")
				continue
			}

			normalized, matchKey, err := normalizeResultPayload(payload.Response, c.msgID, c.expectedPlanHash)
			if err != nil {
				if _, disqualified := equivocated[senderNorm]; disqualified {
					continue
				}
				if _, alreadyVoted := senderVotes[senderNorm]; alreadyVoted {
					// Retain at most one malformed variant per authenticated
					// sender. A later valid vote may replace it below.
					continue
				}
				reason := truncateFrostResultReason(err.Error())
				c.logger.Warn().Err(err).Str("sender", payload.SenderPubKey).Msg("skipping malformed frost result")
				bucket := malformedBuckets[reason]
				if bucket == nil {
					if len(malformedBuckets) >= len(activeByNormalized) {
						continue
					}
					bucket = &malformedResultVoteBucket{
						reason: reason,
						voters: make(map[string]string),
					}
					malformedBuckets[reason] = bucket
				}
				bucket.voters[senderNorm] = activeByNormalized[senderNorm]
				senderVotes[senderNorm] = senderResultVote{key: reason, malformed: true}
				if len(bucket.voters) >= quorum {
					resp := c.failForPubKeys(reason, frostRoundResult, bucket.pubKeys())
					return resp, nil
				}
				continue
			}
			if _, disqualified := equivocated[senderNorm]; disqualified {
				continue
			}
			if existing, alreadyVoted := senderVotes[senderNorm]; alreadyVoted {
				if existing.malformed {
					if malformedBucket := malformedBuckets[existing.key]; malformedBucket != nil {
						delete(malformedBucket.voters, senderNorm)
						if len(malformedBucket.voters) == 0 {
							delete(malformedBuckets, existing.key)
						}
					}
					delete(senderVotes, senderNorm)
				} else if existing.key == matchKey {
					continue
				} else {
					// Two different well-formed results from one authenticated
					// sender are equivocation. Remove its first vote and retain
					// only the bounded disqualification marker.
					if previousBucket := buckets[existing.key]; previousBucket != nil {
						delete(previousBucket.voters, senderNorm)
						if len(previousBucket.voters) == 0 {
							delete(buckets, existing.key)
						}
					}
					delete(senderVotes, senderNorm)
					equivocated[senderNorm] = struct{}{}
					c.logger.Warn().Str("sender", payload.SenderPubKey).Msg("disqualifying frost result sender after conflicting valid votes")
					continue
				}
			}

			bucket := buckets[matchKey]
			if bucket == nil {
				if len(buckets) >= len(activeByNormalized) {
					continue
				}
				bucket = &resultVoteBucket{
					response: normalized,
					voters:   make(map[string]struct{}),
				}
				buckets[matchKey] = bucket
			}
			bucket.voters[senderNorm] = struct{}{}
			senderVotes[senderNorm] = senderResultVote{key: matchKey}
			if len(bucket.voters) >= quorum {
				return bucket.response, nil
			}
		case <-timer.C:
			return FrostKeySignResponse{}, errFrostResultTimeout
		}
	}
}

func (c *FrostSignCoordinator) BroadcastResult(resp FrostKeySignResponse, peers []peer.ID) error {
	result := FrostResultPayload{
		SenderPubKey: c.localPubKey,
		Response: FrostResult{
			MsgID:           resp.MsgID,
			TxHash:          resp.TxHash,
			TxHex:           resp.TxHex,
			TxKey:           resp.TxKey,
			FeeAtomic:       resp.FeeAtomic,
			Eventuality:     resp.Eventuality,
			SpentOutputRefs: resp.SpentOutputRefs,
			Status:          uint8(resp.Status),
			Blame:           resp.Blame,
		},
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("fail to marshal frost result payload: %w", err)
	}
	return c.broadcast(messages.TSSFrostResultMsg, payload, peers)
}

func (c *FrostSignCoordinator) broadcast(msgType messages.THORChainTSSMessageType, payload []byte, peers []peer.ID) error {
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

func (c *FrostSignCoordinator) broadcastTranscriptVerification(
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

func (c *FrostSignCoordinator) broadcastSignedTranscriptVerification(
	target messages.FrostSignedEnvelope,
	peers []peer.ID,
) error {
	if !c.strictTranscript {
		return c.broadcastTranscriptVerification(
			target.Round,
			target.SenderPubKey,
			target.ParticipantIndex,
			target.PayloadHash,
			peers,
		)
	}
	echo, err := newFrostSignedEcho(c.privateKey, c.localPubKey, target)
	if err != nil {
		return err
	}
	payload := FrostVerifyPayload{
		ProtocolVersion:        echo.ProtocolVersion,
		SenderPubKey:           echo.AttesterPubKey,
		Round:                  echo.Target.Round,
		TargetSenderPubKey:     echo.Target.SenderPubKey,
		TargetParticipantIndex: echo.Target.ParticipantIndex,
		PayloadHash:            echo.Target.PayloadHash,
		TargetSignature:        echo.Target.Signature,
		AttesterSignature:      echo.Signature,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal signed transcript verification payload: %w", err)
	}
	return c.broadcast(messages.TSSFrostVerifyMsg, buf, peers)
}

func (c *FrostSignCoordinator) validateSenderPeerBinding(msgPeer peer.ID, senderPubKey string) error {
	expectedPeerID, err := conversion.GetPeerIDFromPubKey(senderPubKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender pubkey")
	}
	if expectedPeerID != msgPeer {
		return fmt.Errorf("sender peer binding mismatch")
	}
	return nil
}

func (c *FrostSignCoordinator) failForMalformed(msgPeer peer.ID, reason, round string) FrostKeySignResponse {
	pubKey, err := conversion.GetPubKeyFromPeerID(msgPeer.String())
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame(reason, round, nil),
		}
	}
	return c.failForPubKeys(reason, round, []string{pubKey})
}

func (c *FrostSignCoordinator) failForPubKeys(reason, round string, pubKeys []string) FrostKeySignResponse {
	return FrostKeySignResponse{
		Status: tcommon.Fail,
		Blame:  buildBlame(reason, round, pubKeys),
	}
}

func (c *FrostSignCoordinator) failForEquivocation(evidence messages.FrostEquivocationEvidence) FrostKeySignResponse {
	if !c.evidenceActivated {
		// The v2 round identifiers are consensus-gated at 3.20. Before that
		// boundary, preserve the local diagnostic but emit no actionable blame.
		return c.failForPubKeys(frostEquivocationFailReason, "", nil)
	}
	buf, err := messages.MarshalFrostEquivocationEvidence(evidence)
	if err != nil {
		return c.failForPubKeys(fmt.Sprintf("failed to encode FROST equivocation evidence: %v", err), "", nil)
	}
	round := ""
	switch evidence.First.Round {
	case frostRoundPreprocess:
		round = messages.FrostBlameRoundPreprocessEquivocation
	case frostRoundShare:
		round = messages.FrostBlameRoundShareEquivocation
	default:
		return c.failForPubKeys("unsupported FROST equivocation evidence round", "", nil)
	}
	b := blame.NewBlame(frostEquivocationFailReason, []blame.Node{{
		Pubkey:    evidence.First.SenderPubKey,
		BlameData: buf,
	}})
	b.Round = round
	return FrostKeySignResponse{Status: tcommon.Fail, Blame: b}
}

func frostEvidenceBlameActivated(version string) bool {
	old, err := conversion.VersionLTCheck(strings.TrimSpace(version), "3.20.0")
	return err == nil && !old
}

func parseWrappedMessage(raw []byte) (messages.WrappedMessage, bool) {
	var wrapped messages.WrappedMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return messages.WrappedMessage{}, false
	}
	return wrapped, true
}

type resultVoteBucket struct {
	response FrostKeySignResponse
	voters   map[string]struct{}
}

type senderResultVote struct {
	key       string
	malformed bool
}

type malformedResultVoteBucket struct {
	reason string
	voters map[string]string
}

func (b *malformedResultVoteBucket) pubKeys() []string {
	pubKeys := make([]string, 0, len(b.voters))
	for _, pubKey := range b.voters {
		pubKeys = append(pubKeys, pubKey)
	}
	sort.Strings(pubKeys)
	return pubKeys
}

func truncateFrostResultReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) <= frostMaxResultReasonBytes {
		return reason
	}
	return reason[:frostMaxResultReasonBytes]
}

func normalizeResultPayload(result FrostResult, expectedMsgID, expectedPlanHash string) (FrostKeySignResponse, string, error) {
	expectedMsgID = strings.TrimSpace(expectedMsgID)
	expectedPlanHash = strings.ToLower(strings.TrimSpace(expectedPlanHash))
	status := tcommon.Status(result.Status)
	if status != tcommon.Success && status != tcommon.Fail {
		return FrostKeySignResponse{}, "", fmt.Errorf("invalid frost result status")
	}

	normalized := FrostKeySignResponse{
		MsgID:     expectedMsgID,
		Status:    status,
		FeeAtomic: result.FeeAtomic,
	}

	switch status {
	case tcommon.Success:
		if len(result.TxHex) > frostMaxResultTxHexChars {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_hex exceeds limit")
		}
		if len(result.SpentOutputRefs) > frostMaxResultSpentRefs {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result spent refs exceeds limit")
		}
		payloadMsgID := strings.TrimSpace(result.MsgID)
		if payloadMsgID == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has empty msg_id")
		}
		if expectedMsgID != "" && payloadMsgID != expectedMsgID {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result msg_id mismatch")
		}
		txHash := strings.ToLower(strings.TrimSpace(result.TxHash))
		txHex := strings.ToLower(strings.TrimSpace(result.TxHex))
		txKey := result.TxKey
		if txHash == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has empty tx_hash")
		}
		if txHex == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has empty tx_hex")
		}
		expectedTxKeyHash, err := FrostTxKeyHash(txKey)
		if err != nil {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has invalid tx_key")
		}
		if _, err := hex.DecodeString(txHex); err != nil {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_hex is not valid hex")
		}
		if err := validateHashHex("tx_hash", txHash); err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if expectedPlanHash == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("expected plan hash is empty")
		}
		eventuality := FrostEventuality{
			Version:       result.Eventuality.Version,
			PlanHash:      strings.ToLower(strings.TrimSpace(result.Eventuality.PlanHash)),
			TxHexHash:     strings.ToLower(strings.TrimSpace(result.Eventuality.TxHexHash)),
			TxKeyHash:     strings.ToLower(strings.TrimSpace(result.Eventuality.TxKeyHash)),
			EventualityID: strings.ToLower(strings.TrimSpace(result.Eventuality.EventualityID)),
		}
		if eventuality.Version != frostEventualityVersion {
			return FrostKeySignResponse{}, "", fmt.Errorf("invalid frost eventuality version")
		}
		if eventuality.PlanHash != expectedPlanHash {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result plan_hash mismatch")
		}
		if err := validateHashHex("tx_hex_hash", eventuality.TxHexHash); err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if eventuality.TxKeyHash != expectedTxKeyHash {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_key_hash mismatch")
		}
		if err := validateHashHex("eventuality_id", eventuality.EventualityID); err != nil {
			return FrostKeySignResponse{}, "", err
		}
		expectedTxHexHash, err := FrostTxHexHash(txHex)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if eventuality.TxHexHash != strings.ToLower(expectedTxHexHash) {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_hex_hash mismatch")
		}
		expectedEventualityID, err := FrostEventualityID(expectedPlanHash, txHash, expectedTxHexHash, expectedTxKeyHash, result.FeeAtomic)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if eventuality.EventualityID != strings.ToLower(expectedEventualityID) {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result eventuality_id mismatch")
		}
		normalizedRefs := make([]FrostSpentOutputRef, len(result.SpentOutputRefs))
		for i, ref := range result.SpentOutputRefs {
			normalizedRefs[i] = FrostSpentOutputRef{
				TxHash:      strings.ToLower(strings.TrimSpace(ref.TxHash)),
				OutputIndex: ref.OutputIndex,
				KeyImage:    strings.ToLower(strings.TrimSpace(ref.KeyImage)),
				SpendTxHash: strings.ToLower(strings.TrimSpace(ref.SpendTxHash)),
			}
		}
		normalized.MsgID = payloadMsgID
		normalized.TxHash = txHash
		normalized.TxHex = txHex
		normalized.TxKey = txKey
		normalized.Eventuality = eventuality
		normalized.SpentOutputRefs = normalizedRefs
	case tcommon.Fail:
		if len(result.Blame.BlameNodes) > frostMaxResultBlameNodes {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost fail result blame nodes exceeds limit")
		}
		payloadMsgID := strings.TrimSpace(result.MsgID)
		if payloadMsgID != "" && expectedMsgID != "" && payloadMsgID != expectedMsgID {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost fail result msg_id mismatch")
		}
		normalizedBlame, err := normalizeBlameForResult(result.Blame)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		normalized.Blame = normalizedBlame
	}

	keyBytes, err := json.Marshal(struct {
		Status          tcommon.Status        `json:"status"`
		MsgID           string                `json:"msg_id"`
		TxHash          string                `json:"tx_hash"`
		TxHex           string                `json:"tx_hex"`
		TxKey           string                `json:"tx_key"`
		FeeAtomic       uint64                `json:"fee_atomic"`
		Eventuality     FrostEventuality      `json:"eventuality"`
		SpentOutputRefs []FrostSpentOutputRef `json:"spent_output_refs"`
		Blame           blame.Blame           `json:"blame"`
	}{
		Status:          normalized.Status,
		MsgID:           normalized.MsgID,
		TxHash:          normalized.TxHash,
		TxHex:           normalized.TxHex,
		TxKey:           normalized.TxKey,
		FeeAtomic:       normalized.FeeAtomic,
		Eventuality:     normalized.Eventuality,
		SpentOutputRefs: normalized.SpentOutputRefs,
		Blame:           normalized.Blame,
	})
	if err != nil {
		return FrostKeySignResponse{}, "", fmt.Errorf("failed to encode frost result comparison key")
	}
	matchKey, err := tcommon.MsgToHashString(keyBytes)
	if err != nil {
		return FrostKeySignResponse{}, "", fmt.Errorf("failed to hash frost result comparison key")
	}

	return normalized, matchKey, nil
}

func normalizeBlameForResult(in blame.Blame) (blame.Blame, error) {
	reason := strings.TrimSpace(in.FailReason)
	if reason == "" {
		return blame.Blame{}, fmt.Errorf("frost fail result missing blame reason")
	}
	if len(reason) > frostMaxResultReasonBytes {
		return blame.Blame{}, fmt.Errorf("frost fail result reason exceeds limit")
	}
	if len(in.BlameNodes) > frostMaxResultBlameNodes {
		return blame.Blame{}, fmt.Errorf("frost fail result blame nodes exceeds limit")
	}
	if len(in.BlameNodes) == 0 {
		return blame.Blame{}, fmt.Errorf("frost fail result missing blame nodes")
	}

	nodes := make([]blame.Node, 0, len(in.BlameNodes))
	seen := make(map[string]struct{}, len(in.BlameNodes))
	for _, node := range in.BlameNodes {
		if len(node.BlameData)+len(node.BlameSignature) > frostMaxResultEvidenceBytes {
			return blame.Blame{}, fmt.Errorf("frost fail result evidence exceeds limit")
		}
		pubKey := strings.TrimSpace(node.Pubkey)
		if pubKey == "" {
			return blame.Blame{}, fmt.Errorf("frost fail result contains empty blame node pubkey")
		}
		key := strings.Join(
			[]string{
				strings.ToLower(pubKey),
				hex.EncodeToString(node.BlameData),
				hex.EncodeToString(node.BlameSignature),
			},
			"|",
		)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		nodes = append(nodes, blame.Node{
			Pubkey:         pubKey,
			BlameData:      node.BlameData,
			BlameSignature: node.BlameSignature,
		})
	}
	if len(nodes) == 0 {
		return blame.Blame{}, fmt.Errorf("frost fail result missing blame nodes")
	}
	sort.Slice(nodes, func(i, j int) bool {
		iKey := strings.ToLower(nodes[i].Pubkey) + "|" + hex.EncodeToString(nodes[i].BlameData) + "|" + hex.EncodeToString(nodes[i].BlameSignature)
		jKey := strings.ToLower(nodes[j].Pubkey) + "|" + hex.EncodeToString(nodes[j].BlameData) + "|" + hex.EncodeToString(nodes[j].BlameSignature)
		return iKey < jKey
	})

	normalized := blame.NewBlame(reason, nodes)
	normalized.IsUnicast = in.IsUnicast
	normalized.Round = strings.TrimSpace(in.Round)
	return normalized, nil
}

type activeSignerSet struct {
	originalByNormalized map[string]string
}

func (a *activeSignerSet) missingPubKeys(seen map[string]struct{}) []string {
	missing := make([]string, 0)
	for norm, original := range a.originalByNormalized {
		if _, ok := seen[norm]; ok {
			continue
		}
		missing = append(missing, original)
	}
	sort.Strings(missing)
	return missing
}

func buildActiveSet(activeParticipants []string, known map[string]string) (*activeSignerSet, error) {
	set := make(map[string]string, len(activeParticipants))
	for _, participant := range activeParticipants {
		norm := normalizePubKey(participant)
		if norm == "" {
			continue
		}
		original, ok := known[norm]
		if !ok {
			return nil, fmt.Errorf("active participant %s is not in signer set", participant)
		}
		set[norm] = original
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("active signer set is empty")
	}
	return &activeSignerSet{originalByNormalized: set}, nil
}

func buildBlame(reason, round string, pubKeys []string) blame.Blame {
	nodes := make([]blame.Node, 0, len(pubKeys))
	seen := make(map[string]struct{}, len(pubKeys))
	for _, pubKey := range pubKeys {
		pubKey = strings.TrimSpace(pubKey)
		if pubKey == "" {
			continue
		}
		if _, ok := seen[pubKey]; ok {
			continue
		}
		seen[pubKey] = struct{}{}
		nodes = append(nodes, blame.NewNode(pubKey, nil, nil))
	}
	b := blame.NewBlame(reason, nodes)
	b.Round = round
	return b
}

func peerIDsToStrings(pIDs []peer.ID) []string {
	out := make([]string, 0, len(pIDs))
	for _, id := range pIDs {
		out = append(out, id.String())
	}
	return out
}

func buildParticipantIndex(members []string) ([]string, map[string]uint32, map[string]string, error) {
	originalByNormalized := make(map[string]string, len(members))
	normalized := make([]string, 0, len(members))
	for _, member := range members {
		norm := normalizePubKey(member)
		if norm == "" {
			continue
		}
		if _, exists := originalByNormalized[norm]; exists {
			return nil, nil, nil, fmt.Errorf("duplicate signer pubkey %s", member)
		}
		originalByNormalized[norm] = strings.TrimSpace(member)
		normalized = append(normalized, norm)
	}
	if len(normalized) == 0 {
		return nil, nil, nil, fmt.Errorf("signer pub keys are empty")
	}
	sort.Strings(normalized)
	indexMap := make(map[string]uint32, len(normalized))
	for i, norm := range normalized {
		indexMap[norm] = uint32(i + 1)
	}
	return normalized, indexMap, originalByNormalized, nil
}

// normalizePubKey canonicalizes pubkey strings for set membership checks.
func normalizePubKey(pubKey string) string {
	return strings.ToLower(strings.TrimSpace(pubKey))
}

// containsNormalizedPubKey reports whether pubKeys contains pubKey after the
// same trimming and case-folding used in FROST peer validation.
func containsNormalizedPubKey(pubKeys []string, pubKey string) bool {
	target := normalizePubKey(pubKey)
	if target == "" {
		return false
	}
	for _, candidate := range pubKeys {
		if normalizePubKey(candidate) == target {
			return true
		}
	}
	return false
}
