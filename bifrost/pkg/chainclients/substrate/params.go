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
	// ChargeAssetTxPayment marks runtimes (e.g. Polkadot Asset Hub) whose fee
	// extension is ChargeAssetTxPayment instead of ChargeTransactionPayment,
	// encoding an extra Option<AssetId> byte after the tip.
	ChargeAssetTxPayment bool
	Indexes              map[string]byte
}

func (p Params) ValidateMetadata(metadata *types.Metadata) error {
	if metadata == nil {
		return fmt.Errorf("nil metadata")
	}

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

	if err := validateSystemResultEvents(metadata); err != nil {
		return err
	}
	if err := validateTransactionFeeEvent(metadata); err != nil {
		return err
	}

	return nil
}

func validateSystemResultEvents(metadata *types.Metadata) error {
	expected := map[string]struct{}{
		"ExtrinsicSuccess": {},
		"ExtrinsicFailed":  {},
	}

	for _, module := range metadata.Modules {
		if module.Name != SystemPallet {
			continue
		}

		found := make(map[string]struct{}, len(expected))
		for _, event := range module.Events {
			_, required := expected[event.Name]
			if !required {
				continue
			}
			found[event.Name] = struct{}{}
		}

		for name := range expected {
			if _, ok := found[name]; !ok {
				return fmt.Errorf("required event %s:%s not found in metadata", SystemPallet, name)
			}
		}
		return nil
	}

	return fmt.Errorf("required pallet %s not found in metadata", SystemPallet)
}

func validateTransactionFeeEvent(metadata *types.Metadata) error {
	for _, module := range metadata.Modules {
		if module.Name != TransactionPaymentPallet {
			continue
		}
		for _, event := range module.Events {
			if event.Name != "TransactionFeePaid" {
				continue
			}
			for _, name := range event.ArgsName {
				if name == "actual_fee" {
					return nil
				}
			}
			return fmt.Errorf("required parameter actual_fee not found in %s:%s metadata", module.Name, event.Name)
		}
		return fmt.Errorf("required event %s:TransactionFeePaid not found in metadata", TransactionPaymentPallet)
	}
	return fmt.Errorf("required pallet %s not found in metadata", TransactionPaymentPallet)
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
