package app

import (
	"context"
	"encoding/hex"
	"testing"

	bankapi "cosmossdk.io/api/cosmos/bank/v1beta1"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	multisigtypes "github.com/cosmos/cosmos-sdk/crypto/types/multisig"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/migrations/legacytx"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	evmcodec "github.com/cosmos/evm/encoding/codec"
	"github.com/cosmos/evm/ethereum/eip712"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	prefix "gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"

	appparams "gitlab.com/thorchain/thornode/v3/app/params"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestSDKSigningRoundTrip(t *testing.T) {
	ec := appparams.MakeEncodingConfig()
	std.RegisterInterfaces(ec.InterfaceRegistry)
	banktypes.RegisterInterfaces(ec.InterfaceRegistry)
	thorchaintypes.RegisterInterfaces(ec.InterfaceRegistry)
	evmcodec.RegisterInterfaces(ec.InterfaceRegistry)
	cfg, err := appparams.TxConfig(ec.Codec, func(context.Context, string) (*bankapi.Metadata, error) {
		return &bankapi.Metadata{Base: "rune", Display: "rune", DenomUnits: []*bankapi.DenomUnit{{Denom: "rune", Exponent: 0}}}, nil
	})
	require.NoError(t, err)

	key := secp256k1.GenPrivKeyFromSecret([]byte("sdk-upgrade-signing-fixture"))
	ethKey := &ethsecp256k1.PrivKey{Key: key.Bytes()}
	for _, tc := range []struct {
		name string
		mode signing.SignMode
		key  cryptotypes.PrivKey
	}{
		{"direct", signing.SignMode_SIGN_MODE_DIRECT, key},
		{"direct-aux", signing.SignMode_SIGN_MODE_DIRECT_AUX, key},
		{"textual", signing.SignMode_SIGN_MODE_TEXTUAL, key},
		{"custom-amino", signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, key},
		{"ethereum-direct", signing.SignMode_SIGN_MODE_DIRECT, ethKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := sdk.AccAddress(tc.key.PubKey().Address())
			builder := cfg.NewTxBuilder()
			msg := sdk.Msg(banktypes.NewMsgSend(address, address, sdk.NewCoins(sdk.NewInt64Coin("rune", 100))))
			if tc.name == "custom-amino" {
				msg = thorchaintypes.NewMsgSend(address, address, sdk.NewCoins(sdk.NewInt64Coin("rune", 100)))
			}
			require.NoError(t, builder.SetMsgs(msg))
			builder.SetMemo("sdk-v0.54-compatibility")
			builder.SetGasLimit(200000)
			builder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("rune", math.NewInt(100))))
			if tc.mode == signing.SignMode_SIGN_MODE_DIRECT_AUX {
				// An auxiliary signer must not be the fee payer.
				builder.SetFeePayer(sdk.AccAddress(secp256k1.GenPrivKeyFromSecret([]byte("fee-payer")).PubKey().Address()))
			}
			data := authsigning.SignerData{Address: address.String(), ChainID: "thorchain-1", AccountNumber: 123, Sequence: 456, PubKey: tc.key.PubKey()}
			sig := signing.SignatureV2{PubKey: tc.key.PubKey(), Sequence: data.Sequence, Data: &signing.SingleSignatureData{SignMode: tc.mode}}
			require.NoError(t, builder.SetSignatures(sig))
			signBytes, err := authsigning.GetSignBytesAdapter(context.Background(), cfg.SignModeHandler(), tc.mode, data, builder.GetTx())
			require.NoError(t, err)
			require.NotEmpty(t, signBytes)
			if tc.mode == signing.SignMode_SIGN_MODE_DIRECT {
				var doc txtypes.SignDoc
				require.NoError(t, doc.Unmarshal(signBytes))
				require.Equal(t, "thorchain-1", doc.ChainId)
				require.Equal(t, uint64(123), doc.AccountNumber)
				require.NotEmpty(t, doc.BodyBytes)
				require.NotEmpty(t, doc.AuthInfoBytes)
			}
			signature, err := tc.key.Sign(signBytes)
			require.NoError(t, err)
			signatureData, ok := sig.Data.(*signing.SingleSignatureData)
			require.True(t, ok)
			signatureData.Signature = signature
			require.NoError(t, builder.SetSignatures(sig))
			encoded, err := cfg.TxEncoder()(builder.GetTx())
			require.NoError(t, err)
			decoded, err := cfg.TxDecoder()(encoded)
			require.NoError(t, err)
			signedTx, ok := decoded.(authsigning.Tx)
			require.True(t, ok)
			signers, err := signedTx.GetSigners()
			require.NoError(t, err)
			require.Equal(t, []byte(address), signers[0])
			signatures, err := signedTx.GetSignaturesV2()
			require.NoError(t, err)
			require.Equal(t, data.Sequence, signatures[0].Sequence)
			decodedBytes, err := authsigning.GetSignBytesAdapter(context.Background(), cfg.SignModeHandler(), tc.mode, data, decoded)
			require.NoError(t, err)
			require.Equal(t, signBytes, decodedBytes)
			require.True(t, tc.key.PubKey().VerifySignature(decodedBytes, signature))
			require.False(t, tc.key.PubKey().VerifySignature(append(decodedBytes, 0), signature))
		})
	}
}

