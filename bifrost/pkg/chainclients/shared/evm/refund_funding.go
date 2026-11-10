package evm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"

	metatypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	ctypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// RefundFundingRPC is implemented by the raw clients on both v3.21.0 and develop.
// The proof does not depend on scanner recovery, journal or canonical-view state.
var (
	ErrRefundFundingRejected                 = errors.New("funding transaction rejected by extraction rules")
	ErrRefundFundingBlockReceiptsUnsupported = errors.New("funding block receipts RPC is unsupported")
)

type RefundFundingRPC interface {
	TransactionReceipt(context.Context, ecommon.Hash) (*etypes.Receipt, error)
	BlockByNumber(context.Context, *big.Int) (*etypes.Block, error)
	HeaderByNumber(context.Context, *big.Int) (*etypes.Header, error)
	HeaderByHash(context.Context, ecommon.Hash) (*etypes.Header, error)
	BlockReceipts(context.Context, rpc.BlockNumberOrHash) ([]*etypes.Receipt, error)
}

// RefundFundingSource uses bounded location hints only to find a transaction.
// A hint is never funding evidence; the block, receipt and extracted funds must
// still satisfy the same canonical proof as a hash-index lookup.
type RefundFundingSource struct {
	RPC          RefundFundingRPC
	Chain        common.Chain
	Extract      func(*etypes.Transaction, *etypes.Receipt) (*stypes.TxInItem, error)
	Required     func(context.Context, *etypes.Block, *stypes.TxInItem, map[ecommon.Hash]*etypes.Receipt) (int64, error)
	Tip          func(context.Context) (*etypes.Header, error)
	MinedHeights []int64
	// SearchHeights is a bounded last resort after hash lookup is unavailable.
	SearchHeights []int64
}

func ValidateRefundFunding(ctx context.Context, original common.ObservedTx, source RefundFundingSource) error {
	id, err := hex.DecodeString(strings.TrimPrefix(original.Tx.ID.String(), "0x"))
	if err != nil || len(id) != 32 || !original.Tx.Chain.Equals(source.Chain) || source.RPC == nil || source.Extract == nil || source.Required == nil || source.Tip == nil {
		return fmt.Errorf("invalid EVM refund funding source")
	}
	hash := ecommon.BytesToHash(id)
	location, err := refundFundingAtHeights(ctx, source, hash, source.MinedHeights, 17)
	if err != nil {
		return err
	}
	if location == nil {
		receipt, receiptErr := source.RPC.TransactionReceipt(ctx, hash)
		if receiptErr != nil || receipt == nil {
			location, err = refundFundingAtHeights(ctx, source, hash, source.SearchHeights, 64)
			if err != nil {
				return err
			}
			if location == nil {
				if receiptErr != nil {
					return fmt.Errorf("funding receipt unavailable: %w", receiptErr)
				}
				return fmt.Errorf("funding receipt unavailable")
			}
		} else {
			if receipt.TxHash != hash || receipt.Status != etypes.ReceiptStatusSuccessful || receipt.BlockNumber == nil || !receipt.BlockNumber.IsInt64() || receipt.BlockNumber.Sign() <= 0 {
				return fmt.Errorf("funding receipt is unmined, failed or mismatched")
			}
			block, blockErr := source.RPC.BlockByNumber(ctx, receipt.BlockNumber)
			if blockErr != nil {
				return fmt.Errorf("funding block unavailable: %w", blockErr)
			}
			location = &refundFundingLocation{block: block, receipt: receipt}
		}
	}
	block, receipt := location.block, location.receipt
	if err = ValidateRefundReceipt(ctx, source.RPC, source.Chain, block, receipt); err != nil {
		return err
	}
	tx := block.Transactions()[receipt.TransactionIndex]
	item, err := source.Extract(tx, receipt)
	if err != nil {
		return fmt.Errorf("funding extraction: %w", err)
	}
	if !ctypes.RefundFundingMatches(original, item) {
		return fmt.Errorf("canonical deposit does not match the credited funding")
	}
	required, err := source.Required(ctx, block, item, location.receipts)
	if err != nil {
		return fmt.Errorf("funding confirmation policy: %w", err)
	}
	tip, err := source.Tip(ctx)
	if err != nil {
		return fmt.Errorf("funding tip: %w", err)
	}
	if required < 0 || tip == nil || tip.Number == nil || !tip.Number.IsInt64() || tip.Number.Int64() < block.Number().Int64() || tip.Number.Int64()-block.Number().Int64() < required {
		return fmt.Errorf("funding deposit has insufficient canonical confirmations")
	}
	current, err := source.RPC.HeaderByNumber(ctx, block.Number())
	if err != nil {
		return err
	}
	if current == nil || current.Hash() != block.Hash() {
		return fmt.Errorf("funding block changed during validation")
	}
	return ctx.Err()
}

