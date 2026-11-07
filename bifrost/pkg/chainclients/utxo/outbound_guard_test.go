package utxo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// existingOutboundFixture is a scheduled BTC outbound and a vault payment of it
// as the observer would decode it.
type existingOutboundFixture struct {
	item     stypes.TxOutItem
	payment  stypes.TxInItem
	paidHash string
}

func (s *BitcoinSignerSuite) newExistingOutboundFixture(c *C) existingOutboundFixture {
	inHash := types2.GetRandomTxHash()
	to, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	sender, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	s.client.asgardCache.Store(&utxo.AsgardCache{Addresses: []common.Address{sender}, FetchedAt: time.Now()})

	item := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   to,
		VaultPubKey: types2.GetRandomPubKey(),
		Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10_000))},
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))},
		Memo:        "OUT:" + inHash.String(),
		InHash:      inHash,
	}
	paidHash := types2.GetRandomTxHash().String()
	return existingOutboundFixture{
		item:     item,
		paidHash: paidHash,
		payment: stypes.TxInItem{
			Tx:     paidHash,
			Sender: sender.String(),
			To:     to.String(),
			Coins:  common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10_000))},
			Gas:    common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(500))},
			Memo:   item.Memo,
		},
	}
}

func (s *BitcoinSignerSuite) TestOutboundPaysItem(c *C) {
	f := s.newExistingOutboundFixture(c)
	c.Check(s.client.outboundPaysItem(f.payment, f.item), Equals, true)

	for name, mutate := range map[string]func(*stypes.TxInItem){
		"not sent by a vault": func(p *stypes.TxInItem) {
			other, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
			c.Assert(err, IsNil)
			p.Sender = other.String()
		},
		"memo names another inbound": func(p *stypes.TxInItem) { p.Memo = "OUT:" + types2.GetRandomTxHash().String() },
		"memo is not an outbound":    func(p *stypes.TxInItem) { p.Memo = "" },
		"another destination": func(p *stypes.TxInItem) {
			other, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
			c.Assert(err, IsNil)
			p.To = other.String()
		},
		"another amount": func(p *stypes.TxInItem) {
			p.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(9_000))}
		},
		"nothing decoded": func(p *stypes.TxInItem) { *p = stypes.TxInItem{} },
	} {
		payment := f.payment
		mutate(&payment)
		c.Check(s.client.outboundPaysItem(payment, f.item), Equals, false, Commentf("case %q", name))
	}

	// Paying less once gas is accounted for, at the same total spend, is still
	// this outbound.
	payment := f.payment
	payment.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(9_500))}
	payment.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_500))}
	c.Check(s.client.outboundPaysItem(payment, f.item), Equals, true)
}

func (s *BitcoinSignerSuite) TestOutboundPaysZECTexDestination(c *C) {
	f := s.newExistingOutboundFixture(c)
	previousChain := s.client.cfg.ChainID
	s.client.cfg.ChainID = common.ZECChain
	defer func() { s.client.cfg.ChainID = previousChain }()

	sender := common.Address("t1bgJzZzNhbXEgFcRo9XmJHdLadhEAbzFbh")
	if common.CurrentChainNetwork == common.MockNet {
		sender = common.Address("tmTX4KQUn6G2jpVosTsqW9xJ6Bcn3hLu8Fx")
	}
	s.client.asgardCache.Store(&utxo.AsgardCache{Addresses: []common.Address{sender}, FetchedAt: time.Now()})
	f.payment.Sender = sender.String()
	f.payment.Coins = common.Coins{common.NewCoin(common.ZECAsset, cosmos.NewUint(10_000))}
	f.payment.Gas = common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(500))}
	f.item.Coins = common.Coins{common.NewCoin(common.ZECAsset, cosmos.NewUint(10_000))}
	f.item.MaxGas = common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(1_000))}
	f.item.Chain = common.ZECChain
	tex, err := sender.ToTexAddress()
	c.Assert(err, IsNil)
	f.item.ToAddress = tex
	f.payment.To = sender.String()
	c.Check(s.client.outboundPaysItem(f.payment, f.item), Equals, true)

	f.item.ToAddress = sender
	f.payment.To = tex.String()
	c.Check(s.client.outboundPaysItem(f.payment, f.item), Equals, true)

	other, err := types2.GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	f.payment.To = other.String()
	c.Check(s.client.outboundPaysItem(f.payment, f.item), Equals, false)
}

