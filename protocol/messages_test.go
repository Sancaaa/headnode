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
