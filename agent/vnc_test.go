package agent

import (
	"net"
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
