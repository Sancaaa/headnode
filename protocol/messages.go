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
	Mode      string `json:"mode,omitempty"`    // chmod: octal, for example "0755"
	Content   string `json:"content"`           // write: the new text of the file
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
	// VNC says whether something accepts connections on 127.0.0.1:5900 on the
	// host, so the dashboard offers the Screen page only where it can work (D-50).
	// An agent older than 0.8.0 does not send it, which reads as false.
	VNC bool `json:"vnc"`
	// VNCControl says that the agent can start and stop the host's VNC server
	// (a TightVNC service is installed): only then is `vnc_set` worth sending (D-51).
	VNCControl bool `json:"vnc_control"`
	// VNCPassword is the password of the VNC server the agent started (D-64): a new
	// one each time. The server keeps it in memory and gives it only with a screen
	// tunnel to someone allowed to open one: it is removed from every heartbeat the
	// server hands out (registry.status). Empty when the VNC server takes none.
	VNCPassword string `json:"vnc_password,omitempty"`
	// Docker says whether the host has a Docker Engine this agent may use: DockerNone,
	// DockerNoAccess or DockerOK (M12, D-70). An agent older than 0.12.0 does not send
	// it, which reads as DockerNone.
	Docker string `json:"docker,omitempty"`
}

// The values of Heartbeat.Docker.
const (
	DockerNone     = "none"      // no socket, nothing answers on it, or not Linux
	DockerNoAccess = "no_access" // the socket is there but this agent's user may not open it
	DockerOK       = "ok"        // /_ping answered
)

// TypeVNCSet is server -> agent: start or stop the host's VNC server. There is no
// reply: the next heartbeat shows the result in `vnc`.
const TypeVNCSet = "vnc_set"

// VNCSet is the body of vnc_set. Enabled false stops the VNC server and keeps it
// from starting at boot; true sets it to start at boot and starts it now.
type VNCSet struct {
	Enabled bool `json:"enabled"`
}

// ---------------------------------------------------------------- Docker (M12, D-70)

// KindDocker is the Docker tab's session (docs/protocol.md §4): JSON requests and
// answers in text frames, like a files session. It needs the role of a shell,
// because the Docker socket is root on the host.
const KindDocker = "docker"

// TypeDockerChange is what the agent reports, on the control connection, after it
// tried to change something in a docker session, Docker's refusals included. It
// plays the part file_change plays for the file manager (D-37): the relay does not
// read the session, so the agent is the one that knows what was asked and what
// Docker answered.
const TypeDockerChange = "docker_change"

