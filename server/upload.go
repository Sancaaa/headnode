package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/coder/websocket"
)

// A ceiling, not a target: far more than the programs and archives this tool
// is for, but enough to stop a runaway upload from filling the disk by itself.
const maxUploadBytes = 4 << 30

// canUpload says whether the user may put files on the server: an admin, or
// anyone who is an operator in at least one group (the files_write permission
// somewhere). Sending a file to a particular host is checked per host, later.
func (s *Server) canUpload(u user) (bool, error) {
	if u.IsAdmin {
		return true, nil
	}
	roles, err := s.roles(u)
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if roleAllows(role, actionFilesWrite) {
			return true, nil
		}
	}
	return false, nil
}

// handleUpload is POST /api/files (protocol.md §1): a multipart upload with one
// part named "file". The bytes are streamed to disk while they are hashed, so
// the server never holds the file in memory, and the file is stored under its
// SHA-256 (D-03).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if s.filesDir == "" {
		writeError(w, http.StatusServiceUnavailable, "file uploads are not set up on this server")
		return
	}
	allowed, err := s.canUpload(u)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "you do not have permission to upload files")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	// MultipartReader hands over the parts as a stream. ParseMultipartForm would
	// buffer the whole upload (in memory, then in temporary files).
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}
	var name string
	var tmpPath string
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath) // after a successful rename there is nothing to remove, which is fine
		}
	}()

	var sum string
	var size int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, `no file in the upload: send it as the form field "file"`)
			return
		}
		if err != nil {
			writeUploadError(w, err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}

		name = cleanFileName(part.FileName())
		tmp, err := os.CreateTemp(s.filesDir, ".upload-*") // created 0600
		if err != nil {
			internalError(w, "create upload file", err)
			return
		}
		tmpPath = tmp.Name()
		hasher := sha256.New()
		size, err = io.Copy(io.MultiWriter(tmp, hasher), part)
		closeErr := tmp.Close()
		if err != nil {
			writeUploadError(w, err)
			return
		}
		if closeErr != nil {
			internalError(w, "write upload file", closeErr)
			return
		}
		sum = hex.EncodeToString(hasher.Sum(nil))
		break
	}

	idSuffix, err := randomHex(8)
	if err != nil {
		internalError(w, "create file id", err)
		return
	}
	id := "f_" + idSuffix
	if err := s.storeUpload(tmpPath, sum, id, name, size, u.ID); err != nil {
		internalError(w, "store upload", err)
		return
	}

	s.audit(u.ID, "", "file_upload", fmt.Sprintf("file=%s size=%d sha256=%.12s", auditText(name), size, sum))
	slog.Info("file uploaded", "file", name, "size", size, "user", u.Username)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "size": size, "sha256": sum})
}

