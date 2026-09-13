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
		if r.err != nil {
			m.log.Warn().Err(r.err).Str("endpoint", e.Name).Msg("endpoint unreachable at startup")
			e.MarkUnhealthy(ErrorKindProbe)
		} else {
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
	if ref == "" && len(verified) == 1 {
		for name, id := range verified {
			ref = id
			m.log.Warn().Str("endpoint", name).Str("identity", id).
				Msg("reference identity taken from the only verified endpoint; set expected_identity to pin it")
		}
	}
	m.setReference(ref)

	for i, e := range m.entries {
		id, ok := verified[e.Name]
		if !ok {
			if e.identityRequiredValue() {
				e.MarkUnhealthy(ErrorKindProbe)
			}
			continue
		}
		if ref != "" && id != ref {
			if i == 0 && m.expectedIdentity != "" {
				return fmt.Errorf("chain %s: priority-1 endpoint %s reports identity %q but expected_identity is %q; check the endpoint is on the right network",
					m.chain, e.Name, id, ref)
			}
			m.log.Error().Str("endpoint", e.Name).Str("expected", ref).Str("got", id).
				Msg("endpoint reports a different chain identity; disabling it permanently")
			e.Disable(fmt.Sprintf("identity %q != %q", id, ref))
			continue
		}
		e.SetIdentity(id)
	}
	if ref == "" && len(verified) > 1 {
		m.log.Error().Msg("verified endpoints disagree on chain identity with no majority; none is healthy until expected_identity is set or a majority forms")
	}
	if m.AllDown() {
		m.log.Error().Msg("no healthy endpoint at startup; continuing and retrying in the background")
	}
	m.reselect("startup")
	m.publishMetrics()
	return nil
}

// majorityIdentity returns the identity held by a strict majority of verified
// endpoints, or "" when there is none.
func majorityIdentity(verified map[string]string) string {
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
			return true
		default:
			return e.RecoveryDue(m.cfg.RecoveryCheckInterval)
		}
	}
	results := m.probeAll(ctx, selected, true)

	// 1. identity verification for endpoints that were unverified
	ref := m.ReferenceIdentity()
	for i, e := range m.entries {
		r := results[i]
		if e.IdentityVerified() || r.idErr != nil || r.identity == "" {
			continue
		}
		if ref == "" {
			// no reference yet: the priority-1 endpoint sets it, otherwise the first
			// verified endpoint does (the operator can pin it with expected_identity)
			if i == 0 || m.entries[0].State() == StateDisabled || !m.anyVerified() {
				m.setReference(r.identity)
				ref = r.identity
			}
		}
		if ref != "" && r.identity != ref {
			m.log.Error().Str("endpoint", e.Name).Str("expected", ref).Str("got", r.identity).
				Msg("endpoint reports a different chain identity; disabling it permanently")
			m.Disable(e.Name, fmt.Sprintf("identity %q != %q", r.identity, ref))
			results[i].err = errors.New("disabled: identity mismatch")
			continue
		}
		if ref != "" {
			e.SetIdentity(r.identity)
		}
	}

	// 2. record probe data
	heights := make(map[string]int64, len(m.entries))
	for i, e := range m.entries {
		r := results[i]
		if r.err != nil {
			continue
		}
		e.RecordProbe(r.height, r.latency)
		heights[e.Name] = r.height
		if m.metrics != nil {
			m.metrics.probeLatency.WithLabelValues(m.chain.String(), e.Name).Set(r.latency.Seconds())
		}
	}

	// 3. consensus tip (quorum of two within max_block_lag); the scanner baseline is
	// only meaningful with a single endpoint (with several, the scanner reads from
	// the active endpoint and would let it evict every other endpoint)
	tip, quorum := consensusTip(heights, m.cfg.MaxBlockLag)
	var scanner int64
	if m.scannerHeight != nil && len(m.entries) == 1 {
		scanner = m.scannerHeight()
	}
	lagging := func(h int64) (bool, string) {
		if quorum && tip-h > m.cfg.MaxBlockLag {
			return true, fmt.Sprintf("behind consensus tip %d by %d blocks", tip, tip-h)
		}
		if scanner > 0 && scanner-h > m.cfg.MaxBlockLag {
			return true, fmt.Sprintf("behind scanner height %d by %d blocks", scanner, scanner-h)
		}
		return false, ""
	}

	// 4. integrity: majority hash at tip - depth among healthy hash-capable endpoints
	badHash := map[string]bool{}
	if quorum && m.cfg.IntegrityCheckDepth > 0 && tip-m.cfg.IntegrityCheckDepth > 0 {
		badHash = m.integrityCheck(ctx, tip-m.cfg.IntegrityCheckDepth, heights)
	}

	// 5. apply results per entry
	for i, e := range m.entries {
		r := results[i]
		if errors.Is(r.err, errSkipped) {
			continue
		}
		switch e.State() {
		case StateHealthy:
			if r.err != nil {
				m.log.Warn().Err(r.err).Str("endpoint", e.Name).Msg("health probe failed")
				m.recordProbeStrike(e, ErrorKindProbe, "probe failed")
				continue
			}
			if lag, why := lagging(r.height); lag {
				m.log.Warn().Str("endpoint", e.Name).Int64("height", r.height).Msg("endpoint lagging: " + why)
				m.recordProbeStrike(e, ErrorKindLag, why)
				continue
			}
			if badHash[e.Name] {
				m.recordProbeStrike(e, ErrorKindIntegrity, "minority block hash")
				continue
			}
			e.RecordProbePass()
		case StateUnhealthy, StateRecovering:
			ok := r.err == nil
			if ok {
				if lag, _ := lagging(r.height); lag {
					ok = false
				}
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

func (m *Manager) anyVerified() bool {
	for _, e := range m.entries {
		if e.IdentityVerified() {
			return true
		}
	}
	return false
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

// integrityCheck fetches the block hash at height from every healthy endpoint that
// supports it and returns the set of endpoints in the minority. Ties yield nothing.
func (m *Manager) integrityCheck(ctx context.Context, height int64, heights map[string]int64) map[string]bool {
	type res struct {
		name string
		hash string
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []res
	)
	for i, e := range m.entries {
		hp, ok := m.probes[i].(HashProbe)
		if !ok || e.State() != StateHealthy {
			continue
		}
		if h, seen := heights[e.Name]; !seen || h < height {
			continue // endpoint has not reached that height yet; not evidence of anything
		}
		wg.Add(1)
		go func(e *Entry, hp HashProbe) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, m.cfg.HealthCheckTimeout)
			defer cancel()
			h, err := hp.HashAt(pctx, height)
			if err != nil || h == "" {
				return // a failed hash fetch is not an integrity signal
			}
			mu.Lock()
			results = append(results, res{e.Name, strings.ToLower(h)})
			mu.Unlock()
		}(e, hp)
	}
	wg.Wait()
	if len(results) < 2 {
		return nil
	}
	counts := map[string]int{}
	for _, r := range results {
		counts[r.hash]++
	}
	best, bestN, tie := "", 0, false
	for h, n := range counts {
		switch {
		case n > bestN:
			best, bestN, tie = h, n, false
		case n == bestN:
			tie = true
		}
	}
	if tie {
		return nil
	}
	bad := map[string]bool{}
	for _, r := range results {
		if r.hash != best {
			bad[r.name] = true
			m.log.Error().Str("endpoint", r.name).Int64("height", height).Str("hash", r.hash).Str("majority", best).
				Msg("endpoint block hash disagrees with majority")
		}
	}
	return bad
}
