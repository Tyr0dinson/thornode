package utxo

import (
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	. "gopkg.in/check.v1"
)

func (s *FiroSuite) TestFiroMempoolSnapshotAcrossBlockCleanup(c *C) {
	for _, cacheSize := range []int{0, 10} {
		func() {
			client, f, cleanup := newFiroFixture(c, cacheSize)
			defer cleanup()
			parent := f.funding(200, 12, 2, f.vault)
			tx := f.funding(201, 0, 1, f.user)
			tx.Vin = []btcjson.Vin{{Txid: parent.Txid}}
			tx.BlockHash, tx.Confirmations, tx.ChainLock = "", 0, false
			f.mempool = []string{tx.Txid}
			first, err := client.FetchMemPool(98)
			c.Assert(err, IsNil)
			c.Assert(first.TxArray, HasLen, 1) // an unlocked outbound still progresses
			c.Assert(first.MemPool, Equals, true)
			c.Assert(client.GetConfirmationCount(first), Equals, int64(0))
			c.Assert(client.ConfirmationCountReady(first), Equals, true)

			snapshot, resume := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}()
			f.mempoolSnapshot = func() []string {
				ids := append([]string(nil), f.mempool...)
				close(snapshot)
				<-resume
				return ids
			}
			type result struct {
				batch types.TxIn
				err   error
			}
			done := make(chan result, 1)
			go func() {
				batch, fetchErr := client.FetchMemPool(99)
				done <- result{batch, fetchErr}
			}()
			select {
			case <-snapshot:
			case <-time.After(5 * time.Second):
				c.Fatal("mempool snapshot was not requested")
			}
			tx.Height, tx.BlockHash, tx.ChainLock, tx.Confirmations = 99, firoHash(99), true, 1
			block, err := client.getBlockFIRO(99)
			c.Assert(err, IsNil)
			mined, err := client.extractTxs(block)
			c.Assert(err, IsNil)
			c.Assert(mined.TxArray, HasLen, 1)
			close(resume) // return the stale list after the block removed its work marker
			select {
			case got := <-done:
				c.Assert(got.err, IsNil)
				c.Assert(got.batch.TxArray, HasLen, 0)
			case <-time.After(5 * time.Second):
				c.Fatal("mempool scan did not finish")
			}
			added, err := client.temporalStorage.TrackMempoolTx(tx.Txid)
			c.Assert(err, IsNil)
			c.Assert(added, Equals, true) // the stale response must not leave a marker behind
		}()
	}
}

func (s *FiroSuite) TestFiroLaunchRequiresChainLockedCustomerDeposits(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	tx := f.instantDeposit(200, 12, 1)
	c.Assert(client.instantSend.enabled, Equals, false)
	for range 2 {
		batch, err := client.FetchMemPool(99)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0) // even a native InstantSend lock is insufficient
	}
	tx.Height, tx.BlockHash, tx.Confirmations = 100, firoHash(100), 1
	_, err := client.getBlockFIRO(100)
	c.Assert(err, NotNil) // mined without a ChainLock
	f.locked = 100
	tx.ChainLock = true
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	block, err := client.getBlockFIRO(100)
	c.Assert(err, IsNil)
	batch, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, tx.Txid)
}
