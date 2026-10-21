package solana

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const testRecipient = "D9A6eE2pZ6oSiGb8BPkag4gvAeEhHvc3eEYPBoLSMshG"

func usdcAsset(c *C) common.Asset {
	a, err := common.NewAsset(testUSDCAssetStr)
	c.Assert(err, IsNil)
	return a
}

func splTestClient() *SOLClient {
	return &SOLClient{
		logger: log.Logger,
		spl:    testSPLRegistry(testUSDCMint),
	}
}

func splTestTxOut(c *C, vault common.PubKey, amount uint64) stypes.TxOutItem {
	return stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        testRecipient,
		VaultPubKey:      vault,
		VaultPubKeyEddsa: vault,
		Coins:            common.Coins{common.NewCoin(usdcAsset(c), cosmos.NewUint(amount))},
		MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(15000))},
		GasRate:          1,
		Memo:             "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
	}
}

// tokenAcct is a jsonParsed getAccountInfo value for an SPL token account.
func tokenAcct(mint, owner, amount, state string) string {
	return fmt.Sprintf(`{"owner":%q,"executable":false,"lamports":2039280,"space":165,"data":{"program":"spl-token","parsed":{"type":"account","info":{"mint":%q,"owner":%q,"state":%q,"tokenAmount":{"amount":%q,"decimals":6}}}}}`,
		testTokenProgram, mint, owner, state, amount)
}

// transferChecked finds the TransferChecked instruction of a built transaction and
// returns it with its amount and decimals.
func transferChecked(c *C, tx types.Transaction) (types.CompiledInstruction, uint64, uint8) {
	m := tx.Message
	for _, ins := range m.Instructions {
		if m.Accounts[ins.ProgramIDIndex] == types.TokenProgramID {
			c.Assert(ins.Data, HasLen, 10)
			c.Assert(ins.Data[0], Equals, byte(types.TokenInstructionTransferChecked))
			return ins, binary.LittleEndian.Uint64(ins.Data[1:9]), ins.Data[9]
		}
	}
	c.Fatal("no TransferChecked instruction")
	return types.CompiledInstruction{}, 0, 0
}

func (s *SolTestSuite) TestSPLCreateTx(c *C) {
	cl := splTestClient()
	vault := types2.GetRandomEd25519PubKey()
	tx := splTestTxOut(c, vault, 1_234_567_890) // 12.3456789 USDC; 6 decimals keep 12.345678
	blockhash := mockBlockhashForSlot(950)

	solTx, err := cl.CreateTx(tx, tx.Memo, blockhash)
	c.Assert(err, IsNil)
	m := solTx.Message
	c.Check(m.RecentBlockHash, Equals, blockhash)

	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	c.Check(m.Accounts[0].String(), Equals, vaultAddr.String())
	ins, amount, decimals := transferChecked(c, solTx)
	c.Check(amount, Equals, uint64(12_345_678))
	c.Check(decimals, Equals, uint8(6))
	c.Check(m.Accounts[ins.Accounts[0]].String(), Equals, ata(c, vaultAddr.String(), testUSDCMint))
	c.Check(m.Accounts[ins.Accounts[1]].String(), Equals, testUSDCMint)
	c.Check(m.Accounts[ins.Accounts[2]].String(), Equals, ata(c, testRecipient, testUSDCMint))
	c.Check(ins.Accounts[3], Equals, 0)

	// The recipient token account is created idempotently, paid by the vault.
	var created bool
	for _, i := range m.Instructions {
		if m.Accounts[i.ProgramIDIndex] == types.AssociatedTokenProgramID {
			created = true
			c.Check(i.Data, DeepEquals, []byte{types.AssociatedTokenInstructionCreateIdempotent})
			c.Check(i.Accounts[0], Equals, 0)
			c.Check(m.Accounts[i.Accounts[2]].String(), Equals, testRecipient)
		}
	}
	c.Check(created, Equals, true)

	// Deterministic across keysign parties.
	again, err := cl.CreateTx(tx, tx.Memo, blockhash)
	c.Assert(err, IsNil)
	a, err := solTx.Message.Serialize()
	c.Assert(err, IsNil)
	b, err := again.Message.Serialize()
	c.Assert(err, IsNil)
	c.Check(a, DeepEquals, b)
}

