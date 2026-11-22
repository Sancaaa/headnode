package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// fetchClient bounds connection setup and header waits so a stalled peer cannot
// hang a transfer forever, while leaving Client.Timeout unset so multi-GB
// transfers on slow links can finish.
var fetchClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

var (
	errBadMode     = errors.New("mode must be at most 0777")
	errBadChecksum = errors.New("the file did not arrive intact (checksum mismatch)")
	errBadSize     = errors.New("the file did not arrive intact (size mismatch)")
)

// runFetch carries out one `fetch` command from the server and reports how it
// went on the control connection (protocol.md §3). It runs on its own
// goroutine: a transfer can take minutes, and heartbeats must go on.
func runFetch(ctx context.Context, cfg Config, conn *websocket.Conn, cmd protocol.Fetch) {
	log := slog.With("transfer", cmd.TransferID, "dest", cmd.DestPath)
	log.Info("fetching a file")

	err := fetchFile(ctx, cfg, cmd)
	result := protocol.FetchResult{TransferID: cmd.TransferID, OK: err == nil}
	if err != nil {
		result.Error = closeReason(describe(err)) // the server shows this to people
		log.Warn("fetch failed", "err", err)
	} else {
		log.Info("fetch done")
	}
	if err := send(ctx, conn, protocol.TypeFetchResult, result); err != nil {
		// The control connection dropped while we worked. The server marks the
		// transfer as failed when it notices (D-33).
		log.Warn("could not report the result", "err", err)
	}
}

// fetchFile downloads the file and puts it at cmd.DestPath so that the path
// either holds the complete, verified file or is left exactly as it was.
//
// The file is written to a ".part" file first. Only when every byte has arrived
// and the checksum matches does it get its final mode and get renamed over the
// destination. A rename is atomic, so nobody ever sees a half-written file at
// the destination, and a program that is running right now can be replaced
// (writing into a running binary fails with "text file busy").
func fetchFile(ctx context.Context, cfg Config, cmd protocol.Fetch) error {
	dest, err := absPath(cmd.DestPath)
	if err != nil {
		return err
	}
	mode, err := parseMode(cmd.Mode)
	if err != nil {
		return err
	}
	if len(cmd.SHA256) != sha256.Size*2 {
		return errors.New("the server sent no usable checksum")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(cfg.Server, "/")+"/agent/files/"+url.PathEscape(cmd.FileID), nil)
	if err != nil {
		return err
	}
	req.Header = authHeader(cfg)
	// No overall timeout on purpose: a big file on a slow line takes as long as
	// it takes. Dial, TLS and response-header waits are bounded by fetchClient.
	resp, err := fetchClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
	}

	// A temporary file next to the destination (same folder, so the final rename
	// stays on one filesystem and is atomic), named "<name>.<random>.part".
	// The random part means two attempts at the same path never share a file:
	// two transfers at once, or an earlier attempt that was killed half way.
	// CreateTemp makes it 0600: a file that is half there, and may be a secret or
	// a program, is not readable by everyone.
	//
	// For an archive to unpack, dest is the folder the contents go into, so the
	// temporary file lives inside it.
	tempDir, tempName := filepath.Dir(dest), filepath.Base(dest)+".*.part"
	if cmd.Extract {
		tempDir, tempName = dest, ".headnode-archive-*.part"
	}
	file, err := os.CreateTemp(tempDir, tempName)
	if err != nil {
		return err
	}
	part := file.Name()
	committed := false
	defer func() {
		file.Close()
		if !committed {
			os.Remove(part) // a failed attempt leaves nothing behind
		}
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), resp.Body)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if written != cmd.Size {
		return errBadSize
	}
	if hex.EncodeToString(hasher.Sum(nil)) != cmd.SHA256 {
		return errBadChecksum
	}

	// Make sure the bytes are on the disk, not only in the system's cache,
	// before the name points at them.
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if cmd.Extract {
		// The archive itself is not kept: the deferred cleanup removes it, whether
		// or not unpacking worked.
		return extractZip(part, dest)
	}
	if err := os.Chmod(part, mode); err != nil {
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	committed = true
	return nil
}

// parseMode reads an octal mode like "0755". The server already checked its
// shape, and the agent checks again: no setuid, setgid or sticky bits.
func parseMode(s string) (os.FileMode, error) {
	if s == "" {
		return 0o644, nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 {
		return 0, errBadMode
	}
	return os.FileMode(n), nil
}