// handlePullFile is POST /api/hosts/{id}/pull (protocol.md §1): take a file that
// is on a host and store it on the server as an upload, so it can be sent to
// other hosts ("Send to hosts" in the file manager). It is the host-to-server
// direction of what handleUpload does for the browser, and everything after the
// bytes arrive is the same: stored by hash, listed as an upload, sent with
// POST /api/transfers.
//
// The bytes come over a `download` session (D-32), so the agent needed nothing
// new, and they are streamed to disk while hashed: nothing is held in memory.
func (s *Server) handlePullFile(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	hostID := r.PathValue("id")
	if s.filesDir == "" {
		writeError(w, http.StatusServiceUnavailable, "file uploads are not set up on this server")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Path == "" || len(req.Path) > 4096 || strings.ContainsRune(req.Path, 0) {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	// Reading the file needs the same permission as downloading it, and storing it
	// needs the same as uploading: this is both at once, so it needs both.
	if !s.requireHostAction(w, u, hostID, actionFilesRead, "you do not have permission to read files on this host") {
		return
	}
	allowed, err := s.canUpload(u)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "you do not have permission to upload files")
		return
	}

	stream, status, message := s.openHostFile(r.Context(), u.ID, hostID, req.Path)
	if stream == nil {
		if status != 0 {
			writeError(w, status, message)
		}
		return
	}
	defer stream.close()
	// A folder arrives as a zip archive of unknown size (-1 in the header): the
	// agent builds it as it sends it. It is stored as an ordinary upload named
	// "<folder>.zip", and sending it with `extract` unpacks it on the hosts.
	isFolder := stream.head.Size < 0
	if stream.head.Size > maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than the %d GB limit", maxUploadBytes>>30))
		return
	}

	tmp, err := os.CreateTemp(s.filesDir, ".upload-*") // created 0600
	if err != nil {
		internalError(w, "create upload file", err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // after a successful rename there is nothing to remove, which is fine
	hasher := sha256.New()
	var size int64
	for {
		// A host that goes quiet in the middle must not hold this request forever.
		readCtx, cancel := context.WithTimeout(r.Context(), downloadStallTimeout)
		kind, data, err := stream.conn.Read(readCtx)
		cancel()
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				break // the whole file was sent
			}
			tmp.Close()
			slog.Warn("pull from host interrupted", "agent_id", hostID, "err", err)
			writeError(w, http.StatusBadGateway, "the transfer from the host broke off")
			return
		}
		if kind != websocket.MessageBinary {
			continue
		}
		size += int64(len(data))
		if size > maxUploadBytes {
			tmp.Close()
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than the %d GB limit", maxUploadBytes>>30))
			return
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			internalError(w, "write upload file", err)
			return
		}
		hasher.Write(data)
	}
	if err := tmp.Close(); err != nil {
		internalError(w, "write upload file", err)
		return
	}
	// A file that was written to while it was being copied (a log) is not the file
	// the header described. Better to say so than to store a torn copy.
	if !isFolder && size != stream.head.Size {
		writeError(w, http.StatusConflict, "the file changed while it was being copied: try again")
		return
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	name := cleanFileName(stream.head.Name)
	idSuffix, err := randomHex(8)
	if err != nil {
		internalError(w, "create file id", err)
		return
	}
	id := "f_" + idSuffix
	if err := s.storeUpload(tmpPath, sum, id, name, size, u.ID); err != nil {
		internalError(w, "store upload", err)
		return
	}

	from := auditText(req.Path)
	if isFolder {
		from += " (a folder, zipped)"
	}
	s.audit(u.ID, hostID, "file_upload", fmt.Sprintf("file=%s size=%d sha256=%.12s from=%s", auditText(name), size, sum, from))
	slog.Info("file pulled from a host", "file", name, "size", size, "folder", isFolder, "agent_id", hostID, "user", u.Username)
	// "archive" tells the page this is a zipped folder: send it with `extract`.
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "size": size, "sha256": sum, "archive": isFolder})
}

