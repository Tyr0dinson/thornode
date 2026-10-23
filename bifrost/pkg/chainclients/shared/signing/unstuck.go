package signing

import "gitlab.com/thorchain/thornode/v3/constants"

// UnstuckWindow measures a signed outbound's age in wall-clock time over the
// block time history, as the signer and THORChain do, so unstuck never clears
// the signer cache while the signer would still re-sign before a reschedule.
// signingPeriod is the period in blocks at blockTimeMs; bufferRefBlocks is the
// reschedule buffer in reference blocks. It reports whether the outbound is
// still waiting (younger than the period less the buffer since it was signed
// at signedHeight) and whether it is outside the buffer at the end of its
// current signing period (counted from txOutHeight). Without a block time
// change these are the block-count checks height - signedHeight < period -
// buffer and period - (height - txOutHeight) % period > buffer.
func UnstuckWindow(history constants.BlockTimeHistory, signedHeight, txOutHeight, height, signingPeriod, blockTimeMs, bufferRefBlocks int64) (waiting, outsideBuffer bool) {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	periodMs := signingPeriod * blockTimeMs
	bufferMs := constants.ScaleDuration(bufferRefBlocks, refMs, blockTimeMs) * blockTimeMs
	waiting = history.ElapsedMs(signedHeight, height, refMs) < periodMs-bufferMs
	if periodMs > 0 {
		outsideBuffer = periodMs-history.ElapsedMs(txOutHeight, height, refMs)%periodMs > bufferMs
	}
	return waiting, outsideBuffer
}
