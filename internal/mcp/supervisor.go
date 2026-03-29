package mcp

import (
	"context"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HealthState represents the health of an MCP server.
type HealthState int

const (
	StateHealthy HealthState = iota
	StateDegraded
	StateDisconnected
)

func (s HealthState) String() string {
	switch s {
	case StateHealthy:
		return "healthy"
	case StateDegraded:
		return "degraded"
	case StateDisconnected:
		return "disconnected"
	default:
		return "unknown"
	}
}

// ServerHealth tracks per-server health evidence.
type ServerHealth struct {
	State               HealthState
	Since               time.Time
	LastTransportOK     time.Time
	LastCapabilityOK    time.Time
	LastTransportError  string
	LastCapabilityError string
	ConsecutiveFailures int
}

// ProbeResult is the structured return from a capability probe.
type ProbeResult struct {
	Degraded bool
	Detail   string
}

// ToolCaller is the subset of ClientManager that probes need.
type ToolCaller interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (string, bool, error)
}

// CapabilityProbe tests whether an MCP server's real dependency is usable.
type CapabilityProbe interface {
	Probe(ctx context.Context, caller ToolCaller, serverName string) (ProbeResult, error)
}

// backoffState tracks retry interval progression for a single probe tier.
type backoffState struct {
	interval time.Duration
	baseMin  time.Duration
	baseMax  time.Duration
	dormant  time.Duration
	attempts int
}

func newBackoffState(baseMin, baseMax, dormant time.Duration) *backoffState {
	return &backoffState{
		baseMin: baseMin,
		baseMax: baseMax,
		dormant: dormant,
	}
}

func (b *backoffState) recordFailure() {
	b.attempts++
	base := b.baseMin * time.Duration(1<<(b.attempts-1))
	if base > b.baseMax {
		base = b.dormant
	}
	jitter := time.Duration(float64(base) * (0.8 + 0.4*rand.Float64()))
	b.interval = jitter
}

func (b *backoffState) recordSuccess() {
	b.interval = 0
	b.attempts = 0
}

func (b *backoffState) isDormant() bool {
	return b.interval >= b.dormant
}

// Supervisor monitors MCP server health via periodic transport and capability probes.
type Supervisor struct {
	mu                 sync.Mutex
	mgr                *ClientManager
	servers            map[string]*serverEntry
	probes             map[string]CapabilityProbe
	onChange           func(serverName string, oldState, newState HealthState)
	onReconnect        func(ctx context.Context, serverName string) // called after successful reconnect; ctx is cancelled on Stop()
	cancel             context.CancelFunc
	started            bool
	transportInterval  time.Duration
	capabilityInterval time.Duration
	wg                 sync.WaitGroup
}

type serverEntry struct {
	config            MCPServerConfig
	health            ServerHealth
	transportBackoff  *backoffState
	capabilityBackoff *backoffState
	probeNowCh        chan struct{}       // signal channel (buffered size 1)
	waitersMu         sync.Mutex          // protects waiters slice
	waiters           []chan ServerHealth // pending ProbeNow callers
}

// NewSupervisor creates a Supervisor that monitors MCP servers via the given ClientManager.
func NewSupervisor(mgr *ClientManager) *Supervisor {
	return &Supervisor{
		mgr:                mgr,
		servers:            make(map[string]*serverEntry),
		probes:             make(map[string]CapabilityProbe),
		transportInterval:  30 * time.Second,
		capabilityInterval: 60 * time.Second,
	}
}

// RegisterCapabilityProbe associates a capability probe with a server name.
func (s *Supervisor) RegisterCapabilityProbe(serverName string, probe CapabilityProbe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes[serverName] = probe
}

// SetOnReconnect registers a callback invoked after a successful server reconnect.
// The ctx is cancelled when the supervisor stops, so long-running cleanup is safe.
func (s *Supervisor) SetOnReconnect(fn func(ctx context.Context, serverName string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onReconnect = fn
}

// SetOnChange registers a callback invoked on health state transitions.
func (s *Supervisor) SetOnChange(fn func(serverName string, oldState, newState HealthState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// HealthStates returns a snapshot of all monitored servers' health.
func (s *Supervisor) HealthStates() map[string]ServerHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]ServerHealth, len(s.servers))
	for name, entry := range s.servers {
		result[name] = entry.health
	}
	return result
}