func (s *SolTestSuite) TestSPLCreateTxErrors(c *C) {
	cl := splTestClient()
	vault := types2.GetRandomEd25519PubKey()
	blockhash := mockBlockhashForSlot(950)

	// Below the mint's precision.
	tx := splTestTxOut(c, vault, 1)
	_, err := cl.CreateTx(tx, tx.Memo, blockhash)
	c.Check(err, ErrorMatches, ".*token value is zero.*")

	// A token this node has not whitelisted: the asset guard (canSendAsset) refuses it
	// before the SPL builder runs.
	usdt, err := common.NewAsset("SOL.USDT-ES9VMFRZACERMJFRF4H2FYD4KCONKY11MCCE8BENWNYB")
	c.Assert(err, IsNil)
	tx = splTestTxOut(c, vault, 100_000_000)
	tx.Coins = common.Coins{common.NewCoin(usdt, cosmos.NewUint(100_000_000))}
	_, err = cl.CreateTx(tx, tx.Memo, blockhash)
	c.Check(err, ErrorMatches, ".*unsupported outbound asset.*")

	// No whitelist at all (a SOL-only client).
	tx = splTestTxOut(c, vault, 100_000_000)
	_, err = (&SOLClient{}).CreateTx(tx, tx.Memo, blockhash)
	c.Check(err, ErrorMatches, ".*unsupported outbound asset.*")
}

func (s *SolTestSuite) TestSPLCreateTxWithNonce(c *C) {
	cl := splTestClient()
	f := newDurableFixture(c)
	tx := splTestTxOut(c, f.vault, 250_000_000)
	nonceAcct, vaultPK, err := cl.committedNonceKeys(tx, f.committed)
	c.Assert(err, IsNil)

	solTx, err := cl.CreateTxWithNonce(tx, tx.Memo, f.committed.Nonce, nonceAcct, vaultPK)
	c.Assert(err, IsNil)
	m := solTx.Message
	c.Check(m.RecentBlockHash, Equals, f.committed.Nonce)
	c.Assert(m.Instructions, Not(HasLen), 0)
	first := m.Instructions[0]
	c.Check(m.Accounts[first.ProgramIDIndex], Equals, types.SystemProgramID)
	c.Check(binary.LittleEndian.Uint32(first.Data[:4]), Equals, uint32(solSysAdvanceNonce))
	c.Check(m.Accounts[first.Accounts[0]], Equals, nonceAcct)
	_, amount, _ := transferChecked(c, solTx)
	c.Check(amount, Equals, uint64(2_500_000))

	// The raw-tx parser used for broadcast-error recovery still reads the nonce.
	solTx.Signatures[0] = make([]byte, 64)
	raw, err := solTx.Serialize()
	c.Assert(err, IsNil)
	baked, ok := txBlockhashFromRawTx(raw)
	c.Assert(ok, Equals, true)
	c.Check(baked, Equals, f.committed.Nonce)
}

