package gaia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"
	cmtcoretypes "github.com/cometbft/cometbft/rpc/core/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	atypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// mockGaiaBridgeCov overrides the few bridge calls exercised by the client.
// Any other call panics on the nil embedded interface, which keeps the tests
// honest about what is being touched.
type mockGaiaBridgeCov struct {
	thorclient.ThorchainBridge
	asgards         thortypes.Vaults
	asgardErr       error
	asgardCalls     int
	lastObservedErr error
	mimir           map[string]int64
}

func (m *mockGaiaBridgeCov) GetMimir(key string) (int64, error) {
	return m.mimir[key], nil
}

func (m *mockGaiaBridgeCov) GetBlockHeight() (int64, error) { return 100, nil }

func (m *mockGaiaBridgeCov) GetAsgards() (thortypes.Vaults, error) {
	m.asgardCalls++
	return m.asgards, m.asgardErr
}

func (m *mockGaiaBridgeCov) GetLastObservedInHeight(common.Chain) (int64, error) {
	return 0, m.lastObservedErr
}

// covTestVaultPubKey resolves (via the mock account/bank services) to an
// account holding 4966941uatom == 496694100 GAIA.ATOM.
const covTestVaultPubKey = common.PubKey("sthorpub1addwnpepqf72ur2e8zk8r5augtrly40cuy94f7e663zh798tyms6pu2k8qdswf4es66")

func (s *CosmosTestSuite) newCovClient(c *C, bridge thorclient.ThorchainBridge, startHeight int64) (*CosmosClient, func()) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	fakeGRPCHost := strings.ReplaceAll(server.URL, "http://", "grpc://")
	client, err := NewCosmosClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID:        common.GAIAChain,
		RPCHost:        server.URL,
		CosmosGRPCHost: fakeGRPCHost,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:          common.GAIAChain,
			CosmosGRPCHost:   fakeGRPCHost,
			StartBlockHeight: startHeight,
			WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
				{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
			},
		},
	}, nil, bridge, GetMetricForTest(c), nil)
	c.Assert(err, IsNil)
	client.bankClient = NewMockBankServiceClient()
	client.accountClient = NewMockAccountServiceClient()
	return client, server.Close
}

func (s *CosmosTestSuite) TestNewCosmosClientNilBridgeCov(c *C) {
	_, err := NewCosmosClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.GAIAChain,
	}, nil, nil, GetMetricForTest(c), nil)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "thorchain bridge is nil")
}

func (s *CosmosTestSuite) TestBlockScannerAccessorsCov(c *C) {
	bridge := &mockGaiaBridgeCov{lastObservedErr: errors.New("thornode down")}
	client, done := s.newCovClient(c, bridge, 42)
	defer done()

	// A freshly created scanner has not scanned anything yet.
	c.Check(client.IsBlockScannerHealthy(), Equals, false)
	height, err := client.GetBlockScannerHeight()
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(42))

	// Rollback needs the last observed height from THORChain.
	err = client.RollbackBlockScanner()
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*thornode down.*")

	// When THORChain has no observed height there is nothing to roll back.
	bridge.lastObservedErr = nil
	c.Assert(client.RollbackBlockScanner(), IsNil)
	height, err = client.GetBlockScannerHeight()
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(42))
}

func (s *CosmosTestSuite) TestGetLatestTxForVaultCov(c *C) {
	client, done := s.newCovClient(c, &mockGaiaBridgeCov{}, 1)
	defer done()
	vault := covTestVaultPubKey.String()

	// Nothing recorded yet.
	_, _, err := client.GetLatestTxForVault(vault)
	c.Assert(err, NotNil)

	chain := common.GAIAChain.String()
	c.Assert(client.signerCacheManager.SetSigned("in-hash", stypes.InboundCacheKey(vault, chain), "OBSERVED"), IsNil)

	// Inbound recorded but no broadcast: the observed hash is returned
	// along with the broadcast lookup error.
	observed, broadcast, err := client.GetLatestTxForVault(vault)
	c.Assert(err, NotNil)
	c.Check(observed, Equals, "OBSERVED")
	c.Check(broadcast, Equals, "")

	c.Assert(client.signerCacheManager.SetSigned("out-hash", stypes.BroadcastCacheKey(vault, chain), "BROADCAST"), IsNil)
	observed, broadcast, err = client.GetLatestTxForVault(vault)
	c.Assert(err, IsNil)
	c.Check(observed, Equals, "OBSERVED")
	c.Check(broadcast, Equals, "BROADCAST")
}