// HealthFor returns the health of a single server, or StateDisconnected if unknown.
func (s *Supervisor) HealthFor(serverName string) ServerHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.servers[serverName]; ok {
		return entry.health
	}
	return ServerHealth{State: StateDisconnected}
}

// ProbeNow requests an immediate probe for a server. Before Start(), returns current health.
// Uses waiter list for coalescing: all concurrent callers get the same result.
func (s *Supervisor) ProbeNow(serverName string) ServerHealth {
	s.mu.Lock()
	if !s.started {
		if entry, ok := s.servers[serverName]; ok {
			h := entry.health
			s.mu.Unlock()
			return h
		}
		s.mu.Unlock()
		return ServerHealth{State: StateDisconnected}
	}
	entry, ok := s.servers[serverName]
	if !ok {
		s.mu.Unlock()
		return ServerHealth{State: StateDisconnected}
	}

	health := entry.health
	inBackoff := entry.transportBackoff.interval > 0 || entry.capabilityBackoff.interval > 0
	stale := time.Since(health.LastTransportOK) > 60*time.Second
	s.mu.Unlock()

	if health.State == StateHealthy && !inBackoff && !stale {
		return health
	}

	respCh := make(chan ServerHealth, 1)
	entry.waitersMu.Lock()
	entry.waiters = append(entry.waiters, respCh)
	entry.waitersMu.Unlock()

	select {
	case entry.probeNowCh <- struct{}{}:
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case h := <-respCh:
		return h
	case <-ctx.Done():
		s.mu.Lock()
		h := entry.health
		s.mu.Unlock()
		return h
	}
}

// Start discovers servers from the manager and spawns a probe goroutine per server.
func (s *Supervisor) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	if s.mgr == nil {
		return
	}

	ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.mgr.SetSupervised(true)

	// Discover servers from manager configs.
	s.mgr.mu.Lock()
	configs := make(map[string]MCPServerConfig, len(s.mgr.configs))
	for name, cfg := range s.mgr.configs {
		configs[name] = cfg
	}
	connectedServers := make(map[string]bool, len(s.mgr.clients))
	for name := range s.mgr.clients {
		connectedServers[name] = true
	}
	s.mgr.mu.Unlock()

	now := time.Now()
	for name, cfg := range configs {
		var lastTransportOK time.Time
		if connectedServers[name] {
			lastTransportOK = now
		}
		// Seed initial state from actual client presence.
		// Servers with a connected client start as StateHealthy (provisional for
		// those with capability probes — the initial probe cycle validates).
		// Servers without a client start as StateDisconnected to avoid
		// briefly reporting a disconnected server as healthy.
		initialState := StateDisconnected
		if connectedServers[name] {
			initialState = StateHealthy
		}
		entry := &serverEntry{
			config: cfg,
			health: ServerHealth{
				State:           initialState,
				Since:           now,
				LastTransportOK: lastTransportOK,
			},
			transportBackoff:  newBackoffState(5*time.Second, 30*time.Second, 5*time.Minute),
			capabilityBackoff: newBackoffState(10*time.Second, 60*time.Second, 5*time.Minute),
			probeNowCh:        make(chan struct{}, 1),
		}
		s.servers[name] = entry
		s.wg.Add(1)
		go s.serverLoop(ctx, name, entry)
	}
}

// Stop cancels all probe goroutines and waits for them to finish.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait()

	s.mu.Lock()
	s.started = false
	s.mu.Unlock()
}

