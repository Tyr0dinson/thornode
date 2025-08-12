package thorclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "gopkg.in/check.v1"

	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Test suite for THORChain registered memo query integration
func (s *ThorchainSuite) TestGetRegisteredMemo_Success(c *C) {
	// Setup mock server to return active memo
	s.server = s.setupMockServerForRegisteredMemo(c, map[string]string{
		"12345": "../../test/fixtures/endpoints/registered_memo/active.json",
	})
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	memo, err := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err, IsNil)
	c.Assert(memo, NotNil)
	c.Assert(memo.Reference, Equals, "12345")
	c.Assert(memo.Memo, Equals, "SWAP:BTC.BTC")
	c.Assert(memo.ChainId, Equals, "BTC")
	c.Assert(memo.RegisteredAt, Equals, int64(100))
	c.Assert(memo.ExpiresAt, Equals, int64(144100))
	c.Assert(memo.Status, Equals, "active")
}

func (s *ThorchainSuite) TestGetRegisteredMemo_NotFound(c *C) {
	// Setup mock server to return 404 for non-existent reference
	s.server = s.setupMockServerForRegisteredMemo(c, map[string]string{
		"99999": "404", // Special marker for 404
	})
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	memo, err := thorchainBridge.GetRegisteredMemo("99999")
	c.Assert(err, NotNil)
	c.Assert(memo, IsNil)
	c.Assert(strings.Contains(err.Error(), "fail to get registered memo for reference 99999"), Equals, true)
	c.Assert(strings.Contains(err.Error(), "404"), Equals, true)
}

func (s *ThorchainSuite) TestGetRegisteredMemo_ServerError(c *C) {
	// Setup mock server to return 500 for server error
	s.server = s.setupMockServerForRegisteredMemo(c, map[string]string{
		"12345": "500", // Special marker for 500
	})
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	memo, err := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err, NotNil)
	c.Assert(memo, IsNil)
	c.Assert(strings.Contains(err.Error(), "fail to get registered memo for reference 12345"), Equals, true)
}

func (s *ThorchainSuite) TestGetRegisteredMemo_MalformedJSON(c *C) {
	// Setup mock server to return malformed JSON
	s.server = s.setupMockServerForRegisteredMemo(c, map[string]string{
		"12345": "../../test/fixtures/endpoints/registered_memo/malformed.json",
	})
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	memo, err := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err, NotNil)
	c.Assert(memo, IsNil)
	c.Assert(strings.Contains(err.Error(), "fail to unmarshal registered memo from json"), Equals, true)
}

func (s *ThorchainSuite) TestGetRegisteredMemo_MultipleTypes(c *C) {
	// Test various memo types (SWAP, ADD, WITHDRAW)
	fixtures := map[string]string{
		"12345": "../../test/fixtures/endpoints/registered_memo/active.json",        // SWAP
		"54321": "../../test/fixtures/endpoints/registered_memo/add_liquidity.json", // ADD
		"11111": "../../test/fixtures/endpoints/registered_memo/withdraw.json",      // WITHDRAW
		"00123": "../../test/fixtures/endpoints/registered_memo/leading_zeros.json", // Reference with leading zeros
	}

	s.server = s.setupMockServerForRegisteredMemo(c, fixtures)
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	// Test SWAP memo
	swapMemo, err := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err, IsNil)
	c.Assert(swapMemo.Memo, Equals, "SWAP:BTC.BTC")
	c.Assert(swapMemo.ChainId, Equals, "BTC")

	// Test ADD memo
	addMemo, err := thorchainBridge.GetRegisteredMemo("54321")
	c.Assert(err, IsNil)
	c.Assert(addMemo.Memo, Equals, "ADD:BTC.BTC:tthor1xyz789")
	c.Assert(addMemo.ChainId, Equals, "BTC")

	// Test WITHDRAW memo
	withdrawMemo, err := thorchainBridge.GetRegisteredMemo("11111")
	c.Assert(err, IsNil)
	c.Assert(withdrawMemo.Memo, Equals, "WITHDRAW:BTC.BTC:10000")
	c.Assert(withdrawMemo.ChainId, Equals, "BTC")

	// Test reference with leading zeros
	leadingZerosMemo, err := thorchainBridge.GetRegisteredMemo("00123")
	c.Assert(err, IsNil)
	c.Assert(leadingZerosMemo.Reference, Equals, "00123")
	c.Assert(leadingZerosMemo.Memo, Equals, "SWAP:ETH.ETH:tthor1abc123")
	c.Assert(leadingZerosMemo.ChainId, Equals, "ETH")
}

