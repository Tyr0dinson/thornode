package solana

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
)

// repoRoot is the thornode checkout root, relative to this package.
const repoRoot = "../../../.."

var mocknetSPLDir = filepath.Join(repoRoot, "build/docker/mocknet/solana/spl")

// TestMocknetSPLAccountsMatchCommittedFiles keeps the validator's genesis accounts in
// sync with MocknetSPLAccounts. Regenerate them with
//
//	UPDATE_SOLANA_MOCKNET_SPL=1 go test ./pkg/solana -run TestMocknetSPLAccountsMatchCommittedFiles
func TestMocknetSPLAccountsMatchCommittedFiles(t *testing.T) {
	accounts, err := MocknetSPLAccounts()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range accounts {
		path := filepath.Join(mocknetSPLDir, name)
		if os.Getenv("UPDATE_SOLANA_MOCKNET_SPL") != "" {
			if err = os.WriteFile(path, want, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (regenerate with UPDATE_SOLANA_MOCKNET_SPL=1)", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s is stale; regenerate with UPDATE_SOLANA_MOCKNET_SPL=1", path)
		}
	}
}

func decodeGenesisAccount(t *testing.T, raw []byte) (GenesisAccount, []byte) {
	t.Helper()
	var a GenesisAccount
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	if a.Account.Data[1] != "base64" {
		t.Fatalf("unexpected data encoding %q", a.Account.Data[1])
	}
	data, err := base64.StdEncoding.DecodeString(a.Account.Data[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.Account.Owner != stypes.TokenProgramID.String() || a.Account.Executable || a.Account.Space != len(data) {
		t.Fatalf("unexpected account metadata %+v", a.Account)
	}
	return a, data
}

// TestMocknetSPLAccountLayout decodes the genesis accounts field by field against
// the SPL Token program's Mint and Account layouts.
func TestMocknetSPLAccountLayout(t *testing.T) {
	accounts, err := MocknetSPLAccounts()
	if err != nil {
		t.Fatal(err)
	}
	master := stypes.MustPublicKeyFromString(MocknetMasterAddress)
	mint := MocknetTKNMint()

	mintAcct, data := decodeGenesisAccount(t, accounts["tkn-mint.json"])
	if mintAcct.Pubkey != mint.String() || len(data) != 82 || mintAcct.Account.Lamports != mintAccountRentLamports {
		t.Fatalf("unexpected mint account %s len %d lamports %d", mintAcct.Pubkey, len(data), mintAcct.Account.Lamports)
	}
	if binary.LittleEndian.Uint32(data[0:4]) != 1 || stypes.PublicKeyFromBytes(data[4:36]) != master {
		t.Fatal("mint authority is not the master")
	}
	if binary.LittleEndian.Uint64(data[36:44]) != MocknetTKNSupply || data[44] != MocknetTKNDecimals || data[45] != 1 {
		t.Fatal("unexpected mint supply, decimals or initialization")
	}
	if binary.LittleEndian.Uint32(data[46:50]) != 0 {
		t.Fatal("mint has a freeze authority")
	}

	ataAcct, data := decodeGenesisAccount(t, accounts["tkn-master-ata.json"])
	wantATA, err := stypes.FindAssociatedTokenAddress(master, mint)
	if err != nil {
		t.Fatal(err)
	}
	if ataAcct.Pubkey != wantATA.String() || len(data) != 165 || ataAcct.Account.Lamports != tokenAccountRentLamports {
		t.Fatalf("unexpected token account %s len %d lamports %d", ataAcct.Pubkey, len(data), ataAcct.Account.Lamports)
	}
	if stypes.PublicKeyFromBytes(data[0:32]) != mint || stypes.PublicKeyFromBytes(data[32:64]) != master {
		t.Fatal("token account mint or owner mismatch")
	}
	if binary.LittleEndian.Uint64(data[64:72]) != MocknetTKNSupply || data[108] != 1 {
		t.Fatal("token account amount or state mismatch")
	}
}

// TestMocknetSPLConfigInSync checks every place the mocknet mint must appear: the
// mocknet SOL token list Bifrost embeds, the Bifrost whitelist in docker-compose,
// and the validator entrypoint's --account flags; and that mocknet activates SPL
// from slot 1 through the SOLTokens mimir.
func TestMocknetSPLConfigInSync(t *testing.T) {
	mint := MocknetTKNMint().String()
	masterATA, err := stypes.FindAssociatedTokenAddress(stypes.MustPublicKeyFromString(MocknetMasterAddress), MocknetTKNMint())
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(repoRoot, "common/tokenlist/soltokens/sol_mocknet_latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list tokenlist.EVMTokenList
	if err = json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for asset, token := range Tokens() {
		for _, listed := range list.Tokens {
			if listed.Address == token.Address && listed.Decimals == token.Decimals && listed.Asset(common.SOLChain).Equals(asset) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("mocknet SOL token list does not list %s", mint)
	}

	compose, err := os.ReadFile(filepath.Join(repoRoot, "build/docker/docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), `BIFROST_CHAINS_SOL_BLOCK_SCANNER_WHITELIST_TOKENS: "`+mint+`"`) {
		t.Fatalf("docker-compose does not whitelist %s for bifrost", mint)
	}
	if !strings.Contains(string(compose), `AIRDROP_ADDRESS: "`+MocknetMasterAddress+`"`) {
		t.Fatal("docker-compose solana airdrop address is not the simulation master")
	}
	// Without the SOLTokens mimir THORChain no-ops SPL deposits and Bifrost does not
	// scan token accounts, so every SPL simulation flow would silently do nothing.
	if !strings.Contains(string(compose), "SOL_TOKENS: 1") {
		t.Fatal("docker-compose does not enable the SOLTokens mimir (SOL_TOKENS) on thornode")
	}

	entrypoint, err := os.ReadFile(filepath.Join(repoRoot, "build/docker/mocknet/solana/entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--account " + mint + " /mocknet/spl/tkn-mint.json",
		"--account " + masterATA.String() + " /mocknet/spl/tkn-master-ata.json",
	} {
		if !strings.Contains(string(entrypoint), want) {
			t.Fatalf("entrypoint.sh is missing %q", want)
		}
	}
}
