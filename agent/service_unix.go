//go:build !windows

package agent

import "errors"

// On Linux (and any other system that is not Windows) the service is a systemd
// unit, see service.go. These are the names the program's main function uses; on
// Windows service_windows.go provides the same ones.

// DefaultConfigPath is where the service looks for the credentials the install
// script saved.
func DefaultConfigPath() string { return "/etc/headnode/agent.json" }

// LogHint tells the person where to read the agent's log.
func LogHint() string { return "Logs: journalctl -u headnode-agent -f" }

// InstallService installs and starts the systemd service, running the agent as
// runUser ("" means root).
func InstallService(configPath, runUser string) error { return installSystemdService(configPath, runUser) }

// UninstallService stops and removes the systemd service.
func UninstallService() error { return uninstallSystemdService() }

// RunningAsService says whether the Windows service manager started this
// process. On Linux systemd starts `headnode-agent run` like any command, so
// there is nothing special to do.
func RunningAsService() bool { return false }

// RunService is only for Windows.
func RunService(cfg Config) error { return errors.New("not a Windows service") }
