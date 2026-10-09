package signer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type newSignerBridgeCov struct {
	thorclient.ThorchainBridge

	mu      sync.Mutex
	na      *types2.NodeAccount
	naErr   error
	asgards types2.Vaults
	asgErr  error
	asgCall int
}

func (b *newSignerBridgeCov) GetNodeAccount(string) (*types2.NodeAccount, error) {
	return b.na, b.naErr
}

func (b *newSignerBridgeCov) GetAsgards() (types2.Vaults, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asgCall++
	return b.asgards, b.asgErr
}

func (b *newSignerBridgeCov) asgardCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asgCall
}

type recordingPubKeyValidatorCov struct {
	*pubkeymanager.MockPoolAddressValidator
	added map[common.SigningAlgo]common.PubKey
}

func (r *recordingPubKeyValidatorCov) AddNodePubKey(pk common.PubKey, algo common.SigningAlgo) {
	r.added[algo] = pk
}

type NewSignerCovSuite struct {
	base SignSuite
}

var _ = Suite(&NewSignerCovSuite{})

func (s *NewSignerCovSuite) SetUpSuite(c *C) {
	s.base.SetUpSuite(c)
}

func (s *NewSignerCovSuite) signerCfg(c *C) config.Bifrost {
	return config.Bifrost{Signer: config.BifrostSignerConfiguration{
		SignerDbPath: filepath.Join(c.MkDir(), "signer"),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            "ThorChain",
			StartBlockHeight:   1,
			EnforceBlockHeight: true,
		},
	}}
}