type refundFundingLocation struct {
	block    *etypes.Block
	receipt  *etypes.Receipt
	receipts map[ecommon.Hash]*etypes.Receipt
}

// indexRefundFundingReceipts rejects nil entries and duplicate transaction IDs.
// Each consumed receipt must still be bound to its exact block transaction.
func indexRefundFundingReceipts(receipts []*etypes.Receipt) (map[ecommon.Hash]*etypes.Receipt, error) {
	indexed := make(map[ecommon.Hash]*etypes.Receipt, len(receipts))
	for _, receipt := range receipts {
		if receipt == nil {
			return nil, fmt.Errorf("funding block contains a missing receipt")
		}
		if _, exists := indexed[receipt.TxHash]; exists {
			return nil, fmt.Errorf("funding block contains duplicate receipts")
		}
		indexed[receipt.TxHash] = receipt
	}
	return indexed, nil
}

// ReadRefundFundingBlockReceipts uses the block number because AVAX's reported
// hash can include header extensions the geth client does not decode. Consumed
// receipts still need ValidateRefundReceipt and a final canonical header check.
func ReadRefundFundingBlockReceipts(ctx context.Context, client RefundFundingRPC, block *etypes.Block) (map[ecommon.Hash]*etypes.Receipt, error) {
	receipts, err := client.BlockReceipts(ctx, rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(block.Number().Int64())))
	if err != nil {
		var rpcErr rpc.Error
		if errors.As(err, &rpcErr) && rpcErr.ErrorCode() == -32601 {
			return nil, ErrRefundFundingBlockReceiptsUnsupported
		}
		return nil, fmt.Errorf("funding block receipts unavailable: %w", err)
	}
	return indexRefundFundingReceipts(receipts)
}

func refundFundingAtHeights(ctx context.Context, source RefundFundingSource, hash ecommon.Hash, heights []int64, limit int) (*refundFundingLocation, error) {
	seen := make(map[int64]bool)
	for _, height := range heights {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if height <= 0 || seen[height] {
			continue
		}
		if len(seen) >= limit {
			break
		}
		seen[height] = true
		block, err := source.RPC.BlockByNumber(ctx, big.NewInt(height))
		if errors.Is(err, ethereum.NotFound) || (err == nil && block == nil) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("funding hinted block unavailable: %w", err)
		}
		if block.Number() == nil || !block.Number().IsInt64() || block.Number().Int64() != height {
			return nil, fmt.Errorf("funding hinted block height mismatch")
		}
		for _, tx := range block.Transactions() {
			if tx.Hash() != hash {
				continue
			}
			indexed, err := ReadRefundFundingBlockReceipts(ctx, source.RPC, block)
			if errors.Is(err, ErrRefundFundingBlockReceiptsUnsupported) {
				// Older RPCs may only expose hash receipt lookup. That path
				// must independently prove the transaction's current location.
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			receipt := indexed[hash]
			if receipt == nil {
				return nil, fmt.Errorf("funding block receipt is unavailable")
			}
			if receipt.TxHash != hash || receipt.Status != etypes.ReceiptStatusSuccessful {
				return nil, fmt.Errorf("funding receipt is failed or mismatched")
			}
			return &refundFundingLocation{block: block, receipt: receipt, receipts: indexed}, nil
		}
	}
	return nil, ctx.Err()
}

// ValidateRefundReceipt binds every consumed receipt/log to the transaction in the
// current block. AVAX's RPC hash may include fields unknown to geth's Header type.
func ValidateRefundReceipt(ctx context.Context, rpc RefundFundingRPC, chain common.Chain, block *etypes.Block, receipt *etypes.Receipt) error {
	if block == nil || block.Number() == nil || !block.Number().IsInt64() || receipt == nil || receipt.BlockNumber == nil || receipt.BlockNumber.Cmp(block.Number()) != 0 || receipt.Status != etypes.ReceiptStatusSuccessful || uint64(receipt.TransactionIndex) >= uint64(len(block.Transactions())) || block.Transactions()[receipt.TransactionIndex].Hash() != receipt.TxHash {
		return fmt.Errorf("funding receipt is not bound to its block transaction")
	}
	if receipt.BlockHash != block.Hash() {
		if !chain.Equals(common.AVAXChain) {
			return fmt.Errorf("funding receipt block identity is not canonical")
		}
		header, err := rpc.HeaderByHash(ctx, receipt.BlockHash)
		if err != nil {
			return err
		}
		if header == nil || header.Number == nil || header.Number.Cmp(block.Number()) != 0 || header.Hash() != block.Hash() {
			return fmt.Errorf("funding receipt block identity is not canonical")
		}
	}
	for _, log := range receipt.Logs {
		if log == nil || log.Removed || log.TxHash != receipt.TxHash || log.BlockHash != receipt.BlockHash || log.BlockNumber != receipt.BlockNumber.Uint64() || log.TxIndex != receipt.TransactionIndex {
			return fmt.Errorf("funding log is not bound to its successful receipt")
		}
	}
	return nil
}

