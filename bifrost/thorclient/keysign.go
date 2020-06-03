package thorclient

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	btypes "gitlab.com/thorchain/thornode/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	xtypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

var ErrNotFound error = fmt.Errorf("not found")

type QueryKeysign struct {
	Keysign   xtypes.TxOut `json:"keysign"`
	Signature string       `json:"signature"`
}

// GetKeysign retrieves txout from this block height from thorchain
func (b *ThorchainBridge) GetKeysign(blockHeight int64, pk string) (types.TxOut, error) {
	path := fmt.Sprintf("%s/%d/%s", KeysignEndpoint, blockHeight, pk)
	body, status, err := b.getWithPath(path)
	if err != nil {
		b.errCounter.WithLabelValues("fail_get_tx_out", strconv.FormatInt(blockHeight, 10)).Inc()
		if status == http.StatusNotFound {
			return types.TxOut{}, btypes.UnavailableBlock
		}
		return types.TxOut{}, fmt.Errorf("failed to get tx from a block height: %w", err)
	}
	var query QueryKeysign
	if err = b.cdc.UnmarshalJSON(body, &query); err != nil {
		b.errCounter.WithLabelValues("fail_unmarshal_tx_out", strconv.FormatInt(blockHeight, 10)).Inc()
		return types.TxOut{}, fmt.Errorf("failed to unmarshal TxOut: %w", err)
	}
	if query.Signature == "" {
		return types.TxOut{}, errors.New("invalid keysign signature: empty")
	}
	buf, err := json.Marshal(query.Keysign)
	if err != nil {
		return types.TxOut{}, fmt.Errorf("fail to marshal keysign block to json: %w", err)
	}
	pubKey := b.keys.signerInfo.GetPubKey()
	s, err := base64.StdEncoding.DecodeString(query.Signature)
	if err != nil {
		return types.TxOut{}, errors.New("invalid keysign signature: cannot decode signature")
	}
	if !pubKey.VerifyBytes(buf, s) {
		return types.TxOut{}, errors.New("invalid keysign signature: bad signature")
	}
	txOut := types.TxOut{
		Height:  query.Keysign.Height,
		TxArray: []types.TxArrayItem{},
	}
	for _, item := range query.Keysign.TxArray {
		txOut.TxArray = append(txOut.TxArray, types.TxArrayItem{
			Chain: item.Chain,
			ToAddress: item.ToAddress,
			VaultPubKey: item.VaultPubKey,
			Coin: item.Coin,
			Memo: item.Memo,
			MaxGas: item.MaxGas,
			InHash: item.InHash,
			OutHash: item.OutHash,
		})
	}
	return txOut, nil
}