func TestSDKLegacyMultisigSigning(t *testing.T) {
	ec := appparams.MakeEncodingConfig()
	std.RegisterInterfaces(ec.InterfaceRegistry)
	thorchaintypes.RegisterInterfaces(ec.InterfaceRegistry)
	keys := []cryptotypes.PrivKey{
		secp256k1.GenPrivKeyFromSecret([]byte("multisig-one")),
		secp256k1.GenPrivKeyFromSecret([]byte("multisig-two")),
	}
	pub := multisig.NewLegacyAminoPubKey(2, []cryptotypes.PubKey{keys[0].PubKey(), keys[1].PubKey()})
	address := sdk.AccAddress(pub.Address())
	builder := ec.TxConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(thorchaintypes.NewMsgSend(address, address, sdk.NewCoins(sdk.NewInt64Coin("rune", 100)))))
	data := authsigning.SignerData{Address: address.String(), ChainID: "thorchain-1", AccountNumber: 123, Sequence: 456, PubKey: pub}
	getSignBytes := func(mode signing.SignMode) ([]byte, error) {
		return authsigning.GetSignBytesAdapter(context.Background(), ec.TxConfig.SignModeHandler(), mode, data, builder.GetTx())
	}
	bytes, err := getSignBytes(signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON)
	require.NoError(t, err)
	multi := multisigtypes.NewMultisig(len(keys))
	for i, key := range keys {
		signature, signErr := key.Sign(bytes)
		require.NoError(t, signErr)
		multi.BitArray.SetIndex(i, true)
		multi.Signatures = append(multi.Signatures, &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, Signature: signature})
	}
	require.NoError(t, builder.SetSignatures(signing.SignatureV2{PubKey: pub, Sequence: data.Sequence, Data: multi}))
	encoded, err := ec.TxConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)
	decoded, err := ec.TxConfig.TxDecoder()(encoded)
	require.NoError(t, err)
	decodedSignBytes := func(mode signing.SignMode) ([]byte, error) {
		return authsigning.GetSignBytesAdapter(context.Background(), ec.TxConfig.SignModeHandler(), mode, data, decoded)
	}
	require.NoError(t, pub.VerifyMultisignature(decodedSignBytes, multi))
	signatureData, ok := multi.Signatures[1].(*signing.SingleSignatureData)
	require.True(t, ok)
	signatureData.Signature[0] ^= 1
	require.Error(t, pub.VerifyMultisignature(getSignBytes, multi))
}

