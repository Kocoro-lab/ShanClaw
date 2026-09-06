package koe

import "sync"

// NativeFloorEnabled keeps ASR out of playback-overlap admission. With VPIO
// barge-in active, Realtime hears the raw audio and receives only narrow
// turn-control choices for the paused assistant response.
func NativeFloorEnabled() bool { return koeEnvBool("KOE_NATIVE_FLOOR", true) }

func nativeFloorControlEnabled(fullDuplexAEC bool) bool {
	return fullDuplexAEC &&
		koeEnvBool("KOE_VPIO_BARGE_IN", false) &&
		NativeFloorEnabled() &&
		!koeEnvBool("KOE_INTERRUPT_RESPONSE", false)
}

const nativeFloorInstructions = "Decide turn-taking from the user's most recent RAW SPOKEN AUDIO, not from a transcript. Call exactly one function and say nothing. Classify the conversational function, not merely whether speech was detected. Call resume_playback for a brief backchannel, filler, laugh, acknowledgement, or non-directed sound that does not ask Kocoro to stop, change, answer, or act. Clear resume examples include mm-hmm, mhm, uh-huh, hmm, 嗯, 嗯嗯, 对, 好的, うん, はい, laughter, and sighs when they contain no request or correction. Call stop_speaking when the user only wants the current speech to stop while keeping the voice call active: 停, 停一下, 别说了, 闭嘴, stop, stop talking, or shut up. Call end_call only when the user explicitly dismisses Kocoro or ends the entire voice conversation: 退出, 退出吧, 结束通话, 再见, 拜拜, exit, quit, bye, goodbye, or that's all. Call accept_turn for other semantic content that still needs Kocoro to respond or act after the current speech stops: a question, request, correction, or topic change. An explicit whole-conversation dismissal must choose end_call, never accept_turn or resume_playback. If a very short or non-lexical vocalization is ambiguous, prefer resume_playback; if intelligible words may contain a request or correction, prefer accept_turn."

