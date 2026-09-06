package koe

import (
	"context"
	"math/rand"
	"time"
)

// WarmRetryPolicy decides whether — and how long after — a failed warm-session
// bringup should be retried.
//
// Why it is a type instead of the old inline `time.After(5 * time.Second)`:
//
//   - The old schedule was a FIXED 5s, forever, with no give-up. Measured
//     2026-08-28: 29,897 connect failures against a backend that had returned
//     the same error for three days, ~8.8 MB/day of log, and every one of them
//     a Cloud request that could never succeed.
//   - 91% of those (27,290) were `account_required` — a SHARED prerequisite. It
//     is not a provider outage and not a network blip: without a billing
//     principal neither provider can start, so retrying on a timer changes
//     nothing until the account state itself changes. That class must pause,
//     not back off.
//   - The remaining classes ARE worth retrying, but not at a fixed 5s. Backoff
//     with jitter keeps a transient blip fast to recover while a sustained
//     outage costs ~1 request/minute instead of 12.
//
// Pausing is deliberately scoped to IDLE pre-warming. A user pressing the call
// button always attempts, whatever the policy says (see UserRequestedCall) —
// that is the escape hatch that makes pausing safe, and it is why no auth-state
// polling is needed to recover.
//
// Pure and clock-free apart from the durations it returns, so the whole decision
// table is unit-testable without a daemon, a network, or a wall clock.
type WarmRetryPolicy struct {
	// consecutive failures since the last success or user request.
	streak int
	// paused is set by a failure whose cause cannot be fixed by waiting.
	paused bool
	// pausedCode records WHY, for logging — a paused pre-warm with no visible
	// reason is the kind of silence this whole change exists to remove.
	pausedCode string
}

// warmRetryBackoff is the delay for the Nth consecutive failure (1-based),
// capped at the last entry. The first step keeps the pre-change 5s so a single
// transient failure recovers exactly as fast as it used to.
var warmRetryBackoff = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
	60 * time.Second,
}

// WarmRetryJitterFraction spreads retries so many machines (or many restarts of
// one) do not align on the same second against a recovering backend.
const WarmRetryJitterFraction = 0.2

// blocksIdleWarm reports whether a failure cause makes further IDLE retrying
// pointless until something outside this process changes.
//
// account_required is the shared-prerequisite case above. quota_exceeded is the
// same shape: the mint gate is realtime's only pre-spend check, and it will keep
// refusing until the plan or the period changes. Everything else — backend 5xx,
// network, local audio, unclassified — is plausibly transient and backs off
// instead, because pausing on those would turn a blip into "voice is dead until
// you press the button".
func blocksIdleWarm(code string) bool {
	switch code {
	case CallFailureAccountRequired, CallFailureQuotaExceeded:
		return true
	default:
		return false
	}
}

// OnFailure records one failed bringup and returns the delay before the next
// IDLE retry. retry=false means idle pre-warming is now paused; a user-initiated
// call still goes through.
func (p *WarmRetryPolicy) OnFailure(code string) (delay time.Duration, retry bool) {
	p.streak++
	if blocksIdleWarm(code) {
		p.paused = true
		p.pausedCode = code
		return 0, false
	}
	i := p.streak - 1
	if i >= len(warmRetryBackoff) {
		i = len(warmRetryBackoff) - 1
	}
	return warmRetryBackoff[i], true
}

// OnSuccess clears the streak and any pause: the prerequisite that was missing
// is evidently present again.
func (p *WarmRetryPolicy) OnSuccess() {
	p.streak = 0
	p.paused = false
	p.pausedCode = ""
}

// UserRequestedCall is the escape hatch. Pressing the call button always tries,
// and resets the schedule so the attempt is immediate rather than inheriting a
// 60s backoff. It does NOT clear `paused` — only an actual success does — so a
// still-broken prerequisite re-pauses instead of restarting the storm.
func (p *WarmRetryPolicy) UserRequestedCall() {
	p.streak = 0
}

// Paused reports whether idle pre-warming is suspended, and why.
func (p *WarmRetryPolicy) Paused() (bool, string) { return p.paused, p.pausedCode }

// Streak is the current consecutive-failure count, for logging.
func (p *WarmRetryPolicy) Streak() int { return p.streak }

// JitterWarmRetryDelay spreads a retry by ±WarmRetryJitterFraction so many
// machines — or many restarts of one — do not align on the same second against
// a recovering backend.
//
// Deliberately the ONLY randomised part of this file: WarmRetryPolicy stays
// pure so its whole decision table is deterministically testable, and the
// randomness lives here where the only property that matters is the bound.
// Never returns less than a second, so jitter cannot turn a backoff into a hot
// loop.
func JitterWarmRetryDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	span := float64(d) * WarmRetryJitterFraction
	offset := (rand.Float64()*2 - 1) * span // [-span, +span)
	out := time.Duration(float64(d) + offset)
	if out < time.Second {
		out = time.Second
	}
	return out
}

// WarmRetryLane owns the ONE pending idle pre-warm retry timer.
//
// Why the generation counter it wraps is a type rather than a bare uint64 in
// cmd/koe.go: it is the half of the 2026-08-28 fix that no test could reach.
// Every failure used to spawn its own timer goroutine, and the caller's
// `warming` latch only merges timers that wake while an attempt is actually in
// flight. Those failures returned in under 100 ms, so the latch was essentially
// never engaged and every lane ever created survived — one per call-button
// press, one per audio retry — and the aggregate failure rate crept up with
// usage: measured 1 failure/5s on a fresh process against 1 per 2.3s after 12
// presses, 29,897 failures over three days. Arm retires its predecessor, so
// exactly one lane is live however many times it is armed.
//
// The generation is guarded by the CALLER's lock (cmd/koe.go's sessMu), not by
// one of the lane's own. IsCurrent has to be read in the same critical section
// as the caller's session state (curConn == nil && !warming), or the two reads
// are not one decision; a second lock could not provide that and would only
// invite a lock-order bug. Arm, Retire and IsCurrent must therefore all be
// called under that lock.
type WarmRetryLane struct {
	gen uint64
	// after is time.After in production. Tests substitute a channel they fire by
	// hand, so the lane's bookkeeping is verifiable without a wall clock.
	after func(time.Duration) <-chan time.Time
}

// Retire invalidates any pending lane without arming a new one. Used when idle
// pre-warming pauses, and when a user-initiated call takes the attempt over.
func (l *WarmRetryLane) Retire() { l.gen++ }

// Arm retires any pending lane and schedules fire after delay. fire runs on its
// own goroutine: it must take the caller's lock and consult IsCurrent with the
// generation it was handed before acting. A cancelled ctx drops the lane
// silently — process teardown, not a superseded retry.
func (l *WarmRetryLane) Arm(ctx context.Context, delay time.Duration, fire func(gen uint64)) {
	l.gen++
	gen := l.gen
	wait := l.after
	if wait == nil {
		wait = time.After
	}
	// The timer starts HERE, under the caller's lock — not whenever the goroutine
	// happens to be scheduled. The delay the policy chose is measured from the
	// failure that caused it, and the lane is observably armed the moment Arm
	// returns.
	timer := wait(delay)
	go func() {
		select {
		case <-timer:
		case <-ctx.Done():
			return
		}
		fire(gen)
	}()
}

// IsCurrent reports whether gen is still the live lane.
func (l *WarmRetryLane) IsCurrent(gen uint64) bool { return gen == l.gen }
