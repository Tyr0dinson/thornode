package tss

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/client"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// fakeCometCov implements only the CometRPC calls used by key share recovery.
type fakeCometCov struct {
	client.CometRPC

	mu          sync.Mutex
	latest      int64
	earliest    int64
	statusErr   error
	statusCalls int
	// earliestAfterFirst replaces earliest once Status has been called once,
	// simulating pruning that advances during a scan.
	earliestAfterFirst int64
	txs                map[int64][]cmttypes.Tx
	blockErr           map[int64]error
	blockCalls         []int64
}

func (f *fakeCometCov) Status(_ context.Context) (*coretypes.ResultStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	earliest := f.earliest
	if f.statusCalls > 1 && f.earliestAfterFirst > 0 {
		earliest = f.earliestAfterFirst
	}
	return &coretypes.ResultStatus{SyncInfo: coretypes.SyncInfo{
		LatestBlockHeight:   f.latest,
		EarliestBlockHeight: earliest,
	}}, nil
}

func (f *fakeCometCov) Block(_ context.Context, height *int64) (*coretypes.ResultBlock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockCalls = append(f.blockCalls, *height)
	if err := f.blockErr[*height]; err != nil {
		return nil, err
	}
	return &coretypes.ResultBlock{Block: &cmttypes.Block{Data: cmttypes.Data{Txs: f.txs[*height]}}}, nil
}

type fakeRecoveryBridgeCov struct {
	thorclient.ThorchainBridge

	comet     *fakeCometCov
	na        *types.NodeAccount
	naErr     error
	vaults    types.Vaults
	vaultsErr error
	byPubKey  map[string]types.Vault
}

func (b *fakeRecoveryBridgeCov) GetContext() client.Context {
	return client.Context{}.WithClient(b.comet).WithFromAddress(b.na.NodeAddress)
}

func (b *fakeRecoveryBridgeCov) GetNodeAccount(string) (*types.NodeAccount, error) {
	if b.naErr != nil {
		return nil, b.naErr
	}
	return b.na, nil
}

func (b *fakeRecoveryBridgeCov) GetAsgards() (types.Vaults, error) {
	return b.vaults, b.vaultsErr
}

func (b *fakeRecoveryBridgeCov) GetVault(pk string) (types.Vault, error) {
	if v, ok := b.byPubKey[pk]; ok {
		return v, nil
	}
	return types.Vault{}, errors.New("vault not found")
}

type RecoverKeysharesCovSuite struct{}

var _ = Suite(&RecoverKeysharesCovSuite{})

func tssPoolTxCov(c *C, msg *types.MsgTssPool) cmttypes.Tx {
	encCfg := app.MakeEncodingConfig()
	builder := encCfg.TxConfig.NewTxBuilder()
	c.Assert(builder.SetMsgs(msg), IsNil)
	bz, err := encCfg.TxConfig.TxEncoder()(builder.GetTx())
	c.Assert(err, IsNil)
	return bz
}

func newRecoveryFixtureCov(c *C, membership ...common.PubKey) (*fakeRecoveryBridgeCov, cosmos.AccAddress) {
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	na := &types.NodeAccount{NodeAddress: signer}
	for _, pk := range membership {
		na.SignerMembership = append(na.SignerMembership, pk.String())
	}
	return &fakeRecoveryBridgeCov{
		comet: &fakeCometCov{
			latest:   20,
			earliest: 1,
			txs:      map[int64][]cmttypes.Tx{},
			blockErr: map[int64]error{},
		},
		na:       na,
		byPubKey: map[string]types.Vault{},
	}, signer
}

func recoveryConfCov(network string) config.Bifrost {
	conf := config.Bifrost{}
	conf.TSS.MaxKeyshareRecoverScanBlocks = 5
	conf.Chains.XMR.SignerExpectedNetwork = network
	return conf
}

func (s *RecoverKeysharesCovSuite) TestRecoverKeySharesBridgeErrors(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	bridge, _ := newRecoveryFixtureCov(c, types.GetRandomPubKey())
	bridge.naErr = errors.New("no node")
	err := RecoverKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, ErrorMatches, "fail to get node account: no node")

	bridge.naErr = nil
	bridge.vaultsErr = errors.New("no asgards")
	err = RecoverKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, ErrorMatches, "fail to get asgards: no asgards")

	// no membership at all is a no-op, even with a broken asgard lookup
	empty, _ := newRecoveryFixtureCov(c)
	empty.vaultsErr = errors.New("must not be called")
	c.Assert(RecoverKeyShares(recoveryConfCov("mocknet"), empty), IsNil)
}

