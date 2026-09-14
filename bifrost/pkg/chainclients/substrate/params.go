package substrate

import (
	"fmt"
	"slices"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/common"
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
	SpecName         string
	MortalEraPeriod  uint32        // blocks a mortal era tx is valid for
	Lookback         time.Duration // time before the THORChain signing block used to choose the mortal-era block
	DefaultFee       uint64
	BalanceSize      int
	SignedExtensions []types.SignedExtension
	Indexes          map[string]byte
}

// Validate checks parameters used for signing, account reads, and fee estimates.
func (p Params) Validate() error {
	period := p.MortalEraPeriod
	// Larger periods can round the era's birth away from the signing block.
	if period < 4 || period > 4096 || period&(period-1) != 0 {
		return fmt.Errorf("mortal era period must be a power of two between 4 and 4096: %d", period)
	}
	if p.DefaultFee == 0 {
		return fmt.Errorf("default fee must be positive")
	}
	if p.BalanceSize != 8 && p.BalanceSize != 16 {
		return fmt.Errorf("balance size must be 8 or 16: %d", p.BalanceSize)
	}
	if p.Lookback < time.Millisecond {
		return fmt.Errorf("lookback must be at least one millisecond: %s", p.Lookback)
	}
	return nil
}

var SignedExtensions = map[common.Chain][]types.SignedExtension{
	common.DOTChain: {
		{Identifier: "AuthorizeCall", ExtraType: "frame_system:extensions:authorize_call:AuthorizeCall", AdditionalType: "NULL"},
		{Identifier: "CheckNonZeroSender", ExtraType: "frame_system:extensions:check_non_zero_sender:CheckNonZeroSender", AdditionalType: "NULL"},
		{Identifier: "CheckSpecVersion", ExtraType: "frame_system:extensions:check_spec_version:CheckSpecVersion", AdditionalType: "U32"},
		{Identifier: "CheckTxVersion", ExtraType: "frame_system:extensions:check_tx_version:CheckTxVersion", AdditionalType: "U32"},
		{Identifier: "CheckGenesis", ExtraType: "frame_system:extensions:check_genesis:CheckGenesis", AdditionalType: "H256"},
		{Identifier: "CheckMortality", ExtraType: "sp_runtime:generic:era:Era", AdditionalType: "H256"},
		{Identifier: "CheckNonce", ExtraType: "compact<U32>", AdditionalType: "NULL"},
		{Identifier: "CheckWeight", ExtraType: "frame_system:extensions:check_weight:CheckWeight", AdditionalType: "NULL"},
		{Identifier: "ChargeAssetTxPayment", ExtraType: "pallet_asset_conversion_tx_payment:ChargeAssetTxPayment", AdditionalType: "NULL"},
		{Identifier: "PrevalidateAttests", ExtraType: "polkadot_runtime_common:claims:PrevalidateAttests", AdditionalType: "NULL"},
		{Identifier: "CheckMetadataHash", ExtraType: "frame_metadata_hash_extension:Mode", AdditionalType: "option<[U8; 32]>"},
		{Identifier: "EthSetOrigin", ExtraType: "pallet_revive:evm:tx_extension:SetOrigin", AdditionalType: "NULL"},
		{Identifier: "StorageWeightReclaim", ExtraType: "NULL", AdditionalType: "NULL"},
	},
	common.TAOChain: {
		{Identifier: "CheckNonZeroSender", ExtraType: "frame_system:extensions:check_non_zero_sender:CheckNonZeroSender", AdditionalType: "NULL"},
		{Identifier: "CheckSpecVersion", ExtraType: "frame_system:extensions:check_spec_version:CheckSpecVersion", AdditionalType: "U32"},
		{Identifier: "CheckTxVersion", ExtraType: "frame_system:extensions:check_tx_version:CheckTxVersion", AdditionalType: "U32"},
		{Identifier: "CheckGenesis", ExtraType: "frame_system:extensions:check_genesis:CheckGenesis", AdditionalType: "H256"},
		{Identifier: "CheckMortality", ExtraType: "sp_runtime:generic:era:Era", AdditionalType: "H256"},
		{Identifier: "CheckNonce", ExtraType: "compact<U32>", AdditionalType: "NULL"},
		{Identifier: "CheckWeight", ExtraType: "frame_system:extensions:check_weight:CheckWeight", AdditionalType: "NULL"},
		{Identifier: "ChargeTransactionPayment", ExtraType: "compact<U64>", AdditionalType: "NULL"},
		{Identifier: "SudoTransactionExtension", ExtraType: "node_subtensor_runtime:sudo_wrapper:SudoTransactionExtension", AdditionalType: "NULL"},
		{Identifier: "CheckShieldedTxValidity", ExtraType: "pallet_shield:extension:CheckShieldedTxValidity", AdditionalType: "NULL"},
		{Identifier: "SubtensorTransactionExtension", ExtraType: "pallet_subtensor:extensions:subtensor:SubtensorTransactionExtension", AdditionalType: "NULL"},
		{Identifier: "DrandPriority", ExtraType: "pallet_drand:drand_priority:DrandPriority", AdditionalType: "NULL"},
		{Identifier: "CheckMetadataHash", ExtraType: "frame_metadata_hash_extension:Mode", AdditionalType: "option<[U8; 32]>"},
	},
}

func (p Params) ValidateMetadata(metadata *types.Metadata) (types.ExtrinsicLayout, error) {
	if metadata == nil {
		return types.ExtrinsicLayout{}, fmt.Errorf("nil metadata")
	}

	// for validating call/event/pallet string consts
	keys := map[string]struct{}{}

	for _, module := range metadata.Modules {
		index, found := p.Indexes[module.Name]
		if !found {
			continue
		}

		if int(index) != module.Index {
			return types.ExtrinsicLayout{}, fmt.Errorf(
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
				return types.ExtrinsicLayout{}, fmt.Errorf(
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
				return types.ExtrinsicLayout{}, fmt.Errorf(
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
			return types.ExtrinsicLayout{}, fmt.Errorf("configured index key %q not found in metadata", key)
		}
	}

	if err := validateSystemResultEvents(metadata); err != nil {
		return types.ExtrinsicLayout{}, err
	}
	if err := validateTransactionFeeEvent(metadata); err != nil {
		return types.ExtrinsicLayout{}, err
	}

	layout, err := types.NewExtrinsicLayout(p.SignedExtensions)
	if err != nil {
		return types.ExtrinsicLayout{}, fmt.Errorf("invalid configured signed extensions: %w", err)
	}
	if err = layout.Validate(metadata.SignedExtensions); err != nil {
		return types.ExtrinsicLayout{}, err
	}

	return layout, nil
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
			for _, name := range []string{"actual_fee", "tip"} {
				if !slices.Contains(event.ArgsName, name) {
					return fmt.Errorf("required parameter %s not found in %s:%s metadata", name, module.Name, event.Name)
				}
			}
			return nil
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