// RefundFundingParser reuses the router ABI without calling ordinary scanner
// entry points, which can mutate signer caches or reject a now-retired vault.
type RefundFundingParser struct {
	Chain    common.Chain
	Signer   etypes.Signer
	ABI      *abi.ABI
	MaxLogs  int64
	Metadata func(string) (metatypes.TokenMeta, error)
	// Vault address -> its router. All addresses are normalized to lowercase.
	Vaults           map[string]common.Address
	Contracts        map[string]bool
	PricedTokens     map[string]bool
	ScanAllContracts bool
	DirectTransfers  bool
}

func (p *RefundFundingParser) Candidate(tx *etypes.Transaction) bool {
	if tx.To() == nil || tx.Type() == etypes.BlobTxType {
		return false
	}
	to := strings.ToLower(tx.To().Hex())
	_, vault := p.Vaults[to]
	return vault || p.Contracts[to] || (p.ScanAllContracts && len(tx.Data()) >= 4) || (p.DirectTransfers && HasERC20TransferSelector(tx) && p.PricedTokens[to])
}

func (p *RefundFundingParser) coin(token ecommon.Address, amount *big.Int) (common.Coin, error) {
	asset := p.Chain.GetGasAsset()
	decimals := uint64(18)
	if token != (ecommon.Address{}) {
		if p.Metadata == nil {
			return common.NoCoin, fmt.Errorf("funding token metadata unavailable")
		}
		meta, err := p.Metadata(token.Hex())
		if err != nil {
			return common.NoCoin, err
		}
		if meta.IsEmpty() || meta.Decimal > 255 || !strings.EqualFold(meta.Address, token.Hex()) {
			return common.NoCoin, fmt.Errorf("funding token metadata is invalid")
		}
		asset, err = common.NewAsset(p.Chain.String() + "." + meta.Symbol + "-" + strings.ToUpper(token.Hex()))
		if err != nil {
			return common.NoCoin, err
		}
		decimals = meta.Decimal
	}
	if amount == nil || amount.Sign() < 0 {
		return common.NoCoin, fmt.Errorf("funding amount is invalid")
	}
	scaled := new(big.Int).Mul(new(big.Int).Set(amount), big.NewInt(common.One))
	scaled.Quo(scaled, new(big.Int).Exp(big.NewInt(10), new(big.Int).SetUint64(decimals), nil))
	if scaled.BitLen() > 256 {
		return common.NoCoin, fmt.Errorf("funding amount overflows")
	}
	coin := common.NewCoin(asset, cosmos.NewUintFromBigInt(scaled))
	if token != (ecommon.Address{}) && decimals < common.THORChainDecimals {
		coin = coin.WithDecimals(int64(decimals))
	}
	return coin, nil
}

