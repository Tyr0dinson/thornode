//go:build stagenet
// +build stagenet

package bsc

import (
	_ "embed"
)

//go:embed token_list_stagenet.json
var tokenList []byte
