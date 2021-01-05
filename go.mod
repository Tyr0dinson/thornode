module gitlab.com/thorchain/thornode

go 1.14

require (
	github.com/99designs/keyring v1.1.6
	github.com/binance-chain/go-sdk v1.2.2
	github.com/binance-chain/ledger-cosmos-go v0.9.9 // indirect
	github.com/binance-chain/tss-lib v1.3.2
	github.com/blang/semver v3.5.1+incompatible
	github.com/btcsuite/btcd v0.21.0-beta
	github.com/btcsuite/btcutil v1.0.2
	github.com/cenkalti/backoff v2.2.1+incompatible
	github.com/cosmos/cosmos-sdk v0.40.0-rc5
	github.com/cosmos/go-bip39 v1.0.0
	github.com/decred/dcrd/dcrec/edwards v1.0.0
	github.com/didip/tollbooth v4.0.2+incompatible
	github.com/ethereum/go-ethereum v1.9.12
	github.com/gogo/protobuf v1.3.1
	github.com/google/go-cmp v0.5.1 // indirect
	github.com/gorilla/mux v1.8.0
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/hashicorp/go-multierror v1.1.0
	github.com/hashicorp/go-retryablehttp v0.6.4
	github.com/ipfs/go-log v1.0.4
	github.com/multiformats/go-multiaddr v0.3.1
	github.com/patrickmn/go-cache v2.1.0+incompatible // indirect
	github.com/prometheus/client_golang v1.8.0
	github.com/rakyll/statik v0.1.7
	github.com/rjeczalik/notify v0.9.2 // indirect
	github.com/rs/zerolog v1.20.0
	github.com/spf13/cast v1.3.1
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/spf13/viper v1.7.1
	github.com/syndtr/goleveldb v1.0.1-0.20200815110645-5c35d600f0ca
	github.com/tendermint/btcd v0.1.1
	github.com/tendermint/go-amino v0.16.0
	github.com/tendermint/tendermint v0.34.0
	github.com/tendermint/tm-db v0.6.3
	gitlab.com/thorchain/binance-sdk v1.2.3-0.20210102180356-7b2f8924ed23
	gitlab.com/thorchain/tss/go-tss v1.2.8-0.20210102180906-1dbf1355dba6
	gitlab.com/thorchain/txscript v0.0.0-20200413023754-8aaf3443d92b
	golang.org/x/tools v0.0.0-20200731060945-b5fad4ed8dd6 // indirect
	gopkg.in/check.v1 v1.0.0-20200902074654-038fdea0a05b
	gopkg.in/ini.v1 v1.52.0 // indirect
)

replace (
	github.com/agl/ed25519 => github.com/binance-chain/edwards25519 v0.0.0-20200305024217-f36fc4b53d43
	github.com/binance-chain/tss-lib => gitlab.com/thorchain/tss/tss-lib v0.0.0-20201118045712-70b2cb4bf916
	github.com/gogo/protobuf => github.com/regen-network/protobuf v1.3.2-alpha.regen.4
	github.com/tendermint/go-amino => github.com/binance-chain/bnc-go-amino v0.14.1-binance.1
	github.com/zondax/ledger-go => github.com/binance-chain/ledger-go v0.9.1
)
