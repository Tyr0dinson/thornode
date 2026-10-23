package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------------------------------------------------------------------------
// Responses
// -------------------------------------------------------------------------------------

type P2PStatusPeer struct {
	Address   string `json:"address"`
	IP        string `json:"ip"`
	Status    string `json:"status"`
	Direction string `json:"direction"`

	StoredPeerID string `json:"stored_peer_id"`
	NodesPeerID  string `json:"nodes_peer_id"`

	P2PPortOpen bool  `json:"p2p_port_open"`
	P2PDialMs   int64 `json:"p2p_dial_ms"`
}

type P2PStatusResponse struct {
	ThornodeHeight int64           `json:"thornode_height"`
	Peers          []P2PStatusPeer `json:"peers"`
	PeerCount      int             `json:"peer_count"`
	Errors         []string        `json:"errors"`
}

type ProviderEntry struct {
	// Legacy single-host fields (backward compat).
	RPCHost   string `json:"rpc_host,omitempty"`
	APIHost   string `json:"api_host,omitempty"`
	ChainHost string `json:"chain_host,omitempty"`
	GRPCHost  string `json:"grpc_host,omitempty"`
	// Providers lists all configured providers for this chain.
	Providers []ProviderSummary `json:"providers,omitempty"`
}

type ProviderSummary struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Host     string `json:"host"`
	// Live health from the endpoint manager (empty for chains without one).
	State            string `json:"state,omitempty"`
	Active           bool   `json:"active,omitempty"`
	Height           int64  `json:"height,omitempty"`
	ProbeLatencyMs   int64  `json:"probe_latency_ms,omitempty"`
	InCooldown       bool   `json:"in_cooldown,omitempty"`
	IdentityVerified bool   `json:"identity_verified,omitempty"`
	IntegrityEvicted bool   `json:"integrity_evicted,omitempty"`
	InlineErrors     int    `json:"inline_errors,omitempty"`
	ProbeStrikes     int    `json:"probe_strikes,omitempty"`
	LastErrorKind    string `json:"last_error_kind,omitempty"`
	DisabledReason   string `json:"disabled_reason,omitempty"`
}

type ProviderResponse map[string]ProviderEntry

type ScannerResponse struct {
	Chain              string `json:"chain"`
	ChainHeight        int64  `json:"chain_height"`
	BlockScannerHeight int64  `json:"block_scanner_height"`
	ScannerHeightDiff  int64  `json:"scanner_height_diff"`
	Healthy            *bool  `json:"healthy,omitempty"`
}

type signingChain struct {
	Chain               string `json:"chain"`
	LatestBroadcastedTx string `json:"latest_broadcasted_tx"`
	LatestObservedTx    string `json:"latest_observed_tx"`
	CurrentSequence     int64  `json:"current_sequence"`
}

type VaultResponse struct {
	Pubkey       common.PubKey     `json:"pubkey"`
	Status       types.VaultStatus `json:"status"`
	ChainDetails []signingChain    `json:"chain_details"`
}

// -------------------------------------------------------------------------------------
// Health Server
// -------------------------------------------------------------------------------------

// HealthServer to provide something for health check and also p2pid
type HealthServer struct {
	logger    zerolog.Logger
	s         *http.Server
	tssServer tss.Server
	chains    map[common.Chain]chainclients.ChainClient
}

// NewHealthServer create a new instance of health server
func NewHealthServer(addr string, tssServer tss.Server, chains map[common.Chain]chainclients.ChainClient) *HealthServer {
	hs := &HealthServer{
		logger:    log.With().Str("module", "http").Logger(),
		tssServer: tssServer,
		chains:    chains,
	}
	s := &http.Server{
		Addr:              addr,
		Handler:           hs.newHandler(),
		ReadHeaderTimeout: 2 * time.Second,
	}
	hs.s = s

	return hs
}

func (s *HealthServer) newHandler() http.Handler {
	router := mux.NewRouter()
	router.Handle("/ping", http.HandlerFunc(s.pingHandler)).Methods(http.MethodGet)
	router.Handle("/p2pid", http.HandlerFunc(s.getP2pIDHandler)).Methods(http.MethodGet)
	router.Handle("/status/p2p", http.HandlerFunc(s.p2pStatus)).Methods(http.MethodGet)
	router.Handle("/status/scanner", http.HandlerFunc(s.chainScanner)).Methods(http.MethodGet)
	router.Handle("/status/provider", http.HandlerFunc(s.providerStatus)).Methods(http.MethodGet)
	router.Handle("/status/provider/{chain}", http.HandlerFunc(s.providerStatusForChain)).Methods(http.MethodGet)
	router.Handle("/status/signing", http.HandlerFunc(s.currentSigning)).Methods(http.MethodGet)
	router.Handle("/version", http.HandlerFunc(s.versionHandler)).Methods(http.MethodGet)
	return router
}

