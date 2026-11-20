package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"headnode/protocol"
)

const (
	transferPending = "pending"
	transferOK      = "ok"
	transferError   = "error"

	// How long a finished transfer stays readable. Status lives in memory only
	// (D-03), so this just stops the table from growing forever.
	transferKeepFor = 24 * time.Hour

	maxTransferHosts = 200

	// The Transfers page shows this many of a person's most recent sends.
	maxListedTransfers = 50
)

// A mode is three octal digits, with an optional leading 0: "644", "0755".
// Four-digit modes with setuid, setgid or sticky bits are refused: sending a
// file that runs as someone else is not something this tool should make easy.
var modeRe = regexp.MustCompile(`^0?[0-7]{3}$`)

// transferHost is how one host is doing.
type transferHost struct {
	agentID string
	status  string // pending, ok or error
	err     string
}

// transfer is one file sent to one or more hosts.
type transfer struct {
	id       string
	userID   int64 // who started it, and who may look at its status
	userName string
	fileID   string
	fileName string
	size     int64
	destPath string
	mode     string
	extract  bool // the file is a zip to unpack into destPath (a whole folder is being sent)
	created  time.Time
	hosts    []*transferHost
}

// transferTable holds every transfer in progress, and finished ones for a day.
// A Mutex and a map, like the other in-memory tables (principles.md §2).
type transferTable struct {
	mu   sync.Mutex
	byID map[string]*transfer
}

func newTransferTable() *transferTable {
	return &transferTable{byID: make(map[string]*transfer)}
}

func (t *transferTable) add(tr *transfer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, old := range t.byID {
		if time.Since(old.created) > transferKeepFor {
			delete(t.byID, id)
		}
	}
	t.byID[tr.id] = tr
}

type transferSnapshot struct {
	tr    transfer
	hosts []transferHost
}

// snapshots returns a copy of every transfer, safe to read after the lock is
// released.
func (t *transferTable) snapshots() []transferSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]transferSnapshot, 0, len(t.byID))
	for _, tr := range t.byID {
		hosts := make([]transferHost, len(tr.hosts))
		for i, h := range tr.hosts {
			hosts[i] = *h
		}
		out = append(out, transferSnapshot{tr: *tr, hosts: hosts})
	}
	return out
}

// view returns a copy that is safe to read after the lock is released.
func (t *transferTable) view(id string) (transfer, []transferHost, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, ok := t.byID[id]
	if !ok {
		return transfer{}, nil, false
	}
	hosts := make([]transferHost, len(tr.hosts))
	for i, h := range tr.hosts {
		hosts[i] = *h
	}
	return *tr, hosts, true
}

// finish records an agent's answer. It only counts if that agent really has
// this transfer and has not already answered: a host cannot report on someone
// else's transfer, or change its mind.
func (t *transferTable) finish(transferID, agentID string, ok bool, message string) (transfer, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr, found := t.byID[transferID]
	if !found {
		return transfer{}, false
	}
	for _, h := range tr.hosts {
		if h.agentID == agentID && h.status == transferPending {
			h.status = transferOK
			if !ok {
				h.status = transferError
				h.err = message
			}
			return *tr, true
		}
	}
	return transfer{}, false
}

// failPending marks every transfer still waiting on this agent as failed. Used
// when the agent's control connection is gone: it cannot report any more.
func (t *transferTable) failPending(agentID, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.byID {
		for _, h := range tr.hosts {
			if h.agentID == agentID && h.status == transferPending {
				h.status = transferError
				h.err = reason
			}
		}
	}
}

// hasPending says whether this agent is, right now, supposed to be fetching
// this file. It is the only thing that lets an agent download an uploaded file:
// an agent that was not told to fetch it cannot read it.
func (t *transferTable) hasPending(agentID, fileID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.byID {
		if tr.fileID != fileID {
			continue
		}
		for _, h := range tr.hosts {
			if h.agentID == agentID && h.status == transferPending {
				return true
			}
		}
	}
	return false
}

// transferStuckAfter is how long a transfer may stay unanswered and still count
// as "in use". A host that never answers (an old agent that ignores `fetch`, or
// one that hung) would otherwise keep its file from being deleted for good,
// because a pending status only ends when the host answers or disconnects.
const transferStuckAfter = time.Hour

// filesInUse is the set of uploaded files that some transfer is still waiting
// on: at least one of its hosts has not answered yet. Such a file must not be
// deleted, or that host's fetch would fail half way (D-38).
func (t *transferTable) filesInUse() map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	inUse := map[string]bool{}
	for _, tr := range t.byID {
		if time.Since(tr.created) > transferStuckAfter {
			continue
		}
		for _, h := range tr.hosts {
			if h.status == transferPending {
				inUse[tr.fileID] = true
				break
			}
		}
	}
	return inUse
}

