package server

import (
	_ "embed"
	"net/http"
	"regexp"
	"strings"
)

// installScript is the installer a new host runs (install.sh in this folder). It
// is built into the program, like the web pages (D-06), so the script always
// matches the server that serves it. __SERVER__ in it stands for this server's
// own address, filled in for every request.
//
//go:embed install.sh
var installScript string

// A server address that is safe to put inside a shell script's quotes: a scheme,
// a host name or IP address (or a bracketed IPv6 one), and a port. Nothing else.
// The address comes from the Host header, which the client chooses, so it is
// checked instead of trusted: a value with a quote or a semicolon in it would
// otherwise become part of the script.
var serverAddressRe = regexp.MustCompile(`^https?://([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)

// handleInstallScript is GET /install.sh (protocol.md §1): the installer, with
// this server's address filled in, so that
//
//	curl -fsSL https://headnode.example.com/install.sh | sudo sh -s -- --token T
//
// needs nothing else. It is public: a new host has no login yet, and the script
// holds nothing secret (the token is given on the command line).
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	address := serverURL(r)
	if !serverAddressRe.MatchString(address) {
		writeError(w, http.StatusBadRequest, "this server's address cannot be used in an install script")
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // it is written for the address it was asked on
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write([]byte(strings.ReplaceAll(installScript, "__SERVER__", address)))
}
