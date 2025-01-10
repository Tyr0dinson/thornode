package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/rpca/types"
)

type XrpClient struct {
	url string
	log *zerolog.Logger
}

func NewXrpClient(url string, log *zerolog.Logger) *XrpClient {
	return &XrpClient{
		url: url,
		log: log,
	}
}

func makeRPCCall(url string, method string, params []any) (*types.RPCResponse, error) {
	request := types.RPCRequest{
		Method: method,
		Params: params,
		ID:     1,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(requestBody)) //nolint:gosec,noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response types.RPCResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	if response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s (code: %d)", response.Error.Message, response.Error.Code)
	}

	return &response, nil
}