func (s *SolTestSuite) TestSPLGetAccountByAddress(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	usdcATA := ata(c, vaultAddr.String(), testUSDCMint)
	usdtATA := ata(c, vaultAddr.String(), testUSDTMint)

	failATA := false
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		c.Assert(json.Unmarshal(body, &r), IsNil)
		var addr string
		c.Assert(json.Unmarshal(r.Params[0], &addr), IsNil)
		switch r.Method {
		case "getBalance":
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":5000000000}}`))
		case "getAccountInfo":
			switch {
			case failATA:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
			case addr == usdcATA:
				_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":%s}}`, tokenAcct(testUSDCMint, vaultAddr.String(), "12345678", "initialized"))
			case addr == usdtATA:
				// a pre-funded plain account at the not-yet-created token account address
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"owner":"11111111111111111111111111111111","lamports":1,"data":["","base64"]}}}`))
			default:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":null}}`))
			}
		default:
			c.Fatalf("unexpected RPC method %q", r.Method)
		}
	}))
	defer srv.Close()

	cl := &SOLClient{
		logger:    log.Logger,
		rpcClient: newSolRPCForServer(srv),
		spl:       testSPLRegistry(testUSDCMint, testUSDTMint),
	}

	// Token balances are reported whether or not SPL is active: once THORChain holds
	// tokens, turning the SOLTokens mimir back off must not make the vaults look
	// insolvent in them.
	for _, activeFrom := range []uint64{0, 1} {
		cl.spl.activeFrom.Store(activeFrom)
		acct, err := cl.GetAccountByAddress(vaultAddr.String(), nil)
		c.Assert(err, IsNil)
		c.Assert(acct.Coins, HasLen, 3, Commentf("active from %d", activeFrom))
		c.Check(acct.Coins.GetCoin(usdcAsset(c)).Amount.String(), Equals, "1234567800")
	}

	acct, err := cl.GetAccountByAddress(vaultAddr.String(), nil)
	c.Assert(err, IsNil)
	c.Assert(acct.Coins, HasLen, 3)
	c.Check(acct.Coins.GetCoin(common.SOLAsset).Amount.String(), Equals, "500000000")
	usdc := acct.Coins.GetCoin(usdcAsset(c))
	c.Check(usdc.Amount.String(), Equals, "1234567800")
	c.Check(usdc.Decimals, Equals, int64(6))
	usdt, err := common.NewAsset("SOL.USDT-ES9VMFRZACERMJFRF4H2FYD4KCONKY11MCCE8BENWNYB")
	c.Assert(err, IsNil)
	// Reported, as zero.
	c.Check(acct.Coins[2].Asset.Equals(usdt), Equals, true)
	c.Check(acct.Coins[2].Amount.IsZero(), Equals, true)
	c.Check(acct.Coins[2].Decimals, Equals, int64(6))

	// A token balance that cannot be read fails the whole read: a vault reported
	// without one of its tokens would look insolvent in it.
	failATA = true
	_, err = cl.GetAccountByAddress(vaultAddr.String(), nil)
	c.Check(err, NotNil)
}

// splPresignOutcome signs a durable USDC outbound of 25 USDC against a scenario and
// reports whether it reached the keysign or was dropped by the pre-sign filters.
// The vault's token account holds 30 USDC unless setup overrides it.
func (s *SolTestSuite) splPresignOutcome(c *C, setup func(sc *solRPCScenario, vault, source, dest string)) bool {
	f := newDurableFixture(c)
	tx := splTestTxOut(c, f.vault, 2_500_000_000)
	vaultAddr, err := f.vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	source := ata(c, vaultAddr.String(), testUSDCMint)
	dest := ata(c, testRecipient, testUSDCMint)

	sc := presignScenario()
	sc.accounts = map[string]string{source: tokenAcct(testUSDCMint, vaultAddr.String(), "30000000", "initialized")}
	if setup != nil {
		setup(&sc, vaultAddr.String(), source, dest)
	}
	srv := sc.server(c)
	defer srv.Close()
	cl := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: f.committed}, srv)
	cl.spl = testSPLRegistry(testUSDCMint)
	cl.tssKeyManager = &mockSOLThorchainKeyManager{err: errors.New("keysign reached")}
	result, checkpoint, obs, err := cl.SignTx(tx, keysignHeight)
	if err != nil {
		c.Assert(err, ErrorMatches, ".*keysign reached.*")
		return true
	}
	c.Assert(result, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(obs, IsNil)
	return false
}

