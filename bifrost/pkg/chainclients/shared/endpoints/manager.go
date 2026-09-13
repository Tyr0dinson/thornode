package endpoints

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ManagerProvider is implemented by chain clients that run an endpoint Manager. The
// bifrost health server uses it to render live endpoint state without changing the
// ChainClient interface.
type ManagerProvider interface {
	EndpointManager() *Manager
}

// Manager coordinates the endpoints of one chain: it owns the entries, decides which
// endpoint is active, orders read candidates, and runs the background health loop
// (health.go). It is safe for concurrent use.
type Manager struct {
	chain            common.Chain
	cfg              config.ChainFailoverConfig
	entries          []*Entry // sorted by Priority ascending, stable; immutable after construction
	byName           map[string]*Entry
	log              zerolog.Logger
	metrics          *Metrics
	now              func() time.Time
	expectedIdentity string

	active atomic.Int32 // index into entries
	mu     sync.Mutex   // serializes failover / failback decisions

	// health loop
	probes        []Probe
	scannerHeight func() int64
	identity      string
	identityMu    sync.Mutex
	loopCtx       context.Context
	loopCancel    context.CancelFunc
	stopOnce      sync.Once
	startOnce     sync.Once
	wg            sync.WaitGroup
}

// Option customizes a Manager.
type Option func(*Manager)

// WithMetrics attaches Prometheus metrics (nil disables them).
func WithMetrics(m *Metrics) Option { return func(mg *Manager) { mg.metrics = m } }

// WithClock replaces the time source for the manager and every entry (tests only).
func WithClock(now func() time.Time) Option { return func(mg *Manager) { mg.now = now } }

// WithExpectedIdentity pins the identity every endpoint must report.
func WithExpectedIdentity(id string) Option { return func(mg *Manager) { mg.expectedIdentity = id } }

// NewManager builds a manager from the chain's endpoint list (any order) and the
// failover config with defaults applied. It fails on an empty list or duplicate
// names; both are also rejected by config validation, this is defense in depth.
func NewManager(chain common.Chain, eps []config.ChainEndpointConfig, cfg config.ChainFailoverConfig, log zerolog.Logger, opts ...Option) (*Manager, error) {
	if len(eps) == 0 {
		return nil, fmt.Errorf("chain %s: endpoint manager needs at least one endpoint", chain)
	}
	sorted := make([]config.ChainEndpointConfig, len(eps))
	copy(sorted, eps)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		chain:      chain,
		cfg:        cfg,
		byName:     make(map[string]*Entry, len(sorted)),
		log:        log.With().Str("module", "endpoints").Stringer("chain", chain).Logger(),
		now:        time.Now,
		loopCtx:    ctx,
		loopCancel: cancel,
	}
	for _, o := range opts {
		o(m)
	}
	requireIdentity := len(sorted) > 1 || m.expectedIdentity != ""
	for _, e := range sorted {
		if _, dup := m.byName[e.Name]; dup {
			cancel()
			return nil, fmt.Errorf("chain %s: duplicate endpoint name %q", chain, e.Name)
		}
		entry := NewEntry(e.Name, e.Priority)
		entry.SetClock(m.now)
		entry.RequireIdentity(requireIdentity)
		m.entries = append(m.entries, entry)
		m.byName[e.Name] = entry
	}
	m.identity = m.expectedIdentity
	m.active.Store(0)
	m.publishMetrics()
	return m, nil
}

// EndpointByName returns the endpoint config with the given name (zero value if absent).
func EndpointByName(eps []config.ChainEndpointConfig, name string) config.ChainEndpointConfig {
	for _, e := range eps {
		if e.Name == name {
			return e
		}
	}
	return config.ChainEndpointConfig{}
}

// OrderedEndpoints returns eps in the manager's entry (priority) order.
func OrderedEndpoints(m *Manager, eps []config.ChainEndpointConfig) []config.ChainEndpointConfig {
	out := make([]config.ChainEndpointConfig, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, EndpointByName(eps, e.Name))
	}
	return out
}

// Chain returns the chain this manager serves.
func (m *Manager) Chain() common.Chain { return m.chain }

// Config returns the failover configuration in use.
func (m *Manager) Config() config.ChainFailoverConfig { return m.cfg }

// Len returns the number of endpoints.
func (m *Manager) Len() int { return len(m.entries) }

