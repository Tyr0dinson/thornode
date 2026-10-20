package solana

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/mr-tron/base58"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	testUSDCMint       = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	testUSDTMint       = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
	testUnlistedMint   = "So11111111111111111111111111111111111111112"
	testTokenProgram   = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	testToken2022      = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
	testATAProgram     = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"
	testUSDCAssetStr   = "SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V"
	testSenderTokenAcc = "2Y7HATmn9aJBcxCskE5V2U2epmjvkZmB51zTJBbhj4cU"
)

type SPLScannerSuite struct{}

// testSPLTokens is a fixed token list, so tests do not depend on the embedded SOL
// token list, which differs between mainnet and mocknet builds.
var testSPLTokens = []tokenlist.ERC20Token{
	{Address: testUSDCMint, Symbol: "USDC", Name: "USD Coin", Decimals: 6},
	{Address: testUSDTMint, Symbol: "USDT", Name: "Tether USD", Decimals: 6},
}

func testSPLRegistry(whitelist ...string) *splRegistry {
	return newSPLRegistryFromTokens(common.SOLChain, whitelist, testSPLTokens, log.Logger)
}

var _ = Suite(&SPLScannerSuite{})

// newSPLTestScanner is a scanner with USDC and USDT whitelisted and SPL enabled (as
// by the SOLTokens mimir).
func newSPLTestScanner() *SOLScanner {
	logger := log.Logger.With().Str("module", "test").Logger()
	s := &SOLScanner{
		cfg:    config.BifrostBlockScannerConfiguration{ChainID: common.SOLChain},
		logger: logger,
		spl:    testSPLRegistry(testUSDCMint, testUSDTMint),
	}
	s.splEnabled.Store(true)
	return s
}

func makeTransferCheckedData(amount uint64, decimals uint8) string {
	data := make([]byte, 10)
	data[0] = types.TokenInstructionTransferChecked
	binary.LittleEndian.PutUint64(data[1:9], amount)
	data[9] = decimals
	return base58.Encode(data)
}

func makeTokenTransferData(amount uint64) string {
	data := make([]byte, 9)
	data[0] = types.TokenInstructionTransfer
	binary.LittleEndian.PutUint64(data[1:9], amount)
	return base58.Encode(data)
}

func tokenBalance(accountIndex int, mint, owner, amount string, decimals int) map[string]interface{} {
	return map[string]interface{}{
		"accountIndex":  accountIndex,
		"mint":          mint,
		"owner":         owner,
		"programId":     testTokenProgram,
		"uiTokenAmount": map[string]interface{}{"amount": amount, "decimals": decimals},
	}
}

func ata(c *C, owner, mint string) string {
	a, err := types.FindAssociatedTokenAddress(types.MustPublicKeyFromString(owner), types.MustPublicKeyFromString(mint))
	c.Assert(err, IsNil)
	return a.String()
}

// splInbound builds a user -> vault USDC TransferChecked with a memo:
//
//	0 sender (fee payer), 1 sender token account, 2 vault token account,
//	3 USDC mint, 4 token program, 5 memo program.
func splInbound(c *C, amount uint64) *rpc.TransactionResult {
	return splInboundTo(c, testVault, amount)
}

// splInboundTo is splInbound to the given vault wallet.
func splInboundTo(c *C, vault string, amount uint64) *rpc.TransactionResult {
	vaultATA := ata(c, vault, testUSDCMint)
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{
			Fee:          5000,
			PreBalances:  []uint64{1_000_000_000, 2_039_280, 2_039_280, 1, 1, 1},
			PostBalances: []uint64{1_000_000_000 - 5000, 2_039_280, 2_039_280, 1, 1, 1},
			PreTokenBalances: []interface{}{
				tokenBalance(1, testUSDCMint, testSender, "50000000", 6),
				tokenBalance(2, testUSDCMint, vault, "7000000", 6),
			},
			PostTokenBalances: []interface{}{
				tokenBalance(1, testUSDCMint, testSender, fmt.Sprint(50_000_000-amount), 6),
				tokenBalance(2, testUSDCMint, vault, fmt.Sprint(7_000_000+amount), 6),
			},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{testSender, testSenderTokenAcc, vaultATA, testUSDCMint, testTokenProgram, testMemoProgram},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 4, Accounts: []int{1, 3, 2, 0}, Data: makeTransferCheckedData(amount, 6)},
					{ProgramIdIndex: 5, Accounts: []int{0}, Data: makeMemoData("=:BTC.BTC:bc1qxyz")},
				},
			},
		},
	}
}

