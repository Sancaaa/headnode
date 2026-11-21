//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The tray icon of a Windows host (M9, D-57): `headnode-agent tray`. A person at the
// machine can see whether headnode is connected, and turn it off and on, without a
// command and without administrator rights. It runs in the person's own session. It
// may start the agent's service and send it control codes, which `service allow`
// permits that one person; it may not reconfigure any service (D-58): the agent, as
// LocalSystem, does that when it is asked.
//
// "Off" really is off: the service is stopped and set to start only by hand, so
// nothing of headnode runs until it is turned on. The icon is not headnode: closing it
// (Quit) changes nothing about the service.

const trayRunValue = "headnode-tray" // the name of the login entry, in install.ps1 too

// RunTray shows the icon and runs until Quit is chosen.
func RunTray() error {
	// One icon per person: the login entry and the installer both start one, and a
	// second copy would only be a second icon.
	name, err := windows.UTF16PtrFromString(`Local\headnode-tray`)
	if err != nil {
		return err
	}
	lock, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create the single-instance lock: %w", err)
	}
	defer windows.CloseHandle(lock)
	if err != nil {
		return nil // already running
	}

	// This program is a console program, and started at login it would keep an empty
	// console window open. The icon has nothing to print.
	windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call()

	green := trayIcon(0x2e, 0x9e, 0x5b)
	grey := trayIcon(0x8a, 0x8a, 0x8a)
	systray.Run(func() { trayReady(green, grey) }, func() {})
	return nil
}

func trayReady(green, grey []byte) {
	systray.SetIcon(grey)
	systray.SetTooltip("headnode")

	status := systray.AddMenuItem("headnode", "")
	status.Disable() // a line of text, not a command
	systray.AddSeparator()
	toggle := systray.AddMenuItem("Turn headnode off", "Stop headnode on this computer, and keep it from starting at boot")
	screen := systray.AddMenuItem("Start VNC", "Share this computer's screen with headnode")
	screen.Hide() // until we know that TightVNC is installed
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Close this icon. headnode keeps running or stays off as it is.")

	go trayLoop(status, toggle, screen, quit, green, grey)
}

// trayLoop keeps the icon up to date and does what the menu asks, one thing at a time.
func trayLoop(status, toggle, screen, quit *systray.MenuItem, green, grey []byte) {
	var (
		problem      string // the last action's error, shown for a while
		problemUntil time.Time
		shownGreen   *bool // which icon is up, so it is only replaced when it changes
	)
	refresh := func() {
		st := readTrayState()
		line, isGreen, toggleLabel := trayView(st.running, st.notAtBoot, st.connected)
		if st.err != nil {
			line, isGreen = st.err.Error(), false
		}
		if problem != "" && time.Now().Before(problemUntil) {
			line = problem
		}
		status.SetTitle(line)
		toggle.SetTitle(toggleLabel)
		toggle.Enable()
		systray.SetTooltip("headnode: " + line)
		if shownGreen == nil || *shownGreen != isGreen {
			if isGreen {
				systray.SetIcon(green)
			} else {
				systray.SetIcon(grey)
			}
			shownGreen = &isGreen
		}
		// The agent switches the screen for the icon, so with headnode off there is
		// nobody to ask (and nothing to reach the screen through anyway).
		if st.vncKnown && st.running {
			screen.Show()
			if st.vncRunning {
				screen.SetTitle("Stop VNC")
			} else {
				screen.SetTitle("Start VNC")
			}
		} else {
			screen.Hide()
		}
	}
	fail := func(what string, err error) {
		problem = what + ": " + err.Error()
		problemUntil = time.Now().Add(15 * time.Second)
	}

	refresh()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			refresh()
		case <-toggle.ClickedCh:
			st := readTrayState()
			toggle.SetTitle("Working…")
			toggle.Disable()
			if err := switchAgent(!st.running); err != nil {
				fail("Could not switch headnode", err)
			}
			refresh()
		case <-screen.ClickedCh:
			st := readTrayState()
			screen.Disable()
			if err := askAgentVNC(!st.vncRunning); err != nil {
				fail("Could not switch VNC", err)
			}
			screen.Enable()
			refresh()
		case <-quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// trayState is what the icon knows about this machine.
type trayState struct {
	running, notAtBoot, connected bool
	vncKnown, vncRunning         bool  // TightVNC is installed, and running
	err                          error // headnode itself cannot be looked at
}

func readTrayState() trayState {
	var st trayState
	s, err := openService(serviceName, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST):
			st.err = errors.New("headnode is not installed")
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			st.err = errors.New("no permission: run the install command again")
		default:
			st.err = err
		}
		return st
	}
	defer s.Close()

	if status, err := s.Query(); err == nil {
		st.running = status.State == svc.Running || status.State == svc.StartPending
	}
	if cfg, err := s.Config(); err == nil {
		st.notAtBoot = cfg.StartType != mgr.StartAutomatic
	}
	if st.running {
		if text, err := os.ReadFile(statusPath()); err == nil {
			st.connected = parseStatus(string(text), time.Now())
		}
	}
	if v, err := openService(vncServiceName, windows.SERVICE_QUERY_STATUS); err == nil {
		defer v.Close()
		st.vncKnown = true
		if status, err := v.Query(); err == nil {
			st.vncRunning = status.State == svc.Running || status.State == svc.StartPending
		}
	}
	return st
}

// switchAgent turns headnode on or off for good. Off is stopped AND set to start by
// hand, so a reboot does not bring it back, and nothing of headnode runs; on is
// started, and the agent then sets itself to start at boot (service_windows.go). The
// icon changes no configuration itself (D-58): for off it asks the agent with a
// control code, and the agent sets its start type and stops.
func switchAgent(on bool) error {
	s, err := openService(serviceName, trayServiceRights)
	if err != nil {
		return err
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return err
	}
	running := status.State == svc.Running || status.State == svc.StartPending
	if on {
		if running {
			return nil
		}
		return s.Start()
	}
	if !running {
		return nil
	}
	if _, err := s.Control(ctlTurnOff); err != nil {
		return fmt.Errorf("ask headnode to turn off: %w", err)
	}
	return waitStopped(s) // from service_windows.go
}

// askAgentVNC asks the agent to start or stop TightVNC, the same thing the
// dashboard's Start VNC / Stop VNC does. The agent runs as LocalSystem and may switch
// TightVNC's service; the icon's user may not, and needs not (D-58).
func askAgentVNC(on bool) error {
	s, err := openService(serviceName, trayServiceRights)
	if err != nil {
		return err
	}
	defer s.Close()
	code := ctlVNCOff
	if on {
		code = ctlVNCOn
	}
	if _, err := s.Control(code); err != nil {
		return fmt.Errorf("ask headnode to switch VNC: %w", err)
	}
	return nil
}

// removeTrayAutostart deletes the login entry that install.ps1 made for the person
// running this (the entry is per user). Part of `service uninstall`; a failure, or no
// entry, is not worth an error: the point is that afterwards there is none.
func removeTrayAutostart() {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	key.DeleteValue(trayRunValue)
}
