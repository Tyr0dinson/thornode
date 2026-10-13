# Cosmos EVM signing compatibility

These five packages are copied unchanged from [Cosmos EVM v0.6.3](https://github.com/cosmos/evm/tree/v0.6.3), under the accompanying Apache 2.0 license. THORNode uses only these signing and codec packages; the EVM application and its dependency-test infrastructure are not included.

The upstream v0.6.3 module pulls SDK v0.53 application tests into `go mod tidy`, which cannot resolve under SDK v0.54. EVM v0.7.3 resolves that graph but changes THORChain DIRECT-to-EIP712 signing preimages. This local module preserves existing signatures and the `ethsecp256k1` key type URLs (`/cosmos.evm.crypto.v1.ethsecp256k1.*`) while avoiding that incompatible application dependency.

The two EIP-712 extension options do **not** keep the type URLs of the previous `cosmos/evm` v1.0.0-rc2 dependency. v0.6.3 moved them from `cosmos.evm.types.v1` to `cosmos.evm.eip712.v1.ExtensionOptionsWeb3Tx` and `cosmos.evm.ante.v1.ExtensionOptionDynamicFeeTx`, and no aliases are registered. After the upgrade a transaction carrying either option under the old URL fails to decode; critical extension options were already rejected by the ante handler. `TestCosmosEVMTypeURLs` pins these URLs.

Compatibility is checked by `go test ./app -run 'TestSDK|TestEip712Signing|TestMergedRegistry|TestCosmosEVMTypeURLs'` and the existing application test suite. Replace this module with upstream once upstream supports THORChain custom Amino encoding and the fixed pre-upgrade signature fixtures pass unchanged.
