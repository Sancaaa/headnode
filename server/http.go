package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"headnode/protocol"
	"headnode/web"
)

// Version is the server's own version, shown at the bottom of the sidebar (it
// comes to the page in /api/me). The agent keeps its version the same way, in
// one constant (agent.Version). Change it here when a release is cut.
const Version = "0.1.0"

// Server holds what the HTTP handlers share. It is a plain struct, not an
// interface: handlers are methods so they can reach s.db without globals.
type Server struct {
	db         *sql.DB
	agents     *registry      // which agents are online right now
	sessions   *sessionTable  // shells waiting for their two sides, or running
	transfers  *transferTable // file sends in progress or recently finished (D-03)
	loginLimit *loginLimiter  // slows down password guessing
	filesDir   string         // where uploaded files are stored; "" = uploads are off
	agentDir   string         // where the built agent programs are; "" = /download is off
	started    time.Time      // when this server started: the page works out the uptime

	// blobMu makes "store a blob and record its row" and "delete a row and maybe
	// its blob" run one at a time. Without it, a delete could remove a blob in
	// the instant between an upload noticing it already exists and recording
	// the row that points to it (D-38).
	blobMu sync.Mutex
}

func New(db *sql.DB) *Server {
	return &Server{
		db:         db,
		agents:     newRegistry(),
		sessions:   newSessionTable(sessionPairTimeout),
		transfers:  newTransferTable(),
		loginLimit: newLoginLimiter(),
		started:    time.Now(),
	}
}

// UseFilesDir turns on file uploads, storing the files in dir (created if it
// does not exist). Only the owner may read it: an uploaded file can be a
// program or a secret.
func (s *Server) UseFilesDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create files directory: %w", err)
	}
	s.filesDir = dir
	return nil
}

// Handler returns the router. Go 1.22+ patterns like "POST /agent/enroll"
// let the mux check the method for us (D-04).
func (s *Server) Handler() http.Handler {
	// Default-deny (D-22): every route registered on `api` sits behind the
	// login check, because the whole mux is wrapped below. A new API route
	// cannot forget authentication, it would have to be registered on the
	// outer mux on purpose, as login is.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/hosts", s.handleListHosts)
	api.HandleFunc("GET /api/hosts/{id}", s.handleGetHost)
	api.HandleFunc("GET /api/me", s.handleMe)
	api.HandleFunc("POST /api/logout", s.handleLogout)
	// Admin-only routes. adminOnly is a second, narrower check on top of the
	// login wrapper, so a test (TestAdminRoutesRequireAdmin) lists every one.
	api.HandleFunc("GET /api/users", s.adminOnly(s.handleListUsers))
	api.HandleFunc("POST /api/users", s.adminOnly(s.handleCreateUser))
	api.HandleFunc("GET /api/groups", s.adminOnly(s.handleListGroups))
	api.HandleFunc("POST /api/groups", s.adminOnly(s.handleCreateGroup))
	api.HandleFunc("GET /api/groups/{id}", s.adminOnly(s.handleGetGroup))
	api.HandleFunc("PUT /api/groups/{id}/members/{user_id}", s.adminOnly(s.handleSetMember))
	api.HandleFunc("DELETE /api/groups/{id}/members/{user_id}", s.adminOnly(s.handleRemoveMember))
	api.HandleFunc("POST /api/enroll-tokens", s.adminOnly(s.handleCreateEnrollToken))
	api.HandleFunc("GET /api/audit", s.adminOnly(s.handleListAudit))
	// Not admin-only: any user with the operator role may open a terminal.
	// handleOpenSession checks that with can().
	api.HandleFunc("POST /api/hosts/{id}/sessions", s.handleOpenSession)
	// Any role with files_read may download: handleDownload checks that.
	api.HandleFunc("GET /api/hosts/{id}/download", s.handleDownload)
	// "Send to hosts" in the file manager: copy a file from a host to the server's
	// uploads. Reading the file and storing an upload are both checked in the handler.
	api.HandleFunc("POST /api/hosts/{id}/pull", s.handlePullFile)
	// Uploading and sending files need the files_write permission (on the target
	// hosts for a send, on any group for an upload): the handlers check it.
	api.HandleFunc("POST /api/files", s.handleUpload)
	// Looking at every upload and deleting one are admin matters (D-38).
	api.HandleFunc("GET /api/files", s.adminOnly(s.handleListFiles))
	api.HandleFunc("DELETE /api/files/{id}", s.adminOnly(s.handleDeleteFile))
	api.HandleFunc("POST /api/transfers", s.handleCreateTransfer)
	api.HandleFunc("GET /api/transfers", s.handleListTransfers)
	api.HandleFunc("GET /api/transfers/{id}", s.handleGetTransfer)
	api.HandleFunc("PATCH /api/hosts/{id}", s.adminOnly(s.handlePatchHost))
	api.HandleFunc("DELETE /api/hosts/{id}", s.adminOnly(s.handleDeleteHost))
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	mux := http.NewServeMux()
	// Public: the web page itself (it holds no data, and it has to load
	// before anyone can log in), the agent endpoints (they have their own
	// credentials), and login.
	mux.Handle("GET /", uiHandler())
	mux.HandleFunc("POST /agent/enroll", s.handleEnroll)
	mux.HandleFunc("GET /agent/connect", s.handleAgentConnect)
	mux.HandleFunc("GET /agent/sessions/{id}", s.handleAgentSession)
	mux.HandleFunc("GET /agent/files/{id}", s.handleAgentFile)
	// The agent program, for the install command. Public on purpose (see the handler).
	mux.HandleFunc("GET /download/{name}", s.handleAgentBinary)
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	// The browser's WebSocket for a session. It is not under /api/, so it gets
	// the login wrapper by hand. handleUISession adds the Origin check.
	mux.Handle("GET /ui/sessions/{id}", s.requireLogin(http.HandlerFunc(s.handleUISession)))

	// Everything else under /api/. One line per method, because a pattern
	// without a method would conflict with "GET /" above. "POST /api/login"
	// is more specific than "POST /api/", so login still wins.
	guarded := s.requireLogin(api)
	for _, method := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
		mux.Handle(method+" /api/", guarded)
	}
	return mux
}

