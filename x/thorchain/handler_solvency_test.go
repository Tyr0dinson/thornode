package thorchain

import (
	"errors"
	"fmt"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerSolvencyTestSuite struct{}

var _ = Suite(&HandlerSolvencyTestSuite{})

func (s *HandlerSolvencyTestSuite) TestValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewSolvencyHandler(mgr)
	// msgSolvency signed by  none active node should be rejected
	msgSolvency, err := NewMsgSolvency(common.ETHChain,
		GetRandomPubKey(),
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1024*common.One)),
		),
		1024,
		GetRandomBech32Addr())
	c.Assert(err, IsNil)
	c.Assert(handler.validate(ctx, *msgSolvency), NotNil)
	// active node
	var activeNodes [4]NodeAccount
	for i := 0; i < 4; i++ {
		node := GetRandomValidatorNode(NodeActive)
		activeNodes[i] = node
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	}
	// msgSolvency signed by active node should be accepted
	msgSolvency.Signer = activeNodes[0].NodeAddress

	c.Assert(err, IsNil)
	c.Assert(handler.validate(ctx, *msgSolvency), IsNil)

	result, err := handler.Run(ctx, msgSolvency)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	// solvency voter should have been created
	voter, err := mgr.Keeper().GetSolvencyVoter(ctx, msgSolvency.Id, msgSolvency.Chain)
	c.Assert(err, IsNil)
	c.Assert(voter.Empty(), Equals, false)

	asgard := NewVault(1024, ActiveVault, AsgardVault, msgSolvency.PubKey, []string{
		common.ETHChain.String(),
		common.BTCChain.String(),
		common.LTCChain.String(),
		common.BCHChain.String(),
	}, nil)
	asgard.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

	// Set up ETH pool with liquidity so gap can be valued in RUNE
	// Pool has 1000 ETH and 10000 RUNE, so 1 ETH = 10 RUNE
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(10000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Set RUNE price to $5 so DollarConfigInRune can convert the $500 threshold
	// At $5/RUNE, $500 = 100 RUNE
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// second active node report solvency , it should be accepted
	// reach consensus , vault is solvent , everything continues
	msgSolvency.Signer = activeNodes[1].NodeAddress
	result, err = handler.Run(ctx, msgSolvency)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// third active node report solvency , it should be accepted
	msgSolvency.Signer = activeNodes[2].NodeAddress
	result, err = handler.Run(ctx, msgSolvency)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// vault suppose to have 1024 ETH, however only 100 left , vault is insolvent , chain should stop
	msgSolvency1, err := NewMsgSolvency(common.ETHChain,
		asgard.PubKey,
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		),
		1024,
		GetRandomBech32Addr())
	c.Assert(err, IsNil)
	msgSolvency1.Signer = activeNodes[0].NodeAddress
	result, err = handler.Run(ctx, msgSolvency1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// minority , so the second voter should reach consensus
	msgSolvency1.Signer = activeNodes[1].NodeAddress
	result, err = handler.Run(ctx, msgSolvency1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())
	c.Assert(mgr.Keeper().DeleteMimir(ctx, "SolvencyHaltETHChain"), IsNil)

	// vault suppose to have 1024 ETH, however only 1000 left , but there are 20 ETH in the pending outbound queue
	// chain should not stopped
	txOut := NewTxOut(ctx.BlockHeight())
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   "0x3a196410a0f5facd08fd7880a4b8551cd085c031",
			VaultPubKey: asgard.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(20*common.One)),
			Memo:        "OUT:693c3337193b1185fb0a36d8b7ec3f612ad57e599fd25e7ad6ec887aae43b291",
			InHash:      "693c3337193b1185fb0a36d8b7ec3f612ad57e599fd25e7ad6ec887aae43b291",
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 100)
	msgSolvency2, err := NewMsgSolvency(common.ETHChain,
		asgard.PubKey,
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		),
		1024,
		GetRandomBech32Addr())

	c.Assert(err, IsNil)
	msgSolvency2.Signer = activeNodes[0].NodeAddress
	result, err = handler.Run(ctx, msgSolvency2)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	msgSolvency2.Signer = activeNodes[1].NodeAddress
	result, err = handler.Run(ctx, msgSolvency2)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))

	// tampered MsgSolvency should be rejected
	msgSolvency2.Coins = common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1024*common.One)),
	)
	result, err = handler.Run(ctx, msgSolvency2)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, se.ErrUnknownRequest), Equals, true)
	c.Assert(result, IsNil)
}

