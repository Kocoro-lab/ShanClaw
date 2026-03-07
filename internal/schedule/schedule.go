package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kocoro-lab/shan/internal/agents"
	"github.com/adhocore/gronx"
)

const plistPrefix = "com.shannon.schedule"

type Schedule struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`
	Cron       string    `json:"cron"`
	Prompt     string    `json:"prompt"`
	Enabled    bool      `json:"enabled"`
	SyncStatus string    `json:"sync_status"`
	CreatedAt  time.Time `json:"created_at"`
}

type UpdateOpts struct {
	Cron    *string
	Prompt  *string
	Enabled *bool
}

type Manager struct {
	indexPath string
	plistDir  string
}

func NewManager(indexPath, plistDir string) *Manager {
	return &Manager{indexPath: indexPath, plistDir: plistDir}
}

// plistPath returns the plist path scoped to this Manager's plistDir.
// In production, plistDir is ~/Library/LaunchAgents. In tests, it's a temp dir.
func (m *Manager) plistPath(id string) string {
	return filepath.Join(m.plistDir, plistPrefix+"."+id+".plist")
}

func validateCron(expr string) error {
	g := gronx.New()
	if !g.IsValid(expr) {
		return fmt.Errorf("invalid cron expression: %q", expr)
	}
	if err := ValidateCronComplexity(expr); err != nil {
		return err
	}
	return nil
}

// ValidateCronComplexity checks that a cron expression doesn't produce too many
// launchd CalendarInterval combinations. Step expressions (*/N) are always fine.
func ValidateCronComplexity(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil // will be caught by gronx
	}
	// Step expressions use StartInterval — no combination limit
	for _, f := range fields {
		if strings.Contains(f, "/") {
			return nil
		}
	}
	// Count total combinations
	total := 1
	for _, field := range fields {
		if field == "*" {
			continue
		}
		count := 0
		for _, part := range strings.Split(field, ",") {
			part = strings.TrimSpace(part)
			if strings.Contains(part, "-") {
				bounds := strings.SplitN(part, "-", 2)
				lo, err1 := strconv.Atoi(bounds[0])
				hi, err2 := strconv.Atoi(bounds[1])
				if err1 != nil || err2 != nil {
					return nil // gronx will catch syntax errors
				}
				count += hi - lo + 1
			} else {
				count++
			}
		}
		total *= count
		if total > 512 {
			return fmt.Errorf("cron expression %q produces too many combinations (%d+) for launchd; simplify or use step syntax (*/N)", expr, total)
		}
	}
	return nil
}

func validateAgent(name string) error {
	if name == "" {
		return nil
	}
	return agents.ValidateAgentName(name)
}

func validatePrompt(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("prompt cannot be empty")
	}
	if strings.ContainsRune(prompt, 0) {
		return fmt.Errorf("prompt contains null bytes")
	}
	return nil
}

func (m *Manager) load() ([]Schedule, error) {
	f, err := os.Open(m.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return nil, fmt.Errorf("flock shared: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	var schedules []Schedule
	if err := json.NewDecoder(f).Decode(&schedules); err != nil {
		return nil, err
	}
	return schedules, nil
}

func (m *Manager) save(schedules []Schedule) error {
	dir := filepath.Dir(m.indexPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".schedules-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if err := syscall.Flock(int(tmp.Fd()), syscall.LOCK_EX); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("flock exclusive: %w", err)
	}
	data, err := json.MarshalIndent(schedules, "", "  ")
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	if err := os.Rename(tmpPath, m.indexPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

func (m *Manager) lockedModify(fn func([]Schedule) ([]Schedule, error)) error {
	dir := filepath.Dir(m.indexPath)
	os.MkdirAll(dir, 0700)
	lockPath := m.indexPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer lockFile.Close()
	// Do NOT os.Remove the lock file — concurrent goroutines may flock
	// on different inodes if the file is deleted and recreated between them.
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	var schedules []Schedule
	if data, err := os.ReadFile(m.indexPath); err == nil {
		json.Unmarshal(data, &schedules)
	}
	schedules, err = fn(schedules)
	if err != nil {
		return err
	}
	return m.save(schedules)
}

func (m *Manager) Create(agentName, cron, prompt string) (string, error) {
	if err := validateAgent(agentName); err != nil {
		return "", err
	}
	if err := validateCron(cron); err != nil {
		return "", err
	}
	if err := validatePrompt(prompt); err != nil {
		return "", err
	}
	id := generateScheduleID()
	s := Schedule{
		ID: id, Agent: agentName, Cron: cron, Prompt: prompt,
		Enabled: true, SyncStatus: "pending", CreatedAt: time.Now(),
	}
	err := m.lockedModify(func(schedules []Schedule) ([]Schedule, error) {
		return append(schedules, s), nil
	})
	if err != nil {
		return "", err
	}

	// Generate and load plist
	plist := GeneratePlist(id, agentName, cron, prompt, ShanBinary())
	plistPath := m.plistPath(id)
	if err := WritePlist(plistPath, plist); err != nil {
		m.SetSyncStatus(id, "failed")
		return id, fmt.Errorf("schedule created but plist write failed: %w", err)
	}
	if err := LaunchctlLoad(plistPath); err != nil {
		m.SetSyncStatus(id, "failed")
		return id, fmt.Errorf("schedule created but launchctl load failed: %w", err)
	}
	m.SetSyncStatus(id, "ok")

	return id, nil
}

func (m *Manager) List() ([]Schedule, error) {
	return m.load()
}

func (m *Manager) Get(id string) (*Schedule, error) {
	schedules, err := m.load()
	if err != nil {
		return nil, err
	}
	for _, s := range schedules {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("schedule %q not found", id)
}

func (m *Manager) Update(id string, opts *UpdateOpts) error {
	if opts.Cron == nil && opts.Prompt == nil && opts.Enabled == nil {
		return fmt.Errorf("no fields to update")
	}
	if opts.Cron != nil {
		if err := validateCron(*opts.Cron); err != nil {
			return err
		}
	}
	if opts.Prompt != nil {
		if err := validatePrompt(*opts.Prompt); err != nil {
			return err
		}
	}
	err := m.lockedModify(func(schedules []Schedule) ([]Schedule, error) {
		for i, s := range schedules {
			if s.ID == id {
				if opts.Cron != nil {
					schedules[i].Cron = *opts.Cron
				}
				if opts.Prompt != nil {
					schedules[i].Prompt = *opts.Prompt
				}
				if opts.Enabled != nil {
					schedules[i].Enabled = *opts.Enabled
				}
				schedules[i].SyncStatus = "pending"
				return schedules, nil
			}
		}
		return nil, fmt.Errorf("schedule %q not found", id)
	})
	if err != nil {
		return err
	}

	// Resync plist after update
	s, getErr := m.Get(id)
	if getErr != nil {
		return nil // index updated, plist sync can happen via `shan schedule sync`
	}
	plistPath := m.plistPath(id)
	if s.Enabled {
		plist := GeneratePlist(id, s.Agent, s.Cron, s.Prompt, ShanBinary())
		LaunchctlUnload(plistPath)
		if err := WritePlist(plistPath, plist); err != nil {
			m.SetSyncStatus(id, "failed")
			return nil
		}
		if err := LaunchctlLoad(plistPath); err != nil {
			m.SetSyncStatus(id, "failed")
			return nil
		}
	} else {
		LaunchctlUnload(plistPath)
	}
	m.SetSyncStatus(id, "ok")
	return nil
}

func (m *Manager) Remove(id string) error {
	// Unload and remove plist before modifying index (best-effort)
	plistPath := m.plistPath(id)
	LaunchctlUnload(plistPath)
	RemovePlist(plistPath)

	return m.lockedModify(func(schedules []Schedule) ([]Schedule, error) {
		filtered := make([]Schedule, 0, len(schedules))
		found := false
		for _, s := range schedules {
			if s.ID == id {
				found = true
				continue
			}
			filtered = append(filtered, s)
		}
		if !found {
			return nil, fmt.Errorf("schedule %q not found", id)
		}
		return filtered, nil
	})
}

func (m *Manager) SetSyncStatus(id, status string) error {
	return m.lockedModify(func(schedules []Schedule) ([]Schedule, error) {
		for i, s := range schedules {
			if s.ID == id {
				schedules[i].SyncStatus = status
				return schedules, nil
			}
		}
		return schedules, nil
	})
}

func (m *Manager) Sync() (int, error) {
	schedules, err := m.load()
	if err != nil {
		return 0, err
	}
	synced := 0
	for _, s := range schedules {
		if s.SyncStatus == "ok" {
			continue
		}
		if !s.Enabled {
			LaunchctlUnload(m.plistPath(s.ID))
			m.SetSyncStatus(s.ID, "ok")
			synced++
			continue
		}
		plist := GeneratePlist(s.ID, s.Agent, s.Cron, s.Prompt, ShanBinary())
		plistPath := m.plistPath(s.ID)
		if err := WritePlist(plistPath, plist); err != nil {
			continue
		}
		LaunchctlUnload(plistPath)
		if err := LaunchctlLoad(plistPath); err != nil {
			continue
		}
		m.SetSyncStatus(s.ID, "ok")
		synced++
	}
	return synced, nil
}

func generateScheduleID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
