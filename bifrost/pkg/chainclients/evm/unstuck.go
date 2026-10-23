package evm

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// unstuck should be called in a goroutine and runs until the client stop channel is
// closed. It ensures that stuck transactions are re-broadcast with higher gas price
// before being rescheduled to a different vault.
func (c *EVMClient) unstuck() {
	c.logger.Info().Msg("starting unstuck routine")
	defer c.logger.Info().Msg("stopping unstuck routine")
	defer c.wg.Done()

	for {
		select {
		case <-c.stopchan: // exit when stopchan is closed
			return
		case <-time.After(constants.ThorchainBlockTime):
			c.unstuckAction()
		}
	}
}

func (c *EVMClient) unstuckAction() {
	// Honour HALTSIGNING / HaltSigning<Chain> before starting an unstuck pass.
	// unstuckTx builds and TSS-signs a fresh cancel transaction, which would
	// otherwise bypass signer/sign.go's halt gating.
	if signing.IsSigningHalted(c.bridge, c.cfg.ChainID, c.logger) {
		return
	}

	height, err := c.bridge.GetBlockHeight()
	if err != nil {
		c.logger.Err(err).Msg("failed to get THORChain block height")
		return
	}

	// We only attempt unstuck on transactions within the reschedule buffer blocks of the
	// next signing period. This will ensure we do not clear the signer cache and
	// re-attempt signing right before a reschedule, which may assign to a different vault
	// (behavior post https://gitlab.com/thorchain/thornode/-/merge_requests/3266 should
	// not) or adjust gas values for the tx out. This should result in no more than one
	// sign and broadcast per signing period for a given outbound.
	constValues, err := c.bridge.GetConstants()
	if err != nil {
		c.logger.Err(err).Msg("failed to get THORChain constants")
		return
	}
	signingPeriod := constValues[constants.SigningTransactionPeriod.String()]
	if signingPeriod <= 0 {
		c.logger.Err(err).Int64("signingPeriod", signingPeriod).Msg("invalid signing period")
		return
	}
	rescheduleBufferBlocks := config.GetBifrost().Signer.RescheduleBufferBlocks
	txWaitBlocks := signingPeriod - rescheduleBufferBlocks

	signedTxItems, err := c.evmScanner.blockMetaAccessor.GetSignedTxItems()
	if err != nil {
		c.logger.Err(err).Msg("failed to get all signed tx items")
		return
	}
	for idx, item := range signedTxItems {
		clog := c.logger.With().
			Str("txid", item.Hash).
			Str("vault", item.VaultPubKey).
			Interface("txout", item.TxOutItem).
			Logger()

		// this should not possible, but just skip it
		if item.Height > height {
			clog.Warn().Msg("signed outbound height greater than current thorchain height")
			continue
		}

		if (height - item.Height) < txWaitBlocks {
			// not time yet, continue to wait for this tx to commit
			continue
		}

		// only attempt unstuck during the reschedule buffer of the signing period
		if item.TxOutItem != nil {
			periodBlock := (height - item.TxOutItem.Height) % signingPeriod
			if signingPeriod-periodBlock > rescheduleBufferBlocks {
				clog.Warn().Msg("waiting for start of reschedule buffer blocks to unstuck")
				continue
			}
		}

		clog.Warn().Msg("attempting unstuck")

		err = c.unstuckTx(clog, item)
		if errors.Is(err, errUnstuckRetryLater) {
			// nothing was keysigned for this item. Whether an item is deferred depends
			// on this node's own endpoints, so moving on to another item of the same
			// vault would start a keysign the other members are not in (they are on
			// this one); only an item of a different vault, whose party cannot
			// collide, is taken this round.
			clog.Warn().Err(err).Msg("unstuck deferred")
			if idx+1 < len(signedTxItems) && signedTxItems[idx+1].VaultPubKey != item.VaultPubKey {
				continue
			}
			break
		}
		if err != nil {
			clog.Err(err).Msg("failed to unstuck tx")
			// Break on error so that if a keysign fails from members getting out of sync
			// (for multiple cancel transactions)
			// all vault members will together next try to keysign the first item in the list.
			break
		}

		// remove stuck transaction from block meta
		if err = c.evmScanner.blockMetaAccessor.RemoveSignedTxItem(item.Hash); err != nil {
			clog.Err(err).Msg("failed to remove block meta tx item")
		}
	}
}

// errUnstuckRetryLater marks a stuck item that cannot be acted on this round
// without risking a duplicate outbound (not every endpoint could say whether the
// broadcast is still live). unstuckAction moves on to the next item instead of
// stopping, since nothing was keysigned.
var errUnstuckRetryLater = errors.New("unstuck deferred")

