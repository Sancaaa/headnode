//go:build windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The agent as a Windows service (D-47): the counterpart of the systemd unit in
// service.go. `headnode-agent service install` registers it with the service
// manager, and when the manager starts it, `headnode-agent run` notices (see
// RunningAsService) and runs inside the manager's protocol instead of in a
// console.

// (serviceName, the name of the service in the manager, is in service.go.)

// stopTimeout is how long uninstall and reinstall wait for a running service to stop.
const stopTimeout = 30 * time.Second

var errNotAdmin = errors.New("installing a service needs Administrator rights: open PowerShell with \"Run as administrator\"")

// DefaultConfigPath is where the service looks for the credentials the install
// script saved: under ProgramData, which every user may read by default, so the
// install script narrows who can (install.ps1).
func DefaultConfigPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "headnode", "agent.json")
}

// LogHint tells the person where to read the agent's log.
func LogHint() string {
	return "Log: " + filepath.Join(filepath.Dir(DefaultConfigPath()), "agent.log")
}

// RunningAsService says whether the service manager started this process.
func RunningAsService() bool {
	// An error means "cannot tell", which for a program started from a console
	// is the answer no.
	return svc.IsWindowsService()
}

// serviceCommandLine is the command the service manager runs. windows.EscapeArg
// quotes a path with spaces ("C:\Program Files\...") the way the manager and
// CommandLineToArgvW read it, so no path can split the command or add to it.
func serviceCommandLine(exe, config string) string {
	return strings.Join([]string{windows.EscapeArg(exe), "run", "-config", windows.EscapeArg(config)}, " ")
}

// InstallService registers the agent as a Windows service that starts at boot,
// and starts it now (or restarts it, so a new program replaces the running one).
// It is `headnode-agent service install`, and the last step of install.ps1.
// Running it again is safe.
//
// The service runs as LocalSystem, like the agent does on Linux as root: it
// manages the machine for the people the server says may.
func InstallService(configPath, runUser string) error {
	if runUser != "" {
		// A Windows service runs as a named account only with that account's
		// password. The agent has no shell here (D-44), so what it can reach is
		// small; this is for a later milestone.
		return errors.New("-user is not supported on Windows: the service runs as LocalSystem")
	}
	config, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(config); err != nil {
		return fmt.Errorf("%s not found: enroll this host first (headnode-agent enroll)", config)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this program: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return fmt.Errorf("find this program: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("%w (%v)", errNotAdmin, err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err == nil {
		// Already installed: point it at this program and these credentials, and
		// restart it so the new program is the one that runs.
		defer s.Close()
		if err := stopAndWait(s); err != nil {
			return err
		}
		cfg, err := s.Config()
		if err != nil {
			return fmt.Errorf("read the existing service: %w", err)
		}
		cfg.BinaryPathName = serviceCommandLine(exe, config)
		cfg.StartType = mgr.StartAutomatic
		if err := s.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update the service: %w", err)
		}
	} else {
		s, err = m.CreateService(serviceName, exe, mgr.Config{
			DisplayName: "headnode agent",
			Description: "Connects this computer to its headnode server, which can then manage it.",
			StartType:   mgr.StartAutomatic,
		}, "run", "-config", config)
		if err != nil {
			return fmt.Errorf("create the service: %w", err)
		}
		defer s.Close()
	}

	// If the agent ever exits, start it again after 5 seconds. A failure of this
	// is not worth failing the install for: the service works without it.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*3600); err != nil {
		slog.Warn("could not set the service's restart-on-failure actions", "err", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the service: %w", err)
	}
	return nil
}

// UninstallService stops the service and removes it from the service manager.
// The program and the credentials file are left alone.
func UninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("%w (%v)", errNotAdmin, err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil // not installed: the point is that afterwards it is not there
	}
	defer s.Close()
	if err := stopAndWait(s); err != nil {
		return err
	}
	return s.Delete()
}

// stopAndWait asks a running service to stop and waits until it has.
func stopAndWait(s *mgr.Service) error {
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop the service: %w", err)
	}
	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		if status, err = s.Query(); err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("the service did not stop in time")
}

// RunService runs the agent inside the service manager's protocol: it tells the
// manager when the service is running and stops it cleanly when asked. cfg is
// what `run` loaded from the credentials file.
func RunService(cfg Config) error {
	closeLog := logToFile()
	defer closeLog()
	return svc.Run(serviceName, &serviceHandler{cfg: cfg})
}

type serviceHandler struct{ cfg Config }

// Execute is called by the service manager. The agent runs until the manager
// says stop (or the machine shuts down), or until it fails for good.
func (h *serviceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- Run(ctx, h.cfg) }()
	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-finished:
				case <-time.After(10 * time.Second): // do not hang the shutdown on a stuck connection
				}
				return false, 0
			}
		case err := <-finished:
			// Run only returns on its own for an error retrying cannot fix. Exit
			// with an error code, so the manager's restart actions apply.
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("the agent stopped", "err", err)
				return false, 1
			}
			return false, 0
		}
	}
}

// logToFile sends the agent's log to a file next to its credentials: a service has
// no console, so without this nothing would ever be written down. A log that
// has grown past 10 MB is kept as agent.log.old and started again.
func logToFile() (closeFile func()) {
	path := filepath.Join(filepath.Dir(DefaultConfigPath()), "agent.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 10<<20 {
		os.Remove(path + ".old")
		os.Rename(path, path+".old")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(file, nil)))
	return func() { file.Close() }
}
