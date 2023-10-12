//go:build mocknet
// +build mocknet

package keeperv1

import "github.com/blang/semver"

func GetTorAnchors(version semver.Version) []string {
	switch {
	case version.GTE(semver.MustParse("1.123.0")):
		return []string{
			"TORANCHOR-BNB-BUSD-BD1",
			"ETH.USDT-0XA3910454BF2CB59B8B3A401589A3BACC5CA42306",
		}
	default:
		return []string{}
	}
}