func (s *BitcoinSignerSuite) TestOutboundAmountMatches(c *C) {
	u := cosmos.NewUint
	c.Check(outboundAmountMatches(u(10_000), u(1_000), u(10_000), u(500)), Equals, true)
	c.Check(outboundAmountMatches(u(10_000), u(1_000), u(10_500), u(500)), Equals, true)
	c.Check(outboundAmountMatches(u(10_000), u(1_000), u(9_500), u(1_500)), Equals, true)
	c.Check(outboundAmountMatches(u(10_000), u(1_000), u(9_499), u(1_500)), Equals, false)
	c.Check(outboundAmountMatches(u(10_000), u(1_000), u(11_001), u(0)), Equals, false)
}

func (s *BitcoinSignerSuite) TestCandidatePaysUniqueAction(c *C) {
	f := s.newExistingOutboundFixture(c)
	action := types2.TxOutItem{
		Chain: f.item.Chain, ToAddress: f.item.ToAddress, Coin: f.item.Coins[0],
		MaxGas: f.item.MaxGas, Memo: f.item.Memo, InHash: f.item.InHash,
	}
	c.Check(candidatePaysUniqueAction(f.payment, f.item, []types2.TxOutItem{action}), Equals, true)

	// The sibling has a different signer-cache key, but the same total spend
	// after gas adjustment. Neither leg may claim the other's payment.
	sibling := action
	sibling.Coin.Amount = cosmos.NewUint(9_500)
	c.Check(f.item.CacheHash() == (stypes.TxOutItem{
		Chain: sibling.Chain, ToAddress: sibling.ToAddress, Coins: common.Coins{sibling.Coin},
		Memo: sibling.Memo, InHash: sibling.InHash,
	}).CacheHash(), Equals, false)
	c.Check(candidatePaysUniqueAction(f.payment, f.item, []types2.TxOutItem{action, sibling}), Equals, false)
	siblingItem := f.item
	siblingItem.Coins = common.Coins{sibling.Coin}
	c.Check(candidatePaysUniqueAction(f.payment, siblingItem, []types2.TxOutItem{action, sibling}), Equals, false)

	// Both legs paid: the sibling's own payment fits only the sibling, so the
	// payment that fits both must be this leg's, whichever is seen first.
	legs := []types2.TxOutItem{action, sibling}
	siblingPayment := f.payment
	siblingPayment.Tx = "sibling"
	siblingPayment.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(9_500))}
	siblingPayment.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(400))}
	c.Check(paymentsCoverActions([]stypes.TxInItem{f.payment, siblingPayment}, f.item, legs), Equals, 0)
	c.Check(paymentsCoverActions([]stypes.TxInItem{siblingPayment, f.payment}, f.item, legs), Equals, 1)
	// The sibling's payment alone says nothing about this leg.
	c.Check(paymentsCoverActions([]stypes.TxInItem{siblingPayment}, f.item, legs), Equals, -1)

	// A memoless action keeps its memo as OriginalMemo, while a rescheduled
	// queue item can carry it as Memo again. THORChain matches them, and so
	// must the guard, whatever the case.
	memoless := action
	memoless.Memo = ""
	memoless.OriginalMemo = strings.ToLower(f.item.Memo)
	c.Check(candidatePaysUniqueAction(f.payment, f.item, []types2.TxOutItem{memoless}), Equals, true)
}

// Equal split parts to one destination share their payments: one payment
// lets the unpaid twin be signed, a payment for each part stops a third.
func (s *BitcoinSignerSuite) TestPaymentsCoverLookAlikeActions(c *C) {
	f := s.newExistingOutboundFixture(c)
	action := types2.TxOutItem{
		Chain: f.item.Chain, ToAddress: f.item.ToAddress, Coin: f.item.Coins[0],
		MaxGas: f.item.MaxGas, Memo: f.item.Memo, InHash: f.item.InHash,
	}
	twins := []types2.TxOutItem{action, action}
	other := f.payment
	other.Tx = "other"

	c.Check(paymentsCoverActions([]stypes.TxInItem{f.payment}, f.item, twins), Equals, -1)
	c.Check(paymentsCoverActions([]stypes.TxInItem{f.payment, other}, f.item, twins), Equals, 0)
	// A unique action is still stopped by its one payment.
	c.Check(paymentsCoverActions([]stypes.TxInItem{f.payment}, f.item, twins[:1]), Equals, 0)
	c.Check(paymentsCoverActions(nil, f.item, twins[:1]), Equals, -1)

	// A paid pair stays paid when a payment that also fits a second, partly
	// paid pair joins it.
	otherPair := action
	otherPair.Coin.Amount = cosmos.NewUint(9_500)
	pay := func(hash string, amount, gas uint64) stypes.TxInItem {
		payment := f.payment
		payment.Tx = hash
		payment.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(amount))}
		payment.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(gas))}
		return payment
	}
	a, b, spanning := pay("a", 10_000, 300), pay("b", 10_000, 300), pay("spanning", 9_500, 1_500)
	pairs := []types2.TxOutItem{action, action, otherPair, otherPair}
	c.Check(paymentsCoverActions([]stypes.TxInItem{a, b, spanning}, f.item, pairs), Equals, 0)
	c.Check(paymentsCoverActions([]stypes.TxInItem{a, spanning}, f.item, pairs), Equals, -1)
}

