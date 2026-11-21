//go:build windows

package agent

import (
	"time"

	"golang.org/x/sys/windows/registry"
)

// The one line that tells the tray icon whether the agent is connected (tray_logic.go)
// lives in the registry, in HKLM\SOFTWARE\headnode, value Status (D-63).
//
// Not in a file under ProgramData, as it was first: there, a user who is not an
// administrator may create folders, so one could make headnode-status before the
// agent did, turn it into a link, and have the agent (as LocalSystem) overwrite any
// file on the machine with this line, a driver included. Under HKLM\SOFTWARE only
// administrators and the system may create keys, and every user may read them, which
// is exactly what this line needs: written by the agent, read by the icon.
const statusKey = `SOFTWARE\headnode`

// writeStatus records whether the agent is connected. Failing to write it only costs
// the icon its colour, so errors are not worth stopping the agent for.
func writeStatus(connected bool) {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, statusKey, registry.SET_VALUE)
	if err != nil {
		return // not the service (an agent started by hand, without administrator rights)
	}
	defer key.Close()
	key.SetStringValue("Status", statusLine(connected, time.Now()))
}

// readStatus returns what writeStatus last wrote, or "" if nothing was.
func readStatus() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, statusKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	text, _, err := key.GetStringValue("Status")
	if err != nil {
		return ""
	}
	return text
}

// removeStatus deletes the key, for `service uninstall`. Nothing else is kept in it.
func removeStatus() {
	registry.DeleteKey(registry.LOCAL_MACHINE, statusKey)
}