func (s *HandlerSolvencyTestSuite) TestObservingSlashing(c *C) {
	ctx, mgr := setupManagerForTest(c)
	height := int64(1024)
	ctx = ctx.WithBlockHeight(height)

	// Check expected slash point amounts
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := mgr.GetConstants().GetInt64Value(constants.ObservationDelayFlexibility)
	c.Assert(observeSlashPoints, Equals, int64(1))
	c.Assert(lackOfObservationPenalty, Equals, int64(2))
	c.Assert(observeFlex, Equals, int64(10))

	asgardVault := GetRandomVault()
	asgardVault.Chains = []string{common.ETHChain.String()}
	asgardVault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, asgardVault), IsNil)

	nas := NodeAccounts{
		// 6 Active nodes, 1 Standby node; 1/3rd SolvencyVoter consensus needs 2.
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeStandby),
	}
	for _, item := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, item), IsNil)
	}

	msg, err := NewMsgSolvency(common.ETHChain,
		asgardVault.PubKey,
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		),
		1024,
		cosmos.AccAddress{})
	c.Assert(err, IsNil)

	handler := NewSolvencyHandler(mgr)

	broadcast := func(c *C, ctx cosmos.Context, na NodeAccount, msg *MsgSolvency) {
		msg.Signer = na.NodeAddress
		_, err := handler.handle(ctx, *msg)
		c.Assert(err, IsNil)
	}

	checkSlashPoints := func(c *C, ctx cosmos.Context, nas NodeAccounts, expected [7]int64) {
		var slashPoints [7]int64
		for i, na := range nas {
			slashPoint, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			slashPoints[i] = slashPoint
		}
		c.Assert(slashPoints == expected, Equals, true, Commentf(fmt.Sprint(slashPoints)))
	}

	checkSlashPoints(c, ctx, nas, [7]int64{0, 0, 0, 0, 0, 0, 0})

	// 1 of 6 Active nodes observe.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 0, 0, 0, 0, 0, 0})

	// nas[0] observes again.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 0, 0})

	// nas[1] observes, reaching consensus (2/6, being exactly the 1/3 SolvencyVoter threshold).
	// (Active nodes which observed are decremented ObserveSlashPoints;
	//  those which haven't are incremented LackOfObservationPenalty.)
	broadcast(c, ctx, nas[1], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 0, 2, 2, 2, 2, 0})

	// nas[0] observes again.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 2, 2, 2, 2, 0})

	// Within the ObservationDelayFlexibility period, nas[2] observes
	// (and is decremented LackOfObservationPenalty).
	height += observeFlex
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[2], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 2, 2, 2, 0})

	// The ObservationDelayFlexibility period ends, after which nas[3] observes;
	// it is not incremented ObserveSlashPoints (and it is added to the list of signers)
	// and is also not decremented LackOfObservationPenalty.
	height++
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[3], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 2, 2, 2, 0})

	// nas[3] observes again, this time incremented ObserveSlashPoints for the extra signing.
	broadcast(c, ctx, nas[3], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 3, 2, 2, 0})

	// Note that nas[6], the Standby node, remains unaffected by the Actives nodes' observations.
}

