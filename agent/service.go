package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// serviceName is the systemd unit the agent installs itself as.
const serviceName = "headnode-agent"

// These are variables, not constants, only so a test can point them at a
// temporary folder and a fake systemctl instead of the real system.
var (
	unitDir   = "/etc/systemd/system"
	isRoot    = func() bool { return os.Geteuid() == 0 }
	// executable is where this program really is (links followed), so the unit
	// starts the program itself and not a link that may point somewhere else later.
	executable = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
	systemctl = func(args ...string) error {
		// The arguments are passed separately, never joined into a shell command.
		out, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

var errNotRoot = errors.New("installing a service needs root: run this with sudo")

func unitPath() string {
	return filepath.Join(unitDir, serviceName+".service")
}

// unitFile is the text of the systemd unit. exe and config are put in a line
// systemd reads itself (no shell), so a path with a space, a quote or a percent
// sign would be split or expanded: such paths are refused instead of escaped.
func unitFile(exe, config string) (string, error) {
	for _, p := range []string{exe, config} {
		// path, not filepath: a unit holds Linux paths whatever machine this is
		// compiled on.
		if !path.IsAbs(p) {
			return "", fmt.Errorf("%q is not an absolute path", p)
		}
		if strings.ContainsAny(p, " \t\r\n\"'\\%$") {
			return "", fmt.Errorf("%q has a character a systemd unit cannot hold safely (space, quote, backslash, %% or $)", p)
		}
	}
	return fmt.Sprintf(`[Unit]
Description=headnode agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run -config %s
# The agent dials out, so when the server is away it just tries again. If it ever
# exits anyway, start it again.
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, exe, config), nil
}

// InstallService installs the agent as a systemd service that starts at boot,
// and starts it now (or restarts it, so a new program replaces the running one).
// It is `headnode-agent service install`, and the last step of the install
// script. Running it again is safe: it rewrites the unit and restarts.
//
// The service runs as root, like the agent does when started by hand: it opens
// shells and manages files for the people the server says may. A hardened unit
// (no new privileges, a read-only system) would break exactly those things.
func InstallService(configPath string) error {
	if !isRoot() {
		return errNotRoot
	}
	config, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(config); err != nil {
		return fmt.Errorf("%s not found: enroll this host first (headnode-agent enroll)", config)
	}
	exe, err := executable()
	if err != nil {
		return fmt.Errorf("find this program: %w", err)
	}

	unit, err := unitFile(exe, config)
	if err != nil {
		return err
	}
	if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", unitPath(), err)
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", serviceName},
		{"restart", serviceName},
	} {
		if err := systemctl(args...); err != nil {
			return err
		}
	}
	return nil
}

// UninstallService stops the service and removes its unit. It leaves the program
// and the credentials file alone: deleting those is a decision for the person
// (and the host stays known to the server until an admin deletes it there).
func UninstallService() error {
	if !isRoot() {
		return errNotRoot
	}
	// A service that was never installed is not an error here: the point is that
	// afterwards it is not there. So failures of stop/disable are ignored, and the
	// removal of the file decides.
	systemctl("disable", "--now", serviceName)
	if err := os.Remove(unitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", unitPath(), err)
	}
	return systemctl("daemon-reload")
}
