//go:build mocknet && tss_fullsize

package tss

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/libp2p/go-libp2p/core/peerstore"
	btsskeygen "gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
)

// Run explicitly: go test -tags 'mocknet tss_fullsize' ./bifrost/tss/go-tss/tss -run '^TestFullSizeKeygen$' -timeout 10m
// This executes real EdDSA keygen over TCP with the default libp2p resource manager.
func TestFullSizeKeygen(t *testing.T) {
	conversion.SetupBech32Prefix()
	const count = 40 // Production AsgardSize; mocknet overrides it to six.
	// NewTss requires ECDSA preparameters even for EdDSA. They are unused here.
	fixture, err := os.ReadFile("../test_data/preParam_test.data")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.Split(string(fixture), "\n")[0])
	if err != nil {
		t.Fatal(err)
	}
	var preParams btsskeygen.LocalPreParams
	if err := json.Unmarshal(raw, &preParams); err != nil {
		t.Fatal(err)
	}
	servers := make([]*TssServer, count)
	keys := make([]string, count)
	for i := range servers {
		key := secp256k1.GenPrivKey()
		comm, state, err := p2p.StartP2P(&p2p.Config{Port: 0, RendezvousString: "fullsize-test"}, key, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		server, err := NewTss(comm, state, key, key, tsscommon.TssConfig{KeyGenTimeout: 5 * time.Minute, PartyTimeout: time.Minute}, &preParams)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(server.Stop)
		servers[i] = server
		keys[i] = server.localNodePubKey
	}
	// Supply the full peer address book so the test measures ceremony transport,
	// independently of DHT discovery convergence.
	for _, server := range servers {
		for _, other := range servers {
			host := other.p2pCommunication.GetHost()
			server.p2pCommunication.GetHost().Peerstore().AddAddrs(host.ID(), host.Addrs(), peerstore.PermanentAddrTTL)
		}
	}
	responses := make([]keygen.Response, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses[i], errs[i] = server.Keygen(keygen.NewRequest(append([]string(nil), keys...), 10, newJoinPartyVersion, tcommon.SigningAlgoEd25519))
		}()
	}
	wg.Wait()
	for i, response := range responses {
		if errs[i] != nil {
			t.Errorf("party %d: %v", i, errs[i])
		}
		if response.PubKey == "" || response.PubKey != responses[0].PubKey {
			t.Errorf("party %d did not agree on the generated key: %+v", i, response)
		}
	}
}
