//go:build windows

package agent

import (
	"strings"
	"testing"
)

// The command the service manager runs must survive a path with spaces
// ("C:\Program Files\...") and must not let a path add arguments of its own.
func TestServiceCommandLineQuotesPaths(t *testing.T) {
	got := serviceCommandLine(`C:\Program Files\headnode\headnode-agent.exe`, `C:\ProgramData\headnode\agent.json`)
	want := `"C:\Program Files\headnode\headnode-agent.exe" run -config C:\ProgramData\headnode\agent.json`
	if got != want {
		t.Errorf("command line = %s, want %s", got, want)
	}

	// A path that tries to end its own quote and add a flag stays one argument.
	hostile := serviceCommandLine(`C:\x\agent.exe`, `C:\a" -config "C:\evil.json`)
	if !strings.Contains(hostile, `\"`) {
		t.Errorf("a quote in a path was not escaped: %s", hostile)
	}
}

func TestDefaultConfigPathIsUnderProgramData(t *testing.T) {
	t.Setenv("ProgramData", `D:\Data`)
	if got := DefaultConfigPath(); got != `D:\Data\headnode\agent.json` {
		t.Errorf("DefaultConfigPath = %s", got)
	}
}

func TestNotRunningAsServiceFromAConsole(t *testing.T) {
	if RunningAsService() {
		t.Error("the tests run in a console, not as a service")
	}
}