// uiHandler serves the embedded web pages (D-06).
func uiHandler() http.Handler {
	files := http.FileServerFS(web.Files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// web/ has no inline scripts or styles, so everything can be locked to
		// our own origin. If a bug ever lets agent-supplied text become markup,
		// the browser still refuses to run it. frame-ancestors stops other
		// sites from embedding the dashboard in a frame (clickjacking).
		//
		// style-src also allows inline styles, for xterm.js (D-28): it creates
		// <style> elements at run time to size and color the terminal. Scripts
		// stay locked to our own files.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so all we can do is log it.
		slog.Error("write response", "err", err)
	}
}

// writeError uses the error shape from protocol.md: {"error": "message"}.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// readJSON decodes a small JSON request body into into. On failure it has
// already answered 400 and returns false, so the caller just returns.
func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	// Almost every request body we accept is a few hundred bytes.
	return readJSONLimit(w, r, into, 4096)
}

// readJSONLimit is readJSON with a bigger cap, for a request that can carry a
// list (a file send names every target host).
func readJSONLimit(w http.ResponseWriter, r *http.Request, into any, limit int64) bool {
	// The cap stops anyone from making the server read a huge body.
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// sameOrigin is the CSRF / cross-site WebSocket defense (architecture.md §7).
// A browser attaches an Origin header to every cross-site request and says
// which site the page came from. If that is not this server, another website
// is making the browser send the request, and we refuse it. A request with no
// Origin at all is refused too: browsers always send one on POST, so a missing
// one means the caller is not a browser tab, and cookies must not be accepted.
//
// We compare the Origin's host with the Host the request was sent to. That
// also holds behind Caddy, which forwards the original Host header.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && u.Host == r.Host
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// remoteIP is the address of the peer that opened the TCP connection.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// fromLocalProxy reports whether the request came through a proxy on this
// machine (Caddy, D-07). Only then may we believe the X-Forwarded-* headers:
// from anyone else, they are just text the client made up.
func fromLocalProxy(r *http.Request) bool {
	ip := net.ParseIP(remoteIP(r))
	return ip != nil && ip.IsLoopback()
}

// clientIP is the address of the real client. Behind Caddy every request
// arrives from 127.0.0.1, so the client's address is in X-Forwarded-For. We
// take the LAST entry: that is the one our own proxy appended. Earlier entries
// could have been written by the client.
func clientIP(r *http.Request) string {
	if fromLocalProxy(r) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return remoteIP(r)
}

// isHTTPS tells whether the browser talked to us over HTTPS. The server itself
// speaks plain HTTP (Caddy does the TLS), so we ask Caddy via X-Forwarded-Proto.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (fromLocalProxy(r) && r.Header.Get("X-Forwarded-Proto") == "https")
}

// serverURL is this server's address as the browser used it, for example
// "https://headnode.example.com". Behind Caddy the Host header is the public
// name and X-Forwarded-Proto says whether it was HTTPS.
func serverURL(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handleEnroll is the one agent endpoint with no auth header: the enroll
// token in the body is the credential (architecture.md §5).
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req protocol.EnrollRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Token == "" || req.Hostname == "" || len(req.Hostname) > 255 || len(req.OS) > 32 || len(req.Arch) > 32 {
		writeError(w, http.StatusBadRequest, "token and hostname are required; fields too long")
		return
	}

	resp, err := EnrollAgent(s.db, req)
	if errors.Is(err, ErrInvalidToken) {
		slog.Warn("enroll rejected", "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid or expired enroll token")
		return
	}
	if err != nil {
		// Details go to the log; the caller only learns that something broke.
		slog.Error("enroll agent", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("agent enrolled", "agent_id", resp.AgentID, "hostname", req.Hostname)
	s.audit(0, resp.AgentID, "agent_enroll", "hostname="+req.Hostname+" ip="+clientIP(r))
	// The response holds a secret, so no proxy or browser may cache it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
