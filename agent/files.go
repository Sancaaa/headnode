package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// listLimit caps one directory listing. A directory with a million entries
// would otherwise become one enormous message. A variable, not a constant, so
// a test can lower it.
var listLimit = 2000

// walkLimit caps how many entries one folder-size count or one folder copy looks
// at. Walking a whole disk to answer "how big is /?" would keep the file manager
// waiting for minutes, and copying that many entries by accident would fill the
// disk. Past the limit a size is reported as partial and a copy is refused. A
// variable so a test can lower it.
var walkLimit = 100000

// filesSupported is false on Windows: Windows hosts have no file manager (D-47),
// and the Windows agent runs as LocalSystem so a files session must be refused
// here as well as on the server. A variable so WebSocket tests can run on a
// Windows development machine.
var filesSupported = runtime.GOOS != "windows"

// The answers a person is meant to read. Anything else is described by the
// operating system's own error, see describe.
var (
	errNotAbsolute      = errors.New("path must be absolute")
	errReadOnly         = errors.New("this session is read-only")
	errUnknownOp        = errors.New("unknown operation")
	errExists           = errors.New("destination already exists")
	errDirExists        = errors.New("a folder with that name already exists")
	errFileExists       = errors.New("a file with that name already exists")
	errIntoItself       = errors.New("cannot copy a folder into itself")
	errSpecial          = errors.New("cannot copy a special file (a device, socket or pipe)")
	errRoot             = errors.New("refusing to delete the root directory")
	errFilesUnsupported = errors.New("file manager sessions are not supported on Windows")
)

// runFiles serves a file manager session: the browser sends one JSON request
// per text frame, and the agent answers each with one JSON response carrying
// the same id (protocol.md §4). It ends when the connection does.
//
// report is told about every attempt to change something (mkdir, rename,
// delete), so the server can audit it: the relay does not read this session's
// traffic (D-27), so the agent is the only one that sees what was asked and
// what happened (D-37). It may be nil.
func runFiles(ctx context.Context, conn *websocket.Conn, rawParams json.RawMessage, report func(protocol.FileChange)) error {
	if !filesSupported {
		conn.Close(websocket.StatusInternalError, errFilesUnsupported.Error())
		return errFilesUnsupported
	}
	var params protocol.FilesParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			conn.Close(websocket.StatusInternalError, "bad session parameters")
			return err
		}
	}
	// A save carries a whole small file (protocol.MaxEditBytes) as JSON text, which
	// can be several times bigger once it is escaped.
	conn.SetReadLimit(4 << 20)
	slog.Info("file session started", "readonly", params.ReadOnly)

	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return nil // the browser left, or the agent is stopping: a normal end
		}
		if kind != websocket.MessageText {
			continue // only text frames carry requests
		}
		reply, err := json.Marshal(handleFileRequest(data, params.ReadOnly, report))
		if err != nil {
			return err
		}
		// A plain text frame holding the JSON, not the {"type","data"} envelope of
		// the control connection: a session has its own, simpler format.
		writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
		err = conn.Write(writeCtx, websocket.MessageText, reply)
		cancel()
		if err != nil {
			return nil
		}
	}
}

// handleFileRequest decodes one request and returns its response. It never
// fails: an error becomes {"ok":false,"error":"..."} for that request, so one
// bad request cannot end the session.
func handleFileRequest(data []byte, readOnly bool, report func(protocol.FileChange)) protocol.FileResponse {
	var req protocol.FileRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return protocol.FileResponse{Error: "invalid request"}
	}
	resp, err := doFileOp(req, readOnly)
	reportChange(report, req, err)
	if err != nil {
		return protocol.FileResponse{ID: req.ID, Error: describe(err)}
	}
	resp.ID = req.ID
	resp.OK = true
	return resp
}

// reportChange tells the server about one attempt to change something, after
// it ran, with its outcome. Failures are reported too: a refused or failed
// attempt (permission denied, a path that is not there) is exactly what a
// person reading the audit log wants to see.
//
// Nothing is reported for reads (list, stat, home: too noisy, and downloads
// are audited by the server itself), for operations that are not changes, or
// for a change a read-only session refused: nothing was attempted on the disk.
// The report says what happened, never who did it: the agent does not know
// that, and the server looks the person up from the session.
func reportChange(report func(protocol.FileChange), req protocol.FileRequest, err error) {
	if report == nil {
		return
	}
	switch req.Op {
	case protocol.OpMkdir, protocol.OpRename, protocol.OpDelete, protocol.OpCopy, protocol.OpChmod, protocol.OpWrite:
	default:
		return
	}
	if errors.Is(err, errReadOnly) {
		return
	}

	change := protocol.FileChange{Op: req.Op, Path: req.Path, OK: err == nil}
	switch req.Op {
	case protocol.OpRename, protocol.OpCopy:
		change.Path, change.To = req.From, req.To
	case protocol.OpDelete:
		change.Recursive = req.Recursive
	case protocol.OpChmod:
		change.Mode = req.Mode
	case protocol.OpWrite:
		change.Size = int64(len(req.Content)) // what was asked for: never the text itself
	}
	if err != nil {
		change.Error = describe(err)
	}
	report(change)
}

