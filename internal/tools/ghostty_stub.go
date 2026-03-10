//go:build !darwin

package tools

import "fmt"

const minGhosttyVersion = "1.3.0"

var errNotDarwin = fmt.Errorf("ghostty integration requires macOS")

func ghosttyAvailable() bool                                                   { return false }
func GhosttyAvailable() bool                                                   { return false }
func ghosttyNewTab(command, title, color string) (int, int, error)             { return 0, 0, errNotDarwin }
func ghosttyNewSplit(direction, command, title, color string) (int, int, error) { return 0, 0, errNotDarwin }
func ghosttySendInput(windowIdx, tabIdx int, text string) error                { return errNotDarwin }
func SetGhosttyTabAppearance(agentName string)                                 {}
func ghosttyWorkspaceScript(shanBinary string, agentNames []string) string     { return "" }
func GhosttyWorkspaceScript(shanBinary string, agentNames []string) string     { return "" }
func ExecGhosttyScript(script string) error                                    { return errNotDarwin }
