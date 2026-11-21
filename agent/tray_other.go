//go:build !windows

package agent

import "errors"

// RunTray is only for Windows: Linux hosts are managed with systemctl (D-57).
func RunTray() error { return errors.New("the tray icon is only for Windows") }

// AllowService is only for Windows.
func AllowService(sid string) error { return errors.New("service permissions are only for Windows") }
