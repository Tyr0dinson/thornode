//go:build mocknet

package monero

const (
	defaultCoinbaseValue = 35000000000000

	// Temporarily over-reserve XMR vault/FROST outbound gas in mocknet so
	// churn migrations are not blocked by fee spikes before the full MaxGas
	// feedback/reschedule path lands.
	typicalVaultOutboundWeight = 10000
)
