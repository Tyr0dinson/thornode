// Package dag provides execution of an actor DAG (directed acyclic graph).
package dag

import (
	"bytes"
	"context"
	"fmt"
	rand "math/rand/v2"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// Execute executes the actor DAG from the provided root. It is precondition that the
// root actor points to a proper DAG and contains no cycles.
func Execute(c *OpConfig, root *Actor, parallelism int, rng *rand.Rand) {
	if err := execute(c, root, parallelism, rng); err != nil {
		log.Fatal().Err(err).Msg("actor execution failed")
	}
}

func execute(c *OpConfig, root *Actor, parallelism int, rng *rand.Rand) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var workers sync.WaitGroup
	// determine the total number of actors in dag
	seen := map[*Actor]bool{}
	root.WalkDepthFirst(func(a *Actor) bool {
		seen[a] = true
		return true
	})
	total := len(seen)
	// Each actor can report one terminal error, including during shutdown.
	failures := make(chan error, total)

	// initialize dag
	root.InitRoot()

	status := func() (ready, running, backgrounded, finished map[*Actor]bool) {
		// determine all actors that are ready to execute
		ready = map[*Actor]bool{}
		backgrounded = map[*Actor]bool{}
		finished = map[*Actor]bool{}
		running = map[*Actor]bool{}
		root.WalkDepthFirst(func(a *Actor) bool {
			if a.Finished() {
				finished[a] = true
				return true
			}
			if a.Backgrounded() {
				backgrounded[a] = true
				return true
			}
			if a.Started() {
				running[a] = true
				return true
			}

			// all parents must be finished or backgrounded to start
			for parent := range a.Parents() {
				if !parent.Finished() && !parent.Backgrounded() {
					return false
				}
			}

			ready[a] = true
			return true
		})

		return ready, running, backgrounded, finished
	}

	// execute dag
	log.Info().Int("actors", total).Int("parallelism", parallelism).Msg("executing dag")
	// Scheduler re-evaluation cadence. This only controls how often the DAG is
	// polled for ready actors/free slots; on-chain pacing is governed by each
	// actor's Interval, not this tick. Default 100ms (was 1s) to cut idle gap
	// between an actor finishing and the next ready actor being admitted.
	// Override with DAG_TICK_INTERVAL (e.g. "1s") for an instant rollback.
	tickInterval := 100 * time.Millisecond
	if v := os.Getenv("DAG_TICK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 10*time.Millisecond {
			tickInterval = d
		} else {
			// don't silently swallow a misconfigured override (e.g. "100" missing a
			// unit, or a below-floor "5ms") — log and fall back to the default.
			log.Warn().Str("DAG_TICK_INTERVAL", v).Dur("using", tickInterval).
				Msg("ignoring invalid DAG_TICK_INTERVAL (must parse as a duration >= 10ms)")
		}
	}
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()

	// Throttle the "waiting" log: only emit when the wait situation actually
	// changes, not on every tick. At a 100ms tick and low parallelism the
	// scheduler is almost always waiting, so logging every tick floods CI logs
	// (e.g. GitLab's 4MB trace cap). Key = the counts shown in that log line.
	lastWaitKey := [6]int{-1, -1, -1, -1, -1, -1}
	for {
		select {
		case err := <-failures:
			cancel()
			joinWorkers(&workers)
			return err
		case <-tick.C:
		}
		ready, running, backgrounded, finished := status()

		// if only finished/backgrounded actors remain, foreground work is done
		if len(running) == 0 && len(finished)+len(backgrounded) == total {
			cancel()
			joinWorkers(&workers)
			// Finished state may be visible before its error reaches the channel.
			// Collect errors returned during the grace period before declaring success.
			select {
			case err := <-failures:
				return err
			default:
			}
			log.Info().
				Int("finished", len(finished)).
				Int("backgrounded", len(backgrounded)).
				Msg("simulation finished successfully")
			return nil
		}

		// info log context
		infoLog := log.Info().
			Int("finished", len(finished)).
			Int("backgrounded", len(backgrounded)).
			Int("running", len(running)).
			Int("remaining", total-len(finished)-len(backgrounded)).
			Int("ready", len(ready))

		// determine how many users are available; unit tests may execute DAGs
		// without a user set, in which case user availability should not gate work.
		lockedUsers := 0
		availableUsers := len(ready)
		if c != nil && len(c.Users) > 0 {
			availableUsers = 0
			for _, user := range c.Users {
				if !user.IsLocked() {
					availableUsers++
				} else {
					lockedUsers++
				}
			}
		}

		// backgrounded actors should not consume a foreground execution slot.
		if len(ready) == 0 || availableUsers == 0 || len(running) >= parallelism {
			// only log when the wait situation changes (see lastWaitKey above),
			// so a fast tick at low parallelism doesn't flood the log
			waitKey := [6]int{len(finished), len(backgrounded), len(running), len(ready), availableUsers, lockedUsers}
			if waitKey != lastWaitKey {
				lastWaitKey = waitKey
				infoLog.
					Int("available_users", availableUsers).
					Int("locked_users", lockedUsers).
					Int("parallelism", parallelism).
					Msg("waiting for ready actors, users, and slots")
			} else {
				// not logging this tick: Discard() disables the event so the
				// following Send() writes nothing, and Send() runs the finalizer
				// (write -> putEvent) that returns the event to zerolog's pool.
				// Discard() alone does NOT pool it — in zerolog it just sets
				// level=Disabled and returns nil without recycling.
				infoLog.Discard()
				infoLog.Send()
			}

			continue
		}

		// randomly select an actor to execute
		random := rng.Int64N(int64(len(ready)))
		readySlice := make([]*Actor, 0, len(ready))
		for a := range ready {
			readySlice = append(readySlice, a)
		}
		sort.Slice(readySlice, func(ax_idx, ay_idx int) bool {
			return readySlice[ax_idx].Name < readySlice[ay_idx].Name
		})
		a := readySlice[random]

		// execute actor
		infoLog.Str("actor", a.Name).Msg("executing actor")
		a.Start()
		workers.Add(1)
		go func(a *Actor, start time.Time) {
			defer workers.Done()
			defer func() {
				duration := time.Since(start) / time.Second * time.Second // round to second
				a.Log().Info().Str("duration", duration.String()).Msg("finished")
			}()

			// tee the actor logs to a buffer that we dump if it fails
			buf := new(bytes.Buffer)
			teeWriter := zerolog.MultiLevelWriter(buf, os.Stdout)
			a.SetLogger(a.Log().Output(zerolog.ConsoleWriter{
				Out:        teeWriter,
				TimeFormat: time.TimeOnly,
			}))

			err := a.ExecuteContext(ctx, c)
			if err != nil {
				os.Stderr.Write([]byte("\n\nFailed actor logs:\n" + buf.String() + "\n\n"))

				// print currently running actors
				os.Stderr.Write([]byte("\n\nCurrently running actors:\n"))
				_, running, backgrounded, _ := status()
				for a := range running {
					os.Stderr.Write([]byte(a.Name + "\n"))
				}
				for a := range backgrounded {
					os.Stderr.Write([]byte(a.Name + "\n"))
				}
				os.Stderr.Write([]byte("\n\n"))

				failures <- fmt.Errorf("actor %s: %w", a.Name, err)
			}
		}(a, time.Now())
	}
}

// In-flight operations may not observe cancellation. Bound cleanup so a stuck
// RPC cannot indefinitely hide an actor failure or delay successful completion.
const shutdownGrace = 30 * time.Second

func joinWorkers(workers *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Warn().Dur("grace", shutdownGrace).Msg("actors still running at shutdown")
	}
}
