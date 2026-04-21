//go:build !stagenet
// +build !stagenet

package thorchain

// defaultTCYClaimers returns the list of TCYClaimers seeded into
// DefaultGenesisState. On non-stagenet networks these are delivered via
// the Migrate4to5 path at the configured upgrade height; returning nil
// keeps that flow intact.
func defaultTCYClaimers() []TCYClaimer {
	return nil
}
