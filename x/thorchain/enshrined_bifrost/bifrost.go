package enshrined_bifrost

import (
	context "context"
	fmt "fmt"
	"net"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	types "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/grpc"
)

type EnshrinedBifrost struct {
	cdc codec.Codec
	s   *grpc.Server
}

type enshrinedBifrostServer struct {
}

func (s *enshrinedBifrostServer) TxInQuorum(ctx context.Context, req *types.MsgObservedTxInQuorum) (*TxInQuorumResult, error) {
	return nil, nil
}

func NewBifrost(cdc codec.Codec) *EnshrinedBifrost {
	s := grpc.NewServer()
	srv := enshrinedBifrostServer{}

	RegisterEnshrinedBifrostServer(s, &srv)
	return &EnshrinedBifrost{
		cdc: cdc,
		s:   s,
	}
}

func (b *EnshrinedBifrost) Start(port int) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	go func() {
		if err := b.s.Serve(lis); err != nil {
			// TODO: how to handle errors here?
			panic(err)
		}
	}()

	return nil
}

func (b *EnshrinedBifrost) Stop() {
	b.s.Stop()
}

// ProposalInjectTxs is intended to be called by the current proposing validator during PrepareProposal
// and will return a list of in-quorum transactions to be included in the next block.
func (b *EnshrinedBifrost) ProposalInjectTxs(ctx sdk.Context) [][]byte {
	var injectTxs [][]byte

	return injectTxs
}
