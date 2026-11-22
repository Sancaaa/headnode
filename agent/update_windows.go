//go:build windows

package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// The Windows half of `headnode-agent update` (D-67). It runs elevated (the tray icon
// starts it behind the system's administrator prompt) and with no window, so its result
// goes where the icon can read it: the registry, see writeUpdateResult.

// checkCanUpdate: the program is in Program Files and the service belongs to the system.
func checkCanUpdate() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errNotAdmin
	}
	return nil
}

// recordUpdateResult writes the outcome of Update to the registry for the tray icon.
func recordUpdateResult(err error) { writeUpdateResult(err) }

// swapProgram stops the service, puts the new program in place, starts the service, and
// brings the tray icon back. Windows will not overwrite or delete a program that is
// running, but it lets one be renamed: the old one is moved aside, the new one takes its
// name, and the old one is deleted if nothing holds it (this very process does, when it
// is the one being replaced, so it may stay until the next update). If a step fails the
// service is started again, so the host is not left off.
func swapProgram(exe, newPath string, out io.Writer) (err error) {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("%w (%v)", errNotAdmin, err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return errors.New("headnode is not installed as a service on this computer")
	}
	defer s.Close()

	if err := stopAndWait(s); err != nil {
		return err
	}
	defer func() {
		if err != nil { // do not leave the host off because of a failed update
			s.Start()
		}
	}()

	// The tray icons are this same program, and are stopped with it (the one that
	// started this update among them): a new one is started below. Every other process
	// of this name goes; this one, which is being replaced, stays.
	exec.Command("taskkill", "/F", "/IM", filepath.Base(exe), "/FI", fmt.Sprintf("PID ne %d", os.Getpid())).Run()

	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("move the old program aside: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		os.Rename(old, exe) // put the old one back
		return fmt.Errorf("put the new program in place: %w", err)
	}
	os.Remove(old)

	if err := s.Start(); err != nil {
		return fmt.Errorf("the program is replaced, but the service did not start: %w", err)
	}
	fmt.Fprintln(out, "The agent is running again.")
	startTrayForUser()
	return nil
}

// startTrayForUser brings the icon back for the person whose Start menu entry exists.
// This process is elevated, and an icon started from it would be too, so the Start menu
// entry is opened by Explorer, which runs it as the signed-in person.
func startTrayForUser() {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return
	}
	entry := filepath.Join(appData, `Microsoft\Windows\Start Menu\Programs`, trayShortcut)
	if _, err := os.Stat(entry); err != nil {
		return // no icon was set up for this person
	}
	exec.Command("explorer.exe", entry).Start()
}
