package agent

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestVNCProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &vncProbe{addr: ln.Addr().String()}
	if !p.listening() {
		t.Fatal("a listening port should be reported as VNC available")
	}

	// The answer is remembered: closing the listener changes nothing until the
	// probe interval has passed.
	ln.Close()
	if !p.listening() {
		t.Error("the answer should be cached between probes")
	}

	// After the interval it looks again and sees that nothing listens any more.
	p.at = time.Now().Add(-2 * vncProbeEvery)
	if p.listening() {
		t.Error("a closed port should not be reported as VNC available")
	}
}

func TestVNCProbeUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wayvnc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix domain sockets unavailable here: %v", err)
	}
	defer ln.Close()

	// Closed TCP port ensures we only succeed because the Unix socket is listening.
	deadTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := deadTCP.Addr().String()
	deadTCP.Close()

	p := &vncProbe{addr: deadAddr, socket: sock}
	if !p.listening() {
		t.Fatal("a listening wayvnc Unix socket should be reported as VNC available")
	}
	ln.Close()
	p.at = time.Now().Add(-2 * vncProbeEvery)
	if p.listening() {
		t.Error("a closed Unix socket should not be reported as VNC available")
	}
}