func nativeFloorToolDefs() []ToolDef {
	return []ToolDef{
		{
			Type:        "function",
			Name:        "resume_playback",
			Description: "The overlapping audio is only a backchannel or non-directed sound. Resume the exact paused assistant response and do not reply.",
			Parameters:  obj(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Type:        "function",
			Name:        "stop_speaking",
			Description: "The user wants only the current speech to stop. Discard the paused response, say nothing, and keep the voice call active.",
			Parameters:  obj(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Type:        "function",
			Name:        "accept_turn",
			Description: "The overlapping audio is a genuine question, request, correction, or topic change that needs a response or action. Discard the paused response and handle this turn.",
			Parameters:  obj(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Type:        "function",
			Name:        "end_call",
			Description: "The user explicitly dismisses Kocoro or ends the entire voice conversation. Discard the paused response and terminate the call immediately without speaking.",
			Parameters:  obj(`{"type":"object","properties":{},"required":[]}`),
		},
	}
}

type floorDecision uint8

const (
	floorDecisionNone floorDecision = iota
	floorDecisionResume
	floorDecisionStop
	floorDecisionAccept
	floorDecisionEnd
)

type floorStage uint8

const (
	floorIdle floorStage = iota
	floorPaused
	floorWaitingForJudge
	floorJudging
	floorResuming
	floorStopping
	floorAccepting
	floorEnding
)

type floorToolClaim struct {
	handled   bool
	duplicate bool
	decision  floorDecision
	turnID    int64
	reason    string
}

// nativeFloorController is deliberately semantic-free. It tracks which exact
// assistant response was paused and which narrow judge response may decide its
// fate. The Realtime model makes the semantic decision from raw audio.
type nativeFloorController struct {
	mu               sync.Mutex
	stage            floorStage
	sourceResponseID string
	turnID           int64
	judgeResponseID  string
	claimedCalls     map[string]struct{}
}

func newNativeFloorController() *nativeFloorController { return &nativeFloorController{} }

func (f *nativeFloorController) begin(sourceResponseID string) bool {
	if f == nil || sourceResponseID == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stage != floorIdle {
		return true
	}
	f.stage = floorPaused
	f.sourceResponseID = sourceResponseID
	f.turnID = 0
	f.judgeResponseID = ""
	f.claimedCalls = nil
	return true
}

// noteUserCommit returns true when this committed user turn is the overlapping
// audio currently holding playback and therefore needs the narrow judge.
func (f *nativeFloorController) noteUserCommit(turnID int64) bool {
	if f == nil || turnID <= 0 {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stage != floorPaused {
		return false
	}
	f.turnID = turnID
	f.stage = floorWaitingForJudge
	return true
}

func (f *nativeFloorController) bindJudge(responseID string, turnID int64) bool {
	if f == nil || responseID == "" || turnID <= 0 {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stage != floorWaitingForJudge || f.turnID != turnID {
		return false
	}
	f.judgeResponseID = responseID
	f.claimedCalls = make(map[string]struct{})
	f.stage = floorJudging
	return true
}

func (f *nativeFloorController) awaitingJudge(turnID int64) bool {
	if f == nil || turnID <= 0 {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stage == floorWaitingForJudge && f.turnID == turnID
}

func (f *nativeFloorController) claim(responseID, callID, tool string) floorToolClaim {
	if f == nil || responseID == "" {
		return floorToolClaim{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if responseID != f.judgeResponseID ||
		(f.stage != floorJudging && f.stage != floorResuming && f.stage != floorStopping &&
			f.stage != floorAccepting && f.stage != floorEnding) {
		return floorToolClaim{}
	}
	claim := floorToolClaim{handled: true, turnID: f.turnID}
	fingerprint := callID
	if fingerprint == "" {
		fingerprint = tool
	}
	if _, exists := f.claimedCalls[fingerprint]; exists {
		claim.duplicate = true
		claim.reason = "duplicate_floor_event"
		return claim
	}
	f.claimedCalls[fingerprint] = struct{}{}
	if f.stage != floorJudging {
		claim.reason = "floor_already_decided"
		return claim
	}
	switch tool {
	case "resume_playback":
		f.stage = floorResuming
		claim.decision = floorDecisionResume
	case "stop_speaking":
		f.stage = floorStopping
		claim.decision = floorDecisionStop
	case "accept_turn":
		f.stage = floorAccepting
		claim.decision = floorDecisionAccept
	case "end_call":
		f.stage = floorEnding
		claim.decision = floorDecisionEnd
	default:
		claim.reason = "invalid_floor_tool"
	}
	return claim
}

func (f *nativeFloorController) holdsPlayback() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stage == floorPaused || f.stage == floorWaitingForJudge || f.stage == floorJudging
}

func (f *nativeFloorController) holdsSource(responseID string) bool {
	if f == nil || responseID == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourceResponseID == responseID &&
		(f.stage == floorPaused || f.stage == floorWaitingForJudge || f.stage == floorJudging)
}

// heldSourceID returns the paused assistant response while the controller still
// owns it in any stage — including the instant an accept decision is applied,
// when holdsSource is already false but the interrupted item must be truncated.
func (f *nativeFloorController) heldSourceID() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourceResponseID
}

func (f *nativeFloorController) finishResponse(responseID string) floorDecision {
	if f == nil || responseID == "" {
		return floorDecisionNone
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if responseID != f.judgeResponseID {
		return floorDecisionNone
	}
	decision := floorDecisionNone
	// A judging response that did not produce a valid narrow tool call must never
	// strand paused speech. Decisions already applied at function-call time only
	// need their controller state cleared here.
	if f.stage == floorJudging {
		decision = floorDecisionResume
	}
	f.resetLocked()
	return decision
}

func (f *nativeFloorController) failTurn(turnID int64) floorDecision {
	if f == nil || turnID <= 0 {
		return floorDecisionNone
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.turnID != turnID || f.stage == floorIdle {
		return floorDecisionNone
	}
	f.resetLocked()
	return floorDecisionResume
}

func (f *nativeFloorController) timeoutTurn(turnID int64) (floorDecision, string) {
	if f == nil || turnID <= 0 {
		return floorDecisionNone, ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.turnID != turnID || (f.stage != floorWaitingForJudge && f.stage != floorJudging) {
		return floorDecisionNone, ""
	}
	judgeResponseID := f.judgeResponseID
	f.resetLocked()
	return floorDecisionResume, judgeResponseID
}

func (f *nativeFloorController) abort() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stage == floorIdle {
		return false
	}
	f.resetLocked()
	return true
}

func (f *nativeFloorController) resetLocked() {
	f.stage = floorIdle
	f.sourceResponseID = ""
	f.turnID = 0
	f.judgeResponseID = ""
	f.claimedCalls = nil
}
