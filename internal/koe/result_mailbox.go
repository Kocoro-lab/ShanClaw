package koe

import (
	"sync"
	"time"
)

// ResultMailbox owns completed do_task speech independently of a Realtime
// connection but never independently of its originating call. A replacement
// handler may recover delivery only for the same burst; ending the call retires
// that burst so a later Option-key activation starts with a clean voice state.
//
// Entries stay in the mailbox until the result response reaches response.done.
// response.created is only an acceptance acknowledgement: removing an entry there
// would lose it if playback is cancelled or the connection closes mid-response.
type ResultMailbox struct {
	mu           sync.Mutex
	nextID       uint64
	entries      []resultMailboxEntry
	activeBursts map[string]struct{}
	taskGroups   map[string]*taskResultGroup
	notify       chan struct{}
}

type resultMailboxEntry struct {
	id         uint64
	burstID    string
	groupID    string
	callID     string
	result     SayResult
	resumptive bool
	owner      string
}

type resultAnnouncement struct {
	id         uint64
	callID     string
	result     SayResult
	resumptive bool
}

type taskResultTicket struct {
	burstID string
	groupID string
	callID  string
}

type taskResultGroup struct {
	burstID    string
	responseID string
	expected   map[string]struct{}
	completed  map[string]struct{}
	sealed     bool
	sealEpoch  uint64
}

func NewResultMailbox() *ResultMailbox {
	return &ResultMailbox{
		activeBursts: make(map[string]struct{}),
		taskGroups:   make(map[string]*taskResultGroup),
		notify:       make(chan struct{}, 1),
	}
}

// BeginBurst opens voice delivery for one call. A burst is the hard boundary
// created by an Option-key activation: completed work may persist elsewhere,
// but only this exact call may claim its spoken result.
func (m *ResultMailbox) BeginBurst(burstID string) {
	if m == nil || burstID == "" {
		return
	}
	m.mu.Lock()
	m.activeBursts[burstID] = struct{}{}
	m.mu.Unlock()
}

