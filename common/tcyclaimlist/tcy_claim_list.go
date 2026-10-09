package tcyclaimlist

import (
	"encoding/json"
)

type TCYClaimsJSON struct {
	Asset    string `json:"asset"`
	Address  string `json:"address"`
	TCYClaim uint64 `json:"tcy_claim"`
}

var tcyClaims []TCYClaimsJSON

func init() {
	tcyClaims = mustParseClaims(TCYClaimsListRaw)
}

// mustParseClaims decodes the embedded claims list, panicking on malformed data.
func mustParseClaims(raw []byte) []TCYClaimsJSON {
	var claims []TCYClaimsJSON
	if err := json.Unmarshal(raw, &claims); err != nil {
		panic(err)
	}
	return claims
}

func GetTCYClaimsList() []TCYClaimsJSON {
	return tcyClaims
}
