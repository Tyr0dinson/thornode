package endpoints

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type probeResult struct {
	height   int64
	latency  time.Duration
	err      error
	identity string
	idErr    error
}

var errSkipped = errors.New("skipped")

// ErrIdentityMismatch is returned (wrapped) by a probe when it has positively
// established that the endpoint, or one of its hosts, serves another network than
// the one it is verified as or configured for. The manager disables such an
// endpoint permanently instead of treating the error as a liveness failure; for
// the priority-1 endpoint with expected_identity set it fails startup.
var ErrIdentityMismatch = errors.New("endpoint serves another network")

// identityMismatch reports whether a probe result proved a network mismatch.
func identityMismatch(r probeResult) bool {
	return errors.Is(r.err, ErrIdentityMismatch) || errors.Is(r.idErr, ErrIdentityMismatch)
}

// mismatchReason is the one-line reason of a proven network mismatch.
func mismatchReason(r probeResult) string {
	if errors.Is(r.err, ErrIdentityMismatch) {
		return r.err.Error()
	}
	return r.idErr.Error()
}

// Bootstrap probes every endpoint once (height + identity, concurrently, bounded by
// health_check_timeout), establishes the reference identity, and selects the
// highest-priority healthy endpoint as active.
//
// Reference identity, in order: the configured expected identity; the priority-1
// endpoint's verified identity; the identity shared by a majority of verified
// endpoints; the single verified identity. The priority-1 endpoint disagreeing with
// a configured expected identity is fatal. Any other disagreeing endpoint is
// disabled. Unreachable or unverified endpoints start unhealthy (when verification
// is required) and are verified by the health loop. Nothing reachable is logged, not
// fatal: the routers keep trying every endpoint.
func (m *Manager) Bootstrap(ctx context.Context, probes []Probe) error {
	if err := m.bindProbes(probes); err != nil {
		return err
	}
	results := m.probeAll(ctx, func(*Entry) bool { return true }, true)

	verified := make(map[string]string, len(m.entries))
	for i, e := range m.entries {
		r := results[i]
		if identityMismatch(r) {
			reason := mismatchReason(r)
			if i == 0 && m.expectedIdentity != "" {
				return fmt.Errorf("chain %s: priority-1 endpoint %s: %s; check the endpoint is on the right network", m.chain, e.Name, reason)
			}
			m.log.Error().Str("endpoint", e.Name).Str("reason", reason).Msg("endpoint proved to serve another network; disabling it permanently")
			e.Disable(reason)
			continue
		}
		switch {
		case r.err != nil && errors.Is(r.err, ErrRateLimited):
			m.log.Warn().Str("endpoint", e.Name).Msg("endpoint rate limited at startup; cooling down")
			e.RecordRateLimited(m.cfg.RateLimitBackoff)
		case r.err != nil:
			m.log.Warn().Err(r.err).Str("endpoint", e.Name).Msg("endpoint unreachable at startup")
			e.MarkUnhealthy(ErrorKindProbe)
		default:
			e.RecordProbe(r.height, r.latency)
		}
		if r.idErr == nil && r.identity != "" {
			verified[e.Name] = r.identity
		} else if r.err == nil {
			m.log.Warn().AnErr("err", r.idErr).Str("endpoint", e.Name).Msg("endpoint identity unavailable; will verify later")
		}
	}

	ref := m.expectedIdentity
	if ref == "" {
		if id, ok := verified[m.entries[0].Name]; ok {
			ref = id
		} else {
			ref = majorityIdentity(verified)
		}
	}
	if ref == "" && len(verified) > 0 {
		m.log.Warn().Int("verified", len(verified)).
			Msg("no reference chain identity yet: the priority-1 endpoint has not answered and no two endpoints agree; " +
				"endpoints serve but none can be disabled for a mismatch until one does, or set expected_identity to pin it")
	}
	m.setReference(ref)

	for _, e := range m.entries {
		id, ok := verified[e.Name]
		if !ok {
			// no identity at all: unverified, and out of rotation when verification
			// is required, until the health loop obtains one
			if e.identityRequiredValue() {
				e.MarkUnhealthy(ErrorKindProbe)
			}
			continue
		}
		// record what the endpoint claims even when there is no reference yet: it is
		// compared as soon as one exists, and until then the endpoint can still serve
		// (that is the behavior that predates endpoint failover)
		e.SetIdentity(id)
	}
	if ref != "" && m.expectedIdentity != "" {
		if id, ok := verified[m.entries[0].Name]; ok && id != ref {
			return fmt.Errorf("chain %s: priority-1 endpoint %s reports identity %q but expected_identity is %q; check the endpoint is on the right network",
				m.chain, m.entries[0].Name, id, ref)
		}
	}
	m.disableIdentityMismatches(ref)
	if m.AllDown() {
		m.log.Error().Msg("no healthy endpoint at startup; continuing and retrying in the background")
	}
	m.reselect("startup")
	m.publishMetrics()
	return nil
}

