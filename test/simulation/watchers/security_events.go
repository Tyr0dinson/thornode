package watchers

import (
	"fmt"
	"time"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
)

func NewSecurityEvents() *Watcher {
	thorscan.SetAPIEndpoint(thornodeURL)
	// Establish the current tip as a baseline, then watch subsequent blocks.
	// Starting at genesis makes a no-reset profile replay old security events;
	// starting at (and processing) the current tip can replay an event from the
	// immediately preceding run. Actors have not started when watchers are
	// constructed, so discarding this one baseline block cannot miss a run event.
	scanner := thorscan.Scan(0, 0)
	<-scanner

	return &Watcher{
		Name:     "SecurityEvents",
		Interval: time.Second,
		Fn: func(config *OpConfig) error {
			for {
				select {
				case block := <-scanner:
					events := block.FinalizeBlockEvents
					events = append(events, block.BeginBlockEvents...)
					events = append(events, block.EndBlockEvents...)
					for _, tx := range block.Txs {
						events = append(events, tx.Result.Events...)
					}
					for _, event := range events {
						if err := checkSecurityEvent(config, block.Header.Height, event); err != nil {
							return err
						}
					}
				case <-time.After(10 * time.Millisecond):
					return nil
				}
			}
		},
	}
}

func checkSecurityEvent(config *OpConfig, height int64, event map[string]string) error {
	if event["type"] != "security" {
		return nil
	}
	if config.ConsumeExpectedSecurityEvent(event["id"], event["msg"]) {
		return nil
	}
	return fmt.Errorf("security event detected at height %d: %v", height, event)
}
