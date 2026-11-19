package agent

import (
	"strings"
	"testing"
)

func TestPickShell(t *testing.T) {
	existing := map[string]bool{"/bin/bash": true, "/bin/sh": true, "/usr/bin/zsh": true}
	exists := func(p string) bool { return existing[p] }

	cases := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"$SHELL wins when it exists", []string{"/usr/bin/zsh", "/bin/bash", "/bin/sh"}, "/usr/bin/zsh"},
		{"falls back when $SHELL is not there", []string{"/usr/bin/fish", "/bin/bash", "/bin/sh"}, "/bin/bash"},
		{"an empty $SHELL is skipped", []string{"", "/bin/bash", "/bin/sh"}, "/bin/bash"},
		{"a relative $SHELL is never used, even if it 'exists'", []string{"bash", "/bin/sh"}, "/bin/sh"},
		{"nothing found", []string{"", "/nope"}, ""},
	}
	for _, c := range cases {
		if got := pickShell(c.candidates, func(p string) bool { return exists(p) || p == "bash" }); got != c.want {
			t.Errorf("%s: pickShell = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWSURL(t *testing.T) {
	cases := []struct{ server, path, want string }{
		{"http://127.0.0.1:8080", "/agent/connect", "ws://127.0.0.1:8080/agent/connect"},
		{"https://headnode.example.com", "/agent/sessions/s_1", "wss://headnode.example.com/agent/sessions/s_1"},
	}
	for _, c := range cases {
		if got := wsURL(c.server, c.path); got != c.want {
			t.Errorf("wsURL(%q, %q) = %q, want %q", c.server, c.path, got, c.want)
		}
	}
}

func TestCloseReason(t *testing.T) {
	if got := closeReason("short"); got != "short" {
		t.Errorf("closeReason(short) = %q", got)
	}
	// 200 two-byte characters: cut to 120 bytes at a character boundary.
	long := strings.Repeat("é", 200)
	got := closeReason(long)
	if len(got) > 120 || len(got)%2 != 0 || strings.ToValidUTF8(got, "") != got {
		t.Errorf("closeReason cut to %d bytes, valid UTF-8 = %v", len(got), strings.ToValidUTF8(got, "") == got)
	}
}
