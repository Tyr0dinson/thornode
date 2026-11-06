package utxo

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	firoDefaultFeeRate int64  = 25
	firoMinFeeRate     int64  = 2
	firoRelayFeePerKB  uint64 = 1000
)

func firoScriptAddress(value string) (*firoutil.Address, error) {
	script, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return firoutil.AddressFromScript(script, common.FIRONetwork())
}

func (c *Client) firoTransaction(id string, cache map[string]*rpc.FiroTxResult) (*rpc.FiroTxResult, error) {
	if tx := cache[id]; tx != nil {
		return tx, nil
	}
	if !isHash256Hex(id) {
		return nil, fmt.Errorf("invalid FIRO previous transaction ID")
	}
	tx, err := c.rpc.GetRawTransactionVerboseFIRO(id)
	if err != nil {
		return nil, err
	}
	cache[id] = tx
	return tx, nil
}

func (c *Client) getPreviousOutputFIRO(id string, index uint32, cache map[string]*rpc.FiroTxResult) (btcjson.Vout, error) {
	tx, err := c.firoTransaction(id, cache)
	if err != nil {
		return btcjson.Vout{}, err
	}
	if uint64(index) >= uint64(len(tx.Vout)) || tx.Vout[index].N != index {
		return btcjson.Vout{}, fmt.Errorf("invalid FIRO previous output %s:%d", id, index)
	}
	return tx.Vout[index], nil
}

// isSenderInAddressesFIRO checks whether the first input's source address
// exactly matches an entry in the supplied list.
func (c *Client) isSenderInAddressesFIRO(tx *rpc.FiroTxResult, cache map[string]*rpc.FiroTxResult, addresses []common.Address) (bool, error) {
	if len(tx.Vin) == 0 || tx.Vin[0].Txid == "" {
		return false, nil
	}
	previous, err := c.getPreviousOutputFIRO(tx.Vin[0].Txid, tx.Vin[0].Vout, cache)
	if err != nil {
		return false, err
	}
	address, err := firoScriptAddress(previous.ScriptPubKey.Hex)
	return err == nil && slices.Contains(addresses, common.Address(address.String())), nil
}

func (c *Client) getUtxoToSpendFIRO(pubkey common.PubKey, target, floor btcutil.Amount) ([]btcjson.ListUnspentResult, error) {
	return c.selectFiroUTXOs(pubkey, target, floor, nil)
}

