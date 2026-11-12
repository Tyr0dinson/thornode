package utxo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ChainLock-based signing must stop well before a prolonged ChainLock stall can lead to
// outbound reassignment. Allow one block of lag and one expected block interval
// of height-data age, rather than the shared scanner's longer MaxHealthyLag.
const maxHealthyChainLockLag int64 = 1

func (c *Client) getInstantSendAccounting() (*ttypes.QueryInstantSendResponse, error) {
	if c.bridge == nil {
		return nil, errors.New("InstantSend bridge unavailable")
	}
	result, err := c.bridge.GetInstantSend(c.GetChain())
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("invalid InstantSend response")
	}
	found := make(map[common.PubKey]bool)
	for _, vault := range result.Vaults {
		state := vault.State
		err = state.Validate()
		if err != nil {
			return nil, err
		}
		if !state.Chain.Equals(c.GetChain()) || found[state.PubKey] {
			return nil, errors.New("invalid InstantSend vault")
		}
		found[state.PubKey] = true
	}
	return result, nil
}

func instantSendAdmissionState(chain common.Chain, state *ttypes.QueryInstantSendResponse, recipient string) *common.InstantSendState {
	if state == nil || state.Status.Disabled || state.ObservationHeight <= 0 || state.MaxAmount == 0 {
		return nil
	}
	for _, vault := range state.Vaults {
		entry := vault.State
		if !vault.Eligible || !entry.Chain.Equals(chain) || len(entry.Pending) >= common.InstantSendMaxPending {
			continue
		}
		address, err := entry.PubKey.GetAddress(chain)
		if err == nil && address.Equals(common.Address(recipient)) {
			return &entry
		}
	}
	return nil
}

// Poll independently of admission and scanning. Wall time schedules reads only;
// the report itself always carries a verified external block height.
func (c *Client) runDashSolvency() {
	defer c.wg.Done()
	ticker := time.NewTicker(10 * constants.ThorchainBlockTime)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			err := c.reportDashSolvency()
			if err != nil {
				c.log.Err(err).Msg("fail to report Dash solvency")
			}
		}
	}
}

func (c *Client) reportDashSolvency() error {
	c.solvencyLock.Lock()
	defer c.solvencyLock.Unlock()
	accounting, err := c.getInstantSendAccounting()
	if err != nil {
		return err
	}
	snapshot, err := c.getChainLockSnapshot()
	if err != nil {
		return err
	}
	if snapshot.lockHeight <= 0 {
		return errors.New("solvency requires a positive ChainLock height")
	}
	var insolvent, solvent []types.Solvency
	var failures []error
	gas := cosmos.NewUint(3 * c.cfg.UTXO.EstimatedAverageTxSize * c.lastFeeRate.Load())
	for _, entry := range accounting.Vaults {
		account, accountErr := c.getSettledAccount(entry.State.PubKey, snapshot)
		if accountErr != nil {
			failures = append(failures, accountErr)
			continue
		}
		allowance := entry.State.SolvencyAllowance(accounting.Status.Overdue)
		expected := ttypes.Vault{PubKey: entry.State.PubKey, Coins: entry.Coins.Copy().SafeSub(common.NewCoin(c.GetChain().GetGasAsset(), allowance))}
		report := types.Solvency{Chain: c.GetChain(), Height: snapshot.lockHeight, PubKey: entry.State.PubKey, Coins: account.Coins}
		if runners.IsVaultSolvent(c.GetChain(), account, expected, gas) {
			solvent = append(solvent, report)
		} else {
			insolvent = append(insolvent, report)
		}
	}
	current, err := c.getChainLockSnapshot()
	if err != nil {
		return err
	}
	if current != snapshot {
		return errors.New("chain changed during solvency collection")
	}
	reports := insolvent
	if len(reports) == 0 && len(failures) == 0 && len(solvent) > 0 {
		halted, haltErr := runners.IsHaltedOnChain(c.bridge, c.GetChain())
		if haltErr != nil {
			return haltErr
		}
		if halted {
			reports = solvent
		}
	}
	for _, report := range reports {
		previous, exists := c.solvencyReports[report.PubKey]
		if exists && previous.Height == report.Height && previous.Coins.EqualsEx(report.Coins) {
			continue
		}
		select {
		case c.globalSolvencyQueue <- report:
			if c.solvencyReports == nil {
				c.solvencyReports = make(map[common.PubKey]types.Solvency)
			}
			c.solvencyReports[report.PubKey] = report
		case <-c.stopchan:
			return errors.New("solvency stopped")
		case <-time.After(constants.ThorchainBlockTime):
			return errors.New("solvency queue timeout")
		}
	}
	c.lastSolvencyCheckHeight.Store(snapshot.lockHeight)
	return errors.Join(failures...)
}