// disableIdentityMismatches permanently disables every endpoint whose recorded
// identity differs from the reference. It is a no-op until a reference exists, so
// an endpoint that answered before one was established is compared as soon as it is.
func (m *Manager) disableIdentityMismatches(ref string) {
	if ref == "" {
		return
	}
	for _, e := range m.entries {
		id := e.Identity()
		if id == "" || id == ref || e.State() == StateDisabled {
			continue
		}
		m.log.Error().Str("endpoint", e.Name).Str("expected", ref).Str("got", id).
			Msg("endpoint reports a different chain identity; disabling it permanently")
		m.Disable(e.Name, fmt.Sprintf("identity %q != %q", id, ref))
	}
}

// majorityIdentity returns the identity held by a strict majority of verified
// endpoints, or "" when there is none. At least two endpoints must agree: a single
// reachable endpoint is not a majority, and letting it latch the reference would
// permanently disable the priority-1 endpoint when it comes back on the right
// network.
func majorityIdentity(verified map[string]string) string {
	if len(verified) < 2 {
		return ""
	}
	counts := map[string]int{}
	for _, id := range verified {
		counts[id]++
	}
	for id, n := range counts {
		if n*2 > len(verified) {
			return id
		}
	}
	return ""
}

func (m *Manager) setReference(id string) {
	m.identityMu.Lock()
	defer m.identityMu.Unlock()
	m.identity = id
}

// ReferenceIdentity returns the identity every endpoint must match ("" until known).
func (m *Manager) ReferenceIdentity() string {
	m.identityMu.Lock()
	defer m.identityMu.Unlock()
	return m.identity
}

func (e *Entry) identityRequiredValue() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.identityRequired
}

// SetScannerHeight installs the block scanner's last processed height as a lag
// baseline. It is only consulted by single-endpoint managers. May be nil.
func (m *Manager) SetScannerHeight(fn func() int64) {
	if m == nil {
		return
	}
	m.scannerHeight = fn
}

// SetReferenceHeight installs THORChain's last observed height for the chain as a
// lag reference for managers with two or more endpoints. It is set only after 2/3 of
// nodes agree and never goes back, so no single endpoint (or node) can move it: an
// endpoint more than max_block_lag below it is behind what the network has already
// observed. This breaks the tie two endpoints cannot (two heights that disagree form
// no quorum tip). Must be called before Start.
func (m *Manager) SetReferenceHeight(fn func() (int64, error)) {
	if m == nil {
		return
	}
	m.referenceHeight = fn
}

// startReference starts reading THORChain's observed height for this cycle and
// returns a function that waits for it. The read runs while the endpoints are probed,
// so a slow thornode and slow probes cost a cycle the longer of the two, not their
// sum, and the integrity check after them keeps its share of the cycle. The answer is
// 0 when there is none to use: a single endpoint, no reference set, an error, or no
// answer by the time both the probes are done and health_check_timeout has passed
// since the start (a slow thornode must not hold up the health loop). A read that has not answered yet is not started again, so a
// hanging thornode leaves one waiting goroutine, not one per cycle.
func (m *Manager) startReference(ctx context.Context) func() int64 {
	none := func() int64 { return 0 }
	if m.referenceHeight == nil || len(m.entries) < 2 {
		return none
	}
	if !m.referenceBusy.CompareAndSwap(false, true) {
		return none
	}
	answer := make(chan int64, 1)
	go func() {
		defer m.referenceBusy.Store(false)
		h, err := m.referenceHeight()
		if err != nil {
			h = 0
		}
		answer <- h
	}()
	timer := time.NewTimer(m.cfg.HealthCheckTimeout)
	return func() int64 {
		defer timer.Stop()
		// an answer that came while the endpoints were probed wins over the timer, which
		// a probe that ran to its own timeout has let fire meanwhile
		select {
		case h := <-answer:
			return h
		default:
		}
		select {
		case h := <-answer:
			return h
		case <-timer.C:
			return 0
		case <-ctx.Done():
			return 0
		}
	}
}

