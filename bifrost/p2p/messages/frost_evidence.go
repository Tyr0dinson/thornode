package messages

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	FrostProtocolVersion  = uint16(2)
	frostHashHexChars     = 64
	frostMaxVaultKeyChars = 256
)

// FrostSignedEnvelope is an application-authenticated statement by the node
// that owns a FROST round payload. Unlike transport authentication, it remains
// independently verifiable after the libp2p stream is gone and can therefore
// be used as positive equivocation evidence.
type FrostSignedEnvelope struct {
	ProtocolVersion  uint16 `json:"protocol_version"`
	SessionID        string `json:"session_id"`
	VaultPubKey      string `json:"vault_pub_key"`
	Network          string `json:"network"`
	Round            string `json:"round"`
	SenderPubKey     string `json:"sender_pub_key"`
	ParticipantIndex uint32 `json:"participant_index"`
	PayloadHash      string `json:"payload_hash"`
	Signature        []byte `json:"signature"`
}

func (e FrostSignedEnvelope) SignBytes() ([]byte, error) {
	if e.ProtocolVersion != FrostProtocolVersion {
		return nil, fmt.Errorf("unsupported FROST envelope version")
	}
	sessionID := strings.ToLower(strings.TrimSpace(e.SessionID))
	vaultPubKey := strings.ToLower(strings.TrimSpace(e.VaultPubKey))
	network := strings.ToLower(strings.TrimSpace(e.Network))
	round := strings.TrimSpace(e.Round)
	sender := strings.TrimSpace(e.SenderPubKey)
	payloadHash := strings.ToLower(strings.TrimSpace(e.PayloadHash))
	if sessionID == "" || vaultPubKey == "" || network == "" || round == "" || sender == "" || e.ParticipantIndex == 0 {
		return nil, fmt.Errorf("incomplete FROST envelope")
	}
	switch network {
	case "mainnet", "mocknet", "stagenet", "chainnet":
	default:
		return nil, fmt.Errorf("invalid FROST envelope network")
	}
	if len(vaultPubKey) > frostMaxVaultKeyChars {
		return nil, fmt.Errorf("invalid FROST envelope vault pubkey length")
	}
	if len(sessionID) != frostHashHexChars {
		return nil, fmt.Errorf("invalid FROST envelope session id length")
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		return nil, fmt.Errorf("invalid FROST envelope session id")
	}
	if len(payloadHash) != frostHashHexChars {
		return nil, fmt.Errorf("invalid FROST envelope payload hash length")
	}
	if _, err := hex.DecodeString(payloadHash); err != nil {
		return nil, fmt.Errorf("invalid FROST envelope payload hash")
	}
	return []byte(strings.Join([]string{
		"domain=thorchain-frost-signed-envelope-v2",
		"protocol_version=" + strconv.FormatUint(uint64(e.ProtocolVersion), 10),
		"session_id=" + sessionID,
		"vault_pub_key=" + vaultPubKey,
		"network=" + network,
		"round=" + round,
		"sender_pub_key=" + sender,
		"participant_index=" + strconv.FormatUint(uint64(e.ParticipantIndex), 10),
		"payload_hash=" + payloadHash,
	}, "\n")), nil
}

// FrostSignedEcho authenticates an attester's statement that it received the
// enclosed target-signed envelope. The target signature is included in the
// signed bytes, preventing an echo from being transplanted onto another proof.
type FrostSignedEcho struct {
	ProtocolVersion uint16              `json:"protocol_version"`
	AttesterPubKey  string              `json:"attester_pub_key"`
	Target          FrostSignedEnvelope `json:"target"`
	Signature       []byte              `json:"signature"`
}

func (e FrostSignedEcho) SignBytes() ([]byte, error) {
	targetBytes, err := e.Target.SignBytes()
	if err != nil {
		return nil, err
	}
	if e.ProtocolVersion != FrostProtocolVersion || strings.TrimSpace(e.AttesterPubKey) == "" {
		return nil, fmt.Errorf("incomplete FROST echo")
	}
	return []byte(strings.Join([]string{
		"domain=thorchain-frost-signed-echo-v2",
		"protocol_version=" + strconv.FormatUint(uint64(e.ProtocolVersion), 10),
		"attester_pub_key=" + strings.TrimSpace(e.AttesterPubKey),
		"target_statement=" + hex.EncodeToString(targetBytes),
		"target_signature=" + hex.EncodeToString(e.Target.Signature),
	}, "\n")), nil
}
