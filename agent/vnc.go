package agent

import (
	"log/slog"
	"net"
	"time"

	"headnode/protocol"
)

// runVNCSet starts or stops the host's VNC server (D-51). It is the agent's answer
// to `vnc_set`, and has no reply of its own: when it is done it nudges the
// heartbeat loop (kick), which sends a fresh heartbeat at once, and that is how
// the server and the dashboard learn the result. A failure is only logged on the
// host: the dashboard sees the state did not change.
func runVNCSet(set protocol.VNCSet, kick func()) {
	defer kick()
	if !canControlVNC() {
		slog.Warn("asked to change the VNC server, but this host has none that the agent can control")
		return
	}
	if err := setVNCService(set.Enabled); err != nil {
		slog.Error("change the VNC server", "enabled", set.Enabled, "err", err)
		return
	}
	slog.Info("changed the VNC server", "enabled", set.Enabled)

	// The service reports "running" a moment before its port accepts connections:
	// wait until the port agrees, so the heartbeat that follows tells the truth.
	for i := 0; i < 10; i++ {
		var probe vncProbe
		if probe.listening() == set.Enabled {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// vncAddress is where the dashboard's Screen page looks for a VNC server (D-49).
const vncAddress = "127.0.0.1:5900"

// vncProbeEvery is how often the agent really looks. A heartbeat goes out every
// 20 seconds, but opening a connection to a VNC server each time would fill its
// log with connections that say nothing: once a minute is often enough for a
// button that appears or disappears.
const vncProbeEvery = time.Minute

// vncProbe remembers the last answer, so the heartbeat loop can ask on every
// beat and only the first beat of each minute costs a connection.
type vncProbe struct {
	addr   string // empty means vncAddress; tests point it at their own listener
	at     time.Time
	listen bool
}

// listening reports whether something accepts connections on the VNC port.
//
// It connects and hangs up at once instead of, say, trying to bind the port and
// seeing it fail: that gives the same answer on Linux and Windows, and needs no
// rights. The dial is on the loopback address, so it never leaves the machine.
func (p *vncProbe) listening() bool {
	if !p.at.IsZero() && time.Since(p.at) < vncProbeEvery {
		return p.listen
	}
	addr := p.addr
	if addr == "" {
		addr = vncAddress
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	p.listen = err == nil
	if err == nil {
		conn.Close()
	}
	p.at = time.Now()
	return p.listen
}
