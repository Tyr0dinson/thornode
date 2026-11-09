package util

import (
	"time"

	"github.com/rs/zerolog/log"
)

////////////////////////////////////////////////////////////////////////////////////////
// Retry
////////////////////////////////////////////////////////////////////////////////////////

// sleep waits out a retry backoff; tests swap it to record backoffs instead of waiting.
var sleep = time.Sleep

func Retry(retries int, fn func() error) error {
	var err error
	backoff := time.Second
	for i := 0; i <= retries; i++ {
		err = fn()
		if err == nil {
			break
		}

		// break if this was the last retry
		if i == retries {
			break
		}

		// backoff and retry
		log.Err(err).
			Str("backoff", backoff.String()).
			Msgf("retrying %d/%d after backoff", i+1, retries)
		sleep(backoff)

		// exponential backoff, max 10 seconds
		backoff *= 2
		if backoff > time.Second*10 {
			backoff = time.Second * 10
		}
	}
	return err
}