func (c *Client) selectFiroUTXOs(pubkey common.PubKey, target, floor btcutil.Amount, feeForInputs func([]btcjson.ListUnspentResult) int64) ([]btcjson.ListUnspentResult, error) {
	if target < 0 || floor < 0 || floor > target {
		return nil, fmt.Errorf("invalid FIRO input target")
	}
	address, err := pubkey.GetAddress(common.FIROChain)
	if err != nil {
		return nil, err
	}
	err = c.registerAddressFIRO(address.String())
	if err != nil {
		return nil, err
	}
	listed, err := c.rpc.ListUnspent(address.String())
	if err != nil {
		return nil, err
	}
	cache := make(map[string]*rpc.FiroTxResult)
	var vaults []common.Address
	seen := make(map[string]bool)
	eligible := make([]btcjson.ListUnspentResult, 0, len(listed))
	for _, item := range listed {
		if item.Confirmations < max(int64(1), c.cfg.UTXO.MinUTXOConfirmations) {
			continue
		}
		var decoded *firoutil.Address
		decoded, err = firoScriptAddress(item.ScriptPubKey)
		if err != nil || decoded.Kind() != firoutil.Exchange || decoded.String() != address.String() {
			return nil, fmt.Errorf("FIRO wallet returned an unexpected vault script")
		}
		id := formatUtxoKey(item.TxID, item.Vout)
		if seen[id] {
			return nil, fmt.Errorf("duplicate FIRO UTXO %s", id)
		}
		seen[id] = true
		var previous btcjson.Vout
		previous, err = c.getPreviousOutputFIRO(item.TxID, item.Vout, cache)
		if err != nil {
			return nil, err
		}
		amount, amountErr := amountToSats(item.Amount)
		previousAmount, previousErr := amountToSats(previous.Value)
		if amountErr != nil || previousErr != nil || amount != previousAmount || !strings.EqualFold(previous.ScriptPubKey.Hex, item.ScriptPubKey) || cache[item.TxID].Height <= 0 {
			return nil, fmt.Errorf("inconsistent FIRO UTXO %s", id)
		}
		if amount == 0 {
			continue
		}
		if uint64(amount) < common.FIROChain.DustThreshold().Uint64() {
			if vaults == nil {
				vaults, err = c.getAsgardAddress()
				if err != nil {
					return nil, err
				}
				vaults = slices.Clone(vaults)
				vaults = append(vaults, address)
			}
			var fromVault bool
			fromVault, err = c.isSenderInAddressesFIRO(cache[item.TxID], cache, vaults)
			if err != nil {
				return nil, err
			}
			if !fromVault {
				continue
			}
		}
		eligible = append(eligible, item)
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if cache[a.TxID].Height != cache[b.TxID].Height {
			return cache[a.TxID].Height < cache[b.TxID].Height
		}
		if a.TxID != b.TxID {
			return a.TxID < b.TxID
		}
		return a.Vout < b.Vout
	})
	var total int64
	required := int64(target)
	var selected []btcjson.ListUnspentResult
	for _, item := range eligible {
		amount, _ := amountToSats(item.Amount)
		if amount > btcutil.MaxSatoshi-total {
			return nil, fmt.Errorf("FIRO UTXO total overflow")
		}
		total += amount
		selected = append(selected, item)
		if feeForInputs != nil {
			fee := feeForInputs(selected)
			if fee < 0 || fee > btcutil.MaxSatoshi-int64(target) {
				return nil, fmt.Errorf("invalid FIRO input fee budget")
			}
			required = int64(target) + fee
		}
		if target > 0 && total >= required {
			break
		}
		if target == 0 && int64(len(selected)) >= c.getMaximumUtxosToSpend() {
			break
		}
	}
	if total < required && (floor == 0 || total < int64(floor)) {
		return nil, fmt.Errorf("insufficient confirmed FIRO inputs: have %d, need %d", total, required)
	}
	// Lock visibility is a veto after selection, never a reason to replace an
	// input with another one and create a different TSS message.
	for _, item := range selected {
		err = c.requireFiroTransactionFinal(cache[item.TxID])
		if err != nil {
			return nil, err
		}
	}
	return selected, nil
}

// getSigningPrevOutputsFIRO loads the outputs being spent and checks their
// ChainLocks, source script and checkpoint amounts before signing.
func (c *Client) getSigningPrevOutputsFIRO(tx *wire.MsgTx, script []byte, amounts map[string]int64) ([]*wire.TxOut, error) {
	cache := make(map[string]*rpc.FiroTxResult)
	previous := make([]*wire.TxOut, len(tx.TxIn))
	for i, in := range tx.TxIn {
		id, index := in.PreviousOutPoint.Hash.String(), in.PreviousOutPoint.Index
		output, err := c.getPreviousOutputFIRO(id, index, cache)
		if err != nil {
			return nil, err
		}
		err = c.requireFiroTransactionFinal(cache[id])
		if err != nil {
			return nil, err
		}
		actual, err := hex.DecodeString(output.ScriptPubKey.Hex)
		if err != nil || !bytes.Equal(script, actual) {
			return nil, fmt.Errorf("FIRO signing input is not owned by the vault")
		}
		amount, err := amountToSats(output.Value)
		if err != nil || amount <= 0 || amounts[formatUtxoKey(id, index)] != amount {
			return nil, fmt.Errorf("FIRO checkpoint amount mismatch")
		}
		previous[i] = wire.NewTxOut(amount, actual)
	}
	return previous, nil
}