func (s *HandlerSolvencyTestSuite) TestChainAwareConsensus(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1024)

	// 6 active nodes, but only nas[0..2] are in the ETH vault.
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}

	ethVault := GetRandomVault()
	ethVault.Status = ActiveVault
	ethVault.Type = AsgardVault
	ethVault.Chains = []string{common.ETHChain.String()}
	ethVault.Membership = []string{
		nas[0].PubKeySet.Secp256k1.String(),
		nas[1].PubKeySet.Secp256k1.String(),
		nas[2].PubKeySet.Secp256k1.String(),
	}
	ethVault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, ethVault), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(10000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	handler := NewSolvencyHandler(mgr)

	msg, err := NewMsgSolvency(common.ETHChain,
		ethVault.PubKey,
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		),
		1024,
		cosmos.AccAddress{})
	c.Assert(err, IsNil)

	broadcast := func(c *C, na NodeAccount) {
		msg.Signer = na.NodeAddress
		_, handleErr := handler.handle(ctx, *msg)
		c.Assert(handleErr, IsNil)
	}

	// With 3 chain participants, SolvencyVoter consensus needs 1/3 = 1.
	// nas[0] signs — reaches consensus with 1 of 3.
	broadcast(c, nas[0])
	voter, err := mgr.Keeper().GetSolvencyVoter(ctx, msg.Id, msg.Chain)
	c.Assert(err, IsNil)
	c.Assert(voter.ConsensusBlockHeight, Equals, ctx.BlockHeight(), Commentf("should have consensus with 1 of 3 chain participants"))

	// nas[3] (not in vault) should not be penalized — they are not a chain participant.
	pts3, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[3].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts3, Equals, int64(0), Commentf("non-chain-participant should not be penalized"))

	pts4, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[4].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts4, Equals, int64(0))

	pts5, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[5].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts5, Equals, int64(0))

	// nas[2] (in vault but didn't sign) should have LackOfObservationPenalty.
	pts2, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[2].NodeAddress)
	c.Assert(err, IsNil)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	c.Assert(pts2, Equals, lackOfObservationPenalty, Commentf("chain participant non-signer should be penalized"))
}

func (s *HandlerSolvencyTestSuite) TestInsolvencyCheckSkipsUnpriceableGapCoveredByPendingRagnarok(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(200)

	pool := NewPool()
	pool.Asset = common.BCHAsset
	pool.Status = PoolSuspended
	pool.BalanceAsset = cosmos.NewUint(333000)
	pool.BalanceRune = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.Keeper().SetMimir(ctx, "RAGNAROK-"+common.BCHAsset.MimirString(), 1)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.BCHChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(100*common.One))))

	txOut := NewTxOut(ctx.BlockHeight() - 1)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.BCHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BCHAsset, cosmos.NewUint(150*common.One)),
			Memo:        NewRagnarokMemo(1).String(),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	c.Assert(insolvencyCheck(ctx, mgr, vault, common.NewCoins(), common.BCHChain), Equals, false)
}

func (s *HandlerSolvencyTestSuite) TestInsolvencyCheckSkipsMissingPoolGapCoveredByPendingRagnarok(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(200)
	mgr.Keeper().SetMimir(ctx, "RAGNAROK-"+common.BCHAsset.MimirString(), 1)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.BCHChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(100*common.One))))

	txOut := NewTxOut(ctx.BlockHeight() - 1)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.BCHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BCHAsset, cosmos.NewUint(150*common.One)),
			Memo:        NewRagnarokMemo(1).String(),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	c.Assert(insolvencyCheck(ctx, mgr, vault, common.NewCoins(), common.BCHChain), Equals, false)
}

func (s *HandlerSolvencyTestSuite) TestInsolvencyCheckFlagsUnpriceableGapWithoutRagnarokCoverage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(200)

	pool := NewPool()
	pool.Asset = common.BCHAsset
	pool.Status = PoolSuspended
	pool.BalanceAsset = cosmos.NewUint(333000)
	pool.BalanceRune = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.BCHChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BCHAsset, cosmos.NewUint(100*common.One))))

	txOut := NewTxOut(ctx.BlockHeight() - 1)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.BCHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BCHAsset, cosmos.NewUint(150*common.One)),
			Memo:        NewRagnarokMemo(1).String(),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	c.Assert(insolvencyCheck(ctx, mgr, vault, common.NewCoins(), common.BCHChain), Equals, true)
}