func (s *CosmosTestSuite) TestReportSolvencyCov(c *C) {
	atom, err := common.NewAsset("GAIA.ATOM")
	c.Assert(err, IsNil)
	btc, err := common.NewAsset("BTC.BTC")
	c.Assert(err, IsNil)

	bridge := &mockGaiaBridgeCov{}
	// previous height 9 -> the scanner's next height is 10
	client, done := s.newCovClient(c, bridge, 9)
	defer done()
	queue := make(chan stypes.Solvency, 10)
	client.globalSolvencyQueue = queue

	// No fee observed yet: nothing to report.
	c.Assert(client.ReportSolvency(20), IsNil)
	c.Check(bridge.asgardCalls, Equals, 0)

	client.cosmosScanner.setLastFee(sdkmath.NewUint(5000))

	// Not a reporting height.
	c.Assert(client.ReportSolvency(21), IsNil)
	c.Check(bridge.asgardCalls, Equals, 0)

	// Unhealthy scanner and the height is the scanner's next block: only the
	// SolvencyCheckRunner reports in this case.
	c.Assert(client.ReportSolvency(10), IsNil)
	c.Check(bridge.asgardCalls, Equals, 0)

	// Bridge failure is propagated.
	bridge.asgardErr = errors.New("no asgards")
	err = client.ReportSolvency(20)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*no asgards.*")
	bridge.asgardErr = nil

	// Insolvent vault (wallet holds 496694100, vault expects more) is reported.
	insolvent := thortypes.Vault{
		PubKey: covTestVaultPubKey,
		Coins:  common.Coins{common.NewCoin(atom, cosmos.NewUint(900000000))},
	}
	// Vault without GAIA funds is ignored.
	otherChain := thortypes.Vault{
		PubKey: common.PubKey("unused"),
		Coins:  common.Coins{common.NewCoin(btc, cosmos.NewUint(100))},
	}
	bridge.asgards = thortypes.Vaults{insolvent, otherChain}
	c.Assert(client.ReportSolvency(20), IsNil)
	select {
	case msg := <-queue:
		c.Check(msg.Height, Equals, int64(20))
		c.Check(msg.Chain, Equals, common.GAIAChain)
		c.Check(msg.PubKey, Equals, covTestVaultPubKey)
		c.Check(msg.Coins.GetCoin(atom).Amount.Uint64(), Equals, uint64(496694100))
	case <-time.After(time.Second):
		c.Fatal("expected insolvent vault to be reported")
	}
	c.Check(len(queue), Equals, 0)

	// All vaults solvent while the scanner is unhealthy and the chain is
	// solvency-halted on THORChain: solvent vaults are reported so the chain
	// can auto-unhalt.
	bridge.mimir = map[string]int64{"SolvencyHaltGAIAChain": 1}
	solvent := thortypes.Vault{
		PubKey: covTestVaultPubKey,
		Coins:  common.Coins{common.NewCoin(atom, cosmos.NewUint(100000000))},
	}
	bridge.asgards = thortypes.Vaults{solvent}
	c.Assert(client.ReportSolvency(30), IsNil)
	select {
	case msg := <-queue:
		c.Check(msg.Height, Equals, int64(30))
		c.Check(msg.PubKey, Equals, covTestVaultPubKey)
	case <-time.After(time.Second):
		c.Fatal("expected solvent vault to be reported while unhealthy")
	}

	// A vault whose account can't be resolved is skipped, so not every vault
	// was processed and nothing is reported.
	badVault := thortypes.Vault{
		PubKey: common.PubKey("not-a-pubkey"),
		Coins:  common.Coins{common.NewCoin(atom, cosmos.NewUint(1))},
	}
	bridge.asgards = thortypes.Vaults{solvent, badVault}
	c.Assert(client.ReportSolvency(40), IsNil)
	c.Check(len(queue), Equals, 0)
}