func (p *RefundFundingParser) Extract(tx *etypes.Transaction, receipt *etypes.Receipt, pricedOnly bool) (*stypes.TxInItem, error) {
	if tx.To() == nil {
		return nil, nil
	}
	sender, err := p.Signer.Sender(tx)
	if err != nil {
		return nil, err
	}
	item := &stypes.TxInItem{Tx: tx.Hash().Hex()[2:], Sender: strings.ToLower(sender.Hex()), BlockHeight: receipt.BlockNumber.Int64()}
	to := strings.ToLower(tx.To().Hex())
	if _, vault := p.Vaults[to]; vault {
		coin, err := p.coin(ecommon.Address{}, tx.Value())
		if err != nil {
			return nil, err
		}
		memo := string(tx.Data())
		if decoded, decodeErr := hex.DecodeString(memo); decodeErr == nil {
			memo = string(decoded)
		}
		item.To, item.Memo, item.Coins = to, memo, common.NewCoins(coin)
		return item, nil
	}
	if pricedOnly && !p.Contracts[to] && p.MaxLogs > 0 && int64(len(receipt.Logs)) > p.MaxLogs {
		return nil, nil
	}
	if !p.Contracts[to] && HasERC20TransferSelector(tx) && IsERC20Transfer(tx, receipt) {
		if pricedOnly && (!p.DirectTransfers || !p.PricedTokens[to]) {
			return nil, nil
		}
		resolver := &refundFundingTokenResolver{parser: p}
		// transfer(address,uint256): the recipient is the first calldata word.
		// ParseDirectERC20TransferLogs now takes it explicitly (it must match the
		// Transfer log's recipient topic); derive it exactly as the parser does.
		data := tx.Data()
		const recipientEnd = 4 + ecommon.HashLength
		if len(data) < recipientEnd+ecommon.HashLength {
			return nil, nil
		}
		recipient := ecommon.BytesToAddress(data[4:recipientEnd])
		_, err := ParseDirectERC20TransferLogs(receipt.Logs, item, *tx.To(), sender, recipient, DirectERC20TransferLogOptions{Chain: p.Chain, PoolAddressProvider: p, TokenResolver: resolver, Logger: zerolog.Nop()})
		if resolver.err != nil {
			return nil, resolver.err
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRefundFundingRejected, err)
		}
	} else if p.ABI != nil {
		event, ok := p.ABI.Events["Deposit"]
		if !ok {
			return nil, fmt.Errorf("funding deposit ABI unavailable")
		}
		var indexed abi.Arguments
		for _, input := range event.Inputs {
			if input.Indexed {
				indexed = append(indexed, input)
			}
		}
		for _, log := range receipt.Logs {
			if len(log.Topics) == 0 || log.Topics[0] != event.ID {
				continue
			}
			trustedRouter := false
			for _, router := range p.Vaults {
				if !router.IsEmpty() && strings.EqualFold(router.String(), log.Address.Hex()) {
					trustedRouter = true
					break
				}
			}
			if !trustedRouter {
				continue
			} // Reject unrelated emitters before ABI decoding.

			var deposit struct {
				To     ecommon.Address
				Asset  ecommon.Address
				Amount *big.Int
				Memo   string
			}
			if err := p.ABI.UnpackIntoInterface(&deposit, "Deposit", log.Data); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrRefundFundingRejected, err)
			}
			if len(log.Topics) != len(indexed)+1 {
				return nil, fmt.Errorf("%w: deposit topics unavailable", ErrRefundFundingRejected)
			}
			if err := abi.ParseTopics(&deposit, indexed, log.Topics[1:]); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrRefundFundingRejected, err)
			}
			vault := strings.ToLower(deposit.To.Hex())
			router, known := p.Vaults[vault]
			if !known || router.IsEmpty() || !strings.EqualFold(router.String(), log.Address.Hex()) {
				continue
			}
			if deposit.Amount == nil || deposit.Amount.Sign() <= 0 {
				continue
			}
			if pricedOnly && deposit.Asset != (ecommon.Address{}) && !p.PricedTokens[strings.ToLower(deposit.Asset.Hex())] {
				continue
			}
			if item.To != "" && (!strings.EqualFold(item.To, vault) || (item.Memo != "" && !strings.EqualFold(item.Memo, deposit.Memo))) {
				return nil, fmt.Errorf("%w: multiple incompatible events", ErrRefundFundingRejected)
			}
			coin, err := p.coin(deposit.Asset, deposit.Amount)
			if err != nil {
				if _, ok := errors.AsType[TokenNotWhitelistedError](err); ok {
					continue
				}
				return nil, err
			}
			item.To, item.Memo = vault, deposit.Memo
			item.Coins = append(item.Coins, coin)
		}
	}
	if item.To == "" || item.Coins.IsEmpty() {
		return nil, nil
	}
	return item, nil
}

func (p *RefundFundingParser) IsActivePoolAddress(address string, chain common.Chain) (bool, common.ChainPoolInfo) {
	_, ok := p.Vaults[strings.ToLower(address)]
	if !chain.Equals(p.Chain) {
		ok = false
	}
	return ok, common.ChainPoolInfo{Chain: p.Chain, PoolAddress: common.Address(address)}
}

type refundFundingTokenResolver struct {
	parser *RefundFundingParser
	err    error
}

func (r *refundFundingTokenResolver) GetAssetFromTokenAddress(token string) (common.Asset, error) {
	coin, err := r.parser.coin(ecommon.HexToAddress(token), big.NewInt(0))
	if err != nil {
		r.err = err
	}
	return coin.Asset, err
}

func (r *refundFundingTokenResolver) GetTokenDecimalsForTHORChain(token string) int64 {
	coin, err := r.parser.coin(ecommon.HexToAddress(token), big.NewInt(0))
	if err != nil {
		r.err = err
		return 0
	}
	return coin.Decimals
}

func (r *refundFundingTokenResolver) ConvertAmount(token string, amount *big.Int) cosmos.Uint {
	coin, err := r.parser.coin(ecommon.HexToAddress(token), amount)
	if err != nil {
		r.err = err
		return cosmos.ZeroUint()
	}
	return coin.Amount
}