// RetireBurst closes a call's voice-delivery scope and drops any speech that was
// still queued for it. Later results from detached do_task goroutines are also
// rejected because the burst is no longer active.
func (m *ResultMailbox) RetireBurst(burstID string) int {
	if m == nil || burstID == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeBursts, burstID)
	for groupID, group := range m.taskGroups {
		if group.burstID == burstID {
			delete(m.taskGroups, groupID)
		}
	}
	kept := m.entries[:0]
	removed := 0
	for _, entry := range m.entries {
		if entry.burstID == burstID {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	m.entries = kept
	return removed
}

// Enqueue records a result before waking a sender. The wake is deliberately
// edge-triggered and lossy; the result is not. Queue saturation therefore cannot
// discard completed work.
func (m *ResultMailbox) Enqueue(result SayResult, resumptive bool) uint64 {
	return m.enqueue("", "", "", result, resumptive)
}

// EnqueueForBurst records speech only while its originating call still owns a
// live voice scope. The task result itself remains persisted by the daemon even
// when this returns zero after a hang-up.
func (m *ResultMailbox) EnqueueForBurst(burstID string, result SayResult, resumptive bool) uint64 {
	return m.enqueue(burstID, "", "", result, resumptive)
}

// BeginTaskResult registers one asynchronous do_task under the Realtime response
// that emitted it. The response boundary is the only deterministic way to know
// the complete parallel call set; completion timing must not split that set into
// multiple spoken replies.
func (m *ResultMailbox) BeginTaskResult(burstID, responseID, callID string) taskResultTicket {
	ticket := taskResultTicket{burstID: burstID, callID: callID}
	if m == nil || responseID == "" || callID == "" {
		return ticket
	}
	ticket.groupID = burstID + "\x00" + responseID
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.taskGroups[ticket.groupID]
	if group == nil {
		group = &taskResultGroup{
			burstID: burstID, responseID: responseID,
			expected: make(map[string]struct{}), completed: make(map[string]struct{}),
		}
		m.taskGroups[ticket.groupID] = group
	}
	if _, exists := group.expected[callID]; !exists {
		group.expected[callID] = struct{}{}
		group.sealed = false
		group.sealEpoch++
	}
	return ticket
}

// ScheduleTaskGroupSeal closes a provider response group after no new tool call
// has joined it for delay. Qwen can wait for function outputs before emitting
// response.done, so response.done alone cannot be its grouping boundary.
func (m *ResultMailbox) ScheduleTaskGroupSeal(ticket taskResultTicket, delay time.Duration) {
	if m == nil || ticket.groupID == "" || delay <= 0 {
		return
	}
	m.mu.Lock()
	group := m.taskGroups[ticket.groupID]
	if group == nil {
		m.mu.Unlock()
		return
	}
	epoch := group.sealEpoch
	m.mu.Unlock()
	time.AfterFunc(delay, func() {
		m.mu.Lock()
		group := m.taskGroups[ticket.groupID]
		if group == nil || group.sealed || group.sealEpoch != epoch {
			m.mu.Unlock()
			return
		}
		group.sealed = true
		ready := m.taskGroupReadyLocked(ticket.groupID)
		m.mu.Unlock()
		if ready {
			m.Wake()
		}
	})
}

// SealTaskResponse records response.done for the response that emitted the
// calls. A group becomes speakable only after it is sealed and every registered
// task has reached a terminal result.
func (m *ResultMailbox) SealTaskResponse(burstID, responseID string) {
	if m == nil || responseID == "" {
		return
	}
	groupID := burstID + "\x00" + responseID
	m.mu.Lock()
	group := m.taskGroups[groupID]
	if group != nil {
		group.sealed = true
		group.sealEpoch++
	}
	ready := m.taskGroupReadyLocked(groupID)
	m.mu.Unlock()
	if ready {
		m.Wake()
	}
}

// EnqueueTaskResult records a terminal task result without making a partial
// parallel group speakable. Zero-group tickets retain the legacy standalone
// behavior used by direct callers that have no Realtime response identity.
func (m *ResultMailbox) EnqueueTaskResult(ticket taskResultTicket, result SayResult, resumptive bool) uint64 {
	if ticket.groupID == "" {
		return m.enqueue(ticket.burstID, "", ticket.callID, result, resumptive)
	}
	if m == nil {
		return 0
	}
	result.Deliverables = append([]Deliverable(nil), result.Deliverables...)
	m.mu.Lock()
	if ticket.burstID != "" {
		if _, active := m.activeBursts[ticket.burstID]; !active {
			m.mu.Unlock()
			return 0
		}
	}
	group := m.taskGroups[ticket.groupID]
	if group == nil {
		// The response group was already sealed, claimed, and completed (a very
		// late sibling on a response Qwen held open past the seal window). Fall
		// back to a standalone burst-scoped entry rather than dropping the result.
		m.mu.Unlock()
		return m.enqueue(ticket.burstID, "", ticket.callID, result, resumptive)
	}
	if _, expected := group.expected[ticket.callID]; !expected {
		m.mu.Unlock()
		return 0
	}
	if _, duplicate := group.completed[ticket.callID]; duplicate {
		m.mu.Unlock()
		return 0
	}
	m.nextID++
	id := m.nextID
	m.entries = append(m.entries, resultMailboxEntry{
		id: id, burstID: ticket.burstID, groupID: ticket.groupID, callID: ticket.callID,
		result: result, resumptive: resumptive,
	})
	group.completed[ticket.callID] = struct{}{}
	ready := m.taskGroupReadyLocked(ticket.groupID)
	m.mu.Unlock()
	if ready {
		m.Wake()
	}
	return id
}

// AbandonTaskResult closes a registered result slot that has no deliverable
// outcome, such as a superseded execution generation. It never creates speech,
// but it must release completed siblings once the response group is otherwise
// terminal.
func (m *ResultMailbox) AbandonTaskResult(ticket taskResultTicket) {
	if m == nil || ticket.groupID == "" {
		return
	}
	m.mu.Lock()
	group := m.taskGroups[ticket.groupID]
	if group == nil {
		m.mu.Unlock()
		return
	}
	if _, expected := group.expected[ticket.callID]; !expected {
		m.mu.Unlock()
		return
	}
	group.completed[ticket.callID] = struct{}{}
	ready := m.taskGroupReadyLocked(ticket.groupID)
	hasEntries := false
	if ready {
		for _, entry := range m.entries {
			if entry.groupID == ticket.groupID {
				hasEntries = true
				break
			}
		}
		if !hasEntries {
			delete(m.taskGroups, ticket.groupID)
		}
	}
	m.mu.Unlock()
	if ready && hasEntries {
		m.Wake()
	}
}

func (m *ResultMailbox) enqueue(burstID, groupID, callID string, result SayResult, resumptive bool) uint64 {
	if m == nil {
		return 0
	}
	if result.Reply == "" && result.Say == "" && len(result.Deliverables) == 0 {
		return 0
	}
	result.Deliverables = append([]Deliverable(nil), result.Deliverables...)
	m.mu.Lock()
	if burstID != "" {
		if _, active := m.activeBursts[burstID]; !active {
			m.mu.Unlock()
			return 0
		}
	}
	m.nextID++
	id := m.nextID
	m.entries = append(m.entries, resultMailboxEntry{
		id: id, burstID: burstID, groupID: groupID, callID: callID, result: result, resumptive: resumptive,
	})
	m.mu.Unlock()
	m.Wake()
	return id
}

// Wake asks the active handler to inspect the mailbox. It is safe to call when no
// handler is active; every newly attached handler also calls Wake once.
func (m *ResultMailbox) Wake() {
	if m == nil {
		return
	}
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *ResultMailbox) notifications() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.notify
}

