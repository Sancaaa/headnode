//go:build windows

package agent

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The name TightVNC gives its service when it is installed as one, which is how
// install.ps1 installs it (D-50).
const vncServiceName = "tvnserver"

// canControlVNC says whether a TightVNC service exists that this agent may change.
// The agent runs as LocalSystem, so it may; run from a console without
// administrator rights it may not, and the answer is no.
func canControlVNC() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(vncServiceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// setVNCService turns the VNC server on or off for good, not just for now.
//
// Off means stopped AND set to start only by hand: a stopped service that comes
// back at the next boot would use the resources again, which is what turning it
// off is for. On means set to start at boot and started now.
func setVNCService(enabled bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(vncServiceName)
	if err != nil {
		return errors.New("TightVNC is not installed as a service on this host")
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
