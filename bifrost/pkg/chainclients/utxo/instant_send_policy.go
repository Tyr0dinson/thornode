package utxo

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"slices"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	dashwire "github.com/dashpay/dashd-go/wire"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

// Use the base fee, never the daemon's local prioritisation fee. Decimal RPC
// amounts are converted exactly, without float comparisons or rounding.
func sufficientInstantSendFee(entry rpc.GetRawMempoolVerboseResult, rate uint64) bool {
	if rate == 0 {
		rate = 8
	}
	if entry.VSize <= 0 {
		return false
	}
	// CVE-2022-23772 was fixed in Go 1.17.7; this module requires Go 1.26.
	fee, ok := new(big.Rat).SetString(entry.Fees.Base.String()) // #nosec G113
	if !ok || fee.Sign() <= 0 {
		return false
	}
	fee.Mul(fee, new(big.Rat).SetInt64(common.One))
	if !fee.IsInt() {
		return false
	}
	minimum := new(big.Int).Mul(new(big.Int).SetUint64(rate), big.NewInt(entry.VSize))
	return fee.Num().Cmp(minimum) >= 0
}

func dashInputs(vins []btcjson.Vin) ([]common.OutputRef, error) {
	refs := make([]common.OutputRef, 0, len(vins))
	for _, vin := range vins {
		if vin.Coinbase != "" {
			return nil, errors.New("coinbase is not a deposit")
		}
		refs = append(refs, common.OutputRef{TxHash: strings.ToUpper(vin.Txid), OutputIndex: vin.Vout})
	}
	slices.SortFunc(refs, func(a, b common.OutputRef) int { return strings.Compare(a.Outpoint(), b.Outpoint()) })
	return refs, common.ValidateDashInputs(refs)
}

func rawDashInputs(raw, id string) ([]common.OutputRef, error) {
	data, err := hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(data)
	var tx dashwire.MsgTx
	err = tx.Deserialize(reader)
	if err != nil {
		return nil, err
	}
	if reader.Len() != 0 || !strings.EqualFold(tx.TxHash().String(), id) {
		return nil, errors.New("raw deposit identity mismatch")
	}
	refs := make([]common.OutputRef, 0, len(tx.TxIn))
	for _, input := range tx.TxIn {
		refs = append(refs, common.OutputRef{TxHash: strings.ToUpper(input.PreviousOutPoint.Hash.String()), OutputIndex: input.PreviousOutPoint.Index})
	}
	slices.SortFunc(refs, func(a, b common.OutputRef) int { return strings.Compare(a.Outpoint(), b.Outpoint()) })
	return refs, common.ValidateDashInputs(refs)
}

// ChainLocked parents exclude unconfirmed ancestor trees from early exposure.
func (c *Client) verifiedInstantSendInputs(tx *rpc.TxResult) ([]common.OutputRef, error) {
	refs, err := rawDashInputs(tx.Hex, tx.Txid)
	if err != nil {
		return nil, err
	}
	verbose, err := dashInputs(tx.Vin)
	if err != nil || !slices.Equal(refs, verbose) {
		return nil, errors.New("deposit input mismatch")
	}
	snapshot, err := c.getChainLockSnapshot()
	if err != nil {
		return nil, err
	}
	for _, input := range refs {
		parent, readErr := c.rpc.GetRawTransactionVerbose(strings.ToLower(input.TxHash))
		if readErr != nil {
			return nil, readErr
		}
		if !strings.EqualFold(parent.Txid, input.TxHash) || !parent.ChainLock || parent.BlockHash == "" || int(input.OutputIndex) >= len(parent.Vout) {
			return nil, errors.New("deposit input is not ChainLocked")
		}
		block, readErr := c.rpc.GetBlockVerbose(parent.BlockHash)
		if readErr != nil {
			return nil, readErr
		}
		if block.Height > snapshot.lockHeight || block.Height < 0 {
			return nil, errors.New("deposit parent exceeds ChainLock")
		}
		hash, readErr := c.rpc.GetBlockHash(block.Height)
		if readErr != nil {
			return nil, readErr
		}
		if !strings.EqualFold(hash, parent.BlockHash) || !slices.ContainsFunc(block.Tx, func(id string) bool { return strings.EqualFold(id, parent.Txid) }) {
			return nil, errors.New("deposit parent is not canonical")
		}
	}
	current, err := c.getChainLockSnapshot()
	if err != nil {
		return nil, err
	}
	if current != snapshot {
		return nil, errors.New("chain changed while checking deposit")
	}
	return refs, nil
}
