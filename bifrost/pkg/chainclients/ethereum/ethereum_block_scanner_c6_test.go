package ethereum

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// signPubKeysMock returns a single sign pubkey under test (our vault), while
// reporting it as not a current pool address, so only the owned-tx gate can
// recognise a block the vault sent.
type signPubKeysMock struct {
	*pubkeymanager.MockPoolAddressValidator
	pk thorcommon.PubKey
}

func (m signPubKeysMock) GetSignPubKeys() thorcommon.PubKeys { return thorcommon.PubKeys{m.pk} }

// c6RealBlockJSON is a real block's header (so block.Hash() recomputes to its
// stated hash, which is the key the ethclient caches the server-reported sender
// under) with the sole transaction's `from` parameterised. Only the tx sender is
// changed; the header — and thus the block hash — is untouched.
func c6RealBlockJSON(fromHexNoPrefix string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"difficulty":"0x2","extraData":"0xd88301091a846765746888676f312e31352e36856c696e757800000000000000e86d9af8b427b780cd1e6f7cabd2f9231ccac25d313ed475351ed64ac19f21491461ed1fae732d3bbf73a5866112aec23b0ca436185685b9baee4f477a950f9400","baseFeePerGas":"0x1","gasLimit":"0x9e0f54","gasUsed":"0xabd3","hash":"0x60b24090796e2b129156e1cab41ef92ea49e56a516ff3a1cbd7252af700f2d14","logsBloom":"0x00010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000040000000000000000010000200020000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000040000020000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001000000000000010000000000000000000000000000000000000000000000020000000000000","miner":"0x0000000000000000000000000000000000000000","mixHash":"0x0000000000000000000000000000000000000000000000000000000000000000","nonce":"0x0000000000000000","number":"0x6b","parentHash":"0xf18470c54efec284fb5ad57c0ee4afe2774d61393bd5224ac5484b39a0a07556","receiptsRoot":"0x794a74d56ec50769a1400f7ae0887061b0ec3ea6702589a0b45b9102df2c9954","sha3Uncles":"0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347","size":"0x30a","stateRoot":"0x1c84090d7f5dc8137d6762e3d4babe10b30bf61fa827618346ae1ba8600a9629","timestamp":"0x6008f03a","totalDifficulty":"0xd7","transactions":[{"blockHash":"0x60b24090796e2b129156e1cab41ef92ea49e56a516ff3a1cbd7252af700f2d14","blockNumber":"0x6b","from":"0x%s","gas":"0x23273","gasPrice":"0x1","hash":"0x501d0b7fc8fcdff367280dc8b0c077f6beb9e324ad9550e2c0e34a2fa8e99aed","input":"0x095ea7b3000000000000000000000000e65e9d372f8cacc7b6dfcd4af6507851ed31bb4400000000000000000000000000000000000000000000000000000000ee6b2800","nonce":"0x1","to":"0x40bcd4db8889a8bf0b1391d0c819dcd9627f9d0a","transactionIndex":"0x0","value":"0x0","v":"0xa95","r":"0x614fa842510a4293d25ce4799a01a3d3cfeada4b79d7157c755bb4872984fff","s":"0x351e831427ca7e2f1b5f45b5101cc1d170d6fd8e7129378c8d55a6a436f403dc"}],"transactionsRoot":"0x4247bb112edbe20ee8cf406864b335f4a3aa215f65ea686c9820f056c637aca6","uncles":[]}}`, fromHexNoPrefix)
}

// c6BlockJSON builds an eth_getBlockByNumber result for a single-tx block whose
// sole transaction reports `from` as the given 0x-less hex sender. The ethclient
// trusts the server-reported sender, so the signature need not recover to it.
// bloom is the block's logsBloom (use an all-zero bloom to force a receipt fetch
// in extractTxs, or the non-matching one below to isolate the owned-tx gate).
func c6BlockJSON(number, fromHexNoPrefix, bloom string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"difficulty":"0x2","extraData":"0x","baseFeePerGas":"0x1","gasLimit":"0x9e0f54","gasUsed":"0xabd3","hash":"0x60b24090796e2b129156e1cab41ef92ea49e56a516ff3a1cbd7252af700f2d14","logsBloom":"%s","miner":"0x0000000000000000000000000000000000000000","mixHash":"0x0000000000000000000000000000000000000000000000000000000000000000","nonce":"0x0000000000000000","number":"%s","parentHash":"0xf18470c54efec284fb5ad57c0ee4afe2774d61393bd5224ac5484b39a0a07556","receiptsRoot":"0x794a74d56ec50769a1400f7ae0887061b0ec3ea6702589a0b45b9102df2c9954","sha3Uncles":"0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347","size":"0x30a","stateRoot":"0x1c84090d7f5dc8137d6762e3d4babe10b30bf61fa827618346ae1ba8600a9629","timestamp":"0x6008f03a","totalDifficulty":"0xd7","transactions":[{"blockHash":"0x60b24090796e2b129156e1cab41ef92ea49e56a516ff3a1cbd7252af700f2d14","blockNumber":"%s","from":"0x%s","gas":"0x23273","gasPrice":"0x1","hash":"0x501d0b7fc8fcdff367280dc8b0c077f6beb9e324ad9550e2c0e34a2fa8e99aed","input":"0x","nonce":"0x1","to":"0x40bcd4db8889a8bf0b1391d0c819dcd9627f9d0a","transactionIndex":"0x0","value":"0x0","v":"0xa95","r":"0x614fa842510a4293d25ce4799a01a3d3cfeada4b79d7157c755bb4872984fff","s":"0x351e831427ca7e2f1b5f45b5101cc1d170d6fd8e7129378c8d55a6a436f403dc"}],"transactionsRoot":"0x4247bb112edbe20ee8cf406864b335f4a3aa215f65ea686c9820f056c637aca6","uncles":[]}}`, bloom, number, number, fromHexNoPrefix)
}

