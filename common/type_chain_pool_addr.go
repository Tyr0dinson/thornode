package common

import "fmt"

// ChainPoolInfo represent the pool address specific for a chain
type ChainPoolInfo struct {
	Chain       Chain   `json:"chain"`
	PubKey      PubKey  `json:"pub_key"`
	PoolAddress Address `json:"pool_address"`
	PubKeyEddsa PubKey  `json:"pub_key_eddsa,omitempty"`
}

// EmptyChainPoolInfo everything is empty
var EmptyChainPoolInfo ChainPoolInfo

// NewChainPoolInfo create a new instance of ChainPoolInfo
func NewChainPoolInfo(chain Chain, pubKey PubKey, pubKeyEddsa PubKey) (ChainPoolInfo, error) {
	if chain.IsEmpty() {
		return EmptyChainPoolInfo, fmt.Errorf("chain is empty")
	}
	if pubKey.IsEmpty() {
		return EmptyChainPoolInfo, fmt.Errorf("pubkey is empty")
	}
	var pubKeyForUse PubKey
	switch chain.GetSigningAlgo() {
	case SigningAlgoSecp256k1:
		pubKeyForUse = pubKey
	case SigningAlgoEd25519:
		pubKeyForUse = pubKeyEddsa
	default:
		return EmptyChainPoolInfo, fmt.Errorf("unsupported signing algo: %s", chain.GetSigningAlgo())
	}
	addr, err := pubKeyForUse.GetAddress(chain)
	if err != nil {
		return EmptyChainPoolInfo, fmt.Errorf("fail to get address for chain %s,%w", chain, err)
	}

	return ChainPoolInfo{
		Chain:       chain,
		PubKey:      pubKey,
		PoolAddress: addr,
		PubKeyEddsa: pubKeyEddsa,
	}, nil
}

// IsEmpty whether the struct is empty
func (cpi ChainPoolInfo) IsEmpty() bool {
	return cpi.Chain.IsEmpty() || cpi.PubKey.IsEmpty()
}
