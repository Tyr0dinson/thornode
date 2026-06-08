//go:build !mocknet && !testnet

package monero

const (
	defaultCoinbaseValue = 600000000000 // ~0.6 XMR tail emission in piconero

	// typicalVaultOutboundWeight is the conservative vault/FROST outbound tx
	// weight in Monero weight units used for THORChain network fee reporting.
	typicalVaultOutboundWeight = 2500
)
