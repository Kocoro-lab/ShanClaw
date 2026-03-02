package agent

import (
	"fmt"
	"testing"
)

func TestLoopDetector_ConsecutiveDup_Nudge(t *testing.T) {
	ld := NewLoopDetector()

	// 1 call: no trigger
	ld.Record("web_search", `{"q":"test"}`, false, "", "")
	action, _ := ld.Check("web_search")
	if action != LoopContinue {
		t.Errorf("1 call should not trigger, got %v", action)
	}

	// 2nd consecutive identical call: nudge (consecDupThreshold=2)
	ld.Record("web_search", `{"q":"test"}`, false, "", "")
	action, msg := ld.Check("web_search")
	if action != LoopNudge {
		t.Errorf("2 consecutive identical calls should nudge, got %v", action)
	}
	if msg == "" {
		t.Error("nudge should have a message")
	}
}

func TestLoopDetector_ConsecutiveDup_ForceStop(t *testing.T) {
	ld := NewLoopDetector()

	// 4 consecutive identical calls: force stop (2× consecDupThreshold)
	for range 4 {
		ld.Record("web_search", `{"q":"test"}`, false, "", "")
	}
	action, _ := ld.Check("web_search")
	if action != LoopForceStop {
		t.Errorf("4 consecutive identical calls should force stop, got %v", action)
	}
}

func TestLoopDetector_NonConsecutiveDup_NoFalsePositive(t *testing.T) {
	ld := NewLoopDetector()

	// read → edit → read: NOT consecutive, 2 in window < exactDupThreshold(3)
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")
	ld.Record("file_edit", `{"file":"main.go","old":"a","new":"b"}`, false, "", "")
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")

	action, _ := ld.Check("file_read")
	if action != LoopContinue {
		t.Errorf("read-edit-read should not trigger (non-consecutive), got %v", action)
	}
}

func TestLoopDetector_WindowDup_Nudge(t *testing.T) {
	ld := NewLoopDetector()

	// 3 spread-out identical calls: window-based nudge (exactDupThreshold=3)
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")
	ld.Record("file_edit", `{"old":"a","new":"b"}`, false, "", "")
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")
	ld.Record("file_edit", `{"old":"b","new":"c"}`, false, "", "")
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")

	action, _ := ld.Check("file_read")
	if action != LoopNudge {
		t.Errorf("3 spread-out identical calls should trigger window nudge, got %v", action)
	}
}

func TestLoopDetector_WindowDup_ForceStop(t *testing.T) {
	ld := NewLoopDetector()

	// 6 spread-out identical calls: window force stop (2× exactDupThreshold)
	for range 6 {
		ld.Record("file_read", `{"file":"main.go"}`, false, "", "")
		ld.Record("file_edit", `{"x":"y"}`, false, "", "")
	}
	action, _ := ld.Check("file_read")
	if action != LoopForceStop {
		t.Errorf("6 spread-out identical calls should force stop, got %v", action)
	}
}

func TestLoopDetector_SameToolError_Nudge(t *testing.T) {
	ld := NewLoopDetector()

	// 3 errors: no trigger (threshold is 4)
	for i := range 3 {
		ld.Record("file_edit", fmt.Sprintf(`{"file":"f%d"}`, i), true, "permission denied", "")
	}
	action, _ := ld.Check("file_edit")
	if action != LoopContinue {
		t.Errorf("3 errors should not trigger, got %v", action)
	}

	// 4th error: nudge
	ld.Record("file_edit", `{"file":"f4"}`, true, "permission denied", "")
	action, msg := ld.Check("file_edit")
	if action != LoopNudge {
		t.Errorf("4 errors should trigger nudge, got %v", action)
	}
	if msg == "" {
		t.Error("nudge should have a message")
	}
}

func TestLoopDetector_SameToolError_ForceStop(t *testing.T) {
	ld := NewLoopDetector()

	// 8 errors: force stop (2× threshold of 4)
	for i := range 8 {
		ld.Record("file_edit", fmt.Sprintf(`{"file":"f%d"}`, i), true, "permission denied", "")
	}
	action, _ := ld.Check("file_edit")
	if action != LoopForceStop {
		t.Errorf("8 errors should trigger force stop, got %v", action)
	}
}

