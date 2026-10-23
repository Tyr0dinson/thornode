package blocktime

import (
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestGetSet(t *testing.T) {
	defer blockMs.Store(0)
	if got := Get(); got != constants.ThorchainBlockTime {
		t.Fatalf("before a read Get() = %s, want the default %s", got, constants.ThorchainBlockTime)
	}
	Set(500)
	if got := Get(); got != 500*time.Millisecond {
		t.Fatalf("Get() = %s, want 500ms", got)
	}
	Set(0)
	Set(-1)
	if got := Get(); got != 500*time.Millisecond {
		t.Fatalf("Get() = %s after invalid sets, want 500ms", got)
	}
}

// The ticker follows a block time change between ticks.
func TestTickerFollowsBlockTime(t *testing.T) {
	defer blockMs.Store(0)
	Set(40)
	ticker := NewTicker()
	defer ticker.Stop()
	start := time.Now()
	<-ticker.C
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Fatalf("first tick after %s, want at least 40ms", d)
	}
	Set(400)
	<-ticker.C // the timer armed before the change
	start = time.Now()
	<-ticker.C
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("tick after the change came after %s, want at least 400ms", d)
	}
	Set(20)
	<-ticker.C
	start = time.Now()
	<-ticker.C
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("tick after speeding up came after %s, want about 20ms", d)
	}
}
