package cardano

import (
	"fmt"
	"strings"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
)

func AddressFromBytes(bz []byte) (ledger.Address, error) {
	hash := lcommon.Blake2b224Hash(bz)

	return ledger.NewAddressFromParts(
		ledger.AddressTypeKeyNone,
		networkId,
		hash[:],
		nil,
	)
}

// ExtractMemo extracts and returns the CIP-20 cbor encoded memo
// { "674": { "msg": ["my memo"] } }
func ExtractMemo(data lcommon.TransactionMetadatum, depth int) string {
	metaMap, okMap := data.(lcommon.MetaMap)
	if !okMap {
		return ""
	}

	var chunks []string

	for _, pair := range metaMap.Pairs {
		switch key := pair.Key.(type) {
		case lcommon.MetaInt:
			if depth == 0 && key.Value.Int64() == 674 {
				return ExtractMemo(pair.Value, depth+1)
			}
		case lcommon.MetaText:
			if depth == 1 && key.Value == "msg" {
				metaList, okList := pair.Value.(lcommon.MetaList)
				if !okList {
					continue
				}

				for _, item := range metaList.Items {
					if text, ok := item.(lcommon.MetaText); ok {
						chunks = append(chunks, text.Value)
					}
				}

				return strings.Join(chunks, "")
			}
		}
	}

	return ""
}

func CreateMemo(memo string) (lcommon.TransactionMetadatum, *lcommon.Blake2b256, error) {
	var chunks []string
	size := 64
	for i := 0; i < len(memo); i += size {
		end := min(i+size, len(memo))
		chunks = append(chunks, memo[i:end])
	}

	var meta lcommon.TransactionMetadatum

	encoded, err := cbor.Encode(map[uint]any{
		674: map[string]any{
			"msg": chunks,
		},
	})
	if err != nil {
		return meta, nil, err
	}

	meta, err = lcommon.DecodeAuxiliaryDataToMetadata(encoded)
	if err != nil {
		return meta, nil, err
	}

	hash := lcommon.Blake2b256Hash(encoded)

	fmt.Println("hash", len(hash), "meta", len(encoded), "memo", memo)

	return meta, &hash, err
}

func CreateConwayTx(
	fromAddress, toAddress ledger.Address,
	amount, minFeeA, minFeeB uint64,
	memo string,
	utxos map[localstatequery.UtxoId]ledger.BabbageTransactionOutput,
) (*conway.ConwayTransaction, error) {
	var inputs []shelley.ShelleyTransactionInput

	// add 2 ADA to ensure , min ADA per utxo ~ 0.86 ADA
	target := amount + 2_000_000
	total := uint64(0)

	for id, output := range utxos {
		total += output.Amount().Uint64()

		inputs = append(inputs, shelley.NewShelleyTransactionInput(
			id.Hash.String(),
			id.Idx,
		))

		if total >= target {
			break
		}
	}

	if total < target {
		return nil, fmt.Errorf("insufficient funds")
	}

	outputs := []babbage.BabbageTransactionOutput{
		{
			OutputAddress: toAddress,
			OutputAmount: mary.MaryTransactionOutputValue{
				Amount: amount,
			},
		},
		{
			OutputAddress: fromAddress,
			OutputAmount: mary.MaryTransactionOutputValue{
				Amount: total - target,
			},
		},
	}

	tx := &conway.ConwayTransaction{}
	tx.Body.TxInputs = conway.NewConwayTransactionInputSet(inputs)
	tx.Body.TxOutputs = outputs
	tx.Body.TxFee = 0        // Will be replaced later
	tx.Body.Ttl = 50_000_000 // TODO: find reasonable value
	tx.TxIsValid = true

	if memo != "" {
		meta, hash, err := CreateMemo(memo)
		if err != nil {
			return nil, fmt.Errorf("failed to create memo: %w", err)
		}

		tx.TxMetadata = meta
		tx.Body.TxAuxDataHash = hash
	}

	// set dummy witness for correct serialized size
	tx.WitnessSet.VkeyWitnesses = cbor.NewSetType(
		[]lcommon.VkeyWitness{
			{
				Vkey:      make([]byte, 32),
				Signature: make([]byte, 64),
			},
		},
		false,
	)

	// calculate fee
	encoded, err := cbor.Encode(tx)
	if err != nil {
		return nil, err
	}

	// add 5 byte for rounding issues or change of fee value
	fee := uint64(len(encoded))*minFeeA + minFeeB + 5
	if fee == 0 {
		return nil, fmt.Errorf("failed to calculate fee")
	}
	tx.Body.TxFee = fee
	tx.Body.TxOutputs[1].OutputAmount.Amount = total - amount - fee

	return tx, nil
}