// serverLoop is the per-server polling goroutine.
func (s *Supervisor) serverLoop(ctx context.Context, name string, entry *serverEntry) {
	defer s.wg.Done()
	defer s.drainProbeNow(entry)

	// Run initial probes for servers that started as healthy.
	s.mu.Lock()
	probe := s.probes[name]
	initialState := entry.health.State
	s.mu.Unlock()

	if initialState == StateHealthy && probe != nil {
		s.runCapabilityProbe(ctx, name, entry, probe)
	}

	transportTimer := time.NewTimer(s.jitter(s.transportInterval))
	defer transportTimer.Stop()

	capTimer := time.NewTimer(s.jitter(s.capabilityInterval))
	defer capTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-entry.probeNowCh:
			s.runTransportProbe(ctx, name, entry)
			// On-demand path: if still disconnected, attempt reconnect.
			// This is the ONLY path that reconnects — periodic probes never do.
			s.mu.Lock()
			stillDisconnected := entry.health.State == StateDisconnected
			s.mu.Unlock()
			if stillDisconnected {
				s.attemptReconnect(ctx, name, entry)
			}
			s.mu.Lock()
			probe = s.probes[name]
			transportOK := entry.health.State != StateDisconnected
			s.mu.Unlock()
			if transportOK && probe != nil {
				s.runCapabilityProbe(ctx, name, entry, probe)
			}
			s.replyToWaiters(entry)
			// Reset timers after an on-demand probe.
			transportTimer.Reset(s.jitter(s.transportInterval))
			capTimer.Reset(s.jitter(s.capabilityInterval))

		case <-transportTimer.C:
			s.runTransportProbe(ctx, name, entry)
			s.mu.Lock()
			backoff := entry.transportBackoff.interval
			s.mu.Unlock()
			next := s.transportInterval
			if backoff > 0 {
				next = backoff
			}
			transportTimer.Reset(s.jitter(next))

		case <-capTimer.C:
			s.mu.Lock()
			probe = s.probes[name]
			transportOK := entry.health.State != StateDisconnected
			s.mu.Unlock()
			if transportOK && probe != nil {
				s.runCapabilityProbe(ctx, name, entry, probe)
			}
			s.mu.Lock()
			backoff := entry.capabilityBackoff.interval
			s.mu.Unlock()
			next := s.capabilityInterval
			if backoff > 0 {
				next = backoff
			}
			capTimer.Reset(s.jitter(next))
		}
	}
}

// attemptReconnect tries to reconnect a disconnected server. Only called from
// the on-demand ProbeNow path, never from periodic probes.
func (s *Supervisor) attemptReconnect(ctx context.Context, name string, entry *serverEntry) {
	// CDP mode: ensure Chrome has the debug port before reconnecting playwright.
	// Only when keepAlive is true — keepAlive=false defers Chrome launch to tool invocation.
	if name == "playwright" && IsPlaywrightCDPMode(entry.config) && entry.config.KeepAlive {
		if err := EnsureChromeDebugPort(PlaywrightCDPPort(entry.config)); err != nil {
			log.Printf("[mcp-supervisor] %s: Chrome CDP unavailable: %v", name, err)
			return
		}
	}

	reconnCtx, reconnCancel := context.WithTimeout(ctx, 15*time.Second)
	defer reconnCancel()

	_, reconnErr := s.mgr.Reconnect(reconnCtx, name)
	if reconnErr != nil {
		log.Printf("[mcp-supervisor] %s: on-demand reconnect failed: %v", name, reconnErr)
		return
	}

	log.Printf("[mcp-supervisor] %s: on-demand reconnect succeeded", name)
	s.mu.Lock()
	fn := s.onReconnect
	s.mu.Unlock()
	if fn != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			fn(ctx, name)
		}()
	}
	s.recordTransportOK(ctx, name, entry)
}