// unstuckTx will re-broadcast the transaction for the given txid with a higher gas price.
func (c *EVMClient) unstuckTx(clog zerolog.Logger, item evmtypes.SignedTxItem) error {
	ctx, cancel := c.getTimeoutContext()
	defer cancel()
	tx, pending, err := c.ethClient.TransactionByHash(ctx, ecommon.HexToHash(item.Hash))
	if err != nil && errors.Is(err, ethereum.NotFound) && c.endpointMgr != nil {
		// The active endpoint does not know the transaction. With several endpoints
		// the broadcast may sit only in the mempool of the endpoint that took it, so
		// every endpoint is asked (pinned). Found elsewhere: the cancellation is built
		// from that endpoint's copy, so the vault's nonce does not stay blocked behind
		// the original. Absent everywhere: the signer cache is cleared so the outbound
		// can be re-signed. Not every endpoint answered: the item is left for a later
		// round, and the signer cache is kept, because clearing it on an incomplete
		// answer could re-sign the outbound at a new nonce while the original is live.
		var res signercache.LookupResult
		tx, pending, res = evm.FindTxEverywhere(ctx, c.endpointMgr, c.ethClient, ecommon.HexToHash(item.Hash), c.getSignerCacheExpiry())
		switch res {
		case signercache.LookupFound:
			err = nil
			clog.Warn().Msg("transaction unknown to the active endpoint but alive on another; cancelling from that copy")
		case signercache.LookupNotFound:
			clog.Err(err).Msg("transaction not found on any endpoint")
			// dropped from mempool or re-orged, remove from signer cache to resign
			c.signerCacheManager.RemoveSigned(item.Hash)
			return nil
		default:
			return fmt.Errorf("%w: transaction %s is unknown to the active endpoint and not every endpoint answered", errUnstuckRetryLater, item.Hash)
		}
	}
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			clog.Err(err).Msg("transaction not found on chain")
			// dropped from mempool or re-orged, remove from signer cache to resign
			c.signerCacheManager.RemoveSigned(item.Hash)
			return nil
		}
		return fmt.Errorf("fail to get transaction by txid: %s, error: %w", item.Hash, err)
	}

	// the transaction is no longer pending
	if !pending {
		clog.Info().Msg("transaction already committed")
		return nil
	}

	pubKey, err := common.NewPubKey(item.VaultPubKey)
	if err != nil {
		clog.Err(err).Msg("vault public key is invalid")
		// this should not happen, and if it does there is no point to try again
		return nil
	}
	address, err := pubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		clog.Err(err).Msg("fail to get EVM address")
		return nil
	}

	clog = clog.With().Uint64("nonce", tx.Nonce()).Logger()
	clog.Info().Msg("cancel tx with nonce")

	// double the current suggest gas price
	currentGasRate := big.NewInt(1).Mul(c.GetGasPrice(), big.NewInt(2))

	// inflate the originGasPrice by 10% as per EVM chain, the transaction to cancel an
	// existing tx in the mempool need to pay at least 10% more than the original price,
	// otherwise it will not allow it. the error will be "replacement transaction
	// underpriced" this is the way to get 110% of the original gas price
	originGasPrice := tx.GasPrice()
	inflatedOriginalGasPrice := big.NewInt(1).Div(big.NewInt(1).Mul(tx.GasPrice(), big.NewInt(11)), big.NewInt(10))
	if inflatedOriginalGasPrice.Cmp(currentGasRate) > 0 {
		currentGasRate = big.NewInt(1).Mul(originGasPrice, big.NewInt(2))
	}

	// create the cancel transaction
	canceltx := etypes.NewTransaction(
		tx.Nonce(),
		ecommon.HexToAddress(address.String()),
		big.NewInt(0),
		c.cfg.BlockScanner.MaxGasLimit,
		currentGasRate,
		nil,
	)
	rawBytes, err := c.kw.Sign(canceltx, pubKey, item.TxOutItem.Height)
	if err != nil {
		return fmt.Errorf("fail to sign tx for cancelling with nonce: %d, err: %w", tx.Nonce(), err)
	}
	broadcastTx := &etypes.Transaction{}
	if err = broadcastTx.UnmarshalJSON(rawBytes); err != nil {
		return fmt.Errorf("fail to unmarshal tx, err: %w", err)
	}

	// broadcast the cancel transaction
	ctx, cancel = c.getTimeoutContext()
	defer cancel()
	err = c.evmScanner.ethClient.SendTransaction(ctx, broadcastTx)
	if !isAcceptableError(err) {
		return fmt.Errorf("fail to broadcast the cancel transaction, hash: %s, err: %w", item.Hash, err)
	}

	clog = clog.With().Stringer("unstuck_txid", broadcastTx.Hash()).Logger()
	clog.Info().Msg("broadcast new tx, old tx cancelled")

	// add cancel transaction to signer cache so scanner removes outbound on confirmation
	toi := item.TxOutItem
	expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
	err = c.signerCacheManager.SetSignedWithExpiry(toi.CacheHash(), toi.CacheVault(c.GetChain()), broadcastTx.Hash().Hex(), expiry)
	if err != nil {
		clog.Err(err).Msg("fail to set signed tx, unstuck outbound will not retry")
	}

	return nil
}

// AddSignedTxItem add the transaction to key value store
func (c *EVMClient) AddSignedTxItem(hash string, height int64, vaultPubKey string, toi *stypes.TxOutItem) error {
	return c.evmScanner.blockMetaAccessor.AddSignedTxItem(evmtypes.SignedTxItem{
		Hash:        hash,
		Height:      height,
		VaultPubKey: vaultPubKey,
		TxOutItem:   toi,
	})
}
