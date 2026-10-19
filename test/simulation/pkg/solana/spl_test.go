package solana

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	btypes "github.com/blocto/solana-go-sdk/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func mocknetTKNAsset(t *testing.T) common.Asset {
	t.Helper()
	for asset := range Tokens() {
		return asset
	}
	t.Fatal("no simulation SPL token")
	return common.EmptyAsset
}

// TestGetAccountReportsTokenBalance reads the token balance from the owner's
// associated token account and converts it to 1e8 units.
func TestGetAccountReportsTokenBalance(t *testing.T) {
	owner := stypes.MustPublicKeyFromString(MocknetMasterAddress)
	ata, err := stypes.FindAssociatedTokenAddress(owner, MocknetTKNMint())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch req.Method {
		case "getBalance":
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"value":1000000000}}`)
		case "getTokenAccountsByOwner":
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"value":[]}}`)
		case "getAccountInfo":
			var addr string
			_ = json.Unmarshal(req.Params[0], &addr)
			if addr != ata.String() {
				t.Errorf("token balance read from %s, want the associated token account %s", addr, ata)
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"owner":%q,"lamports":2039280,"data":{"program":"spl-token","parsed":{"type":"account","info":{"mint":%q,"owner":%q,"state":"initialized","tokenAmount":{"amount":"12345678","decimals":6}}}}}}}`,
				stypes.TokenProgramID, MocknetTKNMint(), MocknetMasterAddress)
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := &Client{chain: common.SOLChain, rpc: rpc.NewSolRPC(server.URL, time.Second), address: common.Address(MocknetMasterAddress)}
	account, err := client.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := account.Coins.GetCoin(mocknetTKNAsset(t)).Amount.String(); got != "1234567800" {
		t.Fatalf("token balance %s, want 1234567800", got)
	}
	if got := account.Coins.GetCoin(common.SOLAsset).Amount.String(); got != "100000000" {
		t.Fatalf("SOL balance %s, want 100000000", got)
	}
}

// TestSignTxBuildsTokenTransfer signs an SPL deposit the way wallets send one:
// CreateIdempotent for the recipient's associated token account, then
// TransferChecked from the sender's, with the memo.
func TestSignTxBuildsTokenTransfer(t *testing.T) {
	const blockhash = "EkSnNWid2cvwEVnVx9aBqawnmiCNiDgp3gUdkDPTKN1N"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"blockhash":%q,"lastValidBlockHeight":1}}}`, blockhash)
	}))
	defer server.Close()

	signer := btypes.NewAccount()
	from := stypes.PublicKeyFromBytes(signer.PublicKey.Bytes())
	vault := stypes.MustPublicKeyFromString("BEcrPLugbAY1zEJGUNYgzcsxMi72rPeVwG6qKm96LK5g")
	client := &Client{
		chain:   common.SOLChain,
		rpc:     rpc.NewSolRPC(server.URL, time.Second),
		signer:  signer,
		address: common.Address(from.String()),
	}

	tx := SimTx{
		Chain:     common.SOLChain,
		ToAddress: common.Address(vault.String()),
		Coin:      common.NewCoin(mocknetTKNAsset(t), cosmos.NewUint(250_000_099)), // 2.50000099 TKN
		Memo:      "=:BTC.BTC:bcrt1qtest",
	}
	signed, err := client.SignTx(tx)
	if err != nil {
		t.Fatal(err)
	}

	// [1 sig][64-byte sig][message]
	if len(signed) < 65 || signed[0] != 1 {
		t.Fatalf("unexpected signed tx prefix")
	}
	want := stypes.NewSPLTransferTransaction(stypes.SPLTransferParams{
		Vault:     from,
		Recipient: vault,
		Mint:      MocknetTKNMint(),
		SourceATA: mustATA(t, from),
		DestATA:   mustATA(t, vault),
		Amount:    2_500_000, // truncated to 6 decimals
		Decimals:  MocknetTKNDecimals,
		Memo:      tx.Memo,
	}, blockhash)
	message, err := want.Message.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if string(signed[65:]) != string(message) {
		t.Fatal("signed message is not the expected SPL transfer")
	}
	if !ed25519.Verify(from.Bytes(), message, signed[1:65]) {
		t.Fatal("invalid signature")
	}

	var transfer stypes.CompiledInstruction
	for _, ins := range want.Message.Instructions {
		if want.Message.Accounts[ins.ProgramIDIndex] == stypes.TokenProgramID {
			transfer = ins
		}
	}
	if binary.LittleEndian.Uint64(transfer.Data[1:9]) != 2_500_000 || transfer.Data[9] != MocknetTKNDecimals {
		t.Fatal("unexpected TransferChecked amount or decimals")
	}

	// Below the mint's precision there is nothing to send.
	tx.Coin = common.NewCoin(mocknetTKNAsset(t), cosmos.NewUint(99))
	if _, err = client.SignTx(tx); err == nil {
		t.Fatal("expected an error for a sub-precision token amount")
	}
	// A token the simulation does not know.
	unknown, err := common.NewAsset("SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V")
	if err != nil {
		t.Fatal(err)
	}
	tx.Coin = common.NewCoin(unknown, cosmos.NewUint(100_000_000))
	if _, err = client.SignTx(tx); err == nil {
		t.Fatal("expected an error for an unknown token")
	}
}

func mustATA(t *testing.T, owner stypes.PublicKey) stypes.PublicKey {
	t.Helper()
	ata, err := stypes.FindAssociatedTokenAddress(owner, MocknetTKNMint())
	if err != nil {
		t.Fatal(err)
	}
	return ata
}