func (s *SPLScannerSuite) TestInboundTransferChecked(c *C) {
	scanner := newSPLTestScanner()
	item := scanner.getTxInItem(splInbound(c, 12_345_678), 100) // 12.345678 USDC
	c.Assert(item, NotNil)
	c.Check(item.Tx, Equals, testTxSig)
	c.Check(item.Sender, Equals, testSender)
	c.Check(item.To, Equals, testVault) // the vault wallet, not its token account
	c.Check(item.Memo, Equals, "=:BTC.BTC:bc1qxyz")
	c.Assert(item.Coins, HasLen, 1)
	c.Check(item.Coins[0].Asset.String(), Equals, testUSDCAssetStr)
	c.Check(item.Coins[0].Amount.String(), Equals, "1234567800") // 1e6 -> 1e8
	c.Check(item.Coins[0].Decimals, Equals, int64(6))
	c.Assert(item.Gas, HasLen, 1)
	c.Check(item.Gas[0].Asset, Equals, common.SOLAsset)
	c.Check(item.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(5000).String())
}

func (s *SPLScannerSuite) TestInboundPlainTransfer(c *C) {
	// Transfer (3) carries no mint account; the mint comes from the token balances.
	scanner := newSPLTestScanner()
	txn := splInbound(c, 2_000_000)
	txn.Transaction.Message.Instructions[0] = rpc.RPCInstruction{ProgramIdIndex: 4, Accounts: []int{1, 2, 0}, Data: makeTokenTransferData(2_000_000)}
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Coins[0].Asset.String(), Equals, testUSDCAssetStr)
	c.Check(item.Coins[0].Amount.String(), Equals, "200000000")
}

func (s *SPLScannerSuite) TestInboundCreatingVaultTokenAccount(c *C) {
	// The sender creates the vault's token account in the same tx: the destination has
	// no pre balance, and the sender's lamport spend (fee + rent) is the observed gas.
	scanner := newSPLTestScanner()
	txn := splInbound(c, 1_000_000)
	txn.Meta.PreTokenBalances = txn.Meta.PreTokenBalances[:1]
	txn.Meta.PostTokenBalances[1] = tokenBalance(2, testUSDCMint, testVault, "1000000", 6)
	txn.Meta.PostBalances[0] = 1_000_000_000 - 5000 - 2_039_280
	txn.Meta.PreBalances[2] = 0
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Coins[0].Amount.String(), Equals, "100000000")
	c.Check(item.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(5000+2_039_280).String())
}

func (s *SPLScannerSuite) TestSourceClosedInSameTx(c *C) {
	scanner := newSPLTestScanner()
	txn := splInbound(c, 50_000_000)
	txn.Meta.PostTokenBalances = txn.Meta.PostTokenBalances[1:] // source closed after transfer
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testSender)
}