func TestLoopDetector_NoProgress_Nudge(t *testing.T) {
	ld := NewLoopDetector()

	// 7 calls with different args: no trigger (threshold is 8)
	for i := range 7 {
		ld.Record("grep", fmt.Sprintf(`{"pattern":"p%d"}`, i), false, "", "")
	}
	action, _ := ld.Check("grep")
	if action != LoopContinue {
		t.Errorf("7 calls should not trigger, got %v", action)
	}

	// 8th call: nudge
	ld.Record("grep", `{"pattern":"p8"}`, false, "", "")
	action, _ = ld.Check("grep")
	if action != LoopNudge {
		t.Errorf("8 calls should trigger nudge, got %v", action)
	}
}

func TestLoopDetector_GUIExemptFromNoProgress(t *testing.T) {
	ld := NewLoopDetector()

	// 10 screenshot calls with different args: should NOT trigger NoProgress
	for i := range 10 {
		ld.Record("screenshot", fmt.Sprintf(`{"delay":%d}`, i), false, "", "")
	}
	action, _ := ld.Check("screenshot")
	if action != LoopContinue {
		t.Errorf("screenshot should be exempt from NoProgress, got %v", action)
	}
}

func TestLoopDetector_GUIConsecutiveDupStillDetected(t *testing.T) {
	ld := NewLoopDetector()

	// Even GUI tools should trigger consecutive-duplicate detection
	ld.Record("screenshot", `{}`, false, "", "")
	ld.Record("screenshot", `{}`, false, "", "")
	action, _ := ld.Check("screenshot")
	if action != LoopNudge {
		t.Errorf("2 consecutive identical screenshot calls should nudge, got %v", action)
	}
}

func TestLoopDetector_SlidingWindow(t *testing.T) {
	ld := NewLoopDetector()
	ld.historySize = 5 // small window for testing

	// Fill window with 2 consecutive bash duplicates (triggers consecutive nudge)
	ld.Record("bash", `{"cmd":"ls"}`, false, "", "")
	ld.Record("bash", `{"cmd":"ls"}`, false, "", "")
	action, _ := ld.Check("bash")
	if action != LoopNudge {
		t.Error("2 consecutive exact dups should nudge")
	}

	// Push old records out of window with 5 different calls
	for i := range 5 {
		ld.Record("file_read", fmt.Sprintf(`{"file":"f%d"}`, i), false, "", "")
	}

	// bash dups should have fallen out of window
	action, _ = ld.Check("bash")
	if action != LoopContinue {
		t.Error("old records should have fallen out of sliding window")
	}
}

func TestLoopDetector_MixedWorkflow_NoFalsePositive(t *testing.T) {
	ld := NewLoopDetector()

	// Normal coding workflow: read, edit, read, edit, bash
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")
	ld.Record("file_edit", `{"file":"main.go","old":"a","new":"b"}`, false, "", "")
	ld.Record("file_read", `{"file":"main.go"}`, false, "", "")
	ld.Record("file_edit", `{"file":"main.go","old":"b","new":"c"}`, false, "", "")
	ld.Record("bash", `{"cmd":"go test"}`, false, "", "")

	for _, name := range []string{"file_read", "file_edit", "bash"} {
		action, _ := ld.Check(name)
		if action != LoopContinue {
			t.Errorf("normal workflow should not trigger for %s, got %v", name, action)
		}
	}
}

func TestLoopDetector_DifferentArgsNoDuplicate(t *testing.T) {
	ld := NewLoopDetector()

	// Same tool, different args each time — should not trigger
	for i := range 5 {
		ld.Record("file_read", fmt.Sprintf(`{"file":"file%d.go"}`, i), false, "", "")
	}
	action, _ := ld.Check("file_read")
	if action != LoopContinue {
		t.Errorf("different args should not trigger, got %v", action)
	}
}

func TestLoopDetector_ErrorsOnlyCountForSameTool(t *testing.T) {
	ld := NewLoopDetector()

	// Errors spread across different tools: no trigger for any single tool
	ld.Record("bash", `{"cmd":"a"}`, true, "fail", "")
	ld.Record("file_edit", `{"a":"b"}`, true, "fail", "")
	ld.Record("grep", `{"p":"c"}`, true, "fail", "")
	ld.Record("bash", `{"cmd":"b"}`, true, "fail", "")
	ld.Record("file_edit", `{"a":"c"}`, true, "fail", "")

	for _, name := range []string{"bash", "file_edit", "grep"} {
		action, _ := ld.Check(name)
		if action != LoopContinue {
			t.Errorf("spread errors should not trigger for %s, got %v", name, action)
		}
	}
}