func (s *CosmosTestSuite) TestSignTxExpiredCacheGateCov(c *C) {
	bridge := &mockGaiaBridgeCov{mimir: map[string]int64{}}
	client, done := s.newCovClient(c, bridge, 1)
	defer done()
	client.cosmosScanner.rpc = &mockTendermintRPC{}

	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	pk, err := common.NewPubKeyFromCrypto(temp)
	c.Assert(err, IsNil)
	atom, err := common.NewAsset("GAIA.ATOM")
	c.Assert(err, IsNil)
	toAddr, err := common.NewAddress("cosmos10tjz4ave7znpctgd2rfu6v2r6zkeup2dlmqtuz")
	c.Assert(err, IsNil)
	txOut := stypes.TxOutItem{
		Chain:       common.GAIAChain,
		ToAddress:   toAddr,
		VaultPubKey: pk,
		Coins:       common.Coins{common.NewCoin(atom, cosmos.NewUint(1000000))},
		Memo:        "OUT:AABBCCDDAABBCCDDAABBCCDDAABBCCDDAABBCCDDAABBCCDDAABBCCDDAABBCCDD",
		MaxGas:      common.Gas{common.NewCoin(atom, cosmos.NewUint(100000))},
		GasRate:     750000,
		InHash:      "hash",
		Checkpoint:  []byte("not-json"),
	}
	expired := time.Now().Add(-time.Hour).UnixMilli()
	c.Assert(client.signerCacheManager.SetSignedWithExpiry(txOut.CacheHash(),
		txOut.CacheVault(common.GAIAChain), "SIG1", expired), IsNil)

	// Expired entry with the skip-signer-cache mimir off: still skip signing.
	txBytes, checkpoint, txIn, err := client.SignTx(txOut, 1)
	c.Assert(err, IsNil)
	c.Check(txBytes, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(txIn, IsNil)
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, true)

	// Gate on and the tx is committed on chain: skip signing.
	bridge.mimir[fmt.Sprintf(constants.MimirTemplateSkipSignerCache, common.GAIAChain)] = 1
	client.txClient = mockTxServiceClient{}
	txBytes, _, _, err = client.SignTx(txOut, 1)
	c.Assert(err, IsNil)
	c.Check(txBytes, IsNil)
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, true)

	// Gate on and the tx is neither committed nor in the mempool: the cache
	// entry is cleared and signing proceeds (failing here on the bad checkpoint).
	client.txClient = mockTxServiceClient{getTxErr: status.Error(codes.NotFound, "not found")}
	client.cometRPC = fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Total: 0}}
	_, _, _, err = client.SignTx(txOut, 1)
	c.Assert(err, NotNil)
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, false)
}

func (s *CosmosTestSuite) TestSignMsgInvalidPubKeyCov(c *C) {
	client, done := s.newCovClient(c, &mockGaiaBridgeCov{}, 1)
	defer done()
	_, err := client.signMsg(client.txConfig.NewTxBuilder(), common.PubKey("bogus"), 0, 0, 1)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "unable to GetPubKeyFromBech32.*")
}