func (s *SPLScannerSuite) TestRejectedShapes(c *C) {
	cases := map[string]func(txn *rpc.TransactionResult){
		"non-whitelisted mint": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.AccountKeys[3] = testUnlistedMint
			for _, b := range append(txn.Meta.PreTokenBalances, txn.Meta.PostTokenBalances...) {
				b.(map[string]interface{})["mint"] = testUnlistedMint
			}
		},
		"token-2022 account": func(txn *rpc.TransactionResult) {
			txn.Meta.PostTokenBalances[1].(map[string]interface{})["programId"] = testToken2022
		},
		"decimals mismatch": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.Instructions[0].Data = makeTransferCheckedData(12_345_678, 9)
		},
		"mint account mismatch": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.AccountKeys[3] = testUSDTMint
		},
		"destination credited less than the amount": func(txn *rpc.TransactionResult) {
			txn.Meta.PostTokenBalances[1].(map[string]interface{})["uiTokenAmount"] = map[string]interface{}{"amount": "7000001", "decimals": 6}
		},
		"no destination token balance": func(txn *rpc.TransactionResult) {
			txn.Meta.PostTokenBalances = txn.Meta.PostTokenBalances[:1]
		},
		"source of another mint": func(txn *rpc.TransactionResult) {
			txn.Meta.PreTokenBalances[0].(map[string]interface{})["mint"] = testUSDTMint
		},
		"source mint inconsistent across the transaction": func(txn *rpc.TransactionResult) {
			txn.Meta.PostTokenBalances[0].(map[string]interface{})["mint"] = testUSDTMint
		},
		"two token transfers": func(txn *rpc.TransactionResult) {
			ins := txn.Transaction.Message.Instructions
			txn.Transaction.Message.Instructions = append([]rpc.RPCInstruction{ins[0]}, ins...)
		},
		"malformed transfer": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.Instructions[0].Data = base58.Encode([]byte{types.TokenInstructionTransferChecked, 1, 2})
		},
		"zero amount": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.Instructions[0].Data = makeTransferCheckedData(0, 6)
		},
		"self transfer": func(txn *rpc.TransactionResult) {
			// source == destination: the balance does not change.
			txn.Transaction.Message.Instructions[0].Accounts = []int{2, 3, 2, 0}
			txn.Meta.PostTokenBalances[1] = tokenBalance(2, testUSDCMint, testVault, "7000000", 6)
		},
		"account index out of range": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.Instructions[0].Accounts = []int{1, 3, 42, 0}
		},
		// The sender is the source's owner before the transaction: one created (or
		// funded and reassigned) within it has no pre-state owner to trust.
		"source has no pre-transaction owner": func(txn *rpc.TransactionResult) {
			txn.Meta.PreTokenBalances = txn.Meta.PreTokenBalances[1:]
		},
		"source owner reassigned in the transaction": func(txn *rpc.TransactionResult) {
			txn.Meta.PostTokenBalances[0].(map[string]interface{})["owner"] = testVault
		},
		"destination owner changed in the transaction": func(txn *rpc.TransactionResult) {
			txn.Meta.PreTokenBalances[1].(map[string]interface{})["owner"] = testSender
		},
		// Only the owner's canonical associated token account is credited: Bifrost
		// spends from nothing else.
		"destination is not the owner's associated token account": func(txn *rpc.TransactionResult) {
			txn.Transaction.Message.AccountKeys[2] = testNewAccount
		},
	}
	for name, mutate := range cases {
		txn := splInbound(c, 12_345_678)
		mutate(txn)
		item := newSPLTestScanner().getTxInItem(txn, 100)
		c.Check(item, IsNil, Commentf(name))
	}
}

func (s *SPLScannerSuite) TestNoWhitelistIgnoresTokens(c *C) {
	scanner := newTestScanner() // no SPL registry
	c.Check(scanner.getTxInItem(splInbound(c, 12_345_678), 100), IsNil)
}

func (s *SPLScannerSuite) TestSOLTransferTakesPrecedence(c *C) {
	// A tx that moves SOL is observed exactly as before SPL support: as the SOL
	// transfer, ignoring the token transfer beside it.
	scanner := newSPLTestScanner()
	txn := splInbound(c, 12_345_678)
	txn.Transaction.Message.AccountKeys = append(txn.Transaction.Message.AccountKeys, testVault, testSystemProgram)
	txn.Transaction.Message.Instructions = append(txn.Transaction.Message.Instructions,
		rpc.RPCInstruction{ProgramIdIndex: 7, Accounts: []int{0, 6}, Data: makeTransferData(1_000_000_000)})
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Coins[0].Asset, Equals, common.SOLAsset)
	c.Check(item.Coins[0].Amount.Equal(convertLamportsToTHORChain(1_000_000_000)), Equals, true)
	c.Check(item.To, Equals, testVault)
}

