package evm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"

	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var errApprovalPending = chainclienttypes.ErrTxPending

// Numeric checkpoints remain fixed transfer nonces, including legacy checkpoints.
// An approval checkpoint is persisted by the signer BEFORE any approval signing.
// Its unsigned transaction pins the nonce and approval call across retries and
// restarts. Only its fee may be refreshed after a join failure; any saved signed
// bytes remain immutable. Transfer signing requires identifying the mined approval.
type signingCheckpoint struct {
	Version        int                 `json:"version"`
	Outbound       string              `json:"outbound"`
	FromBlock      uint64              `json:"from_block"`
	Transaction    *etypes.Transaction `json:"approval,omitempty"`
	SignedApproval *etypes.Transaction `json:"signed_approval,omitempty"`
	// Present only when this node has not exposed a transfer signature. Numeric
	// checkpoints conservatively retain the legacy possibly-exposed meaning.
	UnsignedTransferNonce *uint64 `json:"unsigned_transfer_nonce,omitempty"`
}

func unsignedTransferCheckpoint(tx stypes.TxOutItem, nonce uint64) ([]byte, error) {
	return json.Marshal(signingCheckpoint{Version: 1, Outbound: tx.Hash(), UnsignedTransferNonce: &nonce})
}

func (c *EVMClient) prepareApproval(tx stypes.TxOutItem, approval *etypes.Transaction, height uint64) ([]byte, error) {
	checkpoint, err := json.Marshal(signingCheckpoint{
		Version: 1, Outbound: tx.Hash(), FromBlock: height, Transaction: approval,
	})
	if err != nil {
		return nil, err
	}
	c.logger.Info().Uint64("approval_nonce", approval.Nonce()).Uint64("from_block", height).
		Msg("checkpointing approval before signing")
	return checkpoint, errApprovalPending
}

// approvalGasPrice applies the same fee policy during preparation and recovery.
func (c *EVMClient) approvalGasPrice(tx stypes.TxOutItem) *big.Int {
	// Use the same gas price calculation as the main transaction
	_, gasRateUnitsPerOne := c.cfg.ChainID.GetGasUnits()
	toiGasRateWei := cosmos.NewUint(uint64(tx.GasRate)).MulUint64(common.WeiPerOne).Quo(gasRateUnitsPerOne).BigInt()

	gasPrice := c.GetGasPrice()
	if c.cfg.BlockScanner.FixedGasRate > 0 || gasPrice.Sign() == 0 {
		gasPrice = toiGasRateWei
	} else {
		// Apply same gas price logic as main transaction
		lowerBound := toiGasRateWei
		lowerBound.Mul(lowerBound, big.NewInt(2))
		lowerBound.Div(lowerBound, big.NewInt(3))

		gasPrice.Div(gasPrice, big.NewInt(common.One*100))
		if gasPrice.Sign() == 0 {
			gasPrice = big.NewInt(1)
		}
		gasPrice.Mul(gasPrice, big.NewInt(common.One*100))

		if gasPrice.Cmp(lowerBound) < 0 {
			gasPrice = lowerBound
		}
	}

	// Avoid transaction hash collision between chains in mocknet (same chain id)
	gasPrice.Add(gasPrice, c.evmScanner.ApprovalTxRateDustWei())

	return gasPrice
}

