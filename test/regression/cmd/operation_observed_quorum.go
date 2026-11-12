package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// OpTxObservedInQuorum delivers signed observations through proposal injection.
// Nodes are regression account names; omitting them selects the default dog node.
type OpTxObservedInQuorum struct {
	OpBase      `yaml:",inline"`
	Observation common.ObservedTx `json:"observation"`
	Nodes       []string          `json:"nodes"`
}

func (op *OpTxObservedInQuorum) quorum() (*common.QuorumTx, error) {
	nodes := op.Nodes
	if len(nodes) == 0 {
		nodes = []string{"dog"}
	}
	q := &common.QuorumTx{ObsTx: op.Observation, Inbound: true}
	payload, err := q.ObsTx.GetSignablePayloadWithInbound(true)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		key, keyErr := nodeSecpPrivKey(node)
		if keyErr != nil {
			return nil, keyErr
		}
		signature, signErr := key.Sign(payload)
		if signErr != nil {
			return nil, signErr
		}
		q.Attestations = append(q.Attestations, &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: signature})
	}
	return q, nil
}

func (op *OpTxObservedInQuorum) Execute(_ io.Writer, _ string, _, routine int, _ *os.Process, _ chan string) error {
	q, err := op.quorum()
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", regtestEbifrostPort(routine))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = ebifrost.NewLocalhostBifrostClient(conn).SendQuorumTx(ctx, q)
	return err
}
