//go:build !windows

package agent

import "context"

// The connection status line is for the Windows tray icon (D-57). Other systems
// have no icon, so there is nothing to write.
func writeStatus(connected bool) {}

// Nor is there an icon to tell the newest version to (D-68): Linux hosts are updated
// with `sudo headnode update`, which asks the server itself.
func startLatestWatcher(ctx context.Context, cfg Config) {}