func (m *Manager) bindProbes(probes []Probe) error {
	if len(probes) != len(m.entries) {
		return fmt.Errorf("chain %s: %d probes for %d endpoints", m.chain, len(probes), len(m.entries))
	}
	for i, p := range probes {
		if p.Name() != m.entries[i].Name {
			return fmt.Errorf("chain %s: probe %d (%s) does not match endpoint %s", m.chain, i, p.Name(), m.entries[i].Name)
		}
	}
	m.probes = probes
	return nil
}

// Start launches the background loop. Idempotent. Bootstrap must run first.
func (m *Manager) Start() {
	if m == nil {
		return
	}
	if m.probes == nil {
		m.log.Error().Msg("Start called before Bootstrap; health loop disabled")
		return
	}
	m.startOnce.Do(func() {
		m.wg.Add(1)
		go m.run()
	})
}

func (m *Manager) run() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.cfg.HealthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.loopCtx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(m.loopCtx, m.cfg.HealthCheckInterval)
			m.Cycle(ctx)
			cancel()
		}
	}
}

// probeAll probes the selected entries concurrently. withIdentity also fetches the
// identity for entries that are not yet verified.
func (m *Manager) probeAll(ctx context.Context, selected func(*Entry) bool, withIdentity bool) []probeResult {
	results := make([]probeResult, len(m.entries))
	var wg sync.WaitGroup
	for i, e := range m.entries {
		if !selected(e) {
			results[i].err = errSkipped
			continue
		}
		wg.Add(1)
		go func(i int, e *Entry, p Probe) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, m.cfg.HealthCheckTimeout)
			defer cancel()
			start := m.now()
			h, err := p.Height(pctx)
			results[i].latency = m.now().Sub(start)
			if err != nil {
				results[i].err = err
			} else {
				results[i].height = h
			}
			// identity is checked independently of liveness: an endpoint that proves
			// its network but cannot serve heights is verified and unhealthy, not
			// unverified
			if withIdentity && !e.IdentityVerified() && pctx.Err() == nil {
				results[i].identity, results[i].idErr = p.Identity(pctx)
			}
		}(i, e, m.probes[i])
	}
	wg.Wait()
	return results
}