func (c *Client) selectFiroInputs(tx stypes.TxOutItem, source, destination []byte, memos [][]byte, consolidate bool) ([]btcjson.ListUnspentResult, error) {
	target := tx.Coins.GetCoin(common.FIROAsset).Amount
	if len(memos) > 1 {
		target = target.AddUint64(uint64(len(memos)-1) * uint64(common.FIROChain.P2WPKHOutputValue()))
	}
	floor := target
	if !tx.MaxGas.IsEmpty() {
		target = target.Add(tx.MaxGas.ToCoins().GetCoin(common.FIROAsset).Amount)
	}
	if !target.BigInt().IsInt64() || target.BigInt().Int64() > btcutil.MaxSatoshi {
		return nil, fmt.Errorf("FIRO payment exceeds the supported amount range")
	}
	amount := btcutil.Amount(target.BigInt().Int64())
	if !tx.MaxGas.IsEmpty() {
		return c.selectFiroUTXOs(tx.VaultPubKey, amount, btcutil.Amount(floor.BigInt().Int64()), nil)
	}
	if consolidate {
		return c.selectFiroUTXOs(tx.VaultPubKey, amount, 0, nil)
	}
	return c.selectFiroUTXOs(tx.VaultPubKey, amount, 0, func(inputs []btcjson.ListUnspentResult) int64 {
		size := c.estimateTxSize(inputs, memos, destination, source)
		fee := c.getGasCoin(tx, size).Amount
		cap := c.maxFeeSats(inputs, memos, destination, source)
		if cap <= 0 {
			return -1
		}
		if fee.GT(cosmos.NewUint(uint64(cap))) {
			return cap
		}
		return int64(max(fee.Uint64(), firoRelayFeePerKB))
	})
}

func (c *Client) signUTXOFIRO(digest []byte, tx stypes.TxOutItem, thorchainHeight int64) ([]byte, error) {
	var signature *btcec.Signature
	var key *btcec.PublicKey
	var err error
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		key = c.nodePrivKey.PubKey()
		signature, err = c.nodePrivKey.Sign(digest)
	} else {
		var decoded cryptotypes.PubKey
		decoded, err = tx.VaultPubKey.Decode()
		if err != nil {
			return nil, err
		}
		key, err = btcec.ParsePubKey(decoded.Bytes(), btcec.S256())
		if err != nil {
			return nil, err
		}
		var raw []byte
		raw, _, err = c.tssKeySigner.RemoteSign(digest, common.SigningAlgoSecp256k1, tx.VaultPubKey.String(), thorchainHeight)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			return nil, tss.ErrNotSelectedForKeysign
		}
		signature, err = firoutil.ParseSignatureRS(raw)
	}
	if err != nil {
		return nil, err
	}
	return firoutil.SignatureScript(digest, signature, key)
}