// claim transfers every currently-pending entry to one connection owner. Only
// one task-result response is in flight per handler, so owner is sufficient as a
// lease key; a connection teardown releases all of its entries atomically.
func (m *ResultMailbox) claim(owner string) []resultAnnouncement {
	return m.claimForBurst(owner, "")
}

func (m *ResultMailbox) claimForBurst(owner, burstID string) []resultAnnouncement {
	if m == nil || owner == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []resultAnnouncement
	groupID := ""
	for i := range m.entries {
		entry := &m.entries[i]
		if entry.owner != "" || (entry.burstID != "" && entry.burstID != burstID) {
			continue
		}
		if entry.groupID == "" {
			break
		}
		if m.taskGroupReadyLocked(entry.groupID) {
			groupID = entry.groupID
			break
		}
	}
	for i := range m.entries {
		entry := &m.entries[i]
		// Empty burst IDs are retained only for standalone/test compatibility.
		// Production do_task results are always scoped and must match exactly.
		if entry.owner != "" || (entry.burstID != "" && entry.burstID != burstID) {
			continue
		}
		if groupID == "" {
			if entry.groupID != "" {
				continue
			}
		} else if entry.groupID != groupID {
			continue
		}
		entry.owner = owner
		result := entry.result
		result.Deliverables = append([]Deliverable(nil), entry.result.Deliverables...)
		out = append(out, resultAnnouncement{
			id: entry.id, callID: entry.callID, result: result, resumptive: entry.resumptive,
		})
	}
	return out
}

func (m *ResultMailbox) taskGroupReadyLocked(groupID string) bool {
	group := m.taskGroups[groupID]
	return group != nil && group.sealed && len(group.expected) > 0 && len(group.completed) == len(group.expected)
}

// complete removes only entries held by owner. It is called after a completed
// response.done, which is the delivery acknowledgement for this in-memory plane.
func (m *ResultMailbox) complete(owner string) int {
	if m == nil || owner == "" {
		return 0
	}
	m.mu.Lock()
	kept := m.entries[:0]
	completedGroups := make(map[string]struct{})
	removed := 0
	for _, entry := range m.entries {
		if entry.owner == owner {
			removed++
			if entry.groupID != "" {
				completedGroups[entry.groupID] = struct{}{}
			}
			continue
		}
		kept = append(kept, entry)
	}
	m.entries = kept
	for groupID := range completedGroups {
		delete(m.taskGroups, groupID)
	}
	hasPending := len(m.entries) > 0
	m.mu.Unlock()
	if removed > 0 && hasPending {
		// Multiple groups can become ready before the sender consumes the mailbox's
		// single edge-triggered notification. Completing one group must schedule the
		// next instead of leaving it asleep until unrelated activity occurs.
		m.Wake()
	}
	return removed
}

// release returns an owner's in-flight entries to pending. The caller decides
// whether to Wake immediately: connection teardown relies on the next handler's
// startup wake, while a cancelled response can retry after the user yields.
func (m *ResultMailbox) release(owner string) int {
	if m == nil || owner == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	released := 0
	for i := range m.entries {
		if m.entries[i].owner == owner {
			m.entries[i].owner = ""
			released++
		}
	}
	return released
}

func (m *ResultMailbox) pending() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
