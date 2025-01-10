package xrp

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func ctx() context.Context {
	return context.Background()
}

////////////////////////////////////////////////////////////////////////////////////////
// Cosmos
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain    common.Chain
	grpc     *grpc.ClientConn

	keys    *thorclient.Keys
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	panic("XRP NewClient unimplemented")
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	panic("XRP GetAccount unimplemented")
}

func (c *Client) SignContractTx(SimContractTx) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	panic("XRP SignTx unimplemented")
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	panic("XRP BroadcastTx unimplemented")
}
