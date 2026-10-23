package providers

// Each provider test connects to the mock websocket server; skip the real
// settle wait after subscribing (the tests still wait for the messages).
func init() {
	wsSettleDelay = 0
}