// ------------------------------------------------------------------ HTTP

type transferHostView struct {
	AgentID  string  `json:"agent_id"`
	HostName *string `json:"host_name"` // null if the host has been deleted since
	Status   string  `json:"status"`
	Error    string  `json:"error,omitempty"`
}

type transferView struct {
	ID        string             `json:"id"`
	FileName  string             `json:"file_name"`
	FileSize  int64              `json:"file_size"`
	DestPath  string             `json:"dest_path"`
	Mode      string             `json:"mode"`
	Extract   bool               `json:"extract"` // dest_path is a folder the file was unpacked into
	StartedBy string             `json:"started_by"`
	CreatedAt time.Time          `json:"created_at"`
	Hosts     []transferHostView `json:"hosts"`
}

// hostNames returns every host's current name, by id. A transfer remembers ids,
// and the names are looked up when it is shown, so a rename shows up and a
// deleted host shows as null.
func (s *Server) hostNames() (map[string]string, error) {
	names := map[string]string{}
	rows, err := s.db.Query(`SELECT id, name FROM agents`)
	if err != nil {
		return nil, fmt.Errorf("query host names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan host name: %w", err)
		}
		names[id] = name
	}
	return names, rows.Err()
}

// newTransferView builds what the API returns for a transfer.
func newTransferView(tr transfer, hosts []transferHost, names map[string]string) transferView {
	view := transferView{
		ID: tr.id, FileName: tr.fileName, FileSize: tr.size, DestPath: tr.destPath, Mode: tr.mode, Extract: tr.extract,
		StartedBy: tr.userName, CreatedAt: tr.created.UTC(), Hosts: make([]transferHostView, 0, len(hosts)),
	}
	for _, h := range hosts {
		hv := transferHostView{AgentID: h.agentID, Status: h.status, Error: h.err}
		if name, ok := names[h.agentID]; ok {
			hv.HostName = &name
		}
		view.Hosts = append(view.Hosts, hv)
	}
	return view
}

func (s *Server) describeTransfer(tr transfer, hosts []transferHost) (transferView, error) {
	names, err := s.hostNames()
	if err != nil {
		return transferView{}, err
	}
	return newTransferView(tr, hosts, names), nil
}

// handleListTransfers is GET /api/transfers: the transfers the user started,
// newest first, or everyone's for an admin. It is what the Transfers page
// shows, since the status of a send lives only in memory (D-03).
func (s *Server) handleListTransfers(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	names, err := s.hostNames()
	if err != nil {
		internalError(w, "list host names", err)
		return
	}

	all := s.transfers.snapshots()
	sort.Slice(all, func(i, j int) bool { return all[i].tr.created.After(all[j].tr.created) })
	views := []transferView{} // not nil, so "no transfers" is [] and not null
	for _, snap := range all {
		if snap.tr.userID != u.ID && !u.IsAdmin {
			continue
		}
		views = append(views, newTransferView(snap.tr, snap.hosts, names))
		if len(views) == maxListedTransfers {
			break
		}
	}
	writeJSON(w, http.StatusOK, views)
}

