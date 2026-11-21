package agent

import (
	"path/filepath"
	"sort"
	"strings"
)

// The pure part of starting a VNC server on a Linux desktop (D-54): finding the
// desktop session and working out the command to run. Nothing here starts a
// program, so it is tested everywhere; vnc_linux.go does the starting and stopping.

// vncPlan is a VNC server command, ready to run.
type vncPlan struct {
	name       string   // the program, looked up in PATH
	args       []string // its arguments, never joined into a shell string
	env        []string // extra environment, KEY=VALUE: where the desktop is
	config     string   // a file the program reads, or "" for none
	configText string   // what goes in that file
	// passwordFile is where the program reads its VNC password from, or "" when it
	// takes none (D-64). The agent writes a new one each time it starts the program.
	passwordFile string
}

// vncEnv is what the machine looks like, as the plan needs to know it. The real
// values are filled in by vnc_linux.go and tests use their own.
type vncEnv struct {
	runDirs []string          // runtime folders to look for a Wayland socket in
	x11Dir  string            // the folder of X11 sockets (/tmp/.X11-unix)
	have    func(string) bool // whether a program is installed
}

// findWaylandSocket returns the first Wayland socket (wayland-0, ...) in the given
// runtime folders. The ".lock" file that sits beside each socket is not one.
func findWaylandSocket(runDirs []string) (dir, name string, ok bool) {
	for _, d := range runDirs {
		matches, _ := filepath.Glob(filepath.Join(d, "wayland-*"))
		sort.Strings(matches)
		for _, m := range matches {
			if strings.HasSuffix(m, ".lock") {
				continue
			}
			return d, filepath.Base(m), true
		}
	}
	return "", "", false
}

// findX11Display returns the first X11 display (":0", ...) that has a socket in dir.
func findX11Display(dir string) (string, bool) {
	matches, _ := filepath.Glob(filepath.Join(dir, "X*"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return "", false
	}
	return ":" + strings.TrimPrefix(filepath.Base(matches[0]), "X"), true
}

// plan picks the VNC server for the desktop that is running, and says how to start
// it so that it listens on this machine only (127.0.0.1:5900), with no login of its
// own (D-54): the agent's tunnel is the only way in, and headnode's login and roles
// are the gate. x11vnc also gets a password the agent makes (D-64), so that other
// programs and users on this machine cannot use that way in. confDir is where its
// config and password files would go.
//
// A Wayland desktop (the Raspberry Pi 5 default) is served by wayvnc and nothing
// else. An X11 server may be there too, as XWayland does for old programs, but
// sharing that would show only those programs, so Wayland wins whenever it is present.
func (e vncEnv) plan(confDir string) (vncPlan, bool) {
	if dir, name, ok := findWaylandSocket(e.runDirs); ok {
		if !e.have("wayvnc") {
			return vncPlan{}, false
		}
		conf := filepath.Join(confDir, "wayvnc.conf")
		return vncPlan{
			name: "wayvnc",
			// -C: this config, not the machine's (Raspberry Pi OS turns logins on there).
			args:       []string{"-C", conf, "127.0.0.1", "5900"},
			env:        []string{"XDG_RUNTIME_DIR=" + dir, "WAYLAND_DISPLAY=" + name},
			config:     conf,
			configText: "address=127.0.0.1\nport=5900\nenable_auth=false\n",
		}, true
	}
	if display, ok := findX11Display(e.x11Dir); ok && e.have("x11vnc") {
		// -rfbauth: the password is in a file only the agent's account may read, never
		// on the command line, which every user of the machine can see.
		passwords := filepath.Join(confDir, "x11vnc.passwd")
		return vncPlan{
			name:         "x11vnc",
			args:         []string{"-display", display, "-localhost", "-rfbauth", passwords, "-rfbport", "5900", "-forever", "-shared", "-auth", "guess"},
			passwordFile: passwords,
		}, true
	}
	return vncPlan{}, false
}

// cmdlineMatches says whether the contents of /proc/<pid>/cmdline (the arguments,
// each ended by a zero byte) are exactly argv. That is how the agent recognises its
// own VNC server again after it was restarted, without keeping a process id.
func cmdlineMatches(raw []byte, argv []string) bool {
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(parts) != len(argv) {
		return false
	}
	for i := range parts {
		if parts[i] != argv[i] {
			return false
		}
	}
	return true
}
