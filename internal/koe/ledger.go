package koe

import (
	"fmt"
	"sort"

	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
)

type VoiceTaskState string

const (
	TaskRunning   VoiceTaskState = "running"
	TaskCompleted VoiceTaskState = "completed"
	TaskFailed    VoiceTaskState = "failed"
	TaskCancelled VoiceTaskState = "cancelled"
)

// VoiceTask is one call-scoped task lineage. All instances returned by
// CallState are detached snapshots; the mutable ledger-owned copy is guarded by
// CallState.mu.
type VoiceTask struct {
	ID       string
	Label    string
	Agent    string
	ThreadID string
	State    VoiceTaskState

	Revision          int
	DeliveredRevision int
	Reply             string
	Deliverables      []Deliverable
	FailReason        string

	LogicalTaskID string
	ExecutionRuns []executionprofile.Run
}

// TaskLedgerEnabled keeps the ledger and multi-lane tool schema independently
// rollbackable while the native S2S control loop is fielded.
func TaskLedgerEnabled() bool { return koeEnvBool("KOE_TASK_LEDGER", true) }

// BeginTask allocates a stable task id and daemon lane. Sequential work reuses
// the main burst lane for context continuity; a truly concurrent task for the
// same agent receives its own sub-lane.
func (s *CallState) BeginTask(label, agent string) *VoiceTask {
	return s.BeginTaskWithMode(label, agent, executionprofile.ModeFull)
}

func pendingVoiceProfile(mode executionprofile.Mode) executionprofile.Profile {
	mode = executionprofile.NormalizeMode(string(mode))
	if mode == executionprofile.ModeFull {
		return executionprofile.FullProfile(mode, "requested_full")
	}
	return executionprofile.Profile{
		RequestedMode:    mode,
		ResolutionReason: "pending_cloud_resolution",
	}
}

func appendVoiceExecutionRun(task *VoiceTask, requested executionprofile.Mode, parentRunID string) {
	runSeq := len(task.ExecutionRuns) + 1
	task.ExecutionRuns = append(task.ExecutionRuns, executionprofile.Run{
		LogicalTaskID: task.LogicalTaskID,
		RunID:         fmt.Sprintf("%s.r%02d", task.LogicalTaskID, runSeq),
		ParentRunID:   parentRunID,
		Profile:       pendingVoiceProfile(requested),
	})
}

func (s *CallState) BeginTaskWithMode(label, agent string, mode executionprofile.Mode) *VoiceTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskSeq++
	id := fmt.Sprintf("t%02d", s.taskSeq)
	task := &VoiceTask{
		ID:            id,
		Label:         label,
		Agent:         agent,
		ThreadID:      s.burstID,
		State:         TaskRunning,
		Revision:      1,
		LogicalTaskID: s.burstID + ":" + id,
	}
	appendVoiceExecutionRun(task, mode, "")
	for _, other := range s.tasks {
		if other.State == TaskRunning && other.Agent == agent && other.ThreadID == s.burstID {
			task.ThreadID = s.burstID + "." + task.ID
			break
		}
	}
	if s.tasks == nil {
		s.tasks = make(map[string]*VoiceTask)
	}
	s.tasks[task.ID] = task
	snapshot := cloneVoiceTask(task)
	return &snapshot
}

func (s *CallState) BeginFollowUp(taskID, label string) (*VoiceTask, bool) {
	return s.BeginFollowUpWithMode(taskID, label, executionprofile.ModeFull)
}

func (s *CallState) BeginFollowUpWithMode(taskID, label string, mode executionprofile.Mode) (*VoiceTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, false
	}
	wasRunning := task.State == TaskRunning
	requested := executionprofile.NormalizeMode(string(mode))
	var current executionprofile.Run
	if len(task.ExecutionRuns) > 0 {
		current = task.ExecutionRuns[len(task.ExecutionRuns)-1]
	}
	currentMode := current.Profile.EffectiveMode
	if currentMode == "" {
		currentMode = current.Profile.RequestedMode
	}
	needsChild := !wasRunning || (currentMode == executionprofile.ModeFast && requested == executionprofile.ModeFull)
	if needsChild {
		nextMode := requested
		if currentMode == executionprofile.ModeFull && requested == executionprofile.ModeFast {
			nextMode = executionprofile.ModeFull
		}
		appendVoiceExecutionRun(task, nextMode, current.RunID)
		if nextMode == executionprofile.ModeFull && requested == executionprofile.ModeFast {
			last := &task.ExecutionRuns[len(task.ExecutionRuns)-1]
			last.Profile.RequestedMode = requested
			last.Profile.EffectiveMode = executionprofile.ModeFull
			last.Profile.ResolutionReason = "lineage_full_preserved"
		}
	}
	task.Revision++
	task.Label = label
	task.State = TaskRunning
	snapshot := cloneVoiceTask(task)
	return &snapshot, true
}

func (task *VoiceTask) CurrentExecutionRun() executionprofile.Run {
	if task == nil || len(task.ExecutionRuns) == 0 {
		return executionprofile.Run{}
	}
	return cloneVoiceExecutionRun(task.ExecutionRuns[len(task.ExecutionRuns)-1])
}

func (task *VoiceTask) CurrentExecutionMode() executionprofile.Mode {
	run := task.CurrentExecutionRun()
	if run.Profile.EffectiveMode == executionprofile.ModeFull {
		return executionprofile.ModeFull
	}
	return executionprofile.NormalizeMode(string(run.Profile.RequestedMode))
}

