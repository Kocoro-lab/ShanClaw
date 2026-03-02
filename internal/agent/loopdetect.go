package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// LoopAction tells the agent loop how to respond to a detection signal.
type LoopAction int

const (
	LoopContinue  LoopAction = iota // proceed normally
	LoopNudge                        // inject "try different approach" message
	LoopForceStop                    // force final response without tools
)

// ToolCallRecord tracks a single tool invocation for loop detection.
type ToolCallRecord struct {
	Name      string
	ArgsHash  string // hex-encoded hash of raw args
	TopicHash string // hex-encoded hash of normalized args (web tools)
	ResultSig string // domain signature from results (web tools)
	IsError   bool
	ErrorSig  string // first 100 chars of error for grouping
}

// LoopDetector uses a sliding window of recent tool calls to detect stuck loops.
//
// Five detectors (checked in order, first match wins):
//   - ConsecutiveDuplicate: back-to-back identical calls (catches web_search→web_search)
//   - ExactDuplicate: same name+argsHash spread across window (catches read→edit→read→edit→read)
//   - SameToolError: same tool returns errors N+ times in window
//   - FamilyNoProgress: web tools in the same family, counted by topic similarity
//     (3 same-topic → nudge, 5 → stronger nudge, 7 → force stop; 7 family calls → force stop)
//   - NoProgress: same tool called M+ times regardless of args (non-GUI only)
//
// Response escalation: threshold = nudge, 2x threshold = force stop.
type LoopDetector struct {
	history     []ToolCallRecord
	historySize int

	consecDupThreshold   int // back-to-back identical calls (default 2)
	exactDupThreshold    int // spread-out identical calls in window (default 3)
	sameToolErrThreshold int
	noProgressThreshold  int

	repeatableTools map[string]bool
}

// GUITools are tools that indicate GUI automation tasks.
// Used by both LoopDetector (exempt from NoProgress) and effectiveMaxIter (higher limit).
var GUITools = map[string]bool{
	"screenshot": true, "computer": true, "applescript": true, "browser": true,
}

// NewLoopDetector creates a detector with production defaults.
func NewLoopDetector() *LoopDetector {
	return &LoopDetector{
		history:              make([]ToolCallRecord, 0, 20),
		historySize:          20,
		consecDupThreshold:   2,
		exactDupThreshold:    3,
		sameToolErrThreshold: 4,
		noProgressThreshold:  8,
		repeatableTools:      GUITools,
	}
}

// Record adds a tool call to the sliding window.
func (ld *LoopDetector) Record(name, argsJSON string, isError bool, errMsg string, resultSig string) {
	topicHash := ""
	if ToolFamilies[name] != "" {
		normalized := normalizeWebQuery(argsJSON)
		if normalized != "" {
			topicHash = hashArgs(normalized)
		}
	}
	rec := ToolCallRecord{
		Name:      name,
		ArgsHash:  hashArgs(argsJSON),
		TopicHash: topicHash,
		ResultSig: resultSig,
		IsError:   isError,
		ErrorSig:  truncateErrSig(errMsg, 100),
	}
	ld.history = append(ld.history, rec)
	if len(ld.history) > ld.historySize {
		ld.history = ld.history[len(ld.history)-ld.historySize:]
	}
}

