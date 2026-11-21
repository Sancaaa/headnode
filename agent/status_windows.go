//go:build windows

package agent

import (
	"context"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"headnode/protocol"
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

// startLatestWatcher keeps the newest version that the server offers in the registry,
// value Latest, for the tray icon (D-68): the icon decides from it whether its Update
// headnode… item can be pressed. The icon cannot ask the server itself, because the
// server's address is in the credentials file, which is private; the agent, which has
// it, asks. Once at start (again after a minute if the server did not answer, as at
// boot before the network is up), then every ten minutes.
func startLatestWatcher(ctx context.Context, cfg Config) {
	go func() {
		for {
			wait := time.Minute
			if text, err := fetchText(cfg.Server + "/download/VERSION"); err == nil {
				if version := strings.TrimSpace(text); isVersion(version) {
					writeLatest(version)
					wait = 10 * time.Minute
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

func isVersion(text string) bool {
	_, ok := protocol.ParseVersion(text)
	return ok
}

// writeLatest records the newest version the server offers. Failing to write it only
// costs the icon its Update item, so errors are not worth stopping for.
func writeLatest(version string) {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, statusKey, registry.SET_VALUE)
	if err != nil {
		return // not the service
	}
	defer key.Close()
	key.SetStringValue("Latest", version)
}

// readLatest returns what writeLatest last wrote, or "" if nothing was.
func readLatest() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, statusKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	text, _, err := key.GetStringValue("Latest")
	if err != nil {
		return ""
	}
	return text
}

// writeUpdateResult records how the last `headnode-agent update` went, in the same key
// (D-67): the update runs elevated and with no window, so the tray icon, which started
// it, could not otherwise tell the person that it failed. Only an administrator can
// write here (D-63), which is who runs the update.
func writeUpdateResult(err error) {
	key, _, kerr := registry.CreateKey(registry.LOCAL_MACHINE, statusKey, registry.SET_VALUE)
	if kerr != nil {
		return
	}
	defer key.Close()
	key.SetStringValue("Update", updateResultLine(err, time.Now()))
}

// readUpdateResult returns what writeUpdateResult last wrote, or "" if nothing was.
func readUpdateResult() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, statusKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	text, _, err := key.GetStringValue("Update")
	if err != nil {
		return ""
	}
	return text
}

// removeStatus deletes the key, for `service uninstall`. Nothing else is kept in it.
func removeStatus() {
	registry.DeleteKey(registry.LOCAL_MACHINE, statusKey)
}
