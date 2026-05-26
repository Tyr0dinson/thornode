package tss

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/cometbft/cometbft/crypto"
	sdkTypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tendermint/btcd/btcec"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	maxKeysignPerRequest = 15 // the maximum number of messages include in one single TSS keysign request
	tssKeysignTimeout    = 5  // in minutes, the maximum time bifrost is going to wait before the tss result come back
)

type tssServer interface {
	KeySign(req keysign.Request) (keysign.Response, error)
}

// KeySign is a proxy between signer and TSS
type KeySign struct {
	logger         zerolog.Logger
	server         tssServer
	bridge         thorclient.ThorchainBridge
	currentVersion semver.Version
	lastCheck      time.Time
	wg             *sync.WaitGroup
	taskQueue      chan *tssKeySignTask
	done           chan struct{}
}

// NewKeySign create a new instance of KeySign
func NewKeySign(server tssServer, bridge thorclient.ThorchainBridge) (*KeySign, error) {
	return &KeySign{
		server:    server,
		bridge:    bridge,
		logger:    log.With().Str("module", "tss_signer").Logger(),
		wg:        &sync.WaitGroup{},
		taskQueue: make(chan *tssKeySignTask),
		done:      make(chan struct{}),
	}, nil
}

// GetPrivKey THORNode don't actually have any private key , but just return something
func (s *KeySign) GetPrivKey() crypto.PrivKey {
	return nil
}

func (s *KeySign) GetAddr() sdkTypes.AccAddress {
	return nil
}

// ExportAsMnemonic THORNode don't need this function for TSS, just keep it to fulfill KeyManager interface
func (s *KeySign) ExportAsMnemonic() (string, error) {
	return "", nil
}

// ExportAsPrivateKey THORNode don't need this function for TSS, just keep it to fulfill KeyManager interface
func (s *KeySign) ExportAsPrivateKey() (string, error) {
	return "", nil
}

// ExportAsKeyStore THORNode don't need this function for TSS, just keep it to fulfill KeyManager interface
func (s *KeySign) ExportAsKeyStore(password string) (*EncryptedKeyJSON, error) {
	return nil, nil
}

// Start the keysign workers
func (s *KeySign) Start() {
	s.wg.Add(1)
	go s.processKeySignTasks()
}

// Stop Keysign
func (s *KeySign) Stop() {
	close(s.done)
	s.wg.Wait()
	close(s.taskQueue)
}

// RemoteSign send the request to local task queue
func (s *KeySign) RemoteSign(msg []byte, algo common.SigningAlgo, poolPubKey string) ([]byte, []byte, error) {
	return s.remoteSign(msg, algo, poolPubKey, 0, false)
}

// RemoteSignAtHeight signs at a caller-supplied THORChain height so all nodes
// in the same outbound ceremony use the same jail-aware signer policy.
func (s *KeySign) RemoteSignAtHeight(msg []byte, algo common.SigningAlgo, poolPubKey string, thorchainHeight int64) ([]byte, []byte, error) {
	return s.remoteSign(msg, algo, poolPubKey, thorchainHeight, false)
}

// RemoteSignWithLocalStateParty signs using local TSS keygen state instead of a
// vault-backed keysign party. This is only for keygen self-check signatures,
// which run before thornode has saved the new vault.
func (s *KeySign) RemoteSignWithLocalStateParty(msg []byte, algo common.SigningAlgo, poolPubKey string) ([]byte, []byte, error) {
	return s.remoteSign(msg, algo, poolPubKey, 0, true)
}