func (s *RecoverKeysharesCovSuite) TestRecoverLegacyKeySharesFromChain(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	bridge, signer := newRecoveryFixtureCov(c, legacyPubKey)
	// the vault is only discoverable through the per-vault fallback lookup
	vault := types.Vault{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 10, Status: types.VaultStatus_ActiveVault}
	bridge.byPubKey[legacyPubKey.String()] = vault

	backup := encryptedLocalStateTestFile(c)
	// an unrelated TssPool from another signer must be ignored
	other, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	bridge.comet.txs[12] = []cmttypes.Tx{tssPoolTxCov(c, &types.MsgTssPool{
		Signer: other, PoolPubKey: legacyPubKey, KeysharesBackup: []byte("nope"),
	})}
	bridge.comet.txs[11] = []cmttypes.Tx{tssPoolTxCov(c, &types.MsgTssPool{
		Signer:               signer,
		PoolPubKey:           legacyPubKey,
		PoolPubKeyEddsa:      eddsaPubKey,
		KeysharesBackup:      backup,
		KeysharesBackupEddsa: backup,
	})}

	c.Assert(RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge), IsNil)
	original := readLocalStateTestFile(c)
	assertRecoveredLocalState(c, localstateKeysharePath(legacyPubKey), original)
	assertRecoveredLocalState(c, localstateKeysharePath(eddsaPubKey), original)
	// scan starts at min(current, vault+max) = 15 and walks down to the match
	c.Check(bridge.comet.blockCalls, DeepEquals, []int64{15, 14, 13, 12, 11})

	// second run: both shares exist, so no block is fetched
	bridge.comet.blockCalls = nil
	c.Assert(RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge), IsNil)
	c.Check(bridge.comet.blockCalls, HasLen, 0)

	// the legacy scope never touches frost targets
	c.Assert(RecoverFrostKeyShares(recoveryConfCov("mocknet"), bridge), IsNil)
}

func (s *RecoverKeysharesCovSuite) TestRecoverLegacyKeySharesErrors(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	unknown := types.GetRandomPubKey()
	bridge, _ := newRecoveryFixtureCov(c, legacyPubKey, unknown)
	bridge.vaults = types.Vaults{{PubKey: legacyPubKey, BlockHeight: 10, Status: types.VaultStatus_ActiveVault}}

	// undecodable tx in the scanned window and an unknown membership
	bridge.comet.txs[15] = []cmttypes.Tx{[]byte("garbage")}
	err := RecoverKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "(?s).*fail to get creation height of "+unknown.String()+".*")
	c.Check(err, ErrorMatches, "(?s).*fail to decode tx.*")

	// block fetch failure without a floor change stays retryable
	bridge.comet.txs = map[int64][]cmttypes.Tx{}
	bridge.comet.blockErr[14] = errors.New("rpc down")
	err = RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "(?s).*fail to get block: rpc down.*")
	c.Check(IsUnrecoverableKeyshareError(err), Equals, false)

	// the floor advanced past the failing height: the miss becomes unrecoverable
	bridge.comet.statusCalls = 0
	bridge.comet.earliestAfterFirst = 15
	err = RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, NotNil)
	c.Check(IsUnrecoverableKeyshareError(err), Equals, true)
	c.Check(err, ErrorMatches, "(?s).*clamped to the node's earliest available block 15.*")
}

func (s *RecoverKeysharesCovSuite) TestRecoverLegacyKeySharesPrunedAndMissing(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	bridge, _ := newRecoveryFixtureCov(c, legacyPubKey)
	bridge.vaults = types.Vaults{{PubKey: legacyPubKey, BlockHeight: 10, Status: types.VaultStatus_ActiveVault}}

	// whole window below the floor: unrecoverable, and an active vault surfaces it
	bridge.comet.earliest = 100
	bridge.comet.latest = 200
	err := RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, NotNil)
	c.Check(IsUnrecoverableKeyshareError(err), Equals, true)
	c.Check(bridge.comet.blockCalls, HasLen, 0)

	// an inactive vault with unrecoverable shares is skipped so bifrost can start
	bridge.vaults[0].Status = types.VaultStatus_InactiveVault
	c.Assert(RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge), IsNil)

	// status failure: scan from vault height with no floor, and a plain miss is
	// retryable even for an inactive vault
	bridge.comet.statusErr = errors.New("status down")
	err = RecoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errRecoveryBackupNotFound), Equals, true)
	c.Check(bridge.comet.blockCalls, DeepEquals, []int64{10, 9, 8, 7, 6})

	// eddsa key with no matching vault height cannot be recovered
	bridge.comet.statusErr = nil
	bridge.vaults[0].PubKeyEddsa = types.GetRandomEd25519PubKey()
	target := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: legacyPubKey, poolPubKeyEddsa: types.GetRandomEd25519PubKey()}
	err = recoverLegacyKeyShares(recoveryConfCov("mocknet"), bridge, bridge.na.NodeAddress, target, bridge.vaults)
	c.Assert(err, ErrorMatches, "fail to get creation height of .*")
}

