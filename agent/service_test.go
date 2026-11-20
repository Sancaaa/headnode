package agent

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
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
	oldDir, oldRoot, oldExe, oldCtl, oldLookup, oldChown := unitDir, isRoot, executable, systemctl, lookupUser, chownFile
	t.Cleanup(func() {
		unitDir, isRoot, executable, systemctl, lookupUser, chownFile = oldDir, oldRoot, oldExe, oldCtl, oldLookup, oldChown
	})

	unitDir = t.TempDir()
	isRoot = func() bool { return root }
	executable = func() (string, error) { return "/usr/local/bin/headnode-agent", nil }
	// One account exists, alice (uid 1000). Nothing is really chowned.
	lookupUser = func(name string) (*user.User, error) {
		if name == "alice" {
			return &user.User{Username: "alice", Uid: "1000", Gid: "1001"}, nil
		}
		return nil, user.UnknownUserError(name)
	}
	chownFile = func(string, int, int) error { return nil }
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
	unit, err := unitFile("/usr/local/bin/headnode-agent", "/etc/headnode/agent.json", "")
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
	// No user asked for: systemd runs it as root, and the unit says nothing about it.
	if strings.Contains(unit, "User=") {
		t.Errorf("a unit for root has a User line:\n%s", unit)
	}
}

// The agent runs as the person who installed it: a terminal is that user's shell.
func TestUnitFileRunsTheAgentAsTheGivenUser(t *testing.T) {
	unit, err := unitFile("/usr/local/bin/headnode-agent", "/etc/headnode/agent.json", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=alice\n", "WorkingDirectory=~\n"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit lacks %q:\n%s", want, unit)
		}
	}
	// The user line is in [Service], before ExecStart.
	if strings.Index(unit, "User=alice") > strings.Index(unit, "ExecStart=") || strings.Index(unit, "[Service]") > strings.Index(unit, "User=alice") {
		t.Errorf("User= is not inside [Service] before ExecStart:\n%s", unit)
	}
	// "root" asked for by name is written down as User=root: the same account as
	// no line at all, but a choice. The install script reads the line to keep it on
	// an update, and treats a unit with no line (an older install) as never chosen.
	rootUnit, err := unitFile("/usr/local/bin/headnode-agent", "/etc/headnode/agent.json", "root")
	if err != nil || !strings.Contains(rootUnit, "User=root\n") {
		t.Errorf("a unit for root: err %v, want User=root in it:\n%s", err, rootUnit)
	}
}

// The name goes into a file systemd reads line by line: anything but a plain
// account name could add a setting of its own.
func TestUnitFileRefusesAUserNameThatIsNotAPlainAccountName(t *testing.T) {
	for _, bad := range []string{"alice\nExecStartPre=/bin/evil", "al ice", "Alice", "1abc", "a;b", "a$b", "a%b", "a/b", strings.Repeat("a", 33), "-x"} {
		if _, err := unitFile("/usr/local/bin/headnode-agent", "/etc/headnode/agent.json", bad); err == nil {
			t.Errorf("user %q was accepted", bad)
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
		if _, err := unitFile(bad, "/etc/headnode/agent.json", ""); err == nil {
			t.Errorf("exe %q was accepted", bad)
		}
		if _, err := unitFile("/usr/local/bin/headnode-agent", bad, ""); err == nil {
			t.Errorf("config %q was accepted", bad)
		}
	}
}

func TestInstallServiceWritesTheUnitAndStartsIt(t *testing.T) {
	calls := fakeSystem(t, true)
	config := enrolledConfig(t)

	if err := installSystemdService(config, ""); err != nil {
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
	if err := installSystemdService(config, ""); err != nil {
		t.Errorf("a second install failed: %v", err)
	}
}

func TestInstallServiceAsAnotherUserGivesThemTheCredentials(t *testing.T) {
	fakeSystem(t, true)
	var chowned []string
	chownFile = func(path string, uid, gid int) error {
		chowned = append(chowned, path+" "+strconv.Itoa(uid)+":"+strconv.Itoa(gid))
		return nil
	}
	config := enrolledConfig(t)

	if err := installSystemdService(config, "alice"); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(unitPath())
	if !strings.Contains(string(unit), "User=alice\n") {
		t.Errorf("the unit does not run as alice:\n%s", unit)
	}
	// The service runs as alice, so alice (and only alice) must own the secret.
	if len(chowned) != 1 || chowned[0] != config+" 1000:1001" {
		t.Errorf("chown calls = %v, want the credentials given to 1000:1001", chowned)
	}

	// An account that does not exist: nothing is installed.
	fakeSystem(t, true)
	err := installSystemdService(enrolledConfig(t), "nobody-here")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("unknown user: error = %v, want it to say the user does not exist", err)
	}
	if _, statErr := os.Stat(unitPath()); statErr == nil {
		t.Error("a unit was written for a user that does not exist")
	}
	// And a name that is not a name at all is refused before any lookup.
	if err := installSystemdService(enrolledConfig(t), "a b"); err == nil {
		t.Error("a user name with a space was accepted")
	}
}

func TestInstallServiceNeedsRootAndAnEnrolledHost(t *testing.T) {
	calls := fakeSystem(t, false)
	if err := installSystemdService(enrolledConfig(t), ""); !errors.Is(err, errNotRoot) {
		t.Errorf("as a normal user: error = %v, want %v", err, errNotRoot)
	}
	if len(*calls) != 0 {
		t.Errorf("systemctl was called without root: %v", *calls)
	}

	calls = fakeSystem(t, true)
	err := installSystemdService(filepath.Join(t.TempDir(), "nothing.json"), "")
	if err == nil || !strings.Contains(err.Error(), "enroll this host first") {
		t.Errorf("without credentials: error = %v, want it to say to enroll first", err)
	}
	if _, statErr := os.Stat(unitPath()); statErr == nil || len(*calls) != 0 {
		t.Error("a service was installed for a host that is not enrolled")
	}
}

func TestUninstallServiceRemovesTheUnit(t *testing.T) {
	calls := fakeSystem(t, true)
	if err := installSystemdService(enrolledConfig(t), ""); err != nil {
		t.Fatal(err)
	}
	*calls = nil

	if err := uninstallSystemdService(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unitPath()); err == nil {
		t.Error("the unit file is still there")
	}
	if got := strings.Join(*calls, "|"); got != "disable --now headnode-agent|daemon-reload" {
		t.Errorf("systemctl calls = %q", got)
	}

	// Uninstalling what is not installed is not an error.
	if err := uninstallSystemdService(); err != nil {
		t.Errorf("uninstalling twice: %v", err)
	}
	fakeSystem(t, false)
	if err := uninstallSystemdService(); !errors.Is(err, errNotRoot) {
		t.Errorf("as a normal user: error = %v, want %v", err, errNotRoot)
	}
}