func doFileOp(req protocol.FileRequest, readOnly bool) (protocol.FileResponse, error) {
	switch req.Op {
	case protocol.OpHome:
		return protocol.FileResponse{Path: homeDir()}, nil
	case protocol.OpList:
		return listDir(req.Path)
	case protocol.OpStat:
		return statPath(req.Path)
	case protocol.OpProps:
		return propsOf(req.Path)
	case protocol.OpRead:
		return readText(req.Path)
	case protocol.OpMkdir, protocol.OpRename, protocol.OpDelete, protocol.OpCopy, protocol.OpChmod, protocol.OpWrite:
		// The server decided at session start what this user may do and told us
		// (D-27). A read-only session never changes anything on the disk.
		if readOnly {
			return protocol.FileResponse{}, errReadOnly
		}
		switch req.Op {
		case protocol.OpMkdir:
			return protocol.FileResponse{}, makeDir(req.Path)
		case protocol.OpRename:
			return protocol.FileResponse{}, renamePath(req.From, req.To)
		case protocol.OpCopy:
			return protocol.FileResponse{}, copyPath(req.From, req.To)
		case protocol.OpChmod:
			return protocol.FileResponse{}, chmodPath(req.Path, req.Mode)
		case protocol.OpWrite:
			return writeText(req.Path, req.Content, req.Version)
		default:
			return protocol.FileResponse{}, deletePath(req.Path, req.Recursive)
		}
	}
	return protocol.FileResponse{}, errUnknownOp
}

// absPath insists on an absolute path, so a request can never mean "wherever
// the agent happens to be running" (protocol.md §4).
func absPath(p string) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", errNotAbsolute
	}
	return filepath.Clean(p), nil
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		return home
	}
	return string(filepath.Separator)
}

func listDir(p string) (protocol.FileResponse, error) {
	dir, err := absPath(p)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		return protocol.FileResponse{}, err
	}

	resp := protocol.FileResponse{Entries: make([]protocol.FileEntry, 0, min(len(entries), listLimit))}
	for _, e := range entries {
		if len(resp.Entries) >= listLimit {
			resp.Truncated = true
			break
		}
		info, err := e.Info()
		if err != nil {
			continue // it was removed between listing and looking at it
		}
		resp.Entries = append(resp.Entries, entryFor(filepath.Join(dir, e.Name()), info))
	}
	return resp, nil
}

func statPath(p string) (protocol.FileResponse, error) {
	abs, err := absPath(p)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	info, err := os.Lstat(abs) // Lstat: describe a link itself, not its target
	if err != nil {
		return protocol.FileResponse{}, err
	}
	entry := entryFor(abs, info)
	return protocol.FileResponse{Entry: &entry}, nil
}

func entryFor(full string, info fs.FileInfo) protocol.FileEntry {
	entry := protocol.FileEntry{
		Name:    info.Name(),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().UTC(),
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		entry.IsLink = true
		// A link to a directory should open like a directory (/bin is one on
		// many systems), so look at what it points at. A broken link stays a
		// plain entry.
		if target, err := os.Stat(full); err == nil {
			entry.IsDir = target.IsDir()
		}
	}
	return entry
}

// makeDir creates one directory. Its parent must already exist: creating a
// whole chain from one typo would be a surprise.
func makeDir(p string) error {
	abs, err := absPath(p)
	if err != nil {
		return err
	}
	err = os.Mkdir(abs, 0o755)
	if errors.Is(err, fs.ErrExist) {
		// The operating system says "file exists" for a folder too, which is
		// confusing when the person sees the folder right there. Say which it is.
		// Stat, not Lstat: a link to a folder is a folder for the person.
		if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
			return errDirExists
		}
		return errFileExists
	}
	return err
}

// renamePath moves or renames. It refuses to replace something that already
// exists, because os.Rename would silently overwrite a file.
func renamePath(from, to string) error {
	src, err := absPath(from)
	if err != nil {
		return err
	}
	dst, err := absPath(to)
	if err != nil {
		return err
	}
	if srcInfo, err := os.Lstat(src); err == nil && isSecretOrAncestor(srcInfo) {
		return errSecretFile
	}
	if dstInfo, err := os.Lstat(dst); err == nil {
		if isSecretFile(dstInfo) {
			return errSecretFile
		}
		return errExists
	}
	return os.Rename(src, dst)
}

// deletePath removes a file or directory. A directory with things inside needs
// recursive: true, so a plain "delete" cannot wipe a tree by accident.
func deletePath(p string, recursive bool) error {
	abs, err := absPath(p)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) == abs {
		return errRoot // "/" (or "C:\"): its parent is itself
	}
	// RemoveAll reports success for a path that is not there. Looking first
	// gives the person the honest answer.
	info, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if isSecretOrAncestor(info) {
		return errSecretFile
	}
	if recursive {
		return os.RemoveAll(abs)
	}
	return os.Remove(abs)
}

// describe turns an error into the few plain words the browser shows. The
// operating system's errors are wrapped with the path ("open /x: no such file
// or directory"), and the person already knows the path, so keep the last part.
func describe(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err.Error()
	}
	// A failed network connection: "dial tcp 127.0.0.1:22: connect: connection
	// refused" becomes "connect: connection refused".
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err.Error()
	}
	return err.Error()
}