// remoteSign queues a TSS signing task and waits for the worker to return the
// signature material. useLocalStateParty is reserved for keygen check-signs,
// which have local keygen state but no thornode vault record yet.
func (s *KeySign) remoteSign(msg []byte, algo common.SigningAlgo, poolPubKey string, thorchainHeight int64, useLocalStateParty bool) ([]byte, []byte, error) {
	if len(msg) == 0 {
		return nil, nil, nil
	}

	encodedMsg := base64.StdEncoding.EncodeToString(msg)
	task := tssKeySignTask{
		Algo:               algo,
		PoolPubKey:         poolPubKey,
		Msg:                encodedMsg,
		Resp:               make(chan tssKeySignResult, 1),
		ThorchainHeight:    thorchainHeight,
		UseLocalStateParty: useLocalStateParty,
	}
	s.taskQueue <- &task
	select {
	case resp := <-task.Resp:
		if resp.Err != nil {
			return nil, nil, fmt.Errorf("fail to tss sign: %w", resp.Err)
		}

		if algo == common.SigningAlgoEd25519 {
			// ed25519 signature is not R and S , but a single byte array
			sBytes, err := base64.StdEncoding.DecodeString(resp.S)
			if err != nil {
				return nil, nil, err
			}
			return sBytes, nil, nil
		}

		if len(resp.R) == 0 && len(resp.S) == 0 {
			// this means the node tried to do keysign , however this node has not been chosen to take part in the keysign committee
			return nil, nil, ErrNotActiveSigner
		}
		s.logger.Debug().Str("R", resp.R).Str("S", resp.S).Str("recovery", resp.RecoveryID).Msg("tss result")
		data, err := getSignature(resp.R, resp.S)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode tss signature: %w", err)
		}
		bRecoveryId, err := base64.StdEncoding.DecodeString(resp.RecoveryID)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode recovery id: %w", err)
		}
		return data, bRecoveryId, nil
	case <-time.After(time.Minute * tssKeysignTimeout):
		return nil, nil, fmt.Errorf("TIMEOUT: fail to sign message:%s after %d minutes", encodedMsg, tssKeysignTimeout)
	}
}

type tssKeySignTask struct {
	PoolPubKey string
	Algo       common.SigningAlgo
	Msg        string
	Resp       chan tssKeySignResult

	// ThorchainHeight pins signer policy selection to the outbound's THORChain
	// height. A zero value falls back to the current bridge height.
	ThorchainHeight int64

	// UseLocalStateParty bypasses vault-backed signer filtering for keygen
	// check-signatures that run before the new vault exists on thornode.
	UseLocalStateParty bool
}

type tssKeySignResult struct {
	R          string
	S          string
	RecoveryID string
	Err        error
}

func (s *KeySign) processKeySignTasks() {
	defer s.wg.Done()
	tasks := make(map[string][]*tssKeySignTask)
	taskLock := sync.Mutex{}
	for {
		select {
		case <-s.done:
			// requested to exit
			return
		case t := <-s.taskQueue:
			taskLock.Lock()
			_, ok := tasks[t.PoolPubKey]
			if !ok {
				tasks[t.PoolPubKey] = []*tssKeySignTask{
					t,
				}
			} else {
				tasks[t.PoolPubKey] = append(tasks[t.PoolPubKey], t)
			}
			taskLock.Unlock()
		case <-time.After(time.Second):
			// This implementation will check the tasks every second , and send whatever is in the queue to TSS
			// if it has more than maxKeysignPerRequest(15) in the queue , it will only send the first maxKeysignPerRequest(15) of them
			// the reset will be send in the next request
			taskLock.Lock()
			for k, v := range tasks {
				if len(v) == 0 {
					delete(tasks, k)
					continue
				}
				totalTasks := len(v)
				// send no more than maxKeysignPerRequest messages in a single TSS keysign request
				// Always sort tasks to ensure deterministic ordering across all nodes.
				// The order of messages in the slice is non-deterministic due to goroutine execution timing.
				// All bifrosts MUST send messages in the same order for TSS signature pairing to work correctly.
				sort.SliceStable(v, func(i, j int) bool {
					return v[i].Msg < v[j].Msg
				})

				if totalTasks > maxKeysignPerRequest {
					totalTasks = maxKeysignPerRequest
				}
				signingTask := v[:totalTasks]
				tasks[k] = v[totalTasks:]

				// Split by algorithm and by signer source. Secp256k1 keygen
				// self-checks use local keygen state, while normal Secp256k1
				// vault signing uses thornode's filtered keysign party.
				signingTasksSecp256k1Vault := make(map[int64][]*tssKeySignTask)
				signingTasksSecp256k1LocalState := make([]*tssKeySignTask, 0)
				signingTasksEd25519 := make([]*tssKeySignTask, 0)
				for _, task := range signingTask {
					switch task.Algo {
					case common.SigningAlgoSecp256k1:
						if task.UseLocalStateParty {
							signingTasksSecp256k1LocalState = append(signingTasksSecp256k1LocalState, task)
						} else {
							signingTasksSecp256k1Vault[task.ThorchainHeight] = append(signingTasksSecp256k1Vault[task.ThorchainHeight], task)
						}
					case common.SigningAlgoEd25519:
						signingTasksEd25519 = append(signingTasksEd25519, task)
					default:
						s.setTssKeySignTasksFail(signingTask, fmt.Errorf("unknown signing algo %s", task.Algo))
						continue
					}
				}

				for _, tasksAtHeight := range signingTasksSecp256k1Vault {
					if len(tasksAtHeight) > 0 {
						s.wg.Add(1)
						go s.toLocalTSSSigner(k, common.SigningAlgoSecp256k1, tasksAtHeight)
					}
				}
				if len(signingTasksSecp256k1LocalState) > 0 {
					s.wg.Add(1)
					go s.toLocalTSSSigner(k, common.SigningAlgoSecp256k1, signingTasksSecp256k1LocalState)
				}
				if len(signingTasksEd25519) > 0 {
					s.wg.Add(1)
					go s.toLocalTSSSigner(k, common.SigningAlgoEd25519, signingTasksEd25519)
				}
			}
			taskLock.Unlock()
		}
	}
}

