package blockscanner

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	btypes "gitlab.com/thorchain/thornode/bifrost/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/constants"
	ttypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

var (
	ErrVaultInsolvent = errors.New("vault insolvent")
)

type AccountAccessor func(height int64, pubKey common.PubKey) (common.Account, error)
type VaultBalanceStorageAccessor interface {
	GetVaultBalance(chain common.Chain, address common.Address) (btypes.VaultBalance, error)
	SetVaultBalance(bal btypes.VaultBalance) error
}

// SolvencyChecker is client side solvency checker ,which is doing a book keeping on the chain client level
// when the chain client detect it is not solvent , it should stop observing
type SolvencyChecker struct {
	logger          zerolog.Logger
	bridge          *thorclient.ThorchainBridge
	chain           common.Chain
	storage         VaultBalanceStorageAccessor
	accountAccessor AccountAccessor
	lock            *sync.Mutex
}

// NewSolvencyChecker create a new instance of SolvencyChecker
func NewSolvencyChecker(bridge *thorclient.ThorchainBridge,
	chain common.Chain,
	storage VaultBalanceStorageAccessor,
	accountAccessor AccountAccessor) (*SolvencyChecker, error) {
	if bridge == nil {
		return nil, fmt.Errorf("thorchain bridge is nil")
	}
	return &SolvencyChecker{
		chain:           chain,
		logger:          log.Logger.With().Str("module", "solvency_checker").Str("chain", chain.String()).Logger(),
		bridge:          bridge,
		storage:         storage,
		accountAccessor: accountAccessor,
		lock:            &sync.Mutex{},
	}, nil
}

// UpdateMemPool this method is mostly used by UTXO related block chains , like BTC/LTC/BCH , as UTXO based block chain
// bifrost is going to scan mempool , UTXO will be marked as spent as soon as tx get broadcast , and will be observed in mempool first
// because the block time could be longer
func (sc *SolvencyChecker) UpdateMemPool(txs types.TxIn) error {
	if len(txs.TxArray) == 0 {
		return nil
	}
	// this method only deal with txs discovered in mempool
	if !txs.MemPool {
		return nil
	}
	asgards, err := sc.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards from thornode,err: %w", err)
	}
	result, err := sc.updateLocalVaultBalance(txs, asgards)
	if err != nil {
		return fmt.Errorf("fail to update local vault balance")
	}
	if result == nil {
		return nil
	}
	for _, item := range result {
		if err := sc.storage.SetVaultBalance(item); err != nil {
			return fmt.Errorf("fail to save vault balance to key value store, err: %w", err)
		}
	}
	return nil
}

func (sc *SolvencyChecker) updateLocalVaultBalance(txs types.TxIn, asgards ttypes.Vaults) (map[common.Address]btypes.VaultBalance, error) {
	if len(txs.TxArray) == 0 {
		return nil, nil
	}
	sc.lock.Lock()
	defer sc.lock.Unlock()

	inMemoryVaultBalance := make(map[common.Address]btypes.VaultBalance)
	for _, tx := range txs.TxArray {
		sc.logger.Info().Msgf("tx detail: %+v", tx)
		isFromAsgard := false
		// asgard outbound transactions
		if sc.isAsgardTxs(asgards, tx.Sender) {
			fromAddr, err := common.NewAddress(strings.ToLower(tx.Sender))
			if err != nil {
				sc.logger.Err(err).Msgf("fail to parse from address,from: %s", strings.ToLower(tx.Sender))
				continue
			}
			vaultBalance, ok := inMemoryVaultBalance[fromAddr]
			if !ok {
				vaultBalance, err = sc.storage.GetVaultBalance(sc.chain, fromAddr)
				if err != nil {
					sc.logger.Err(err).Msg("fail to get vault balance")
					continue
				}
			}

			if vaultBalance.HasTxID(common.TxID(tx.Tx)) {
				continue
			}

			vaultBalance.Coins = vaultBalance.Coins.Subs(tx.Coins)
			// deduct gas as well , for outbound
			vaultBalance.Coins = vaultBalance.Coins.Subs(tx.Gas.ToCoins())
			vaultBalance.AddTxID(common.TxID(tx.Tx))
			inMemoryVaultBalance[fromAddr] = vaultBalance
			isFromAsgard = true
		}

		// asgard internal transaction , migration or consolidation
		// inbound self transaction can be credit to vault
		if (isFromAsgard || !txs.MemPool) && sc.isAsgardTxs(asgards, tx.To) {
			toAddr, err := common.NewAddress(strings.ToLower(tx.To))
			if err != nil {
				sc.logger.Err(err).Msgf("fail to parse to address,to: %s", strings.ToLower(tx.To))
				continue
			}
			vaultBalance, ok := inMemoryVaultBalance[toAddr]
			if !ok {
				vaultBalance, err = sc.storage.GetVaultBalance(sc.chain, toAddr)
				if err != nil {
					sc.logger.Err(err).Msg("fail to get vault balance")
					continue
				}
			}
			// for consolidation , both sender & to address is the same asgard address
			// the amount has been deducted above, here need to add it back
			if tx.Sender != tx.To && vaultBalance.HasTxID(common.TxID(tx.Tx)) {
				continue
			}
			vaultBalance.Coins = vaultBalance.Coins.Adds(tx.Coins)
			vaultBalance.AddTxID(common.TxID(tx.Tx))
			inMemoryVaultBalance[toAddr] = vaultBalance
		}
	}
	return inMemoryVaultBalance, nil
}
func (sc *SolvencyChecker) getPermittedSolvencyGap() int64 {
	const defaultPermittedSolvencyGap = 100 // default one 100 basis points
	permittedGapBasisPoints, err := sc.bridge.GetMimir(constants.PermittedSolvencyGap.String())
	if err != nil {
		sc.logger.Err(err).Msgf("fail to get mimir %s", constants.PermittedSolvencyGap)
		constantValues, err := sc.bridge.GetConstants()
		if err != nil {
			sc.logger.Err(err).Msg("fail to get constants")
			return defaultPermittedSolvencyGap
		}
		constValue, ok := constantValues[constants.PermittedSolvencyGap.String()]
		if !ok {
			return defaultPermittedSolvencyGap
		}
		permittedGapBasisPoints = constValue
	}
	return permittedGapBasisPoints
}

