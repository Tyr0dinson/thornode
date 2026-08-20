package thorchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	storetypes "cosmossdk.io/store/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvm "github.com/CosmWasm/wasmvm/v2"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

const (
	code188Address       = "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx"
	code188Caller        = "thor1vk6trmz42cjrh4zcxczeaacnsv3snv4f22x8ccu203dqde7vtaxsyevlec"
	code188BoundaryNode  = "thor1gqtwzazgdncthm2cuu947d0mvk3w5fkahm40qp"
	code188FixtureHeight = int64(27489428)
)

type recordingWasmEngine struct {
	wasmtypes.WasmEngine
	lastExecuteGas uint64
}

func (r *recordingWasmEngine) Execute(
	code wasmvm.Checksum,
	env wasmvmtypes.Env,
	info wasmvmtypes.MessageInfo,
	executeMsg []byte,
	store wasmvm.KVStore,
	goapi wasmvm.GoAPI,
	querier wasmvm.Querier,
	gasMeter wasmvm.GasMeter,
	gasLimit uint64,
	deserCost wasmvmtypes.UFraction,
) (*wasmvmtypes.ContractResult, uint64, error) {
	result, gasUsed, err := r.WasmEngine.Execute(
		code,
		env,
		info,
		executeMsg,
		store,
		goapi,
		querier,
		gasMeter,
		gasLimit,
		deserCost,
	)
	r.lastExecuteGas = gasUsed
	return result, gasUsed, err
}

type capturedNodeResponse struct {
	Ordinal        int    `json:"ordinal"`
	Address        string `json:"address"`
	ResponseBase64 string `json:"response_base64"`
}

func (s WasmManagerSuite) TestBruneRefreshNodesObserveChainOrderChangesRawGas(c *C) {
	fixturePath := filepath.Join(
		"../../test/fixtures/wasm/code-188-gas-order",
		"fixtures.tar.gz",
	)
	fixture := loadTarGzipFixture(c, fixturePath)

	sortedResponses := loadSortedNodeResponses(
		c,
		fixture["rawgas-run1-code188-node-responses.jsonl"],
	)
	scrambledResponse := decodeCapturedNodeResponse(
		c,
		fixture["rawgas-run2-boundary-node-response.json"],
	)
	c.Assert(strings.ToLower(scrambledResponse.NodeAddress), Equals, code188BoundaryNode)

	sortedBoundaryResponse, ok := sortedResponses[code188BoundaryNode]
	c.Assert(ok, Equals, true)
	normalizedScrambled := proto.Clone(scrambledResponse).(*thortypes.QueryNodeResponse)
	sortObserveChains(normalizedScrambled.ObserveChains)
	sortedBytes, err := sortedBoundaryResponse.Marshal()
	c.Assert(err, IsNil)
	normalizedBytes, err := normalizedScrambled.Marshal()
	c.Assert(err, IsNil)
	c.Assert(string(normalizedBytes), Equals, string(sortedBytes))

	useScrambledResponse := false
	nodeQueryCount := 0
	grpcQuerier := func(_ sdk.Context, request *wasmvmtypes.GrpcQuery) (proto.Message, error) {
		switch request.Path {
		case "/types.Query/MimirWithKey":
			return &thortypes.QueryMimirWithKeyResponse{Value: 30_000_000_000_000}, nil
		case "/types.Query/Node":
			var nodeRequest thortypes.QueryNodeRequest
			if err := nodeRequest.Unmarshal(request.Data); err != nil {
				return nil, fmt.Errorf("decode node query: %w", err)
			}
			nodeQueryCount++
			address := strings.ToLower(nodeRequest.Address)
			if useScrambledResponse && address == code188BoundaryNode {
				return scrambledResponse, nil
			}
			response, ok := sortedResponses[address]
			if !ok {
				return nil, fmt.Errorf("no captured response for node %s", address)
			}
			return response, nil
		default:
			return nil, fmt.Errorf("unexpected Code 188 query path %s", request.Path)
		}
	}

	var engine *recordingWasmEngine
	ctx, mgr := setupManagerForTest(
		c,
		wasmkeeper.WithQueryPlugins(&wasmkeeper.QueryPlugins{Grpc: grpcQuerier}),
		wasmkeeper.WithWasmEngineDecorator(func(original wasmtypes.WasmEngine) wasmtypes.WasmEngine {
			engine = &recordingWasmEngine{WasmEngine: original}
			return engine
		}),
	)
	c.Assert(engine, NotNil)

	blockTime, err := time.Parse(time.RFC3339Nano, "2026-08-19T06:33:53.641770830Z")
	c.Assert(err, IsNil)
	ctx = ctx.WithBlockHeight(code188FixtureHeight).WithBlockTime(blockTime)

	var genesis wasmtypes.GenesisState
	c.Assert(mgr.cdc.UnmarshalJSON(
		fixture["code-188-wasm-genesis-27489427.json"],
		&genesis,
	), IsNil)
	_, err = wasmkeeper.InitGenesis(ctx, &mgr.wasmKeeper, genesis)
	c.Assert(err, IsNil)

	contractAddress, err := sdk.AccAddressFromBech32(code188Address)
	c.Assert(err, IsNil)
	caller, err := sdk.AccAddressFromBech32(code188Caller)
	c.Assert(err, IsNil)

	execute := func() uint64 {
		nodeQueryCount = 0
		engine.lastExecuteGas = 0
		executeCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		_, err := mgr.WasmManager().ExecuteContract(
			executeCtx,
			contractAddress,
			caller,
			[]byte(`{"refresh_nodes":{}}`),
			nil,
		)
		c.Assert(err, ErrorMatches, `(?s).*Value too big.*586991 bytes.*131072.*`)
		c.Assert(nodeQueryCount, Equals, 70)
		c.Assert(engine.lastExecuteGas > 0, Equals, true)
		return engine.lastExecuteGas
	}

	sortedGas := execute()
	useScrambledResponse = true
	scrambledGas := execute()

	c.Assert(sortedGas > scrambledGas, Equals, true)
	c.Assert(sortedGas-scrambledGas, Equals, uint64(6_900))
	c.Logf(
		"Code 188 ObserveChains order: sorted_raw_gas=%d scrambled_raw_gas=%d delta=%d",
		sortedGas,
		scrambledGas,
		sortedGas-scrambledGas,
	)
}

