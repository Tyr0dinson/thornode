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

		for _, call := range module.Calls {
			key := module.Name + ":" + call.Name
			if !p.isValidIndex(key, call.LookupIndex) {
				return fmt.Errorf(
					"invalid call index for %s: got %d, expected %d",
					key, call.LookupIndex, p.Indexes[key],
				)
			}
		}

		// only configured event is Balances::TransferEvent
		if module.Name != BalancesPallet {
			continue
		}

		for _, event := range module.Events {
			key := module.Name + ":" + event.Name
			if !p.isValidIndex(key, event.LookupIndex) {
				return fmt.Errorf(
					"invalid event index for %s: got %d, expected %d",
					key, event.LookupIndex, p.Indexes[key],
				)
			}
		}
	}

	return nil
}

func (p Params) isValidIndex(key string, index int) bool {
	expected, found := p.Indexes[key]
	if !found {
		// treat any unconfigured indexes as valid
		return true
	}
	if int(expected) != index {
		return false
	}
	return true
}

func (p Params) Index(key string) byte {
	return p.Indexes[key]
}
