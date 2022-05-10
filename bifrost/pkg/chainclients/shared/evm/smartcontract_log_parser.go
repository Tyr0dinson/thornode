package evm

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	memo "gitlab.com/thorchain/thornode/x/thorchain/memo"
)

const (
	nativeToken            = "0x0000000000000000000000000000000000000000"
	depositEvent           = "0xef519b7eb82aaf6ac376a6df2d793843ebfd593de5f1a0601d3cc6ab49ebb395"
	transferOutEvent       = "0xa9cd03aa3c1b4515114539cd53d22085129d495cb9e9f9af77864526240f1bf7"
	transferAllowanceEvent = "0x05b90458f953d3fcb2d7fb25616a2fddeca749d0c47cc5c9832d0266b5346eea"
	vaultTransferEvent     = "0x281daef48d91e5cd3d32db0784f6af69cd8d8d2e8c612a3568dca51ded51e08f"
)

type (
	contractAddressValidator func(addr *ecommon.Address, includeWhitelist bool) bool
	assetResolver            func(token string) (common.Asset, error)
	tokenDecimalResolver     func(token string) int64
	amountConverter          func(token string, amt *big.Int) cosmos.Uint
)

type SmartContractLogParser struct {
	addressValidator contractAddressValidator
	assetResolver    assetResolver
	decimalResolver  tokenDecimalResolver
	amtConverter     amountConverter
	logger           zerolog.Logger
	vaultABI         *abi.ABI
}

func NewSmartContractLogParser(validator contractAddressValidator,
	resolver assetResolver,
	decimalResolver tokenDecimalResolver,
	amtConverter amountConverter,
	vaultABI *abi.ABI,
) SmartContractLogParser {
	return SmartContractLogParser{
		addressValidator: validator,
		assetResolver:    resolver,
		decimalResolver:  decimalResolver,
		vaultABI:         vaultABI,
		amtConverter:     amtConverter,
		logger:           log.Logger.With().Str("module", "SmartContractLogParser").Logger(),
	}
}

// vaultDepositEvent represent a vault deposit
type vaultDepositEvent struct {
	To     ecommon.Address
	Asset  ecommon.Address
	Amount *big.Int
	Memo   string
}

func (scp *SmartContractLogParser) parseDeposit(log etypes.Log) (vaultDepositEvent, error) {
	const DepositEventName = "Deposit"
	event := vaultDepositEvent{}
	if err := scp.unpackVaultLog(&event, DepositEventName, log); err != nil {
		return event, fmt.Errorf("fail to unpack event: %w", err)
	}
	return event, nil
}

// RouterCoin represent the coins transfer between vault
type RouterCoin struct {
	Asset  ecommon.Address
	Amount *big.Int
}

type routerVaultTransfer struct {
	OldVault ecommon.Address
	NewVault ecommon.Address
	Coins    []RouterCoin
	Memo     string
}

func (scp *SmartContractLogParser) parseVaultTransfer(log etypes.Log) (routerVaultTransfer, error) {
	const vaultTransferEventName = "VaultTransfer"
	event := routerVaultTransfer{}
	if err := scp.unpackVaultLog(&event, vaultTransferEventName, log); err != nil {
		return event, fmt.Errorf("fail to unpack event: %w", err)
	}
	return event, nil
}

func (scp *SmartContractLogParser) unpackVaultLog(out interface{}, event string, log etypes.Log) error {
	if len(log.Data) > 0 {
		if err := scp.vaultABI.UnpackIntoInterface(out, event, log.Data); err != nil {
			return fmt.Errorf("fail to parse event: %w", err)
		}
	}
	var indexed abi.Arguments
	for _, arg := range scp.vaultABI.Events[event].Inputs {
		if arg.Indexed {
			indexed = append(indexed, arg)
		}
	}
	return abi.ParseTopics(out, indexed, log.Topics[1:])
}

type vaultTransferOutEvent struct {
	Vault  ecommon.Address
	To     ecommon.Address
	Asset  ecommon.Address
	Amount *big.Int
	Memo   string
}

func (scp *SmartContractLogParser) parseTransferOut(log etypes.Log) (vaultTransferOutEvent, error) {
	const TransferOutEventName = "TransferOut"
	event := vaultTransferOutEvent{}
	if err := scp.unpackVaultLog(&event, TransferOutEventName, log); err != nil {
		return event, fmt.Errorf("fail to parse transfer out event")
	}
	return event, nil
}

