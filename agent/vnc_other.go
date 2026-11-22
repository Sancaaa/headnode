//go:build !windows && !linux

package agent

import "errors"

// Starting and stopping the VNC server is for the Windows and Linux agents
// (D-51, D-54). Any other system has none.

func canControlVNC() bool { return false }

func setVNCService(enabled bool) error {
	return errors.New("starting and stopping VNC is only supported on Windows and Linux")
}

func vncPassword() string { return "" }

func vncSocketPath() string { return "" }
