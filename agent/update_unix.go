//go:build !windows

package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// The Linux half of `headnode-agent update` (D-67).

// checkCanUpdate: the program is in a folder only root may write (/usr/local/bin), and
// restarting a system service needs root as well.
func checkCanUpdate() error {
	if os.Geteuid() != 0 {
		return errors.New("updating replaces the installed program and restarts its service, which needs root: run  sudo headnode update")
	}
	return nil
}

// swapProgram moves the new program over the installed one, then restarts the service.
// The move is a rename, so the running agent is never replaced by half a file, and on
// Linux a program that is running can be replaced. The restart comes last and without
// waiting (--no-block): if this was run from a shell the agent itself opened (the
// dashboard's Shell tab), the restart ends that shell, and the restart must go on
// without it.
func swapProgram(exe, newPath string, out io.Writer) error {
	if err := os.Chmod(newPath, 0o755); err != nil {
		return fmt.Errorf("make the new program runnable: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}

	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Fprintln(out, "The program is replaced, but systemctl was not found: restart the agent yourself.")
		return nil
	}
	if output, err := exec.Command("systemctl", "restart", "--no-block", serviceName).CombinedOutput(); err != nil {
		return fmt.Errorf("the program is replaced, but restarting the service failed (restart it yourself): %v: %s", err, output)
	}
	fmt.Fprintln(out, "Restarting the agent. The host reconnects within a few seconds.")
	return nil
}

// recordUpdateResult is a no-op on Unix (only the Windows tray icon reads the registry).
func recordUpdateResult(err error) {}
