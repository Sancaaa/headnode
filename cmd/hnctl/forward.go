package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// hostInfo is the part of a host that hnctl needs. The API returns more.
type hostInfo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Online    bool    `json:"online"`
	GroupName *string `json:"group_name"`
	WANAddr   string  `json:"wan_addr"`
	CanShell  bool    `json:"can_shell"`
	Info      *struct {
		LANIPs []string `json:"lan_ips"`
	} `json:"info"`
}

// logf writes one timestamped line to standard error. Standard output is kept
// free, so a program can read what hnctl prints (like the host list).
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// forwardSpec is what `hnctl forward 2222:web-01:127.0.0.1:22` means.
type forwardSpec struct {
	localPort int
	host      string // a host's name or id
	target    string // host:port on that host
}

// parseForward reads PORT:HOST:TARGET, where TARGET is itself host:port. HOST
// is split off at the first two colons, so a host name must not contain one
// (use the host's id instead).
func parseForward(spec string) (forwardSpec, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) != 3 {
		return forwardSpec{}, errors.New("expected PORT:HOST:TARGET, for example 2222:web-01:127.0.0.1:22")
	}
	port, err := strconv.Atoi(parts[0])
	if err != nil || port < 1 || port > 65535 {
		return forwardSpec{}, errors.New("the local port must be a number from 1 to 65535")
	}
	if parts[1] == "" {
		return forwardSpec{}, errors.New("the host is missing: use its name or id (see `hnctl hosts`)")
	}
	// The same check the server and the agent make, so a mistake is reported
	// here at once instead of on the first connection.
	target, err := protocol.ParseTunnelTarget(parts[2])
	if err != nil {
		return forwardSpec{}, err
	}
	return forwardSpec{localPort: port, host: parts[1], target: target}, nil
}

// resolveHost finds a host by id, or by name (any capitalization). A name that
// two hosts share is refused, so a tunnel never goes to the wrong machine.
func resolveHost(hosts []hostInfo, ref string) (hostInfo, error) {
	for _, h := range hosts {
		if h.ID == ref {
			return h, nil
		}
	}
	var matches []hostInfo
	for _, h := range hosts {
		if strings.EqualFold(h.Name, ref) {
			matches = append(matches, h)
		}
	}
	switch len(matches) {
	case 0:
		return hostInfo{}, fmt.Errorf("no host named %q: see `hnctl hosts`", ref)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, h := range matches {
		ids[i] = h.ID
	}
	return hostInfo{}, fmt.Errorf("%d hosts are named %q, use an id instead: %s", len(matches), ref, strings.Join(ids, ", "))
}

// serveForward accepts connections on ln and gives each one its own tunnel,
// until ln is closed. One TCP connection is one tunnel session (D-02), so an
// `ssh` and an `scp` at the same time are two sessions.
func serveForward(ctx context.Context, ln net.Listener, c *client, host hostInfo, target string) {
	var active sync.WaitGroup
	for {
		local, err := ln.Accept()
		if err != nil {
			break // the listener was closed: shutting down
		}
		active.Add(1)
		go func() {
			defer active.Done()
			c.tunnel(ctx, local, host, target)
		}()
	}
	active.Wait()
}

// tunnel carries one local connection through the server to the host.
func (c *client) tunnel(ctx context.Context, local net.Conn, host hostInfo, target string) {
	defer local.Close()
	from := local.RemoteAddr().String()

	// Ask for a tunnel session. The server checks the role, the target, and that
	// the host is online, and asks the agent to dial back.
	var session struct {
		ID string `json:"id"`
	}
	err := c.do("POST", "/api/hosts/"+url.PathEscape(host.ID)+"/sessions",
		map[string]any{"kind": "tunnel", "params": map[string]any{"target": target}}, &session)
	if err != nil {
		logf("%s: cannot open a tunnel: %v", from, err)
		return
	}

	// Attach to it. The token goes in the Authorization header, as for the API.
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, _, err := websocket.Dial(dialCtx, wsURL(c.server, "/ui/sessions/"+url.PathEscape(session.ID)),
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}}})
	cancel()
	if err != nil {
		logf("%s: cannot attach to the tunnel: %v", from, err)
		return
	}
	logf("%s: connected to %s on %s", from, host.Name, target)

	// NetConn makes the WebSocket look like a TCP connection, so the two ends
	// are plain net.Conn and joining them is a copy loop.
	remote := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	if err := pipe(local, remote); err != nil {
		// Typically the host said why it could not connect: "connect: connection refused".
		logf("%s: closed: %v", from, err)
		return
	}
	logf("%s: closed", from)
}

// pipe copies bytes both ways between a and b until one side ends, then closes
// both. It returns the error that ended it, or nil for an ordinary hang-up.
func pipe(a, b net.Conn) error {
	results := make(chan error, 2)
	go func() { _, err := io.Copy(a, b); results <- err }()
	go func() { _, err := io.Copy(b, a); results <- err }()
	first := <-results
	a.Close()
	b.Close()
	<-results // closing both ends is what lets the other copy return
	return first
}
