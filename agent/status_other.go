//go:build !windows

package agent

// The connection status line is for the Windows tray icon (D-57). Other systems
// have no icon, so there is nothing to write.
func writeStatus(connected bool) {}

// The server's address is written for the tray icon's update item (M10, D-66): Windows only.
func writeServer(server string) {}