// handleCreateTransfer is POST /api/transfers (protocol.md §1): send an
// uploaded file to several hosts. It checks everything before it sends
// anything, so a request is either started for every host or refused.
func (s *Server) handleCreateTransfer(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	var req struct {
		FileID   string   `json:"file_id"`
		AgentIDs []string `json:"agent_ids"`
		DestPath string   `json:"dest_path"`
		Mode     string   `json:"mode"`
		Extract  bool     `json:"extract"`
	}
	if !readJSONLimit(w, r, &req, 64<<10) {
		return
	}

	// The same host listed twice would fetch the file twice into the same place.
	var agentIDs []string
	seen := map[string]bool{}
	for _, id := range req.AgentIDs {
		if !seen[id] {
			seen[id] = true
			agentIDs = append(agentIDs, id)
		}
	}
	if len(agentIDs) == 0 || len(agentIDs) > maxTransferHosts {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("agent_ids must name between 1 and %d hosts", maxTransferHosts))
		return
	}
	if req.DestPath == "" || len(req.DestPath) > 4096 || strings.ContainsRune(req.DestPath, 0) {
		writeError(w, http.StatusBadRequest, "dest_path is required")
		return
	}
	mode := req.Mode
	if req.Extract {
		// An unpacked folder keeps the modes its files had: a single mode for all of
		// them has no meaning, so none is taken.
		mode = ""
	} else {
		if mode == "" {
			mode = "0644"
		}
		if !modeRe.MatchString(mode) {
			writeError(w, http.StatusBadRequest, "mode must be three octal digits, like 0644 or 0755")
			return
		}
	}

	// Every target must be a host the user may write to. This comes first: it is
	// the main rule, and it does not depend on which file is being sent.
	for _, id := range agentIDs {
		if !s.requireHostAction(w, u, id, actionFilesWrite, "you do not have permission to send files to host "+id) {
			return
		}
	}

	// The file must be one this user may send: their own upload, or any upload
	// for an admin. Anyone else gets "not found", like a hidden host.
	var file struct {
		name, sha256 string
		size         int64
		uploadedBy   int64
	}
	err := s.db.QueryRow(`SELECT name, sha256, size, uploaded_by FROM files WHERE id = ?`, req.FileID).
		Scan(&file.name, &file.sha256, &file.size, &file.uploadedBy)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && file.uploadedBy != u.ID && !u.IsAdmin) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		internalError(w, "look up file", err)
		return
	}
	// Only an archive can be unpacked. This is a sanity check (the agent refuses
	// anything that is not a zip anyway), so the person gets the reason at once.
	if req.Extract && !strings.HasSuffix(strings.ToLower(file.name), ".zip") {
		writeError(w, http.StatusBadRequest, "only a .zip file can be unpacked")
		return
	}

	idSuffix, err := randomHex(8)
	if err != nil {
		internalError(w, "create transfer id", err)
		return
	}
	tr := &transfer{
		id: "t_" + idSuffix, userID: u.ID, userName: u.Username, fileID: req.FileID,
		fileName: file.name, size: file.size, destPath: req.DestPath, mode: mode, extract: req.Extract, created: time.Now(),
	}
	for _, id := range agentIDs {
		tr.hosts = append(tr.hosts, &transferHost{agentID: id, status: transferPending})
	}
	s.transfers.add(tr)
	unpack := ""
	if req.Extract {
		unpack = " unpack=yes"
	}
	s.audit(u.ID, "", "transfer_start", fmt.Sprintf("file=%s sha256=%.12s dest=%s hosts=%d%s",
		auditText(file.name), file.sha256, auditText(req.DestPath), len(agentIDs), unpack))
	slog.Info("transfer started", "transfer", tr.id, "file", file.name, "size", file.size,
		"dest", req.DestPath, "hosts", len(agentIDs), "user", u.Username)

	// Tell each online host to fetch. An offline one cannot be told: its entry
	// fails at once, and the others go ahead (D-03: the server does not queue).
	command := protocol.Fetch{
		TransferID: tr.id, FileID: req.FileID, DestPath: req.DestPath, Mode: mode,
		SHA256: file.sha256, Size: file.size, Extract: req.Extract,
	}
	for _, id := range agentIDs {
		err := s.agents.send(id, protocol.TypeFetch, command)
		if err == nil {
			continue
		}
		message := "could not reach the host"
		if errors.Is(err, errAgentOffline) {
			message = "host offline"
		} else {
			slog.Error("send fetch to agent", "agent_id", id, "err", err)
		}
		s.transfers.finish(tr.id, id, false, message)
	}

	snapshot, hosts, _ := s.transfers.view(tr.id)
	view, err := s.describeTransfer(snapshot, hosts)
	if err != nil {
		internalError(w, "build transfer view", err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// handleGetTransfer is GET /api/transfers/{id}: how each host is doing. Only the
// user who started the transfer, or an admin, can look.
func (s *Server) handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	snapshot, hosts, ok := s.transfers.view(r.PathValue("id"))
	if !ok || (snapshot.userID != u.ID && !u.IsAdmin) {
		writeError(w, http.StatusNotFound, "transfer not found")
		return
	}
	view, err := s.describeTransfer(snapshot, hosts)
	if err != nil {
		internalError(w, "build transfer view", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// finishTransfer handles an agent's fetch_result on its control connection.
func (s *Server) finishTransfer(agentID string, res protocol.FetchResult) {
	// The agent's wording is shown to people and written to the log, so keep it
	// short and free of control characters.
	message := auditText(res.Error)
	if len(message) > 300 {
		message = message[:300]
	}
	tr, ok := s.transfers.finish(res.TransferID, agentID, res.OK, message)
	if !ok {
		slog.Warn("fetch result for an unknown or finished transfer", "agent_id", agentID, "transfer", res.TransferID)
		return
	}
	slog.Info("transfer result", "transfer", res.TransferID, "agent_id", agentID, "ok", res.OK, "error", message)
	detail := fmt.Sprintf("file=%s dest=%s ok", auditText(tr.fileName), auditText(tr.destPath))
	if !res.OK {
		detail = fmt.Sprintf("file=%s dest=%s failed: %s", auditText(tr.fileName), auditText(tr.destPath), message)
	}
	// Recorded under the user who started the transfer, on the host it ran on.
	s.audit(tr.userID, agentID, "transfer_result", detail)
}