func (s *SolTestSuite) TestSPLPreSignFilter(c *C) {
	// Baseline: a funded vault token account, a missing recipient token account the
	// vault can afford to create, and a clean simulation.
	c.Check(s.splPresignOutcome(c, nil), Equals, true)

	// The recipient already has a token account.
	c.Check(s.splPresignOutcome(c, func(sc *solRPCScenario, _, _, dest string) {
		sc.accounts[dest] = tokenAcct(testUSDCMint, testRecipient, "0", "initialized")
	}), Equals, true)

	const fee, rent = uint64(5000), uint64(890_880) // the mock serves 890,880 for any rent query
	rejects := map[string]func(sc *solRPCScenario, vault, source, dest string){
		"vault token balance short": func(sc *solRPCScenario, vault, source, _ string) {
			sc.accounts[source] = tokenAcct(testUSDCMint, vault, "24999999", "initialized")
		},
		"vault token account missing": func(sc *solRPCScenario, _, source, _ string) {
			sc.accounts[source] = "null"
		},
		"vault token account frozen": func(sc *solRPCScenario, vault, source, _ string) {
			sc.accounts[source] = tokenAcct(testUSDCMint, vault, "30000000", "frozen")
		},
		"recipient token account frozen": func(sc *solRPCScenario, _, _, dest string) {
			sc.accounts[dest] = tokenAcct(testUSDCMint, testRecipient, "0", "frozen")
		},
		"vault cannot pay fee and rent": func(sc *solRPCScenario, vault, _, _ string) {
			sc.accounts[vault] = acct(system, false, fee+rent-1)
		},
		"vault left below rent-exempt": func(sc *solRPCScenario, vault, _, _ string) {
			sc.accounts[vault] = acct(system, false, fee+rent+100)
		},
		"simulation fails": func(sc *solRPCScenario, _, _, _ string) {
			sc.simErr = `{"InstructionError":[2,{"Custom":1}]}`
		},
	}
	for name, setup := range rejects {
		c.Check(s.splPresignOutcome(c, setup), Equals, false, Commentf(name))
	}

	// Exactly enough lamports for fee + rent (vault left empty) passes.
	c.Check(s.splPresignOutcome(c, func(sc *solRPCScenario, vault, _, _ string) {
		sc.accounts[vault] = acct(system, false, fee+rent)
	}), Equals, true)

	// RPC failures fail open.
	c.Check(s.splPresignOutcome(c, func(sc *solRPCScenario, _, source, dest string) {
		sc.accounts[source] = "rpcerror"
		sc.accounts[dest] = "rpcerror"
	}), Equals, true)
}

// newSPLSignerTestClient is newSolCacheTestClient with USDC whitelisted; the vault is
// this node's key, so SignTx signs locally.
func (s *SolTestSuite) newSPLSignerTestClient(c *C, server *httptest.Server) (*SOLClient, common.PubKey, *pubkeymanager.PubKeyManager) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewSOLClient(s.thorKeys,
		config.BifrostChainConfiguration{
			RPCHost: "http://" + server.Listener.Addr().String(),
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            common.SOLChain,
				StartBlockHeight:   1,
				HTTPRequestTimeout: time.Second,
				MaxGasLimit:        80000,
				// The first token of the build's own list (mainnet or mocknet), to
				// check NewSOLClient wires the configured whitelist into the client.
				WhitelistTokens: []string{tokenlist.GetSOLTokenList().Tokens[0].Address},
			},
		}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	e.solScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	return e, pubkeyMgr.GetPubKeys()[0], pubkeyMgr
}

func (s *SolTestSuite) TestSPLSignTxObservation(c *C) {
	recipientMissing := true
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &r)
		if res, ok := consensusBlockhashRPCResponse(r.Method, r.Params); ok {
			_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":%s}`, res)
			return
		}
		switch r.Method {
		case "getAccountInfo":
			value := "null"
			if !recipientMissing {
				value = tokenAcct(testUSDCMint, testRecipient, "0", "initialized")
			}
			_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":%s}}`, value)
		case "getMinimumBalanceForRentExemption":
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":2039280}`))
		default:
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}))
	defer server.Close()

	e, pub, pubkeyMgr := s.newSPLSignerTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	c.Assert(e.spl, NotNil)
	c.Assert(e.spl.tokens, HasLen, 1)
	c.Assert(e.solScanner.spl, Equals, e.spl)
	e.spl = testSPLRegistry(testUSDCMint) // the fixtures below use USDC

	txOut := splTestTxOut(c, pub, 2_500_000_000)
	raw, _, obs, err := e.SignTx(txOut, 1)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil)
	c.Assert(obs, NotNil)
	vaultAddr, err := pub.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	c.Check(obs.Sender, Equals, vaultAddr.String())
	c.Check(obs.To, Equals, testRecipient)
	c.Check(obs.Coins, DeepEquals, txOut.Coins)
	// Fee plus the rent to create the missing recipient token account.
	c.Check(obs.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(baseFeePerSigLamports+2_039_280).String())

	// The signed bytes are a single-signature legacy tx whose blockhash parses back.
	_, ok := txBlockhashFromRawTx(raw)
	c.Check(ok, Equals, true)

	// With the recipient token account present, only the fee.
	recipientMissing = false
	txOut.Memo = "OUT:5D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87"
	_, _, obs, err = e.SignTx(txOut, 1)
	c.Assert(err, IsNil)
	c.Assert(obs, NotNil)
	c.Check(obs.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(baseFeePerSigLamports).String())
}

