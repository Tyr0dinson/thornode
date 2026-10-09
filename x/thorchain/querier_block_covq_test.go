package thorchain

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	tmhttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	tmtypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covQRPCServer is a minimal CometBFT JSON-RPC server serving "block" and
// "block_results" from canned results.
type covQRPCServer struct {
	block        *ctypes.ResultBlock
	results      *ctypes.ResultBlockResults
	failResults  bool
	blockQueries atomic.Int64
}

func (r *covQRPCServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rpcReq rpctypes.RPCRequest
	if err = json.Unmarshal(body, &rpcReq); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var resp rpctypes.RPCResponse
	switch rpcReq.Method {
	case "block":
		r.blockQueries.Add(1)
		resp = rpctypes.NewRPCSuccessResponse(rpcReq.ID, r.block)
	case "block_results":
		if r.failResults {
			resp = rpctypes.RPCInternalError(rpcReq.ID, errors.New("covq results unavailable"))
		} else {
			resp = rpctypes.NewRPCSuccessResponse(rpcReq.ID, r.results)
		}
	default:
		resp = rpctypes.RPCMethodNotFoundError(rpcReq.ID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// covQWithTendermintClient points the package-level tendermint client at the
// given server for the duration of fn, restoring the previous globals after.
func covQWithTendermintClient(c *C, url string, fn func()) {
	// make sure the lazy init has already happened so it can't clobber the
	// client installed below
	initTendermintOnce.Do(initTendermint)
	oldClient, oldErr := tendermintClient, tendermintClientErr
	defer func() {
		tendermintClient, tendermintClientErr = oldClient, oldErr
	}()
	client, err := tmhttp.New(url, "/websocket")
	c.Assert(err, IsNil)
	tendermintClient, tendermintClientErr = client, nil
	fn()
}

func (s *QuerierCovQSuite) TestInitTendermintCovQ(c *C) {
	oldClient, oldErr := tendermintClient, tendermintClientErr
	defer func() {
		tendermintClient, tendermintClientErr = oldClient, oldErr
	}()
	tendermintClient, tendermintClientErr = nil, nil
	initTendermint()
	// creating the client does not dial, so it always succeeds for the
	// configured local port
	c.Assert(tendermintClientErr, IsNil)
	c.Assert(tendermintClient, NotNil)
}

func (s *QuerierCovQSuite) TestQueryBlockClientErrorCovQ(c *C) {
	initTendermintOnce.Do(initTendermint)
	oldClient, oldErr := tendermintClient, tendermintClientErr
	defer func() {
		tendermintClient, tendermintClientErr = oldClient, oldErr
	}()
	tendermintClientErr = errors.New("covq no client")
	_, err := s.qs.Block(s.ctx, &types.QueryBlockRequest{})
	c.Assert(err, ErrorMatches, "covq no client")
}

func (s *QuerierCovQSuite) TestQueryBlockCovQ(c *C) {
	// a tx config able to encode/decode THORChain messages
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	types.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	txConfig, err := params.TxConfig(cdc, nil)
	c.Assert(err, IsNil)
	s.qs.txConfig = txConfig

	signer := GetRandomBech32Addr()
	builder := txConfig.NewTxBuilder()
	c.Assert(builder.SetMsgs(types.NewMsgMimir("COVQKEY", 3, signer)), IsNil)
	builder.SetMemo("covq memo")
	txBytes, err := txConfig.TxEncoder()(builder.GetTx())
	c.Assert(err, IsNil)

	blockTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	block := tmtypes.MakeBlock(42, []tmtypes.Tx{txBytes}, nil, nil)
	block.ChainID = "thorchain"
	block.Time = blockTime
	blockID := tmtypes.BlockID{Hash: block.Hash()}

	srv := &covQRPCServer{
		block: &ctypes.ResultBlock{BlockID: blockID, Block: block},
		results: &ctypes.ResultBlockResults{
			Height: 42,
			TxsResults: []*abci.ExecTxResult{{
				Code:      0,
				Data:      []byte("covq-data"),
				Log:       "covq-log",
				GasWanted: 100,
				GasUsed:   60,
				Events: []abci.Event{{
					Type:       "message",
					Attributes: []abci.EventAttribute{{Key: "action", Value: "mimir"}},
				}},
			}},
			FinalizeBlockEvents: []abci.Event{
				{Type: "begin_evt", Attributes: []abci.EventAttribute{{Key: "mode", Value: "BeginBlock"}}},
				{Type: "end_evt", Attributes: []abci.EventAttribute{{Key: "mode", Value: "EndBlock"}, {Key: "k", Value: "v"}}},
				{Type: "finalize_evt", Attributes: []abci.EventAttribute{{Key: "x", Value: "y"}}},
			},
		},
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	covQWithTendermintClient(c, ts.URL, func() {
		resp, err := s.qs.Block(s.ctx, &types.QueryBlockRequest{Height: "42"})
		c.Assert(err, IsNil)
		c.Assert(resp.Id.Hash, Equals, blockID.Hash.String())
		c.Assert(resp.Header.ChainId, Equals, "thorchain")
		c.Assert(resp.Header.Height, Equals, int64(42))
		c.Assert(resp.Header.Time, Equals, blockTime.Format(time.RFC3339Nano))

		c.Assert(resp.BeginBlockEvents, HasLen, 1)
		c.Assert(resp.BeginBlockEvents[0].EventKvPair[0].Value, Equals, "begin_evt")
		c.Assert(resp.EndBlockEvents, HasLen, 1)
		c.Assert(resp.EndBlockEvents[0].EventKvPair, HasLen, 3)
		c.Assert(resp.FinalizeBlockEvents, HasLen, 1)
		c.Assert(resp.FinalizeBlockEvents[0].EventKvPair[1].Key, Equals, "x")

		c.Assert(resp.Txs, HasLen, 1)
		tx := resp.Txs[0]
		c.Assert(tx.Hash, Equals, strings.ToUpper(hex.EncodeToString(tmtypes.Tx(txBytes).Hash())))
		c.Assert(string(tx.Tx), Matches, `(?s).*covq memo.*`)
		c.Assert(tx.Result.Log, Equals, "covq-log")
		c.Assert(tx.Result.GasWanted, Equals, "100")
		c.Assert(tx.Result.GasUsed, Equals, "60")
		c.Assert(tx.Result.Data, Equals, "covq-data")
		c.Assert(tx.Result.Events, HasLen, 1)
		c.Assert(tx.Result.Events[0].EventKvPair[0].Value, Equals, "message")
		c.Assert(tx.Result.Events[0].EventKvPair[1].Key, Equals, "action")

		// empty height defaults to the context height
		_, err = s.qs.Block(s.ctx, &types.QueryBlockRequest{})
		c.Assert(err, IsNil)
		c.Assert(srv.blockQueries.Load(), Equals, int64(2))

		// an undecodable tx is reported
		srv.block.Block.Data.Txs = []tmtypes.Tx{[]byte("garbage")}
		_, err = s.qs.Block(s.ctx, &types.QueryBlockRequest{Height: "42"})
		c.Assert(err, ErrorMatches, "(?s)fail to decode tx.*")

		// block results failure
		srv.failResults = true
		_, err = s.qs.Block(s.ctx, &types.QueryBlockRequest{Height: "42"})
		c.Assert(err, ErrorMatches, "(?s)fail to get block results from tendermint rpc.*")
	})

	// unreachable endpoint
	ts.Close()
	covQWithTendermintClient(c, ts.URL, func() {
		_, err := s.qs.Block(s.ctx, &types.QueryBlockRequest{Height: "1"})
		c.Assert(err, ErrorMatches, "(?s)fail to get block from tendermint rpc.*")
	})
}

func (s *QuerierCovQSuite) TestBlockEventCovQ(c *C) {
	ev := sdk.NewEvent("covq", sdk.NewAttribute("a", "1"), sdk.NewAttribute("b", "2"))
	be := blockEvent(ev)
	c.Assert(be.EventKvPair, HasLen, 3)
	c.Assert(be.EventKvPair[0].Key, Equals, "type")
	c.Assert(be.EventKvPair[0].Value, Equals, "covq")
	c.Assert(be.EventKvPair[2].Key, Equals, "b")
	c.Assert(be.EventKvPair[2].Value, Equals, "2")
	m := eventMap(ev)
	c.Assert(m, DeepEquals, map[string]string{"type": "covq", "a": "1", "b": "2"})
}
