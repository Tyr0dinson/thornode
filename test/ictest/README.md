# THORNode Interchaintest Testing Framework

Interchaintest was initially develop to easily test IBC between any Cosmos-based chain (over 100 supported) using any supported relayer (GoRelayer, Hermes, or Hyperspace). It has expanded to support non-Cosmos chains (Ethereum, BSC, Penumbra, Polkadot, BTC, BCH, LTC, DOGE, and soon Avalanche).

The initial test case developed was based on the mocknet simulation test case. With the additions of utilizing the upgrade module, IBC, and Cosmwasm, this framework will natively help support development of those test cases.

Local-interchain has also extended interchaintest to allow quickly spinning up a testnet cluster and for tests to be written in Rust, Python, Bash, and Typescript/Javascript instead of Go.

## Setup

A local docker image of thornode must be built first using heighliner.

### Install heighliner
```bash
make get-heighliner
```
or
```bash
git clone https://github.com/strangelove-ventures/heighliner.git
cd heighliner && go install
```

### Build thornode docker image
```bash
make local-image
```

## Running Tests
```bash
make ictest-sim
```