//go:build windows

package agent

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The name TightVNC gives its service when it is installed as one, which is how
// install.ps1 installs it (D-50).
const vncServiceName = "tvnserver"

// What the agent, as LocalSystem, needs to switch TightVNC's service. Only the agent
// does it: the tray icon asks the agent with a control code (D-58).
const vncSwitchRights = windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG |
	windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP

// canControlVNC says whether a TightVNC service exists that this program may look
// at. The agent runs as LocalSystem, and the tray icon as an ordinary user, who may
// look at any service by default.
func canControlVNC() bool {
	s, err := openService(vncServiceName, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// setVNCService turns the VNC server on or off for good, not just for now. It is
// used by the agent, for the dashboard's Start VNC and for the tray icon's.
//
// Off means stopped AND set to start only by hand: a stopped service that comes
// back at the next boot would use the resources again, which is what turning it
// off is for. On means set to start at boot and started now.
func setVNCService(enabled bool) error {
	s, err := openService(vncServiceName, vncSwitchRights)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return errors.New("TightVNC is not installed as a service on this host")
		}
		return fmt.Errorf("open the VNC service: %w", err)
	}
	defer s.Close()

	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("read the VNC service: %w", err)
	}
	if enabled {
		cfg.StartType = mgr.StartAutomatic
	} else {
		cfg.StartType = mgr.StartManual
	}
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("update the VNC service: %w", err)
	}

	if !enabled {
		return stopAndWait(s) // from service_windows.go
	}
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Running || status.State == svc.StartPending {
		return nil
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the VNC service: %w", err)
	}
	return nil
}
