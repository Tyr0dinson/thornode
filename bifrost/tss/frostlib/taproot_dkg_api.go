package frostlib

import (
	"errors"
	"time"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
)

// ErrNotLinked is returned by builds that do not contain the go-frost library.
var ErrNotLinked = errors.New("go-frost library is not linked into this build")

// DefaultTaprootDKGSessionTTL bounds how long an unfinished keygen session and
// its key material stay in memory.
const DefaultTaprootDKGSessionTTL = 30 * time.Minute

// TaprootDKGConfig configures the in-process FROST DKG engine of one node.
type TaprootDKGConfig struct {
	// Chain is the chain whose Taproot vault the keygen creates.
	Chain common.Chain
	// NodePubKey is this node's THORChain node pubkey: its identity in the
	// keygen and the key its incoming shares are locked to.
	NodePubKey common.PubKey
	// NodePrivKey is the 32-byte secp256k1 private key of NodePubKey. It signs
	// outgoing shares and opens incoming ones.
	NodePrivKey []byte
	// SessionTTL overrides DefaultTaprootDKGSessionTTL when positive.
	SessionTTL time.Duration
}

// TaprootDKGEngine is the FROST DKG engine the keygen coordinator drives. It
// has no chain-height provider, so the coordinator records no scanner birthday
// for its vaults.
type TaprootDKGEngine interface {
	tssp.FrostDKGEngine
	// AbortSession releases a session that will not be finalized, with all of
	// its key material. Unknown sessions are ignored.
	AbortSession(sessionID string)
	// Close releases every session.
	Close()
}

// ShareDisputeVerdict is the outcome every keygen member reaches about one
// disputed round-2 share.
type ShareDisputeVerdict int

const (
	// ShareDisputeSenderAtFault: the sender broadcast a share that is not
	// validly signed or whose lock does not open with the genuine lock key.
	ShareDisputeSenderAtFault ShareDisputeVerdict = iota + 1
	// ShareDisputeReceiverAtFault: the receiver revealed a key that is not the
	// lock key it announced.
	ShareDisputeReceiverAtFault
	// ShareDisputeShareOpened: the lock opened and the share is returned. The
	// caller checks it against the sender's round-1 package: a valid share puts
	// the receiver at fault, an invalid one the sender.
	ShareDisputeShareOpened
)

// ShareDisputeError is returned by round 3 when the share of one sender could
// not be used. The keygen session is dead once this error is returned.
//
// When the fault can only be seen by opening the share, it carries the
// throwaway lock key this node announced for that sender, so the other members
// can open that one share themselves. When the fault is visible in the
// broadcast (a missing, malformed or wrongly signed share), no key is revealed.
type ShareDisputeError struct {
	SessionID   string
	Sender      string
	SenderIndex uint32
	// RevealedLockKeyHex is the private half of the lock key, or empty when the
	// fault needs no key to be seen. It opens only the disputed share.
	RevealedLockKeyHex string
	Reason             string
}

func (e *ShareDisputeError) Error() string {
	return "round2 share from " + e.Sender + " rejected: " + e.Reason
}

// ShareDisputeCase is the public material one member needs to judge a dispute.
// Round1 and SealedShareHex must be what this member itself received in the
// keygen broadcasts, never what the accusing receiver supplies.
type ShareDisputeCase struct {
	SessionID    string
	Threshold    uint32
	Participants []string
	// Round1 maps participant index (decimal) to that member's round-1 message.
	Round1             map[string]string
	SenderIndex        uint32
	ReceiverIndex      uint32
	SealedShareHex     string
	RevealedLockKeyHex string
}