func TestLoopDetector_WebFamily_SameTopicNudge(t *testing.T) {
	ld := NewLoopDetector()
	// 3 web_search calls with varied but same-topic queries → family nudge at 3
	ld.Record("web_search", `{"query":"world climate today March 2 2026 major headlines"}`, false, "", "")
	ld.Record("web_search", `{"query":"world climate March 2 2026 top headlines latest"}`, false, "", "")
	ld.Record("web_search", `{"query":"world climate today March 2 2026 breaking news"}`, false, "", "")
	action, msg := ld.Check("web_search")
	if action != LoopNudge {
		t.Errorf("3 same-topic web searches should nudge, got %v", action)
	}
	if msg == "" {
		t.Error("nudge should have a message")
	}
}

func TestLoopDetector_WebFamily_CrossToolCounting(t *testing.T) {
	ld := NewLoopDetector()
	// web_search x2 + web_fetch x1 = 3 web family calls
	// Use queries that normalize to the same topic
	ld.Record("web_search", `{"query":"golang tutorial 2026"}`, false, "", "")
	ld.Record("web_search", `{"query":"golang tutorial latest"}`, false, "", "")
	// web_fetch with URL — topic hash will differ, but result sig can match
	ld.Record("web_fetch", `{"url":"https://go.dev/doc/tutorial"}`, false, "", "go.dev")
	// Note: This test checks family count. With familyCount=3 but no matching topic/result,
	// it should NOT trigger (need 7 family calls without topic match, or 3 with topic match).
	// The web_fetch URL normalizes differently from the search queries.
	action, _ := ld.Check("web_fetch")
	// familyCount=3 but progressCount<3 (different topics), so no trigger
	if action != LoopContinue {
		t.Errorf("3 web family calls with different topics should continue, got %v", action)
	}
}

func TestLoopDetector_WebFamily_ResultSigDedup(t *testing.T) {
	ld := NewLoopDetector()
	// 3 calls returning the same domains → no new info → nudge
	ld.Record("web_search", `{"query":"ai research papers"}`, false, "", "reuters.com,bbc.com")
	ld.Record("web_search", `{"query":"ai research latest papers"}`, false, "", "reuters.com,bbc.com")
	ld.Record("web_search", `{"query":"ai research papers review"}`, false, "", "reuters.com,bbc.com")
	action, _ := ld.Check("web_search")
	if action != LoopNudge {
		t.Errorf("3 calls with same result signature should nudge, got %v", action)
	}
}

func TestLoopDetector_WebFamily_ForceStopAt7(t *testing.T) {
	ld := NewLoopDetector()
	// 7 web calls with same topic → force stop
	for i := 0; i < 7; i++ {
		ld.Record("web_search", `{"query":"climate change report"}`, false, "", "")
	}
	action, _ := ld.Check("web_search")
	if action != LoopForceStop {
		t.Errorf("7 same-topic web calls should force stop, got %v", action)
	}
}

func TestLoopDetector_WebFamily_7FamilyCallsForceStop(t *testing.T) {
	ld := NewLoopDetector()
	// 7 web family calls total (mixed tools, different topics) → force stop
	for i := 0; i < 4; i++ {
		ld.Record("web_search", fmt.Sprintf(`{"query":"topic%d search"}`, i), false, "", "")
	}
	for i := 0; i < 3; i++ {
		ld.Record("web_fetch", fmt.Sprintf(`{"url":"https://example%d.com/page"}`, i), false, "", "")
	}
	action, _ := ld.Check("web_fetch")
	if action != LoopForceStop {
		t.Errorf("7 web family calls should force stop regardless of topic, got %v", action)
	}
}