func (s *RecoverKeysharesCovSuite) TestRecoverFrostKeySharesFromChain(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	frostPubKey := types.GetRandomEd25519PubKey()
	bridge, signer := newRecoveryFixtureCov(c, frostPubKey)
	bridge.vaults = types.Vaults{{PubKey: types.GetRandomPubKey(), PubKeyFrost: frostPubKey, BlockHeight: 10, Status: types.VaultStatus_ActiveVault}}

	raw := []byte("frost-key-material")
	backup, err := EncryptRawKeyshares(raw, Mnemonic)
	c.Assert(err, IsNil)
	// a legacy (non-XMR) keygen for the same pubkey must not match a frost target
	bridge.comet.txs[14] = []cmttypes.Tx{tssPoolTxCov(c, &types.MsgTssPool{
		Signer: signer, PoolPubKeyFrost: frostPubKey, KeysharesBackupFrost: []byte("nope"),
	})}
	bridge.comet.txs[13] = []cmttypes.Tx{tssPoolTxCov(c, &types.MsgTssPool{
		Signer:               signer,
		KeygenType:           types.KeygenType_XMRMonovaultKeygen,
		PoolPubKeyFrost:      frostPubKey,
		KeysharesBackupFrost: backup,
	})}

	conf := recoveryConfCov("mocknet")
	c.Assert(RecoverFrostKeyShares(conf, bridge), IsNil)
	path := frostKeyshareRecoveryPath(frostPubKey)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", frostPubKey), IsNil)

	// a valid artifact short-circuits without scanning
	bridge.comet.blockCalls = nil
	c.Assert(RecoverKeyShares(conf, bridge), IsNil)
	c.Check(bridge.comet.blockCalls, HasLen, 0)

	// a tampered artifact is quarantined and recovered again
	c.Assert(os.Remove(path), IsNil)
	c.Assert(os.WriteFile(path, []byte("tampered"), 0o600), IsNil)
	c.Assert(RecoverFrostKeyShares(conf, bridge), IsNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", frostPubKey), IsNil)
	matches, err := filepath.Glob(path + ".invalid-*")
	c.Assert(err, IsNil)
	c.Assert(matches, HasLen, 1)
	quarantined, err := os.ReadFile(matches[0])
	c.Assert(err, IsNil)
	c.Check(string(quarantined), Equals, "tampered")
	recovered, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Check(storage.IsEncryptedAtRest(recovered), Equals, true)
}

func (s *RecoverKeysharesCovSuite) TestRecoverFrostKeySharesErrors(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	frostPubKey := types.GetRandomEd25519PubKey()
	bridge, signer := newRecoveryFixtureCov(c, frostPubKey)
	bridge.vaults = types.Vaults{{PubKeyFrost: frostPubKey, BlockHeight: 10}}

	// misconfigured network
	err := RecoverFrostKeyShares(recoveryConfCov("testnet"), bridge)
	c.Assert(err, ErrorMatches, "(?s).*xmr signer expected network must be one of.*")

	// missing backup is tolerated (local signer state may already exist)
	c.Assert(RecoverFrostKeyShares(recoveryConfCov("mocknet"), bridge), IsNil)
	_, statErr := os.Stat(frostKeyshareRecoveryPath(frostPubKey))
	c.Check(os.IsNotExist(statErr), Equals, true)

	// transient block failures propagate
	bridge.comet.blockErr[15] = errors.New("rpc down")
	err = RecoverFrostKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, ErrorMatches, "(?s).*fail to get block: rpc down.*")
	delete(bridge.comet.blockErr, 15)

	// undecryptable backup
	bridge.comet.txs[12] = []cmttypes.Tx{tssPoolTxCov(c, &types.MsgTssPool{
		Signer:               signer,
		KeygenType:           types.KeygenType_XMRMonovaultKeygen,
		PoolPubKeyFrost:      frostPubKey,
		KeysharesBackupFrost: []byte("not-encrypted"),
	})}
	err = RecoverFrostKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, ErrorMatches, "(?s).*failed to recover frost key shares.*")

	// recovery path is a directory: stat refuses it
	c.Assert(os.Mkdir(frostKeyshareRecoveryPath(frostPubKey), 0o700), IsNil)
	err = RecoverFrostKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, ErrorMatches, "(?s).*failed to stat frost key shares.*")

	// missing seed phrase
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE_FILE"), IsNil)
	err = RecoverFrostKeyShares(recoveryConfCov("mocknet"), bridge)
	c.Assert(err, ErrorMatches, "(?s).*failed to get signer seed phrase.*")
}

