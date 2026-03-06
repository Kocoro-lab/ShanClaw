//go:build darwin

package schedule

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// PlistPath returns the default system-level plist path.
// Prefer Manager.plistPath() which respects the configured plistDir.
func PlistPath(id string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistPrefix+"."+id+".plist")
}

func GeneratePlist(id, agent, cron, prompt, shanBin string) string {
	label := plistPrefix + "." + id
	args := []string{shanBin, "-y"}
	if agent != "" {
		args = append(args, "--agent", agent)
	}
	args = append(args, prompt)

	var argXML strings.Builder
	for _, a := range args {
		argXML.WriteString(fmt.Sprintf("\t\t<string>%s</string>\n", xmlEscape(a)))
	}

	calendarInterval := cronToCalendarInterval(cron)

	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".shannon", "logs")
	os.MkdirAll(logDir, 0700)
	logPath := filepath.Join(logDir, "schedule-"+id+".log")

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
%s
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, argXML.String(), calendarInterval, xmlEscape(logPath), xmlEscape(logPath))
}

// cronToCalendarInterval converts a 5-field cron expression to launchd XML.
// For expressions with only simple values and *, uses StartCalendarInterval dict.
// For ranges (1-5) and lists (1,3,5), expands into an array of CalendarInterval dicts.
// For step expressions (*/5), uses StartInterval.
func cronToCalendarInterval(cron string) string {
	fields := strings.Fields(cron)
	if len(fields) != 5 {
		return "\t<key>StartInterval</key>\n\t<integer>3600</integer>"
	}

	// Check if any field uses step syntax — must use StartInterval
	for _, f := range fields {
		if strings.Contains(f, "/") {
			return estimateInterval(fields)
		}
	}

	// Expand each field into a list of integer values (or nil for *)
	keys := []string{"Minute", "Hour", "Day", "Month", "Weekday"}
	expanded := make([][]int, 5)
	for i, field := range fields {
		if field == "*" {
			expanded[i] = nil // nil = wildcard
			continue
		}
		vals, err := expandCronField(field)
		if err != nil {
			return "\t<key>StartInterval</key>\n\t<integer>3600</integer>"
		}
		expanded[i] = vals
	}

	// Build all combinations via cartesian product
	combos := cartesianProduct(expanded)

	if len(combos) == 0 {
		return "\t<key>StartInterval</key>\n\t<integer>60</integer>"
	}

	if len(combos) == 1 {
		// Single dict
		var parts []string
		for i, val := range combos[0] {
			if val < 0 {
				continue // wildcard
			}
			parts = append(parts, fmt.Sprintf("\t\t<key>%s</key>\n\t\t<integer>%d</integer>", keys[i], val))
		}
		if len(parts) == 0 {
			return "\t<key>StartInterval</key>\n\t<integer>60</integer>"
		}
		return "\t<key>StartCalendarInterval</key>\n\t<dict>\n" + strings.Join(parts, "\n") + "\n\t</dict>"
	}

	// Multiple dicts — use array form
	var dicts []string
	for _, combo := range combos {
		var parts []string
		for i, val := range combo {
			if val < 0 {
				continue
			}
			parts = append(parts, fmt.Sprintf("\t\t\t<key>%s</key>\n\t\t\t<integer>%d</integer>", keys[i], val))
		}
		if len(parts) > 0 {
			dicts = append(dicts, "\t\t<dict>\n"+strings.Join(parts, "\n")+"\n\t\t</dict>")
		}
	}
	return "\t<key>StartCalendarInterval</key>\n\t<array>\n" + strings.Join(dicts, "\n") + "\n\t</array>"
}

// expandCronField expands a cron field (e.g., "1-5", "1,3,5", "9") into individual values.
func expandCronField(field string) ([]int, error) {
	var result []int
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err := strconv.Atoi(bounds[0])
			if err != nil {
				return nil, err
			}
			hi, err := strconv.Atoi(bounds[1])
			if err != nil {
				return nil, err
			}
			for v := lo; v <= hi; v++ {
				result = append(result, v)
			}
		} else {
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, err
			}
			result = append(result, v)
		}
	}
	return result, nil
}

// cartesianProduct computes all combinations across fields.
// nil entries are treated as wildcards (represented as -1 in output).
// Caps at 512 combinations to prevent plist explosion.
func cartesianProduct(fields [][]int) [][]int {
	result := [][]int{{}}
	for i, vals := range fields {
		if vals == nil {
			// Wildcard — append -1 sentinel to each existing combo
			for j := range result {
				result[j] = append(result[j], -1)
			}
			continue
		}
		var next [][]int
		for _, combo := range result {
			for _, v := range vals {
				newCombo := make([]int, len(combo), len(combo)+1)
				copy(newCombo, combo)
				next = append(next, append(newCombo, v))
			}
		}
		result = next
		if len(result) > 512 {
			// Too many combinations — fall back
			_ = i
			return nil
		}
	}
	return result
}

func estimateInterval(fields []string) string {
	minute := fields[0]
	if strings.Contains(minute, "/") {
		n, err := strconv.Atoi(strings.TrimPrefix(minute, "*/"))
		if err == nil && n > 0 {
			return fmt.Sprintf("\t<key>StartInterval</key>\n\t<integer>%d</integer>", n*60)
		}
	}
	hour := fields[1]
	if strings.Contains(hour, "/") {
		n, err := strconv.Atoi(strings.TrimPrefix(hour, "*/"))
		if err == nil && n > 0 {
			return fmt.Sprintf("\t<key>StartInterval</key>\n\t<integer>%d</integer>", n*3600)
		}
	}
	return "\t<key>StartInterval</key>\n\t<integer>3600</integer>"
}

func WritePlist(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".plist-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	return os.Rename(tmpPath, path)
}

func RemovePlist(path string) error {
	return os.Remove(path)
}

func LaunchctlLoad(plistPath string) error {
	out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load %s: %w: %s", plistPath, err, string(out))
	}
	return nil
}

func LaunchctlUnload(plistPath string) error {
	out, err := exec.Command("launchctl", "unload", plistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl unload %s: %w: %s", plistPath, err, string(out))
	}
	return nil
}

func ShanBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "shan"
	}
	return exe
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
