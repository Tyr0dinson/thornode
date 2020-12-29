package types

func (r RagnarokWithdrawPosition) IsEmpty() bool {
	return r.Number < 0 || r.Pool.IsEmpty()
}
