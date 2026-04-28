package monero

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func testFrostTxKeys() []string {
	return []string{strings.Repeat("1", 64)}
}

func testVaultPubKey(t *testing.T, _ string) common.PubKey {
	t.Helper()
	return ttypes.GetRandomEd25519PubKey()
}

func testOutboundXMRDestination() common.Address {
	return common.Address(common.XMRChecksummedDummyAddress('8'))
}

func newTestFrostSignerWithServer(t *testing.T, server *httptest.Server) *FrostSigner {
	t.Helper()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	return signer
}

func newTestFrostSignerHTTPServer(t *testing.T, handler http.Handler) (*FrostSigner, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return newTestFrostSignerWithServer(t, server), server
}

func newTestXMRClientWithFrostSigner(t *testing.T, signer *FrostSigner) *Client {
	t.Helper()

	return &Client{
		frostSigner:        signer,
		signerCacheManager: newTestSignerCacheManager(t),
	}
}

func newTestXMRTxOutItem(vaultPubKey common.PubKey) stypes.TxOutItem {
	return stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
	}
}
