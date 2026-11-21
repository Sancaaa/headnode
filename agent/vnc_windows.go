//go:build windows

package agent

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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
// off is for. On means set to start at boot and started now, with a new password
// (D-64). A server that already runs with a password is left alone, so a person
// already looking at the screen is not thrown out.
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
	running := status.State == svc.Running || status.State == svc.StartPending
	if running && vncPassword() != "" {
		return nil
	}
	if running {
		// Running without a password (set up before D-64): TightVNC reads its
		// password when it starts, so it has to start again.
		if err := stopAndWait(s); err != nil {
			return err
		}
	}
	password, err := newVNCPassword()
	if err != nil {
		return err
	}
	if err := setTightVNCPassword(password); err != nil {
		return fmt.Errorf("give TightVNC its password: %w", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the VNC service: %w", err)
	}
	return nil
}

// tightVNCKey is where TightVNC, as a service, keeps its settings.
const tightVNCKey = `SOFTWARE\TightVNC\Server`

// setTightVNCPassword turns VNC authentication on with this password. The key is then
// made readable by the system and administrators only: everyone may read a new key
// under HKLM\SOFTWARE, and the stored password is only encoded, not secret (vncauth.go).
// TightVNC's service runs as LocalSystem, so it can still read it.
func setTightVNCPassword(password string) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, tightVNCKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetBinaryValue("Password", vncEncryptPassword(password)); err != nil {
		return err
	}
	if err := key.SetDWordValue("UseVncAuthentication", 1); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;CI;KA;;;SY)(A;CI;KA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(`MACHINE\`+tightVNCKey, windows.SE_REGISTRY_KEY,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// vncPassword is TightVNC's current password, for the heartbeat (D-64), or "" if it
// has none (VNC authentication is off, or TightVNC is not set up).
func vncPassword() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, tightVNCKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	if auth, _, err := key.GetIntegerValue("UseVncAuthentication"); err != nil || auth != 1 {
		return ""
	}
	stored, _, err := key.GetBinaryValue("Password")
	if err != nil {
		return ""
	}
	return vncDecryptPassword(stored)
}
