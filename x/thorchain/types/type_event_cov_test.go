package types

import (
	"github.com/blang/semver"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type EventCovSuite struct{}

var _ = Suite(&EventCovSuite{})

type covEvent interface {
	Type() string
	Events() (cosmos.Events, error)
}

// eventAttrsCov flattens the attributes of the single emitted event.
func eventAttrsCov(c *C, e covEvent) map[string]string {
	evts, err := e.Events()
	c.Assert(err, IsNil)
	c.Assert(evts, HasLen, 1)
	c.Assert(evts[0].Type, Equals, e.Type())
	out := make(map[string]string, len(evts[0].Attributes))
	for _, a := range evts[0].Attributes {
		out[a.Key] = a.Value
	}
	return out
}

func checkAttrsCov(c *C, name string, got, want map[string]string) {
	for k, v := range want {
		gv, ok := got[k]
		c.Check(ok, Equals, true, Commentf("%s: missing attribute %s", name, k))
		c.Check(gv, Equals, v, Commentf("%s: attribute %s", name, k))
	}
}

func (s *EventCovSuite) TestEventConstructors(c *C) {
	txID := GetRandomTxHash()
	txID2 := GetRandomTxHash()
	btc := common.NewCoin(common.BTCAsset, cosmos.NewUint(10))
	eth := common.NewCoin(common.ETHAsset, cosmos.NewUint(20))
	thorAddr := GetRandomTHORAddress()
	btcAddr := GetRandomBTCAddress()
	acc := GetRandomBech32Addr()
	acc2 := GetRandomBech32Addr()
	acc3 := GetRandomBech32Addr()
	pk := GetRandomPubKey()

	type tc struct {
		name  string
		evt   covEvent
		typ   string
		attrs map[string]string
	}
	cases := []tc{
		{
			"limitSwap", NewEventLimitSwap(btc, eth, txID), "limit_swap",
			map[string]string{"source": btc.String(), "target": eth.String(), "txid": txID.String()},
		},
		{
			"modifyLimitSwap", NewEventModifyLimitSwap(btcAddr, btc, eth, cosmos.NewUint(5)), "limit_swap_mod",
			map[string]string{"from": btcAddr.String(), "source": btc.String(), "target": eth.String(), "modified_target_amount": "5"},
		},
		{
			"affiliateFee", NewEventAffiliateFee(txID, "memo", "name", thorAddr, common.BTCAsset, 25, cosmos.NewUint(1000), cosmos.NewUint(3)), "affiliate_fee",
			map[string]string{"tx_id": txID.String(), "memo": "memo", "thorname": "name", "rune_address": thorAddr.String(), "asset": "BTC.BTC", "gross_amount": "1000", "fee_bps": "25", "fee_amount": "3"},
		},
		{
			"revShare", NewEventRevShare("name", acc, cosmos.NewUint(7), -1, cosmos.NewUint(2)), "rev_share",
			map[string]string{"thorname": "name", "owner": acc.String(), "accrued_fee": "7", "bps": "0", "payout": "2"},
		},
		{
			"security", NewEventSecurity(common.Tx{ID: txID, Chain: common.BTCChain}, "bad"), "security",
			map[string]string{"msg": "bad", "id": txID.String(), "chain": "BTC"},
		},
		{
			"keygenSuccess", NewEventTssKeygenSuccess(pk, 10, []string{"a", "b"}), "tss_keygen_success",
			map[string]string{"pubkey": pk.String(), "height": "10", "members": "a, b"},
		},
		{
			"keygenFailure", NewEventTssKeygenFailure("why", "r1", true, 11, []string{"x"}), "tss_keygen_failure",
			map[string]string{"reason": "why", "round": "r1", "is_unicast": "true", "height": "11", "blame": "x"},
		},
		{
			"keygenMetric", NewEventTssKeygenMetric(pk, 99), "tss_keygen",
			map[string]string{"pubkey": pk.String(), "median_duration_ms": "99"},
		},
		{
			"keysignMetric", NewEventTssKeysignMetric(txID, 98), "tss_keysign",
			map[string]string{"txid": txID.String(), "median_duration_ms": "98"},
		},
		{
			"keysignFailure", NewEventTssKeysignFailure("why", "r2", false, 12, []string{"x", "y"}, "id1", pk), "tss_keysign_failure",
			map[string]string{"reason": "why", "round": "r2", "is_unicast": "false", "height": "12", "blame": "x, y", "id": "id1", "pubkey": pk.String()},
		},
		{
			"thorname", NewEventTHORName("MyName", common.BTCChain, btcAddr, cosmos.NewUint(1), cosmos.NewUint(2), 300, acc), "thorname",
			map[string]string{"name": "myname", "chain": "BTC", "address": btcAddr.String(), "registration_fee": "1", "fund_amount": "2", "expire": "300", "owner": acc.String()},
		},
		{
			"tradeDeposit", NewEventTradeAccountDeposit(cosmos.NewUint(4), common.BTCAsset, btcAddr, thorAddr, txID), "trade_account_deposit",
			map[string]string{"amount": "4", "asset": "BTC.BTC", "rune_address": thorAddr.String(), "asset_address": btcAddr.String(), "tx_id": txID.String()},
		},
		{
			"tradeWithdraw", NewEventTradeAccountWithdraw(cosmos.NewUint(4), common.BTCAsset, btcAddr, thorAddr, txID), "trade_account_withdraw",
			map[string]string{"amount": "4", "asset": "BTC.BTC", "rune_address": thorAddr.String(), "asset_address": btcAddr.String(), "tx_id": txID.String()},
		},
		{
			"securedDeposit", NewEventSecuredAssetDeposit(cosmos.NewUint(4), common.BTCAsset, btcAddr, thorAddr, txID), "secured_asset_deposit",
			map[string]string{"amount": "4", "asset": "BTC.BTC", "rune_address": thorAddr.String(), "asset_address": btcAddr.String(), "tx_id": txID.String()},
		},
		{
			"securedWithdraw", NewEventSecuredAssetWithdraw(cosmos.NewUint(4), common.BTCAsset, btcAddr, thorAddr, txID), "secured_asset_withdraw",
			map[string]string{"amount": "4", "asset": "BTC.BTC", "rune_address": thorAddr.String(), "asset_address": btcAddr.String(), "tx_id": txID.String()},
		},
		{
			"runePoolWithdraw", NewEventRUNEPoolWithdraw(acc, 5000, cosmos.NewUint(10), cosmos.NewUint(11), txID, thorAddr, 20, cosmos.NewUint(1)), "rune_pool_withdraw",
			map[string]string{"rune_address": acc.String(), "basis_points": "5000", "rune_amount": "10", "units": "11", "tx_id": txID.String(), "affiliate_address": thorAddr.String(), "affiliate_basis_points": "20", "affiliate_amount": "1"},
		},
		{
			"runePoolDeposit", NewEventRUNEPoolDeposit(acc, cosmos.NewUint(10), cosmos.NewUint(11), txID), "rune_pool_deposit",
			map[string]string{"rune_address": acc.String(), "rune_amount": "10", "units": "11", "tx_id": txID.String()},
		},
		{
			"setMimir", NewEventSetMimir("KEY", "1"), "set_mimir",
			map[string]string{"key": "KEY", "value": "1"},
		},
		{
			"mintBurn", NewEventMintBurn(MintBurnSupplyType_burn, "rune", cosmos.NewUint(3), "because"), "mint_burn",
			map[string]string{"supply": "burn", "denom": "rune", "amount": "3", "reason": "because"},
		},
		{
			"setNodeMimir", NewEventSetNodeMimir("KEY", "2", "thor1x"), "set_node_mimir",
			map[string]string{"key": "KEY", "value": "2", "address": "thor1x"},
		},
		{
			"version", NewEventVersion(semver.MustParse("1.2.3")), "version",
			map[string]string{"version": "1.2.3"},
		},
		{
			"switch", NewEventSwitch(cosmos.NewUint(4), common.BTCAsset, btcAddr, thorAddr, txID), "switch",
			map[string]string{"amount": "4", "asset": "BTC.BTC", "rune_address": thorAddr.String(), "asset_address": btcAddr.String(), "tx_id": txID.String()},
		},
		{
			"tcyDistribution", NewEventTCYDistribution(acc, cosmos.NewUint(8)), "tcy_distribution",
			map[string]string{"rune_address": acc.String(), "rune_amount": "8"},
		},
		{
			"tcyClaim", NewEventTCYClaim(thorAddr, btcAddr, cosmos.NewUint(9), common.BTCAsset), "tcy_claim",
			map[string]string{"rune_address": thorAddr.String(), "l1_address": btcAddr.String(), "asset": "BTC.BTC", "tcy_amount": "9"},
		},
		{
			"tcyStake", NewEventTCYStake(thorAddr, cosmos.NewUint(9)), "tcy_stake",
			map[string]string{"address": thorAddr.String(), "amount": "9"},
		},
		{
			"tcyUnstake", NewEventTCYUnstake(thorAddr, cosmos.NewUint(9)), "tcy_unstake",
			map[string]string{"address": thorAddr.String(), "amount": "9"},
		},
		{
			"operatorRotate", NewEventOperatorRotate(acc, acc2, acc3), "operator_rotate",
			map[string]string{"signer": acc.String(), "node_address": acc2.String(), "operator_address": acc3.String()},
		},
		{
			"oraclePrice", NewEventOraclePrice("BTC", "100000"), "oracle_price",
			map[string]string{"symbol": "BTC", "price": "100000"},
		},
		{
			"polReserveDeploy", NewEventPOLReserveDeploy(common.BTCAsset, cosmos.NewUint(6)), "pol_reserve_deploy",
			map[string]string{"pool": "BTC.BTC", "rune_amount": "6"},
		},
		{
			"failedOutboundRecovery", NewEventFailedOutboundRecovery(txID, btc, "refund", btcAddr, "memo"), "failed_outbound_recovery",
			map[string]string{"in_tx_id": txID.String(), "coin": btc.String(), "recovery_type": "refund", "destination": btcAddr.String(), "memo": "memo"},
		},
		{
			"vaultOverSolvency", NewEventVaultOverSolvency([]AssetOverSolvency{
				{Asset: common.BTCAsset, OverSolventAmount: cosmos.NewUint(5), SwapToReserveAmount: cosmos.NewUint(2)},
				{Asset: common.ETHAsset, OverSolventAmount: cosmos.NewUint(6), SwapToReserveAmount: cosmos.NewUint(3)},
			}, 150), "vault_over_solvency",
			map[string]string{
				"over_solvency_basis_points": "150",
				"asset_0_asset":              "BTC.BTC", "asset_0_over_solvent_amount": "5", "asset_0_swap_to_reserve_amount": "2",
				"asset_1_asset": "ETH.ETH", "asset_1_over_solvent_amount": "6", "asset_1_swap_to_reserve_amount": "3",
			},
		},
		{
			"pendingLiquiditySameTx", NewEventPendingLiquidity(common.BTCAsset, PendingLiquidityType_add, thorAddr, cosmos.NewUint(1), btcAddr, cosmos.NewUint(2), txID, txID), "pending_liquidity",
			map[string]string{"pool": "BTC.BTC", "type": "add", "rune_address": thorAddr.String(), "rune_amount": "1", "asset_amount": "2", "asset_address": btcAddr.String(), "BTC_txid": txID.String()},
		},
		{
			"pendingLiquidityTwoTx", NewEventPendingLiquidity(common.BTCAsset, PendingLiquidityType_withdraw, thorAddr, cosmos.NewUint(1), btcAddr, cosmos.NewUint(2), txID, txID2), "pending_liquidity",
			map[string]string{"type": "withdraw", "THOR_txid": txID.String(), "BTC_txid": txID2.String()},
		},
	}
	for _, t := range cases {
		c.Check(t.evt.Type(), Equals, t.typ, Commentf(t.name))
		checkAttrsCov(c, t.name, eventAttrsCov(c, t.evt), t.attrs)
	}

	// pending liquidity with a shared tx id does not emit a THOR txid,
	// and no txids at all when both are empty
	attrs := eventAttrsCov(c, NewEventPendingLiquidity(common.BTCAsset, PendingLiquidityType_add, thorAddr, cosmos.NewUint(1), btcAddr, cosmos.NewUint(2), txID, txID))
	_, ok := attrs["THOR_txid"]
	c.Check(ok, Equals, false)
	attrs = eventAttrsCov(c, NewEventPendingLiquidity(common.BTCAsset, PendingLiquidityType_add, thorAddr, cosmos.NewUint(1), btcAddr, cosmos.NewUint(2), "", ""))
	_, ok = attrs["BTC_txid"]
	c.Check(ok, Equals, false)

	// positive rev share bps is kept as-is
	c.Check(NewEventRevShare("n", acc, cosmos.ZeroUint(), 30, cosmos.ZeroUint()).Bps, Equals, int64(30))
}

func (s *EventCovSuite) TestEventStreamingSwap(c *C) {
	txID := GetRandomTxHash()
	swp := StreamingSwap{
		TxID: txID, Interval: 1, Quantity: 5, Count: 3, LastHeight: 77,
		Deposit: cosmos.NewUint(100), In: cosmos.NewUint(60), Out: cosmos.NewUint(30),
		FailedSwaps: []uint64{2, 4}, FailedSwapReasons: []string{"r1", "r2"},
	}
	evt := NewEventStreamingSwap(common.BTCAsset, common.ETHAsset, swp)
	c.Assert(evt.Deposit.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(evt.Out.Asset.Equals(common.ETHAsset), Equals, true)
	c.Assert(evt.Type(), Equals, "streaming_swap")
	checkAttrsCov(c, "streaming", eventAttrsCov(c, evt), map[string]string{
		"tx_id": txID.String(), "interval": "1", "quantity": "5", "count": "3", "last_height": "77",
		"deposit": "100 BTC.BTC", "in": "60 BTC.BTC", "out": "30 ETH.ETH",
		"failed_swaps": "2, 4", "failed_swap_reasons": "r1\n r2",
	})
}

func (s *EventCovSuite) TestEventScheduledOutbound(c *C) {
	pk := GetRandomPubKey()
	toAddr := GetRandomBTCAddress()
	in := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   toAddr,
		VaultPubKey: pk,
		Coin:        common.Coin{Asset: common.BTCAsset, Amount: cosmos.NewUint(50), Decimals: 8},
		Memo:        "OUT:x",
		MaxGas:      common.Gas{common.Coin{Asset: common.BTCAsset, Amount: cosmos.NewUint(3), Decimals: 8}},
		GasRate:     12,
		InHash:      in,
		ModuleName:  "asgard",
	}
	evt := NewEventScheduledOutbound(item)
	c.Assert(evt.Type(), Equals, "scheduled_outbound")
	checkAttrsCov(c, "scheduledOutbound", eventAttrsCov(c, evt), map[string]string{
		"chain": "BTC", "to_address": toAddr.String(), "vault_pub_key": pk.String(),
		"coin_asset": "BTC.BTC", "coin_amount": "50", "coin_decimals": "8", "memo": "OUT:x",
		"gas_rate": "12", "in_hash": in.String(), "out_hash": "", "module_name": "asgard",
		"max_gas_asset_0": "BTC.BTC", "max_gas_amount_0": "3", "max_gas_decimals_0": "8",
	})
}
