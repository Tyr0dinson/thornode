package unscaled

import "constants"

type consts struct{}

func (consts) GetInt64Value(constants.ConstantName) int64 { return 0 }

type keeper struct{}

func (keeper) GetMimir(_ int, _ string) (int64, error)                         { return 0, nil }
func (keeper) ScaleConfigInt64(_ int, _ constants.ConstantName, v int64) int64 { return v }

func unscaledConstant(c consts) int64 {
	return c.GetInt64Value(constants.ChurnInterval) // want "unscaled read of ChurnInterval"
}

func unscaledMimir(k keeper) int64 {
	v, _ := k.GetMimir(0, constants.TxOutDelayRate.String()) // want "unscaled read of TxOutDelayRate"
	return v
}

func unscaledLiteral(k keeper) int64 {
	v, _ := k.GetMimir(0, "CHURNINTERVAL") // want "unscaled read of CHURNINTERVAL"
	return v
}

func count(c consts, k keeper) int64 {
	v, _ := k.GetMimir(0, constants.DesiredValidatorSet.String())
	return v + c.GetInt64Value(constants.DesiredValidatorSet)
}

func scaled(c consts, k keeper) int64 {
	v, _ := k.GetMimir(0, constants.ChurnInterval.String())
	if v < 0 {
		v = c.GetInt64Value(constants.ChurnInterval)
	}
	return k.ScaleConfigInt64(0, constants.ChurnInterval, v)
}

func ignored(c consts) int64 {
	// analyze-ignore(unscaled-config)
	return c.GetInt64Value(constants.ChurnInterval)
}