func (c *EVMClient) retryApproval(tx stypes.TxOutItem) ([]byte, error) {
	var checkpoint signingCheckpoint
	if err := json.Unmarshal(tx.Checkpoint, &checkpoint); err != nil {
		return tx.Checkpoint, fmt.Errorf("decode approval checkpoint: %w", err)
	}
	approval := checkpoint.Transaction
	if checkpoint.Version != 1 || checkpoint.Outbound != tx.Hash() || approval == nil || approval.To() == nil || checkpoint.UnsignedTransferNonce != nil {
		return tx.Checkpoint, errors.New("invalid approval checkpoint")
	}
	if checkpoint.SignedApproval != nil {
		return c.retrySignedApproval(tx, checkpoint)
	}

	// Inspect chain evidence even if the allowance Mimir has since changed.
	// Allowance alone does not identify which nonce was consumed. In particular,
	// a lagging node may have prepared an approval before a preceding outbound
	// reached its RPC. Use the matching mined approval's nonce, never the current
	// pending nonce (which could already include the peer-signed transfer).
	nonce, found, approvalErr := c.minedApprovalNonce(tx, checkpoint)
	if approvalErr != nil {
		return tx.Checkpoint, approvalErr
	}
	if found {
		if nonce == math.MaxUint64 {
			return tx.Checkpoint, errors.New("approval nonce overflow")
		}
		transferCheckpoint, err := unsignedTransferCheckpoint(tx, nonce+1)
		if err != nil {
			return tx.Checkpoint, err
		}
		c.logger.Info().Uint64("approval_nonce", nonce).Uint64("transfer_nonce", nonce+1).
			Msg("checkpointing transfer after mined approval")
		// Return to the signer to durably record the transfer phase before any
		// transfer signature can be exposed. A crash cannot revert it to approval.
		return transferCheckpoint, errApprovalPending
	}

	owner, addressErr := tx.VaultPubKey.GetAddress(c.GetChain())
	if addressErr != nil {
		return tx.Checkpoint, addressErr
	}
	confirmed, nonceErr := c.evmScanner.GetNonceFinalized(owner.String())
	if nonceErr != nil {
		return tx.Checkpoint, fmt.Errorf("check approval confirmed nonce: %w", nonceErr)
	}
	if confirmed > approval.Nonce() {
		// A peer's different signature/fee variant can revert without an
		// Approval event. The nonce cannot be reused, but absence of a matching
		// successful approval never authorizes a transfer at another nonce.
		c.logger.Info().Uint64("approval_nonce", approval.Nonce()).Uint64("confirmed_nonce", confirmed).
			Msg("retiring consumed approval attempt without matched completion")
		return nil, nil
	}

	raw, err := c.sign(approval, tx.VaultPubKey, tx)
	if err != nil {
		var keysignErr tss.KeysignError
		if errors.As(err, &keysignErr) && keysignErr.IsJoinPartyFailure() && approval.Type() == etypes.LegacyTxType {
			gasPrice := c.approvalGasPrice(tx)
			if gasPrice.Cmp(approval.GasPrice()) != 0 {
				// A prior attempt may have exposed shares despite this join failure.
				// Only the fee changes: both variants have the same nonce and exact
				// approval call, so at most one can execute on the canonical chain.
				// Keep the old search height so a peer's old variant is recoverable.
				checkpoint.Transaction = etypes.NewTransaction(approval.Nonce(), *approval.To(), approval.Value(), approval.Gas(), gasPrice, approval.Data())
				saved, saveErr := json.Marshal(checkpoint)
				if saveErr != nil {
					return tx.Checkpoint, saveErr
				}
				c.logger.Info().Uint64("approval_nonce", approval.Nonce()).
					Str("gas_price", gasPrice.String()).Msg("checkpointing repriced approval after join failure")
				// Persist the replacement before entering another signing attempt.
				return saved, errApprovalPending
			}
		}
		return tx.Checkpoint, fmt.Errorf("sign approval: %w", err)
	}
	var signed etypes.Transaction
	if decodeErr := signed.UnmarshalJSON(raw); decodeErr != nil {
		return tx.Checkpoint, fmt.Errorf("decode signed approval: %w", decodeErr)
	}
	// Persist the signed bytes and their hash BEFORE submission. Retries and
	// restarts never enter another TSS ceremony once these bytes are available.
	checkpoint.SignedApproval = &signed
	saved, err := json.Marshal(checkpoint)
	if err != nil {
		return tx.Checkpoint, err
	}
	return saved, errApprovalPending
}

func (c *EVMClient) retrySignedApproval(tx stypes.TxOutItem, checkpoint signingCheckpoint) ([]byte, error) {
	signed := checkpoint.SignedApproval
	// The persisted signature must still bind the prepared approval and vault.
	if c.evmScanner.eipSigner.Hash(signed) != c.evmScanner.eipSigner.Hash(checkpoint.Transaction) {
		return tx.Checkpoint, errors.New("signed approval does not match checkpoint")
	}
	owner, err := tx.VaultPubKey.GetAddress(c.GetChain())
	if err != nil {
		return tx.Checkpoint, err
	}
	sender, err := etypes.Sender(c.evmScanner.eipSigner, signed)
	if err != nil || sender != ecommon.HexToAddress(owner.String()) {
		return tx.Checkpoint, errors.New("signed approval has wrong sender")
	}
	ctx, cancel := c.getTimeoutContext()
	defer cancel()
	known, pending, err := c.ethClient.TransactionByHash(ctx, signed.Hash())
	if err != nil && !errors.Is(err, ethereum.NotFound) {
		return tx.Checkpoint, fmt.Errorf("lookup signed approval: %w", err)
	}
	if err == nil {
		if known.Hash() != signed.Hash() {
			return tx.Checkpoint, errors.New("approval lookup returned wrong transaction")
		}
		if pending {
			return tx.Checkpoint, errApprovalPending
		}
		receipt, receiptErr := c.ethClient.TransactionReceipt(ctx, signed.Hash())
		if errors.Is(receiptErr, ethereum.NotFound) {
			return tx.Checkpoint, errApprovalPending
		}
		if receiptErr != nil {
			return tx.Checkpoint, fmt.Errorf("get signed approval receipt: %w", receiptErr)
		}
		if receipt.TxHash != signed.Hash() || receipt.BlockNumber == nil {
			return tx.Checkpoint, errors.New("invalid signed approval receipt")
		}
		if receipt.Status == etypes.ReceiptStatusFailed {
			// This is still the approval phase: no local transfer signature has
			// been exposed. Retire only this attempt, without observing payment;
			// THORChain can reschedule its outstanding outbound obligation.
			c.logger.Warn().Str("approval_txid", signed.Hash().Hex()).Msg("retiring reverted approval attempt")
			return nil, nil
		}
		if receipt.Status != etypes.ReceiptStatusSuccessful {
			return tx.Checkpoint, errors.New("invalid signed approval receipt status")
		}
		if signed.Nonce() == math.MaxUint64 {
			return tx.Checkpoint, errors.New("approval nonce overflow")
		}
		next, checkpointErr := unsignedTransferCheckpoint(tx, signed.Nonce()+1)
		if checkpointErr != nil {
			return tx.Checkpoint, checkpointErr
		}
		return next, errApprovalPending
	}

	// If this nonce has been mined but our hash is absent, a different party
	// may have completed the approval with another signature. Only that case
	// needs the event search; normal submission does not depend on eth_getLogs.
	confirmed, err := c.evmScanner.GetNonceFinalized(owner.String())
	if err != nil {
		return tx.Checkpoint, err
	}
	if confirmed > signed.Nonce() {
		nonce, found, err := c.minedApprovalNonce(tx, checkpoint)
		if err != nil {
			return tx.Checkpoint, err
		}
		if found {
			if nonce == math.MaxUint64 {
				return tx.Checkpoint, errors.New("approval nonce overflow")
			}
			next, err := unsignedTransferCheckpoint(tx, nonce+1)
			if err != nil {
				return tx.Checkpoint, err
			}
			return next, errApprovalPending
		}
		// The saved hash is absent and this nonce has already been mined.
		// Retire locally without claiming the outbound succeeded, including
		// when an older fee variant reverted and emitted no Approval event.
		c.logger.Info().Uint64("approval_nonce", signed.Nonce()).Uint64("confirmed_nonce", confirmed).
			Msg("retiring consumed signed approval without matched completion")
		return nil, nil
	}
	if err := c.ethClient.SendTransaction(ctx, signed); !isAcceptableError(err) {
		return tx.Checkpoint, fmt.Errorf("broadcast approval: %w", err)
	}
	c.logger.Info().Str("approval_txid", signed.Hash().Hex()).Uint64("approval_nonce", signed.Nonce()).
		Msg("approval submitted; awaiting mined confirmation")
	return tx.Checkpoint, errApprovalPending
}

