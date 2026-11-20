// Package protocol holds the JSON shapes shared by the server and the agent.
// Keep it in sync with docs/protocol.md: a change here is a protocol change.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Version is checked in the hello message. Bump it when a change would make
// old agents misbehave against a new server.
const Version = 1

// EnrollRequest is the body of POST /agent/enroll.
type EnrollRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// EnrollResponse is the reply to a successful enroll. AgentSecret is sent
// exactly once; the server keeps only its hash (D-09).
type EnrollResponse struct {
	AgentID     string `json:"agent_id"`
	AgentSecret string `json:"agent_secret"`
}

// Message types on the control connection (docs/protocol.md §3).
const (
	TypeHello     = "hello"
	TypeHeartbeat = "heartbeat"
	TypeOpen      = "open" // server -> agent: please open a session
)

// TypeFileChange is what the agent reports, on the control connection, after it
// tried to change something in a files session (docs/protocol.md §3, D-37).
// The file manager's own traffic goes through the relay, which does not read
// it (D-27), so this is how the server learns what was changed.
const TypeFileChange = "file_change"

// FileChange says what one mkdir, rename or delete did. It says WHAT happened
// and never WHO did it: the server finds the person from SessionID, because it
// created that session for them. Reads (list, stat, home) are not reported.
//
// Op is mkdir, rename, copy, chmod, write or delete. Path is the path acted on
// (for a rename or a copy, the old one). To is the new path, for a rename or a
// copy only. Recursive is for a delete only. Mode is for a chmod only. Size is
// for a write only. Error is short text, only when OK is false.
type FileChange struct {
	SessionID string `json:"session_id"`
	Op        string `json:"op"`
	Path      string `json:"path"`
	To        string `json:"to,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Size      int64  `json:"size,omitempty"` // write only: how many bytes were written
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// Bulk file send (docs/protocol.md §3, D-03): the server tells an agent to
// fetch a file it holds, and the agent reports how it went.
const (
	TypeFetch       = "fetch"        // server -> agent
	TypeFetchResult = "fetch_result" // agent -> server
)

// Fetch asks the agent to download file FileID from the server and put it at
// DestPath. SHA256 and Size are what the server stored when the file was
// uploaded: the agent checks what it received against them before the file
// appears at DestPath.
//
// With Extract the file is a zip archive and DestPath is an existing folder:
// the agent unpacks the archive into it instead of saving the zip itself. That
// is how a whole folder is sent (the file manager zips it). Mode is not used
// then: every file keeps the mode it had in the archive.
type Fetch struct {
	TransferID string `json:"transfer_id"`
	FileID     string `json:"file_id"`
	DestPath   string `json:"dest_path"`
	Mode       string `json:"mode"` // octal, for example "0755"
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Extract    bool   `json:"extract,omitempty"`
}

// FetchResult is the agent's answer to Fetch. Error is empty when OK is true.
type FetchResult struct {
	TransferID string `json:"transfer_id"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// KindShell is the first session kind (docs/protocol.md §4).
const KindShell = "shell"

// Open asks the agent to open a session: the agent then dials
// /agent/sessions/{SessionID}. What Params holds depends on Kind, so it stays
// raw until the agent knows the kind (a ShellParams for a shell).
type Open struct {
	SessionID string          `json:"session_id"`
	Kind      string          `json:"kind"`
	Params    json.RawMessage `json:"params"`
}

// ShellParams are the parameters of a shell session.
type ShellParams struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// KindFiles is the file manager session (docs/protocol.md §4).
const KindFiles = "files"

// FilesParams are the parameters of a files session. The browser does not send
// them: the server works them out from the user's role and puts them in the
// `open` message. ReadOnly means the agent must refuse mkdir, rename and delete.
type FilesParams struct {
	ReadOnly bool `json:"readonly"`
}

// KindTunnel carries a raw TCP connection to a port on the host
// (docs/protocol.md §4): what makes `ssh` to a machine behind NAT possible.
const KindTunnel = "tunnel"

// TunnelParams are the parameters of a tunnel session.
type TunnelParams struct {
	Target string `json:"target"` // "host:port", on the host itself
}

// ParseTunnelTarget checks a tunnel target and returns it in a form that can be
// dialed. Only the machine the agent runs on may be a target (D-35): the host
// must be "localhost" or a loopback address. The server uses this on what the
// user asks for, and the agent uses it again before it dials anything, because
// the agent should not trust anyone.
func ParseTunnelTarget(target string) (string, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return "", errors.New("target must look like host:port, for example 127.0.0.1:22")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("target port must be a number from 1 to 65535")
	}
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return "", errors.New("target must be on the host itself: localhost, or a loopback address like 127.0.0.1")
		}
	}
	return net.JoinHostPort(host, portText), nil
}

// KindDownload is a one-way file read, used by the server itself to stream a
// file to the browser as an HTTP response (docs/protocol.md §4). No browser
// ever attaches to a download session.
const KindDownload = "download"

// DownloadParams are the parameters of a download session.
type DownloadParams struct {
	Path string `json:"path"`
}

