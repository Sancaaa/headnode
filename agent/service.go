package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// This file is the service on Linux: a systemd unit. It is built on every system
// (so its text and its checks are tested on the Windows laptop too), and only
// service_unix.go makes it the agent's InstallService. service_windows.go is the
// same set of functions for the Windows service manager (D-47).

// serviceName is the name of the service: the systemd unit here, and the entry in
// the service manager on Windows.
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

// A user name as Linux writes them. Checked before it goes into a unit file (one
// word, no spaces or specifiers), and before it is looked up.
var userNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Variables only so a test can stand in for the real system.
var (
	lookupUser = user.Lookup
	chownFile  = os.Chown
)

func unitPath() string {
	return filepath.Join(unitDir, serviceName+".service")
}

// unitFile is the text of the systemd unit. exe and config are put in a line
// systemd reads itself (no shell), so a path with a space, a quote or a percent
// sign would be split or expanded: such paths are refused instead of escaped.
//
// runUser is the account the agent runs as, and so the account every terminal
// and file operation it opens runs as (D-48). "" or "root" leaves out the User
// line: systemd then runs it as root.
func unitFile(exe, config, runUser string) (string, error) {
	identity := ""
	if runUser != "" && runUser != "root" {
		if !userNameRe.MatchString(runUser) {
			return "", fmt.Errorf("%q is not a valid user name", runUser)
		}
		// WorkingDirectory=~ is the user's home folder, where a terminal should start.
		identity = "User=" + runUser + "\nWorkingDirectory=~\n"
	}
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
%sExecStart=%s run -config %s
# The agent dials out, so when the server is away it just tries again. If it ever
# exits anyway, start it again.
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, identity, exe, config), nil
}

// installSystemdService installs the agent as a systemd service that starts at
// boot, and starts it now (or restarts it, so a new program replaces the running
// one). It is `headnode-agent service install`, and the last step of the install
// script. Running it again is safe: it rewrites the unit and restarts.
//
// The service runs as runUser, and everything the agent opens runs as that user:
// a terminal is that user's shell, the file manager sees what that user may see.
// Installing is done as root (it writes a system unit), but the agent itself does
// not have to be root. "" or "root" runs it as root, like an agent started by
// hand from a root shell. A hardened unit (no new privileges, a read-only
// system) would break shells and file changes, so the unit is not hardened: the
// protection is the account.
func installSystemdService(configPath, runUser string) error {
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

	unit, err := unitFile(exe, config, runUser)
	if err != nil {
		return err
	}
	if runUser != "" && runUser != "root" {
		// The credentials were written by root, and a service running as someone
		// else could not read them. They stay private: owned by that user, mode
		// 0600 (enroll made it so).
		account, err := lookupUser(runUser)
		if err != nil {
			return fmt.Errorf("the user %q does not exist on this machine", runUser)
		}
		uid, err1 := strconv.Atoi(account.Uid)
		gid, err2 := strconv.Atoi(account.Gid)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("cannot read the account of %q", runUser)
		}
		if err := chownFile(config, uid, gid); err != nil {
			return fmt.Errorf("give %s to %s: %w", config, runUser, err)
		}
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

// uninstallSystemdService stops the service and removes its unit. It leaves the
// program and the credentials file alone: deleting those is a decision for the
// person (and the host stays known to the server until an admin deletes it there).
func uninstallSystemdService() error {
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