func (s *HealthServer) pingHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *HealthServer) versionHandler(w http.ResponseWriter, _ *http.Request) {
	resp := struct {
		Version   string `json:"version"`
		GitCommit string `json:"git_commit"`
	}{
		Version:   constants.Version,
		GitCommit: constants.GitCommit,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error().Err(err).Msg("fail to write version response")
	}
}

func (s *HealthServer) getP2pIDHandler(w http.ResponseWriter, _ *http.Request) {
	localPeerID := s.tssServer.GetLocalPeerID()
	_, err := w.Write([]byte(localPeerID))
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to write to response")
	}
}

func (s *HealthServer) p2pStatus(w http.ResponseWriter, _ *http.Request) {
	res := &P2PStatusResponse{Peers: make([]P2PStatusPeer, 0)}

	// get thorchain nodes
	nodesByIP := map[string]openapi.Node{}
	thornode := config.GetBifrost().Thorchain.ChainHost
	url := fmt.Sprintf("http://%s/thorchain/nodes", thornode)
	resp, err := http.Get(url)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get thornode status")
	} else {
		defer resp.Body.Close()

		// set the height from header
		res.ThornodeHeight, err = strconv.ParseInt(resp.Header.Get("grpc-metadata-x-cosmos-block-height"), 10, 64)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to parse thornode height")
		}

		nodes := make([]openapi.Node, 0)
		if err = json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
			s.logger.Error().Err(err).Msg("fail to decode thornode status")
		} else {
			for _, node := range nodes {
				otherNode, exists := nodesByIP[node.IpAddress]

				if !exists || (otherNode.Status != types.NodeStatus_Active.String() && otherNode.PreflightStatus.Status != types.NodeStatus_Ready.String()) {
					// only add node if the IP is not already in the map
					nodesByIP[node.IpAddress] = node
				} else if node.Status == types.NodeStatus_Active.String() || node.PreflightStatus.Status == types.NodeStatus_Ready.String() {
					// if both nodes are active or ready, report an error
					res.Errors = append(res.Errors, fmt.Sprintf("active node IP reuse: %s", node.IpAddress))
				}
			}
		}
	}

	// get all connected peers
	peerInfos := s.tssServer.GetKnownPeers()
	res.PeerCount = len(peerInfos)

	// ping and http get /p2pid on all peers
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}
	for _, pi := range peerInfos {
		wg.Add(1)
		go func(pi tss.PeerInfo) {
			peer := P2PStatusPeer{
				StoredPeerID: pi.ID,
				IP:           pi.Address,
				Direction:    pi.Direction,
			}

			defer func() {
				mu.Lock()
				res.Peers = append(res.Peers, peer)
				mu.Unlock()
				wg.Done()
			}()

			// nothing to do if no addresses
			if pi.Address == "" {
				return
			}

			// check if the node is in thornode
			if node, ok := nodesByIP[pi.Address]; ok {
				peer.Address = node.NodeAddress
				peer.Status = node.Status
				peer.NodesPeerID = node.PeerId
			}

			// check the p2p port
			start := time.Now()
			peer.P2PPortOpen = checkPortOpen(pi.Address, 5040)
			peer.P2PDialMs = int64(time.Since(start) / time.Millisecond)
		}(pi)
	}
	wg.Wait()

	// write the response
	jsonBytes, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to write to response")
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		_, err = w.Write(jsonBytes)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to write to response")
		}
	}
}

