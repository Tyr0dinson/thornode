package monero

import (
	"math"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newFeeRateTestClient(t *testing.T, tssServer *consolidationTSS, vaultPubKey common.PubKey, vaultAddr common.Address) *Client {
	t.Helper()

	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyEddsa: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 1,
				Membership:  []string{"member-a"},
			},
		},
	}
	balanceServer := newFrostBalanceServer(t, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:         100_000 * moneroAtomicToThorchain,
			UnlockedBalance: 100_000 * moneroAtomicToThorchain,
		},
	})
	t.Cleanup(balanceServer.Close)

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	return &Client{
		chain:              common.XMRChain,
		localNodePubKey:    common.PubKey("member-a"),
		frostSigner:        frostSigner,
		tssServer:          tssServer,
		signerCacheManager: newTestSignerCacheManager(t),
		thorchainBridge:    bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
}

func TestSignTxConvertsThorchainGasRateForSidecarFeeOverride(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					Status:    tsscommon.Success,
					TxHex:     "deadbeef",
					TxHash:    "fee-rate-outbound-hash",
					FeeAtomic: 180_000_000,
					TxKeys:    testFrostTxKeys(),
				},
			},
		},
	}
	client := newFeeRateTestClient(t, tssServer, vaultPubKey, vaultAddr)

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo:    "MIGRATE:26113",
		GasRate: 12,
		MaxGas: common.Gas{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(18_000)),
		},
	}

	if _, _, _, err := client.SignTx(tx, 26113); err != nil {
		t.Fatalf("SignTx failed: %v", err)
	}

	req := tssServer.request()
	if req.FeeRate != 12*moneroAtomicToThorchain {
		t.Fatalf("expected sidecar fee rate %d, got %d", 12*moneroAtomicToThorchain, req.FeeRate)
	}
}

func TestSignTxPreservesNativeFeeRateForConsolidation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					Status:    tsscommon.Success,
					TxHex:     "deadbeef",
					TxHash:    "fee-rate-consolidate-hash",
					FeeAtomic: 1_000,
					TxKeys:    testFrostTxKeys(),
				},
			},
		},
	}
	client := newFeeRateTestClient(t, tssServer, vaultPubKey, vaultAddr)

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        vaultAddr,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo:    memo.NewConsolidateMemo().String(),
		GasRate: 12345,
	}

	if _, _, _, err := client.SignTx(tx, 100); err != nil {
		t.Fatalf("SignTx failed: %v", err)
	}

	req := tssServer.request()
	if req.FeeRate != 12345 {
		t.Fatalf("expected native consolidation fee rate 12345, got %d", req.FeeRate)
	}
}

func TestXMRSignerFeeRateOverrideClampsOverflow(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Memo:    "MIGRATE:1",
		GasRate: math.MaxInt64,
	}

	if got := xmrSignerFeeRateOverride(tx); got != math.MaxUint64 {
		t.Fatalf("expected overflow clamp to %d, got %d", uint64(math.MaxUint64), got)
	}
}
