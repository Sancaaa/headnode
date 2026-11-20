package server

import (
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

	// The same bytes uploaded twice are stored once. Replacing an existing file
	// with identical content would be pointless, so keep what is there.
	final := filepath.Join(s.filesDir, sum)
	if _, err := os.Stat(final); err != nil {
		if err := os.Rename(tmpPath, final); err != nil {
			internalError(w, "store upload", err)
			return
		}
	}

	idSuffix, err := randomHex(8)
	if err != nil {
		internalError(w, "create file id", err)
		return
	}
	id := "f_" + idSuffix
	_, err = s.db.Exec(
		`INSERT INTO files (id, name, size, sha256, uploaded_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, size, sum, u.ID, time.Now().Unix())
	if err != nil {
		internalError(w, "record upload", err)
		return
	}

	s.audit(u.ID, "", "file_upload", fmt.Sprintf("file=%s size=%d sha256=%.12s", auditText(name), size, sum))
	slog.Info("file uploaded", "file", name, "size", size, "user", u.Username)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "size": size, "sha256": sum})
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
