package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"headnode/protocol"
)

// `headnode-agent update` (M10, D-67), also reachable as `headnode update` on Linux:
// the agent replaces its own program with the newest one on its server. It is how a
// host is updated, from `sudo headnode update`, and, on Windows, from the tray icon's
// Update headnode… (which starts it behind the system's administrator prompt, with no
// window).
//
// What it does, in this order, and the first four change nothing on the machine:
//  1. asks the server which version is newest, and stops if this one is not older;
//  2. downloads the program for this system, next to the installed one;
//  3. runs the download with `version`, so a wrong or damaged file never replaces a
//     working one;
//  4. checks that the version it prints is the one the server announced;
//  5. swaps the program in and restarts the service (swapProgram, per system).

// assetName is the name under which the server offers the program for a system, or
// false for a system it has none for (the list is agentBinaries in server/agentdist.go).
func assetName(goos, goarch string) (string, bool) {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64":
		return "headnode-agent-" + goos + "-" + goarch, true
	case "windows/amd64":
		return "headnode-agent-windows-amd64.exe", true
	}
	return "", false
}

// newProgramPath is where the download goes: next to the installed program, so that
// moving it into place is a rename inside one folder (atomic), and with the same
// extension, because Windows will only run a file that has .exe.
func newProgramPath(exe string) string {
	ext := filepath.Ext(exe)
	return strings.TrimSuffix(exe, ext) + ".new" + ext
}

// Update replaces the running program with the newest one on the server named in the
// credentials file at configPath. With force it does so even if the server has the
// same version. What it says goes to out.
func Update(configPath string, force bool, out io.Writer) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	name, ok := assetName(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("the server offers no program for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	text, err := fetchText(cfg.Server + "/download/VERSION")
	if err != nil {
		return fmt.Errorf("ask the server which version is newest: %w", err)
	}
	latest := strings.TrimSpace(text)
	if _, ok := protocol.ParseVersion(latest); !ok {
		return fmt.Errorf("the server answered %q, which is not a version", latest)
	}
	if !force && !protocol.VersionOlder(Version, latest) {
		fmt.Fprintf(out, "headnode-agent %s is up to date (the server has %s).\n", Version, latest)
		return nil
	}
	// After the version check: finding out that nothing is to be done needs no rights.
	if err := checkCanUpdate(); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this program: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil { // `headnode` is a link to it
		return fmt.Errorf("find this program: %w", err)
	}
	newPath := newProgramPath(exe)
	defer os.Remove(newPath) // gone already if it was moved into place; a leftover if anything failed

	fmt.Fprintf(out, "Downloading headnode-agent %s...\n", latest)
	if err := download(cfg.Server+"/download/"+name, newPath); err != nil {
		return err
	}
	got, err := programVersion(newPath)
	if err != nil {
		return fmt.Errorf("the downloaded program does not run on this machine, so it was not installed: %w", err)
	}
	if got != latest {
		return fmt.Errorf("the downloaded program is version %s but the server announced %s, so it was not installed: rebuild the agents on the server (scripts/build-agents)", got, latest)
	}

	if err := swapProgram(exe, newPath, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "Updated headnode-agent %s -> %s.\n", Version, latest)
	return nil
}

// fetchText GETs a small text (a version) from the server.
func fetchText(url string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d (does the server have its agents built? scripts/build-agents writes the version)", resp.StatusCode)
	}
	text, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	return string(text), err
}

// download saves a program from the server to path, ready to run.
func download(url, path string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("save the download next to the installed program: %w", err)
	}
	n, err := io.Copy(file, resp.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n == 0 {
		return errors.New("the downloaded file is empty")
	}
	return nil
}

// programVersion runs a program with `version` and returns what it prints. It is the
// proof that a download is a real agent for this machine before it replaces anything.
func programVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