func getSignature(r, s string) ([]byte, error) {
	rBytes, err := base64.StdEncoding.DecodeString(r)
	if err != nil {
		return nil, err
	}
	sBytes, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}

	R := new(big.Int).SetBytes(rBytes)
	S := new(big.Int).SetBytes(sBytes)
	N := btcec.S256().N
	halfOrder := new(big.Int).Rsh(N, 1)
	// see: https://github.com/ethereum/go-ethereum/blob/f9401ae011ddf7f8d2d95020b7446c17f8d98dc1/crypto/signature_nocgo.go#L90-L93
	if S.Cmp(halfOrder) == 1 {
		S.Sub(N, S)
	}

	// Serialize signature to R || S.
	// R, S are padded to 32 bytes respectively.
	rBytes = R.Bytes()
	sBytes = S.Bytes()

	sigBytes := make([]byte, 64)
	// 0 pad the byte arrays from the left if they aren't big enough.
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)
	return sigBytes, nil
}

func (s *KeySign) getVersion() semver.Version {
	requestTime := time.Now()
	// analyze-ignore(float-comparison)
	if !s.currentVersion.Equals(semver.Version{}) && requestTime.Sub(s.lastCheck).Seconds() < constants.ThorchainBlockTime.Seconds() {
		return s.currentVersion
	}
	version, err := s.bridge.GetThorchainVersion()
	if err != nil {
		s.logger.Err(err).Msg("fail to get current thorchain version")
		return s.currentVersion
	}
	s.currentVersion = version
	s.lastCheck = requestTime
	return s.currentVersion
}

// getCompromisedVaultMimir reports whether the vault-level operational Mimir is
// set. A positive value disables normal Secp256k1 signing for that vault before
// go-tss opens a join party.
func (s *KeySign) getCompromisedVaultMimir(poolPubKey string) (bool, error) {
	val, err := s.bridge.GetMimirWithRef(constants.MimirTemplateCompromisedVault, poolPubKey)
	if err != nil {
		return false, err
	}
	if val > 0 {
		return true, nil
	}

	// Some Mimir paths normalize node-vote and event keys to uppercase. Support
	// that spelling as well so operators can recover even if the flag was set in
	// uppercase form.
	key := fmt.Sprintf(constants.MimirTemplateCompromisedVault, poolPubKey)
	val, err = s.bridge.GetMimir(strings.ToUpper(key))
	if err != nil {
		return false, err
	}
	return val > 0, nil
}

