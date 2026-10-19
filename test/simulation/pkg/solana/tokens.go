package solana

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
)

////////////////////////////////////////////////////////////////////////////////////////
// Mocknet SPL token
////////////////////////////////////////////////////////////////////////////////////////

// The mocknet validator boots with one classic SPL Token mint, preloaded into genesis
// from build/docker/mocknet/solana/spl (see MocknetSPLAccounts). The simulation
// master holds the whole supply in its associated token account and funds users
// from it. Bifrost whitelists the mint through the mocknet SOL token list and
// BIFROST_CHAINS_SOL_BLOCK_SCANNER_WHITELIST_TOKENS.
const (
	// MocknetMasterAddress is the simulation master's SOL address (the validator
	// airdrop target, AIRDROP_ADDRESS in docker-compose).
	MocknetMasterAddress = "AAH1XhfycX678et7Pjvwp1DMWnHpSP92wpJwV19MXHtX"

	// mocknetTKNSeed derives the mint address from the master:
	// create_with_seed(master, seed, SPL Token program).
	mocknetTKNSeed = "thorchain-mocknet-tkn"

	// MocknetTKNDecimals is below THORChain's 8, so outbounds exercise truncation.
	MocknetTKNDecimals = 6

	// MocknetTKNSupply is the whole supply, in base units (1,000,000,000 TKN).
	MocknetTKNSupply = uint64(1_000_000_000_000_000)

	// Rent-exempt minimums of a mint (82 bytes) and a token account (165 bytes).
	mintAccountRentLamports  = uint64(1_461_600)
	tokenAccountRentLamports = uint64(2_039_280)
)

// MocknetTKNMint returns the mocknet mint address.
func MocknetTKNMint() stypes.PublicKey {
	mint, err := stypes.CreateWithSeed(stypes.MustPublicKeyFromString(MocknetMasterAddress), mocknetTKNSeed, stypes.TokenProgramID)
	if err != nil {
		panic(fmt.Sprintf("fail to derive mocknet SPL mint: %s", err))
	}
	return mint
}

// Tokens returns the SPL tokens used in the simulation, keyed by asset. Every token
// is included in GetAccount responses and can be sent with SignTx.
func Tokens() map[common.Asset]tokenlist.ERC20Token {
	token := tokenlist.ERC20Token{
		Address:  MocknetTKNMint().String(),
		Symbol:   "TKN",
		Name:     "Mocknet Token",
		Decimals: MocknetTKNDecimals,
	}
	return map[common.Asset]tokenlist.ERC20Token{token.Asset(common.SOLChain): token}
}

// tokenByAsset returns the simulation token for an asset.
func tokenByAsset(asset common.Asset) (tokenlist.ERC20Token, stypes.PublicKey, bool) {
	for a, token := range Tokens() {
		if a.Equals(asset) {
			mint, err := stypes.PublicKeyFromStringStrict(token.Address)
			if err != nil {
				return tokenlist.ERC20Token{}, stypes.PublicKey{}, false
			}
			return token, mint, true
		}
	}
	return tokenlist.ERC20Token{}, stypes.PublicKey{}, false
}

////////////////////////////////////////////////////////////////////////////////////////
// Genesis accounts
////////////////////////////////////////////////////////////////////////////////////////

// GenesisAccount is the `solana account --output json` shape that
// solana-test-validator --account loads into genesis.
type GenesisAccount struct {
	Pubkey  string `json:"pubkey"`
	Account struct {
		Lamports   uint64    `json:"lamports"`
		Data       [2]string `json:"data"`
		Owner      string    `json:"owner"`
		Executable bool      `json:"executable"`
		RentEpoch  uint64    `json:"rentEpoch"`
		Space      int       `json:"space"`
	} `json:"account"`
}

func newGenesisAccount(pubkey stypes.PublicKey, lamports uint64, data []byte) GenesisAccount {
	var a GenesisAccount
	a.Pubkey = pubkey.String()
	a.Account.Lamports = lamports
	a.Account.Data = [2]string{base64.StdEncoding.EncodeToString(data), "base64"}
	a.Account.Owner = stypes.TokenProgramID.String()
	a.Account.RentEpoch = math.MaxUint64 // rent exempt
	a.Account.Space = len(data)
	return a
}

// mintAccountData is the 82-byte SPL Token Mint layout: mint_authority
// COption<Pubkey>, supply u64, decimals u8, is_initialized bool, freeze_authority
// COption<Pubkey>.
func mintAccountData(authority stypes.PublicKey, supply uint64, decimals uint8) []byte {
	data := make([]byte, 0, 82)
	data = binary.LittleEndian.AppendUint32(data, 1) // Some(authority)
	data = append(data, authority[:]...)
	data = binary.LittleEndian.AppendUint64(data, supply)
	data = append(data, decimals, 1)                 // decimals, initialized
	data = binary.LittleEndian.AppendUint32(data, 0) // no freeze authority
	data = append(data, make([]byte, 32)...)
	return data
}

// tokenAccountData is the 165-byte SPL Token Account layout: mint, owner, amount
// u64, delegate COption<Pubkey>, state u8, is_native COption<u64>, delegated_amount
// u64, close_authority COption<Pubkey>.
func tokenAccountData(mint, owner stypes.PublicKey, amount uint64) []byte {
	data := make([]byte, 0, 165)
	data = append(data, mint[:]...)
	data = append(data, owner[:]...)
	data = binary.LittleEndian.AppendUint64(data, amount)
	data = binary.LittleEndian.AppendUint32(data, 0) // no delegate
	data = append(data, make([]byte, 32)...)
	data = append(data, 1)                           // initialized
	data = binary.LittleEndian.AppendUint32(data, 0) // not native
	data = binary.LittleEndian.AppendUint64(data, 0)
	data = binary.LittleEndian.AppendUint64(data, 0) // delegated amount
	data = binary.LittleEndian.AppendUint32(data, 0) // no close authority
	data = append(data, make([]byte, 32)...)
	return data
}

// MocknetSPLAccounts returns the genesis accounts the mocknet validator preloads,
// keyed by file name: the mint (authority: master) and the master's associated
// token account holding the whole supply.
func MocknetSPLAccounts() (map[string][]byte, error) {
	master := stypes.MustPublicKeyFromString(MocknetMasterAddress)
	mint := MocknetTKNMint()
	masterATA, err := stypes.FindAssociatedTokenAddress(master, mint)
	if err != nil {
		return nil, err
	}
	accounts := map[string]GenesisAccount{
		"tkn-mint.json":       newGenesisAccount(mint, mintAccountRentLamports, mintAccountData(master, MocknetTKNSupply, MocknetTKNDecimals)),
		"tkn-master-ata.json": newGenesisAccount(masterATA, tokenAccountRentLamports, tokenAccountData(mint, master, MocknetTKNSupply)),
	}
	out := make(map[string][]byte, len(accounts))
	for name, a := range accounts {
		b, err := json.MarshalIndent(a, "", "  ")
		if err != nil {
			return nil, err
		}
		out[name] = append(b, '\n')
	}
	return out, nil
}