// Check evaluates all five detectors for the named tool.
// Returns the most severe action and an appropriate message.
func (ld *LoopDetector) Check(name string) (LoopAction, string) {
	if len(ld.history) < 2 {
		return LoopContinue, ""
	}

	// Find latest argsHash for this tool (must be called right after Record).
	var latestHash string
	for i := len(ld.history) - 1; i >= 0; i-- {
		if ld.history[i].Name == name {
			latestHash = ld.history[i].ArgsHash
			break
		}
	}

	// 1a. Consecutive exact duplicate — catches back-to-back identical calls
	// like web_search→web_search. Does NOT fire for read→edit→read patterns
	// because the intervening edit breaks the consecutive run.
	consecCount := 0
	for i := len(ld.history) - 1; i >= 0; i-- {
		if ld.history[i].Name != name || ld.history[i].ArgsHash != latestHash {
			break
		}
		consecCount++
	}
	if consecCount >= ld.consecDupThreshold*2 {
		return LoopForceStop, fmt.Sprintf(
			"You have called %s with identical arguments %d times in a row. Stop retrying and provide your answer now.", name, consecCount)
	}
	if consecCount >= ld.consecDupThreshold {
		return LoopNudge, fmt.Sprintf(
			"You've called %s %d times consecutively with identical arguments. The results won't change. Use the results you already have or try a different approach.", name, consecCount)
	}

	// 1b. Window-based exact duplicate — catches spread-out repeats
	// like read→edit→read→edit→read (same args appearing 3+ times in window).
	dupCount := 0
	if latestHash != "" {
		for _, rec := range ld.history {
			if rec.Name == name && rec.ArgsHash == latestHash {
				dupCount++
			}
		}
	}
	if dupCount >= ld.exactDupThreshold*2 {
		return LoopForceStop, fmt.Sprintf(
			"You have called %s with identical arguments %d times. Stop retrying and provide your answer now.", name, dupCount)
	}
	if dupCount >= ld.exactDupThreshold {
		return LoopNudge, fmt.Sprintf(
			"You've called %s %d times with identical arguments and similar results. Try a fundamentally different approach.", name, dupCount)
	}

	// 2. Same tool error detector: same tool returning errors
	errCount := 0
	var lastErr string
	for _, rec := range ld.history {
		if rec.Name == name && rec.IsError {
			errCount++
			lastErr = rec.ErrorSig
		}
	}
	if errCount >= ld.sameToolErrThreshold*2 {
		return LoopForceStop, fmt.Sprintf(
			"Tool %s has failed %d times. Stop using it and provide your answer now.", name, errCount)
	}
	if errCount >= ld.sameToolErrThreshold {
		return LoopNudge, fmt.Sprintf(
			"Tool %s has failed %d times with: %s. Do NOT retry this tool. Use a different approach or ask the user.", name, errCount, lastErr)
	}

	// 3. Family no-progress: web tools in the same family, counted by topic similarity.
	// Tiered escalation: 3 same-topic → nudge, 5 → stronger nudge, 7 → force stop.
	family := ToolFamilies[name]
	if family != "" {
		latestTopic := ""
		latestResult := ""
		for i := len(ld.history) - 1; i >= 0; i-- {
			if ToolFamilies[ld.history[i].Name] == family {
				if latestTopic == "" && ld.history[i].TopicHash != "" {
					latestTopic = ld.history[i].TopicHash
				}
				if latestResult == "" && ld.history[i].ResultSig != "" {
					latestResult = ld.history[i].ResultSig
				}
				if latestTopic != "" && latestResult != "" {
					break
				}
			}
		}

		familyCount := 0
		sameTopicCount := 0
		sameResultCount := 0
		for _, rec := range ld.history {
			if ToolFamilies[rec.Name] != family {
				continue
			}
			familyCount++
			if latestTopic != "" && rec.TopicHash == latestTopic {
				sameTopicCount++
			}
			if latestResult != "" && rec.ResultSig == latestResult {
				sameResultCount++
			}
		}

		progressCount := sameTopicCount
		if sameResultCount > progressCount {
			progressCount = sameResultCount
		}

		if progressCount >= 7 {
			return LoopForceStop, fmt.Sprintf(
				"You have made %d web calls with %d on the same topic. Return your collected results now.", familyCount, progressCount)
		}
		if progressCount >= 5 {
			return LoopNudge, fmt.Sprintf(
				"You've searched the same topic %d times. Summarize what you've found and present it to the user. Do not search again.", progressCount)
		}
		if progressCount >= 3 {
			return LoopNudge, fmt.Sprintf(
				"You've searched the same topic %d times with similar results. Use the results you already have or try a fundamentally different query.", progressCount)
		}
	}

	// 4. No progress detector: same tool called too many times (skip for GUI tools)
	if !ld.repeatableTools[name] {
		count := 0
		for _, rec := range ld.history {
			if rec.Name == name {
				count++
			}
		}
		if count >= ld.noProgressThreshold*2 {
			return LoopForceStop, fmt.Sprintf(
				"You have called %s %d times without meaningful progress. Provide your answer now.", name, count)
		}
		if count >= ld.noProgressThreshold {
			return LoopNudge, fmt.Sprintf(
				"You've called %s %d times. Summarize what you've learned and try a different approach.", name, count)
		}
	}

	return LoopContinue, ""
}

func hashArgs(args string) string {
	h := sha256.Sum256([]byte(args))
	return hex.EncodeToString(h[:8])
}

func truncateErrSig(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