func (s *SPLScannerSuite) TestVaultOutboundBooksRentAsGas(c *C) {
	// The vault's own outbound, as NewSPLTransferTransaction builds it, creating the
	// recipient's token account. Gas is the vault's whole lamport spend.
	scanner := newSPLTestScanner()
	vaultATA := ata(c, testVault, testUSDCMint)
	recipientATA := ata(c, testSender, testUSDCMint)
	txn := &rpc.TransactionResult{
		Slot: 200,
		Meta: rpc.RPCMeta{
			Fee:          5000,
			PreBalances:  []uint64{10_000_000_000, 2_039_280, 0, 1, 1, 1, 1, 1, 1},
			PostBalances: []uint64{10_000_000_000 - 5000 - 2_039_280, 2_039_280, 2_039_280, 1, 1, 1, 1, 1, 1},
			PreTokenBalances: []interface{}{
				tokenBalance(1, testUSDCMint, testVault, "90000000", 6),
			},
			PostTokenBalances: []interface{}{
				tokenBalance(1, testUSDCMint, testVault, "65000000", 6),
				tokenBalance(2, testUSDCMint, testSender, "25000000", 6),
			},
		},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{testVault, vaultATA, recipientATA, testSender, testUSDCMint, testSystemProgram, testTokenProgram, testATAProgram, testMemoProgram},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 8, Accounts: []int{0}, Data: makeMemoData("OUT:ABCDEF")},
					{ProgramIdIndex: 7, Accounts: []int{0, 2, 3, 4, 5, 6}, Data: base58.Encode([]byte{1})},
					{ProgramIdIndex: 6, Accounts: []int{1, 4, 2, 0}, Data: makeTransferCheckedData(25_000_000, 6)},
				},
			},
		},
	}
	item := scanner.getTxInItem(txn, 200)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testVault)
	c.Check(item.To, Equals, testSender)
	c.Check(item.Memo, Equals, "OUT:ABCDEF")
	c.Check(item.Coins[0].Amount.String(), Equals, "2500000000")
	c.Check(item.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(5000+2_039_280).String())

	// The recipient token account already existed: gas is just the fee.
	txn.Meta.PreBalances[2] = 2_039_280
	txn.Meta.PostBalances[0] = 10_000_000_000 - 5000
	txn.Meta.PreTokenBalances = append(txn.Meta.PreTokenBalances, tokenBalance(2, testUSDCMint, testSender, "0", 6))
	item = scanner.getTxInItem(txn, 200)
	c.Assert(item, NotNil)
	c.Check(item.Gas[0].Amount.String(), Equals, convertLamportsToTHORChain(5000).String())
}

func (s *SPLScannerSuite) TestDurableOutboundStillObserved(c *C) {
	// AdvanceNonceAccount is a SystemProgram instruction but not a transfer, so a
	// durable SPL outbound still takes the token path.
	scanner := newSPLTestScanner()
	txn := splInbound(c, 3_000_000)
	nonceAcct, err := common.SolNonceAccountAddress(testSender)
	c.Assert(err, IsNil)
	keys := txn.Transaction.Message.AccountKeys
	txn.Transaction.Message.AccountKeys = append(keys, nonceAcct, "SysvarRecentB1ockHashes11111111111111111111", testSystemProgram)
	advance := make([]byte, 4)
	binary.LittleEndian.PutUint32(advance, 4)
	txn.Transaction.Message.Instructions = append([]rpc.RPCInstruction{
		{ProgramIdIndex: 8, Accounts: []int{6, 7, 0}, Data: base58.Encode(advance)},
	}, txn.Transaction.Message.Instructions...)
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.Coins[0].Amount.String(), Equals, "300000000")
}

