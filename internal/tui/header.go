package tui

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Kocoro-lab/shan/internal/session"
)

// ASCII crab — two claw frames for pinch animation.
var crabClawOpen = ` (\)      (/)` // claws open
var crabClawShut = ` (/)      (\` // claws shut
var crabBody = []string{
	`  ( °  ° )`,
	`   )    (`,
	`  /|    |\`,
	` (_|    |_)`,
}

// Color palette for the startup header.
var (
	crabColor   = lipgloss.Color("174") // Claude orange (#d78787) — entire crab
	borderColor = lipgloss.Color("174") // Claude orange — box border
	accentColor = lipgloss.Color("174") // Claude orange — section headers
	dimColor    = lipgloss.Color("243") // medium gray — secondary text
	infoColor   = lipgloss.Color("39")  // blue — activity header
)

const (
	headerTotalFrames = 13 // 6 claw-pinch frames + info reveal
	headerTickMs      = 80 // ms per frame (~1s total)
	headerLeftWidth   = 28 // left column visual width
	clawAnimFrames    = 6  // frames 0-5: claw animation
)

// Tips shown in the info section of the startup header.
var headerTips = []string{
	"Try /research for deep analysis",
	"Use /sessions to resume work",
	"Type /help to see all commands",
	"Use /model to switch model tier",
	"Try /swarm for multi-agent tasks",
}

// headerFrameTick returns a tea.Cmd that sends a headerTickMsg after the tick interval.
func headerFrameTick() tea.Cmd {
	return tea.Tick(time.Duration(headerTickMs)*time.Millisecond, func(time.Time) tea.Msg {
		return headerTickMsg{}
	})
}

// renderStartupHeader builds the animated two-column startup header for the given frame.
// tipIdx and cwd should be pre-computed by the caller (no I/O inside this function).
func renderStartupHeader(frame int, width int, version string, modelTier string, endpoint string, cwd string, sessions []session.SessionSummary, tipIdx int) string {
	if width < 50 {
		width = 50
	}
	if width > 100 {
		width = 100
	}

	innerWidth := width - 2 // inside box borders (│ on each side)
	rightWidth := innerWidth - headerLeftWidth - 1 // -1 for middle divider

	// --- Build left column lines ---
	var leftLines []string

	// Crab: always fully visible, claws alternate during frames 0-5.
	clawLine := crabClawOpen
	if frame < clawAnimFrames && frame%2 == 1 {
		clawLine = crabClawShut
	}
	leftLines = append(leftLines, colorizeCrab(clawLine))
	for _, line := range crabBody {
		leftLines = append(leftLines, colorizeCrab(line))
	}
	leftLines = append(leftLines, "")

	// Model + CWD + Endpoint — always visible.
	modelStyle := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	cwdStyle := lipgloss.NewStyle().Foreground(dimColor)
	epStyle := lipgloss.NewStyle().Foreground(dimColor)
	leftLines = append(leftLines, "  "+modelStyle.Render(modelTier))
	leftLines = append(leftLines, "  "+cwdStyle.Render(truncateStr(cwd, headerLeftWidth-4)))
	leftLines = append(leftLines, "  "+epStyle.Render(truncateStr(endpoint, headerLeftWidth-4)))

	// --- Build right column lines (all immediate) ---
	var rightLines []string

	// Tips.
	tipHeader := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("Tips")
	tipStyle := lipgloss.NewStyle().Foreground(dimColor)
	rightLines = append(rightLines, " "+tipHeader)
	rightLines = append(rightLines, " "+tipStyle.Render(truncateStr(headerTips[tipIdx%len(headerTips)], rightWidth-3)))

	// Divider.
	rightLines = append(rightLines, " "+lipgloss.NewStyle().Foreground(dimColor).Render(strings.Repeat("─", rightWidth-2)))

	// Recent activity.
	actHeader := lipgloss.NewStyle().Foreground(infoColor).Bold(true).Render("Recent activity")
	rightLines = append(rightLines, " "+actHeader)

	if len(sessions) == 0 {
		rightLines = append(rightLines, " "+lipgloss.NewStyle().Foreground(dimColor).Render("No recent sessions"))
	} else {
		s := sessions[0]
		title := truncateStr(s.Title, rightWidth-4)
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		agoStyle := lipgloss.NewStyle().Foreground(dimColor)
		rightLines = append(rightLines, " "+titleStyle.Render(title))
		rightLines = append(rightLines, " "+agoStyle.Render(fmt.Sprintf("%s, %d msgs", timeAgo(s.CreatedAt), s.MsgCount)))
	}

	// Equalize line counts between columns.
	for len(leftLines) < len(rightLines) {
		leftLines = append(leftLines, "")
	}
	for len(rightLines) < len(leftLines) {
		rightLines = append(rightLines, "")
	}

	// --- Assemble box ---
	bdr := lipgloss.NewStyle().Foreground(borderColor)

	var sb strings.Builder

	// Top border with title.
	titlePart := "─ Shannon CLI "
	titleVisWidth := lipgloss.Width(titlePart)
	remaining := innerWidth - titleVisWidth
	if remaining < 0 {
		remaining = 0
	}
	sb.WriteString(bdr.Render("╭"+titlePart+strings.Repeat("─", remaining)+"╮") + "\n")

	// Content rows: │ left │ right │
	divider := bdr.Render("│")
	for i := range leftLines {
		left := padToWidth(leftLines[i], headerLeftWidth)
		right := padToWidth(rightLines[i], rightWidth)
		sb.WriteString(bdr.Render("│") + left + divider + right + bdr.Render("│") + "\n")
	}

	// Bottom border.
	sb.WriteString(bdr.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))

	return sb.String()
}

// colorizeCrab renders a crab line in orange.
func colorizeCrab(line string) string {
	return lipgloss.NewStyle().Foreground(crabColor).Render(line)
}

// padToWidth pads a (possibly ANSI-styled) string so its visible width
// reaches targetWidth. Uses lipgloss.Width which correctly handles
// ANSI escape codes and double-width CJK characters.
func padToWidth(styled string, targetWidth int) string {
	visible := lipgloss.Width(styled)
	if visible >= targetWidth {
		return styled
	}
	return styled + strings.Repeat(" ", targetWidth-visible)
}

// ansiRe matches ANSI escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripAnsi removes ANSI escape codes from a string for width calculation.
func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// truncateStr truncates a string with "..." if it exceeds maxLen.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		maxLen = 4
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

// timeAgo returns a human-readable relative time string.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d min ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	case d < 48*time.Hour:
		return "yesterday"
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%d days ago", days)
	}
}

// pickTipIdx returns a stable random tip index for a session.
func pickTipIdx() int {
	return rand.Intn(len(headerTips))
}