// Entries returns the entries in priority order (a copy of the slice).
func (m *Manager) Entries() []*Entry {
	out := make([]*Entry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Names returns the endpoint names in priority order.
func (m *Manager) Names() []string {
	out := make([]string, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.Name
	}
	return out
}

// Entry returns the entry with the given name, or nil.
func (m *Manager) Entry(name string) *Entry { return m.byName[name] }

// Active returns the sticky active entry. It never returns nil.
func (m *Manager) Active() *Entry { return m.entries[m.active.Load()] }

// WriteTarget is the only endpoint writes go to. It is nil when the active entry
// is Disabled (wrong network) and nothing else could replace it; routers must then
// refuse the write rather than send it to a rejected endpoint.
func (m *Manager) WriteTarget() *Entry {
	e := m.Active()
	if e.State() == StateDisabled {
		return nil
	}
	return e
}

// AllDown reports whether no endpoint is Healthy.
func (m *Manager) AllDown() bool {
	for _, e := range m.entries {
		if e.State() == StateHealthy {
			return false
		}
	}
	return true
}

// ReadCandidates returns endpoints for a read, best first: the active endpoint, then
// the other healthy ones by priority, all excluding rate-limit cooldown. When that
// leaves nothing (every endpoint is down or cooling off) it returns every
// non-disabled endpoint by priority so bifrost keeps trying (#2828 failover logic 5).
func (m *Manager) ReadCandidates() []*Entry {
	active := m.Active()
	out := make([]*Entry, 0, len(m.entries))
	if active.Eligible() {
		out = append(out, active)
	}
	for _, e := range m.entries {
		if e != active && e.Eligible() {
			out = append(out, e)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, e := range m.entries {
		if e.State() != StateDisabled {
			out = append(out, e)
		}
	}
	return out
}

// RecordSuccess is called by routers after a successful request. While every
// endpoint is down, an inline success on a non-healthy endpoint counts as one
// passing recovery check so bifrost can climb out of all-down without waiting for
// the probe loop (identity verification still gates the promotion).
func (m *Manager) RecordSuccess(name string) {
	e := m.byName[name]
	if e == nil {
		return
	}
	switch e.State() {
	case StateHealthy:
		e.RecordSuccess()
	case StateUnhealthy, StateRecovering:
		if m.AllDown() {
			from, to := e.RecordHealthCheck(true, m.cfg)
			m.logTransition(e, from, to, "inline success while all endpoints down")
			if to == StateHealthy {
				m.reselect("recovered")
			}
		}
	}
}

// RecordError records an inline error. It returns true when the endpoint left
// Healthy (or Recovering). If that endpoint was active, a failover happens here.
func (m *Manager) RecordError(name string, kind ErrorKind) bool {
	e := m.byName[name]
	if e == nil {
		return false
	}
	m.countError(name, kind)
	moved := e.RecordError(m.cfg, kind)
	if moved {
		m.log.Warn().Str("endpoint", name).Str("kind", string(kind)).Msg("endpoint marked unhealthy")
		if e == m.Active() {
			m.reselect(fmt.Sprintf("active endpoint %s unhealthy (%s)", name, kind))
		}
		m.publishMetrics()
	}
	return moved
}

// recordProbeStrike is the health loop's counterpart of RecordError.
func (m *Manager) recordProbeStrike(e *Entry, kind ErrorKind, why string) {
	m.countError(e.Name, kind)
	if e.RecordProbeStrike(m.cfg, kind) {
		m.log.Warn().Str("endpoint", e.Name).Str("kind", string(kind)).Str("why", why).Msg("endpoint marked unhealthy by health probe")
		if e == m.Active() {
			m.reselect(fmt.Sprintf("active endpoint %s failed health probes (%s)", e.Name, kind))
		}
	}
}

func (m *Manager) countError(name string, kind ErrorKind) {
	if m.metrics != nil {
		m.metrics.errors.WithLabelValues(m.chain.String(), name, string(kind)).Inc()
	}
}

// RecordRateLimited puts an endpoint into cooldown for rate_limit_backoff.
func (m *Manager) RecordRateLimited(name string) {
	e := m.byName[name]
	if e == nil {
		return
	}
	m.countError(name, ErrorKindRateLimit)
	e.RecordRateLimited(m.cfg.RateLimitBackoff)
	m.log.Warn().Str("endpoint", name).Dur("cooldown", m.cfg.RateLimitBackoff).Msg("endpoint rate limited")
	if e == m.Active() {
		m.handOff(fmt.Sprintf("active endpoint %s rate limited", name))
	}
}

// Disable permanently removes an endpoint and moves active off it if needed.
func (m *Manager) Disable(name, reason string) {
	e := m.byName[name]
	if e == nil {
		return
	}
	e.Disable(reason)
	if e == m.Active() {
		m.reselect("active endpoint disabled: " + reason)
	}
	m.publishMetrics()
}

// handOff moves active to another eligible (healthy, not cooling down) endpoint
// while the current active is cooling down. Sticky otherwise.
func (m *Manager) handOff(reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.entries[m.active.Load()]
	if cur.Eligible() {
		return false
	}
	for i, e := range m.entries {
		if e != cur && e.Eligible() {
			m.switchActive(cur, e, int32(i), reason, "failover")
			return true
		}
	}
	return false
}

// reselect moves active to the highest-priority Healthy endpoint if the current
// active is not Healthy. With nothing Healthy, a Disabled active is replaced by the
// best non-Disabled endpoint so writes never target a wrong-network node. Returns
// true when active changed.
func (m *Manager) reselect(reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.entries[m.active.Load()]
	if cur.State() == StateHealthy {
		return false
	}
	// prefer a healthy endpoint that is not cooling down; fall back to any healthy one
	for i, e := range m.entries {
		if e.Eligible() {
			m.switchActive(cur, e, int32(i), reason, "failover")
			return true
		}
	}
	for i, e := range m.entries {
		if e.State() == StateHealthy {
			m.switchActive(cur, e, int32(i), reason, "failover")
			return true
		}
	}
	m.log.Error().Str("active", cur.Name).Msg("all endpoints unhealthy; retrying every endpoint")
	if m.metrics != nil {
		m.metrics.allDown.WithLabelValues(m.chain.String()).Set(1)
	}
	if cur.State() == StateDisabled {
		for i, e := range m.entries {
			if e.State() != StateDisabled {
				m.switchActive(cur, e, int32(i), reason+" (active disabled)", "failover")
				return true
			}
		}
	}
	return false
}

// MaybeFailback switches to a higher-priority Healthy endpoint when
// prefer_higher_priority is set. Returns true when active changed.
func (m *Manager) MaybeFailback() bool {
	if !m.cfg.PreferHigherPriority {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	curIdx := m.active.Load()
	cur := m.entries[curIdx]
	for i, e := range m.entries {
		if int32(i) >= curIdx {
			return false
		}
		if e.Eligible() && e.Priority < cur.Priority {
			m.switchActive(cur, e, int32(i), "higher-priority endpoint healthy", "failback")
			return true
		}
	}
	return false
}

// switchActive must be called with m.mu held.
func (m *Manager) switchActive(from, to *Entry, toIdx int32, reason, event string) {
	m.active.Store(toIdx)
	m.log.Warn().Str("from", from.Name).Str("to", to.Name).Str("reason", reason).Msg("endpoint " + event)
	if m.metrics != nil {
		m.metrics.failovers.WithLabelValues(m.chain.String(), from.Name, to.Name, event).Inc()
	}
	m.publishMetrics()
}

func (m *Manager) logTransition(e *Entry, from, to State, why string) {
	if from == to {
		return
	}
	m.log.Info().Str("endpoint", e.Name).Str("from", from.String()).Str("to", to.String()).Str("why", why).Msg("endpoint state transition")
	if to == StateHealthy && m.metrics != nil {
		m.metrics.recoveries.WithLabelValues(m.chain.String(), e.Name).Inc()
	}
}

// Status returns a snapshot of every endpoint in priority order.
func (m *Manager) Status() []Snapshot {
	active := m.Active()
	out := make([]Snapshot, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.Snapshot()
		out[i].Active = e == active
	}
	return out
}

// Stop halts the health loop (if started), cancelling any in-flight cycle, and
// waits for it. Idempotent and safe on a manager that never started.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() { m.loopCancel() })
	m.wg.Wait()
}

// publishMetrics exports state/active gauges for every endpoint.
func (m *Manager) publishMetrics() {
	if m.metrics == nil {
		return
	}
	active := m.Active()
	chain := m.chain.String()
	allDown := 1.0
	for _, e := range m.entries {
		st := e.State()
		if st == StateHealthy {
			allDown = 0
		}
		m.metrics.state.WithLabelValues(chain, e.Name).Set(float64(st))
		isActive := 0.0
		if e == active {
			isActive = 1
		}
		m.metrics.active.WithLabelValues(chain, e.Name).Set(isActive)
		if h := e.LastHeight(); h > 0 {
			m.metrics.height.WithLabelValues(chain, e.Name).Set(float64(h))
		}
	}
	m.metrics.allDown.WithLabelValues(chain).Set(allDown)
}
