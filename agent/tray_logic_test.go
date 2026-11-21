package agent

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestTrayView(t *testing.T) {
	tests := []struct {
		running, notAtBoot, connected bool
		status                       string
		green                        bool
		toggle                       string
	}{
		{true, false, true, "Connected to the headnode server", true, "Turn headnode off"},
		{true, false, false, "Running, not connected to the server", false, "Turn headnode off"},
		{false, true, false, "Off", false, "Turn headnode on"},
		{false, false, false, "Stopped", false, "Turn headnode on"},
		// A status file that says "connected" is not believed for a service that is not running.
		{false, true, true, "Off", false, "Turn headnode on"},
	}
	for _, tc := range tests {
		status, green, toggle := trayView(tc.running, tc.notAtBoot, tc.connected)
		if status != tc.status || green != tc.green || toggle != tc.toggle {
			t.Errorf("trayView(%v,%v,%v) = %q,%v,%q; want %q,%v,%q",
				tc.running, tc.notAtBoot, tc.connected, status, green, toggle, tc.status, tc.green, tc.toggle)
		}
	}
}

func TestStatusFile(t *testing.T) {
	now := time.Unix(1_763_700_000, 0)
	if got := statusLine(true, now); got != "connected 1763700000\n" {
		t.Errorf("statusLine = %q", got)
	}
	if !parseStatus(statusLine(true, now), now.Add(20*time.Second)) {
		t.Error("a fresh 'connected' should count as connected")
	}
	if parseStatus(statusLine(true, now), now.Add(2*time.Minute)) {
		t.Error("an old 'connected' (a stuck or dead agent) must not count")
	}
	for _, text := range []string{"", "disconnected 1763700000", "connected", "connected soon", "connected 1763700000 extra"} {
		if parseStatus(text, now) {
			t.Errorf("parseStatus(%q) = true, want false", text)
		}
	}
}

func TestServiceSDDL(t *testing.T) {
	if !validSID("S-1-5-21-1004336348-1177238915-682003330-1001") {
		t.Error("a normal user SID was refused")
	}
	for _, bad := range []string{"", "S-1-5", "S-1-5-21-1)(A;;GA;;;WD", "S-1-5-21-1 (A;;GA;;;WD)", "x S-1-5-21-1"} {
		if validSID(bad) {
			t.Errorf("validSID(%q) = true: it could change the permission string", bad)
		}
	}
	got := serviceSDDL("S-1-5-21-1-2-3-1001")
	if !strings.HasPrefix(got, agentServiceDACL) || !strings.HasSuffix(got, "(A;;CCLCRPLOCRRC;;;S-1-5-21-1-2-3-1001)") {
		t.Errorf("serviceSDDL = %q", got)
	}
	// D-58: DC (change config) would let the user point the service at any program,
	// which then runs as LocalSystem. Delete, change permissions and take ownership are
	// out too, and stop: turning off goes through a control code.
	assertNoRights(t, "the user's rule", trayUserRights, "DC", "SD", "WD", "WO", "WP")
}

// TestAgentServiceDACL checks that nobody but the system and administrators may
// reconfigure the agent's service, and that everyone at the console may only look:
// a control code (CR) turns headnode off or the screen on, so it is not for everyone.
func TestAgentServiceDACL(t *testing.T) {
	for _, ace := range strings.Split(strings.TrimPrefix(agentServiceDACL, "D:("), ")(") {
		ace = strings.TrimSuffix(ace, ")")
		parts := strings.Split(ace, ";")
		rights, who := parts[2], parts[5]
		if who == "SY" || who == "BA" {
			continue
		}
		assertNoRights(t, "the rule for "+who, rights, "DC", "CR", "RP", "WP", "SD", "WD", "WO")
	}
}

// assertNoRights fails if an SDDL rights string holds any of the two-letter rights.
// The string is read two letters at a time, so "LOCR" holds LO and CR but not OC.
func assertNoRights(t *testing.T, what, rights string, forbidden ...string) {
	t.Helper()
	if len(rights)%2 != 0 {
		t.Fatalf("%s: %q is not a list of two-letter rights", what, rights)
	}
	for i := 0; i < len(rights); i += 2 {
		for _, f := range forbidden {
			if rights[i:i+2] == f {
				t.Errorf("%s has the right %s (%q)", what, f, rights)
			}
		}
	}
}

func TestTrayIconIsAValidIcon(t *testing.T) {
	ico := trayIcon(0x2e, 0x9e, 0x5b)
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 3 {
		t.Fatal("not an icon with three images")
	}
	for i, size := range []int{16, 32, 48} {
		entry := ico[6+16*i:]
		if int(entry[0]) != size || int(entry[1]) != size {
			t.Errorf("image %d: size %dx%d, want %d", i, entry[0], entry[1], size)
		}
		length := int(binary.LittleEndian.Uint32(entry[8:]))
		offset := int(binary.LittleEndian.Uint32(entry[12:]))
		if offset+length > len(ico) {
			t.Fatalf("image %d runs past the end of the file", i)
		}
		img := ico[offset : offset+length]
		// The bitmap is stored upside down: the middle of the mark is the middle row.
		center := img[40+((size/2)*size+size/2)*4:]
		if center[3] != 255 || center[2] != 0x2e || center[1] != 0x9e || center[0] != 0x5b {
			t.Errorf("image %d: the center pixel is %v, want the solid color", i, center[:4])
		}
		corner := img[40:] // the bottom-left corner
		if corner[3] != 0 {
			t.Errorf("image %d: a corner pixel has alpha %d, want transparent", i, corner[3])
		}
	}
}
