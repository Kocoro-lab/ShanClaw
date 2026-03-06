//go:build !darwin

package schedule

import "fmt"

func PlistPath(id string) string                                   { return "" }
func GeneratePlist(id, agent, cron, prompt, shanBin string) string { return "" }
func WritePlist(path, content string) error                        { return fmt.Errorf("launchd not supported on this platform") }
func RemovePlist(path string) error                                { return nil }
func LaunchctlLoad(plistPath string) error                         { return fmt.Errorf("launchd not supported on this platform") }
func LaunchctlUnload(plistPath string) error                       { return nil }
func ShanBinary() string                                           { return "shan" }