func (s *RecoverKeysharesCovSuite) TestRecoveryFileHelpersErrors(c *C) {
	dir := c.MkDir()

	// quarantine of a missing / non-regular file
	c.Assert(quarantineRecoveryFile(filepath.Join(dir, "missing")), NotNil)
	sub := filepath.Join(dir, "sub")
	c.Assert(os.Mkdir(sub, 0o700), IsNil)
	c.Assert(quarantineRecoveryFile(sub), ErrorMatches, "recovery artifact is not a regular file")

	// validate: missing, directory, oversized and empty
	vault := types.GetRandomEd25519PubKey()
	c.Assert(validateFrostRecoveryFile(filepath.Join(dir, "missing"), Mnemonic, "mocknet", vault), NotNil)
	c.Assert(validateFrostRecoveryFile(sub, Mnemonic, "mocknet", vault), ErrorMatches, "recovery artifact is not a regular file")
	empty := filepath.Join(dir, "empty")
	c.Assert(os.WriteFile(empty, nil, 0o600), IsNil)
	c.Assert(validateFrostRecoveryFile(empty, Mnemonic, "mocknet", vault), ErrorMatches, "recovery artifact size 0 is outside.*")
	garbage := filepath.Join(dir, "garbage")
	c.Assert(os.WriteFile(garbage, []byte("garbage"), 0o600), IsNil)
	c.Assert(validateFrostRecoveryFile(garbage, Mnemonic, "mocknet", vault), ErrorMatches, "recovery artifact authentication failed.*")

	// recoveryFileExists rejects non-regular files
	_, err := recoveryFileExists(sub)
	c.Assert(err, ErrorMatches, "recovery path is not a regular file.*")

	// writeRecoveredBytes: size bounds, bad directory, non-regular destination
	c.Assert(writeRecoveredBytes(filepath.Join(dir, "x"), nil), ErrorMatches, "recovery artifact size 0 is outside.*")
	c.Assert(writeRecoveredBytes(filepath.Join(dir, "nodir", "x"), []byte("data")), ErrorMatches, "failed to open recovery temp file.*")
	c.Assert(writeRecoveredBytes(sub, []byte("data")), ErrorMatches, "recovery destination is not a regular file.*")
	entries, err := os.ReadDir(dir)
	c.Assert(err, IsNil)
	c.Check(entries, HasLen, 3) // sub, empty, garbage: no temp file leaked

	c.Assert(syncRecoveryDirectory(filepath.Join(dir, "missing")), ErrorMatches, "failed to open recovery directory.*")

	// raw/decompressed writers reject empty and malformed input
	c.Assert(writeRecoveredRawKeyshares(filepath.Join(dir, "r"), nil, Mnemonic, "mocknet", vault), ErrorMatches, "encrypted raw key shares backup is empty")
	c.Assert(writeRecoveredRawKeyshares(filepath.Join(dir, "r"), []byte("junk"), Mnemonic, "mocknet", vault), ErrorMatches, "failed to decrypt raw key shares.*")
	enc, err := EncryptRawKeyshares([]byte("k"), Mnemonic)
	c.Assert(err, IsNil)
	c.Assert(writeRecoveredRawKeyshares(filepath.Join(dir, "r"), enc, Mnemonic, "devnet", vault), ErrorMatches, "invalid frost recovery network")
	c.Assert(writeRecoveredDecompressedKeyshares(filepath.Join(dir, "d"), nil, Mnemonic), ErrorMatches, "encrypted key shares backup is empty")
}

