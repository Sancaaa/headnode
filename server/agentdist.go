package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

// agentBinaries are the only names GET /download/{name} will serve. A fixed list,
// not "whatever file is in the folder": the name comes from the URL, and an
// allow-list means it can never be used to reach another file.
//
// Linux only for now (D-12). A new platform is one more line here and one more
// build in scripts/build-agents.
var agentBinaries = map[string]bool{
	"headnode-agent-linux-amd64": true,
	"headnode-agent-linux-arm64": true,
}

// UseAgentDir says where the built agent programs are (the output of
// scripts/build-agents). It is not created: the server only reads it. Without
// it, /download answers 404.
func (s *Server) UseAgentDir(dir string) {
	s.agentDir = dir
}

// handleAgentBinary is GET /download/{name} (protocol.md §1): the agent program
// for one platform. It needs no login, on purpose: a new host has nothing to
// log in with yet, and the install command has to be able to fetch it. That is
// fine because the program holds no secret. What makes a host belong to this
// server is the enroll token, not the binary.
func (s *Server) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !agentBinaries[name] {
		writeError(w, http.StatusNotFound, "unknown download")
		return
	}
	if s.agentDir == "" {
		writeError(w, http.StatusNotFound, "agent downloads are not set up on this server")
		return
	}

	file, err := os.Open(filepath.Join(s.agentDir, name))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, name+" has not been built: run scripts/build-agents on the server's machine and put the result in the agents folder")
		return
	}
	if err != nil {
		internalError(w, "open agent binary", err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "unknown download")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// A rebuilt program must reach the next install, not a copy a proxy kept.
	w.Header().Set("Cache-Control", "no-cache")
	// ServeContent adds Content-Length, Last-Modified and Range support.
	http.ServeContent(w, r, name, info.ModTime(), file)
}
