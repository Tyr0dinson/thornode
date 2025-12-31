package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgIntentSuite struct{}

var _ = Suite(&MsgIntentSuite{})

// === MsgIntentOpen Tests ===

func (MsgIntentSuite) TestMsgIntentOpen(c *C) {
	addr := GetRandomBech32Addr()
	c.Check(addr.Empty(), Equals, false)
	ethAddress := GetRandomETHAddress()
	thorAddress := GetRandomTHORAddress()
	txID := GetRandomTxHash()
	c.Check(txID.IsEmpty(), Equals, false)

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"=:ETH.ETH:...",
	)

	// Test valid message - happy path
	m := NewMsgIntentOpen(
		tx,
		common.ETHAsset,
		ethAddress,
		cosmos.NewUint(200*common.One),
		cosmos.NewUint(100*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		addr,
	)
	EnsureMsgBasicCorrect(m, c)

	// Test with affiliate address and basis points
	m = NewMsgIntentOpen(
		tx,
		common.ETHAsset,
		ethAddress,
		cosmos.NewUint(200*common.One),
		cosmos.NewUint(100*common.One),
		thorAddress,
		cosmos.NewUint(500),
		addr,
	)
	c.Assert(m.ValidateBasic(), IsNil)

	// Test with high target equal to low target (boundary case)
	m = NewMsgIntentOpen(
		tx,
		common.ETHAsset,
		ethAddress,
		cosmos.NewUint(100*common.One),
		cosmos.NewUint(100*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		addr,
	)
	c.Assert(m.ValidateBasic(), IsNil)

	// Test invalid cases
	inputs := []struct {
		name        string
		tx          common.Tx
		targetAsset common.Asset
		targetAddr  common.Address
		highTarget  cosmos.Uint
		lowTarget   cosmos.Uint
		affAddr     common.Address
		affBps      cosmos.Uint
		signer      cosmos.AccAddress
	}{
		{
			name:        "empty signer",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      cosmos.AccAddress{},
		},
		{
			name:        "empty target asset",
			tx:          tx,
			targetAsset: common.EmptyAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "invalid tx - empty tx id",
			tx:          common.NewTx(common.TxID(""), GetRandomETHAddress(), GetRandomETHAddress(), common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}, common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}, "=:ETH.ETH:..."),
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "zero high target",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.ZeroUint(),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "zero low target",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.ZeroUint(),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "high target less than low target",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(50 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "empty target address",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  common.NoAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "target address chain mismatch",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  GetRandomBTCAddress(),
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.ZeroUint(),
			signer:      addr,
		},
		{
			name:        "affiliate address not THOR chain",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     ethAddress,
			affBps:      cosmos.NewUint(500),
			signer:      addr,
		},
		{
			name:        "empty affiliate address with non-zero basis points",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     common.NoAddress,
			affBps:      cosmos.NewUint(100),
			signer:      addr,
		},
		{
			name:        "affiliate basis points exceeds maximum",
			tx:          tx,
			targetAsset: common.ETHAsset,
			targetAddr:  ethAddress,
			highTarget:  cosmos.NewUint(200 * common.One),
			lowTarget:   cosmos.NewUint(100 * common.One),
			affAddr:     thorAddress,
			affBps:      cosmos.NewUint(MaxAffiliateFeeBasisPoints + 1),
			signer:      addr,
		},
	}

	for _, item := range inputs {
		c.Logf("Test case: %s", item.name)
		m = NewMsgIntentOpen(item.tx, item.targetAsset, item.targetAddr, item.highTarget, item.lowTarget, item.affAddr, item.affBps, item.signer)
		c.Assert(m.ValidateBasic(), NotNil, Commentf("Expected error for: %s", item.name))
	}
}

