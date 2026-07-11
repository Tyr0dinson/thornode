package types

import "fmt"

var (
	ErrUnavailableBlock        = fmt.Errorf("block is not yet available")
	ErrFailOutputMatchCriteria = fmt.Errorf("fail to get output matching criteria")
)

// RewindRequiredError tells BlockScanner that a chain-scanner cursor moved
// backwards and the next fetch must start at NextHeight. It is deliberately a
// typed control-flow error so arbitrary RPC failures cannot move the durable
// block-scanner cursor.
type RewindRequiredError struct {
	NextHeight int64
}

func (e RewindRequiredError) Error() string {
	return fmt.Sprintf("block scanner rewind required: next height %d", e.NextHeight)
}