// DockerChange says what one change did. It says WHAT happened and never WHO did
// it: the server finds the person from SessionID. Command is the docker command
// form of the change. Error is Docker's own message, only when OK is false.
type DockerChange struct {
	SessionID string `json:"session_id"`
	Op        string `json:"op"`
	Command   string `json:"command"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// The operations of a docker session. The first group only reads; the second
// group changes something, and every one of those is reported as a DockerChange.
const (
	DockerOpContainers = "containers"
	DockerOpInspect    = "inspect"
	DockerOpLogs       = "logs"
	DockerOpImages     = "images"
	DockerOpVolumes    = "volumes"
	DockerOpNetworks   = "networks"
	DockerOpStacks     = "stacks"

	DockerOpStart       = "start"
	DockerOpStop        = "stop"
	DockerOpRestart     = "restart"
	DockerOpRemove      = "remove"
	DockerOpRemoveImage = "remove_image"
	DockerOpPrune       = "prune"
	DockerOpStack       = "stack"
)

// IsDockerChange says whether an operation of a docker session changes something
// (and so is reported and audited). The server uses it on what an agent reports,
// the agent on what it ran.
func IsDockerChange(op string) bool {
	switch op {
	case DockerOpStart, DockerOpStop, DockerOpRestart, DockerOpRemove,
		DockerOpRemoveImage, DockerOpPrune, DockerOpStack:
		return true
	}
	return false
}

// DockerRequest is one request. Which fields are used depends on Op. There is no
// "force" field on purpose: nothing is ever forced (D-70).
type DockerRequest struct {
	ID        int64  `json:"id"`
	Op        string `json:"op"`
	Container string `json:"container,omitempty"` // inspect, logs, start, stop, restart, remove: a name or an id
	Image     string `json:"image,omitempty"`     // remove_image: sha256:<64 hex>, or the 64 hex alone
	Tail      int    `json:"tail,omitempty"`      // logs: how many lines, 1 to 2000 (default 200)
	What      string `json:"what,omitempty"`      // prune: containers, images or build_cache
	All       bool   `json:"all,omitempty"`       // prune images: every image no container uses, not only dangling ones
	Project   string `json:"project,omitempty"`   // stack: the name of a stack that exists
	Action    string `json:"action,omitempty"`    // stack: down, up, restart or recycle
	Volumes   bool   `json:"volumes,omitempty"`   // stack down or recycle: also delete named volumes (-v)
}

// The words of DockerRequest.What and DockerRequest.Action.
const (
	DockerPruneContainers = "containers"
	DockerPruneImages     = "images"
	DockerPruneBuildCache = "build_cache"

	DockerStackDown    = "down"
	DockerStackUp      = "up"
	DockerStackRestart = "restart"
	DockerStackRecycle = "recycle" // down, then up -d
)

// DockerContainer is one line of `docker ps -a`.
type DockerContainer struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Image    string    `json:"image"`
	ImageID  string    `json:"image_id"`
	State    string    `json:"state"`  // running, exited, ...
	Status   string    `json:"status"` // "Up 3 hours"
	Created  time.Time `json:"created"`
	Ports    []string  `json:"ports"`
	Project  string    `json:"project"`  // the Compose project, "" if Compose does not own it
	Service  string    `json:"service"`  // the Compose service
	Revision string    `json:"revision"` // the image's org.opencontainers.image.revision label: which commit runs here
}

// DockerImage is one line of `docker images`.
type DockerImage struct {
	ID       string    `json:"id"`
	Tags     []string  `json:"tags"`
	Size     int64     `json:"size"`
	Created  time.Time `json:"created"`
	UsedBy   []string  `json:"used_by"` // names of the containers that use it
	Dangling bool      `json:"dangling"`
}

// DockerVolume is one line of `docker volume ls`.
type DockerVolume struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Mountpoint string   `json:"mountpoint"`
	Anonymous  bool     `json:"anonymous"` // its name is 64 hex characters: Docker made it, nobody named it
	UsedBy     []string `json:"used_by"`
	Project    string   `json:"project"`
}

// DockerNetworkMember is a container on a network.
type DockerNetworkMember struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// DockerNetwork is one line of `docker network ls`, with who is on it.
type DockerNetwork struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Driver     string                `json:"driver"`
	Scope      string                `json:"scope"`
	Containers []DockerNetworkMember `json:"containers"`
}

// DockerStack is a Compose project, found from the labels of its containers.
// Command is the start of the docker compose command that acts on it, so the page
// can show what a button will run.
type DockerStack struct {
	Project      string   `json:"project"`
	WorkingDir   string   `json:"working_dir"`
	ConfigFiles  []string `json:"config_files"`
	MissingFiles []string `json:"missing_files"` // config files that no longer exist on the host
	Services     []string `json:"services"`
	Containers   []string `json:"containers"`
	Running      int      `json:"running"`
	Total        int      `json:"total"`
	Command      string   `json:"command"`
}

// DockerResponse answers the request with the same ID. When OK is false, Error is
// Docker's own message (or the agent's reason). Command is the docker command that
// was done or tried, in every answer that got as far as knowing it.
type DockerResponse struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Command string `json:"command,omitempty"`

	Containers []DockerContainer `json:"containers,omitempty"`
	Inspect    json.RawMessage   `json:"inspect,omitempty"`
	Logs       string            `json:"logs,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"` // logs: the beginning was cut to fit
	Images     []DockerImage     `json:"images,omitempty"`
	Volumes    []DockerVolume    `json:"volumes,omitempty"`
	Networks   []DockerNetwork   `json:"networks,omitempty"`
	Stacks     []DockerStack     `json:"stacks,omitempty"`

	Removed        []string `json:"removed,omitempty"`         // remove_image, prune: what Docker says it removed
	SpaceReclaimed uint64   `json:"space_reclaimed,omitempty"` // prune, in bytes
	Output         string   `json:"output,omitempty"`          // stack: the last lines of docker compose's output
}