func (s *BitcoinSignerSuite) TestMatchExistingOutboundCountsTwinPayments(c *C) {
	f := s.newExistingOutboundFixture(c)
	action := types2.TxOutItem{
		Chain: f.item.Chain, ToAddress: f.item.ToAddress, Coin: f.item.Coins[0],
		MaxGas: f.item.MaxGas, Memo: f.item.Memo, InHash: f.item.InHash,
	}
	twins := []types2.TxOutItem{action, action}
	first := f.paidHash
	second := types2.GetRandomTxHash().String()
	payments := map[string]stypes.TxInItem{first: f.payment}
	s.client.outboundLookup = func(hash string, _ common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		payment, ok := payments[hash]
		if !ok {
			return stypes.TxInItem{}, signercache.LookupNotFound
		}
		return payment, signercache.LookupFound
	}
	defer func() { s.client.outboundLookup = nil }()
	candidates := []outboundCandidate{{hash: first, selfLocal: true}, {hash: second, selfLocal: true}}

	// One twin paid: the other may still be signed.
	_, result := s.client.matchExistingOutboundForActions(f.item, candidates, twins)
	c.Check(result, Equals, signercache.LookupNotFound)

	// Both twins paid: no third payment.
	secondPayment := f.payment
	secondPayment.Tx = second
	payments[second] = secondPayment
	hash, result := s.client.matchExistingOutboundForActions(f.item, candidates, twins)
	c.Check(result, Equals, signercache.LookupFound)
	c.Check(hash, Equals, first)
}

// With unequal legs both paid, the sibling's own payment is kept even though it
// cannot pay the leg being signed: it shows that the payment fitting both legs
// is this leg's, so a second payment is not signed.
func (s *BitcoinSignerSuite) TestMatchExistingOutboundCountsSiblingPayment(c *C) {
	f := s.newExistingOutboundFixture(c)
	action := types2.TxOutItem{
		Chain: f.item.Chain, ToAddress: f.item.ToAddress, Coin: f.item.Coins[0],
		MaxGas: f.item.MaxGas, Memo: f.item.Memo, InHash: f.item.InHash,
	}
	sibling := action
	sibling.Coin.Amount = cosmos.NewUint(9_500)
	legs := []types2.TxOutItem{action, sibling}
	siblingHash := types2.GetRandomTxHash().String()
	siblingPayment := f.payment
	siblingPayment.Tx = siblingHash
	siblingPayment.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(9_500))}
	siblingPayment.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(400))}
	payments := map[string]stypes.TxInItem{siblingHash: siblingPayment}
	s.client.outboundLookup = func(hash string, _ common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		payment, ok := payments[hash]
		if !ok {
			return stypes.TxInItem{}, signercache.LookupNotFound
		}
		return payment, signercache.LookupFound
	}
	defer func() { s.client.outboundLookup = nil }()
	candidates := []outboundCandidate{{hash: siblingHash, selfLocal: true}, {hash: f.paidHash, selfLocal: true}}

	// Only the sibling paid: this leg may be signed.
	_, result := s.client.matchExistingOutboundForActions(f.item, candidates, legs)
	c.Check(result, Equals, signercache.LookupNotFound)

	// Both legs paid.
	payments[f.paidHash] = f.payment
	hash, result := s.client.matchExistingOutboundForActions(f.item, candidates, legs)
	c.Check(result, Equals, signercache.LookupFound)
	c.Check(hash, Equals, f.paidHash)
}