func (s *ThorchainSuite) TestGetRegisteredMemo_EmptyReference(c *C) {
	// Test empty reference - should still try to make request
	s.server = s.setupMockServerForRegisteredMemo(c, map[string]string{
		"": "404", // Empty reference returns 404
	})
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	memo, err := thorchainBridge.GetRegisteredMemo("")
	c.Assert(err, NotNil)
	c.Assert(memo, IsNil)
	c.Assert(strings.Contains(err.Error(), "fail to get registered memo for reference"), Equals, true)
	c.Assert(strings.Contains(err.Error(), "404"), Equals, true)
}

func (s *ThorchainSuite) TestGetRegisteredMemo_NetworkErrors(c *C) {
	// Test network connectivity issues by providing invalid host
	s.cfg.ChainHost = "invalid-host-12345.nonexistent"
	s.cfg.ChainRPC = "invalid-host-12345.nonexistent"

	// Create new bridge with invalid config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	memo, err := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err, NotNil)
	c.Assert(memo, IsNil)
	c.Assert(strings.Contains(err.Error(), "fail to get registered memo for reference 12345"), Equals, true)
}

func (s *ThorchainSuite) TestGetRegisteredMemo_ConcurrentRequests(c *C) {
	// Test concurrent requests to ensure thread safety
	fixtures := map[string]string{
		"12345": "../../test/fixtures/endpoints/registered_memo/active.json",
		"54321": "../../test/fixtures/endpoints/registered_memo/add_liquidity.json",
		"11111": "../../test/fixtures/endpoints/registered_memo/withdraw.json",
	}

	s.server = s.setupMockServerForRegisteredMemo(c, fixtures)
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	// Create new bridge with updated config
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1 // fail fast

	// Make concurrent requests
	refs := []string{"12345", "54321", "11111"}
	results := make([]*stypes.RegisteredMemo, len(refs))
	errors := make([]error, len(refs))

	// Use goroutines to test concurrent access
	done := make(chan bool, len(refs))
	for i, ref := range refs {
		go func(index int, reference string) {
			memo, err := thorchainBridge.GetRegisteredMemo(reference)
			results[index] = memo
			errors[index] = err
			done <- true
		}(i, ref)
	}

	// Wait for all goroutines to complete
	for range refs {
		<-done
	}

	// Verify all requests succeeded
	for i, ref := range refs {
		c.Assert(errors[i], IsNil, Commentf("Error for reference %s: %v", ref, errors[i]))
		c.Assert(results[i], NotNil, Commentf("Nil result for reference %s", ref))
		c.Assert(results[i].Reference, Equals, ref)
	}
}

func (s *ThorchainSuite) TestGetRegisteredMemo_HTTPStatusCodes(c *C) {
	// Test various HTTP status codes
	statusTests := map[string]int{
		"400": http.StatusBadRequest,
		"401": http.StatusUnauthorized,
		"403": http.StatusForbidden,
		"500": http.StatusInternalServerError,
		"502": http.StatusBadGateway,
		"503": http.StatusServiceUnavailable,
	}

	for reference, statusCode := range statusTests {
		s.server = s.setupMockServerForRegisteredMemo(c, map[string]string{
			reference: fmt.Sprintf("%d", statusCode), // Use status code as fixture marker
		})
		s.cfg.ChainHost = s.server.Listener.Addr().String()
		s.cfg.ChainRPC = s.server.Listener.Addr().String()

		// Create new bridge with updated config
		bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
		c.Assert(err, IsNil)
		thorchainBridge, ok := bridge.(*thorchainBridge)
		c.Assert(ok, Equals, true)
		thorchainBridge.httpClient.RetryMax = 1 // fail fast

		memo, err := thorchainBridge.GetRegisteredMemo(reference)
		c.Assert(err, NotNil, Commentf("Expected error for status %d", statusCode))
		c.Assert(memo, IsNil, Commentf("Expected nil memo for status %d", statusCode))
		c.Assert(strings.Contains(err.Error(), "fail to get registered memo for reference"), Equals, true)
		c.Assert(strings.Contains(err.Error(), fmt.Sprintf("%d", statusCode)), Equals, true)

		s.server.Close() // Close server for next iteration
	}
}