// Cycle runs one health-check round. It is exported so tests can drive it
// deterministically; the background loop calls it every health_check_interval.
func (m *Manager) Cycle(ctx context.Context) {
	if m.probes == nil {
		m.reselect("health cycle")
		return
	}
	selected := func(e *Entry) bool {
		switch e.State() {
		case StateDisabled:
			return false
		case StateHealthy:
			return !e.InCooldown() // a rate-limited endpoint is left alone until its cooldown ends
		default:
			return e.RecoveryDue(m.cfg.RecoveryCheckInterval)
		}
	}
	waitReference := m.startReference(ctx)
	results := m.probeAll(ctx, selected, true)

	// 1. identity verification for endpoints that were unverified. Without a
	// reference, one is established only from the priority-1 endpoint or from a
	// strict majority of every identity known so far; never from the first
	// endpoint that happens to answer.
	for i, e := range m.entries {
		r := results[i]
		if errors.Is(r.err, errSkipped) || e.State() == StateDisabled {
			continue
		}
		if identityMismatch(r) {
			reason := mismatchReason(r)
			m.log.Error().Str("endpoint", e.Name).Str("reason", reason).Msg("endpoint proved to serve another network; disabling it permanently")
			m.Disable(e.Name, reason)
			continue
		}
		if e.IdentityVerified() || r.idErr != nil || r.identity == "" {
			continue
		}
		e.SetIdentity(r.identity)
	}
	ref := m.ReferenceIdentity()
	if ref == "" {
		known := make(map[string]string, len(m.entries))
		for _, e := range m.entries {
			if id := e.Identity(); id != "" {
				known[e.Name] = id
			}
		}
		if id, ok := known[m.entries[0].Name]; ok && m.entries[0].State() != StateDisabled {
			ref = id
		} else {
			ref = majorityIdentity(known)
		}
		if ref != "" {
			m.setReference(ref)
			m.log.Info().Str("identity", ref).Msg("reference chain identity established")
		}
	}
	m.disableIdentityMismatches(ref)

	// 2. record probe data
	heights := make(map[string]int64, len(m.entries))
	for i, e := range m.entries {
		r := results[i]
		if r.err != nil {
			continue
		}
		e.RecordProbe(r.height, r.latency)
		// only an endpoint that may serve votes on the tip: one disabled this cycle,
		// or one whose required network is not verified, must not make the
		// endpoints that are in use look behind
		if e.Usable() {
			heights[e.Name] = r.height
		}
		if m.metrics != nil {
			m.metrics.probeLatency.WithLabelValues(m.chain.String(), e.Name).Set(r.latency.Seconds())
		}
	}

	// 3. consensus tip (quorum of two within max_block_lag); the scanner baseline is
	// only meaningful with a single endpoint (with several, the scanner reads from
	// the active endpoint and would let it evict every other endpoint)
	// An endpoint quarantined for a minority block hash is on another fork: its height
	// neither sets the tip the others are judged against nor counts as reaching
	// THORChain's height. It still pairs with a healthy endpoint for the integrity
	// check when the others form no tip, which is how one healthy endpoint clears it.
	tipHeights := make(map[string]int64, len(heights))
	for name, h := range heights {
		if !m.byName[name].IntegrityEvicted() {
			tipHeights[name] = h
		}
	}
	tip, quorum := consensusTip(tipHeights, m.cfg.MaxBlockLag)
	// the hashes are compared at the level of the endpoints the tip was formed from
	// (their lower median), not below the tip: endpoints on another fork that report
	// heights ahead must not leave the others out of the vote
	checkFrom, checkQuorum := tipHeights, quorum
	if !quorum {
		_, checkQuorum = consensusTip(heights, m.cfg.MaxBlockLag)
		checkFrom = heights
	}
	// the median is of the healthy ones among them: endpoints far behind that are
	// probed while they recover must not pull the compared height down to theirs
	medianFrom := make(map[string]int64, len(checkFrom))
	for name, h := range checkFrom {
		if m.byName[name].State() == StateHealthy {
			medianFrom[name] = h
		}
	}
	if len(medianFrom) == 0 {
		medianFrom = checkFrom
	}
	checkHeight := lowerMedian(medianFrom) - m.cfg.IntegrityCheckDepth
	var scanner int64
	if m.scannerHeight != nil && len(m.entries) == 1 {
		scanner = m.scannerHeight()
	}
	observed := waitReference()

	// 4. integrity: majority hash at that height among healthy hash-capable endpoints;
	// endpoints evicted for a minority hash must positively match it before they may
	// recover (matchedHash), a check that merely ran or returned nothing is not enough
	badHash, matchedHash, vote := map[string]bool{}, map[string]bool{}, voteNone
	if checkQuorum && m.cfg.IntegrityCheckDepth > 0 && checkHeight > 0 {
		badHash, matchedHash, vote = m.integrityCheck(ctx, checkHeight, heights)
	}
	// THORChain's height counts as reached by any endpoint not shown to be on another
	// fork (not quarantined, no minority hash this cycle), including one whose hash
	// could not be read: the height is the network's own, so an endpoint that reaches
	// it cannot make the others look behind it.
	reachedBy := make(map[string]int64, len(tipHeights))
	for name, h := range tipHeights {
		if !badHash[name] {
			reachedBy[name] = h
		}
	}
	// once two or more healthy endpoints have voted, an endpoint may set the lag tip
	// only if it showed the majority hash: one with a minority hash is on another
	// fork, and one whose hash could not be read, or that was not compared, has not
	// shown it is not. A vote with no majority (a split, or too few answers) shows no
	// fork agreement at all, so the tip strikes no healthy endpoint this cycle
	// (THORChain's height still does); it still holds back the recovery of an endpoint
	// behind it.
	split := vote == voteInconclusive
	if vote == voteMajority {
		for name := range tipHeights {
			if !matchedHash[name] {
				delete(tipHeights, name)
			}
		}
		tip, quorum = consensusTip(tipHeights, m.cfg.MaxBlockLag)
	}

	// trust THORChain's height only once such an endpoint has reached it (within
	// max_block_lag): a height no endpoint shows (forced on THORChain, or every
	// endpoint behind) must not strike all of them and take the chain down
	if observed > 0 {
		reached := false
		for _, h := range reachedBy {
			if h+m.cfg.MaxBlockLag >= observed {
				reached = true
			}
		}
		if !reached {
			observed = 0
		}
	}
	// lagging judges a height; healthy says whether it is a healthy endpoint's strike
	// (suspended against the tip during a split vote) or a recovery check
	lagging := func(h int64, healthy bool) (bool, string) {
		if quorum && !(split && healthy) && tip-h > m.cfg.MaxBlockLag {
			return true, fmt.Sprintf("behind consensus tip %d by %d blocks", tip, tip-h)
		}
		if observed > 0 && observed-h > m.cfg.MaxBlockLag {
			return true, fmt.Sprintf("behind THORChain's observed height %d by %d blocks", observed, observed-h)
		}
		if scanner > 0 && scanner-h > m.cfg.MaxBlockLag {
			return true, fmt.Sprintf("behind scanner height %d by %d blocks", scanner, scanner-h)
		}
		return false, ""
	}

	// 5. apply results per entry
	for i, e := range m.entries {
		r := results[i]
		if errors.Is(r.err, errSkipped) || e.State() == StateDisabled {
			continue
		}
		if r.err != nil && errors.Is(r.err, ErrRateLimited) {
			// a rate limit is a cooldown, never a strike or a failed recovery check
			m.RecordRateLimited(e.Name)
			continue
		}
		switch e.State() {
		case StateHealthy:
			if r.err != nil {
				m.log.Warn().Err(r.err).Str("endpoint", e.Name).Msg("health probe failed")
				m.recordProbeStrike(e, ErrorKindProbe, "probe failed")
				continue
			}
			// a hash disagreement outranks lag: an endpoint that is both behind and on
			// another fork must be quarantined, not merely struck for lag
			if badHash[e.Name] {
				m.recordProbeStrike(e, ErrorKindIntegrity, "minority block hash")
				continue
			}
			if lag, why := lagging(r.height, true); lag {
				m.log.Warn().Str("endpoint", e.Name).Int64("height", r.height).Msg("endpoint lagging: " + why)
				m.recordProbeStrike(e, ErrorKindLag, why)
				continue
			}
			e.RecordProbePass()
		case StateUnhealthy, StateRecovering:
			ok := r.err == nil
			if badHash[e.Name] {
				// a minority hash found this cycle quarantines the endpoint however it
				// left the healthy state meanwhile (inline errors while the vote ran)
				if !e.IntegrityEvicted() {
					m.log.Warn().Str("endpoint", e.Name).Msg("minority block hash: endpoint quarantined")
					e.MarkUnhealthy(ErrorKindIntegrity)
				}
				ok = false
			}
			if ok {
				if lag, _ := lagging(r.height, false); lag {
					ok = false
				}
			}
			if ok && e.IntegrityEvicted() && m.cfg.IntegrityCheckDepth > 0 {
				// evicted for a minority hash: only a hash that matched the healthy
				// reference this cycle clears the quarantine; a failed or empty hash
				// fetch, a missing quorum, or a tie among the healthy endpoints does not
				ok = matchedHash[e.Name]
			}
			from, to := e.RecordHealthCheck(ok, m.cfg)
			m.logTransition(e, from, to, "health probe")
		}
	}

	// 6. failover / failback
	if !m.reselect("health cycle") {
		m.MaybeFailback()
	}
	m.publishMetrics()
}

