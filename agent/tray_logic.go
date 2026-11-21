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

// trayView turns what is known about the agent service into the line the menu shows
// and whether the icon is blue (connected) or grey. There is no "off" here (D-60):
// headnode that was exited has no icon. "Stopped" is only what the icon says if the
// service stops while the icon is up (somebody stopped it by hand, or it failed).
func trayView(running, connected bool) (status string, blue bool) {
	switch {
	case running && connected:
		return "Connected to the headnode server", true
	case running:
		return "Running, not connected to the server", false
	default:
		return "Stopped", false
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

// windowsDefaultServiceDACL is who may do what with a Windows service when nothing
// else is said (the system and administrators: everything; every logged-in user: look,
// and send control codes). SDDL letters: CC query config, DC change config, LC query
// status, SW list dependents, RP start, WP stop, DT pause, LO interrogate, CR user
// control, SD delete, RC read permissions, WD change permissions, WO take ownership.
const windowsDefaultServiceDACL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

// agentServiceDACL is the Windows default without CR for everyone at the console: a
// control code to the agent turns headnode off or the screen on (D-58), so only the
// account that `service allow` names, and administrators, may send one.
const agentServiceDACL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLORC;;;IU)(A;;CCLCSWLORC;;;SU)"

// trayUserRights is what the icon's user may do with the agent's service: look at it,
// start it, and send it the icon's control codes. Never DC (change config): with it,
// a user can point the service at any program, which then runs as LocalSystem (D-58).
// Never WP, SD, WD or WO either: turning off goes through a control code, and the
// user may not delete the service or change who may use it.
const trayUserRights = "CCLCRPLOCRRC"

var sidRe = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+)+$`)

// validSID says whether text looks like a security identifier ("S-1-5-21-..."). It
// is checked before the text goes into a permission string.
func validSID(text string) bool { return sidRe.MatchString(text) }

// serviceSDDL is the agent's base permissions plus one more rule, for this one user
// (trayUserRights). It is how a person at the machine can switch headnode without
// administrator rights.
func serviceSDDL(sid string) string {
	return agentServiceDACL + "(A;;" + trayUserRights + ";;;" + sid + ")"
}
