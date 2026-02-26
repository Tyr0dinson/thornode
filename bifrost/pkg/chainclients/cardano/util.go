package cardano

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type Utxo struct {
	Hash   string
	Index  int
	Amount uint64
}

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
			if depth == 0 && key.Value.IsInt64() && key.Value.Int64() == 674 {
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

				memo := strings.Join(chunks, "")

				if len(memo) > constants.MaxMemoSize {
					memo = memo[:constants.MaxMemoSize]
				}

				return memo
			}
		}
	}

	return ""
}

func CreateMemo(memo string) (lcommon.TransactionMetadatum, *lcommon.Blake2b256, error) {
	if len(memo) == 0 {
		return nil, nil, fmt.Errorf("memo is empty")
	}

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

	return meta, &hash, err
}

func CreateConwayTx(
	fromAddress, toAddress ledger.Address,
	amount, minFeeA, minFeeB, maxFee uint64,
	memo string,
	utxos []Utxo,
) (*conway.ConwayTransaction, error) {
	sort.Slice(utxos, func(i, j int) bool {
		if utxos[i].Amount != utxos[j].Amount {
			return utxos[i].Amount > utxos[j].Amount
		}
		if utxos[i].Hash != utxos[j].Hash {
			return utxos[i].Hash < utxos[j].Hash
		}
		return utxos[i].Index < utxos[j].Index
	})

	// put n amount of the smallest utxos to the front of the slice
	// to be consumed first for consolidation
	if len(utxos) > numSmallUtxos {
		small := make([]Utxo, numSmallUtxos)
		for i := 0; i < numSmallUtxos; i++ {
			small[i] = utxos[len(utxos)-i-1]
		}

		utxos = append(small, utxos[:len(utxos)-numSmallUtxos]...)
	}

	var inputs []shelley.ShelleyTransactionInput

	target := amount + maxFee
	total := uint64(0)

	for _, utxo := range utxos {
		total += utxo.Amount

		inputs = append(inputs, shelley.NewShelleyTransactionInput(
			utxo.Hash,
			utxo.Index,
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
	tx.Body.TxFee = 1_000_000 // replaced after determining tx size
	tx.Body.Ttl = 50_000_000  // simtest default, replaced in client
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
	fee, err := calculateFee(tx, minFeeA, minFeeB, maxFee)
	if err != nil {
		return nil, err
	}

	remain := total - amount - fee

	if remain > 0 {
		for remain < dustThreshold && len(utxos) > len(inputs) {
			utxo := utxos[len(inputs)]

			inputs = append(inputs, shelley.NewShelleyTransactionInput(
				utxo.Hash,
				utxo.Index,
			))

			total += utxo.Amount

			tx.Body.TxInputs = conway.NewConwayTransactionInputSet(inputs)

			fee, err = calculateFee(tx, minFeeA, minFeeB, maxFee)
			if err != nil {
				return nil, err
			}

			remain = total - amount - fee
		}
	}

	// if still below dustThreshold, spend remain as fee
	if remain < dustThreshold {
		tx.Body.TxOutputs = tx.Body.TxOutputs[:1]
		fee = total - amount
	} else {
		tx.Body.TxOutputs[1].OutputAmount.Amount = remain
	}

	tx.Body.TxFee = fee

	return tx, nil
}

func calculateFee(
	tx *conway.ConwayTransaction,
	minFeeA, minFeeB, maxFee uint64,
) (uint64, error) {
	encoded, err := cbor.Encode(tx)
	if err != nil {
		return 0, err
	}

	if len(encoded) > maxTransactionSize {
		return 0, fmt.Errorf("transaction too large")
	}

	// add 5 byte for rounding issues or change of fee value
	fee := uint64(len(encoded))*minFeeA + minFeeB + 5

	if fee > maxFee {
		return 0, fmt.Errorf("fee exceeds max fee")
	}

	return fee, nil
}

func getUtxoKey(hash []byte, index uint32) []byte {
	key := make([]byte, len(hash)+4)
	copy(key, hash)
	binary.BigEndian.PutUint32(key[len(hash):], index)
	return key
}