func (s *SolTestSuite) TestParseTokenBalancesTolerant(c *C) {
	balances := rpc.ParseTokenBalances([]interface{}{
		tokenBalance(1, testUSDCMint, testVault, "5", 6),
		"garbage",
		map[string]interface{}{"accountIndex": "not-a-number"},
		map[string]interface{}{"accountIndex": 3}, // no mint
	})
	c.Assert(balances, HasLen, 1)
	b, ok := rpc.FindTokenBalance(balances, 1)
	c.Assert(ok, Equals, true)
	amt, ok := b.RawAmount()
	c.Assert(ok, Equals, true)
	c.Check(amt.String(), Equals, "5")
	_, ok = rpc.FindTokenBalance(balances, 2)
	c.Check(ok, Equals, false)
}

// TestSPLSelfSendNeverSigned: an SPL outbound from a vault to its own address would
// TransferChecked from its token account to the same account. The Token program
// accepts that as a no-op, so the transaction lands, pays the fee and moves nothing,
// and the observer (which requires the destination balance to grow by the amount)
// can never report it: THORChain would see no outbound and re-issue it, burning a fee
// each time. Such an outbound is never built or signed.
func (s *SolTestSuite) TestSPLSelfSendNeverSigned(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	vaultATA := ata(c, vaultAddr.String(), testUSDCMint)

	// What the landed self-send looks like: one token account as source and
	// destination, its balance unchanged. It is not observable.
	landed := &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{
			Fee:               5000,
			PreBalances:       []uint64{1_000_000_000, 2_039_280, 1, 1, 1},
			PostBalances:      []uint64{1_000_000_000 - 5000, 2_039_280, 1, 1, 1},
			PreTokenBalances:  []interface{}{tokenBalance(1, testUSDCMint, vaultAddr.String(), "100000000", 6)},
			PostTokenBalances: []interface{}{tokenBalance(1, testUSDCMint, vaultAddr.String(), "100000000", 6)},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{vaultAddr.String(), vaultATA, testUSDCMint, testTokenProgram, testMemoProgram},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 4, Accounts: []int{0}, Data: makeMemoData("OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87")},
					{ProgramIdIndex: 3, Accounts: []int{1, 2, 1, 0}, Data: makeTransferCheckedData(1_000_000, 6)},
				},
			},
		},
	}
	c.Check(newSPLTestScanner().getTxInItem(landed, 100), IsNil)

	tx := splTestTxOut(c, vault, 100_000_000)
	tx.ToAddress = vaultAddr

	// CreateTx refuses to build it.
	_, err = splTestClient().CreateTx(tx, tx.Memo, mockBlockhashForSlot(950))
	c.Check(err, ErrorMatches, ".*self-send.*")

	// SignTx drops it for reschedule without signing or retaining it.
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &r)
		if res, ok := consensusBlockhashRPCResponse(r.Method, r.Params); ok {
			_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":%s}`, res)
			return
		}
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer server.Close()
	e, pub, pubkeyMgr := s.newSPLSignerTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.spl = testSPLRegistry(testUSDCMint)
	pubAddr, err := pub.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	selfOut := splTestTxOut(c, pub, 100_000_000)
	selfOut.ToAddress = pubAddr
	raw, checkpoint, obs, err := e.SignTx(selfOut, 1)
	c.Check(err, IsNil)
	c.Check(raw, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(obs, IsNil)
}
