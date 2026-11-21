package agent

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The parts of the Windows tray icon (M9, D-57) that need no Windows: what the icon
// says, how the agent tells it whether it is connected, and who may switch the
// service. They live here, without a build tag, so they are tested everywhere.

// ---------------------------------------------------------------- what the icon says

// trayView turns what is known about the agent service into the line the menu shows,
// whether the icon is green, and the label of the on/off item. "Off" means the
// service is stopped AND set not to start at boot (disabled): that is what Turn
// headnode off does, and nothing of headnode runs then (D-57).
func trayView(running, disabled, connected bool) (status string, green bool, toggle string) {
	switch {
	case running && connected:
		return "Connected to the headnode server", true, "Turn headnode off"
	case running:
		return "Running, not connected to the server", false, "Turn headnode off"
	case disabled:
		return "Off", false, "Turn headnode on"
	default:
		return "Stopped", false, "Turn headnode on"
	}
}

// --------------------------------------------------------- the connection status file

// statusStaleAfter is how old the agent's status may be before the icon stops
// believing "connected". The agent refreshes it with every heartbeat (20 seconds), so
// a status older than this means the agent is stuck or gone, whatever the file says.
const statusStaleAfter = 60 * time.Second

// statusLine is what the agent writes: "connected 1763700000". The icon cannot ask
// the service whether it reached the server, and the credentials file is private, so
// the agent leaves this one line, which holds nothing secret, where every user of the
// machine may read it (status_windows.go).
func statusLine(connected bool, now time.Time) string {
	word := "disconnected"
	if connected {
		word = "connected"
	}
	return word + " " + strconv.FormatInt(now.Unix(), 10) + "\n"
}

// parseStatus reads what statusLine wrote. Anything unreadable, or old, is "not connected".
func parseStatus(text string, now time.Time) bool {
	fields := strings.Fields(text)
	if len(fields) != 2 || fields[0] != "connected" {
		return false
	}
	at, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return false
	}
	return now.Sub(time.Unix(at, 0)) <= statusStaleAfter
}

// -------------------------------------------------------------- who may switch the service

// defaultServiceDACL is who may do what with a Windows service when nothing else is
// said (the system and administrators: everything; every logged-in user: look).
// SDDL letters: CC query config, DC change config, LC query status, SW list
// dependents, RP start, WP stop, DT pause, LO interrogate, CR user control, SD delete,
// RC read permissions, WD change permissions, WO take ownership.
const defaultServiceDACL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

var sidRe = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+)+$`)

// validSID says whether text looks like a security identifier ("S-1-5-21-..."). It
// is checked before the text goes into a permission string.
func validSID(text string) bool { return sidRe.MatchString(text) }

// serviceSDDL is the default permissions plus one more rule: this one user may query
// the service, start it, stop it and change how it starts, and nothing more. It is
// how a person at the machine can switch headnode without administrator rights.
func serviceSDDL(sid string) string {
	return defaultServiceDACL + "(A;;CCDCLCRPWPLORC;;;" + sid + ")"
}
