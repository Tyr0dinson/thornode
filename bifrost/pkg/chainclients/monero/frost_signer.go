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
	"os"
	"strconv"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ErrFrostUnavailableBlock indicates the requested block height is above the canonical chain tip.
var ErrFrostUnavailableBlock = errors.New("frost chain block unavailable")

const maxFrostResponseBytes = 10 * 1024 * 1024

// FrostSigner is the HTTP client for the xmr-frost-signer sidecar.
type FrostSigner struct {
	endpoint string

	daemonEndpoint string

	dkgRound1Path         string
	dkgRound2Path         string
	dkgRound3Path         string
	dkgFinalizePath       string
	signPreparePath       string
	signPartialPath       string
	signCombinePath       string
	keyImagePreparePath   string
	keyImagePartialPath   string
	keyImageCombinePath   string
	keyImageValidatePath  string
	validatePath          string
	outgoingRehydratePath string
	vaultStatePath        string
	outgoingStatePath     string
	outgoingPrunePath     string
	clearSessionsPath     string
	broadcastPath         string
	balancePath           string
	transfersPath         string
	scannerSyncPath       string
	addressesPath         string
	outputsPath           string
	registerPath          string
	feeRatePath           string
	chainHeightPath       string
	chainBlockPath        string
	chainTxPath           string

	client       *http.Client
	daemonClient *http.Client
	signClient   *http.Client // longer timeout for sign/prepare; decoy retries can take a while
	username     string
	password     string
	bearer       string
}

type xmrTSSResponseEnvelope struct {
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

type moneroDaemonJSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type moneroDaemonJSONRPCResponse struct {
	Result json.RawMessage       `json:"result"`
	Error  *moneroDaemonRPCError `json:"error"`
}

type moneroDaemonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type moneroDaemonGetTransactionsRequest struct {
	TxHashes     []string `json:"txs_hashes"`
	DecodeAsJSON bool     `json:"decode_as_json"`
}

type moneroDaemonGetTransactionsResponse struct {
	Txs      []moneroDaemonTxEntry `json:"txs"`
	MissedTx []string              `json:"missed_tx"`
	Status   string                `json:"status"`
}

type moneroDaemonTxEntry struct {
	TxHash      string  `json:"tx_hash"`
	AsHex       string  `json:"as_hex"`
	AsJSON      string  `json:"as_json"`
	BlockHeight *uint64 `json:"block_height"`
	InPool      *bool   `json:"in_pool"`
}

type moneroDaemonParsedTx struct {
	RctSignatures struct {
		TxnFee json.RawMessage `json:"txnFee"`
	} `json:"rct_signatures"`
}

func (e *moneroDaemonRPCError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("daemon rpc error %d", e.Code)
	}
	return fmt.Sprintf("daemon rpc error %d: %s", e.Code, strings.TrimSpace(e.Message))
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
	Success     bool   `json:"success"`
	KeyBytesHex string `json:"key_bytes_hex"`
}

// FROST Signing request/response types
type xmrFrostSignPrepareRequest struct {
	MsgID        string `json:"msg_id"`
	VaultPubKey  string `json:"vault_pub_key"`
	ToAddress    string `json:"to_address"`
	AmountAtomic uint64 `json:"amount_atomic"`
	MaxGasAtomic uint64 `json:"max_gas_atomic,omitempty"`
	SweepIfFinal bool   `json:"sweep_if_final,omitempty"`
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
	TxHash       string   `json:"tx_hash"`
	TxHex        string   `json:"tx_hex"`
	AmountAtomic uint64   `json:"amount_atomic,omitempty"`
	FeeAtomic    uint64   `json:"fee_atomic"`
	TxKeys       []string `json:"tx_keys,omitempty"`
}

type xmrFrostKeyImagePrepareRequest struct {
	MsgID       string `json:"msg_id"`
	VaultPubKey string `json:"vault_pub_key"`
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
}

type xmrFrostKeyImagePrepareResponse struct {
	SessionID  string `json:"session_id"`
	Preprocess string `json:"preprocess"`
}

type xmrFrostKeyImagePartialRequest struct {
	MsgID           string            `json:"msg_id"`
	SessionID       string            `json:"session_id"`
	AllPreprocesses map[string]string `json:"all_preprocesses"`
}

type xmrFrostKeyImagePartialResponse struct {
	SignatureShare string `json:"signature_share"`
}