// DownloadHeader is the first frame of a download session, a text frame. The
// file's bytes follow as binary frames, and the agent ends with a normal close.
// If the file cannot be read, there is no header: the agent closes with the
// error as the reason instead.
type DownloadHeader struct {
	Name string `json:"name"` // the file's name, without its folder
	Size int64  `json:"size"` // how many bytes to expect
}

// The operations of a files session. Requests and responses are JSON text
// frames, matched by ID (docs/protocol.md §4).
const (
	OpList   = "list"
	OpStat   = "stat"
	OpHome   = "home"
	OpMkdir  = "mkdir"
	OpRename = "rename"
	OpDelete = "delete"
	OpCopy   = "copy"  // a file, or a folder with everything in it
	OpChmod  = "chmod" // the permission bits of one file or folder
	OpProps  = "props" // size, mode, owner...: a read, like stat, but a folder's size is added up
	OpRead   = "read"  // the text of a small file, for the editor
	OpWrite  = "write" // replace the text of a small file, if it has not changed since it was read
)

// MaxEditBytes is the biggest file the editor opens or saves. It keeps one
// request well under the relay's message limit, even after JSON escaping.
const MaxEditBytes = 512 * 1024

// FileRequest is one request. Which fields are used depends on Op.
type FileRequest struct {
	ID        int64  `json:"id"`
	Op        string `json:"op"`
	Path      string `json:"path,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Mode      string `json:"mode,omitempty"` // chmod: octal, for example "0755"
	Content   string `json:"content"`        // write: the new text of the file
	Version   string `json:"version,omitempty"` // write: the version that `read` returned
}

// FileProps is the answer to `props`: what the file manager's "Properties"
// window shows.
type FileProps struct {
	Name       string    `json:"name"`
	IsDir      bool      `json:"is_dir"`
	IsLink     bool      `json:"is_link"`
	LinkTarget string    `json:"link_target,omitempty"`
	Mode       string    `json:"mode"` // like "-rw-r--r--"
	Perm       string    `json:"perm"` // the permission bits in octal, like "0644"
	Owner      string    `json:"owner,omitempty"`
	Group      string    `json:"group,omitempty"`
	ModTime    time.Time `json:"mod_time"`
	// Size is a file's own size. For a folder it is the total of the files inside.
	Size  int64 `json:"size"`
	Files int   `json:"files,omitempty"` // a folder: how many files (and links) are inside, at any depth
	Dirs  int   `json:"dirs,omitempty"`  // a folder: how many folders are inside, at any depth
	// Truncated means the folder was too big to count fully: Size, Files and Dirs
	// are what was found before the limit, so the real numbers are higher.
	Truncated bool `json:"truncated,omitempty"`
}

// FileEntry describes one file, directory or link.
type FileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"` // for a link: whether what it points at is a directory
	IsLink  bool      `json:"is_link"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"` // like "-rw-r--r--"
	ModTime time.Time `json:"mod_time"`
}

// FileResponse answers the request with the same ID. When OK is false, Error
// says why in a few plain words.
type FileResponse struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Entries   []FileEntry `json:"entries,omitempty"`   // list
	Truncated bool        `json:"truncated,omitempty"` // list: the directory has more entries than were sent
	Entry     *FileEntry  `json:"entry,omitempty"`     // stat
	Props     *FileProps  `json:"props,omitempty"`     // props
	Text      string      `json:"text,omitempty"`      // read
	Version   string      `json:"version,omitempty"`   // read, write: changes whenever the file does
	Path      string      `json:"path,omitempty"`      // home
}

// TypeResize is the text frame the browser sends when the terminal changes
// size. The server relays it without looking at it, the agent acts on it.
const TypeResize = "resize"

type Resize struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

const (
	defaultCols     = 80
	defaultRows     = 24
	maxTerminalSize = 500 // no real terminal is bigger, and it stops silly values
)

// ClampSize makes a terminal size usable: a missing or non-positive value
// becomes the classic 80x24, and a huge one is capped. The server uses it on
// what the browser sends, and the agent uses it again on what the server
// sends, because the agent should not trust anyone's numbers.
func ClampSize(cols, rows int) (int, int) {
	return clampDimension(cols, defaultCols), clampDimension(rows, defaultRows)
}

func clampDimension(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return min(n, maxTerminalSize)
}

// Envelope wraps every control message: {"type": "...", "data": {...}}.
// Data stays raw until the type is known, so no reflection is needed:
// decode the envelope first, then decode Data into the struct for that type.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// NewMessage encodes a typed message, ready to send as one WebSocket text frame.
func NewMessage(msgType string, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode %s data: %w", msgType, err)
	}
	return json.Marshal(Envelope{Type: msgType, Data: raw})
}

// Hello is sent once, as the first message after connecting.
type Hello struct {
	Protocol     int      `json:"protocol"`
	AgentVersion string   `json:"agent_version"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Platform     string   `json:"platform"`
	Kernel       string   `json:"kernel"`
	CPUModel     string   `json:"cpu_model"`
	CPUCores     int      `json:"cpu_cores"`
	MemTotal     uint64   `json:"mem_total"`
	LANIPs       []string `json:"lan_ips"`
}

// Heartbeat is sent every 20 seconds. Sizes are in bytes.
type Heartbeat struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskTotal  uint64  `json:"disk_total"`
	UptimeSec  uint64  `json:"uptime_sec"`
}
