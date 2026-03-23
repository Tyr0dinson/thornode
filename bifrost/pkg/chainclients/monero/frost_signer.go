package monero

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

var (
	// ErrFrostUnavailableBlock indicates the requested block height is above the canonical chain tip.
	ErrFrostUnavailableBlock = errors.New("frost chain block unavailable")
)

const maxFrostResponseBytes = 10 * 1024 * 1024

// FrostSigner is the HTTP client for the xmr-frost-signer sidecar.
type FrostSigner struct {
	endpoint string

	dkgRound1Path   string
	dkgRound2Path   string
	dkgRound3Path   string
	dkgFinalizePath string
	signPreparePath string
	signPartialPath string
	signCombinePath string
	broadcastPath   string
	balancePath     string
	transfersPath   string
	scannerSyncPath string
	addressesPath   string
	outputsPath     string
	feeRatePath     string
	chainHeightPath string
	chainBlockPath  string
	chainTxPath     string

	client   *http.Client
	username string
	password string
	bearer   string
}

type xmrTSSResponseEnvelope struct {
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

// FROST DKG request/response types
type xmrFrostDKGRound1Request struct {
	VaultPubKey      string `json:"vault_pub_key"`
	Threshold        uint32 `json:"threshold"`
	N                uint32 `json:"n"`
	ParticipantIndex uint32 `json:"participant_index"`
	Context          string `json:"context,omitempty"`
	SessionID        string `json:"session_id"`
}

type xmrFrostDKGRound1Response struct {
	Commitments string `json:"commitments"`
}

type xmrFrostDKGRound2Request struct {
	SessionID      string            `json:"session_id"`
	AllCommitments map[string]string `json:"all_commitments"`
}

type xmrFrostDKGRound2Response struct {
	EncryptedShares map[string]string `json:"encrypted_shares"`
}

type xmrFrostDKGRound3Request struct {
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type xmrFrostDKGRound3Response struct {
	Address  string `json:"address"`
	GroupKey string `json:"group_key"`
}

type xmrFrostDKGFinalizeRequest struct {
	SessionID string `json:"session_id"`
	GroupKey  string `json:"group_key"`
	Address   string `json:"address"`
}

type xmrFrostDKGFinalizeResponse struct {
	Success bool `json:"success"`
}

// FROST Signing request/response types
type xmrFrostSignPrepareRequest struct {
	MsgID        string `json:"msg_id"`
	VaultPubKey  string `json:"vault_pub_key"`
	ToAddress    string `json:"to_address"`
	AmountAtomic uint64 `json:"amount_atomic"`
	Memo         string `json:"memo,omitempty"`
	FeeRate      uint64 `json:"fee_rate,omitempty"`
	DecoyContext string `json:"decoy_context"`
}

type xmrFrostSignPrepareResponse struct {
	SessionID  string `json:"session_id"`
	Preprocess string `json:"preprocess"`
}

type xmrFrostSignPartialRequest struct {
	MsgID           string            `json:"msg_id"`
	SessionID       string            `json:"session_id"`
	AllPreprocesses map[string]string `json:"all_preprocesses"`
}

type xmrFrostSignPartialResponse struct {
	SignatureShare string `json:"signature_share"`
}

type xmrFrostSignCombineRequest struct {
	MsgID     string            `json:"msg_id"`
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type xmrFrostSignCombineResponse struct {
	TxHash    string `json:"tx_hash"`
	TxHex     string `json:"tx_hex"`
	FeeAtomic uint64 `json:"fee_atomic"`
}

type xmrFrostBroadcastRequest struct {
	TxHex       string `json:"tx_hex"`
	TxHash      string `json:"tx_hash,omitempty"`
	VaultPubKey string `json:"vault_pub_key"`
	MsgID       string `json:"msg_id"`
}

type xmrFrostBroadcastResponse struct {
	TxHash string `json:"tx_hash"`
}

// Scanner/balance response types from the sidecar
type xmrFrostBalanceResponse struct {
	Balance         uint64 `json:"balance"`
	UnlockedBalance uint64 `json:"unlocked_balance"`
}

type xmrFrostTransferEntry struct {
	TxID          string `json:"txid"`
	Address       string `json:"address"`
	FromAddress   string `json:"from_address,omitempty"`
	VaultPubKey   string `json:"vault_pub_key,omitempty"`
	Amount        uint64 `json:"amount"`
	Fee           uint64 `json:"fee"`
	Height        uint64 `json:"height"`
	Confirmations uint64 `json:"confirmations"`
	Note          string `json:"note"`
	Locked        bool   `json:"locked"`
}

type xmrFrostTransfersResponse struct {
	Incoming []xmrFrostTransferEntry `json:"incoming"`
	Outgoing []xmrFrostTransferEntry `json:"outgoing"`
}

type xmrFrostScannerSyncRequest struct {
	Height      int64  `json:"height"`
	TipHeight   int64  `json:"tip_height"`
	BlockHash   string `json:"block_hash"`
	ParentHash  string `json:"parent_hash"`
	ReorgWindow int64  `json:"reorg_window"`
}

type xmrFrostAddressesResponse struct {
	Addresses []string `json:"addresses"`
}

type xmrFrostOutputEntry struct {
	TxHash          string `json:"tx_hash"`
	OutputIndex     uint32 `json:"output_index"`
	Amount          uint64 `json:"amount"`
	Height          uint64 `json:"height"`
	KeyImage        string `json:"key_image,omitempty"`
	Spent           bool   `json:"spent"`
	State           string `json:"state,omitempty"`
	SubaddressMajor uint32 `json:"subaddress_major"`
	SubaddressMinor uint32 `json:"subaddress_minor"`
}

type xmrFrostOutputsResponse struct {
	Outputs []xmrFrostOutputEntry `json:"outputs"`
}

type xmrFrostFeeRateResponse struct {
	FeeRatePerWeight uint64 `json:"fee_rate_per_weight"`
}

type xmrFrostChainHeightResponse struct {
	Height int64 `json:"height"`
}

type xmrFrostChainBlockResponse struct {
	Height       int64    `json:"height"`
	Reward       uint64   `json:"reward"`
	Hash         string   `json:"hash"`
	PreviousHash string   `json:"previous_hash"`
	TxHashes     []string `json:"tx_hashes"`
}

type xmrFrostChainTxExistsResponse struct {
	Exists bool `json:"exists"`
}

type chainBlock struct {
	Height       int64
	Reward       uint64
	Hash         string
	PreviousHash string
	TxHashes     []string
}

type frostHTTPError struct {
	path   string
	status int
	body   string
}

func (e *frostHTTPError) Error() string {
	return fmt.Sprintf("frost endpoint %s returned status %d: %s", e.path, e.status, e.body)
}

func isFrostHTTPStatus(err error, status int) bool {
	var httpErr *frostHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.status == status
}

func readBoundedResponseBody(body io.Reader, maxBytes int) ([]byte, error) {
	respBody, err := io.ReadAll(io.LimitReader(body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(respBody) > maxBytes {
		return nil, fmt.Errorf("frost response exceeded %d byte limit", maxBytes)
	}
	return respBody, nil
}

func NewFrostSigner(cfg config.BifrostChainConfiguration) (*FrostSigner, error) {
	endpoint := strings.TrimSpace(cfg.SignerEndpoint)
	return NewFrostSignerWithEndpoint(cfg, endpoint)
}

func NewFrostSignerWithEndpoint(cfg config.BifrostChainConfiguration, endpoint string) (*FrostSigner, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("frost signer endpoint is empty")
	}

	parsedEndpoint, err := parseSignerEndpointURL(endpoint)
	if err != nil {
		return nil, err
	}
	if parsedEndpoint.Scheme == "http" &&
		!isLoopbackHost(parsedEndpoint.Hostname()) &&
		!allowInsecureFrostSignerHTTP(cfg) {
		return nil, fmt.Errorf("frost signer endpoint %q must use https when not loopback", endpoint)
	}

	username := strings.TrimSpace(cfg.UserName)
	password := strings.TrimSpace(cfg.Password)
	bearer := strings.TrimSpace(cfg.AuthorizationBearer)
	if (username == "") != (password == "") {
		return nil, fmt.Errorf("frost signer requires both username and password when basic auth is configured")
	}
	if !isLoopbackHost(parsedEndpoint.Hostname()) && bearer == "" && username == "" {
		return nil, fmt.Errorf("frost signer endpoint %q requires authorization_bearer or username/password", endpoint)
	}

	endpoint = strings.TrimSuffix(parsedEndpoint.String(), "/")

	timeout := cfg.BlockScanner.HTTPRequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &FrostSigner{
		endpoint: endpoint,

		dkgRound1Path:   "/v1/monero/dkg/round1",
		dkgRound2Path:   "/v1/monero/dkg/round2",
		dkgRound3Path:   "/v1/monero/dkg/round3",
		dkgFinalizePath: "/v1/monero/dkg/finalize",
		signPreparePath: "/v1/monero/sign/prepare",
		signPartialPath: "/v1/monero/sign/partial",
		signCombinePath: "/v1/monero/sign/combine",
		broadcastPath:   "/v1/monero/broadcast",
		balancePath:     "/v1/monero/balance",
		transfersPath:   "/v1/monero/transfers",
		scannerSyncPath: "/v1/monero/scanner/sync",
		addressesPath:   "/v1/monero/addresses",
		outputsPath:     "/v1/monero/outputs",
		feeRatePath:     "/v1/monero/fee-rate",
		chainHeightPath: "/v1/monero/chain/height",
		chainBlockPath:  "/v1/monero/chain/block",
		chainTxPath:     "/v1/monero/chain/tx",

		client:   &http.Client{Timeout: timeout},
		username: username,
		password: password,
		bearer:   bearer,
	}, nil
}

func parseSignerEndpointURL(endpoint string) (*url.URL, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("frost signer endpoint is empty")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid frost signer endpoint %q: %w", endpoint, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid frost signer endpoint scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, fmt.Errorf("invalid frost signer endpoint host")
	}
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func allowInsecureFrostSignerHTTP(cfg config.BifrostChainConfiguration) bool {
	chainNetwork := strings.TrimSpace(cfg.ChainNetwork)
	if strings.EqualFold(chainNetwork, "mocknet") {
		return true
	}
	return common.CurrentChainNetwork == common.MockNet
}

// --- FROST DKG methods ---

func (s *FrostSigner) FrostDKGRound1(req xmrFrostDKGRound1Request) (xmrFrostDKGRound1Response, error) {
	var out xmrFrostDKGRound1Response
	if err := s.call(http.MethodPost, s.dkgRound1Path, req, &out); err != nil {
		return xmrFrostDKGRound1Response{}, fmt.Errorf("fail to call frost dkg round1 endpoint: %w", err)
	}
	if strings.TrimSpace(out.Commitments) == "" {
		return xmrFrostDKGRound1Response{}, fmt.Errorf("frost dkg round1 endpoint returned empty commitments")
	}
	return out, nil
}

func (s *FrostSigner) FrostDKGRound2(req xmrFrostDKGRound2Request) (xmrFrostDKGRound2Response, error) {
	var out xmrFrostDKGRound2Response
	if err := s.call(http.MethodPost, s.dkgRound2Path, req, &out); err != nil {
		return xmrFrostDKGRound2Response{}, fmt.Errorf("fail to call frost dkg round2 endpoint: %w", err)
	}
	if len(out.EncryptedShares) == 0 {
		return xmrFrostDKGRound2Response{}, fmt.Errorf("frost dkg round2 endpoint returned empty encrypted_shares")
	}
	return out, nil
}

func (s *FrostSigner) FrostDKGRound3(req xmrFrostDKGRound3Request) (xmrFrostDKGRound3Response, error) {
	var out xmrFrostDKGRound3Response
	if err := s.call(http.MethodPost, s.dkgRound3Path, req, &out); err != nil {
		return xmrFrostDKGRound3Response{}, fmt.Errorf("fail to call frost dkg round3 endpoint: %w", err)
	}
	if strings.TrimSpace(out.Address) == "" {
		return xmrFrostDKGRound3Response{}, fmt.Errorf("frost dkg round3 endpoint returned empty address")
	}
	return out, nil
}

func (s *FrostSigner) FrostDKGFinalize(req xmrFrostDKGFinalizeRequest) (xmrFrostDKGFinalizeResponse, error) {
	var out xmrFrostDKGFinalizeResponse
	if err := s.call(http.MethodPost, s.dkgFinalizePath, req, &out); err != nil {
		return xmrFrostDKGFinalizeResponse{}, fmt.Errorf("fail to call frost dkg finalize endpoint: %w", err)
	}
	if !out.Success {
		return xmrFrostDKGFinalizeResponse{}, fmt.Errorf("frost dkg finalize endpoint returned unsuccessful response")
	}
	return out, nil
}

// --- FROST Signing methods ---

func (s *FrostSigner) FrostSignPrepare(req xmrFrostSignPrepareRequest) (xmrFrostSignPrepareResponse, error) {
	var out xmrFrostSignPrepareResponse
	if err := s.call(http.MethodPost, s.signPreparePath, req, &out); err != nil {
		return xmrFrostSignPrepareResponse{}, fmt.Errorf("fail to call frost sign prepare endpoint: %w", err)
	}
	if strings.TrimSpace(out.SessionID) == "" {
		return xmrFrostSignPrepareResponse{}, fmt.Errorf("frost sign prepare endpoint returned empty session_id")
	}
	if strings.TrimSpace(out.Preprocess) == "" {
		return xmrFrostSignPrepareResponse{}, fmt.Errorf("frost sign prepare endpoint returned empty preprocess")
	}
	return out, nil
}

func (s *FrostSigner) FrostSignPartial(req xmrFrostSignPartialRequest) (xmrFrostSignPartialResponse, error) {
	var out xmrFrostSignPartialResponse
	if err := s.call(http.MethodPost, s.signPartialPath, req, &out); err != nil {
		return xmrFrostSignPartialResponse{}, fmt.Errorf("fail to call frost sign partial endpoint: %w", err)
	}
	if strings.TrimSpace(out.SignatureShare) == "" {
		return xmrFrostSignPartialResponse{}, fmt.Errorf("frost sign partial endpoint returned empty signature_share")
	}
	return out, nil
}

func (s *FrostSigner) FrostSignCombine(req xmrFrostSignCombineRequest) (xmrFrostSignCombineResponse, error) {
	var out xmrFrostSignCombineResponse
	if err := s.call(http.MethodPost, s.signCombinePath, req, &out); err != nil {
		return xmrFrostSignCombineResponse{}, fmt.Errorf("fail to call frost sign combine endpoint: %w", err)
	}
	if strings.TrimSpace(out.TxHash) == "" {
		return xmrFrostSignCombineResponse{}, fmt.Errorf("frost sign combine endpoint returned empty tx_hash")
	}
	return out, nil
}

func (s *FrostSigner) FrostBroadcast(req xmrFrostBroadcastRequest) (xmrFrostBroadcastResponse, error) {
	var out xmrFrostBroadcastResponse
	if err := s.call(http.MethodPost, s.broadcastPath, req, &out); err != nil {
		return xmrFrostBroadcastResponse{}, fmt.Errorf("fail to call frost broadcast endpoint: %w", err)
	}
	return out, nil
}

// --- Scanner/Balance methods ---

func (s *FrostSigner) GetBalance(vaultPubKey string) (xmrFrostBalanceResponse, error) {
	var out xmrFrostBalanceResponse
	path := s.balancePath + "/" + url.PathEscape(vaultPubKey)
	if err := s.callGet(path, &out); err != nil {
		return xmrFrostBalanceResponse{}, fmt.Errorf("fail to call frost balance endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) GetTransfersByHeight(height int64) (xmrFrostTransfersResponse, error) {
	var out xmrFrostTransfersResponse
	path := fmt.Sprintf("%s?height=%d", s.transfersPath, height)
	if err := s.callGet(path, &out); err != nil {
		return xmrFrostTransfersResponse{}, fmt.Errorf("fail to call frost transfers endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) SyncTransfersByHeight(height, tipHeight int64, blockHash, parentHash string, reorgWindow int64) (xmrFrostTransfersResponse, error) {
	if height < 0 {
		return xmrFrostTransfersResponse{}, fmt.Errorf("height must be >= 0")
	}
	if tipHeight < 0 {
		tipHeight = 0
	}
	if reorgWindow < 0 {
		reorgWindow = 0
	}

	var out xmrFrostTransfersResponse
	req := xmrFrostScannerSyncRequest{
		Height:      height,
		TipHeight:   tipHeight,
		BlockHash:   strings.TrimSpace(blockHash),
		ParentHash:  strings.TrimSpace(parentHash),
		ReorgWindow: reorgWindow,
	}
	if err := s.call(http.MethodPost, s.scannerSyncPath, req, &out); err != nil {
		return xmrFrostTransfersResponse{}, fmt.Errorf("fail to call frost scanner sync endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) GetMemPoolTransfers() (xmrFrostTransfersResponse, error) {
	var out xmrFrostTransfersResponse
	path := fmt.Sprintf("%s?mempool=true", s.transfersPath)
	if err := s.callGet(path, &out); err != nil {
		return xmrFrostTransfersResponse{}, fmt.Errorf("fail to call frost mempool transfers endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) GetAddresses(vaultPubKey string) ([]string, error) {
	var out xmrFrostAddressesResponse
	path := s.addressesPath + "/" + url.PathEscape(vaultPubKey)
	if err := s.callGet(path, &out); err != nil {
		return nil, fmt.Errorf("fail to call frost addresses endpoint: %w", err)
	}
	return out.Addresses, nil
}

func (s *FrostSigner) GetOutputs(vaultPubKey string) ([]xmrFrostOutputEntry, error) {
	var out xmrFrostOutputsResponse
	path := s.outputsPath + "/" + url.PathEscape(vaultPubKey)
	if err := s.callGet(path, &out); err != nil {
		return nil, fmt.Errorf("fail to call frost outputs endpoint: %w", err)
	}
	return out.Outputs, nil
}

func (s *FrostSigner) GetFeeRate() (uint64, error) {
	var out xmrFrostFeeRateResponse
	if err := s.callGet(s.feeRatePath, &out); err != nil {
		return 0, fmt.Errorf("fail to call frost fee-rate endpoint: %w", err)
	}
	return out.FeeRatePerWeight, nil
}

func (s *FrostSigner) GetChainHeight() (int64, error) {
	var out xmrFrostChainHeightResponse
	if err := s.callGet(s.chainHeightPath, &out); err != nil {
		return 0, fmt.Errorf("fail to call frost chain-height endpoint: %w", err)
	}
	return out.Height, nil
}

func (s *FrostSigner) GetChainBlock(height int64) (chainBlock, error) {
	if height < 0 {
		return chainBlock{}, fmt.Errorf("height must be >= 0")
	}

	var out xmrFrostChainBlockResponse
	path := fmt.Sprintf("%s/%d", s.chainBlockPath, height)
	if err := s.callGet(path, &out); err != nil {
		if isFrostHTTPStatus(err, http.StatusConflict) {
			return chainBlock{}, fmt.Errorf("%w: %d", ErrFrostUnavailableBlock, height)
		}
		return chainBlock{}, fmt.Errorf("fail to call frost chain-block endpoint: %w", err)
	}
	return chainBlock{
		Height:       out.Height,
		Reward:       out.Reward,
		Hash:         out.Hash,
		PreviousHash: out.PreviousHash,
		TxHashes:     out.TxHashes,
	}, nil
}

func (s *FrostSigner) TxExists(txHash string) (bool, error) {
	normalized := strings.TrimSpace(txHash)
	if normalized == "" {
		return false, fmt.Errorf("tx hash is empty")
	}

	var out xmrFrostChainTxExistsResponse
	path := fmt.Sprintf("%s/%s/exists", s.chainTxPath, url.PathEscape(normalized))
	if err := s.callGet(path, &out); err != nil {
		return false, fmt.Errorf("fail to call frost chain tx-exists endpoint: %w", err)
	}
	return out.Exists, nil
}

// --- HTTP helpers ---

func (s *FrostSigner) callGet(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, s.endpoint+path, nil)
	if err != nil {
		return fmt.Errorf("fail to create frost request: %w", err)
	}
	if s.bearer != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.bearer))
	}
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("fail to call frost endpoint %s: %w", path, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	respBody, err := readBoundedResponseBody(resp.Body, maxFrostResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read frost response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return &frostHTTPError{
			path:   path,
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(respBody)),
		}
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("frost endpoint %s returned empty response body", path)
	}

	if err = json.Unmarshal(respBody, out); err == nil {
		return nil
	}

	var envelope xmrTSSResponseEnvelope
	if envErr := json.Unmarshal(respBody, &envelope); envErr != nil {
		return fmt.Errorf("fail to unmarshal frost response: %w", err)
	}
	if envelope.Error != "" || envelope.Message != "" {
		msg := strings.TrimSpace(envelope.Error)
		if msg == "" {
			msg = strings.TrimSpace(envelope.Message)
		}
		return fmt.Errorf("frost endpoint %s error: %s", path, msg)
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("frost endpoint %s returned empty result", path)
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("fail to unmarshal frost result: %w", err)
	}
	return nil
}

func (s *FrostSigner) callWithTimeout(method, path string, payload any, out any, timeout time.Duration) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fail to marshal frost request payload: %w", err)
	}

	req, err := http.NewRequest(method, s.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fail to create frost request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.bearer != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.bearer))
	}
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fail to call frost endpoint %s: %w", path, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	respBody, err := readBoundedResponseBody(resp.Body, maxFrostResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read frost response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return &frostHTTPError{
			path:   path,
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(respBody)),
		}
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("frost endpoint %s returned empty response body", path)
	}

	if err = json.Unmarshal(respBody, out); err == nil {
		return nil
	}

	var envelope xmrTSSResponseEnvelope
	if envErr := json.Unmarshal(respBody, &envelope); envErr != nil {
		return fmt.Errorf("fail to unmarshal frost response: %w", err)
	}
	if envelope.Error != "" || envelope.Message != "" {
		msg := strings.TrimSpace(envelope.Error)
		if msg == "" {
			msg = strings.TrimSpace(envelope.Message)
		}
		return fmt.Errorf("frost endpoint %s error: %s", path, msg)
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("frost endpoint %s returned empty result", path)
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("fail to unmarshal frost result: %w", err)
	}
	return nil
}

func (s *FrostSigner) call(method, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fail to marshal frost request payload: %w", err)
	}

	req, err := http.NewRequest(method, s.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fail to create frost request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.bearer != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.bearer))
	}
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("fail to call frost endpoint %s: %w", path, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	respBody, err := readBoundedResponseBody(resp.Body, maxFrostResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read frost response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return &frostHTTPError{
			path:   path,
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(respBody)),
		}
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("frost endpoint %s returned empty response body", path)
	}

	// Accept both direct payload responses and envelope-style responses.
	if err = json.Unmarshal(respBody, out); err == nil {
		return nil
	}

	var envelope xmrTSSResponseEnvelope
	if envErr := json.Unmarshal(respBody, &envelope); envErr != nil {
		return fmt.Errorf("fail to unmarshal frost response: %w", err)
	}
	if envelope.Error != "" || envelope.Message != "" {
		msg := strings.TrimSpace(envelope.Error)
		if msg == "" {
			msg = strings.TrimSpace(envelope.Message)
		}
		return fmt.Errorf("frost endpoint %s error: %s", path, msg)
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("frost endpoint %s returned empty result", path)
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("fail to unmarshal frost result: %w", err)
	}
	return nil
}