func (MsgIntentSuite) TestNewMsgIntentOpen(c *C) {
	addr := GetRandomBech32Addr()
	txID := GetRandomTxHash()
	ethAddress := GetRandomETHAddress()
	thorAddress := GetRandomTHORAddress()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"=:ETH.ETH:...",
	)

	highTarget := cosmos.NewUint(200 * common.One)
	lowTarget := cosmos.NewUint(100 * common.One)
	affBps := cosmos.NewUint(500)

	msg := NewMsgIntentOpen(
		tx,
		common.ETHAsset,
		ethAddress,
		highTarget,
		lowTarget,
		thorAddress,
		affBps,
		addr,
	)

	// Verify all fields are correctly set
	c.Assert(msg.Tx.ID.Equals(txID), Equals, true)
	c.Assert(msg.TargetAsset.Equals(common.ETHAsset), Equals, true)
	c.Assert(msg.TargetAddress.Equals(ethAddress), Equals, true)
	c.Assert(msg.HighTarget.Equal(highTarget), Equals, true)
	c.Assert(msg.LowTarget.Equal(lowTarget), Equals, true)
	c.Assert(msg.AffiliateAddress.Equals(thorAddress), Equals, true)
	c.Assert(msg.AffiliateBasisPoints.Equal(affBps), Equals, true)
	c.Assert(msg.Signer.Equals(addr), Equals, true)

	// Verify GetSigners
	signers := msg.GetSigners()
	c.Assert(len(signers), Equals, 1)
	c.Assert(signers[0].Equals(addr), Equals, true)
}

// === MsgIntentCancel Tests ===

func (MsgIntentSuite) TestMsgIntentCancel(c *C) {
	addr := GetRandomBech32Addr()
	c.Check(addr.Empty(), Equals, false)
	txID := GetRandomTxHash()
	intentTxID := GetRandomTxHash()
	c.Check(txID.IsEmpty(), Equals, false)

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"-:intenttxid",
	)

	// Test valid message - happy path
	m := NewMsgIntentCancel(tx, intentTxID, addr)
	EnsureMsgBasicCorrect(m, c)

	// Test invalid cases
	inputs := []struct {
		name       string
		tx         common.Tx
		intentTxID common.TxID
		signer     cosmos.AccAddress
	}{
		{
			name:       "empty signer",
			tx:         tx,
			intentTxID: intentTxID,
			signer:     cosmos.AccAddress{},
		},
		{
			name:       "invalid tx - empty tx id",
			tx:         common.NewTx(common.TxID(""), GetRandomETHAddress(), GetRandomETHAddress(), common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))}, common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}, "-:intenttxid"),
			intentTxID: intentTxID,
			signer:     addr,
		},
		{
			name:       "empty intent tx id",
			tx:         tx,
			intentTxID: common.TxID(""),
			signer:     addr,
		},
	}

	for _, item := range inputs {
		c.Logf("Test case: %s", item.name)
		m = NewMsgIntentCancel(item.tx, item.intentTxID, item.signer)
		c.Assert(m.ValidateBasic(), NotNil, Commentf("Expected error for: %s", item.name))
	}
}

func (MsgIntentSuite) TestNewMsgIntentCancel(c *C) {
	addr := GetRandomBech32Addr()
	txID := GetRandomTxHash()
	intentTxID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"-:intenttxid",
	)

	msg := NewMsgIntentCancel(tx, intentTxID, addr)

	// Verify all fields are correctly set
	c.Assert(msg.Tx.ID.Equals(txID), Equals, true)
	c.Assert(msg.IntentTxID.Equals(intentTxID), Equals, true)
	c.Assert(msg.Signer.Equals(addr), Equals, true)

	// Verify GetSigners
	signers := msg.GetSigners()
	c.Assert(len(signers), Equals, 1)
	c.Assert(signers[0].Equals(addr), Equals, true)
}

// === MsgIntentSolve Tests ===