func (s *HealthServer) currentSigning(w http.ResponseWriter, _ *http.Request) {
	res := make([]VaultResponse, 0)

	thornode := config.GetBifrost().Thorchain.ChainHost
	url := fmt.Sprintf("http://%s%s", thornode, thorclient.AsgardVault)
	resp, err := http.Get(url)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get thornode status")
	} else {
		defer resp.Body.Close()

		vaults := make([]openapi.Vault, 0)
		if err = json.NewDecoder(resp.Body).Decode(&vaults); err != nil {
			s.logger.Error().Err(err).Msg("fail to decode thornode status")
		}
		for _, vault := range vaults {
			valRes := VaultResponse{
				Pubkey:       common.PubKey(*vault.PubKey),
				Status:       types.VaultStatus(types.VaultStatus_value[vault.Status]),
				ChainDetails: make([]signingChain, 0),
			}

			for _, chain := range vault.Chains {
				client, found := s.chains[common.Chain(strings.ToUpper(chain))]
				if !found {
					s.logger.Error().Msgf("failed to get bifrost chain client for %s", chain)
					continue
				}
				var account common.Account
				account, err = client.GetAccount(common.PubKey(*vault.PubKey), nil)
				if err != nil {
					s.logger.Error().Err(err).Msgf("failed to get account for vault:%s on chain:%s", *vault.PubKey, chain)
					continue
				}
				var lastObserved, lastBroadcasted string
				lastObserved, lastBroadcasted, err = client.GetLatestTxForVault(*vault.PubKey)
				if err != nil {
					s.logger.Error().Err(err).Msgf("failed to get latest tx for vault:%s on chain:%s", *vault.PubKey, chain)
				}
				valRes.ChainDetails = append(valRes.ChainDetails, signingChain{
					Chain:               chain,
					LatestBroadcastedTx: lastBroadcasted,
					LatestObservedTx:    lastObserved,
					CurrentSequence:     account.Sequence,
				})
			}
			res = append(res, valRes)
		}
	}

	// write the response
	jsonBytes, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to write to response")
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		_, err = w.Write(jsonBytes)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to write to response")
		}
	}
}

func (s *HealthServer) chainScanner(w http.ResponseWriter, _ *http.Request) {
	res := make(map[string]ScannerResponse)

	// Iterate through each chain client
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}
	for chain, client := range s.chains {
		wg.Add(1)
		chain := chain
		client := client
		go func() {
			defer wg.Done()

			// Fetch the current block height of the chain daemon
			height, err := client.GetHeight()
			if err != nil {
				// failed to get chain height
				height = -1
			}

			// check for local blockScanner height
			blockScannerHeight, err := client.GetBlockScannerHeight()
			if err != nil {
				blockScannerHeight = -1
			}

			var scannerHeightDiff int64
			if height < 0 || blockScannerHeight < 0 {
				scannerHeightDiff = -1
			} else {
				scannerHeightDiff = height - blockScannerHeight
			}

			healthy := client.IsBlockScannerHealthy()
			mu.Lock()
			res[chain.String()] = ScannerResponse{
				Chain:              chain.String(),
				ChainHeight:        height,
				BlockScannerHeight: blockScannerHeight,
				ScannerHeightDiff:  scannerHeightDiff,
				Healthy:            &healthy,
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Fetch thorchain height
	thornode := config.GetBifrost().Thorchain.ChainHost
	url := fmt.Sprintf("http://%s/thorchain/lastblock", thornode)
	resp, err := http.Get(url)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get thornode status")
	} else {
		defer resp.Body.Close()
		var height int64
		height, err = strconv.ParseInt(resp.Header.Get("grpc-metadata-x-cosmos-block-height"), 10, 64)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to parse thornode height")
		}
		res[common.THORChain.String()] = ScannerResponse{
			Chain:              common.THORChain.String(),
			ChainHeight:        height,
			BlockScannerHeight: -1, // TODO: pending for thorchain
			ScannerHeightDiff:  -1,
		}
	}

	// write the response
	jsonBytes, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to write to response")
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		_, err = w.Write(jsonBytes)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to write to response")
		}
	}
}