// lowerMedian returns the lower median of heights, 0 for none.
func lowerMedian(heights map[string]int64) int64 {
	if len(heights) == 0 {
		return 0
	}
	hs := make([]int64, 0, len(heights))
	for _, h := range heights {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] < hs[j] })
	return hs[(len(hs)-1)/2]
}

// consensusTip returns the greatest height that at least one other endpoint is
// within lag of. quorum is false when no two endpoints agree, in which case tip-based
// eviction is disabled (a single reporter can never evict the others).
func consensusTip(heights map[string]int64, lag int64) (tip int64, quorum bool) {
	hs := make([]int64, 0, len(heights))
	for _, h := range heights {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] > hs[j] })
	for i, h := range hs {
		for j, other := range hs {
			if i != j && h-other <= lag && h-other >= 0 {
				return h, true
			}
		}
	}
	return 0, false
}

// hashVote is the outcome of one integrity check among the healthy endpoints.
type hashVote int

const (
	// voteNone: fewer than two healthy endpoints were asked, so nothing was voted on
	voteNone hashVote = iota
	// voteMajority: one hash holds a strict majority of two or more healthy answers
	voteMajority
	// voteInconclusive: two or more healthy endpoints were asked and no hash holds a
	// strict majority of two or more answers (a tie, a plurality, or fewer than two
	// answers because hash reads failed): no fork agreement was shown
	voteInconclusive
)

