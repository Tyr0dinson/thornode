package observer

import (
	"testing"
	"time"
)

// SetSubscribeRetryBackoff shortens the EventClient resubscribe backoff for one test.
func SetSubscribeRetryBackoff(t *testing.T, d time.Duration) {
	old := subscribeRetryBackoff
	subscribeRetryBackoff = d
	t.Cleanup(func() { subscribeRetryBackoff = old })
}