func (s *SPLScannerSuite) TestLoadedAddressIndices(c *C) {
	// In a versioned tx the vault token account and mint can come from a lookup
	// table; token balance indices refer to the resolved account list.
	scanner := newSPLTestScanner()
	txn := splInbound(c, 4_000_000)
	keys := txn.Transaction.Message.AccountKeys
	// static: sender, sender ATA, token program, memo ; loaded writable: vault ATA ; loaded readonly: mint
	txn.Transaction.Message.AccountKeys = []string{keys[0], keys[1], keys[4], keys[5]}
	txn.Meta.LoadedAddresses = rpc.RPCAddrList{Writable: []interface{}{keys[2]}, Readonly: []interface{}{keys[3]}}
	txn.Transaction.Message.Instructions = []rpc.RPCInstruction{
		{ProgramIdIndex: 2, Accounts: []int{1, 5, 4, 0}, Data: makeTransferCheckedData(4_000_000, 6)},
		{ProgramIdIndex: 3, Accounts: []int{0}, Data: makeMemoData("=:BTC.BTC:bc1qxyz")},
	}
	txn.Meta.PreTokenBalances = []interface{}{
		tokenBalance(1, testUSDCMint, testSender, "50000000", 6),
		tokenBalance(4, testUSDCMint, testVault, "7000000", 6),
	}
	txn.Meta.PostTokenBalances = []interface{}{
		tokenBalance(1, testUSDCMint, testSender, "46000000", 6),
		tokenBalance(4, testUSDCMint, testVault, "11000000", 6),
	}
	item := scanner.getTxInItem(txn, 100)
	c.Assert(item, NotNil)
	c.Check(item.To, Equals, testVault)
	c.Check(item.Coins[0].Amount.String(), Equals, "400000000")
}

func (s *SPLScannerSuite) TestRegistry(c *C) {
	logger := log.Logger
	r := newSPLRegistryFromTokens(common.SOLChain, []string{
		testUSDCMint,
		testUSDCMint, // duplicate
		"epjfwdd5aufqssqem2qn1xzybapc8g4weggkzwytdt1v", // wrong case: not the listed mint
		testUnlistedMint, // not in the token list
	}, testSPLTokens, logger)
	c.Assert(r.tokens, HasLen, 1)

	tok, ok := r.tokenByMint(testUSDCMint)
	c.Assert(ok, Equals, true)
	c.Check(tok.decimals, Equals, 6)
	_, ok = r.tokenByMint("epjfwdd5aufqssqem2qn1xzybapc8g4weggkzwytdt1v")
	c.Check(ok, Equals, false)

	asset, err := common.NewAsset(testUSDCAssetStr)
	c.Assert(err, IsNil)
	tok, ok = r.tokenByAsset(asset)
	c.Assert(ok, Equals, true)
	c.Check(tok.mint.String(), Equals, testUSDCMint)
	// lowercase symbol still resolves (asset symbols compare case-insensitively)
	lower, err := common.NewAsset("SOL.usdc-epjfwdd5aufqssqem2qn1xzybapc8g4weggkzwytdt1v")
	c.Assert(err, IsNil)
	_, ok = r.tokenByAsset(lower)
	c.Check(ok, Equals, true)
	_, ok = r.tokenByAsset(asset.GetTradeAsset())
	c.Check(ok, Equals, false)
	_, ok = r.tokenByAsset(common.SOLAsset)
	c.Check(ok, Equals, false)

	accts, err := r.vaultTokenAccounts(testVault)
	c.Assert(err, IsNil)
	c.Assert(accts, HasLen, 1)
	c.Check(accts[0].address, Equals, ata(c, testVault, testUSDCMint))

	var nilRegistry *splRegistry
	_, ok = nilRegistry.tokenByMint(testUSDCMint)
	c.Check(ok, Equals, false)
	accts, err = nilRegistry.vaultTokenAccounts(testVault)
	c.Check(err, IsNil)
	c.Check(accts, HasLen, 0)
}

