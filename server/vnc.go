package server

import (
	"errors"
	"log/slog"
	"net/http"

	"headnode/protocol"
)

// handleSetVNC is POST /api/hosts/{id}/vnc with {"enabled": true|false}: start or
// stop the host's VNC server (D-51), so a machine short of resources does not
// run one all day. It answers 202 as soon as the agent has been told; the result
// shows up in the host's next heartbeat (`heartbeat.vnc`), which the Hosts page
// already polls.
//
// It needs the same permission as a shell or a tunnel: whoever may open a screen
// on a host may also decide whether there is one.
func (s *Server) handleSetVNC(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	hostID := r.PathValue("id")

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	exists, err := s.hostExists(hostID)
	if err != nil {
		internalError(w, "look up host", err)
		return
	}
	// A host the user may not see is "not found", like everywhere else (D-24).
	canView, err := s.can(u, hostID, actionView)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if !exists || !canView {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	allowed, err := s.can(u, hostID, actionShell)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "you do not have permission to start or stop VNC on this host")
		return
	}

	online, hb := s.agents.status(hostID)
	if !online {
		writeError(w, http.StatusConflict, "host offline")
		return
	}
	// The agent says whether it has a VNC server it can control. Asking one that
	// has none would only produce a line in its log, so say so here instead.
	if hb == nil || !hb.VNCControl {
		writeError(w, http.StatusConflict, "this host has no VNC server that headnode can start and stop (see docs/windows.md)")
		return
	}

	err = s.agents.send(hostID, protocol.TypeVNCSet, protocol.VNCSet{Enabled: req.Enabled})
	if errors.Is(err, errAgentOffline) {
		writeError(w, http.StatusConflict, "host offline")
		return
	}
	if err != nil {
		slog.Error("tell the agent to change VNC", "agent_id", hostID, "err", err)
		writeError(w, http.StatusBadGateway, "could not reach the host")
		return
	}

	action := "vnc_off"
	if req.Enabled {
		action = "vnc_on"
	}
	s.audit(u.ID, hostID, action, "")
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}
