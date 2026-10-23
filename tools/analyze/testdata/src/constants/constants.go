package constants

type ConstantName int

const (
	ChurnInterval ConstantName = iota
	DesiredValidatorSet
	TxOutDelayRate
)

func (c ConstantName) String() string {
	return [...]string{"ChurnInterval", "DesiredValidatorSet", "TxOutDelayRate"}[c]
}