// integrityCheck fetches the block hash at height from every healthy endpoint that
// supports it (and from non-healthy endpoints that were evicted for a minority
// hash, so they can be re-checked). It returns the set of endpoints whose hash
// differs from the reference (bad), the set whose hash positively matched it
// (matched), and the vote's outcome; an endpoint whose hash could not be fetched is
// in neither set. The reference is the hash held by a strict majority of the healthy
// endpoints that answered, or the single healthy endpoint's hash. Without a strict
// majority (a tie, or a plurality such as H, H, X, Y) nothing is evicted or cleared,
// and the disagreement is logged; an integrity-evicted endpoint with no healthy
// endpoint to compare against is reported as bad, since it cannot be cleared.
func (m *Manager) integrityCheck(ctx context.Context, height int64, heights map[string]int64) (bad, matched map[string]bool, vote hashVote) {
	type res struct {
		name    string
		hash    string
		healthy bool
	}
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		results     []res
		rateLimited []string
		// healthyAsked counts the healthy endpoints asked for their hash
		healthyAsked int
	)
	for i, e := range m.entries {
		hp, ok := m.probes[i].(HashProbe)
		if !ok {
			continue
		}
		st := e.State()
		healthy := st == StateHealthy
		recheck := (st == StateUnhealthy || st == StateRecovering) && e.IntegrityEvicted()
		if !healthy && !recheck {
			continue
		}
		if h, seen := heights[e.Name]; !seen || h < height {
			continue // endpoint has not reached that height yet; not evidence of anything
		}
		if healthy {
			healthyAsked++
		}
		wg.Add(1)
		go func(e *Entry, hp HashProbe, healthy bool) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, m.cfg.HealthCheckTimeout)
			defer cancel()
			h, err := hp.HashAt(pctx, height)
			if errors.Is(err, ErrRateLimited) {
				mu.Lock()
				rateLimited = append(rateLimited, e.Name)
				mu.Unlock()
				return
			}
			if err != nil || h == "" {
				return // a failed hash fetch is not an integrity signal
			}
			mu.Lock()
			results = append(results, res{e.Name, strings.ToLower(h), healthy})
			mu.Unlock()
		}(e, hp, healthy)
	}
	wg.Wait()
	// a rate-limited hash probe is a cooldown, as a rate-limited height probe is
	// (these probes bypass the routers, which would otherwise record it)
	sort.Strings(rateLimited)
	for _, name := range rateLimited {
		m.RecordRateLimited(name)
	}
	counts := map[string]int{}
	healthyN := 0
	for _, r := range results {
		if r.healthy {
			counts[r.hash]++
			healthyN++
		}
	}
	bad, matched = map[string]bool{}, map[string]bool{}
	// fewer than two healthy answers make no majority (a single one is still the
	// reference for re-checks); when two or more were asked, the missing answers leave
	// fork agreement unshown
	unanswered := voteNone
	if healthyAsked >= 2 {
		unanswered = voteInconclusive
	}
	if healthyN == 0 {
		// nothing to compare against: integrity-evicted endpoints stay evicted
		for _, r := range results {
			bad[r.name] = true
		}
		return bad, matched, unanswered
	}
	if healthyN < 2 {
		// a single healthy endpoint is the reference for re-checks but cannot be
		// outvoted; only compare the non-healthy candidates to it
		var ref string
		for h := range counts {
			ref = h
		}
		for _, r := range results {
			switch {
			case r.healthy:
			case r.hash == ref:
				matched[r.name] = true
			default:
				bad[r.name] = true
			}
		}
		return bad, matched, unanswered
	}
	best, bestN := "", 0
	for h, n := range counts {
		if n > bestN {
			best, bestN = h, n
		}
	}
	if bestN*2 <= healthyN {
		// no strict healthy majority (a tie, or a plurality): an inconclusive
		// disagreement must not pick the fork used for observations, so nothing is
		// evicted or cleared this cycle. With two endpoints this is the only outcome
		// of a disagreement, so make it visible.
		m.log.Warn().Int64("height", height).Int("healthy", healthyN).Int("agreeing", bestN).
			Msg("healthy endpoints disagree on the block hash with no majority; integrity check skipped")
		return map[string]bool{}, map[string]bool{}, voteInconclusive
	}
	for _, r := range results {
		if r.hash != best {
			bad[r.name] = true
			m.log.Error().Str("endpoint", r.name).Int64("height", height).Str("hash", r.hash).Str("majority", best).
				Msg("endpoint block hash disagrees with majority")
			continue
		}
		matched[r.name] = true
	}
	return bad, matched, voteMajority
}
