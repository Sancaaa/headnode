package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSystem points the service code at a temporary folder and records the
// systemctl calls instead of running them.
func fakeSystem(t *testing.T, root bool) (calls *[]string) {
	t.Helper()
	// The unit holds Linux paths (a backslash or a drive letter is refused), so the
	// tests that go through InstallService cannot use the Windows laptop's folders.
	if runtime.GOOS == "windows" {
		t.Skip("the service is Linux only (D-12)")
	}
	oldDir, oldRoot, oldExe, oldCtl := unitDir, isRoot, executable, systemctl
	t.Cleanup(func() { unitDir, isRoot, executable, systemctl = oldDir, oldRoot, oldExe, oldCtl })

	unitDir = t.TempDir()
	isRoot = func() bool { return root }
	executable = func() (string, error) { return "/usr/local/bin/headnode-agent", nil }
	var recorded []string
	systemctl = func(args ...string) error {
		recorded = append(recorded, strings.Join(args, " "))
		return nil
	}
	return &recorded
}

func enrolledConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"server":"https://x","agent_id":"a_1","agent_secret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnitFile(t *testing.T) {
	unit, err := unitFile("/usr/local/bin/headnode-agent", "/etc/headnode/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=/usr/local/bin/headnode-agent run -config /etc/headnode/agent.json",
		"Restart=always",
		"WantedBy=multi-user.target",
		"After=network-online.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit lacks %q:\n%s", want, unit)
		}
	}
}

func TestUnitFileRefusesPathsSystemdWouldMisreadOrExpand(t *testing.T) {
	for _, bad := range []string{
		"/opt/my agent/headnode-agent", // a space splits the command
		`/opt/"quoted"/agent`,
		"/opt/100%/agent", // % is a specifier
		"/opt/$HOME/agent",
		`/opt/back\slash/agent`,
		"relative/agent",
		"/opt/new\nline/agent", // would start a new line in the unit
	} {
		if _, err := unitFile(bad, "/etc/headnode/agent.json"); err == nil {
			t.Errorf("exe %q was accepted", bad)
		}
		if _, err := unitFile("/usr/local/bin/headnode-agent", bad); err == nil {
			t.Errorf("config %q was accepted", bad)
		}
	}
}

func TestInstallServiceWritesTheUnitAndStartsIt(t *testing.T) {
	calls := fakeSystem(t, true)
	config := enrolledConfig(t)

	if err := InstallService(config); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(unitPath())
	if err != nil {
		t.Fatalf("the unit was not written: %v", err)
	}
	if !strings.Contains(string(unit), "ExecStart=/usr/local/bin/headnode-agent run -config "+config) {
		t.Errorf("the unit does not run with the credentials file %s:\n%s", config, unit)
	}
	// In this order: systemd must reread its units before it can enable one.
	// restart (not start) so a replaced program is the one that runs.
	want := "daemon-reload|enable headnode-agent|restart headnode-agent"
	if got := strings.Join(*calls, "|"); got != want {
		t.Errorf("systemctl calls = %q, want %q", got, want)
	}

	// Running it again is fine, and does not break anything.
	if err := InstallService(config); err != nil {
		t.Errorf("a second install failed: %v", err)
	}
}

func TestInstallServiceNeedsRootAndAnEnrolledHost(t *testing.T) {
	calls := fakeSystem(t, false)
	if err := InstallService(enrolledConfig(t)); !errors.Is(err, errNotRoot) {
		t.Errorf("as a normal user: error = %v, want %v", err, errNotRoot)
	}
	if len(*calls) != 0 {
		t.Errorf("systemctl was called without root: %v", *calls)
	}

	calls = fakeSystem(t, true)
	err := InstallService(filepath.Join(t.TempDir(), "nothing.json"))
	if err == nil || !strings.Contains(err.Error(), "enroll this host first") {
		t.Errorf("without credentials: error = %v, want it to say to enroll first", err)
	}
	if _, statErr := os.Stat(unitPath()); statErr == nil || len(*calls) != 0 {
		t.Error("a service was installed for a host that is not enrolled")
	}
}

func TestUninstallServiceRemovesTheUnit(t *testing.T) {
	calls := fakeSystem(t, true)
	if err := InstallService(enrolledConfig(t)); err != nil {
		t.Fatal(err)
	}
	*calls = nil

	if err := UninstallService(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unitPath()); err == nil {
		t.Error("the unit file is still there")
	}
	if got := strings.Join(*calls, "|"); got != "disable --now headnode-agent|daemon-reload" {
		t.Errorf("systemctl calls = %q", got)
	}

	// Uninstalling what is not installed is not an error.
	if err := UninstallService(); err != nil {
		t.Errorf("uninstalling twice: %v", err)
	}
	fakeSystem(t, false)
	if err := UninstallService(); !errors.Is(err, errNotRoot) {
		t.Errorf("as a normal user: error = %v, want %v", err, errNotRoot)
	}
}