func loadSortedNodeResponses(c *C, data []byte) map[string]*thortypes.QueryNodeResponse {
	responses := make(map[string]*thortypes.QueryNodeResponse)
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var captured capturedNodeResponse
		err := decoder.Decode(&captured)
		if err == io.EOF {
			break
		}
		c.Assert(err, IsNil)
		response := decodeNodeResponse(c, captured.ResponseBase64)
		sortObserveChains(response.ObserveChains)
		responses[strings.ToLower(captured.Address)] = response
	}
	c.Assert(responses, HasLen, 70)
	return responses
}

func decodeCapturedNodeResponse(c *C, data []byte) *thortypes.QueryNodeResponse {
	var captured capturedNodeResponse
	c.Assert(json.Unmarshal(data, &captured), IsNil)
	c.Assert(captured.Ordinal, Equals, 32)
	c.Assert(strings.ToLower(captured.Address), Equals, code188BoundaryNode)
	return decodeNodeResponse(c, captured.ResponseBase64)
}

func decodeNodeResponse(c *C, encoded string) *thortypes.QueryNodeResponse {
	data, err := base64.StdEncoding.DecodeString(encoded)
	c.Assert(err, IsNil)
	var response thortypes.QueryNodeResponse
	c.Assert(response.Unmarshal(data), IsNil)
	return &response
}

func loadTarGzipFixture(c *C, fixturePath string) map[string][]byte {
	file, err := os.Open(fixturePath)
	c.Assert(err, IsNil)
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	c.Assert(err, IsNil)
	defer gzipReader.Close()

	fixture := make(map[string][]byte)
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		c.Assert(err, IsNil)
		data, err := io.ReadAll(tarReader)
		c.Assert(err, IsNil)
		fixture[header.Name] = data
	}
	c.Assert(fixture, HasLen, 3)
	return fixture
}