type xmrFrostKeyImageCombineRequest struct {
	MsgID     string            `json:"msg_id"`
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type xmrFrostKeyImageCombineResponse struct {
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
	KeyImage    string `json:"key_image"`
	ProofC      string `json:"proof_c"`
	ProofS      string `json:"proof_s"`
}

type xmrFrostKeyImageValidateRequest struct {
	VaultPubKey string `json:"vault_pub_key"`
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
	KeyImage    string `json:"key_image"`
	ProofC      string `json:"proof_c"`
	ProofS      string `json:"proof_s"`
}

type xmrFrostKeyImageValidateResponse struct {
	Valid bool `json:"valid"`
}

type xmrFrostOutputRef struct {
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
}

type xmrFrostApplyKeyImagesRequest struct {
	VaultPubKey string                     `json:"vault_pub_key"`
	KeyImages   []xmrFrostApplyKeyImageRef `json:"key_images"`
	Reconcile   bool                       `json:"reconcile,omitempty"`
	Spend       bool                       `json:"spend,omitempty"`
	SpentHeight uint64                     `json:"spent_height,omitempty"`
}

type xmrFrostApplyKeyImageRef struct {
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
	KeyImage    string `json:"key_image"`
}

type xmrFrostApplyKeyImagesResponse struct {
	Applied  int `json:"applied"`
	NotFound int `json:"not_found"`
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

type xmrFrostChainTxInfo struct {
	TxHash      string
	BlockHeight *uint64
	Fee         uint64
}

// Scanner/balance response types from the sidecar
type xmrFrostBalanceResponse struct {
	Balance          uint64  `json:"balance"`
	UnlockedBalance  uint64  `json:"unlocked_balance"`
	SpendableBalance *uint64 `json:"spendable_balance,omitempty"`
}

type xmrFrostRegisterVaultRequest struct {
	VaultPubKey         string                       `json:"vault_pub_key"`
	GroupKey            string                       `json:"group_key"`
	Address             string                       `json:"address"`
	SpendKeyHex         string                       `json:"spend_key_hex,omitempty"`
	Source              string                       `json:"source,omitempty"`
	VaultCreationHeight int64                        `json:"birthday,omitempty"`
	KeyImages           []xmrFrostApplyKeyImageRef   `json:"key_images,omitempty"`
	TxKeyEntries        []xmrFrostRegisterTxKeyEntry `json:"tx_keys,omitempty"`
}

type xmrFrostRegisterVaultResponse struct {
	Registered bool `json:"registered"`
}

type xmrFrostRegisterTxKeyEntry struct {
	TxHash      string   `json:"tx_hash"`
	TxKeys      []string `json:"tx_keys"`
	FromAddress string   `json:"from_address,omitempty"`
	ToAddress   string   `json:"to_address"`
	Amount      uint64   `json:"amount"`
	Fee         uint64   `json:"fee,omitempty"`
	Memo        string   `json:"memo,omitempty"`
}

type xmrFrostTransferEntry struct {
	TxID          string   `json:"txid"`
	Address       string   `json:"address"`
	FromAddress   string   `json:"from_address,omitempty"`
	VaultPubKey   string   `json:"vault_pub_key,omitempty"`
	TxKeys        []string `json:"tx_keys,omitempty"`
	Amount        uint64   `json:"amount"`
	Fee           uint64   `json:"fee"`
	Height        uint64   `json:"height"`
	Confirmations uint64   `json:"confirmations"`
	Note          string   `json:"note"`
	Locked        bool     `json:"locked"`
}

type xmrFrostVaultStateEntry struct {
	VaultPubKey             string  `json:"vault_pub_key"`
	Address                 string  `json:"address"`
	Birthday                uint64  `json:"birthday"`
	Rescan                  bool    `json:"rescan"`
	ScanHeight              *uint64 `json:"scan_height,omitempty"`
	InventoryFingerprint    string  `json:"inventory_fingerprint,omitempty"`
	RegisteredKeyImageCount int     `json:"registered_key_image_count,omitempty"`
	RegisteredTxKeyCount    int     `json:"registered_tx_key_count,omitempty"`
}

type xmrFrostVaultStatesResponse struct {
	Vaults []xmrFrostVaultStateEntry `json:"vaults"`
}

type xmrFrostTransfersResponse struct {
	Incoming []xmrFrostTransferEntry `json:"incoming"`
	Outgoing []xmrFrostTransferEntry `json:"outgoing"`
}

type xmrFrostOutgoingTransferStateEntry struct {
	TxHash                  string   `json:"tx_hash"`
	TxKeys                  []string `json:"tx_keys,omitempty"`
	FromAddress             string   `json:"from_address"`
	ToAddress               string   `json:"to_address"`
	Amount                  uint64   `json:"amount"`
	Fee                     uint64   `json:"fee"`
	Memo                    string   `json:"memo"`
	PlannedHeight           uint64   `json:"planned_height"`
	ObservedHeight          *uint64  `json:"observed_height,omitempty"`
	Status                  string   `json:"status"`
	ReportedEffectiveHeight *uint64  `json:"reported_effective_height,omitempty"`
	CreatedAt               int64    `json:"created_at"`
}

type xmrFrostOutgoingTransfersStateResponse struct {
	VaultPubKey string                               `json:"vault_pub_key"`
	Outgoing    []xmrFrostOutgoingTransferStateEntry `json:"outgoing"`
}

type xmrFrostValidateOutboundRequest struct {
	VaultPubKey string   `json:"vault_pub_key"`
	TxHash      string   `json:"tx_hash"`
	TxKeys      []string `json:"tx_keys"`
	FromAddress string   `json:"from_address,omitempty"`
	ToAddress   string   `json:"to_address"`
	Amount      uint64   `json:"amount"`
	Fee         uint64   `json:"fee,omitempty"`
	Memo        string   `json:"memo,omitempty"`
	Reconcile   bool     `json:"reconcile,omitempty"`
}

type xmrFrostValidateOutboundResponse struct {
	Tracked  bool `json:"tracked"`
	Marked   int  `json:"marked"`
	NotFound int  `json:"not_found"`
}

type xmrFrostRehydrateObservedOutboundRequest struct {
	VaultPubKey         string   `json:"vault_pub_key"`
	TxHash              string   `json:"tx_hash"`
	TxKeys              []string `json:"tx_keys,omitempty"`
	RegisteredInventory bool     `json:"registered_inventory,omitempty"`
	FromAddress         string   `json:"from_address,omitempty"`
	ToAddress           string   `json:"to_address"`
	Amount              uint64   `json:"amount"`
	Fee                 uint64   `json:"fee,omitempty"`
	Memo                string   `json:"memo,omitempty"`
	ObservedHeight      uint64   `json:"observed_height"`
}

type xmrFrostRehydrateObservedOutboundResponse struct {
	Rehydrated bool `json:"rehydrated"`
}

type xmrFrostPruneOutgoingTransfersRequest struct {
	VaultPubKey       string   `json:"vault_pub_key"`
	TxHashes          []string `json:"tx_hashes"`
	ObservedMinedOnly bool     `json:"observed_mined_only,omitempty"`
	TxKeyBackedOnly   bool     `json:"tx_key_backed_only,omitempty"`
}

type xmrFrostPruneOutgoingTransfersResponse struct {
	Removed []string `json:"removed"`
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

type moneroDaemonGetInfoResponse struct {
	Height int64 `json:"height"`
}

type moneroDaemonGetBlockParams struct {
	Height int64 `json:"height"`
}

type moneroDaemonBlockHeader struct {
	Height   int64  `json:"height"`
	Reward   uint64 `json:"reward"`
	Hash     string `json:"hash"`
	PrevHash string `json:"prev_hash"`
}

type moneroDaemonGetBlockResponse struct {
	BlockHeader moneroDaemonBlockHeader `json:"block_header"`
	TxHashes    []string                `json:"tx_hashes"`
	JSON        string                  `json:"json"`
}

type moneroDaemonBlockJSON struct {
	TxHashes []string `json:"tx_hashes"`
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
	if bearer == "" {
		if seed := strings.TrimSpace(os.Getenv("SIGNER_SEED_PHRASE")); seed != "" {
			bearer = deriveAuthBearer(seed)
		}
	}
	if (username == "") != (password == "") {
		return nil, fmt.Errorf("frost signer requires both username and password when basic auth is configured")
	}
	if !isLoopbackHost(parsedEndpoint.Hostname()) && bearer == "" && username == "" {
		return nil, fmt.Errorf("frost signer endpoint %q requires authorization_bearer or username/password", endpoint)
	}

	endpoint = strings.TrimSuffix(parsedEndpoint.String(), "/")

	daemonEndpoint := ""
	if strings.TrimSpace(cfg.RPCHost) != "" {
		parsedDaemonEndpoint, err := parseMoneroRPCEndpointURL(cfg.RPCHost)
		if err != nil {
			return nil, err
		}
		daemonEndpoint = normalizeMoneroRPCEndpoint(parsedDaemonEndpoint)
	}

	timeout := cfg.BlockScanner.HTTPRequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// sign/prepare may need to retry decoy ring selection many times when the
	// local pruned monerod produces undecodable txs at small candidate-set
	// heights. Cap at 120s so the sidecar's full retry budget can finish
	// before bifrost gives up.
	signTimeout := timeout
	if signTimeout < 120*time.Second {
		signTimeout = 120 * time.Second
	}

	return &FrostSigner{
		endpoint:       endpoint,
		daemonEndpoint: daemonEndpoint,

		dkgRound1Path:         "/v1/monero/dkg/round1",
		dkgRound2Path:         "/v1/monero/dkg/round2",
		dkgRound3Path:         "/v1/monero/dkg/round3",
		dkgFinalizePath:       "/v1/monero/dkg/finalize",
		signPreparePath:       "/v1/monero/sign/prepare",
		signPartialPath:       "/v1/monero/sign/partial",
		signCombinePath:       "/v1/monero/sign/combine",
		keyImagePreparePath:   "/v1/monero/key-image/prepare",
		keyImagePartialPath:   "/v1/monero/key-image/partial",
		keyImageCombinePath:   "/v1/monero/key-image/combine",
		keyImageValidatePath:  "/v1/monero/key-image/validate",
		validatePath:          "/v1/monero/outbounds/validate",
		outgoingRehydratePath: "/v1/monero/outbounds/rehydrate",
		vaultStatePath:        "/v1/monero/vaults",
		outgoingStatePath:     "/v1/monero/outbounds",
		outgoingPrunePath:     "/v1/monero/outbounds/prune",
		clearSessionsPath:     "/v1/monero/sessions/clear",
		broadcastPath:         "/v1/monero/broadcast",
		balancePath:           "/v1/monero/balance",
		transfersPath:         "/v1/monero/transfers",
		scannerSyncPath:       "/v1/monero/scanner/sync",
		addressesPath:         "/v1/monero/addresses",
		outputsPath:           "/v1/monero/outputs",
		registerPath:          "/v1/monero/vaults/register",
		feeRatePath:           "/v1/monero/fee-rate",
		chainHeightPath:       "/v1/monero/chain/height",
		chainBlockPath:        "/v1/monero/chain/block",
		chainTxPath:           "/v1/monero/chain/tx",

		client:       &http.Client{Timeout: timeout},
		daemonClient: &http.Client{Timeout: timeout},
		signClient:   &http.Client{Timeout: signTimeout},
		username:     username,
		password:     password,
		bearer:       bearer,
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

func parseMoneroRPCEndpointURL(endpoint string) (*url.URL, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("monero rpc host is empty")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid monero rpc host %q: %w", endpoint, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid monero rpc host scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, fmt.Errorf("invalid monero rpc host")
	}
	return parsed, nil
}

func normalizeMoneroRPCEndpoint(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	copied := *parsed
	copied.Path = strings.TrimSuffix(strings.TrimSuffix(copied.Path, "/"), "/json_rpc")
	return strings.TrimSuffix(copied.String(), "/")
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
	net := common.CurrentChainNetwork
	return net != common.MainNet
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

func (s *FrostSigner) FrostKeyImagePrepare(req xmrFrostKeyImagePrepareRequest) (xmrFrostKeyImagePrepareResponse, error) {
	var out xmrFrostKeyImagePrepareResponse
	if err := s.call(http.MethodPost, s.keyImagePreparePath, req, &out); err != nil {
		return xmrFrostKeyImagePrepareResponse{}, fmt.Errorf("fail to call frost key-image prepare endpoint: %w", err)
	}
	if strings.TrimSpace(out.SessionID) == "" {
		return xmrFrostKeyImagePrepareResponse{}, fmt.Errorf("frost key-image prepare endpoint returned empty session_id")
	}
	if strings.TrimSpace(out.Preprocess) == "" {
		return xmrFrostKeyImagePrepareResponse{}, fmt.Errorf("frost key-image prepare endpoint returned empty preprocess")
	}
	return out, nil
}

func (s *FrostSigner) FrostKeyImagePartial(req xmrFrostKeyImagePartialRequest) (xmrFrostKeyImagePartialResponse, error) {
	var out xmrFrostKeyImagePartialResponse
	if err := s.call(http.MethodPost, s.keyImagePartialPath, req, &out); err != nil {
		return xmrFrostKeyImagePartialResponse{}, fmt.Errorf("fail to call frost key-image partial endpoint: %w", err)
	}
	if strings.TrimSpace(out.SignatureShare) == "" {
		return xmrFrostKeyImagePartialResponse{}, fmt.Errorf("frost key-image partial endpoint returned empty signature_share")
	}
	return out, nil
}

func (s *FrostSigner) FrostKeyImageCombine(req xmrFrostKeyImageCombineRequest) (xmrFrostKeyImageCombineResponse, error) {
	var out xmrFrostKeyImageCombineResponse
	if err := s.call(http.MethodPost, s.keyImageCombinePath, req, &out); err != nil {
		return xmrFrostKeyImageCombineResponse{}, fmt.Errorf("fail to call frost key-image combine endpoint: %w", err)
	}
	if strings.TrimSpace(out.TxHash) == "" {
		return xmrFrostKeyImageCombineResponse{}, fmt.Errorf("frost key-image combine endpoint returned empty tx_hash")
	}
	if strings.TrimSpace(out.KeyImage) == "" || strings.TrimSpace(out.ProofC) == "" || strings.TrimSpace(out.ProofS) == "" {
		return xmrFrostKeyImageCombineResponse{}, fmt.Errorf("frost key-image combine endpoint returned incomplete proof payload")
	}
	return out, nil
}

func (s *FrostSigner) FrostKeyImageValidate(req xmrFrostKeyImageValidateRequest) (xmrFrostKeyImageValidateResponse, error) {
	var out xmrFrostKeyImageValidateResponse
	if err := s.call(http.MethodPost, s.keyImageValidatePath, req, &out); err != nil {
		return xmrFrostKeyImageValidateResponse{}, fmt.Errorf("fail to call frost key-image validate endpoint: %w", err)
	}
	return out, nil
}

// ClearSessions tells the sidecar to drop ALL in-memory FROST signing
// sessions. Called once at bifrost startup so a fresh bifrost lifecycle
// never inherits half-completed sessions left behind by a previous
// bifrost lifecycle (which can cause "leader not reachable" / "result
// sender not in active signer set" sign failures).
//
// Sidecar guarantees: only the in-memory session map + idempotency claims
// + inflight prepare counter are touched. Persistent keyshares and the
// sled vault state are NOT modified.
func (s *FrostSigner) ClearSessions() (int, error) {
	if s == nil || strings.TrimSpace(s.clearSessionsPath) == "" {
		return 0, nil
	}
	var out struct {
		ClearedSessions int    `json:"cleared_sessions"`
		Status          string `json:"status"`
	}
	if err := s.call(http.MethodPost, s.clearSessionsPath, struct{}{}, &out); err != nil {
		return 0, fmt.Errorf("fail to call frost sessions clear endpoint: %w", err)
	}
	return out.ClearedSessions, nil
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

func (s *FrostSigner) RegisterVault(req xmrFrostRegisterVaultRequest) (xmrFrostRegisterVaultResponse, error) {
	var out xmrFrostRegisterVaultResponse
	if err := s.call(http.MethodPost, s.registerPath, req, &out); err != nil {
		return xmrFrostRegisterVaultResponse{}, fmt.Errorf("fail to call frost register vault endpoint: %w", err)
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

func (s *FrostSigner) ListVaultStates() (xmrFrostVaultStatesResponse, error) {
	var out xmrFrostVaultStatesResponse
	if err := s.callGet(s.vaultStatePath, &out); err != nil {
		return xmrFrostVaultStatesResponse{}, fmt.Errorf("fail to call frost vault state endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) GetOutgoingTransferState(vaultPubKey string) (xmrFrostOutgoingTransfersStateResponse, error) {
	var out xmrFrostOutgoingTransfersStateResponse
	path := s.outgoingStatePath + "/" + url.PathEscape(vaultPubKey)
	if err := s.callGet(path, &out); err != nil {
		return xmrFrostOutgoingTransfersStateResponse{}, fmt.Errorf("fail to call frost outgoing state endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) PruneOutgoingTransfers(req xmrFrostPruneOutgoingTransfersRequest) (xmrFrostPruneOutgoingTransfersResponse, error) {
	var out xmrFrostPruneOutgoingTransfersResponse
	if err := s.call(http.MethodPost, s.outgoingPrunePath, req, &out); err != nil {
		return xmrFrostPruneOutgoingTransfersResponse{}, fmt.Errorf("fail to call frost outgoing prune endpoint: %w", err)
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
	return s.getOutputs(vaultPubKey, "", "")
}

func (s *FrostSigner) GetAllOutputs(vaultPubKey string) ([]xmrFrostOutputEntry, error) {
	return s.getOutputs(vaultPubKey, "all", "")
}

func (s *FrostSigner) GetAllPersistedOutputs(vaultPubKey string) ([]xmrFrostOutputEntry, error) {
	return s.getOutputs(vaultPubKey, "all", "store")
}

func (s *FrostSigner) getOutputs(vaultPubKey, view, source string) ([]xmrFrostOutputEntry, error) {
	var out xmrFrostOutputsResponse
	path := s.outputsPath + "/" + url.PathEscape(vaultPubKey)
	values := url.Values{}
	if view != "" {
		values.Set("view", view)
	}
	if source != "" {
		values.Set("source", source)
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
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

func (s *FrostSigner) ResetScanner(vaultPubKey string, birthdayHeight int64) error {
	req := struct {
		VaultPubKey    string `json:"vault_pub_key"`
		BirthdayHeight int64  `json:"birthday_height"`
	}{vaultPubKey, birthdayHeight}
	return s.call(http.MethodPost, "/v1/monero/scanner/reset", req, nil)
}

func (s *FrostSigner) GetChainHeight() (int64, error) {
	var daemonErr error
	if s.hasDaemonRPC() {
		height, err := s.getDaemonChainHeight()
		if err == nil {
			return height, nil
		}
		daemonErr = fmt.Errorf("fail to call monero daemon chain-height rpc: %w", err)
	}

	var out xmrFrostChainHeightResponse
	if err := s.callGet(s.chainHeightPath, &out); err != nil {
		if daemonErr != nil {
			return 0, fmt.Errorf("%v; fail to call frost chain-height endpoint: %w", daemonErr, err)
		}
		return 0, fmt.Errorf("fail to call frost chain-height endpoint: %w", err)
	}
	return out.Height, nil
}

func (s *FrostSigner) GetChainBlock(height int64) (chainBlock, error) {
	if height < 0 {
		return chainBlock{}, fmt.Errorf("height must be >= 0")
	}

	var daemonErr error
	if s.hasDaemonRPC() {
		block, err := s.getDaemonChainBlock(height)
		if err == nil {
			return block, nil
		}
		if errors.Is(err, ErrFrostUnavailableBlock) {
			return chainBlock{}, err
		}
		daemonErr = fmt.Errorf("fail to call monero daemon chain-block rpc: %w", err)
	}

	var out xmrFrostChainBlockResponse
	path := fmt.Sprintf("%s/%d", s.chainBlockPath, height)
	if err := s.callGet(path, &out); err != nil {
		if isFrostHTTPStatus(err, http.StatusConflict) {
			return chainBlock{}, fmt.Errorf("%w: %d", ErrFrostUnavailableBlock, height)
		}
		if daemonErr != nil {
			return chainBlock{}, fmt.Errorf("%v; fail to call frost chain-block endpoint: %w", daemonErr, err)
		}
		return chainBlock{}, fmt.Errorf("fail to call frost chain-block endpoint: %w", err)
	}
	return chainBlock(out), nil
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

func (s *FrostSigner) GetChainTxInfo(txHash string) (xmrFrostChainTxInfo, error) {
	normalized := strings.TrimSpace(txHash)
	if normalized == "" {
		return xmrFrostChainTxInfo{}, fmt.Errorf("tx hash is empty")
	}
	if !s.hasDaemonRPC() {
		return xmrFrostChainTxInfo{}, fmt.Errorf("monero daemon rpc is not configured")
	}

	var out moneroDaemonGetTransactionsResponse
	if err := s.callDaemonJSON("/get_transactions", moneroDaemonGetTransactionsRequest{
		TxHashes:     []string{normalized},
		DecodeAsJSON: true,
	}, &out); err != nil {
		return xmrFrostChainTxInfo{}, fmt.Errorf("fail to query monero daemon transaction %s: %w", normalized, err)
	}

	for _, missed := range out.MissedTx {
		if strings.EqualFold(strings.TrimSpace(missed), normalized) {
			return xmrFrostChainTxInfo{}, fmt.Errorf("transaction %s not found", normalized)
		}
	}

	for _, entry := range out.Txs {
		if !strings.EqualFold(strings.TrimSpace(entry.TxHash), normalized) {
			continue
		}
		var parsed moneroDaemonParsedTx
		if strings.TrimSpace(entry.AsJSON) == "" {
			return xmrFrostChainTxInfo{}, fmt.Errorf("daemon transaction %s is missing json metadata", normalized)
		}
		if err := json.Unmarshal([]byte(entry.AsJSON), &parsed); err != nil {
			return xmrFrostChainTxInfo{}, fmt.Errorf("fail to parse daemon transaction %s metadata: %w", normalized, err)
		}
		fee, err := parseMoneroDaemonUint(parsed.RctSignatures.TxnFee)
		if err != nil {
			return xmrFrostChainTxInfo{}, fmt.Errorf("fail to parse daemon transaction %s fee: %w", normalized, err)
		}

		return xmrFrostChainTxInfo{
			TxHash:      strings.TrimSpace(entry.TxHash),
			BlockHeight: entry.BlockHeight,
			Fee:         fee,
		}, nil
	}

	return xmrFrostChainTxInfo{}, fmt.Errorf("transaction %s not found", normalized)
}

func (s *FrostSigner) ValidateOutbound(req xmrFrostValidateOutboundRequest) (xmrFrostValidateOutboundResponse, error) {
	var out xmrFrostValidateOutboundResponse
	if err := s.call(http.MethodPost, s.validatePath, req, &out); err != nil {
		return xmrFrostValidateOutboundResponse{}, fmt.Errorf("fail to call frost validate-outbound endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) RehydrateObservedOutbound(req xmrFrostRehydrateObservedOutboundRequest) (xmrFrostRehydrateObservedOutboundResponse, error) {
	var out xmrFrostRehydrateObservedOutboundResponse
	if err := s.call(http.MethodPost, s.outgoingRehydratePath, req, &out); err != nil {
		return xmrFrostRehydrateObservedOutboundResponse{}, fmt.Errorf("fail to call frost rehydrate-outbound endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) ApplyKeyImages(vaultPubKey string, refs []xmrFrostApplyKeyImageRef, reconcile bool) (xmrFrostApplyKeyImagesResponse, error) {
	var out xmrFrostApplyKeyImagesResponse
	if refs == nil {
		refs = []xmrFrostApplyKeyImageRef{}
	}
	req := xmrFrostApplyKeyImagesRequest{
		VaultPubKey: vaultPubKey,
		KeyImages:   refs,
		Reconcile:   reconcile,
	}
	err := s.call(http.MethodPost, "/v1/monero/outputs/apply-key-images", req, &out)
	if err != nil {
		return out, fmt.Errorf("fail to call frost apply-key-images endpoint: %w", err)
	}
	return out, nil
}

func (s *FrostSigner) ApplySpentKeyImages(vaultPubKey string, keyImages []string, spentHeight uint64) (xmrFrostApplyKeyImagesResponse, error) {
	var out xmrFrostApplyKeyImagesResponse
	refs := make([]xmrFrostApplyKeyImageRef, 0, len(keyImages))
	for _, keyImage := range keyImages {
		normalized := strings.ToLower(strings.TrimSpace(keyImage))
		if normalized == "" {
			continue
		}
		refs = append(refs, xmrFrostApplyKeyImageRef{KeyImage: normalized})
	}
	req := xmrFrostApplyKeyImagesRequest{
		VaultPubKey: vaultPubKey,
		KeyImages:   refs,
		Spend:       true,
		SpentHeight: spentHeight,
	}
	err := s.call(http.MethodPost, "/v1/monero/outputs/apply-key-images", req, &out)
	if err != nil {
		return out, fmt.Errorf("fail to call frost apply-spent-key-images endpoint: %w", err)
	}
	return out, nil
}

func deriveAuthBearer(seedPhrase string) string {
	return strings.TrimSpace(seedPhrase)
}

type xmrFrostOutboundMeta struct {
	FromAddress    string
	ToAddress      string
	Amount         uint64
	Fee            uint64
	Memo           string
	ObservedHeight int64
}

type xmrFrostVaultKeysEntry struct {
	VaultPubKey  string `json:"vault_pub_key"`
	HasFrostKeys bool   `json:"has_frost_keys"`
}

func (s *FrostSigner) ListVaultKeys() ([]xmrFrostVaultKeysEntry, error) {
	var out struct {
		Vaults []xmrFrostVaultKeysEntry `json:"vaults"`
	}
	err := s.callGet("/v1/monero/keys", &out)
	if err != nil {
		return nil, fmt.Errorf("fail to list vault keys: %w", err)
	}
	return out.Vaults, nil
}

func (s *FrostSigner) ExportFrostKeys(vaultPubKey string) (string, error) {
	var out struct {
		KeyBytesHex string `json:"key_bytes_hex"`
	}
	path := fmt.Sprintf("/v1/monero/keys/%s/export", url.PathEscape(vaultPubKey))
	err := s.callGet(path, &out)
	if err != nil {
		return "", fmt.Errorf("fail to export frost keys: %w", err)
	}
	return out.KeyBytesHex, nil
}

func (s *FrostSigner) RestoreFrostKeys(vaultPubKey, keyBytesHex string) (bool, error) {
	var out struct {
		Restored bool `json:"restored"`
	}
	req := struct {
		VaultPubKey string `json:"vault_pub_key"`
		KeyBytesHex string `json:"key_bytes_hex"`
	}{
		VaultPubKey: vaultPubKey,
		KeyBytesHex: keyBytesHex,
	}
	err := s.call(http.MethodPost, "/v1/monero/keys/restore", req, &out)
	if err != nil {
		return false, fmt.Errorf("fail to restore frost keys: %w", err)
	}
	return out.Restored, nil
}

// --- HTTP helpers ---

func (s *FrostSigner) hasDaemonRPC() bool {
	return s != nil && s.daemonClient != nil && strings.TrimSpace(s.daemonEndpoint) != ""
}

func (s *FrostSigner) getDaemonChainHeight() (int64, error) {
	var out moneroDaemonGetInfoResponse
	if err := s.callDaemonJSONRPC("get_info", struct{}{}, &out); err != nil {
		return 0, err
	}
	if out.Height <= 0 {
		return 0, nil
	}
	return out.Height - 1, nil
}

func (s *FrostSigner) getDaemonChainBlock(height int64) (chainBlock, error) {
	tipHeight, err := s.getDaemonChainHeight()
	if err != nil {
		return chainBlock{}, err
	}
	if height > tipHeight {
		return chainBlock{}, fmt.Errorf("%w: %d", ErrFrostUnavailableBlock, height)
	}

	var out moneroDaemonGetBlockResponse
	if err := s.callDaemonJSONRPC("get_block", moneroDaemonGetBlockParams{Height: height}, &out); err != nil {
		return chainBlock{}, err
	}

	txHashes := out.TxHashes
	if len(txHashes) == 0 && strings.TrimSpace(out.JSON) != "" {
		var parsed moneroDaemonBlockJSON
		if err := json.Unmarshal([]byte(out.JSON), &parsed); err == nil && len(parsed.TxHashes) > 0 {
			txHashes = parsed.TxHashes
		}
	}

	return chainBlock{
		Height:       out.BlockHeader.Height,
		Reward:       out.BlockHeader.Reward,
		Hash:         out.BlockHeader.Hash,
		PreviousHash: out.BlockHeader.PrevHash,
		TxHashes:     txHashes,
	}, nil
}

func (s *FrostSigner) callDaemonJSONRPC(method string, params any, out any) error {
	if !s.hasDaemonRPC() {
		return fmt.Errorf("monero daemon rpc is not configured")
	}

	body, err := json.Marshal(moneroDaemonJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("fail to marshal monero daemon request payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, s.daemonEndpoint+"/json_rpc", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fail to create monero daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.bearer != "" {
		req.Header.Set("Authorization", s.bearer)
	} else if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.daemonClient.Do(req)
	if err != nil {
		return fmt.Errorf("fail to call monero daemon rpc %s: %w", method, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	respBody, err := readBoundedResponseBody(resp.Body, maxFrostResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read monero daemon response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("monero daemon rpc %s returned status %d: %s", method, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("monero daemon rpc %s returned empty response body", method)
	}

	var envelope moneroDaemonJSONRPCResponse
	if err = json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("fail to unmarshal monero daemon rpc response: %w", err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("monero daemon rpc %s returned empty result", method)
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("fail to unmarshal monero daemon rpc result: %w", err)
	}
	return nil
}

func (s *FrostSigner) callDaemonJSON(path string, payload any, out any) error {
	if !s.hasDaemonRPC() {
		return fmt.Errorf("monero daemon rpc is not configured")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fail to marshal monero daemon request payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, s.daemonEndpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fail to create monero daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.bearer != "" {
		req.Header.Set("Authorization", s.bearer)
	} else if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.daemonClient.Do(req)
	if err != nil {
		return fmt.Errorf("fail to call monero daemon endpoint %s: %w", path, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	respBody, err := readBoundedResponseBody(resp.Body, maxFrostResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read monero daemon response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("monero daemon endpoint %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("monero daemon endpoint %s returned empty response body", path)
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("fail to unmarshal monero daemon response: %w", err)
	}
	return nil
}

func parseMoneroDaemonUint(raw json.RawMessage) (uint64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, fmt.Errorf("numeric field is empty")
	}
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		trimmed = strings.TrimSpace(unquoted)
	}
	value, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func (s *FrostSigner) callGet(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, s.endpoint+path, nil)
	if err != nil {
		return fmt.Errorf("fail to create frost request: %w", err)
	}
	if s.bearer != "" {
		req.Header.Set("Authorization", s.bearer)
	} else if s.username != "" {
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
		req.Header.Set("Authorization", s.bearer)
	} else if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	// Sign-prepare can take significantly longer than other endpoints because
	// the sidecar may iterate through many deterministic decoy seeds before
	// finding a ring whose elements all parse cleanly under our local pruned
	// monerod. Route it through a longer-timeout client.
	httpClient := s.client
	if (path == s.signPreparePath || path == s.keyImagePreparePath || path == s.keyImageCombinePath) && s.signClient != nil {
		httpClient = s.signClient
	}
	resp, err := httpClient.Do(req)
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