func (s *KeySign) isTSSSignerFilteringDisabled() (bool, error) {
	val, err := s.bridge.GetMimir(constants.MimirKeyDisableTSSSignerFiltering)
	if err != nil {
		return false, err
	}
	return val > 0, nil
}

func (s *KeySign) isTSSSignerFilteringDisabled() (bool, error) {
	val, err := s.bridge.GetMimir(constants.MimirKeyDisableTSSSignerFiltering)
	if err != nil {
		return false, err
	}
	return val > 0, nil
}

func (s *KeySign) setTssKeySignTasksFail(tasks []*tssKeySignTask, err error) {
	for _, item := range tasks {
		select {
		case item.Resp <- tssKeySignResult{
			Err: err,
		}:
		case <-time.After(time.Second):
			// this is a fallback , if fail to send a failed result back to caller , it doesn't stuck
			continue
		}
	}
}

func (s *KeySign) keysignPolicyHeight(tasks []*tssKeySignTask) (int64, error) {
	if len(tasks) > 0 && tasks[0].ThorchainHeight > 0 {
		return common.RoundKeysignPolicyHeight(tasks[0].ThorchainHeight), nil
	}

	blockHeight, err := s.bridge.GetBlockHeight()
	if err != nil {
		return 0, fmt.Errorf("fail to get block height from thorchain: %w", err)
	}
	return common.RoundKeysignPolicyHeight(blockHeight), nil
}

type keysignPartyAtHeightBridge interface {
	GetKeysignPartyAtHeight(vaultPubKey common.PubKey, blockHeight int64) (common.PubKeys, error)
}

func getKeysignPartyAtHeight(bridge thorclient.ThorchainBridge, vaultPubKey common.PubKey, blockHeight int64) (common.PubKeys, error) {
	if heightAware, ok := bridge.(keysignPartyAtHeightBridge); ok {
		return heightAware.GetKeysignPartyAtHeight(vaultPubKey, blockHeight)
	}
	return bridge.GetKeysignParty(vaultPubKey)
}

