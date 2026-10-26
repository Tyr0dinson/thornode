package utxo

import (
	"github.com/btcsuite/btcd/wire"
	. "gopkg.in/check.v1"
)

// checkSpendsAmountPlusFee asserts the transaction pays the customer exactly refund,
// returns the remainder to the vault, and returns the fee it pays.
//
// Backport shim for mainnet-3.21.1: on develop this helper lives in
// utxo_maxgas_shortfall_test.go, which is not part of this branch. The BTC
// fee-floor tests ported here (bitcoin_signer_test.go) call it, so it is defined
// here to keep the port minimal without pulling the whole max-gas suite.
func checkSpendsAmountPlusFee(c *C, redeemTx *wire.MsgTx, inbound int64, refund uint64, script []byte) int64 {
	c.Assert(redeemTx.TxIn, HasLen, 1)
	// customer, change back to the vault, memo
	c.Assert(redeemTx.TxOut, HasLen, 3)
	c.Check(redeemTx.TxOut[0].Value, Equals, int64(refund))
	c.Check(redeemTx.TxOut[1].PkScript, DeepEquals, script)
	c.Check(redeemTx.TxOut[2].Value, Equals, int64(0))
	var out int64
	for _, o := range redeemTx.TxOut {
		out += o.Value
	}
	fee := inbound - out
	c.Check(redeemTx.TxOut[1].Value, Equals, inbound-int64(refund)-fee)
	return fee
}
