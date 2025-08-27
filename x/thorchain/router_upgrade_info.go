//go:build !mocknet && !stagenet
// +build !mocknet,!stagenet

package thorchain

var (
	// ETH Router
	ethOldRouter = `0xD37BbE5744D730a1d98d8DC97c42F0Ca46aD7146`
	ethNewRouter = `0xd5976E83F160B84BE90510b04C27657F240c7049`

	// AVAX Router
	avaxOldRouter = `0x8F66c4AE756BEbC49Ec8B81966DD8bba9f127549`
	avaxNewRouter = `0xd5976E83F160B84BE90510b04C27657F240c7049`

	// BSC Router
	bscOldRouter = `0xb30ec53f98ff5947ede720d32ac2da7e52a5f56b`
	bscNewRouter = `0xd5976E83F160B84BE90510b04C27657F240c7049`

	// BASE Router
	baseOldRouter = `0x68208d99746b805a1ae41421950a47b711e35681`
	baseNewRouter = `0xd5976E83F160B84BE90510b04C27657F240c7049`
)
