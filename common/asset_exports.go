package common

func IsSupportedExportAsset(asset Asset) bool {
	native, err := NewAsset(asset.Symbol.String())
	if err != nil {
		return false
	}

	_, found := AssetExports[native]
	if !found {
		return false
	}

	if asset.Chain.Equals(THORChain) {
		return true
	}

	_, found = AssetExports[native][asset.Chain]
	return found
}
