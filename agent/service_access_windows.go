//go:build windows

package agent

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// What the tray icon, which runs as an ordinary user, opens the agent's service with
// (D-57, D-58): look at it, start it, and send it the icon's own control codes. No
// SERVICE_CHANGE_CONFIG: whoever may change a service's configuration may point it at
// any program, and the service runs that program as LocalSystem. Changing how the
// service starts, and switching TightVNC, is done by the agent itself, as SYSTEM, when
// the icon asks with a control code.
const trayServiceRights = windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG |
	windows.SERVICE_START | windows.SERVICE_USER_DEFINED_CONTROL

// The control codes the tray icon sends to the agent's service (D-58). Windows keeps
// 128 to 255 for a service's own use, and the service manager lets through only a
// caller who has SERVICE_USER_DEFINED_CONTROL on the service: the one account that
// `service allow` names, and administrators.
const (
	ctlTurnOff svc.Cmd = 128 // set to start by hand, then stop: headnode is off until started again
	ctlVNCOn   svc.Cmd = 129 // the same as the dashboard's Start VNC
	ctlVNCOff  svc.Cmd = 130 // the same as the dashboard's Stop VNC
)

// openService opens a service with exactly the rights asked for. mgr.Connect and
// mgr.OpenService ask for all rights, which only an administrator has: this is how a
// person without administrator rights can still use a service they were given some
// rights to. The caller closes the result.
func openService(name string, access uint32) (*mgr.Service, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(manager)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.OpenService(manager, namePtr, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: handle}, nil
}

// setStartType changes how a service starts (at boot, or by hand). Only the agent
// calls it, as LocalSystem, which is a member of Administrators.
func setStartType(name string, startType uint32) error {
	s, err := openService(name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return err
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	if cfg.StartType == startType {
		return nil
	}
	cfg.StartType = startType
	return s.UpdateConfig(cfg)
}

// setServiceDACL replaces who may do what with a service.
func setServiceDACL(name, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("read the permissions: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the permissions: %w", err)
	}
	return windows.SetNamedSecurityInfo(name, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// resetServicePermissions gives the agent's service its base permissions (no extra
// user), and gives TightVNC's service, if it exists, the Windows default back. It is
// part of every `service install`, so an update repairs a machine that an older
// version set up with the dangerous change-config right (D-58). TightVNC's
// permissions were only ever changed by headnode's `service allow`.
func resetServicePermissions() error {
	if err := setServiceDACL(serviceName, agentServiceDACL); err != nil {
		return fmt.Errorf("set the permissions of the %s service: %w", serviceName, err)
	}
	if s, err := openService(vncServiceName, windows.SERVICE_QUERY_STATUS); err == nil {
		s.Close()
		if err := setServiceDACL(vncServiceName, windowsDefaultServiceDACL); err != nil {
			return fmt.Errorf("set the permissions of the %s service: %w", vncServiceName, err)
		}
	}
	return nil
}

// AllowService lets the user with this SID use the tray icon's switches without
// administrator rights (D-57, D-58): start the agent's service, and send it the icon's
// control codes. It is `headnode-agent service allow`, which install.ps1 runs, as
// administrator, with the SID of whoever runs the installer. TightVNC's service is not
// touched: the agent switches it on the icon's behalf.
func AllowService(sid string) error {
	if !validSID(sid) {
		return errors.New("-sid must be a security identifier like S-1-5-21-...")
	}
	if _, err := windows.StringToSid(sid); err != nil {
		return fmt.Errorf("-sid is not a valid security identifier: %w", err)
	}
	if err := setServiceDACL(serviceName, serviceSDDL(sid)); err != nil {
		return fmt.Errorf("set the permissions of the %s service (run this as Administrator): %w", serviceName, err)
	}
	return nil
}
