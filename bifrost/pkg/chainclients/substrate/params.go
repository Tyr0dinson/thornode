package substrate

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
)

const (
	SystemPallet                   = "System"
	SystemRemarkCall               = "System:remark"
	BalancesPallet                 = "Balances"
	BalancesTransferAllowDeathCall = "Balances:transfer_allow_death"
	BalancesTransferKeepAliveCall  = "Balances:transfer_keep_alive"
	BalancesTransferEvent          = "Balances:Transfer"
	UtilityPallet                  = "Utility"
	UtilityBatchAllCall            = "Utility:batch_all"
	TransactionPaymentPallet       = "TransactionPayment"
)

type Params struct {
	SpecName          string
	MortalEraPeriod   uint32 // blocks a mortal era tx is valid for
	DefaultFee        uint64
	BalanceSize       int
	CheckMetadataHash bool // whether the runtime includes CheckMetadataHash (RFC-0078)
	Indexes           map[string]byte
}

func (p Params) ValidateMetadata(metadata *types.Metadata) error {
	// for validating call/event/pallet string consts
	keys := map[string]struct{}{}

	for _, module := range metadata.Modules {
		index, found := p.Indexes[module.Name]
		if !found {
			continue
		}

		if int(index) != module.Index {
			return fmt.Errorf(
				"invalid pallet index for %s: got %d, expected %d",
				module.Name, module.Index, p.Indexes[module.Name])
		}

		keys[module.Name] = struct{}{}

		for _, call := range module.Calls {
			key := module.Name + ":" + call.Name
			configured, valid := p.isValidIndex(key, call.LookupIndex)
			if !configured {
				continue
			}

			if !valid {
				return fmt.Errorf(
					"invalid call index for %s: got %d, expected %d",
					key, call.LookupIndex, p.Indexes[key],
				)
			}

			keys[key] = struct{}{}
		}

		// only configured event is Balances::TransferEvent
		if module.Name != BalancesPallet {
			continue
		}

		for _, event := range module.Events {
			key := module.Name + ":" + event.Name
			configured, valid := p.isValidIndex(key, event.LookupIndex)
			if !configured {
				continue
			}

			if !valid {
				return fmt.Errorf(
					"invalid event index for %s: got %d, expected %d",
					key, event.LookupIndex, p.Indexes[key],
				)
			}

			keys[key] = struct{}{}
		}
	}

	// check if all configured keys exist in Metadata
	for key := range p.Indexes {
		_, found := keys[key]
		if !found {
			return fmt.Errorf("configured index key %q not found in metadata", key)
		}
	}

	return nil
}

func (p Params) isValidIndex(key string, index int) (configured, valid bool) {
	expected, configured := p.Indexes[key]
	if !configured {
		return false, false
	}
	if int(expected) != index {
		return true, false
	}
	return true, true
}

func (p Params) Index(key string) byte {
	return p.Indexes[key]
}