// storeUpload puts a finished upload in its place and records it.
//
// The same bytes uploaded twice are stored once, as one blob (data/files/<sha256>)
// with one row per upload, so two rows can point at the same blob. That is why
// deleting an upload must not remove a blob that another row still uses
// (handleDeleteFile). blobMu keeps this from interleaving with such a delete.
func (s *Server) storeUpload(tmpPath, sum, id, name string, size, userID int64) error {
	s.blobMu.Lock()
	defer s.blobMu.Unlock()

	final := filepath.Join(s.filesDir, sum)
	// Replacing an existing blob with identical content would be pointless, so
	// keep what is there.
	if _, err := os.Stat(final); err != nil {
		if err := os.Rename(tmpPath, final); err != nil {
			return fmt.Errorf("store blob: %w", err)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO files (id, name, size, sha256, uploaded_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, size, sum, userID, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("record upload: %w", err)
	}
	return nil
}

// maxListedFiles is how many uploads the Uploads page shows, newest first.
const maxListedFiles = 500

type fileView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	UploadedBy *string   `json:"uploaded_by"` // null if that user has been deleted
	CreatedAt  time.Time `json:"created_at"`
	InUse      bool      `json:"in_use"` // a transfer of it is still running: it cannot be deleted now
}

// handleListFiles is GET /api/files (admin only): every upload on the server,
// newest first. Uploads were not listed anywhere before: a person only saw the
// transfers they made from them.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(
		`SELECT f.id, f.name, f.size, f.created_at, u.username
		 FROM files f
		 LEFT JOIN users u ON u.id = f.uploaded_by
		 ORDER BY f.created_at DESC, f.rowid DESC
		 LIMIT ?`, maxListedFiles)
	if err != nil {
		internalError(w, "list files", err)
		return
	}
	defer rows.Close()

	inUse := s.transfers.filesInUse()
	files := []fileView{} // not nil, so "no uploads" is [] and not null
	for rows.Next() {
		var f fileView
		var at int64
		var username sql.NullString
		if err := rows.Scan(&f.ID, &f.Name, &f.Size, &at, &username); err != nil {
			internalError(w, "scan file", err)
			return
		}
		f.CreatedAt = time.Unix(at, 0).UTC()
		if username.Valid {
			f.UploadedBy = &username.String
		}
		f.InUse = inUse[f.ID]
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		internalError(w, "read files", err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

// handleDeleteFile is DELETE /api/files/{id} (admin only, Origin-checked by
// requireLogin like every change): remove one upload, and its blob when no other
// upload shares it. There is no automatic cleanup: an admin decides (D-38).
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if s.filesDir == "" {
		writeError(w, http.StatusServiceUnavailable, "file uploads are not set up on this server")
		return
	}
	id := r.PathValue("id")

	s.blobMu.Lock()
	defer s.blobMu.Unlock()

	var name, sum string
	err := s.db.QueryRow(`SELECT name, sha256 FROM files WHERE id = ?`, id).Scan(&name, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		internalError(w, "look up file", err)
		return
	}

	// An agent that was told to fetch this file is downloading it right now, or
	// about to. Taking it away would make that transfer fail half way.
	if s.transfers.filesInUse()[id] {
		writeError(w, http.StatusConflict, "this file is being sent to a host right now: wait for the transfer to finish, then delete it")
		return
	}

	if _, err := s.db.Exec(`DELETE FROM files WHERE id = ?`, id); err != nil {
		internalError(w, "delete file", err)
		return
	}

	// The blob goes only when this was the last row that used it.
	var others int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files WHERE sha256 = ?`, sum).Scan(&others); err != nil {
		// The row is gone. Keeping the blob is the safe side of not knowing.
		slog.Error("count other uploads of a blob", "sha256", sum, "err", err)
		others = 1
	}
	blob := "kept (another upload uses it)"
	var removeErr error
	if others == 0 {
		blob = "removed"
		// sum is hex from our own database, never a path from the request.
		if err := os.Remove(filepath.Join(s.filesDir, sum)); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr = err
			blob = "NOT removed: " + err.Error()
		}
	}

	s.audit(u.ID, "", "upload_delete", fmt.Sprintf("file=%s sha256=%.12s blob=%s", auditText(name), sum, auditText(blob)))
	slog.Info("upload deleted", "file", name, "blob", blob, "user", u.Username)
	if removeErr != nil {
		slog.Error("remove blob", "sha256", sum, "err", removeErr)
		writeError(w, http.StatusInternalServerError, "the upload was removed from the list, but its data could not be deleted from the disk: see the server log")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeUploadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than the %d GB limit", maxUploadBytes>>30))
		return
	}
	// Most often the browser stopped sending (the tab was closed).
	slog.Warn("upload failed", "err", err)
	writeError(w, http.StatusBadRequest, "the upload did not complete")
}

// cleanFileName makes a name from the browser safe to store and to show: no
// folders, no control characters, not empty, not absurdly long.
func cleanFileName(raw string) string {
	name := path.Base(strings.ReplaceAll(raw, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "upload"
	}
	for len(name) > 255 {
		name = name[:len(name)-1]
	}
	return strings.ToValidUTF8(name, "")
}

// handleAgentFile is GET /agent/files/{id}: an agent downloads an uploaded file
// because it was told to (`fetch`). It serves only a file that this very agent
// has a pending transfer for, so an enrolled host cannot read other uploads.
// http.ServeContent gives us Range support, so an interrupted fetch could be
// resumed, without any code of ours.
func (s *Server) handleAgentFile(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.authenticateAgent(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid agent credentials")
		return
	}
	fileID := r.PathValue("id")
	if !s.transfers.hasPending(agentID, fileID) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	var name, sum string
	err := s.db.QueryRow(`SELECT name, sha256 FROM files WHERE id = ?`, fileID).Scan(&name, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		internalError(w, "look up file", err)
		return
	}
	file, err := os.Open(filepath.Join(s.filesDir, sum))
	if err != nil {
		internalError(w, "open stored file", err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, time.Time{}, file)
}