func (s *BitcoinSignerSuite) TestOutboundGuardApplies(c *C) {
	inHash := types2.GetRandomTxHash()
	for _, tc := range []struct {
		memo         string
		originalMemo string
		inHash       common.TxID
		applies      bool
	}{
		{"OUT:" + inHash.String(), "", inHash, true},
		{"REFUND:" + inHash.String(), "", inHash, true},
		{"OUT:" + types2.GetRandomTxHash().String(), "", inHash, false},
		{"", "", inHash, false},
		// A memoless outbound is not guarded: its payment names no inbound.
		{"", "OUT:" + inHash.String(), inHash, false},
		{"MIGRATE:10", "", common.BlankTxID, false},
		{"OUT:" + inHash.String(), "", "", false},
	} {
		tx := stypes.TxOutItem{Memo: tc.memo, OriginalMemo: tc.originalMemo, InHash: tc.inHash}
		c.Check(outboundGuardApplies(tx), Equals, tc.applies, Commentf("memo %q", tc.memo))
	}
}

func (s *BitcoinSignerSuite) TestMatchExistingOutbound(c *C) {
	f := s.newExistingOutboundFixture(c)
	unrelated := f.payment
	unrelated.Memo = "OUT:" + types2.GetRandomTxHash().String()
	lookups := map[string]struct {
		observed stypes.TxInItem
		presence signercache.LookupResult
	}{}
	s.client.outboundLookup = func(hash string, _ common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		l, ok := lookups[hash]
		if !ok {
			return stypes.TxInItem{}, signercache.LookupNotFound
		}
		return l.observed, l.presence
	}
	defer func() { s.client.outboundLookup = nil }()

	live := f.paidHash
	gone := types2.GetRandomTxHash().String()
	unreadable := types2.GetRandomTxHash().String()
	other := types2.GetRandomTxHash().String()
	lookups[live] = struct {
		observed stypes.TxInItem
		presence signercache.LookupResult
	}{f.payment, signercache.LookupFound}
	lookups[unreadable] = struct {
		observed stypes.TxInItem
		presence signercache.LookupResult
	}{stypes.TxInItem{}, signercache.LookupAmbiguous}
	empty := types2.GetRandomTxHash().String()
	lookups[empty] = struct {
		observed stypes.TxInItem
		presence signercache.LookupResult
	}{stypes.TxInItem{}, signercache.LookupFound}
	lookups[other] = struct {
		observed stypes.TxInItem
		presence signercache.LookupResult
	}{unrelated, signercache.LookupFound}

	for _, tc := range []struct {
		name       string
		candidates []outboundCandidate
		hash       string
		result     signercache.LookupResult
	}{
		{"no candidates", nil, "", signercache.LookupNotFound},
		{"a live payment", []outboundCandidate{{hash: gone, selfLocal: true}, {hash: live, selfLocal: true}}, live, signercache.LookupFound},
		{"no longer on chain", []outboundCandidate{{hash: gone, selfLocal: true}}, "", signercache.LookupNotFound},
		{"an unrelated transaction", []outboundCandidate{{hash: other, selfLocal: true}}, "", signercache.LookupNotFound},
		{"not a transaction hash", []outboundCandidate{{hash: "not-a-hash", selfLocal: true}}, "", signercache.LookupNotFound},
		{"an unreadable local record", []outboundCandidate{{hash: unreadable, selfLocal: true}}, unreadable, signercache.LookupAmbiguous},
		{"an empty local decode", []outboundCandidate{{hash: empty, selfLocal: true}}, empty, signercache.LookupAmbiguous},
		{"an unreadable unverified claim", []outboundCandidate{{hash: unreadable}}, "", signercache.LookupNotFound},
		{"a live payment after an unreadable record", []outboundCandidate{{hash: unreadable, selfLocal: true}, {hash: live}}, live, signercache.LookupFound},
	} {
		hash, result := s.client.matchExistingOutbound(f.item, tc.candidates)
		c.Check(result, Equals, tc.result, Commentf("case %q", tc.name))
		c.Check(hash, Equals, tc.hash, Commentf("case %q", tc.name))
	}
}

