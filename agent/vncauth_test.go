package agent

import (
	"encoding/hex"
	"testing"
)

// The well-known example of the VNC password format: "password" is stored as
// dbd83cfd727a1458 by vncpasswd, TightVNC and x11vnc alike. If this fails, the
// servers would not accept the passwords the agent gives them.
func TestVNCPasswordFormat(t *testing.T) {
	if got := hex.EncodeToString(vncEncryptPassword("password")); got != "dbd83cfd727a1458" {
		t.Errorf(`vncEncryptPassword("password") = %s, want dbd83cfd727a1458`, got)
	}
	for _, pw := range []string{"password", "a", "Zx9Qw2Lm"} {
		if got := vncDecryptPassword(vncEncryptPassword(pw)); got != pw {
			t.Errorf("round trip of %q gave %q", pw, got)
		}
	}
	if vncDecryptPassword([]byte{1, 2, 3}) != "" {
		t.Error("a stored value that is too short should give no password")
	}
}

func TestNewVNCPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		pw, err := newVNCPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != vncPasswordLen {
			t.Fatalf("password %q is %d characters, want %d", pw, len(pw), vncPasswordLen)
		}
		for _, c := range pw {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				t.Fatalf("password %q has %q, want letters and digits only", pw, c)
			}
		}
		seen[pw] = true
	}
	if len(seen) < 20 {
		t.Error("random passwords repeated: the generator is not random")
	}
}