// providerEntry renders one chain's endpoints with live health when the chain
// client runs an endpoint manager. Hosts are classified, never printed raw.
func (s *HealthServer) providerEntry(client chainclients.ChainClient) ProviderEntry {
	cfg := client.GetConfig()
	entry := ProviderEntry{
		RPCHost:   classifyHost(cfg.RPCHost),
		APIHost:   classifyHost(cfg.APIHost),
		ChainHost: classifyHost(cfg.ChainHost),
		GRPCHost:  classifyHost(cfg.CosmosGRPCHost),
		Providers: buildProviderSummaries(cfg),
	}
	mp, ok := client.(endpoints.ManagerProvider)
	if !ok || mp.EndpointManager() == nil {
		return entry
	}
	live := make(map[string]endpoints.Snapshot)
	for _, snap := range mp.EndpointManager().Status() {
		live[snap.Name] = snap
	}
	for i := range entry.Providers {
		snap, found := live[entry.Providers[i].Name]
		if !found {
			continue
		}
		entry.Providers[i].State = snap.State
		entry.Providers[i].Active = snap.Active
		entry.Providers[i].Height = snap.Height
		entry.Providers[i].ProbeLatencyMs = snap.ProbeLatencyMs
		entry.Providers[i].InCooldown = snap.InCooldown
		entry.Providers[i].IdentityVerified = snap.IdentityVerified
		entry.Providers[i].IntegrityEvicted = snap.IntegrityEvicted
		entry.Providers[i].InlineErrors = snap.InlineErrors
		entry.Providers[i].ProbeStrikes = snap.ProbeStrikes
		entry.Providers[i].LastErrorKind = snap.LastErrorKind
		entry.Providers[i].DisabledReason = snap.DisabledReason
	}
	return entry
}

// providerStatus lists every chain's endpoints with live health.
func (s *HealthServer) providerStatus(w http.ResponseWriter, _ *http.Request) {
	res := make(ProviderResponse, len(s.chains))
	for chain, client := range s.chains {
		res[chain.String()] = s.providerEntry(client)
	}
	s.writeJSON(w, res)
}

// providerStatusForChain is providerStatus for a single chain (404 if unknown).
func (s *HealthServer) providerStatusForChain(w http.ResponseWriter, r *http.Request) {
	chain := common.Chain(strings.ToUpper(mux.Vars(r)["chain"]))
	client, ok := s.chains[chain]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.writeJSON(w, ProviderResponse{chain.String(): s.providerEntry(client)})
}

func (s *HealthServer) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error().Err(err).Msg("fail to write response")
	}
}

func buildProviderSummaries(cfg config.BifrostChainConfiguration) []ProviderSummary {
	endpoints := cfg.GetEndpoints()
	summaries := make([]ProviderSummary, len(endpoints))
	for i, e := range endpoints {
		host := e.RPCHost
		if e.CosmosGRPCHost != "" {
			host = e.CosmosGRPCHost
		}
		if e.APIHost != "" {
			host = e.APIHost
		}
		summaries[i] = ProviderSummary{
			Name:     e.Name,
			Priority: e.Priority,
			Host:     classifyHost(host),
		}
	}
	return summaries
}

// classifyHost returns "self-hosted" when rawURL resolves to a loopback, private,
// link-local, or bare (no-dot) hostname, and returns the registrable domain
// (e.g. "infura.io") for external providers. An empty input returns "".
func classifyHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	// Add a scheme so url.Parse can extract the hostname reliably.
	toParse := rawURL
	if !strings.Contains(toParse, "://") {
		toParse = "http://" + toParse
	}
	u, err := url.Parse(toParse)
	if err != nil {
		return "self-hosted"
	}
	host := u.Hostname()
	if host == "" {
		return "self-hosted"
	}

	// IP address – check whether it is in a local/private range.
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return "self-hosted"
		}
		return "external-host" // public IP
	}

	// Bare hostnames (no dots) such as "localhost", "bitcoin-daemon", "thornode".
	if strings.EqualFold(host, "localhost") || !strings.Contains(host, ".") {
		return "self-hosted"
	}

	// Local-network TLDs.
	lower := strings.ToLower(host)
	for _, suffix := range []string{".local", ".internal", ".lan", ".home", ".localdomain"} {
		if strings.HasSuffix(lower, suffix) {
			return "self-hosted"
		}
	}

	// External provider – return the registrable domain (last two labels).
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return "unknown"
}

// Start health server
func (s *HealthServer) Start() error {
	if s.s == nil {
		return errors.New("invalid http server instance")
	}
	if err := s.s.ListenAndServe(); err != nil {
		if err != http.ErrServerClosed {
			return fmt.Errorf("fail to start http server: %w", err)
		}
	}
	return nil
}

func (s *HealthServer) Stop() error {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.s.Shutdown(c)
	if err != nil {
		log.Error().Err(err).Msg("Failed to shutdown the Tss server gracefully")
	}
	return err
}

func checkPortOpen(host string, port int) bool {
	address := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}