// RecordExecutionRun resolves a provisional voice-side generation exactly
// once. Repeated identical evidence is idempotent; conflicting evidence is
// rejected so a live run cannot silently change provider/profile.
func (s *CallState) RecordExecutionRun(taskID string, resolved executionprofile.Run) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok || resolved.RunID == "" {
		return false
	}
	for i := range task.ExecutionRuns {
		current := &task.ExecutionRuns[i]
		if current.RunID != resolved.RunID {
			continue
		}
		if current.Profile.EffectiveMode != "" {
			return current.LogicalTaskID == resolved.LogicalTaskID &&
				current.ParentRunID == resolved.ParentRunID &&
				current.Profile == resolved.Profile
		}
		if current.LogicalTaskID != resolved.LogicalTaskID || current.ParentRunID != resolved.ParentRunID {
			return false
		}
		if err := resolved.ValidatePersisted(); err != nil {
			return false
		}
		*current = cloneVoiceExecutionRun(resolved)
		return true
	}
	if len(task.ExecutionRuns) == 0 {
		return false
	}
	current := task.ExecutionRuns[len(task.ExecutionRuns)-1]
	if resolved.ParentRunID != current.RunID ||
		resolved.LogicalTaskID != current.LogicalTaskID ||
		resolved.RunID == current.RunID {
		return false
	}
	if err := resolved.ValidatePersisted(); err != nil {
		return false
	}
	if current.Profile.EffectiveMode == executionprofile.ModeFull &&
		resolved.Profile.EffectiveMode != executionprofile.ModeFull {
		return false
	}
	task.ExecutionRuns = append(task.ExecutionRuns, cloneVoiceExecutionRun(resolved))
	return true
}

// LandResultForRun applies a terminal result only when it belongs to the task's
// current execution generation. A fast parent can finish while its full child
// is waiting at the daemon route boundary; that stale parent result remains in
// the Desktop session but must not overwrite or be voiced as the child's result.
// The final bool reports whether the generation was current.
func (s *CallState) LandResultForRun(taskID, runID string, result SayResult) (VoiceTask, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return VoiceTask{}, false, false
	}
	current := task.CurrentExecutionRun()
	if runID != "" && current.RunID != "" && runID != current.RunID {
		return cloneVoiceTask(task), false, false
	}
	switch result.Status {
	case "injected":
		return cloneVoiceTask(task), false, true
	case "ok":
		task.State = TaskCompleted
	case "cancelled":
		task.State = TaskCancelled
	default:
		task.State = TaskFailed
	}
	supersedes := task.DeliveredRevision > 0 && task.Revision > task.DeliveredRevision
	task.DeliveredRevision = task.Revision
	if result.Reply != "" {
		task.Reply = result.Reply
	}
	task.Deliverables = append([]Deliverable(nil), result.Deliverables...)
	task.FailReason = result.FailReason
	return cloneVoiceTask(task), supersedes, true
}

func (s *CallState) LandResult(taskID string, result SayResult) (VoiceTask, bool) {
	landed, supersedes, _ := s.LandResultForRun(taskID, "", result)
	return landed, supersedes
}

// MarkFailed terminates a running task whose daemon run finished but whose
// result is undeliverable (e.g. the ledger refused a conflicting execution-run
// identity). Without a terminal transition the task counts as running forever
// and taskInFlight()-gated cleanup never fires.
func (s *CallState) MarkFailed(taskID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[taskID]; ok && task.State == TaskRunning {
		task.State = TaskFailed
		task.FailReason = reason
	}
}

func (s *CallState) MarkCancelled(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[taskID]; ok && task.State == TaskRunning {
		task.State = TaskCancelled
	}
}

func (s *CallState) RunningMainLaneTask(agent string) *VoiceTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if task.State == TaskRunning && task.Agent == agent && task.ThreadID == s.burstID {
			copy := cloneVoiceTask(task)
			return &copy
		}
	}
	return nil
}

func (s *CallState) TaskByID(id string) (VoiceTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id]; ok {
		return cloneVoiceTask(task), true
	}
	return VoiceTask{}, false
}

func (s *CallState) RunningTasks() []VoiceTask {
	return s.tasksWhere(func(task *VoiceTask) bool { return task.State == TaskRunning })
}

func (s *CallState) RunningTasksForAgent(agent string) []VoiceTask {
	return s.tasksWhere(func(task *VoiceTask) bool {
		return task.State == TaskRunning && task.Agent == agent
	})
}

func (s *CallState) AnyRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if task.State == TaskRunning {
			return true
		}
	}
	return false
}

func (s *CallState) HasTasks() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks) > 0
}

func (s *CallState) AllTasks() []VoiceTask {
	return s.tasksWhere(func(*VoiceTask) bool { return true })
}

func (s *CallState) tasksWhere(keep func(*VoiceTask) bool) []VoiceTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]VoiceTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		if keep(task) {
			out = append(out, cloneVoiceTask(task))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].ID) != len(out[j].ID) {
			return len(out[i].ID) < len(out[j].ID)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func cloneVoiceTask(task *VoiceTask) VoiceTask {
	if task == nil {
		return VoiceTask{}
	}
	copy := *task
	copy.Deliverables = append([]Deliverable(nil), task.Deliverables...)
	copy.ExecutionRuns = make([]executionprofile.Run, len(task.ExecutionRuns))
	for i := range task.ExecutionRuns {
		copy.ExecutionRuns[i] = cloneVoiceExecutionRun(task.ExecutionRuns[i])
	}
	return copy
}

func cloneVoiceExecutionRun(run executionprofile.Run) executionprofile.Run {
	if run.ComputerActivation != nil {
		activation := *run.ComputerActivation
		run.ComputerActivation = &activation
	}
	run.Evidence.ToolOutcomes = append(
		[]executionprofile.ToolOutcomeEvidence(nil), run.Evidence.ToolOutcomes...)
	run.Evidence.Deliverables = append(
		[]executionprofile.DeliverableEvidence(nil), run.Evidence.Deliverables...)
	return run
}