// toLocalTSSSigner will send the request to local signer
func (s *KeySign) toLocalTSSSigner(poolPubKey string, algo common.SigningAlgo, tasks []*tssKeySignTask) {
	defer s.wg.Done()
	var msgToSign []string
	for _, item := range tasks {
		msgToSign = append(msgToSign, item.Msg)
	}
	tssMsg := keysign.Request{
		Algo:       string(algo),
		PoolPubKey: poolPubKey,
		Messages:   msgToSign,
	}
	currentVersion := s.getVersion()
	tssMsg.Version = currentVersion.String()
	s.logger.Debug().Msg("new TSS join party")
	policyHeight, err := s.keysignPolicyHeight(tasks)
	if err != nil {
		s.setTssKeySignTasksFail(tasks, err)
		return
	}
	tssMsg.BlockHeight = policyHeight

	useLocalStateParty := len(tasks) > 0 && tasks[0].UseLocalStateParty
	if algo == common.SigningAlgoSecp256k1 && !useLocalStateParty {
		// The compromise mimir is a simple flag: any positive value disables
		// normal ECDSA signing from this vault before go-tss opens a join party.
		flagged, flaggedErr := s.getCompromisedVaultMimir(poolPubKey)
		if flaggedErr != nil {
			s.setTssKeySignTasksFail(tasks, fmt.Errorf("fail to get compromised vault mimir: %w", flaggedErr))
			return
		}
		if flagged {
			s.setTssKeySignTasksFail(tasks, fmt.Errorf("vault %s is flagged as compromised", poolPubKey))
			return
		}

		filteringDisabled, filteringErr := s.isTSSSignerFilteringDisabled()
		if filteringErr != nil {
			s.setTssKeySignTasksFail(tasks, fmt.Errorf("fail to get TSS signer filtering mimir: %w", filteringErr))
			return
		}
		if filteringDisabled {
			s.logger.Warn().Str("pool_pub_key", poolPubKey).Msg("TSS signer filtering disabled by Mimir")
		} else {
			// Filtered signer selection is intentionally scoped to normal GG20/ECDSA
			// keysign. EdDSA/FROST paths do not use this vulnerable Paillier flow, and
			// keygen check-signs must use the local state because their vault is not
			// stored on thornode yet.
			keysignParty, keysignPartyErr := getKeysignPartyAtHeight(s.bridge, common.PubKey(poolPubKey), policyHeight)
			if keysignPartyErr != nil {
				s.setTssKeySignTasksFail(tasks, fmt.Errorf("fail to get keysign party: %w", keysignPartyErr))
				return
			}
			tssMsg.SignerPubKeys = keysignParty.Strings()
		}
	}

	s.logger.Info().Msgf("msgToSign to tss Local node PoolPubKey: %s, Messages: %+v, block height: %d", tssMsg.PoolPubKey, tssMsg.Messages, tssMsg.BlockHeight)

	keySignResp, respErr := s.server.KeySign(tssMsg)
	if respErr != nil {
		s.setTssKeySignTasksFail(tasks, fmt.Errorf("fail tss keysign: %w", respErr))
		return
	}

	// Status values are success=1, fail=2, NA=0. NA with no signatures or blame
	// is the explicit stand-down response for a local node excluded from the
	// filtered ECDSA signer set.
	if keySignResp.Status == tsscommon.NA &&
		len(keySignResp.Signatures) == 0 &&
		keySignResp.Blame.FailReason == "" &&
		len(keySignResp.Blame.BlameNodes) == 0 {
		for _, t := range tasks {
			t.Resp <- tssKeySignResult{}
		}
		return
	}

	// Status values are success=1, fail=2, NA=0. NA with no signatures or blame
	// is the explicit stand-down response for a local node excluded from the
	// filtered ECDSA signer set.
	if keySignResp.Status == tsscommon.NA &&
		len(keySignResp.Signatures) == 0 &&
		keySignResp.Blame.FailReason == "" &&
		len(keySignResp.Blame.BlameNodes) == 0 {
		for _, t := range tasks {
			t.Resp <- tssKeySignResult{}
		}
		return
	}

	// Status values are success=1, fail=2, NA=0. NA with no signatures or blame
	// is the explicit stand-down response for a local node excluded from the
	// filtered ECDSA signer set.
	if keySignResp.Status == tsscommon.NA &&
		len(keySignResp.Signatures) == 0 &&
		keySignResp.Blame.FailReason == "" &&
		len(keySignResp.Blame.BlameNodes) == 0 {
		for _, t := range tasks {
			t.Resp <- tssKeySignResult{}
		}
		return
	}

	// 1 means success,2 means fail , 0 means NA
	if keySignResp.Status == 1 && len(keySignResp.Blame.BlameNodes) == 0 {
		s.logger.Info().Msgf("response: %+v", keySignResp)
		// success
		for _, t := range tasks {
			found := false
			for _, sig := range keySignResp.Signatures {
				if t.Msg == sig.Msg {
					t.Resp <- tssKeySignResult{
						R:          sig.R,
						S:          sig.S,
						RecoveryID: sig.RecoveryID,
						Err:        nil,
					}
					found = true
					break
				}
			}
			// Didn't find the signature in the tss keysign result , notify the task , so it doesn't get stuck
			if !found {
				t.Resp <- tssKeySignResult{
					Err: fmt.Errorf("didn't find signature for message %s in the keysign result", t.Msg),
				}
			}
		}
		return
	}

	// copy blame to our own struct
	blame := types.Blame{
		FailReason: keySignResp.Blame.FailReason,
		Round:      types.CanonicalTssRound(keySignResp.Blame.Round, algo),
		IsUnicast:  keySignResp.Blame.IsUnicast,
		BlameNodes: make([]types.Node, len(keySignResp.Blame.BlameNodes)),
	}
	for i, n := range keySignResp.Blame.BlameNodes {
		blame.BlameNodes[i].Pubkey = n.Pubkey
		blame.BlameNodes[i].BlameData = n.BlameData
		blame.BlameNodes[i].BlameSignature = n.BlameSignature
	}

	// Blame need to be passed back to thorchain , so as thorchain can use the information to slash relevant node account
	s.setTssKeySignTasksFail(tasks, NewKeysignError(blame))
}