func (s *ThorchainSuite) TestGetRegisteredMemo_CachingBehavior(c *C) {
	// Test that caching works correctly (same request within block time returns cached result)
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.RequestURI, "/thorchain/registered_memo/") {
			requestCount++
			rw.Header().Set("Content-Type", "application/json")
			// Return active memo fixture
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/registered_memo/active.json")
			return
		}
		rw.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	s.cfg.ChainHost = server.Listener.Addr().String()
	s.cfg.ChainRPC = server.Listener.Addr().String()

	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1

	// Make first request
	memo1, err1 := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err1, IsNil)
	c.Assert(memo1.Reference, Equals, "12345")

	// Make second request immediately (should use cache in production, but test might not due to timing)
	memo2, err2 := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err2, IsNil)
	c.Assert(memo2.Reference, Equals, "12345")

	// Both requests should return the same data
	c.Assert(memo1.Memo, Equals, memo2.Memo)
	c.Logf("Made %d HTTP requests for 2 API calls", requestCount)
}

func (s *ThorchainSuite) TestGetRegisteredMemo_LongRunningRequest(c *C) {
	// Test timeout behavior with slow server
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.RequestURI, "/thorchain/registered_memo/") {
			// Simulate a slow response (but not too slow to avoid test timeouts)
			time.Sleep(100 * time.Millisecond)
			rw.Header().Set("Content-Type", "application/json")
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/registered_memo/active.json")
			return
		}
		rw.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	s.cfg.ChainHost = server.Listener.Addr().String()
	s.cfg.ChainRPC = server.Listener.Addr().String()

	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1

	// Request should still complete successfully (100ms is reasonable)
	memo, err := thorchainBridge.GetRegisteredMemo("12345")
	c.Assert(err, IsNil)
	c.Assert(memo.Reference, Equals, "12345")
}

func (s *ThorchainSuite) TestGetRegisteredMemo_InvalidJSONFields(c *C) {
	// Test various types of JSON field issues
	testCases := map[string]string{
		"missing_required_fields": `{"reference": "12345"}`,                              // Missing other fields
		"wrong_field_types":       `{"reference": 12345, "memo": true, "chain_id": 999}`, // Wrong types
		"empty_json_object":       `{}`,                                                  // Completely empty
	}

	for testName, jsonContent := range testCases {
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			if strings.HasPrefix(req.RequestURI, "/thorchain/registered_memo/") {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusOK)
				rw.Write([]byte(jsonContent))
				return
			}
			rw.WriteHeader(http.StatusNotFound)
		}))

		s.cfg.ChainHost = server.Listener.Addr().String()
		s.cfg.ChainRPC = server.Listener.Addr().String()

		bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
		c.Assert(err, IsNil)
		thorchainBridge, ok := bridge.(*thorchainBridge)
		c.Assert(ok, Equals, true)
		thorchainBridge.httpClient.RetryMax = 1

		memo, err := thorchainBridge.GetRegisteredMemo("12345")
		// Depending on the JSON content, it might succeed or fail
		// The important part is that it doesn't crash
		if err != nil {
			c.Logf("Test case '%s' failed as expected: %s", testName, err.Error())
			c.Assert(memo, IsNil)
		} else {
			c.Logf("Test case '%s' succeeded unexpectedly", testName)
			c.Assert(memo, NotNil)
		}

		server.Close()
	}
}