func TestLoopDetector_WebFamily_DifferentTopicsUnder7(t *testing.T) {
	ld := NewLoopDetector()
	// 4 web calls with different topics — should NOT trigger (under 7 total, no topic match)
	ld.Record("web_search", `{"query":"golang concurrency patterns"}`, false, "", "")
	ld.Record("web_search", `{"query":"python machine learning tutorial"}`, false, "", "")
	ld.Record("web_search", `{"query":"rust ownership explained"}`, false, "", "")
	ld.Record("web_search", `{"query":"javascript async await"}`, false, "", "")
	action, _ := ld.Check("web_search")
	if action != LoopContinue {
		t.Errorf("4 different-topic web calls should continue, got %v", action)
	}
}

func TestLoopDetector_NonWebToolUnchanged(t *testing.T) {
	ld := NewLoopDetector()
	// 5 file_read calls with different args — should NOT trigger (threshold still 8)
	for i := 0; i < 5; i++ {
		ld.Record("file_read", fmt.Sprintf(`{"file":"file%d.go"}`, i), false, "", "")
	}
	action, _ := ld.Check("file_read")
	if action != LoopContinue {
		t.Errorf("5 file_read calls should not trigger (threshold 8), got %v", action)
	}
}

// TestLoopDetector_RealWorldWebLoop replays the actual bug that prompted this fix:
// 8 web_search calls with varied "world news" queries, then web_fetch calls.
// The detector should catch it much earlier than the original ~15 calls.
func TestLoopDetector_RealWorldWebLoop(t *testing.T) {
	ld := NewLoopDetector()

	searches := []string{
		`{"query":"world news today March 2 2026"}`,
		`{"query":"world news today March 2 2026 major headlines"}`,
		`{"query":"world news March 2 2026 top headlines Reuters BBC Al Jazeera"}`,
		`{"query":"world news today March 2 2026 top headlines Reuters AP BBC"}`,
		`{"query":"world news March 2 2026 Reuters AP BBC Al Jazeera"}`,
		`{"query":"world news March 2 2026 top headlines"}`,
		`{"query":"world news today March 2 2026 top headlines"}`,
		`{"query":"world news March 2 2026 top headlines Reuters AP BBC Al Jazeera CNN"}`,
	}

	var firstNudge, firstForceStop int
	for i, args := range searches {
		ld.Record("web_search", args, false, "", "reuters.com,bbc.com")
		action, _ := ld.Check("web_search")
		if action == LoopNudge && firstNudge == 0 {
			firstNudge = i + 1
		}
		if action == LoopForceStop && firstForceStop == 0 {
			firstForceStop = i + 1
		}
	}

	if firstNudge == 0 || firstNudge > 3 {
		t.Errorf("expected first nudge by call 3, got %d", firstNudge)
	}
	if firstForceStop == 0 || firstForceStop > 7 {
		t.Errorf("expected force stop by call 7, got %d", firstForceStop)
	}
}

// TestLoopDetector_RealWorldWebLoop_CrossTool verifies that switching from
// web_search to web_fetch doesn't reset the family counter.
func TestLoopDetector_RealWorldWebLoop_CrossTool(t *testing.T) {
	ld := NewLoopDetector()

	// 3 searches on same topic
	ld.Record("web_search", `{"query":"world climate today March 2 2026"}`, false, "", "reuters.com")
	ld.Record("web_search", `{"query":"world climate March 2 2026 latest"}`, false, "", "reuters.com")
	ld.Record("web_search", `{"query":"world climate today latest headlines"}`, false, "", "reuters.com")

	// Should already be nudging
	action, _ := ld.Check("web_search")
	if action != LoopNudge {
		t.Errorf("expected nudge after 3 same-topic searches, got %v", action)
	}

	// Switch to web_fetch — family counter should continue
	ld.Record("web_fetch", `{"url":"https://reuters.com/world/climate"}`, false, "", "reuters.com")
	ld.Record("web_fetch", `{"url":"https://bbc.com/news/climate"}`, false, "", "reuters.com,bbc.com")
	ld.Record("web_fetch", `{"url":"https://aljazeera.com/climate"}`, false, "", "reuters.com,bbc.com")
	ld.Record("web_fetch", `{"url":"https://cnn.com/climate"}`, false, "", "reuters.com,bbc.com")

	// 7 total web family calls → force stop
	action, _ = ld.Check("web_fetch")
	if action != LoopForceStop {
		t.Errorf("expected force stop after 7 web family calls, got %v", action)
	}
}
