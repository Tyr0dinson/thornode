package evm

import _ "embed"

// Files used by test/ictest

//go:embed eth-router-abi.json
var EthRouterAbi []byte

//go:embed eth-router-bytecode.txt
var EthRouterByteCode []byte

//go:embed router-abi.json
var RouterAbi []byte

//go:embed router-bytecode.txt
var RouterByteCode []byte