// Customer funds contribute only once their block is ChainLocked. Internal vault
// change retains the existing treatment used for pending outbound accounting.
func (c *Client) getSettledAccount(pubKey common.PubKey, snapshot chainLockSnapshot) (common.Account, error) {
	var account common.Account
	address, err := pubKey.GetAddress(c.GetChain())
	if err != nil {
		return account, err
	}
	outputs, err := c.rpc.ListUnspent(address.String())
	if err != nil {
		return account, err
	}
	total := cosmos.ZeroUint()
	found := make(map[string]bool)
	for _, output := range outputs {
		if !c.isValidUTXO(output.ScriptPubKey) {
			continue
		}
		key := fmt.Sprintf("%s:%d", strings.ToLower(output.TxID), output.Vout)
		if found[key] {
			return account, errors.New("duplicate unspent output")
		}
		found[key] = true
		if output.Confirmations < 0 || output.Confirmations > snapshot.tipHeight+1 {
			return account, errors.New("invalid output confirmations")
		}
		settled := output.Confirmations > 0 && snapshot.tipHeight-output.Confirmations+1 <= snapshot.lockHeight
		if !settled && !c.isSelfTransaction(output.TxID) && !c.isFromAsgard(output.TxID) {
			continue
		}
		amount, amountErr := btcutil.NewAmount(output.Amount)
		if amountErr != nil || amount < 0 {
			return account, errors.New("invalid output amount")
		}
		total = total.Add(cosmos.NewUint(uint64(amount)))
	}
	return common.NewAccount(0, 0, common.NewCoins(common.NewCoin(c.GetChain().GetGasAsset(), total)), false), nil
}

type chainLockSnapshot struct {
	tipHeight, lockHeight int64
	tipHash, lockHash     string
}

// getChainLockSnapshot validates local finality independently of scanner health.
func (c *Client) getChainLockSnapshot() (chainLockSnapshot, error) {
	var snapshot chainLockSnapshot
	lock, err := c.rpc.GetBestChainLock()
	if err != nil {
		return snapshot, fmt.Errorf("fail to get ChainLock: %w", err)
	}
	if lock.Height < 0 || !lock.KnownBlock {
		return snapshot, errors.New("ChainLock is unavailable")
	}
	snapshot.tipHeight, err = c.rpc.GetBlockCount()
	if err != nil {
		return snapshot, fmt.Errorf("fail to get block height: %w", err)
	}
	if snapshot.tipHeight < lock.Height {
		return snapshot, fmt.Errorf("best block height is below the ChainLock height")
	}
	snapshot.lockHash, err = c.rpc.GetBlockHash(lock.Height)
	if err != nil {
		return snapshot, fmt.Errorf("fail to get ChainLock block hash: %w", err)
	}
	if !strings.EqualFold(snapshot.lockHash, lock.BlockHash) {
		return snapshot, fmt.Errorf("best ChainLock does not match the active chain")
	}
	snapshot.lockHeight = lock.Height
	snapshot.tipHash = snapshot.lockHash
	if snapshot.tipHeight != snapshot.lockHeight {
		snapshot.tipHash, err = c.rpc.GetBlockHash(snapshot.tipHeight)
		if err != nil {
			return snapshot, fmt.Errorf("fail to get tip hash: %w", err)
		}
	}
	snapshot.tipHash = strings.ToLower(snapshot.tipHash)
	snapshot.lockHash = strings.ToLower(snapshot.lockHash)
	return snapshot, nil
}

// chainLockHeightHealth is immutable once published; the scanner and signer read it
// concurrently through Client.chainLockHeightHealth.
type chainLockHeightHealth struct {
	tipHeight       int64
	chainLockHeight int64
	checkedAt       time.Time
}

// isHealthy reports whether height data is fresh and both ChainLock and scanner
// lag are within one expected block interval.
func (h *chainLockHeightHealth) isHealthy(chain common.Chain, now time.Time, scannedHeight int64) bool {
	maxAge := time.Duration(maxHealthyChainLockLag*chain.ApproximateBlockMilliseconds()) * time.Millisecond
	if h == nil || h.checkedAt.IsZero() || now.Sub(h.checkedAt) >= maxAge {
		return false
	}
	// Check finality even when the scanner is caught up to a frozen ChainLock.
	// Also bound the unscanned backlog so a new ChainLock cannot immediately
	// restore signing using the scanner's old healthy flag before catch-up.
	return h.tipHeight >= h.chainLockHeight &&
		h.tipHeight-h.chainLockHeight <= maxHealthyChainLockLag &&
		h.tipHeight-scannedHeight <= maxHealthyChainLockLag
}