// runTransportProbe probes transport health and attempts reconnect on failure.
// When transport recovers and the server has a capability probe, runs it
// immediately before declaring healthy — prevents registering Playwright tools
// while Chrome/Bridge is still unavailable.
func (s *Supervisor) runTransportProbe(ctx context.Context, name string, entry *serverEntry) {
	// CDP mode: proactively check if the CDP Chrome is still alive.
	// Only relaunch when keepAlive is true (eager mode). When keepAlive is false,
	// Chrome launches on-demand at first tool invocation — the supervisor should
	// not auto-relaunch it between uses.
	if name == "playwright" && IsPlaywrightCDPMode(entry.config) && entry.config.KeepAlive {
		port := PlaywrightCDPPort(entry.config)
		if !IsChromeCDPReachable(port) {
			log.Printf("[mcp-supervisor] CDP Chrome unreachable — relaunching minimized")
			if err := LaunchCDPChrome(port); err != nil {
				log.Printf("[mcp-supervisor] CDP Chrome relaunch failed: %v", err)
			}
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err := s.mgr.ProbeTransport(probeCtx, name)
	if err == nil {
		s.recordTransportOK(ctx, name, entry)
		return
	}

	// Transport failed — declare disconnected.
	now := time.Now()
	s.mu.Lock()
	entry.transportBackoff.recordFailure()
	entry.health.LastTransportError = err.Error()
	entry.health.ConsecutiveFailures++
	old := entry.health.State
	if old != StateDisconnected {
		entry.health.State = StateDisconnected
		entry.health.Since = now
	}
	s.mu.Unlock()
	if old != StateDisconnected {
		s.fireOnChange(name, old, StateDisconnected)
	}
}

// recordTransportOK is called when transport is confirmed alive (probe or reconnect succeeded).
// For servers with a capability probe, runs it immediately before declaring healthy.
func (s *Supervisor) recordTransportOK(ctx context.Context, name string, entry *serverEntry) {
	now := time.Now()
	s.mu.Lock()
	entry.transportBackoff.recordSuccess()
	entry.health.LastTransportOK = now
	entry.health.LastTransportError = ""
	entry.health.ConsecutiveFailures = 0
	old := entry.health.State
	probe := s.probes[name]
	s.mu.Unlock()

	if probe != nil {
		// Has capability probe — run it before declaring healthy.
		// This prevents registering Playwright tools while Chrome is unavailable.
		s.runCapabilityProbe(ctx, name, entry, probe)
		return
	}

	// No capability probe — transport OK means healthy.
	if old == StateDisconnected {
		s.mu.Lock()
		entry.health.State = StateHealthy
		entry.health.Since = now
		s.mu.Unlock()
		s.fireOnChange(name, old, StateHealthy)
	}
}

// runCapabilityProbe runs the capability probe and updates health state.
func (s *Supervisor) runCapabilityProbe(ctx context.Context, name string, entry *serverEntry, probe CapabilityProbe) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, err := probe.Probe(probeCtx, s.mgr, name)

	now := time.Now()
	s.mu.Lock()
	old := entry.health.State

	var newState HealthState
	var capFailures int
	if err != nil {
		entry.capabilityBackoff.recordFailure()
		entry.health.LastCapabilityError = err.Error()
		entry.health.ConsecutiveFailures++
		capFailures = entry.health.ConsecutiveFailures
		newState = StateDisconnected
	} else if result.Degraded {
		entry.capabilityBackoff.recordFailure()
		entry.health.LastCapabilityError = result.Detail
		entry.health.ConsecutiveFailures++
		capFailures = entry.health.ConsecutiveFailures
		newState = StateDegraded
	} else {
		entry.capabilityBackoff.recordSuccess()
		entry.health.LastCapabilityOK = now
		entry.health.LastCapabilityError = ""
		entry.health.ConsecutiveFailures = 0
		newState = StateHealthy
	}

	if old != newState {
		entry.health.State = newState
		entry.health.Since = now
	}
	s.mu.Unlock()

	if old != newState {
		s.fireOnChange(name, old, newState)
	}

	// Auto-clear playwright readiness marker after 3 consecutive capability probe failures.
	if name == "playwright" && capFailures >= 3 {
		home, _ := os.UserHomeDir()
		localDir := filepath.Join(home, ".shannon", "local")
		if err := ClearPlaywrightMarker(localDir); err != nil {
			log.Printf("[mcp-supervisor] Failed to clear playwright readiness marker: %v", err)
		} else {
			log.Printf("[mcp-supervisor] Playwright extension unreachable after %d probes — cleared readiness marker", capFailures)
		}
	}
}

// fireOnChange logs the transition and calls the onChange callback outside the lock.
func (s *Supervisor) fireOnChange(name string, old, newState HealthState) {
	log.Printf("[mcp-supervisor] %s: %s -> %s", name, old, newState)
	s.mu.Lock()
	fn := s.onChange
	s.mu.Unlock()
	if fn != nil {
		fn(name, old, newState)
	}
}

// replyToWaiters sends the current health to all pending ProbeNow callers.
func (s *Supervisor) replyToWaiters(entry *serverEntry) {
	s.mu.Lock()
	h := entry.health
	s.mu.Unlock()

	entry.waitersMu.Lock()
	waiters := entry.waiters
	entry.waiters = nil
	entry.waitersMu.Unlock()

	for _, ch := range waiters {
		select {
		case ch <- h:
		default:
		}
	}
}

// drainProbeNow replies to all pending waiters on shutdown.
func (s *Supervisor) drainProbeNow(entry *serverEntry) {
	s.mu.Lock()
	h := entry.health
	s.mu.Unlock()

	entry.waitersMu.Lock()
	waiters := entry.waiters
	entry.waiters = nil
	entry.waitersMu.Unlock()

	for _, ch := range waiters {
		select {
		case ch <- h:
		default:
		}
	}
}

// jitter returns a duration with +/- 20% random variation.
func (s *Supervisor) jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}