func (MsgIntentSuite) TestMsgIntentSolve(c *C) {
	addr := GetRandomBech32Addr()
	c.Check(addr.Empty(), Equals, false)
	txID := GetRandomTxHash()
	intentTxID := GetRandomTxHash()
	c.Check(txID.IsEmpty(), Equals, false)

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"+:intenttxid",
	)

	// Test valid message - happy path
	m := NewMsgIntentSolve(tx, intentTxID, addr)
	EnsureMsgBasicCorrect(m, c)

	// Test invalid cases
	inputs := []struct {
		name       string
		tx         common.Tx
		intentTxID common.TxID
		signer     cosmos.AccAddress
	}{
		{
			name:       "empty signer",
			tx:         tx,
			intentTxID: intentTxID,
			signer:     cosmos.AccAddress{},
		},
		{
			name:       "invalid tx - empty tx id",
			tx:         common.NewTx(common.TxID(""), GetRandomETHAddress(), GetRandomETHAddress(), common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}, common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}, "+:intenttxid"),
			intentTxID: intentTxID,
			signer:     addr,
		},
		{
			name:       "empty intent tx id",
			tx:         tx,
			intentTxID: common.TxID(""),
			signer:     addr,
		},
	}

	for _, item := range inputs {
		c.Logf("Test case: %s", item.name)
		m = NewMsgIntentSolve(item.tx, item.intentTxID, item.signer)
		c.Assert(m.ValidateBasic(), NotNil, Commentf("Expected error for: %s", item.name))
	}
}

func (MsgIntentSuite) TestNewMsgIntentSolve(c *C) {
	addr := GetRandomBech32Addr()
	txID := GetRandomTxHash()
	intentTxID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"+:intenttxid",
	)

	msg := NewMsgIntentSolve(tx, intentTxID, addr)

	// Verify all fields are correctly set
	c.Assert(msg.Tx.ID.Equals(txID), Equals, true)
	c.Assert(msg.IntentTxID.Equals(intentTxID), Equals, true)
	c.Assert(msg.Signer.Equals(addr), Equals, true)

	// Verify GetSigners
	signers := msg.GetSigners()
	c.Assert(len(signers), Equals, 1)
	c.Assert(signers[0].Equals(addr), Equals, true)
}

// === MsgIntentAuctionBid Tests ===

func (MsgIntentSuite) TestMsgIntentAuctionBid(c *C) {
	addr := GetRandomBech32Addr()
	c.Check(addr.Empty(), Equals, false)
	txID := GetRandomTxHash()
	intentTxID := GetRandomTxHash()
	c.Check(txID.IsEmpty(), Equals, false)

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"~:intenttxid",
	)

	// Test valid message - happy path
	m := NewMsgIntentAuctionBid(tx, intentTxID, addr)
	EnsureMsgBasicCorrect(m, c)

	// Test invalid cases
	inputs := []struct {
		name       string
		tx         common.Tx
		intentTxID common.TxID
		signer     cosmos.AccAddress
	}{
		{
			name:       "empty signer",
			tx:         tx,
			intentTxID: intentTxID,
			signer:     cosmos.AccAddress{},
		},
		{
			name:       "invalid tx - empty tx id",
			tx:         common.NewTx(common.TxID(""), GetRandomETHAddress(), GetRandomETHAddress(), common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))}, common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}, "~:intenttxid"),
			intentTxID: intentTxID,
			signer:     addr,
		},
		{
			name:       "empty intent tx id",
			tx:         tx,
			intentTxID: common.TxID(""),
			signer:     addr,
		},
	}

	for _, item := range inputs {
		c.Logf("Test case: %s", item.name)
		m = NewMsgIntentAuctionBid(item.tx, item.intentTxID, item.signer)
		c.Assert(m.ValidateBasic(), NotNil, Commentf("Expected error for: %s", item.name))
	}
}

func (MsgIntentSuite) TestNewMsgIntentAuctionBid(c *C) {
	addr := GetRandomBech32Addr()
	txID := GetRandomTxHash()
	intentTxID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"~:intenttxid",
	)

	msg := NewMsgIntentAuctionBid(tx, intentTxID, addr)

	// Verify all fields are correctly set
	c.Assert(msg.Tx.ID.Equals(txID), Equals, true)
	c.Assert(msg.IntentTxID.Equals(intentTxID), Equals, true)
	c.Assert(msg.Signer.Equals(addr), Equals, true)

	// Verify GetSigners
	signers := msg.GetSigners()
	c.Assert(len(signers), Equals, 1)
	c.Assert(signers[0].Equals(addr), Equals, true)
}