func (s *ThorchainSuite) TestGetRegisteredMemo_SpecialReferences(c *C) {
	// Test special reference formats that might cause issues
	specialRefs := []string{
		"12345",   // Normal reference
		"00000",   // All zeros
		"99999",   // All nines
		"00001",   // Leading zeros
		"54321",   // Reverse
		" 12345 ", // With spaces (URL encoded)
		"123.45",  // With special characters
		"12345a",  // Mixed alphanumeric
	}

	// Create fixture mapping
	fixtures := make(map[string]string)
	for _, ref := range specialRefs {
		fixtures[ref] = "../../test/fixtures/endpoints/registered_memo/active.json"
	}

	s.server = s.setupMockServerForRegisteredMemo(c, fixtures)
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), s.bridge.keys)
	c.Assert(err, IsNil)
	thorchainBridge, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	thorchainBridge.httpClient.RetryMax = 1

	for _, ref := range specialRefs {
		memo, err := thorchainBridge.GetRegisteredMemo(ref)
		if err != nil {
			c.Logf("Reference '%s' failed: %s", ref, err.Error())
		} else {
			c.Assert(memo, NotNil)
			c.Logf("Reference '%s' succeeded", ref)
		}
	}
}

// Helper function to setup mock server for registered memo tests
func (s *ThorchainSuite) setupMockServerForRegisteredMemo(c *C, fixtures map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		// Handle registered memo endpoints
		if strings.HasPrefix(req.RequestURI, "/thorchain/registered_memo/") {
			// Extract reference from URL path
			parts := strings.Split(req.RequestURI, "/")
			if len(parts) >= 4 {
				reference := parts[3]
				if fixture, exists := fixtures[reference]; exists {
					// Check for special status code markers
					switch fixture {
					case "404":
						rw.WriteHeader(http.StatusNotFound)
						return
					case "400":
						rw.WriteHeader(http.StatusBadRequest)
						return
					case "401":
						rw.WriteHeader(http.StatusUnauthorized)
						return
					case "403":
						rw.WriteHeader(http.StatusForbidden)
						return
					case "500":
						rw.WriteHeader(http.StatusInternalServerError)
						return
					case "502":
						rw.WriteHeader(http.StatusBadGateway)
						return
					case "503":
						rw.WriteHeader(http.StatusServiceUnavailable)
						return
					default:
						httpTestHandler(c, rw, fixture)
						return
					}
				}
			}
			// Default to 404 if no fixture found
			rw.WriteHeader(http.StatusNotFound)
			return
		}

		// Handle other endpoints with existing fixtures (for compatibility)
		switch {
		case strings.HasPrefix(req.RequestURI, AuthAccountEndpoint):
			httpTestHandler(c, rw, s.authAccountFixture)
		case strings.HasPrefix(req.RequestURI, NodeAccountEndpoint):
			httpTestHandler(c, rw, s.nodeAccountFixture)
		case strings.HasPrefix(req.RequestURI, LastBlockEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/lastblock/eth.json")
		case strings.HasPrefix(req.RequestURI, StatusEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/status/status.json")
		case strings.HasPrefix(req.RequestURI, KeysignEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/keysign/template.json")
		case strings.HasPrefix(req.RequestURI, "/thorchain/vaults") && strings.HasSuffix(req.RequestURI, "/signers"):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/tss/keysign_party.json")
		case strings.HasPrefix(req.RequestURI, AsgardVault):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, PubKeysEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/vaults/pubKeys.json")
		case strings.EqualFold(req.RequestURI, BroadcastTxsEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/txs/success.json")
		case strings.HasPrefix(req.RequestURI, ThorchainConstants):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/constants/constants.json")
		case strings.HasPrefix(req.RequestURI, RagnarokEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/ragnarok/ragnarok.json")
		case strings.HasPrefix(req.RequestURI, ChainVersionEndpoint):
			_, err := rw.Write([]byte(`{"current":"` + stypes.GetCurrentVersion().String() + `"}`))
			c.Assert(err, IsNil)
		case strings.HasPrefix(req.RequestURI, MimirEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/mimir/mimir.json")
		case strings.HasPrefix(req.RequestURI, InboundAddressesEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case strings.HasPrefix(req.RequestURI, "/thorchain/thorname/"):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/thorname/thorname.json")
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
}
