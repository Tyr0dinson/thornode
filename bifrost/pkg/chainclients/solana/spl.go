package solana

import (
	"fmt"
	"math/big"
	"sync/atomic"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
)

// splTokenAccountRentLamports is the rent-exempt minimum of a 165-byte SPL Token
// account, which the vault pays when an outbound creates the recipient's associated
// token account. Used only when the RPC cannot be asked; observed gas always comes
// from the vault's actual lamport change.
const splTokenAccountRentLamports = uint64(2_039_280)

// splTokenAccountSize is the byte length of a classic SPL Token account.
const splTokenAccountSize = 165

// splToken is a whitelisted SPL Token (classic program) mint.
type splToken struct {
	asset    common.Asset
	mint     types.PublicKey
	decimals int
}

// splRegistry maps the whitelisted SPL mints to their THORChain assets and holds
// THORChain's activation of them. Activation gates only token deposits from outside
// the vaults: a vault's own token transfers and its token balances of whitelisted
// mints are always observed and reported. Mints are case-sensitive base58 while asset
// symbols are uppercased, so outbounds resolve an asset to its mint through this
// registry, never by parsing the symbol. The client and scanner share one registry.
type splRegistry struct {
	tokens []splToken // config order, so every iteration is deterministic
	byMint map[string]splToken

	// activeFrom mirrors THORChain's SOLTokens mimir (constants.MimirKeySOLTokens): the
	// Solana slot from which token deposits from outside the vaults are observed, or 0
	// while they are off.
	activeFrom atomic.Uint64
}

// vaultTokenAccount is a vault's associated token account for one whitelisted mint.
type vaultTokenAccount struct {
	address string
	token   splToken
}

// newSPLRegistry builds the registry from the configured whitelist. Each entry must
// be an exact (case-sensitive) mint from the SOL token list; anything else is
// skipped with an error log, as the TRON client does, so a config typo cannot stop
// the SOL client from starting.
func newSPLRegistry(chain common.Chain, whitelist []string, logger zerolog.Logger) *splRegistry {
	return newSPLRegistryFromTokens(chain, whitelist, tokenlist.GetSOLTokenList().Tokens, logger)
}

// newSPLRegistryFromTokens is newSPLRegistry against an explicit token list.
func newSPLRegistryFromTokens(chain common.Chain, whitelist []string, tokens []tokenlist.ERC20Token, logger zerolog.Logger) *splRegistry {
	r := &splRegistry{byMint: map[string]splToken{}}
	if len(whitelist) == 0 {
		return r
	}
	listed := map[string]tokenlist.ERC20Token{}
	for _, t := range tokens {
		listed[t.Address] = t
	}
	assets := map[string]bool{}
	for _, mintStr := range whitelist {
		t, ok := listed[mintStr]
		if !ok {
			logger.Error().Str("mint", mintStr).Msg("whitelisted SPL mint is not in the SOL token list, skipping")
			continue
		}
		mint, err := types.PublicKeyFromStringStrict(mintStr)
		if err != nil {
			logger.Error().Err(err).Str("mint", mintStr).Msg("invalid whitelisted SPL mint, skipping")
			continue
		}
		// THORChain reads 0 decimals as "unset" (cosmos.RoundToDecimal does not round),
		// so a 0-decimal mint's truncated outbounds would never match the scheduled
		// amount.
		if t.Decimals < 1 || t.Decimals > 18 {
			logger.Error().Str("mint", mintStr).Int("decimals", t.Decimals).Msg("unsupported SPL mint decimals, skipping")
			continue
		}
		asset := t.Asset(chain)
		if err = asset.Valid(); err != nil {
			logger.Error().Err(err).Str("mint", mintStr).Msg("invalid SPL token asset, skipping")
			continue
		}
		// Uppercasing could in principle map two mints to one symbol; never let one
		// asset resolve to two mints.
		if _, dup := r.byMint[mintStr]; dup || assets[asset.String()] {
			logger.Error().Str("mint", mintStr).Stringer("asset", asset).Msg("duplicate SPL token, skipping")
			continue
		}
		token := splToken{asset: asset, mint: mint, decimals: t.Decimals}
		r.tokens = append(r.tokens, token)
		r.byMint[mintStr] = token
		assets[asset.String()] = true
	}
	return r
}

// tokenByMint returns the whitelisted token for an exact base58 mint.
func (r *splRegistry) tokenByMint(mint string) (splToken, bool) {
	if r == nil {
		return splToken{}, false
	}
	t, ok := r.byMint[mint]
	return t, ok
}

// tokenByAsset returns the whitelisted token for a THORChain layer-1 asset.
func (r *splRegistry) tokenByAsset(asset common.Asset) (splToken, bool) {
	if r == nil || asset.IsSyntheticAsset() || asset.IsTradeAsset() || asset.IsSecuredAsset() {
		return splToken{}, false
	}
	for _, t := range r.tokens {
		if asset.Equals(t.asset) {
			return t, true
		}
	}
	return splToken{}, false
}

// activationSlot returns the Solana slot from which SPL tokens are active: vault
// token accounts are scanned from it, and token transfers from outside the vaults at
// or after it are observed. 0 means SPL is off. A vault's own token transfers and its
// token balances do not depend on it.
func (r *splRegistry) activationSlot() uint64 {
	if r == nil || len(r.tokens) == 0 {
		return 0
	}
	return r.activeFrom.Load()
}

// activeAt reports whether a token transfer from outside the vaults at slot is
// observed: SPL is active and the slot is not before the activation slot. A vault's
// own token transfer is observed regardless.
func (r *splRegistry) activeAt(slot int64) bool {
	from := r.activationSlot()
	return from > 0 && slot >= 0 && uint64(slot) >= from
}

// vaultTokenAccounts returns the vault's associated token account for every
// whitelisted mint, in whitelist order.
func (r *splRegistry) vaultTokenAccounts(vaultAddr string) ([]vaultTokenAccount, error) {
	if r == nil || len(r.tokens) == 0 {
		return nil, nil
	}
	vault, err := types.PublicKeyFromStringStrict(vaultAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid vault address %s: %w", vaultAddr, err)
	}
	out := make([]vaultTokenAccount, 0, len(r.tokens))
	for _, t := range r.tokens {
		ata, err := types.FindAssociatedTokenAddress(vault, t.mint)
		if err != nil {
			return nil, fmt.Errorf("fail to derive %s token account for %s: %w", t.asset, vaultAddr, err)
		}
		out = append(out, vaultTokenAccount{address: ata.String(), token: t})
	}
	return out, nil
}

// splToTHORChain converts a base-unit token amount to THORChain's 1e8 units.
func splToTHORChain(amount *big.Int, decimals int) (cosmos.Uint, error) {
	v, err := common.ConvertDecimals(amount, decimals, common.THORChainDecimals)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return cosmos.NewUintFromBigInt(v), nil
}

// splFromTHORChain converts a THORChain 1e8 amount to the mint's base units,
// truncating precision the mint cannot express.
func splFromTHORChain(amount cosmos.Uint, decimals int) (uint64, error) {
	v, err := common.ConvertDecimals(amount.BigInt(), common.THORChainDecimals, decimals)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("token amount %s overflows u64", v)
	}
	return v.Uint64(), nil
}
