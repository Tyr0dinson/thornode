package common

func IsSupportedExportAsset(asset Asset) bool {
	asset, err := NewAsset("THOR." + asset.Symbol.String())
	if err != nil {
		return false
	}

	_, found := AssetExports[asset]
	if !found {
		return false
	}

	if asset.Chain.Equals(THORChain) {
		return true
	}

	_, found = AssetExports[asset][asset.Chain]
	return found
}