func TestSDKDirectEIP712Translation(t *testing.T) {
	config := sdk.GetConfig()
	oldPrefix, oldPubPrefix := config.GetBech32AccountAddrPrefix(), config.GetBech32AccountPubPrefix()
	cacheEnabled := sdk.IsAddrCacheEnabled()
	config.SetBech32PrefixForAccount(prefix.Bech32PrefixAccAddr, prefix.Bech32PrefixAccPub)
	sdk.SetAddrCacheEnabled(false)
	t.Cleanup(func() {
		config.SetBech32PrefixForAccount(oldPrefix, oldPubPrefix)
		sdk.SetAddrCacheEnabled(cacheEnabled)
	})
	// Captured separately on HEAD's SDK v0.53.0 / Cosmos EVM v1.0.0-rc2.
	// Address prefixes are part of the signed legacy Amino representation.
	baseline := map[string][2]string{
		"thor":  {"19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4d461543993e28c96f61fcfd8df83d0509458c6cfe3aed6a9039fa15e363ea9629", "19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4d49fff4ef3f640243957d26348b4996e9e7c8ba1b2b74ccf15a412629759f6aa3"},
		"tthor": {"19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4d9440d2be4ab3ffab259aec98bb604a4e694db1184d5d01df0431133ff814db13", "19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4d18880f09b3ccb3232c113af231acfbc83cadc49929f0ba92b1b090fb4490a319"},
		"sthor": {"19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4dc695dfec815dea9d867f80229447b998824dccbee4606f502d0eaeea9a55bd71", "19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4dcd0bd93149bc3e8470e11265ef10dabc09f3ea336087a8c0fc0ca13d71a2c0c5"},
		"cthor": {"19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4dfde769c5325bd4ae0d3ea31453cebba003a6cd45cd8cf627fc119e66addac94d", "19012b9acd74afc6c289b31cb87987f64607b01b9667b01445706211e7d79096ef4da7470af90d1e6319b92584815293b9ad5a09b055e32de87750c195020c4013b4"},
	}[prefix.Bech32PrefixAccAddr]
	require.NotEmpty(t, baseline[0], "missing baseline fixture for network prefix")

	ec := appparams.MakeEncodingConfig()
	std.RegisterInterfaces(ec.InterfaceRegistry)
	thorchaintypes.RegisterInterfaces(ec.InterfaceRegistry)
	thorchaintypes.RegisterLegacyAminoCodec(ec.Amino)
	evmcodec.RegisterInterfaces(ec.InterfaceRegistry)
	evmcodec.RegisterLegacyAminoCodec(ec.Amino)
	legacytx.RegressionTestingAminoCodec = ec.Amino
	eip712.SetEncodingConfig(ec.Amino, ec.InterfaceRegistry, 1)
	key := &ethsecp256k1.PrivKey{Key: secp256k1.GenPrivKeyFromSecret([]byte("sdk-upgrade-eip712-fixture")).Bytes()}
	address := sdk.AccAddress(key.PubKey().Address())
	for _, tc := range []struct {
		name          string
		msg           sdk.Msg
		baselineTyped string
	}{
		{"send", thorchaintypes.NewMsgSend(address, address, sdk.NewCoins(sdk.NewInt64Coin("rune", 100))), baseline[0]},
		{"deposit", thorchaintypes.NewMsgDeposit(common.NewCoins(common.NewCoin(common.ETHAsset, math.NewUint(100000000))), "sdk-upgrade-deposit", address), baseline[1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builder := ec.TxConfig.NewTxBuilder()
			require.NoError(t, builder.SetMsgs(tc.msg))
			builder.SetMemo("sdk-eip712-compatibility")
			builder.SetGasLimit(200000)
			builder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("rune", 100)))
			require.NoError(t, builder.SetSignatures(signing.SignatureV2{PubKey: key.PubKey(), Sequence: 456, Data: &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_DIRECT}}))
			data := authsigning.SignerData{Address: address.String(), ChainID: "thorchain-1", AccountNumber: 123, Sequence: 456, PubKey: key.PubKey()}
			signDoc, err := authsigning.GetSignBytesAdapter(context.Background(), ec.TxConfig.SignModeHandler(), signing.SignMode_SIGN_MODE_DIRECT, data, builder.GetTx())
			require.NoError(t, err)
			typedBytes, err := eip712.GetEIP712BytesForMsg(signDoc)
			require.NoError(t, err)
			ecdsaKey, err := crypto.ToECDSA(key.Key)
			require.NoError(t, err)
			baselineBytes, err := hex.DecodeString(tc.baselineTyped)
			require.NoError(t, err)
			signature, err := crypto.Sign(crypto.Keccak256(baselineBytes), ecdsaKey)
			require.NoError(t, err)
			require.True(t, key.PubKey().VerifySignature(signDoc, signature), "verify translated EIP-712 signature against original DIRECT SignDoc")
			require.Equal(t, baselineBytes, typedBytes, "preserve the baseline EIP-712 signing format")
			require.False(t, key.PubKey().VerifySignature(append(signDoc, 0), signature))
		})
	}
}
