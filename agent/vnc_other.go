//go:build !windows

package agent

import "errors"

// Starting and stopping the VNC server is for the Windows agent (D-51): that is
// where the installer puts one. A Linux host that runs a VNC server manages it itself.

func canControlVNC() bool { return false }

func setVNCService(enabled bool) error {
	return errors.New("starting and stopping VNC is only supported on Windows")
}
