package monero

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// FrostSignerEngine is the interface for FROST DKG and signing operations.
type FrostSignerEngine interface {
	// FROST DKG
	FrostDKGRound1(req xmrFrostDKGRound1Request) (xmrFrostDKGRound1Response, error)
	FrostDKGRound2(req xmrFrostDKGRound2Request) (xmrFrostDKGRound2Response, error)
	FrostDKGRound3(req xmrFrostDKGRound3Request) (xmrFrostDKGRound3Response, error)

	// FROST Signing
	FrostSignPrepare(req xmrFrostSignPrepareRequest) (xmrFrostSignPrepareResponse, error)
	FrostSignPartial(req xmrFrostSignPartialRequest) (xmrFrostSignPartialResponse, error)
	FrostSignCombine(req xmrFrostSignCombineRequest) (xmrFrostSignCombineResponse, error)

	// FROST Broadcast
	FrostBroadcast(req xmrFrostBroadcastRequest) (xmrFrostBroadcastResponse, error)
}

type signerHealthChecker interface {
	HealthCheck(ctx context.Context) error
}

type HTTPSignerService struct {
	logger   zerolog.Logger
	engine   FrostSignerEngine
	server   *http.Server
	username string
	password string
	bearer   string
	now      func() time.Time
}

const (
	xmrSignerMaxRequestBodyBytes = 1 << 20 // 1 MiB
)

func NewHTTPSignerService(addr string, engine FrostSignerEngine, logger zerolog.Logger) *HTTPSignerService {
	return NewHTTPSignerServiceWithAuth(addr, engine, logger, "", "", "")
}

func NewHTTPSignerServiceWithAuth(addr string, engine FrostSignerEngine, logger zerolog.Logger, username, password, bearer string) *HTTPSignerService {
	mux := http.NewServeMux()
	s := &HTTPSignerService{
		logger:   logger,
		engine:   engine,
		username: strings.TrimSpace(username),
		password: strings.TrimSpace(password),
		bearer:   strings.TrimSpace(bearer),
		now:      time.Now,
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
	mux.HandleFunc("/v1/monero/dkg/round1", s.handleFrostDKGRound1)
	mux.HandleFunc("/v1/monero/dkg/round2", s.handleFrostDKGRound2)
	mux.HandleFunc("/v1/monero/dkg/round3", s.handleFrostDKGRound3)
	mux.HandleFunc("/v1/monero/sign/prepare", s.handleFrostSignPrepare)
	mux.HandleFunc("/v1/monero/sign/partial", s.handleFrostSignPartial)
	mux.HandleFunc("/v1/monero/sign/combine", s.handleFrostSignCombine)
	mux.HandleFunc("/v1/monero/broadcast", s.handleFrostBroadcast)
	mux.HandleFunc("/health", s.handleHealth)
	return s
}

func (s *HTTPSignerService) Start() error {
	if s.engine == nil {
		return fmt.Errorf("signer engine is nil")
	}
	if s.username == "" && s.bearer == "" && !isLoopbackListenAddr(s.server.Addr) {
		return fmt.Errorf("unauthenticated xmr signer must bind to loopback")
	}
	s.logger.Info().Str("addr", s.server.Addr).Msg("starting monero signer service")
	err := s.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *HTTPSignerService) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func matchesBearerToken(authHeader, expected string) bool {
	authHeader = strings.TrimSpace(authHeader)
	expected = strings.TrimSpace(expected)
	if authHeader == "" || expected == "" {
		return false
	}
	if secureEqual(authHeader, expected) {
		return true
	}
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		return secureEqual(token, expected)
	}
	return false
}

func (s *HTTPSignerService) ensureAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if s.username == "" && s.bearer == "" {
		return true
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if s.bearer != "" && matchesBearerToken(authHeader, s.bearer) {
		return true
	}
	if s.username != "" {
		username, password, ok := r.BasicAuth()
		if ok && secureEqual(username, s.username) && secureEqual(password, s.password) {
			return true
		}
	}
	writeJSONError(w, http.StatusUnauthorized, "unauthorized")
	return false
}

func (s *HTTPSignerService) handleFrostDKGRound1(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostDKGRound1Request) (xmrFrostDKGRound1Response, error) {
		return s.engine.FrostDKGRound1(req)
	})
}

func (s *HTTPSignerService) handleFrostDKGRound2(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostDKGRound2Request) (xmrFrostDKGRound2Response, error) {
		return s.engine.FrostDKGRound2(req)
	})
}

func (s *HTTPSignerService) handleFrostDKGRound3(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostDKGRound3Request) (xmrFrostDKGRound3Response, error) {
		return s.engine.FrostDKGRound3(req)
	})
}

func (s *HTTPSignerService) handleFrostSignPrepare(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostSignPrepareRequest) (xmrFrostSignPrepareResponse, error) {
		return s.engine.FrostSignPrepare(req)
	})
}

func (s *HTTPSignerService) handleFrostSignPartial(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostSignPartialRequest) (xmrFrostSignPartialResponse, error) {
		return s.engine.FrostSignPartial(req)
	})
}

func (s *HTTPSignerService) handleFrostSignCombine(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostSignCombineRequest) (xmrFrostSignCombineResponse, error) {
		return s.engine.FrostSignCombine(req)
	})
}

func (s *HTTPSignerService) handleFrostBroadcast(w http.ResponseWriter, r *http.Request) {
	handleSignerPost(s, w, r, func(req xmrFrostBroadcastRequest) (xmrFrostBroadcastResponse, error) {
		return s.engine.FrostBroadcast(req)
	})
}

func handleSignerPost[Req any, Resp any](
	s *HTTPSignerService,
	w http.ResponseWriter,
	r *http.Request,
	call func(Req) (Resp, error),
) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req Req
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := call(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *HTTPSignerService) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.engine == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "signer engine is not configured")
		return
	}
	if checker, ok := s.engine.(signerHealthChecker); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := checker.HealthCheck(ctx); err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, fmt.Sprintf("signer engine is unhealthy: %v", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": message,
	})
}

func decodeJSONRequest(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, xmrSignerMaxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain a single json object")
		}
		return err
	}
	return nil
}

func isAlreadyRelayedError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "already in pool") ||
		strings.Contains(lower, "already spent") ||
		strings.Contains(lower, "tx already")
}

func isLoopbackListenAddr(addr string) bool {
	host := strings.TrimSpace(addr)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSpace(splitHost)
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
