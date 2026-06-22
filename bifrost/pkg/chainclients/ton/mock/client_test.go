package mock

import (
	"context"
	"testing"

	tonaddress "github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	. "gopkg.in/check.v1"
)

type MockTestSuite struct{}

var _ = Suite(&MockTestSuite{})

func Test(t *testing.T) {
	TestingT(t)
}

func (s *MockTestSuite) SetUpSuite(c *C) {}

func (s *MockTestSuite) TestClient(c *C) {
	ctx := context.TODO()

	client := NewMockApiClient()
	c.Assert(client, NotNil)

	c.Assert(client.Client(), IsNil)

	time, err := client.GetTime(ctx)
	c.Assert(err, IsNil)
	c.Assert(time, Equals, uint32(0))

	cells, err := client.GetLibraries(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(cells), Equals, 0)

	// master block lookup (120 is a known master)
	master, err := client.LookupBlock(ctx, -1, shard0, 120)
	c.Assert(err, IsNil)
	c.Assert(master, NotNil)
	c.Assert(master.SeqNo, Equals, uint32(120))

	infos, _, err := client.GetBlockTransactionsV2(ctx, master, 100)
	c.Assert(err, IsNil)
	c.Assert(infos, HasLen, 0)

	// master 120 resolves to its single shard block (seqno 121)
	shards, err := client.GetBlockShardsInfo(ctx, master)
	c.Assert(err, IsNil)
	c.Assert(shards, HasLen, 1)
	c.Assert(shards[0].SeqNo, Equals, uint32(121))

	// block data is keyed by the shard seqno; the master seqno has no block
	data, err := client.GetBlockData(ctx, shards[0])
	c.Assert(err, IsNil)
	c.Assert(data, NotNil)

	_, err = client.GetBlockData(ctx, master)
	c.Assert(err, NotNil)

	// an unknown master has no shards
	master.SeqNo = 999
	_, err = client.GetBlockShardsInfo(ctx, master)
	c.Assert(err, NotNil)

	config, err := client.GetBlockchainConfig(ctx, master)
	c.Assert(err, IsNil)
	c.Assert(config, IsNil)

	info, err := client.GetMasterchainInfo(ctx)
	c.Assert(err, IsNil)
	c.Assert(info, NotNil)
	c.Assert(info.SeqNo, Equals, uint32(256674))

	account, err := client.GetAccount(ctx, master, nil)
	c.Assert(err, IsNil)
	c.Assert(account, NotNil)
	c.Assert(account.IsActive, Equals, true)
	c.Assert(account.State.Balance.String(), Equals, "0.012319789")

	c.Assert(client.SendExternalMessage(ctx, nil), IsNil)

	var tx *tlb.Transaction
	tx, _, _, err = client.SendExternalMessageWaitTransaction(ctx, nil)
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)
	c.Assert(tx.Hash, NotNil)

	resp, err := client.RunGetMethod(ctx, nil, nil, "foo")
	c.Assert(resp, IsNil)
	c.Assert(err, NotNil)

	sender, err := tonaddress.ParseAddr("0QAph9pN33D_Ym7WwsdrQQH8J-Ab4DkACxbQwaE_0VWfRqaX")
	c.Assert(err, IsNil)
	txs, err := client.ListTransactions(ctx, sender, 1000, 1, nil)
	c.Assert(err, IsNil)
	c.Assert(len(txs) > 0, Equals, true)

	tx, err = client.GetTransaction(ctx, nil, &tonaddress.Address{}, 1)
	c.Assert(err, IsNil)
	c.Assert(tx, IsNil)

	proof, err := client.GetBlockProof(ctx, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(proof, IsNil)

	info, err = client.CurrentMasterchainInfo(ctx)
	c.Assert(err, IsNil)
	c.Assert(info, NotNil)
	c.Assert(info.SeqNo, Equals, uint32(256674))

	c.Assert(client.VerifyProofChain(ctx, nil, nil), IsNil)

	client = client.WaitForBlock(123)
	c.Assert(client, NotNil)

	client = client.WithRetry(123)
	c.Assert(client, NotNil)

	tx, err = client.FindLastTransactionByInMsgHash(ctx, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(tx, IsNil)

	tx, err = client.FindLastTransactionByOutMsgHash(ctx, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(tx, IsNil)
}