func (c *Client) getAccountFIRO(pubkey common.PubKey) (common.Account, error) {
	if pubkey.IsEmpty() {
		return common.Account{}, fmt.Errorf("FIRO vault key is empty")
	}
	address, err := pubkey.GetAddress(common.FIROChain)
	if err != nil {
		return common.Account{}, err
	}
	err = c.registerAddressFIRO(address.String())
	if err != nil {
		return common.Account{}, err
	}
	_, err = c.getHeightFIRO()
	if err != nil {
		return common.Account{}, err
	}
	listed, err := c.rpc.ListUnspent(address.String())
	if err != nil {
		return common.Account{}, err
	}
	vaults, err := c.getAsgardAddress()
	if err != nil {
		return common.Account{}, err
	}
	vaults = slices.Clone(vaults)
	vaults = append(vaults, address)
	cache := make(map[string]*rpc.FiroTxResult)
	seen := make(map[string]bool)
	var total int64
	for _, item := range listed {
		id := formatUtxoKey(item.TxID, item.Vout)
		if seen[id] {
			return common.Account{}, fmt.Errorf("duplicate FIRO balance UTXO")
		}
		seen[id] = true
		var decoded *firoutil.Address
		decoded, err = firoScriptAddress(item.ScriptPubKey)
		if err != nil || decoded.String() != address.String() {
			return common.Account{}, fmt.Errorf("FIRO balance script mismatch")
		}
		var previous btcjson.Vout
		previous, err = c.getPreviousOutputFIRO(item.TxID, item.Vout, cache)
		if err != nil {
			return common.Account{}, err
		}
		tx := cache[item.TxID]
		if !tx.ChainLock || tx.Confirmations == 0 {
			var fromVault bool
			fromVault, err = c.isSenderInAddressesFIRO(tx, cache, vaults)
			if err != nil {
				return common.Account{}, err
			}
			if !fromVault {
				if tx.Type != 0 {
					continue
				}
				if !tx.InstantLockKnown {
					return common.Account{}, fmt.Errorf("missing FIRO InstantSend balance evidence")
				}
				// Solvency recognizes locked assets independently of admission switches
				// and local reservations. Signing still requires ChainLocks.
				var anchor int64
				anchor, err = c.firoInstantSendAnchor(&tx.TxResult, cache)
				if err != nil {
					return common.Account{}, err
				}
				if anchor <= 0 {
					continue
				}
			}
		} else {
			err = c.requireFiroTransactionFinal(tx)
			if err != nil {
				return common.Account{}, err
			}
		}
		amount, amountErr := amountToSats(item.Amount)
		actual, actualErr := amountToSats(previous.Value)
		if amountErr != nil || actualErr != nil || amount != actual || !strings.EqualFold(previous.ScriptPubKey.Hex, item.ScriptPubKey) || amount > btcutil.MaxSatoshi-total {
			return common.Account{}, fmt.Errorf("invalid FIRO balance amount")
		}
		total += amount
	}
	// A daemon restart can lose in-memory ChainLocks between the solvency
	// runner's height check and these wallet reads. Do not publish a reduced
	// balance until current lock evidence has been checked again.
	_, err = c.getHeightFIRO()
	if err != nil {
		return common.Account{}, err
	}
	return common.NewAccount(0, 0, common.Coins{common.NewCoin(common.FIROAsset, cosmos.NewUint(uint64(total)))}, false), nil
}

func (c *Client) validateBroadcastFIRO(out stypes.TxOutItem, tx *wire.MsgTx) error {
	err := firoutil.ValidateOutboundFormat(tx)
	if err != nil {
		return err
	}
	_, err = c.requireFiroFinalityReady()
	if err != nil {
		return err
	}
	source, err := c.getSourceScript(out)
	if err != nil {
		return err
	}
	cache := make(map[string]*rpc.FiroTxResult)
	amounts := make(map[string]int64)
	var total int64
	for _, in := range tx.TxIn {
		id, index := in.PreviousOutPoint.Hash.String(), in.PreviousOutPoint.Index
		var previous btcjson.Vout
		previous, err = c.getPreviousOutputFIRO(id, index, cache)
		if err != nil {
			return err
		}
		var amount int64
		amount, err = amountToSats(previous.Value)
		if err != nil || amount > btcutil.MaxSatoshi-total {
			return fmt.Errorf("invalid FIRO broadcast input amount")
		}
		amounts[formatUtxoKey(id, index)] = amount
		total += amount
	}
	previous, err := c.getSigningPrevOutputsFIRO(tx, source, amounts)
	if err != nil {
		return err
	}
	err = firoutil.VerifyTransaction(tx, previous)
	if err != nil {
		return err
	}
	for _, output := range tx.TxOut {
		total -= output.Value
	}
	info, err := c.rpc.GetNetworkInfo()
	if err != nil {
		return err
	}
	relay, err := amountToSats(info.RelayFee)
	if err != nil {
		return err
	}
	minimum := cosmos.NewUint(uint64(relay)).MulUint64(uint64(tx.SerializeSize())).AddUint64(999).QuoUint64(1000)
	if cosmos.NewUint(uint64(total)).LT(minimum) {
		return fmt.Errorf("FIRO fee is below the daemon relay minimum")
	}
	return nil
}