func (s *CosmosTestSuite) TestMockServiceClientsCov(c *C) {
	ctx := context.Background()

	bank := NewMockBankServiceClient()
	all, err := bank.AllBalances(ctx, &btypes.QueryAllBalancesRequest{})
	c.Assert(err, IsNil)
	c.Assert(all.Balances, HasLen, 1)
	c.Check(all.Balances[0].Denom, Equals, "uatom")
	c.Check(all.Balances[0].Amount.Int64(), Equals, int64(4966941))

	// All other bank queries are unimplemented stubs returning nothing.
	balance, err := bank.Balance(ctx, nil)
	c.Check(balance, IsNil)
	c.Check(err, IsNil)
	totalSupply, err := bank.TotalSupply(ctx, nil)
	c.Check(totalSupply, IsNil)
	c.Check(err, IsNil)
	spendable, err := bank.SpendableBalances(ctx, nil)
	c.Check(spendable, IsNil)
	c.Check(err, IsNil)
	spendableDenom, err := bank.SpendableBalanceByDenom(ctx, nil)
	c.Check(spendableDenom, IsNil)
	c.Check(err, IsNil)
	supplyOf, err := bank.SupplyOf(ctx, nil)
	c.Check(supplyOf, IsNil)
	c.Check(err, IsNil)
	bankParams, err := bank.Params(ctx, nil)
	c.Check(bankParams, IsNil)
	c.Check(err, IsNil)
	denomMeta, err := bank.DenomMetadata(ctx, nil)
	c.Check(denomMeta, IsNil)
	c.Check(err, IsNil)
	denomsMeta, err := bank.DenomsMetadata(ctx, nil)
	c.Check(denomsMeta, IsNil)
	c.Check(err, IsNil)
	denomMetaQS, err := bank.DenomMetadataByQueryString(ctx, nil)
	c.Check(denomMetaQS, IsNil)
	c.Check(err, IsNil)
	owners, err := bank.DenomOwners(ctx, nil)
	c.Check(owners, IsNil)
	c.Check(err, IsNil)
	ownersQ, err := bank.DenomOwnersByQuery(ctx, nil)
	c.Check(ownersQ, IsNil)
	c.Check(err, IsNil)
	sendEnabled, err := bank.SendEnabled(ctx, nil)
	c.Check(sendEnabled, IsNil)
	c.Check(err, IsNil)

	acct := NewMockAccountServiceClient()
	accRes, err := acct.Account(ctx, &atypes.QueryAccountRequest{})
	c.Assert(err, IsNil)
	ba := new(atypes.BaseAccount)
	c.Assert(ba.Unmarshal(accRes.GetAccount().Value), IsNil)
	c.Check(ba.AccountNumber, Equals, uint64(3530305))
	c.Check(ba.Sequence, Equals, uint64(3))

	accounts, err := acct.Accounts(ctx, nil)
	c.Check(accounts, IsNil)
	c.Check(err, IsNil)
	byID, err := acct.AccountAddressByID(ctx, nil)
	c.Check(byID, IsNil)
	c.Check(err, IsNil)
	authParams, err := acct.Params(ctx, nil)
	c.Check(authParams, IsNil)
	c.Check(err, IsNil)
	modAccts, err := acct.ModuleAccounts(ctx, nil)
	c.Check(modAccts, IsNil)
	c.Check(err, IsNil)
	modAcct, err := acct.ModuleAccountByName(ctx, nil)
	c.Check(modAcct, IsNil)
	c.Check(err, IsNil)
	prefix, err := acct.Bech32Prefix(ctx, nil)
	c.Check(prefix, IsNil)
	c.Check(err, IsNil)
	b2s, err := acct.AddressBytesToString(ctx, nil)
	c.Check(b2s, IsNil)
	c.Check(err, IsNil)
	s2b, err := acct.AddressStringToBytes(ctx, nil)
	c.Check(s2b, IsNil)
	c.Check(err, IsNil)
	info, err := acct.AccountInfo(ctx, nil)
	c.Check(info, IsNil)
	c.Check(err, IsNil)

	// A malformed account fixture surfaces an unmarshal error.
	saved := accountByAddress
	defer func() { accountByAddress = saved }()
	accountByAddress = []byte("{not json")
	_, err = acct.Account(ctx, &atypes.QueryAccountRequest{})
	c.Assert(err, NotNil)
}