func (s *SPLScannerSuite) TestDecimalConversion(c *C) {
	v, err := splToTHORChain(big.NewInt(1_234_567), 6)
	c.Assert(err, IsNil)
	c.Check(v.String(), Equals, "123456700")
	v, err = splToTHORChain(big.NewInt(1_234_567_891), 9)
	c.Assert(err, IsNil)
	c.Check(v.String(), Equals, "123456789")
	v, err = splToTHORChain(big.NewInt(9), 9) // below 1e-8
	c.Assert(err, IsNil)
	c.Check(v.IsZero(), Equals, true)

	n, err := splFromTHORChain(cosmos.NewUint(123_456_789), 6)
	c.Assert(err, IsNil)
	c.Check(n, Equals, uint64(1_234_567)) // truncates what 6 decimals cannot express
	n, err = splFromTHORChain(cosmos.NewUint(123_456_789), 9)
	c.Assert(err, IsNil)
	c.Check(n, Equals, uint64(1_234_567_890))
	_, err = splFromTHORChain(cosmos.NewUintFromString("1000000000000000000000"), 18)
	c.Check(err, NotNil)

	// THORChain matches an observed outbound against the scheduled amount rounded to
	// the pool's decimals, so what the vault sends and the scanner reads back must
	// equal cosmos.RoundToDecimal exactly, for any amount. (0 decimals means "unset" to
	// THORChain; newSPLRegistry refuses such mints.)
	for _, decimals := range []int{1, 2, 5, 6, 8, 9, 12} {
		for _, amt := range []uint64{1, 99, 100, 123_456_789, 999_999_999_999} {
			sent, err := splFromTHORChain(cosmos.NewUint(amt), decimals)
			c.Assert(err, IsNil)
			observed, err := splToTHORChain(new(big.Int).SetUint64(sent), decimals)
			c.Assert(err, IsNil)
			c.Check(observed.String(), Equals, cosmos.RoundToDecimal(cosmos.NewUint(amt), int64(decimals)).String(),
				Commentf("decimals %d amount %d", decimals, amt))
		}
	}
}

// loadTxFixture reads a finalized getTransaction result recorded from a local
// solana-test-validator.
func loadTxFixture(c *C, name string) *rpc.TransactionResult {
	raw, err := os.ReadFile(filepath.Join("testdata", "spl", name))
	c.Assert(err, IsNil)
	var txn rpc.TransactionResult
	c.Assert(json.Unmarshal(raw, &txn), IsNil)
	return &txn
}

// TestRejectsSourceOwnerReassignedInTx replays a transaction recorded on a local
// validator and signed only by an outside key: it transfers out of its own token
// account, then SetAuthority(AccountOwner) hands that account to the vault in the
// same transaction. Post-transaction balances report the vault as the source owner,
// so trusting them would observe a vault outbound the vault never signed.
func (s *SPLScannerSuite) TestRejectsSourceOwnerReassignedInTx(c *C) {
	txn := loadTxFixture(c, "source-owner-reassigned.json")
	c.Assert(txn.Meta.Err, IsNil) // the runtime accepted it
	item, err := newSPLTestScanner().txInItem(txn, int64(txn.Slot))
	c.Assert(err, IsNil)
	c.Check(item, IsNil)
}

// TestRejectsNonCanonicalDestination replays a recorded transfer into a token account
// owned by the vault that is not its associated token account. Bifrost neither scans
// nor spends from such accounts, so crediting the vault would put funds on its books
// that it cannot pay out.
func (s *SPLScannerSuite) TestRejectsNonCanonicalDestination(c *C) {
	txn := loadTxFixture(c, "noncanonical-vault-account.json")
	c.Assert(txn.Meta.Err, IsNil)
	item, err := newSPLTestScanner().txInItem(txn, int64(txn.Slot))
	c.Assert(err, IsNil)
	c.Check(item, IsNil)
}
