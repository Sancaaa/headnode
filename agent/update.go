package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// What it does, in this order, and the first five change nothing on the machine:
//  1. asks the server which version is newest, and stops if this one is not older;
//  2. fetches /download/SHA256SUMS and finds the expected SHA-256 for this platform;
//  3. downloads the program for this system next to the installed one and verifies
//     its SHA-256 digest before making it runnable or executing it (fail-closed);
//  4. runs the verified download with `version`, so a wrong or damaged file never
//     replaces a working one;
//  5. checks that the version it prints is the one the server announced;
//  6. swaps the program in and restarts the service (swapProgram, per system).

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
	if err := downloadVerified(cfg.Server, name, newPath); err != nil {
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

// downloadVerified fetches /download/SHA256SUMS, downloads the target binary to
// path, verifies its SHA-256 digest, and only then marks it executable. Fails
// closed if SHA256SUMS is missing, has no valid entry for name, or mismatches,
// and deletes any unverified file before returning.
func downloadVerified(serverURL, name, path string) error {
	sumsText, err := fetchTextLimit(serverURL+"/download/SHA256SUMS", 16<<10)
	if err != nil {
		return fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	expectedSum, err := parseChecksum(sumsText, name)
	if err != nil {
		return err
	}
	gotSum, err := download(serverURL+"/download/"+name, path)
	if err != nil {
		os.Remove(path)
		return err
	}
	if gotSum != expectedSum {
		os.Remove(path)
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s (refusing to execute or install)", name, gotSum, expectedSum)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		os.Remove(path)
		return fmt.Errorf("make the downloaded program runnable: %w", err)
	}
	return nil
}

// parseChecksum extracts the expected lowercase SHA-256 hex digest for name from
// a standard sha256sum file ("<64-hex>  <filename>" or "<64-hex> *<filename>").
func parseChecksum(sumsText, name string) (string, error) {
	var found string
	for _, raw := range strings.Split(sumsText, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		digest := strings.ToLower(fields[0])
		file := strings.TrimPrefix(fields[1], "*")
		if file != name {
			continue
		}
		if len(digest) != sha256.Size*2 {
			return "", fmt.Errorf("invalid SHA-256 checksum for %s in SHA256SUMS", name)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return "", fmt.Errorf("invalid SHA-256 checksum for %s in SHA256SUMS", name)
		}
		if found != "" {
			return "", fmt.Errorf("duplicate SHA256SUMS entry for %s", name)
		}
		found = digest
	}
	if found == "" {
		return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
	}
	return found, nil
}

// fetchText GETs a small text (a version) from the server.
func fetchText(url string) (string, error) {
	return fetchTextLimit(url, 64)
}

func fetchTextLimit(url string, limit int64) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d (does the server have its agents built? scripts/build-agents writes VERSION and SHA256SUMS)", resp.StatusCode)
	}
	text, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return string(text), err
}

// download saves a program from the server to path (non-executable until verified)
// and returns its lowercase SHA-256 hex digest.
func download(url, path string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("save the download next to the installed program: %w", err)
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hasher), resp.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if n == 0 {
		return "", errors.New("the downloaded file is empty")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
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
