package types

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

type Ledger struct {
	PubKey common.PubKey `json:"pubkey"`
	Coins  common.Coins  `json:"coins"`
}

func (l Ledger) IsValid() error {
	if l.PubKey.IsEmpty() {
		return fmt.Errorf("pubkey is empty")
	}
	return l.Coins.IsValid()
}

func (l Ledger) String() string {
	sort.Slice(l.Coins[:], func(i, j int) bool {
		return l.Coins[i].Asset.String() < l.Coins[j].Asset.String()
	})
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintf("PubKey: %s |", l.PubKey.String()))
	for _, c := range l.Coins {
		sb.WriteString(fmt.Sprintf(" %s", c.String()))
	}
	return sb.String()
}

type LedgerVoter struct {
	ID          string              `json:"id"` // checksum of sorted input
	Chain       common.Chain        `json:"chain"`
	ChainHeight int64               `json:"chain_height"`
	BlockHeight int64               `json:"block_height"`
	Ledgers     []Ledger            `json:"ledgers"`
	Signers     []cosmos.AccAddress `json:"signers"`
}

func NewLedgerVoter(id string, chain common.Chain, height int64) LedgerVoter {
	return LedgerVoter{
		ID:          id,
		Chain:       chain,
		ChainHeight: height,
	}
}

// HasSigned - check if given address has signed
func (l LedgerVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, sign := range l.Signers {
		if sign.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign this voter with given signer address
func (l *LedgerVoter) Sign(signer cosmos.AccAddress) bool {
	if l.HasSigned(signer) {
		return false
	}
	l.Signers = append(l.Signers, signer)
	return true
}

// Determine if this ledger voter has enough signers
func (l *LedgerVoter) HasConsensus(nas NodeAccounts) bool {
	var count int
	for _, signer := range l.Signers {
		if nas.IsNodeKeys(signer) {
			count += 1
		}
	}
	if HasSimpleMajority(count, len(nas)) {
		return true
	}

	return false
}

// Empty to check whether this Voter is empty or not
func (l *LedgerVoter) Empty() bool {
	return len(l.ID) == 0 || l.ChainHeight == 0 || l.Chain.IsEmpty()
}

func (l *LedgerVoter) String() string {
	return l.ID
}
