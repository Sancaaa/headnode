package protocol

import (
	"encoding/json"
	"testing"
)

func TestClampSize(t *testing.T) {
	// Missing and nonsense values get the classic 80x24, huge ones are capped.
	cases := []struct{ cols, rows, wantCols, wantRows int }{
		{120, 32, 120, 32},
		{0, 0, 80, 24},
		{-5, -1, 80, 24},
		{9999, 9999, 500, 500},
		{500, 1, 500, 1},
	}
	for _, c := range cases {
		cols, rows := ClampSize(c.cols, c.rows)
		if cols != c.wantCols || rows != c.wantRows {
			t.Errorf("ClampSize(%d, %d) = %d, %d, want %d, %d", c.cols, c.rows, cols, rows, c.wantCols, c.wantRows)
		}
	}
}

func TestParseTunnelTarget(t *testing.T) {
	// The whole 127.0.0.0/8 range is this machine, so 127.1.2.3 is fine.
	good := []string{"127.0.0.1:22", "localhost:5432", "127.1.2.3:80", "[::1]:8080", "127.0.0.1:65535"}
	for _, in := range good {
		if got, err := ParseTunnelTarget(in); err != nil || got != in {
			t.Errorf("ParseTunnelTarget(%q) = %q, %v, want it accepted unchanged", in, got, err)
		}
	}

	// Everything that is not "a port on this machine" is refused: malformed
	// targets, a machine on the LAN (10.x, the router), any name except
	// localhost (also one that only looks like it), "any address" 0.0.0.0, a
	// link-local neighbor, and the cloud metadata address.
	for _, in := range []string{
		"", "127.0.0.1", ":22", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:ssh", "127.0.0.1:-1",
		"10.0.0.5:22", "192.168.1.1:80", "example.com:443", "localhost.evil.com:22",
		"0.0.0.0:22", "[fe80::1]:22", "169.254.169.254:80",
	} {
		if got, err := ParseTunnelTarget(in); err == nil {
			t.Errorf("ParseTunnelTarget(%q) accepted it as %q", in, got)
		}
	}
}

// The open message must keep params as raw JSON, so each kind can define its
// own shape (docs/protocol.md §3).
func TestOpenMessageRoundTrip(t *testing.T) {
	params, err := json.Marshal(ShellParams{Cols: 120, Rows: 32})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := NewMessage(TypeOpen, Open{SessionID: "s_abc", Kind: KindShell, Params: params})
	if err != nil {
		t.Fatal(err)
	}

	var env Envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != TypeOpen {
		t.Fatalf("type = %q, want %q", env.Type, TypeOpen)
	}
	var open Open
	if err := json.Unmarshal(env.Data, &open); err != nil {
		t.Fatal(err)
	}
	var shell ShellParams
	if err := json.Unmarshal(open.Params, &shell); err != nil {
		t.Fatal(err)
	}
	if open.SessionID != "s_abc" || open.Kind != KindShell || shell.Cols != 120 || shell.Rows != 32 {
		t.Errorf("got %+v with %+v", open, shell)
	}
}