func (s *RecoverKeysharesCovSuite) TestFrostRecoveryEnvelopeValidation(c *C) {
	vault := types.GetRandomEd25519PubKey()

	_, err := encodeFrostRecoveryEnvelope(nil, "mocknet", vault)
	c.Assert(err, ErrorMatches, "frost recovery key material is empty")
	_, err = encodeFrostRecoveryEnvelope([]byte("k"), "devnet", vault)
	c.Assert(err, ErrorMatches, "invalid frost recovery network")
	_, err = encodeFrostRecoveryEnvelope([]byte("k"), "mocknet", common.EmptyPubKey)
	c.Assert(err, ErrorMatches, "frost recovery vault pubkey is empty")
	_, err = encodeFrostRecoveryEnvelope([]byte("k"), "mocknet", common.PubKey("bogus"))
	c.Assert(err, ErrorMatches, "fail to decode frost recovery vault pubkey.*")
	// secp256k1 keys are 33 bytes, not a 32-byte group key
	_, err = encodeFrostRecoveryEnvelope([]byte("k"), "mocknet", types.GetRandomPubKey())
	c.Assert(err, ErrorMatches, "unexpected frost recovery group key length 33")

	magic := []byte(frostRecoveryEnvelopeMagic)
	_, err = decodeFrostRecoveryEnvelope(magic, "devnet", vault)
	c.Assert(err, ErrorMatches, "invalid frost recovery network")
	_, err = decodeFrostRecoveryEnvelope(magic, "mocknet", common.EmptyPubKey)
	c.Assert(err, ErrorMatches, "frost recovery vault pubkey is empty")
	_, err = decodeFrostRecoveryEnvelope(append(magic, []byte(`{"unknown":1}`)...), "mocknet", vault)
	c.Assert(err, ErrorMatches, "invalid frost recovery envelope:.*")

	good, err := encodeFrostRecoveryEnvelope([]byte("k"), "mocknet", vault)
	c.Assert(err, IsNil)
	_, err = decodeFrostRecoveryEnvelope(append(append([]byte{}, good...), []byte(` {}`)...), "mocknet", vault)
	c.Assert(err, ErrorMatches, "invalid frost recovery envelope: trailing JSON value")
	_, err = decodeFrostRecoveryEnvelope(append(append([]byte{}, good...), []byte(` }`)...), "mocknet", vault)
	c.Assert(err, ErrorMatches, "invalid frost recovery envelope trailer:.*")
	_, err = decodeFrostRecoveryEnvelope(good, "stagenet", vault)
	c.Assert(err, ErrorMatches, "frost recovery network mismatch")
	_, err = decodeFrostRecoveryEnvelope(good, "mocknet", types.GetRandomEd25519PubKey())
	c.Assert(err, ErrorMatches, "frost recovery vault pubkey mismatch")

	groupKey, err := frostRecoveryGroupKey(vault)
	c.Assert(err, IsNil)
	build := func(protocol string, version int, keyB64, group string) []byte {
		payload := `{"protocol":"` + protocol + `","version":` + string(rune('0'+version)) +
			`,"network":"mocknet","vault_pub_key":"` + vault.String() + `","group_key":"` + group +
			`","key_bytes_b64":"` + keyB64 + `"}`
		return append(append([]byte{}, magic...), payload...)
	}
	_, err = decodeFrostRecoveryEnvelope(build("other", frostRecoveryVersion, "aw==", groupKey), "mocknet", vault)
	c.Assert(err, ErrorMatches, "frost recovery protocol/version mismatch")
	_, err = decodeFrostRecoveryEnvelope(build(frostRecoveryProtocol, frostRecoveryVersion, "aw==", "00"), "mocknet", vault)
	c.Assert(err, ErrorMatches, "frost recovery group key mismatch")
	_, err = decodeFrostRecoveryEnvelope(build(frostRecoveryProtocol, frostRecoveryVersion, "!!", groupKey), "mocknet", vault)
	c.Assert(err, ErrorMatches, "invalid frost recovery key material encoding.*")
	_, err = decodeFrostRecoveryEnvelope(build(frostRecoveryProtocol, frostRecoveryVersion, "", groupKey), "mocknet", vault)
	c.Assert(err, ErrorMatches, "frost recovery key material size is outside bounds")
	keyBytes, err := decodeFrostRecoveryEnvelope(build(frostRecoveryProtocol, frostRecoveryVersion, "aw==", groupKey), "mocknet", vault)
	c.Assert(err, IsNil)
	c.Check(keyBytes, DeepEquals, []byte("k"))
}

func (s *RecoverKeysharesCovSuite) TestRecoverScanBoundsEdges(c *C) {
	lo, hi := recoverScanBounds(0, 100, 5)
	c.Check(lo, Equals, int64(0))
	c.Check(hi, Equals, int64(0))
	lo, hi = recoverScanBounds(10, 100, 0)
	c.Check(lo, Equals, int64(10))
	c.Check(hi, Equals, int64(10))
	c.Check(recoverScanLowerBound(0, 5), Equals, int64(0))
	c.Check(recoverScanLowerBound(3, 5), Equals, int64(0))
	c.Check(msgMatchesRecoveryTarget(&types.MsgTssPool{}, nil, keyshareRecoveryTarget{kind: recoveryTargetKind(9)}), Equals, false)
	c.Check(allUnrecoverable(nil), Equals, false)
}
