package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// 64 KB per frame: big enough that a gigabyte is not a million tiny writes,
// small enough that the agent holds almost nothing in memory.
const downloadChunk = 64 * 1024

var (
	errIsDir      = errors.New("is a directory")
	errNotRegular = errors.New("not a regular file")
)

// runDownload sends one file to the server (protocol.md §4, `download`): a text
// frame with the name and size, then the bytes as binary frames, then a normal
// close. If the file cannot be opened, it closes with the reason instead of
// sending a header, so the server can answer the browser with a proper error.
func runDownload(ctx context.Context, conn *websocket.Conn, rawParams json.RawMessage) error {
	var params protocol.DownloadParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		conn.Close(websocket.StatusInternalError, "bad session parameters")
		return err
	}

	// A folder is sent as a zip archive. Anything that is not a folder (or cannot
	// be looked at) takes the single-file path, which also reports the errors.
	if abs, err := absPath(params.Path); err == nil {
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return runZipDownload(ctx, conn, abs)
		}
	}

	file, size, name, err := openForDownload(params.Path)
	if err != nil {
		conn.Close(websocket.StatusInternalError, closeReason(describe(err)))
		return err
	}
	defer file.Close()

	// We only write on this connection. CloseRead reads (and drops) what the
	// server sends, and cancels the context it returns when the connection ends,
	// so a browser that cancelled the download stops the agent at once instead of
	// after the next failed write.
	ctx = conn.CloseRead(ctx)

	header, err := json.Marshal(protocol.DownloadHeader{Name: name, Size: size})
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, header); err != nil {
		return nil
	}

	buf := make([]byte, downloadChunk)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := conn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
				return nil // the other side is gone: nothing more to do
			}
		}
		if readErr == io.EOF {
			conn.Close(websocket.StatusNormalClosure, "")
			return nil
		}
		if readErr != nil {
			// Say it plainly: a normal close would make the download look complete.
			conn.Close(websocket.StatusInternalError, closeReason("read error: "+describe(readErr)))
			return readErr
		}
	}
}

// openForDownload opens a regular file for reading. Directories and special
// files (devices, pipes) are refused: reading /dev/zero would never end.
func openForDownload(p string) (file *os.File, size int64, name string, err error) {
	abs, err := absPath(p)
	if err != nil {
		return nil, 0, "", err
	}
	file, err = os.Open(abs)
	if err != nil {
		return nil, 0, "", err
	}
	info, err := file.Stat() // of what was opened: a link has been followed
	if err != nil {
		file.Close()
		return nil, 0, "", err
	}
	if info.IsDir() {
		file.Close()
		return nil, 0, "", errIsDir
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, 0, "", errNotRegular
	}
	if isSecretFile(info) {
		file.Close()
		return nil, 0, "", errSecretFile
	}
	return file, info.Size(), filepath.Base(abs), nil
}
