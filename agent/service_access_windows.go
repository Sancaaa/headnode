//go:build windows

package agent

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// What the tray icon, which runs as an ordinary user, needs from a service: look at
// it, change how it starts, start it and stop it (D-57).
const serviceSwitchRights = windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG |
	windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP

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

// AllowService lets the user with this SID start, stop and reconfigure the headnode
// agent service, and TightVNC's if it is installed, without administrator rights
// (D-57). It is `headnode-agent service allow`, which install.ps1 runs once, as
// administrator, with the SID of whoever runs the installer.
func AllowService(sid string) error {
	if !validSID(sid) {
		return errors.New("-sid must be a security identifier like S-1-5-21-...")
	}
	if _, err := windows.StringToSid(sid); err != nil {
		return fmt.Errorf("-sid is not a valid security identifier: %w", err)
	}
	sddl := serviceSDDL(sid)
	if err := setServiceDACL(serviceName, sddl); err != nil {
		return fmt.Errorf("set the permissions of the %s service (run this as Administrator): %w", serviceName, err)
	}
	// TightVNC's service gets the same rule, so the icon's Start VNC works too. A
	// machine without TightVNC simply has none.
	if s, err := openService(vncServiceName, windows.SERVICE_QUERY_STATUS); err == nil {
		s.Close()
		if err := setServiceDACL(vncServiceName, sddl); err != nil {
			return fmt.Errorf("set the permissions of the %s service: %w", vncServiceName, err)
		}
	}
	return nil
}