// Signing stops when a vault's payment of the same outbound, recorded by this
// node's observer, is live on chain, whatever vault this attempt is scheduled on.
func (s *BitcoinSignerSuite) TestSignTxSkipsOutboundAlreadyPaid(c *C) {
	f := s.newExistingOutboundFixture(c)
	action := types2.TxOutItem{Chain: f.item.Chain, ToAddress: f.item.ToAddress, Coin: f.item.Coins[0], MaxGas: f.item.MaxGas, Memo: f.item.Memo, InHash: f.item.InHash}
	actionsJSON, err := json.Marshal([]types2.TxOutItem{action})
	c.Assert(err, IsNil)
	s.txDetailsBody = fmt.Sprintf(`{"tx":{},"txs":[],"actions":%s,"out_txs":[]}`, actionsJSON)
	defer func() { s.txDetailsBody = "" }()
	// An unusable vault key makes any attempt that gets past the guard fail
	// straight away, when the source script is derived.
	f.item.VaultPubKey = common.PubKey("helloworld")

	// Without a recorded payment the attempt gets past the guard.
	_, _, _, err = s.client.SignTx(f.item, 1)
	c.Assert(err, NotNil)

	c.Assert(s.client.signerCacheManager.RecordObservedOutbound(f.item.InHash.String(), f.paidHash), IsNil)
	s.client.outboundLookup = func(hash string, _ common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		if hash == f.paidHash {
			return f.payment, signercache.LookupFound
		}
		return stypes.TxInItem{}, signercache.LookupNotFound
	}
	defer func() { s.client.outboundLookup = nil }()
	result, checkpoint, obs, err := s.client.SignTx(f.item, 1)
	c.Assert(err, IsNil)
	c.Check(result, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(obs, IsNil)

	// Once that payment has left the chain, signing goes ahead again.
	s.client.outboundLookup = func(string, common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		return stypes.TxInItem{}, signercache.LookupNotFound
	}
	_, _, _, err = s.client.SignTx(f.item, 1)
	c.Assert(err, NotNil)
}

// Outbounds other nodes reported to THORChain are candidates too, but only a
// payment this node's own daemon confirms stops signing.
func (s *BitcoinSignerSuite) TestSignTxChecksReportedOutbounds(c *C) {
	f := s.newExistingOutboundFixture(c)
	action := types2.TxOutItem{Chain: f.item.Chain, ToAddress: f.item.ToAddress, Coin: f.item.Coins[0], MaxGas: f.item.MaxGas, Memo: f.item.Memo, InHash: f.item.InHash}
	actionsJSON, err := json.Marshal([]types2.TxOutItem{action})
	c.Assert(err, IsNil)
	f.item.VaultPubKey = common.PubKey("helloworld")
	fake := types2.GetRandomTxHash().String()
	s.txDetailsBody = fmt.Sprintf(`{"tx":{},"txs":[],"actions":%s,"out_txs":[],"observed_outbound_tx_ids":[%q,%q]}`, actionsJSON, fake, f.paidHash)
	defer func() { s.txDetailsBody = "" }()

	var looked []string
	s.client.outboundLookup = func(hash string, _ common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		looked = append(looked, hash)
		if hash == f.paidHash {
			return f.payment, signercache.LookupFound
		}
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	defer func() { s.client.outboundLookup = nil }()

	result, _, _, err := s.client.SignTx(f.item, 1)
	c.Assert(err, IsNil)
	c.Check(result, IsNil)
	c.Check(looked, DeepEquals, []string{fake, f.paidHash})

	// A report this node cannot confirm on chain does not stop signing.
	s.txDetailsBody = fmt.Sprintf(`{"tx":{},"txs":[],"actions":%s,"out_txs":[],"observed_outbound_tx_ids":[%q]}`, actionsJSON, fake)
	s.client.outboundLookup = func(string, common.TxID) (stypes.TxInItem, signercache.LookupResult) {
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	_, _, _, err = s.client.SignTx(f.item, 2)
	c.Assert(err, NotNil)
}

// rawTxWithMemo is a getrawtransaction answer for a confirmed transaction with
// three inputs, one payment and, unless memo is empty, an OP_RETURN memo.
func rawTxWithMemo(c *C, memo string) string {
	vout := []map[string]any{{
		"value": 0.0001,
		"n":     0,
		"scriptPubKey": map[string]any{
			"hex":  "76a914f3b40cad194d48398755b45036421b1a645bbdcc88ac",
			"type": "pubkeyhash",
		},
	}}
	if memo != "" {
		c.Assert(len(memo) < 0x4c, Equals, true)
		vout = append(vout, map[string]any{
			"value": 0,
			"n":     1,
			"scriptPubKey": map[string]any{
				"hex":  fmt.Sprintf("6a%02x%x", len(memo), memo),
				"type": "nulldata",
			},
		})
	}
	var vin []map[string]any
	for i := 0; i < 3; i++ {
		vin = append(vin, map[string]any{"txid": types2.GetRandomTxHash().String(), "vout": 0, "sequence": 4294967295})
	}
	buf, err := json.Marshal(map[string]any{
		"result": map[string]any{
			"txid":          types2.GetRandomTxHash().String(),
			"hash":          types2.GetRandomTxHash().String(),
			"blockhash":     types2.GetRandomTxHash().String(),
			"confirmations": 1,
			"vin":           vin,
			"vout":          vout,
		},
		"error": nil,
		"id":    1,
	})
	c.Assert(err, IsNil)
	return string(buf)
}

// A reported candidate whose own memo cannot name the inbound is rejected from
// the one read of the transaction, before decoding it reads the transactions
// behind its inputs.
func (s *BitcoinSignerSuite) TestLookupOutboundCandidateChecksMemoFirst(c *C) {
	inHash := types2.GetRandomTxHash()
	defer func() { s.rawTxBody = "" }()
	hash := types2.GetRandomTxHash().String()

	for name, memo := range map[string]string{
		"memo names another inbound": "OUT:" + types2.GetRandomTxHash().String(),
		"no memo":                    "",
		"memo is not an outbound":    "hello",
	} {
		s.rawTxBody = rawTxWithMemo(c, memo)
		s.rawTxCalls = 0
		_, presence := s.client.lookupOutboundCandidate(hash, inHash)
		c.Check(presence, Equals, signercache.LookupNotFound, Commentf("case %q", name))
		c.Check(s.rawTxCalls, Equals, 1, Commentf("case %q", name))
	}

	// A memo that names the inbound is still decoded in full.
	s.rawTxBody = rawTxWithMemo(c, "OUT:"+inHash.String())
	s.rawTxCalls = 0
	s.client.lookupOutboundCandidate(hash, inHash)
	c.Check(s.rawTxCalls > 1, Equals, true)
}

// A reported candidate not sent by a vault is rejected from its sender's read,
// before decoding reads the transaction behind every input.
func (s *BitcoinSignerSuite) TestLookupOutboundCandidateChecksSenderFirst(c *C) {
	inHash := types2.GetRandomTxHash()
	defer func() { s.rawTxBody = "" }()
	s.rawTxBody = rawTxWithMemo(c, "OUT:"+inHash.String())
	s.rawTxCalls = 0
	_, presence := s.client.lookupOutboundCandidate(types2.GetRandomTxHash().String(), inHash)
	c.Check(presence, Equals, signercache.LookupNotFound)
	c.Check(s.rawTxCalls, Equals, 2)
}

// However many candidates are reported, one signing attempt spends a bounded
// number of daemon reads on them.
func (s *BitcoinSignerSuite) TestOutboundGuardBoundsDaemonReads(c *C) {
	inHash := types2.GetRandomTxHash()
	defer func() { s.rawTxBody = "" }()
	s.rawTxBody = rawTxWithMemo(c, "OUT:"+inHash.String())
	s.rawTxCalls = 0
	var candidates []outboundCandidate
	for i := 0; i < maxOutboundGuardDaemonReads; i++ {
		candidates = append(candidates, outboundCandidate{hash: types2.GetRandomTxHash().String()})
	}
	item := stypes.TxOutItem{InHash: inHash, Memo: "OUT:" + inHash.String()}
	_, result := s.client.matchExistingOutbound(item, candidates)
	c.Check(result, Equals, signercache.LookupNotFound)
	c.Check(s.rawTxCalls <= maxOutboundGuardDaemonReads, Equals, true, Commentf("%d reads", s.rawTxCalls))

	// A local record the budget no longer reaches is inconclusive.
	local := outboundCandidate{hash: types2.GetRandomTxHash().String(), selfLocal: true}
	s.rawTxCalls = 0
	_, result = s.client.matchExistingOutbound(item, append(candidates, local))
	c.Check(result, Equals, signercache.LookupAmbiguous)
}

func (s *BitcoinSignerSuite) TestHistoricalVaultSenderOutput(c *C) {
	var response struct {
		Result btcjson.TxRawResult `json:"result"`
	}
	c.Assert(json.Unmarshal([]byte(rawTxWithMemo(c, "")), &response), IsNil)
	// The sender has aged out of the current Asgard list. A locally recorded
	// outbound still needs its customer output decoded from the daemon.
	_, err := s.client.getOutput("historical-vault", &response.Result, false)
	c.Check(err, NotNil)
	output, err := s.client.getOutputWithVaultSender("historical-vault", &response.Result, false, true)
	c.Assert(err, IsNil)
	c.Check(output.Value, Equals, 0.0001)
}