// CheckSolvency process transactions that have been committed to blocks
// when vault is not solvent , it will return an errVaultInsolvent
func (sc *SolvencyChecker) CheckSolvency(txs types.TxIn) error {
	if len(txs.TxArray) == 0 {
		return nil
	}
	// if txs are in mempool should not call this method
	if txs.MemPool {
		return nil
	}
	asgards, err := sc.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards from thornode,err: %w", err)
	}
	inMemoryVaultBalance, err := sc.updateLocalVaultBalance(txs, asgards)
	if err != nil {
		return fmt.Errorf("fail to update local vault balance,err: %w", err)
	}
	permittedGap := sc.getPermittedSolvencyGap()
	for _, v := range asgards {
		account, err := sc.accountAccessor(txs.TxArray[0].BlockHeight, v.PubKey)
		if err != nil {
			sc.logger.Err(err).Msgf("fail to get account balance , pubkey: %s", v.PubKey)
			continue
		}

		addr, err := v.PubKey.GetAddress(sc.chain)
		if err != nil {
			sc.logger.Err(err).Msgf("fail to get address for pubkey: %s", v.PubKey)
			continue
		}
		vaultBalance, ok := inMemoryVaultBalance[addr]
		if !ok {
			vaultBalance, err = sc.storage.GetVaultBalance(sc.chain, addr)
			if err != nil {
				sc.logger.Err(err).Msgf("fail to get vault balance,address: %s", addr)
				continue
			}
		}
		sc.logger.Info().Msgf("account: %+v", account)
		sc.logger.Info().Msgf("local vault balance: %+v", vaultBalance)

		for _, walletCoin := range account.Coins {
			// if an asset exist in wallet , but doesn't exist in local vault balance , that should be ok
			// if an asset exist in wallet, and exist in local vault balance , but local has more than wallet, which means there might be fake transactions
			exist := false
			for _, localCoin := range vaultBalance.Coins {
				if !localCoin.Asset.Equals(walletCoin.Asset) {
					continue
				}
				exist = true
				gap := common.SafeSub(localCoin.Amount, walletCoin.Amount)
				permittedSolvencyGap := localCoin.Amount.MulUint64(uint64(permittedGap)).QuoUint64(10000)
				if gap.GT(permittedSolvencyGap) {
					return fmt.Errorf("insolvent, asset: %s, local: %s , wallet: %s, err: %w", walletCoin.Asset, localCoin.Amount, walletCoin.Amount, ErrVaultInsolvent)
				}
			}
			if !exist && !walletCoin.Amount.IsZero() {
				sc.logger.Info().Msgf("asset: %s, exist in wallet ,amount: %s , but not in local vault balance", walletCoin.Asset, walletCoin.Amount.String())
			}
		}
	}

	// Save the local vault balance to
	for _, item := range inMemoryVaultBalance {
		if err := sc.storage.SetVaultBalance(item); err != nil {
			sc.logger.Err(err).Msg("fail to save vault balance to key value store")
		}
	}
	return nil
}

func (sc *SolvencyChecker) isAsgardTxs(asgards ttypes.Vaults, address string) bool {
	for _, v := range asgards {
		asgardAddr, err := v.PubKey.GetAddress(sc.chain)
		if err != nil {
			sc.logger.Err(err).Msgf("fail to get address for chain %s, pubkey: %s", sc.chain, v.PubKey)
			continue
		}
		if strings.EqualFold(asgardAddr.String(), address) {
			return true
		}
	}
	return false
}

// RestoreVaultBalanceFromAsgard when bifrost restart, or bifrost stopped for a while and start up again
// it will jump start from where the network's last observation height.  In that case , quite some transactions have been skipped
// by local node , thus it need to trust the balance in Asgard
func (sc *SolvencyChecker) RestoreVaultBalanceFromAsgard() error {
	sc.lock.Lock()
	defer sc.lock.Unlock()
	sc.logger.Info().Msg("block scanner jump start , restore vault balance from asgard")
	asgards, err := sc.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards from thornode,err: %w", err)
	}
	for _, vault := range asgards {
		addr, err := vault.PubKey.GetAddress(sc.chain)
		if err != nil {
			sc.logger.Err(err).Msgf("fail to get  address for pub key: %s", vault.PubKey)
			continue
		}
		vaultBalance, err := sc.storage.GetVaultBalance(sc.chain, addr)
		if err != nil {
			sc.logger.Err(err).Msg("fail to get vault balance from key value store")
			continue
		}
		// override the vault balance coins locally with those in asgard
		vaultBalance.Coins = vault.Coins
		if err := sc.storage.SetVaultBalance(vaultBalance); err != nil {
			sc.logger.Err(err).Msg("fail to save value balance to local key value store")
			continue
		}
	}
	return nil
}
