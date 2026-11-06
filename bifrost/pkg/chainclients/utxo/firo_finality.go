package utxo

import (
	"fmt"
	"time"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	utxotypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

// isHealthyFIRO requires fresh finality near the tip and a scanner that has
// reached the ChainLock height.
func (f *finalitySnapshot) isHealthyFIRO(now time.Time, scannedHeight int64) bool {
	maxAge := time.Duration(common.FIROChain.ApproximateBlockMilliseconds()) * time.Millisecond
	return f != nil && !f.checkedAt.IsZero() && now.Sub(f.checkedAt) < maxAge &&
		f.blockHeight >= f.chainLockHeight &&
		f.blockHeight-f.chainLockHeight <= 1 &&
		scannedHeight >= f.chainLockHeight
}

func (c *Client) firoHeaderAt(height int64) (*rpc.FiroHeader, error) {
	hash, err := c.rpc.GetBlockHash(height)
	if err != nil {
		return nil, err
	}
	header, err := c.rpc.GetBlockHeaderFIRO(hash)
	if err != nil {
		return nil, err
	}
	if !isHash256Hex(hash) || header.Hash != hash || header.Height != height || header.Confirmations <= 0 {
		return nil, fmt.Errorf("invalid FIRO active header at %d", height)
	}
	return header, nil
}

// getHeightFIRO uses FIRO's native header RPC; it does not require Dash's
// getbestchainlock. ChainLock coverage is a prefix of the active chain.
func (c *Client) getHeightFIRO() (height int64, err error) {
	c.firoFinalityMu.Lock()
	defer c.firoFinalityMu.Unlock()
	defer func() {
		if err != nil {
			c.finality.Store(nil)
		}
	}()
	checkedAt := time.Now()
	info, err := c.rpc.GetBlockchainInfoFIRO()
	if err != nil {
		return 0, err
	}
	if info.Chain != string(common.FIRONetwork()) || info.Pruned || info.Blocks < 0 || info.Headers > info.Blocks+1 || !isHash256Hex(info.BestBlockHash) {
		return 0, fmt.Errorf("FIRO daemon is on the wrong network, pruned, or catching up")
	}
	tip, err := c.firoHeaderAt(info.Blocks)
	if err != nil {
		return 0, err
	}
	if tip.Hash != info.BestBlockHash {
		return 0, btypes.ErrUnavailableBlock
	}
	// Verify the last persisted observation even after restart or a failed
	// RPC cleared the in-memory health snapshot.
	if c.blockScanner != nil && c.temporalStorage != nil {
		scanned := c.blockScanner.PreviousHeight()
		var stored *utxotypes.BlockMeta
		stored, err = c.temporalStorage.GetBlockMeta(scanned)
		if err != nil {
			return 0, err
		}
		if stored != nil && stored.BlockHash != "" {
			var header *rpc.FiroHeader
			header, err = c.firoHeaderAt(scanned)
			if err != nil {
				return 0, err
			}
			if header.Hash != stored.BlockHash || !header.ChainLock {
				return 0, fmt.Errorf("FIRO persisted finality cannot be verified")
			}
		}
	}
	var cachedHeader *rpc.FiroHeader
	if cached := c.finality.Load(); cached != nil {
		cachedHeader, err = c.firoHeaderAt(cached.chainLockHeight)
		if err != nil {
			return 0, err
		}
		if cachedHeader.Hash != cached.chainLockHash || !cachedHeader.ChainLock {
			return 0, fmt.Errorf("FIRO finalized chain changed or lock evidence was lost")
		}
	}
	var locked *rpc.FiroHeader
	if tip.ChainLock {
		locked = tip
	} else {
		low, high := int64(-1), tip.Height
		if cachedHeader != nil {
			low, locked = cachedHeader.Height, cachedHeader
		}
		for low+1 < high {
			mid := low + (high-low)/2
			var header *rpc.FiroHeader
			header, err = c.firoHeaderAt(mid)
			if err != nil {
				return 0, err
			}
			if header.ChainLock {
				low, locked = mid, header
			} else {
				high = mid
			}
		}
	}
	if locked == nil {
		return 0, btypes.ErrUnavailableBlock
	}
	// A moving tip invalidates the search snapshot, rather than allowing a
	// binary search across headers from different forks.
	end, err := c.rpc.GetBlockchainInfoFIRO()
	if err != nil {
		return 0, err
	}
	if end.BestBlockHash != info.BestBlockHash || end.Blocks != info.Blocks {
		return 0, btypes.ErrUnavailableBlock
	}
	c.finality.Store(&finalitySnapshot{
		blockHeight:     info.Blocks,
		chainLockHeight: locked.Height,
		chainLockHash:   locked.Hash,
		checkedAt:       checkedAt,
	})
	return locked.Height, nil
}

// requireFiroFinalityReady refreshes finality and requires it to be within one
// block of the chain tip before signing, broadcasting, or reporting solvency.
func (c *Client) requireFiroFinalityReady() (int64, error) {
	height, err := c.getHeightFIRO()
	if err != nil {
		return 0, err
	}
	state := c.finality.Load()
	if state == nil || state.blockHeight-state.chainLockHeight > 1 {
		return 0, fmt.Errorf("FIRO finality is behind the chain tip")
	}
	return height, nil
}

func (c *Client) requireFiroTransactionFinal(tx *rpc.FiroTxResult) error {
	if tx == nil || tx.Confirmations == 0 || tx.BlockHash == "" || tx.Height < 0 || !tx.ChainLock {
		return fmt.Errorf("FIRO input transaction is not ChainLocked")
	}
	header, err := c.firoHeaderAt(tx.Height)
	if err != nil {
		return err
	}
	if header.Hash != tx.BlockHash || !header.ChainLock {
		return fmt.Errorf("FIRO transaction is not on the finalized active chain")
	}
	return nil
}

func (c *Client) validateSolvencyFIRO(height int64) error {
	final, err := c.requireFiroFinalityReady()
	if err != nil {
		return err
	}
	if height > final {
		return fmt.Errorf("FIRO solvency is ahead of finality")
	}
	return nil
}
