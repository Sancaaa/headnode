//go:build linux

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// Starting and stopping a VNC server on a Linux desktop (D-54), the counterpart of
// vnc_windows.go. Windows has a service to switch; here there is none, so the agent
// runs the VNC server itself, as the user it runs as, and stops it by signalling it.
// The program, the arguments and where the desktop is come from vnc_session.go.

// linuxVNCEnv describes this machine for vncEnv.plan.
func linuxVNCEnv() vncEnv {
	uid := os.Getuid()
	dirs := []string{fmt.Sprintf("/run/user/%d", uid)}
	if uid == 0 {
		// Run as root (--user root), the desktop is somebody else's: look in every
		// logged-in user's runtime folder.
		others, _ := filepath.Glob("/run/user/*")
		sort.Strings(others)
		dirs = append(dirs, others...)
	}
	return vncEnv{
		runDirs: dirs,
		x11Dir:  "/tmp/.X11-unix",
		have:    func(program string) bool { _, err := exec.LookPath(program); return err == nil },
	}
}

// canControlVNC says whether there is a desktop to share and a VNC server program
// for it: then the dashboard offers Start VNC. A machine without a desktop (a
// server) or without wayvnc or x11vnc says no.
func canControlVNC() bool {
	_, ok := linuxVNCEnv().plan("")
	return ok
}

// vncConfigDir is where the agent keeps the VNC server's config and log: in the
// agent user's own configuration folder, so nobody else can change them.
func vncConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "headnode")
	return dir, os.MkdirAll(dir, 0o700)
}

// setVNCService starts or stops the VNC server. Unlike Windows it does not set
// anything to start at boot: after a reboot (or when the agent restarts, which ends
// its children with it) the screen is off until Start VNC is pressed again.
func setVNCService(enabled bool) error {
	dir, err := vncConfigDir()
	if err != nil {
		return fmt.Errorf("find the agent's config folder: %w", err)
	}
	plan, ok := linuxVNCEnv().plan(dir)
	if !ok {
		return errors.New("no desktop with a VNC server program was found for this user (wayvnc for Wayland, x11vnc for X11)")
	}
	argv := append([]string{plan.name}, plan.args...)
	if !enabled {
		err := stopVNCProcess(argv)
		if plan.socketPath != "" {
			os.Remove(plan.socketPath)
		}
		return err
	}
	return startVNCProcess(plan, argv, dir)
}

// findVNCProcesses returns the process ids of running programs started with exactly
// these arguments.
func findVNCProcesses(argv []string) []int {
	entries, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	var pids []int
	for _, entry := range entries {
		raw, err := os.ReadFile(entry)
		if err != nil || !cmdlineMatches(raw, argv) {
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(entry)))
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func startVNCProcess(plan vncPlan, argv []string, dir string) error {
	if len(findVNCProcesses(argv)) > 0 {
		return nil // already running: starting is safe to repeat
	}
	if plan.config != "" {
		if err := os.WriteFile(plan.config, []byte(plan.configText), 0o600); err != nil {
			return fmt.Errorf("write the VNC server's config: %w", err)
		}
	}
	if plan.socketPath != "" {
		// Remove any stale socket left by a crashed process so wayvnc can bind it.
		os.Remove(plan.socketPath)
	}
	if plan.passwordFile != "" {
		// A new password every time the screen is turned on (D-64). 0600, in the
		// agent's own 0700 folder: the stored form is only encoded (vncauth.go).
		password, err := newVNCPassword()
		if err != nil {
			return err
		}
		os.Remove(plan.passwordFile) // WriteFile keeps the mode of a file that exists
		if err := os.WriteFile(plan.passwordFile, vncEncryptPassword(password), 0o600); err != nil {
			return fmt.Errorf("write the VNC server's password: %w", err)
		}
	}
	// What the program says goes to a file: a service has no console, and "nothing
	// listens on 5900" is not much to go on.
	logPath := filepath.Join(dir, "vnc.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open the VNC server's log: %w", err)
	}

	cmd := exec.Command(plan.name, plan.args...) // separate arguments, never a shell string
	cmd.Env = append(os.Environ(), plan.env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own session: a signal sent to the agent's terminal or process group
	// does not reach it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start %s: %w", plan.name, err)
	}
	exited := make(chan error, 1)
	go func() { // also collects the finished process, so none is left as a zombie
		exited <- cmd.Wait()
		logFile.Close()
	}()
	select {
	case err := <-exited:
		if plan.socketPath != "" {
			os.Remove(plan.socketPath)
		}
		return fmt.Errorf("%s stopped as soon as it started (%v): see %s", plan.name, err, logPath)
	case <-time.After(time.Second):
		// Lock down the socket itself to 0600 in addition to its 0700 parent dir.
		if plan.socketPath != "" {
			if err := os.Chmod(plan.socketPath, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("restrict VNC socket permissions: %w", err)
			}
		}
		return nil
	}
}

func stopVNCProcess(argv []string) error {
	pids := findVNCProcesses(argv)
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive = true
			}
		}
		if !alive {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// vncPassword is the password of the VNC server the agent starts, for the heartbeat
// (D-64), or "" when it takes none (wayvnc) or none was written yet.
func vncPassword() string {
	dir, err := vncConfigDir()
	if err != nil {
		return ""
	}
	plan, ok := linuxVNCEnv().plan(dir)
	if !ok || plan.passwordFile == "" {
		return ""
	}
	stored, err := os.ReadFile(plan.passwordFile)
	if err != nil {
		return ""
	}
	return vncDecryptPassword(stored)
}

// vncSocketPath returns the private Unix domain socket path used when the Linux
// desktop is served by wayvnc, or "" when VNC uses TCP (x11vnc or no Wayland).
func vncSocketPath() string {
	dir, err := vncConfigDir()
	if err != nil {
		return ""
	}
	plan, ok := linuxVNCEnv().plan(dir)
	if !ok {
		return ""
	}
	return plan.socketPath
}