const (
	c6ZeroBloom = "0x" + "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
)

// TestOwnsAnyVaultSentTxForcesFetch covers the opt3 owned-block widening: a block
// carrying a transaction sent by one of this node's vaults (read from the
// RPC-reported cached sender) is recognised as owned even when the vault is not a
// current pool address, so the bloom skip is bypassed. A block sent by a stranger
// is not.
func (s *BlockScannerTestSuite) TestOwnsAnyVaultSentTxForcesFetch(c *C) {
	// A valid pubkey for the test network stands in for our vault's sign key.
	priKey, err := s.keys.GetPrivateKey()
	c.Assert(err, IsNil)
	tm, err := cryptocodec.ToCmtPubKeyInterface(priKey.PubKey())
	c.Assert(err, IsNil)
	vaultPK, err := thorcommon.NewPubKeyFromCrypto(tm)
	c.Assert(err, IsNil)
	vaultAddr, err := vaultPK.GetAddress(thorcommon.ETHChain)
	c.Assert(err, IsNil)
	vaultHex := strings.ToLower(vaultAddr.String()[2:])

	var closers []func()
	defer func() {
		for _, cl := range closers {
			cl()
		}
	}()

	// fetchBlock serves the real block with the given tx sender and returns the
	// decoded block (so its server-reported sender is cached in the ethclient).
	fetchBlock := func(fromHex string) (*ETHScanner, *etypes.Block) {
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			body, _ := io.ReadAll(req.Body)
			var r struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &r)
			switch r.Method {
			case "eth_chainId":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x539"}`))
			case "eth_getBlockByNumber":
				_, _ = rw.Write([]byte(c6RealBlockJSON(fromHex)))
			}
		}))
		closers = append(closers, server.Close)
		ethClient, err := ethclient.Dial(server.URL)
		c.Assert(err, IsNil)
		closers = append(closers, ethClient.Close)
		bs := &ETHScanner{
			cfg:       getConfigForTest(),
			client:    ethClient,
			pubkeyMgr: signPubKeysMock{MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(), pk: vaultPK},
			eipSigner: etypes.NewEIP155Signer(big.NewInt(int64(Mainnet))),
		}
		block, err := ethClient.BlockByNumber(context.Background(), big.NewInt(0x6b))
		c.Assert(err, IsNil)
		return bs, block
	}

	// Block sent by our vault: recognised as owned, bloom skip bypassed.
	bsOwned, owned := fetchBlock(vaultHex)
	c.Check(bsOwned.ownsAnyVaultSentTx(owned), Equals, true)
	c.Check(bsOwned.receiptsMayProduceObservation(owned), Equals, true)

	// Same block sent by a stranger: not owned.
	bsStranger, stranger := fetchBlock("fabb9cc6ec839b1214bb11c53377a56a6ed81762")
	c.Check(bsStranger.ownsAnyVaultSentTx(stranger), Equals, false)
}

// TestTransportReceiptErrorRescans covers the opt3 rescan: a transport/RPC receipt
// error must not silently drop a possible observation; extractTxs returns an error
// so the block is rescanned, rather than advancing past it.
func (s *BlockScannerTestSuite) TestTransportReceiptErrorRescans(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case req.RequestURI == thorclient.ChainVersionEndpoint:
			_, _ = rw.Write([]byte(`{"current":"` + types2.GetCurrentVersion().String() + `"}`))
		case req.RequestURI == thorclient.PubKeysEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		case req.RequestURI == thorclient.InboundAddressesEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case req.RequestURI == thorclient.AsgardVault:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, thorclient.NodeAccountEndpoint):
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/nodeaccount/template.json")
		default:
			body, _ := io.ReadAll(req.Body)
			var r struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &r)
			switch r.Method {
			case "eth_chainId":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x539"}`))
			case "eth_gasPrice":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			case "eth_getBlockByNumber":
				// empty bloom -> receiptsMayProduceObservation forces a fetch.
				_, _ = rw.Write([]byte(c6BlockJSON("0x6b", "fabb9cc6ec839b1214bb11c53377a56a6ed81762", c6ZeroBloom)))
			case "eth_getBlockReceipts", "eth_getTransactionReceipt":
				// transport/RPC failure (not a NotFound null result).
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`))
			}
		}
	}))
	defer server.Close()
	ethClient, err := ethclient.Dial(server.URL)
	c.Assert(err, IsNil)
	defer ethClient.Close()
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer func() { c.Check(storage.Close(), IsNil) }()
	u, err := url.Parse(server.URL)
	c.Assert(err, IsNil)
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       u.Host,
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}, s.m, s.keys)
	c.Assert(err, IsNil)
	pubKeyMgr, err := pubkeymanager.NewPubKeyManager(bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubKeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubKeyMgr.Stop(), IsNil) }()

	bs, err := NewETHScanner(getConfigForTest(), storage, big.NewInt(1337), ethClient, bridge, s.m, pubKeyMgr, func(height int64) error { return nil }, nil)
	c.Assert(err, IsNil)
	bs.globalNetworkFeeQueue = make(chan thorcommon.NetworkFee, 1)

	_, err = bs.FetchTxs(int64(1), int64(1))
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "rescanning"), Equals, true)
}
