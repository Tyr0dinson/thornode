package mock

import (
	"context"
	"testing"

	tonaddress "github.com/xssnick/tonutils-go/address"
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

	block, err := client.LookupBlock(ctx, -1, shard0, 100000)
	c.Assert(err, IsNil)
	c.Assert(block, NotNil)
	c.Assert(block.SeqNo, Equals, uint32(100000))

	data, err := client.GetBlockData(ctx, block)
	c.Assert(err, IsNil)
	c.Assert(data, NotNil)

	infos, _, err := client.GetBlockTransactionsV2(ctx, block, 100)
	c.Assert(err, IsNil)
	c.Assert(infos, HasLen, 3)

	shards, err := client.GetBlockShardsInfo(ctx, block)
	c.Assert(err, IsNil)
	c.Assert(shards, HasLen, 2)

	config, err := client.GetBlockchainConfig(ctx, block)
	c.Assert(err, IsNil)
	c.Assert(config, IsNil)

	info, err := client.GetMasterchainInfo(ctx)
	c.Assert(err, IsNil)
	c.Assert(info, NotNil)
	c.Assert(info.SeqNo, Equals, uint32(256674))

	account, err := client.GetAccount(ctx, block, nil)
	c.Assert(err, IsNil)
	c.Assert(account, NotNil)
	c.Assert(account.IsActive, Equals, true)
	c.Assert(account.State.Balance.String(), Equals, "0.012319789")

	c.Assert(client.SendExternalMessage(ctx, nil), IsNil)

	_, _, _, err = client.SendExternalMessageWaitTransaction(ctx, nil)
	c.Assert(err, IsNil)

	resp, err := client.RunGetMethod(ctx, nil, nil, "foo")
	c.Assert(resp, IsNil)
	c.Assert(err, NotNil)

	txs, err := client.ListTransactions(ctx, nil, 1000, 1, nil)
	c.Assert(err, IsNil)
	c.Assert(txs, HasLen, 0)

	tx, err := client.GetTransaction(ctx, nil, &tonaddress.Address{}, 1)
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
