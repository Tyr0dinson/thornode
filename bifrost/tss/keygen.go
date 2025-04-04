package tss

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// KeyGen is
type KeyGen struct {
	keys           *thorclient.Keys
	logger         zerolog.Logger
	client         *http.Client
	server         *tss.TssServer
	bridge         thorclient.ThorchainBridge
	currentVersion semver.Version
	lastCheck      time.Time
}

// NewTssKeyGen create a new instance of TssKeyGen which will look after TSS key stuff
func NewTssKeyGen(keys *thorclient.Keys, server *tss.TssServer, bridge thorclient.ThorchainBridge) (*KeyGen, error) {
	if keys == nil {
		return nil, fmt.Errorf("keys is nil")
	}
	return &KeyGen{
		keys:   keys,
		logger: log.With().Str("module", "tss_keygen").Logger(),
		client: &http.Client{
			Timeout: time.Second * 130,
		},
		server: server,
		bridge: bridge,
	}, nil
}

func (kg *KeyGen) getVersion() semver.Version {
	requestTime := time.Now()
	if !kg.currentVersion.Equals(semver.Version{}) && requestTime.Sub(kg.lastCheck).Seconds() < constants.ThorchainBlockTime.Seconds() {
		return kg.currentVersion
	}
	version, err := kg.bridge.GetThorchainVersion()
	if err != nil {
		kg.logger.Err(err).Msg("fail to get current thorchain version")
		return kg.currentVersion
	}
	kg.currentVersion = version
	kg.lastCheck = requestTime
	return kg.currentVersion
}

func (kg *KeyGen) GenerateNewKey(keygenBlockHeight int64, pKeys common.PubKeys) (pk common.PubKeySet, blame []types.Blame, err error) {
	// No need to do key gen
	if len(pKeys) == 0 {
		return common.EmptyPubKeySet, nil, nil
	}

	// add some logging
	defer func() {
		if len(blame) == 0 {
			kg.logger.Info().Int64("height", keygenBlockHeight).Str("pubkey", pk.String()).Msg("tss keygen results success")
		} else {
			for _, b := range blame {
				blames := make([]string, len(b.BlameNodes))
				for i := range b.BlameNodes {
					var pk common.PubKey
					pk, err = common.NewPubKey(b.BlameNodes[i].Pubkey)
					if err != nil {
						kg.logger.Error().Err(err).Int64("height", keygenBlockHeight).Str("pubkey", b.BlameNodes[i].Pubkey).Msg("tss keygen results error")
						continue
					}
					var acc cosmos.AccAddress
					acc, err = pk.GetThorAddress()
					if err != nil {
						kg.logger.Error().Err(err).Int64("height", keygenBlockHeight).Str("pubkey", pk.String()).Msg("tss keygen results error")
						continue
					}
					blames[i] = acc.String()
				}
				sort.Strings(blames)
				kg.logger.Info().Int64("height", keygenBlockHeight).Str("pubkey", pk.String()).Str("round", b.Round).Str("blames", strings.Join(blames, ", ")).Str("reason", b.FailReason).Msg("tss keygen results blame")
			}
		}
	}()

	var keys []string
	for _, item := range pKeys {
		keys = append(keys, item.String())
	}
	keyGenReq := keygen.Request{
		Keys: keys,
	}
	currentVersion := kg.getVersion()
	keyGenReq.Version = currentVersion.String()

	// Use the churn try's block to choose the same leader for every node in an Asgard,
	// since a successful keygen requires every node in the Asgard to take part.
	keyGenReq.BlockHeight = keygenBlockHeight

	ch := make(chan bool, 1)
	defer close(ch)
	timer := time.NewTimer(30 * time.Minute)
	defer timer.Stop()

	var responses []keygen.Response
	go func() {
		responses, err = kg.server.KeygenAllAlgo(keyGenReq)
		ch <- true
	}()

	select {
	case <-ch:
		// do nothing
	case <-timer.C:
		panic("tss keygen timeout")
	}

	for _, resp := range responses {
		// copy blame to our own struct
		b := types.Blame{
			FailReason: resp.Blame.FailReason,
			IsUnicast:  resp.Blame.IsUnicast,
			Round:      resp.Blame.Round,
			BlameNodes: make([]types.Node, len(resp.Blame.BlameNodes)),
		}
		for i, n := range resp.Blame.BlameNodes {
			b.BlameNodes[i].Pubkey = n.Pubkey
			b.BlameNodes[i].BlameData = n.BlameData
			b.BlameNodes[i].BlameSignature = n.BlameSignature
		}

		if err != nil {
			// the resp from kg.server.Keygen will not be nil
			if b.IsEmpty() {
				b.FailReason = err.Error()
			}
			blame = append(blame, b)
		}
	}

	if len(blame) > 0 {
		return common.EmptyPubKeySet, blame, fmt.Errorf("fail to keygen :%w", err)
	}

	var ecdsaPubKey common.PubKey
	var eddsaPubKey common.PubKey
	for _, resp := range responses {
		switch resp.Algo {
		case common.SigningAlgoSecp256k1:
			var err error
			ecdsaPubKey, err = common.NewPubKey(resp.PubKey)
			if err != nil {
				return common.EmptyPubKeySet, blame, fmt.Errorf("fail to create ECDSA PubKey: %w", err)
			}
		case common.SigningAlgoEd25519:
			var err error
			eddsaPubKey, err = common.NewPubKey(resp.PubKey)
			if err != nil {
				return common.EmptyPubKeySet, blame, fmt.Errorf("fail to create EDDSA PubKey: %w", err)
			}
		}
	}

	return common.NewPubKeySet(ecdsaPubKey, eddsaPubKey), blame, nil
}
