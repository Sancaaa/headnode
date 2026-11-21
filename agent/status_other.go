//go:build !windows

package agent

// The connection status line is for the Windows tray icon (D-57). Other systems
// have no icon, so there is nothing to write.
func writeStatus(connected bool) {}