// minedApprovalNonce searches only this attempt's history, in bounded RPC pages.
// An Approval event is used to locate candidates; the transaction and receipt
// must also prove that this vault successfully called the exact approval.
func (c *EVMClient) minedApprovalNonce(tx stypes.TxOutItem, checkpoint signingCheckpoint) (uint64, bool, error) {
	owner, err := tx.VaultPubKey.GetAddress(c.GetChain())
	if err != nil {
		return 0, false, err
	}
	approval := checkpoint.Transaction
	ctx, cancel := c.getTimeoutContext()
	defer cancel()
	head, err := c.ethClient.BlockNumber(ctx)
	if err != nil {
		return 0, false, err
	}
	if head < checkpoint.FromBlock {
		return 0, false, errors.New("approval search head behind checkpoint")
	}
	for from := checkpoint.FromBlock; from <= head; {
		to := head
		if head-from > 999 {
			to = from + 999
		}
		logs, err := c.ethClient.FilterLogs(ctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(from), ToBlock: new(big.Int).SetUint64(to),
			Addresses: []ecommon.Address{*approval.To()},
			Topics: [][]ecommon.Hash{
				{c.evmScanner.erc20ABI.Events["Approval"].ID},
				{ecommon.BytesToHash(ecommon.HexToAddress(owner.String()).Bytes())},
			},
		})
		if err != nil {
			return 0, false, fmt.Errorf("find mined approval: %w", err)
		}
		sort.Slice(logs, func(i, j int) bool {
			if logs[i].BlockNumber != logs[j].BlockNumber {
				return logs[i].BlockNumber < logs[j].BlockNumber
			}
			return logs[i].TxIndex < logs[j].TxIndex
		})
		for _, event := range logs {
			if event.Removed || event.Address != *approval.To() || event.BlockNumber < from || event.BlockNumber > to {
				continue
			}
			candidate, pending, err := c.ethClient.TransactionByHash(ctx, event.TxHash)
			if err != nil {
				return 0, false, err
			}
			if pending || candidate.Hash() != event.TxHash || candidate.To() == nil || *candidate.To() != *approval.To() ||
				!bytes.Equal(candidate.Data(), approval.Data()) ||
				candidate.Value().Cmp(approval.Value()) != 0 {
				continue
			}
			sender, err := etypes.Sender(c.evmScanner.eipSigner, candidate)
			if err != nil || sender != ecommon.HexToAddress(owner.String()) {
				continue
			}
			receipt, err := c.ethClient.TransactionReceipt(ctx, event.TxHash)
			if err != nil {
				return 0, false, err
			}
			if receipt.Status == etypes.ReceiptStatusSuccessful && receipt.TxHash == event.TxHash &&
				receipt.BlockHash == event.BlockHash && receipt.BlockNumber != nil && receipt.BlockNumber.Uint64() == event.BlockNumber {
				return candidate.Nonce(), true, nil
			}
		}
		if to == head {
			break
		}
		from = to + 1
	}
	return 0, false, nil
}