type vaultTransferAllowanceEvent struct {
	OldVault ecommon.Address
	NewVault ecommon.Address
	Asset    ecommon.Address
	Amount   *big.Int
	Memo     string
}

func (scp *SmartContractLogParser) parseTransferAllowanceEvent(log etypes.Log) (vaultTransferAllowanceEvent, error) {
	const TransferAllowanceEventName = "TransferAllowance"
	event := vaultTransferAllowanceEvent{}
	if err := scp.unpackVaultLog(&event, TransferAllowanceEventName, log); err != nil {
		return event, fmt.Errorf("fail to parse transfer allowance event")
	}
	return event, nil
}

func (scp *SmartContractLogParser) GetTxInItem(logs []*etypes.Log, txInItem *types.TxInItem) (bool, error) {
	if len(logs) == 0 {
		return false, nil
	}
	isVaultTransfer := false
	for _, item := range logs {
		// only events produced by THORChain router is processed
		if !scp.addressValidator(&item.Address, false) {
			continue
		}
		earlyExit := false
		switch item.Topics[0].String() {
		case depositEvent:
			// router contract , deposit function has re-entrance protection
			depositEvt, err := scp.parseDeposit(*item)
			if err != nil {
				scp.logger.Err(err).Msg("fail to parse deposit event")
				continue
			}
			scp.logger.Info().Msgf("deposit:%+v", depositEvt)
			if len(depositEvt.Amount.Bits()) == 0 {
				scp.logger.Info().Msg("deposit amount is 0, ignore")
				continue
			}
			if len(txInItem.To) > 0 && !strings.EqualFold(txInItem.To, depositEvt.To.String()) {
				return false, fmt.Errorf("multiple events in the same transaction, have different to addresses , ignore")
			}
			if len(txInItem.Memo) > 0 && !strings.EqualFold(txInItem.Memo, depositEvt.Memo) {
				return false, fmt.Errorf("multiple events in the same transaction , have different memo , ignore")
			}
			asset, err := scp.assetResolver(depositEvt.Asset.String())
			if err != nil {
				scp.logger.Err(err).Msgf("fail to get asset from token address: %s", depositEvt.Asset)
				continue
			}
			if asset.IsEmpty() {
				continue
			}
			txInItem.To = depositEvt.To.String()
			txInItem.Memo = depositEvt.Memo
			decimals := scp.decimalResolver(depositEvt.Asset.String())
			scp.logger.Info().Msgf("token:%s,decimals:%d", depositEvt.Asset, decimals)
			txInItem.Coins = append(txInItem.Coins,
				common.NewCoin(asset, scp.amtConverter(depositEvt.Asset.String(), depositEvt.Amount)).WithDecimals(decimals))
			isVaultTransfer = false
		case transferOutEvent:
			// it is not legal to have multiple transferOut event , transferOut event should be final
			transferOutEvt, err := scp.parseTransferOut(*item)
			if err != nil {
				scp.logger.Err(err).Msg("fail to parse transfer out event")
				continue
			}
			scp.logger.Info().Msgf("transfer out: %+v", transferOutEvt)
			m, err := memo.ParseMemo(common.LatestVersion, transferOutEvt.Memo)
			if err != nil {
				scp.logger.Err(err).Msgf("fail to parse memo: %s", transferOutEvt.Memo)
				continue
			}
			if !m.IsOutbound() && !m.IsType(memo.TxMigrate) && !m.IsType(memo.TxYggdrasilFund) {
				scp.logger.Error().Msgf("%s is not correct memo to use transfer out", transferOutEvt.Memo)
				continue
			}
			asset, err := scp.assetResolver(transferOutEvt.Asset.String())
			if err != nil {
				return false, fmt.Errorf("fail to get asset from token address: %w", err)
			}
			if asset.IsEmpty() {
				return false, nil
			}
			txInItem.To = transferOutEvt.To.String()
			txInItem.Memo = transferOutEvt.Memo
			decimals := scp.decimalResolver(transferOutEvt.Asset.String())
			txInItem.Coins = common.NewCoins(
				common.NewCoin(asset, scp.amtConverter(transferOutEvt.Asset.String(), transferOutEvt.Amount)).WithDecimals(decimals),
			)
			earlyExit = true
			isVaultTransfer = false
		case transferAllowanceEvent:
			// there is no circumstance , router will emit multiple transferAllowance event
			// if that does happen , it means something dodgy happened
			transferAllowanceEvt, err := scp.parseTransferAllowanceEvent(*item)
			if err != nil {
				scp.logger.Err(err).Msg("fail to parse transfer allowance event")
				continue
			}
			scp.logger.Info().Msgf("transfer allowance: %+v", transferAllowanceEvt)
			if len(transferAllowanceEvt.Amount.Bits()) == 0 {
				scp.logger.Error().Msg("transfer allowance event with amount 0, ignore")
				continue
			}
			if len(txInItem.Sender) > 0 && !strings.EqualFold(txInItem.Sender, transferAllowanceEvt.OldVault.String()) {
				scp.logger.Error().Msg("transfer allowance event , vault address is not the same as sender, ignore")
				continue
			}
			if len(txInItem.To) > 0 && !strings.EqualFold(txInItem.To, transferAllowanceEvt.NewVault.String()) {
				scp.logger.Error().Msg("multiple transfer allowance events , have different to addresses , ignore")
				continue
			}
			if len(txInItem.Memo) > 0 && !strings.EqualFold(txInItem.Memo, transferAllowanceEvt.Memo) {
				scp.logger.Error().Msg("multiple events in the same transaction , have different memo , ignore")
				continue
			}
			m, err := memo.ParseMemo(common.LatestVersion, transferAllowanceEvt.Memo)
			if err != nil {
				scp.logger.Err(err).Msgf("fail to parse memo: %s", transferAllowanceEvt.Memo)
				continue
			}
			if !(m.IsType(memo.TxMigrate) || m.IsType(memo.TxYggdrasilFund)) {
				scp.logger.Error().Msgf("%s is not correct memo to use transfer allowance", transferAllowanceEvt.Memo)
				continue
			}
			asset, err := scp.assetResolver(transferAllowanceEvt.Asset.String())
			if err != nil {
				scp.logger.Err(err).Msgf("fail to get asset from token address")
				continue
			}
			if asset.IsEmpty() {
				continue
			}
			txInItem.To = transferAllowanceEvt.NewVault.String()
			txInItem.Memo = transferAllowanceEvt.Memo
			decimals := scp.decimalResolver(transferAllowanceEvt.Asset.String())
			txInItem.Coins = common.NewCoins(
				common.NewCoin(asset, scp.amtConverter(transferAllowanceEvt.Asset.String(), transferAllowanceEvt.Amount)).WithDecimals(decimals),
			)
			isVaultTransfer = false
		case vaultTransferEvent:
			transferEvent, err := scp.parseVaultTransfer(*item)
			if err != nil {
				scp.logger.Err(err).Msg("fail to parse vault transfer event")
				continue
			}
			scp.logger.Info().Msgf("vault transfer: %+v", transferEvent)
			if len(txInItem.Sender) > 0 && !strings.EqualFold(txInItem.Sender, transferEvent.OldVault.String()) {
				scp.logger.Error().Msg("vault transfer event , vault address is not the same as sender, ignore")
				continue
			}
			if len(txInItem.To) > 0 && !strings.EqualFold(txInItem.To, transferEvent.NewVault.String()) {
				scp.logger.Error().Msg("multiple vaultTransfer events , have different to addresses , ignore")
				continue
			}
			if len(txInItem.Memo) > 0 && !strings.EqualFold(txInItem.Memo, transferEvent.Memo) {
				scp.logger.Error().Msg("multiple events in the same transaction , have different memo , ignore")
				continue
			}
			m, err := memo.ParseMemo(common.LatestVersion, transferEvent.Memo)
			if err != nil {
				scp.logger.Err(err).Msgf("fail to parse memo: %s", transferEvent.Memo)
				continue
			}
			if !m.IsType(memo.TxYggdrasilReturn) {
				scp.logger.Error().Msgf("%s is not yggdrasil return memo", transferEvent.Memo)
				continue
			}
			txInItem.To = transferEvent.NewVault.String()
			txInItem.Memo = transferEvent.Memo
			var totalCoins common.Coins
			for _, item := range transferEvent.Coins {
				asset, err := scp.assetResolver(item.Asset.String())
				if err != nil {
					scp.logger.Err(err).Msg("fail to get asset from token address")
					continue
				}
				if asset.IsEmpty() {
					continue
				}
				decimals := scp.decimalResolver(item.Asset.String())
				totalCoins = append(totalCoins, common.NewCoin(asset, scp.amtConverter(item.Asset.String(), item.Amount)).WithDecimals(decimals))
			}
			txInItem.Coins = totalCoins
			isVaultTransfer = true
		}
		if earlyExit {
			break
		}
	}
	return isVaultTransfer, nil
}