func (s *NewSignerCovSuite) TestNewSigner(c *C) {
	ecdsa := types2.GetRandomPubKey()
	eddsa := types2.GetRandomEd25519PubKey()
	bridge := &newSignerBridgeCov{
		ThorchainBridge: s.base.bridge,
		na:              &types2.NodeAccount{PubKeySet: common.PubKeySet{Secp256k1: ecdsa, Ed25519: eddsa}},
	}
	pkm := &recordingPubKeyValidatorCov{
		MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
		added:                    map[common.SigningAlgo]common.PubKey{},
	}
	chains := map[common.Chain]chainclients.ChainClient{common.ETHChain: &MockChainClient{}}

	sign, err := NewSigner(s.signerCfg(c), bridge, s.base.thorKeys, pkm, nil, chains, s.base.metrics, metrics.NewTssKeysignMetricMgr(), nil)
	c.Assert(err, IsNil)
	c.Assert(sign, NotNil)
	defer func() { c.Assert(sign.storage.Close(), IsNil) }()

	c.Check(sign.localPubKeyECDSA, Equals, ecdsa)
	c.Check(sign.localPubKeyEDDSA, Equals, eddsa)
	c.Check(pkm.added[common.SigningAlgoSecp256k1], Equals, ecdsa)
	c.Check(pkm.added[common.SigningAlgoEd25519], Equals, eddsa)
	// the signer's block scanner is pinned to THORChain regardless of config
	c.Check(sign.cfg.Signer.BlockScanner.ChainID, Equals, common.THORChain)
	c.Check(sign.tssKeygen, NotNil)
	c.Check(sign.constantsProvider, NotNil)
	chain, err := sign.getChain(common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(chain, NotNil)
	_, err = sign.getChain(common.BTCChain)
	c.Assert(err, ErrorMatches, "not supported")
}

func (s *NewSignerCovSuite) TestNewSignerOnlyECDSA(c *C) {
	ecdsa := types2.GetRandomPubKey()
	bridge := &newSignerBridgeCov{
		ThorchainBridge: s.base.bridge,
		na:              &types2.NodeAccount{PubKeySet: common.PubKeySet{Secp256k1: ecdsa}},
	}
	pkm := &recordingPubKeyValidatorCov{
		MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
		added:                    map[common.SigningAlgo]common.PubKey{},
	}
	sign, err := NewSigner(s.signerCfg(c), bridge, s.base.thorKeys, pkm, nil, nil, s.base.metrics, metrics.NewTssKeysignMetricMgr(), nil)
	c.Assert(err, IsNil)
	defer func() { c.Assert(sign.storage.Close(), IsNil) }()
	c.Check(sign.localPubKeyEDDSA.IsEmpty(), Equals, true)
	c.Check(pkm.added, HasLen, 1)
}

func (s *NewSignerCovSuite) TestNewSignerErrors(c *C) {
	pkm := pubkeymanager.NewMockPoolAddressValidator()
	bridge := &newSignerBridgeCov{
		ThorchainBridge: s.base.bridge,
		na:              &types2.NodeAccount{PubKeySet: common.PubKeySet{Secp256k1: types2.GetRandomPubKey()}},
	}

	// missing keysign metric manager
	_, err := NewSigner(s.signerCfg(c), bridge, s.base.thorKeys, pkm, nil, nil, s.base.metrics, nil, nil)
	c.Assert(err, ErrorMatches, ".*tss keysign metric manager is nil")

	// node account lookup failure
	bridge.naErr = errors.New("thornode down")
	_, err = NewSigner(s.signerCfg(c), bridge, s.base.thorKeys, pkm, nil, nil, s.base.metrics, metrics.NewTssKeysignMetricMgr(), nil)
	c.Assert(err, ErrorMatches, "fail to get node account from thorchain,err:thornode down")

	// signer db path that is a regular file cannot be opened
	filePath := filepath.Join(c.MkDir(), "file")
	c.Assert(os.WriteFile(filePath, []byte("x"), 0o600), IsNil)
	cfg := s.signerCfg(c)
	cfg.Signer.SignerDbPath = filePath
	_, err = NewSigner(cfg, bridge, s.base.thorKeys, pkm, nil, nil, s.base.metrics, metrics.NewTssKeysignMetricMgr(), nil)
	c.Assert(err, ErrorMatches, "fail to create thorchain scan storage.*")
}

func (s *NewSignerCovSuite) TestKeygenCheckBridgeEmptyPolicy(c *C) {
	b := keygenCheckBridge{ThorchainBridge: s.base.bridge}
	policy, err := b.GetKeysignPolicy(types2.GetRandomPubKey(), 10)
	c.Assert(err, IsNil)
	c.Check(policy, DeepEquals, thorclient.KeysignPolicy{})
}

func (s *NewSignerCovSuite) TestWarnOnChainsMissingFromKeygen(c *C) {
	var buf bytes.Buffer
	bridge := &newSignerBridgeCov{
		ThorchainBridge: s.base.bridge,
		asgards: types2.Vaults{
			{Chains: []string{common.ETHChain.String(), common.BTCChain.String(), common.XMRChain.String()}},
			{Chains: []string{common.BTCChain.String(), common.DOGEChain.String()}},
		},
	}
	sign := &Signer{thorchainBridge: bridge, logger: zerolog.New(&buf)}

	// XMR monovault keygen uses a fixed chain set and never queries asgards
	sign.warnOnChainsMissingFromKeygen(common.Chains{common.ETHChain}, types2.KeygenType_XMRMonovaultKeygen)
	c.Check(bridge.asgardCalls(), Equals, 0)
	c.Check(buf.Len(), Equals, 0)

	sign.warnOnChainsMissingFromKeygen(common.Chains{common.ETHChain}, types2.KeygenType_AsgardKeygen)
	c.Check(bridge.asgardCalls(), Equals, 1)
	out := buf.String()
	// BTC is reported once (deduplicated), DOGE once, ETH and XMR never
	c.Check(bytes.Count(buf.Bytes(), []byte(`"chain":"BTC"`)), Equals, 1)
	c.Check(bytes.Count(buf.Bytes(), []byte(`"chain":"DOGE"`)), Equals, 1)
	c.Check(bytes.Contains(buf.Bytes(), []byte(`"chain":"ETH"`)), Equals, false, Commentf("%s", out))
	c.Check(bytes.Contains(buf.Bytes(), []byte(`"chain":"XMR"`)), Equals, false, Commentf("%s", out))

	// asgard lookup failures are swallowed (debug only)
	buf.Reset()
	bridge.asgErr = errors.New("boom")
	sign.warnOnChainsMissingFromKeygen(common.Chains{}, types2.KeygenType_AsgardKeygen)
	c.Check(bridge.asgardCalls(), Equals, 2)
	c.Check(bytes.Contains(buf.Bytes(), []byte("disabled in this bifrost")), Equals, false)
}

func (s *NewSignerCovSuite) TestSecp256k1VerificationSignatureTimesOut(c *C) {
	sign := &Signer{logger: zerolog.Nop()}
	release := make(chan struct{})
	defer close(release)
	sig := sign.secp256k1VerificationSignatureWithTimeout(types2.GetRandomPubKey(), 1, 10*time.Millisecond,
		func(common.PubKey, int64) []byte {
			<-release
			return []byte("late")
		})
	c.Check(sig, IsNil)
}

const (
	seed12Cov = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	seed24Cov = "profit used piece repeat real curtain endorse tennis tenant sentence include glass return learn upgrade apple crane polar attend before ripple doctor decrease depend"
)

func (s *NewSignerCovSuite) TestBuildKeygenBackupsShortSeed(c *C) {
	restore := setSignerSeedPhraseFile(c, c.MkDir(), seed12Cov)
	defer restore()

	var buf bytes.Buffer
	sign := &Signer{logger: zerolog.New(&buf)}
	sign.cfg.Signer.BackupKeyshares = true
	backups := sign.buildKeygenBackups("/does/not/matter", "", "00")
	c.Check(backups, DeepEquals, keygenBackups{})
	c.Check(bytes.Contains(buf.Bytes(), []byte("signer seed is 12 words")), Equals, true, Commentf("%s", buf.String()))
}

func (s *NewSignerCovSuite) TestBuildKeygenBackupsEncryptFailures(c *C) {
	dir := c.MkDir()
	restore := setSignerSeedPhraseFile(c, dir, seed24Cov)
	defer restore()

	origEnc, origRaw := encryptKeysharesFn, encryptRawKeysharesFn
	defer func() { encryptKeysharesFn, encryptRawKeysharesFn = origEnc, origRaw }()
	var mu sync.Mutex
	var paths []string
	encryptKeysharesFn = func(path, seed string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, path)
		return nil, errors.New("encrypt failed")
	}
	encryptRawKeysharesFn = func(raw []byte, seed string) ([]byte, error) {
		return nil, errors.New("raw encrypt failed")
	}

	eddsaPath := filepath.Join(dir, "eddsa.json")
	c.Assert(os.WriteFile(eddsaPath, []byte("{}"), 0o600), IsNil)

	var buf bytes.Buffer
	sign := &Signer{logger: zerolog.New(&buf)}
	sign.cfg.Signer.BackupKeyshares = true
	backups := sign.buildKeygenBackups(filepath.Join(dir, "secp.json"), eddsaPath, "abcd")
	c.Check(backups, DeepEquals, keygenBackups{})
	c.Check(paths, HasLen, 2)
	for _, msg := range []string{"fail to encrypt secp256k1 keyshares", "fail to encrypt eddsa keyshares", "fail to encrypt frost keyshare"} {
		c.Check(bytes.Contains(buf.Bytes(), []byte(msg)), Equals, true, Commentf("missing %q", msg))
	}

	// invalid frost hex is skipped and an eddsa path that cannot be stat'd is
	// logged without an encryption attempt
	buf.Reset()
	paths = nil
	backups = sign.buildKeygenBackups("", filepath.Join(eddsaPath, "child"), "not-hex")
	c.Check(backups, DeepEquals, keygenBackups{})
	c.Check(paths, HasLen, 0)
	c.Check(bytes.Contains(buf.Bytes(), []byte("fail to decode frost keyshare hex")), Equals, true)
	c.Check(bytes.Contains(buf.Bytes(), []byte("fail to stat eddsa keyshares")), Equals, true)
}