// TestSolvencyOscillationPrevention tests how the solvency halt behaves when multiple
// vaults on the same chain report different solvency states. A solvent vault cannot
// unhalt the chain in the same block that an insolvent vault refreshed the halt.
// The protection only covers that block: a solvent report reaching consensus in a
// later block still unhalts the chain until the insolvent vault re-halts it (Step 5).
func (s *HandlerSolvencyTestSuite) TestSolvencyOscillationPrevention(c *C) {
	ctx, mgr := setupManagerForTest(c)
	height := int64(100)
	ctx = ctx.WithBlockHeight(height)

	// Create two vaults for the same chain
	vault1 := GetRandomVault()
	vault1.Chains = []string{common.ETHChain.String()}
	vault1.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)

	vault2 := GetRandomVault()
	vault2.Chains = []string{common.ETHChain.String()}
	vault2.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault2), IsNil)

	// Set up ETH pool so gap can be valued in RUNE
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(10000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Set RUNE price
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// Create 4 active nodes for consensus (need 2/4 = 50% > 33% threshold)
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}

	handler := NewSolvencyHandler(mgr)

	// Helper to run solvency message to consensus
	runToConsensus := func(vault Vault, walletBalance cosmos.Uint, chainHeight int64) {
		msg, err := NewMsgSolvency(common.ETHChain,
			vault.PubKey,
			common.NewCoins(common.NewCoin(common.ETHAsset, walletBalance)),
			chainHeight,
			nas[0].NodeAddress)
		c.Assert(err, IsNil)

		// First signer
		_, err = handler.handle(ctx, *msg)
		c.Assert(err, IsNil)

		// Second signer reaches consensus
		msg.Signer = nas[1].NodeAddress
		_, err = handler.handle(ctx, *msg)
		c.Assert(err, IsNil)
	}

	// Step 1: Vault1 reports insolvent (wallet has 100 ETH, vault has 1000 ETH)
	// This should halt the chain
	runToConsensus(vault1, cosmos.NewUint(100*common.One), 1000)

	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, height, Commentf("Chain should be halted at block %d", height))

	// Step 2: Move to next block
	height++
	ctx = ctx.WithBlockHeight(height)

	// Step 3: Vault1 reports insolvent again - this should refresh the halt height
	runToConsensus(vault1, cosmos.NewUint(100*common.One), 1001)

	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, height, Commentf("Halt should be refreshed to block %d", height))

	// Step 4: Vault2 reports solvent in the same block - this should NOT unhalt
	// because the halt was refreshed to the current block
	runToConsensus(vault2, cosmos.NewUint(1000*common.One), 1001)

	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, height, Commentf("Chain should still be halted - solvent vault should not unhalt when insolvent vault refreshed this block"))

	// Step 5: Move to next block - vault2 reports solvent first (before vault1 can refresh)
	height++
	ctx = ctx.WithBlockHeight(height)

	// Vault2 reports solvent - this COULD unhalt since halt is from previous block
	runToConsensus(vault2, cosmos.NewUint(1000*common.One), 1002)

	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	// The solvent vault unhalts because halt is from previous block and no insolvent vault has refreshed yet
	c.Assert(halt, Equals, int64(0), Commentf("Chain should be unhalted when solvent vault reports and halt is from previous block"))

	// Vault1 reports insolvent - this should re-halt
	runToConsensus(vault1, cosmos.NewUint(100*common.One), 1002)

	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, height, Commentf("Chain should be re-halted by insolvent vault"))

	// Step 6: Test that chain remains halted when insolvent vault keeps refreshing
	for i := 0; i < 3; i++ {
		height++
		ctx = ctx.WithBlockHeight(height)

		// Insolvent vault reports first (refreshes halt)
		runToConsensus(vault1, cosmos.NewUint(100*common.One), 1003+int64(i))

		halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt, Equals, height, Commentf("Halt should be refreshed to block %d", height))

		// Solvent vault reports - should NOT unhalt due to refresh
		runToConsensus(vault2, cosmos.NewUint(1000*common.One), 1003+int64(i))

		halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt, Equals, height, Commentf("Chain should remain halted at block %d", height))
	}

	// Step 7: Test that chain unhalts when insolvent vault becomes solvent
	height++
	ctx = ctx.WithBlockHeight(height)

	// The previously insolvent vault now reports solvent, which unhalts the chain on its own
	runToConsensus(vault1, cosmos.NewUint(1000*common.One), 1010)

	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0), Commentf("Chain should be unhalted when previously insolvent vault becomes solvent"))

	// Step 8: A manual indefinite halt ('1') is never touched by the solvency checker:
	// an insolvent vault must not convert it into a refreshable auto-halt height,
	// and a solvent vault must not lift it.
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 1)
	for i := 0; i < 2; i++ {
		height++
		ctx = ctx.WithBlockHeight(height)

		runToConsensus(vault1, cosmos.NewUint(100*common.One), 1011+int64(i))
		halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt, Equals, int64(1), Commentf("Insolvent vault must not overwrite a manual halt"))

		runToConsensus(vault2, cosmos.NewUint(1000*common.One), 1011+int64(i))
		halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt, Equals, int64(1), Commentf("Solvent vault must not lift a manual halt"))
	}
}
