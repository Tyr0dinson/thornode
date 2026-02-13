package cardano

import (
	"strings"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"gitlab.com/thorchain/thornode/v3/common"
)

func AddressFromBytes(bz []byte) (ledger.Address, error) {
	var network uint8
	if common.CurrentChainNetwork == common.MockNet {
		network = lcommon.AddressNetworkTestnet
	} else {
		network = lcommon.AddressNetworkMainnet
	}

	hash := lcommon.Blake2b224Hash(bz)

	return ledger.NewAddressFromParts(
		ledger.AddressTypeKeyNone,
		network,
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

	var lines []string

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
						lines = append(lines, text.Value)
					}
				}

				return strings.Join(lines, "")
			}
		}
	}

	return ""
}

func CreateMemo(memo string) (lcommon.TransactionMetadatum, *lcommon.Blake2b256, error) {
	var meta lcommon.TransactionMetadatum

	encoded, err := cbor.Encode(map[uint]any{
		674: map[string]any{
			"msg": []string{memo},
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
