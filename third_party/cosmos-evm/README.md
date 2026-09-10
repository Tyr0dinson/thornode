# Cosmos EVM signing compatibility

These five packages are copied unchanged from [Cosmos EVM v0.6.3](https://github.com/cosmos/evm/tree/v0.6.3), under the accompanying Apache 2.0 license. THORNode uses only these signing and codec packages; the EVM application and its dependency-test infrastructure are not included.

The upstream v0.6.3 module pulls SDK v0.53 application tests into `go mod tidy`, which cannot resolve under SDK v0.54. EVM v0.7.3 resolves that graph but changes THORChain DIRECT-to-EIP712 signing preimages. This local module preserves existing signatures and protobuf type identities while avoiding that incompatible application dependency.

Compatibility is checked by `go test ./app -run 'TestSDK|TestEip712Signing|TestMergedRegistry'` and the existing application test suite. Replace this module with upstream once upstream supports THORChain custom Amino encoding and the fixed pre-upgrade signature fixtures pass unchanged.
