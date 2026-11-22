package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func haveOnly(programs ...string) func(string) bool {
	return func(p string) bool {
		for _, have := range programs {
			if p == have {
				return true
			}
		}
		return false
	}
}

func TestFindWaylandSocketSkipsTheLockFile(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "wayland-0.lock"))
	if _, _, ok := findWaylandSocket([]string{dir}); ok {
		t.Fatal("a lock file alone is not a Wayland socket")
	}
	touch(t, filepath.Join(dir, "wayland-0"))
	got, name, ok := findWaylandSocket([]string{t.TempDir(), dir})
	if !ok || got != dir || name != "wayland-0" {
		t.Errorf("findWaylandSocket = %q, %q, %v; want %q, wayland-0, true", got, name, ok, dir)
	}
}

func TestPlanUsesWaylandWhenItIsThere(t *testing.T) {
	run, x11, conf := t.TempDir(), t.TempDir(), t.TempDir()
	touch(t, filepath.Join(run, "wayland-0"))
	touch(t, filepath.Join(x11, "X0")) // XWayland: must not make it choose x11vnc

	env := vncEnv{runDirs: []string{run}, x11Dir: x11, have: haveOnly("wayvnc", "x11vnc")}
	plan, ok := env.plan(conf)
	if !ok || plan.name != "wayvnc" {
		t.Fatalf("plan = %+v, %v; want wayvnc", plan, ok)
	}
	// Private Unix socket inside the agent's 0700 confDir (not TCP 5900), our own
	// config (the machine's may have logins on), and the desktop it must attach to.
	wantSock := filepath.Join(conf, "wayvnc.sock")
	wantArgs := []string{"-C", filepath.Join(conf, "wayvnc.conf"), "-u", wantSock}
	if !reflect.DeepEqual(plan.args, wantArgs) {
		t.Errorf("args = %v, want %v", plan.args, wantArgs)
	}
	wantEnv := []string{"XDG_RUNTIME_DIR=" + run, "WAYLAND_DISPLAY=wayland-0"}
	if !reflect.DeepEqual(plan.env, wantEnv) {
		t.Errorf("env = %v, want %v", plan.env, wantEnv)
	}
	if plan.configText != "enable_auth=false\n" {
		t.Errorf("config = %q", plan.configText)
	}
	if plan.socketPath != wantSock {
		t.Errorf("socketPath = %q, want %q", plan.socketPath, wantSock)
	}

	// Without wayvnc there is nothing to start: never falls back to x11vnc here.
	env.have = haveOnly("x11vnc")
	if _, ok := env.plan(conf); ok {
		t.Error("a Wayland desktop without wayvnc must not be served by x11vnc")
	}
}

func TestPlanUsesX11WhenThereIsNoWayland(t *testing.T) {
	x11 := t.TempDir()
	touch(t, filepath.Join(x11, "X0"))
	env := vncEnv{runDirs: []string{t.TempDir()}, x11Dir: x11, have: haveOnly("x11vnc")}
	conf := t.TempDir()
	plan, ok := env.plan(conf)
	if !ok || plan.name != "x11vnc" {
		t.Fatalf("plan = %+v, %v; want x11vnc", plan, ok)
	}
	// A password from a file (D-64), never -nopw, and never the password itself on
	// the command line.
	passwords := filepath.Join(conf, "x11vnc.passwd")
	want := []string{"-display", ":0", "-localhost", "-rfbauth", passwords, "-rfbport", "5900", "-forever", "-shared", "-auth", "guess"}
	if !reflect.DeepEqual(plan.args, want) {
		t.Errorf("args = %v, want %v", plan.args, want)
	}
	if plan.passwordFile != passwords {
		t.Errorf("passwordFile = %q, want %q", plan.passwordFile, passwords)
	}
}

func TestPlanWithoutADesktopIsNotControllable(t *testing.T) {
	env := vncEnv{runDirs: []string{t.TempDir()}, x11Dir: t.TempDir(), have: haveOnly("wayvnc", "x11vnc")}
	if _, ok := env.plan(t.TempDir()); ok {
		t.Error("a machine with no desktop session has no screen to share")
	}
}

func TestCmdlineMatches(t *testing.T) {
	argv := []string{"wayvnc", "-C", "/home/pi/.config/headnode/wayvnc.conf", "-u", "/home/pi/.config/headnode/wayvnc.sock"}
	raw := []byte("wayvnc\x00-C\x00/home/pi/.config/headnode/wayvnc.conf\x00-u\x00/home/pi/.config/headnode/wayvnc.sock\x00")
	if !cmdlineMatches(raw, argv) {
		t.Error("the exact command line should match")
	}
	if cmdlineMatches([]byte("wayvnc\x00-C\x00/etc/wayvnc/config\x00"), argv) {
		t.Error("another wayvnc (the machine's own) must not be taken for ours")
	}
	if cmdlineMatches(nil, argv) {
		t.Error("an empty command line matches nothing")
	}
}