type keygenStdTxErrBridgeCov struct {
	batchingKeygenBridge
}

func (b *keygenStdTxErrBridgeCov) GetKeygenStdTx(common.PubKey, []byte, []byte, []types2.Blame, common.PubKeys, types2.KeygenType, common.Chains, int64, int64, common.PubKey, []byte, ...any) (sdktypes.Msg, error) {
	return nil, errors.New("cannot build")
}

type addPubKeyRecorderCov struct {
	*pubkeymanager.MockPoolAddressValidator
	added []string
}

func (r *addPubKeyRecorderCov) AddPubKey(pk common.PubKey, signer bool, algo common.SigningAlgo) {
	r.added = append(r.added, fmt.Sprintf("%s/%t/%s", pk, signer, algo))
}

func (s *NewSignerCovSuite) TestFlushPendingKeygenBroadcastsBuildFailure(c *C) {
	bridge := &keygenStdTxErrBridgeCov{}
	pkm := &addPubKeyRecorderCov{MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator()}
	sign := &Signer{
		thorchainBridge: bridge,
		pubkeyMgr:       pkm,
		logger:          zerolog.Nop(),
		errCounter:      s.base.metrics.GetCounterVec(metrics.SignerError),
	}

	// nothing pending is a no-op
	sign.flushPendingKeygenBroadcasts(nil)
	c.Check(bridge.batchSizes, HasLen, 0)

	sign.flushPendingKeygenBroadcasts([]pendingKeygenBroadcast{{
		height: 10,
		poolPk: types2.GetRandomPubKey(),
		input:  common.PubKeys{types2.GetRandomPubKey()},
	}})
	c.Check(bridge.batchSizes, HasLen, 0)
	c.Check(pkm.added, HasLen, 0)

	// an empty broadcast never reaches the bridge
	txID, err := sign.broadcastKeygenMsgs(10)
	c.Assert(err, IsNil)
	c.Check(txID, Equals, common.BlankTxID)
	c.Check(bridge.batchSizes, HasLen, 0)
}

func (s *NewSignerCovSuite) TestScheduleKeygenRetryWhenStopped(c *C) {
	sign := &Signer{stopChan: make(chan struct{}), logger: zerolog.Nop()}
	close(sign.stopChan)
	c.Check(sign.scheduleKeygenRetry(types2.KeygenBlock{Height: 1}, types2.Keygen{}), Equals, false)
	count := 0
	sign.keygenRetries.Range(func(any, any) bool { count++; return true })
	c.Check(count, Equals, 0)
}
