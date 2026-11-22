package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"headnode/protocol"
)

// ------------------------------------------------------------ test helpers

// enableDocker lets a test talk to a fake engine whatever system it runs on.
func enableDocker(t *testing.T) {
	t.Helper()
	old := dockerSupported
	dockerSupported = true
	t.Cleanup(func() { dockerSupported = old })
}

// requestLog remembers every request a fake engine received, as "METHOD /path?query".
type requestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *requestLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// startFakeEngine serves handler on a unix socket in a temporary folder, the way
// the Docker Engine does, and returns the socket's path. The folder name is kept
// short: a unix socket path has a small limit.
func startFakeEngine(t *testing.T, handler http.HandlerFunc) (string, *requestLog) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets are not available here: %v", err)
	}
	log := &requestLog{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method + " " + r.URL.RequestURI())
		handler(w, r)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket, log
}

var versionPrefix = regexp.MustCompile(`^/v\d+\.\d+`)

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// frame is one piece of the multiplexed log stream: the stream number, three zero
// bytes, the size, and the bytes.
func frame(stream byte, text string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
	return append(header, text...)
}

// recorder collects the changes a session reports.
type recorder struct {
	mu      sync.Mutex
	changes []protocol.DockerChange
}

func (r *recorder) report(c protocol.DockerChange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, c)
}

func (r *recorder) all() []protocol.DockerChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.DockerChange(nil), r.changes...)
}

func ask(t *testing.T, s *dockerSession, request string) protocol.DockerResponse {
	t.Helper()
	return s.handle(context.Background(), []byte(request))
}

const dockerRefusal = "You cannot remove a running container 3f2a. Stop the container before attempting removal or force remove"

// A small engine: it answers the calls the tests make, refuses to remove "web"
// (which runs), and lists a Compose stack whose files are named in composeFile.
func engineWith(composeFile, goneFile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := versionPrefix.ReplaceAllString(r.URL.Path, "")
		switch {
		case path == "/_ping":
			_, _ = w.Write([]byte("OK"))
		case path == "/version":
			writeJSON(w, 200, `{"ApiVersion":"1.52","MinAPIVersion":"1.44"}`)
		case path == "/containers/json":
			writeJSON(w, 200, `[
			 {"Id":"3f2a000000000000","Names":["/web"],"Image":"ghcr.io/x/app:9c1e2d4","ImageID":"sha256:aaa","Created":1763800000,
			  "Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"PrivatePort":9000,"Type":"tcp"}],
			  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web",
			            "com.docker.compose.project.working_dir":`+jsonString(filepath.Dir(composeFile))+`,
			            "com.docker.compose.project.config_files":`+jsonString(composeFile)+`,
			            "org.opencontainers.image.revision":"9c1e2d4aabbcc"},
			  "State":"running","Status":"Up 3 hours",
			  "Mounts":[{"Type":"volume","Name":"shop_db"}],
			  "NetworkSettings":{"Networks":{"shop_default":{"IPAddress":"172.18.0.3"}}}},
			 {"Id":"5b6c000000000000","Names":["/shop-db-1"],"Image":"postgres:16","ImageID":"sha256:bbb","Created":1763800000,
			  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"db",
			            "com.docker.compose.project.working_dir":`+jsonString(filepath.Dir(composeFile))+`,
			            "com.docker.compose.project.config_files":`+jsonString(composeFile)+`},
			  "State":"exited","Status":"Exited (0) 1 hour ago",
			  "Mounts":[{"Type":"volume","Name":"shop_db"}],
			  "NetworkSettings":{"Networks":{"shop_default":{"IPAddress":""}}}},
			 {"Id":"7d8e000000000000","Names":["/old-1"],"Image":"busybox","ImageID":"sha256:ccc","Created":1763800000,
			  "Labels":{"com.docker.compose.project":"gone","com.docker.compose.service":"app",
			            "com.docker.compose.project.working_dir":`+jsonString(filepath.Dir(goneFile))+`,
			            "com.docker.compose.project.config_files":`+jsonString(goneFile)+`},
			  "State":"running","Status":"Up 1 day","NetworkSettings":{"Networks":{}}}
			]`)
		case r.Method == http.MethodDelete && path == "/containers/web":
			writeJSON(w, 409, `{"message":`+jsonString(dockerRefusal)+`}`)
		case r.Method == http.MethodDelete && path == "/containers/shop-db-1":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && path == "/containers/web/stop":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && path == "/containers/shop-db-1/start":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && path == "/containers/web/start":
			w.WriteHeader(304) // already running: not an error
		case path == "/containers/web/json":
			writeJSON(w, 200, `{"Id":"3f2a","Config":{"Tty":false,"Env":["DB_PASSWORD=hunter2","MODE=prod"]},"HostConfig":{"Memory":17179869184}}`)
		case path == "/containers/tty/json":
			writeJSON(w, 200, `{"Id":"tty","Config":{"Tty":true}}`)
		case path == "/containers/web/logs":
			w.Write(append(frame(1, "out line\n"), frame(2, "err line\n")...))
		case path == "/containers/tty/logs":
			_, _ = w.Write([]byte("raw line\n"))
		case path == "/images/json":
			writeJSON(w, 200, `[{"Id":"sha256:bbb","RepoTags":["postgres:16"],"Size":400000000,"Created":1763000000},
			                    {"Id":"sha256:aaa","RepoTags":["ghcr.io/x/app:9c1e2d4"],"Size":80000000,"Created":1763000000},
			                    {"Id":"sha256:ddd","RepoTags":["<none>:<none>"],"Size":999000000,"Created":1763000000}]`)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/images/"):
			writeJSON(w, 409, `{"message":"conflict: unable to delete aaa (cannot be forced) - image is being used by running container 3f2a"}`)
		case path == "/volumes":
			writeJSON(w, 200, `{"Volumes":[{"Name":"shop_db","Driver":"local","Mountpoint":"/var/lib/docker/volumes/shop_db/_data","Labels":{"com.docker.compose.project":"shop"}},
			                              {"Name":"`+strings.Repeat("ab", 32)+`","Driver":"local","Mountpoint":"/x"}]}`)
		case path == "/networks":
			writeJSON(w, 200, `[{"Id":"n1","Name":"shop_default","Driver":"bridge","Scope":"local"},{"Id":"n2","Name":"bridge","Driver":"bridge","Scope":"local"}]`)
		case r.Method == http.MethodPost && path == "/containers/prune":
			writeJSON(w, 200, `{"ContainersDeleted":["5b6c"],"SpaceReclaimed":1048576}`)
		case r.Method == http.MethodPost && path == "/images/prune":
			writeJSON(w, 200, `{"ImagesDeleted":[{"Untagged":"x:1"},{"Deleted":"sha256:ddd"}],"SpaceReclaimed":999000000}`)
		default:
			writeJSON(w, 404, `{"message":"page not found"}`)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newTestSession starts a fake engine and a session on it. The compose file of the
// stack "shop" exists; the one of "gone" does not.
func newTestSession(t *testing.T) (*dockerSession, *requestLog, *recorder, string) {
	t.Helper()
	enableDocker(t)
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, log := startFakeEngine(t, engineWith(composeFile, filepath.Join(dir, "deleted.yml")))
	rec := &recorder{}
	return newDockerSession(socket, rec.report), log, rec, composeFile
}

// ------------------------------------------------------- the API version

func TestChooseAPIVersion(t *testing.T) {
	tests := []struct{ engine, minimum, limit, want string }{
		{"1.52", "1.44", "1.47", "1.47"}, // a newer engine: the newest this code knows
		{"1.43", "1.12", "1.47", "1.43"}, // an older engine: its own version
		{"1.47", "1.44", "1.47", "1.47"},
		{"1.52", "1.50", "1.47", "1.50"}, // never below the engine's minimum, even above the cap
		{"1.9", "1.12", "1.47", "1.12"},  // numbers, not text: 1.9 is older than 1.47 (and the minimum wins)
		{"1.9", "", "1.47", "1.9"},
		{"", "1.44", "1.47", ""},    // the engine did not say
		{"junk", "1.44", "1.47", ""}, // or said nonsense
	}
	for _, tc := range tests {
		if got := chooseAPIVersion(tc.engine, tc.minimum, tc.limit); got != tc.want {
			t.Errorf("chooseAPIVersion(%q, %q, %q) = %q, want %q", tc.engine, tc.minimum, tc.limit, got, tc.want)
		}
	}
}

func TestTheVersionIsNegotiatedFromTheEnginesOwnAnswer(t *testing.T) {
	s, log, _, _ := newTestSession(t)
	if resp := ask(t, s, `{"id":1,"op":"containers"}`); !resp.OK {
		t.Fatalf("containers: %+v", resp)
	}
	lines := log.all()
	if len(lines) < 2 || lines[0] != "GET /version" {
		t.Fatalf("requests = %v, want the unversioned /version first", lines)
	}
	// The fake engine says 1.52; the cap is lower, so the cap is what is spoken.
	if want := "GET /v" + maxAPIVersion + "/containers/json?all=1"; lines[1] != want {
		t.Errorf("second request = %q, want %q", lines[1], want)
	}
	// Chosen once per session: a second request does not ask again.
	ask(t, s, `{"id":2,"op":"images"}`)
	versions := 0
	for _, line := range log.all() {
		if line == "GET /version" {
			versions++
		}
	}
	if versions != 1 {
		t.Errorf("/version was asked %d times, want once per session", versions)
	}
}

// ---------------------------------------------------------------- logs

func TestDemuxLogs(t *testing.T) {
	stream := bytes.Join([][]byte{frame(1, "out 1\n"), frame(2, "err 1\n"), frame(1, "out 2\n")}, nil)
	want := "out 1\nerr 1\nout 2\n"

	got, err := demuxLogs(bytes.NewReader(stream))
	if err != nil || string(got) != want {
		t.Errorf("demuxLogs = %q, %v, want %q", got, err, want)
	}
	// One byte per read: every header and every frame is split across reads.
	got, err = demuxLogs(iotest.OneByteReader(bytes.NewReader(stream)))
	if err != nil || string(got) != want {
		t.Errorf("one byte at a time: demuxLogs = %q, %v, want %q", got, err, want)
	}
	// A stream cut in the middle of a frame (the read limit) gives what arrived, without an error.
	got, err = demuxLogs(bytes.NewReader(stream[:len(stream)-3]))
	if err != nil || !strings.HasPrefix(want, string(got)) || len(got) < len("out 1\nerr 1\n") {
		t.Errorf("cut stream: demuxLogs = %q, %v", got, err)
	}
	if got, err := demuxLogs(bytes.NewReader(nil)); err != nil || len(got) != 0 {
		t.Errorf("empty stream: %q, %v", got, err)
	}
}

func TestLogsOfAContainerWithAndWithoutATTY(t *testing.T) {
	s, log, _, _ := newTestSession(t)

	resp := ask(t, s, `{"id":1,"op":"logs","container":"web","tail":3}`)
	if !resp.OK || resp.Logs != "out line\nerr line\n" || resp.Command != "docker logs --tail 3 web" {
		t.Errorf("multiplexed logs = %+v", resp)
	}
	found := false
	for _, line := range log.all() {
		if strings.Contains(line, "/containers/web/logs") && strings.Contains(line, "tail=3") &&
			strings.Contains(line, "stdout=1") && strings.Contains(line, "stderr=1") {
			found = true
		}
	}
	if !found {
		t.Errorf("no logs request with stdout, stderr and tail=3: %v", log.all())
	}

	resp = ask(t, s, `{"id":2,"op":"logs","container":"tty"}`) // with a TTY the stream is raw
	if !resp.OK || resp.Logs != "raw line\n" || resp.Command != "docker logs --tail 200 tty" {
		t.Errorf("raw logs = %+v", resp)
	}
}

// ------------------------------------------------------------- inspect

func TestInspectHidesEnvironmentValues(t *testing.T) {
	s, _, _, _ := newTestSession(t)
	resp := ask(t, s, `{"id":1,"op":"inspect","container":"web"}`)
	if !resp.OK {
		t.Fatalf("inspect: %+v", resp)
	}
	text := string(resp.Inspect)
	if strings.Contains(text, "hunter2") || strings.Contains(text, "prod") {
		t.Errorf("an environment value is still there: %s", text)
	}
	for _, want := range []string{"DB_PASSWORD=(hidden)", "MODE=(hidden)", "17179869184"} { // names stay, numbers are untouched
		if !strings.Contains(text, want) {
			t.Errorf("inspect lacks %q: %s", want, text)
		}
	}
	if resp.Command != "docker inspect web" {
		t.Errorf("command = %q", resp.Command)
	}
}

// ---------------------------------------------------------------- lists

func TestListsContainersImagesVolumesAndNetworks(t *testing.T) {
	s, _, _, _ := newTestSession(t)

	resp := ask(t, s, `{"id":1,"op":"containers"}`)
	if !resp.OK || len(resp.Containers) != 3 || resp.Command != "docker ps -a" {
		t.Fatalf("containers = %+v", resp)
	}
	var web protocol.DockerContainer
	for _, c := range resp.Containers {
		if c.Name == "web" {
			web = c
		}
	}
	if web.Project != "shop" || web.Service != "web" || web.Revision != "9c1e2d4aabbcc" || web.State != "running" ||
		strings.Join(web.Ports, ",") != "0.0.0.0:8080->80/tcp,9000/tcp" || web.Image != "ghcr.io/x/app:9c1e2d4" {
		t.Errorf("web = %+v", web)
	}
	if resp.Containers[0].Name != "old-1" { // sorted by name
		t.Errorf("first container = %q, want them sorted by name", resp.Containers[0].Name)
	}

	resp = ask(t, s, `{"id":2,"op":"images"}`)
	if !resp.OK || len(resp.Images) != 3 {
		t.Fatalf("images = %+v", resp)
	}
	if resp.Images[0].ID != "sha256:ddd" || !resp.Images[0].Dangling || len(resp.Images[0].Tags) != 0 { // the biggest first; <none> is not a tag
		t.Errorf("first image = %+v, want the biggest, dangling one", resp.Images[0])
	}
	if last := resp.Images[2]; last.ID != "sha256:aaa" || strings.Join(last.UsedBy, ",") != "web" {
		t.Errorf("last image = %+v, want aaa used by web", last)
	}

	resp = ask(t, s, `{"id":3,"op":"volumes"}`)
	if !resp.OK || len(resp.Volumes) != 2 {
		t.Fatalf("volumes = %+v", resp)
	}
	for _, v := range resp.Volumes {
		switch {
		case v.Name == "shop_db" && (v.Anonymous || v.Project != "shop" || strings.Join(v.UsedBy, ",") != "shop-db-1,web"):
			t.Errorf("shop_db = %+v", v)
		case v.Name != "shop_db" && !v.Anonymous:
			t.Errorf("%s should be anonymous", v.Name)
		}
	}

	resp = ask(t, s, `{"id":4,"op":"networks"}`)
	if !resp.OK || len(resp.Networks) != 2 || resp.Networks[0].Name != "bridge" {
		t.Fatalf("networks = %+v", resp)
	}
	shop := resp.Networks[1]
	if len(shop.Containers) != 2 || shop.Containers[1].Name != "web" || shop.Containers[1].Address != "172.18.0.3" {
		t.Errorf("shop_default = %+v, want web at 172.18.0.3 and shop-db-1", shop)
	}
}

// ------------------------------------------------------------ stacks

func TestStacksFromLabelsIncludingAProjectWhoseFileWasDeleted(t *testing.T) {
	s, _, _, composeFile := newTestSession(t)
	resp := ask(t, s, `{"id":1,"op":"stacks"}`)
	if !resp.OK || len(resp.Stacks) != 2 || resp.Command != "docker compose ls" {
		t.Fatalf("stacks = %+v", resp)
	}
	gone, shop := resp.Stacks[0], resp.Stacks[1] // sorted by project
	if gone.Project != "gone" || shop.Project != "shop" {
		t.Fatalf("projects = %q, %q", gone.Project, shop.Project)
	}
	if shop.Total != 2 || shop.Running != 1 || strings.Join(shop.Services, ",") != "db,web" ||
		strings.Join(shop.Containers, ",") != "shop-db-1,web" || len(shop.MissingFiles) != 0 ||
		strings.Join(shop.ConfigFiles, ",") != composeFile || shop.WorkingDir != filepath.Dir(composeFile) {
		t.Errorf("shop = %+v", shop)
	}
	if !strings.HasPrefix(shop.Command, "docker compose -p shop ") {
		t.Errorf("shop command = %q", shop.Command)
	}
	if len(gone.MissingFiles) != 1 || !strings.HasSuffix(gone.MissingFiles[0], "deleted.yml") {
		t.Errorf("gone = %+v, want its deleted compose file named", gone)
	}
}

func TestStackActionsRunComposeWithSeparateArgumentsAndNeverDashVByDefault(t *testing.T) {
	s, _, rec, composeFile := newTestSession(t)
	var calls [][]string
	old := runDockerCLI
	runDockerCLI = func(ctx context.Context, args []string) (string, error) {
		calls = append(calls, args)
		return "done", nil
	}
	t.Cleanup(func() { runDockerCLI = old })

	base := []string{"compose", "-p", "shop", "--project-directory", filepath.Dir(composeFile), "-f", composeFile}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }

	resp := ask(t, s, `{"id":1,"op":"stack","project":"shop","action":"recycle"}`)
	if !resp.OK || resp.Output != "done\ndone" {
		t.Fatalf("recycle = %+v", resp)
	}
	if len(calls) != 2 || strings.Join(calls[0], "\x00") != strings.Join(with("down"), "\x00") ||
		strings.Join(calls[1], "\x00") != strings.Join(with("up", "-d"), "\x00") {
		t.Errorf("recycle ran %v, want down and then up -d, with no -v", calls)
	}
	if !strings.Contains(resp.Command, " down && docker compose ") || !strings.HasSuffix(resp.Command, " up -d") {
		t.Errorf("recycle command = %q", resp.Command)
	}

	calls = nil
	ask(t, s, `{"id":2,"op":"stack","project":"shop","action":"down","volumes":true}`)
	if len(calls) != 1 || calls[0][len(calls[0])-2] != "down" || calls[0][len(calls[0])-1] != "-v" {
		t.Errorf("down with volumes ran %v, want it to end with down -v", calls)
	}

	calls = nil
	ask(t, s, `{"id":3,"op":"stack","project":"shop","action":"restart"}`)
	if len(calls) != 1 || strings.Join(calls[0], " ") != strings.Join(with("restart"), " ") {
		t.Errorf("restart ran %v", calls)
	}

	if got := rec.all(); len(got) != 3 || got[0].Op != protocol.DockerOpStack || !got[0].OK {
		t.Errorf("reports = %+v, want three stack changes", got)
	}
}

func TestStackRequestsThatAreRefusedBeforeDockerAreNotReported(t *testing.T) {
	s, _, rec, _ := newTestSession(t)
	called := false
	old := runDockerCLI
	runDockerCLI = func(ctx context.Context, args []string) (string, error) { called = true; return "", nil }
	t.Cleanup(func() { runDockerCLI = old })

	for name, request := range map[string]string{
		"an unknown project":        `{"id":1,"op":"stack","project":"nope","action":"down"}`,
		"a stack whose file is gone": `{"id":2,"op":"stack","project":"gone","action":"up"}`,
		"volumes with up":           `{"id":3,"op":"stack","project":"shop","action":"up","volumes":true}`,
		"a bad action":              `{"id":4,"op":"stack","project":"shop","action":"nuke"}`,
		"no project":                `{"id":5,"op":"stack","action":"down"}`,
	} {
		resp := ask(t, s, request)
		if resp.OK || resp.Error == "" {
			t.Errorf("%s: %+v, want a refusal", name, resp)
		}
	}
	if called {
		t.Error("docker compose was run for a request that had to be refused")
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("reports = %+v, want none: nothing was attempted", got)
	}
}

func TestAFailingComposeIsReportedAsAFailedChange(t *testing.T) {
	s, _, rec, _ := newTestSession(t)
	old := runDockerCLI
	runDockerCLI = func(ctx context.Context, args []string) (string, error) {
		return "no such service", &dockerError{message: "docker compose exited with status 1: no such service"}
	}
	t.Cleanup(func() { runDockerCLI = old })

	resp := ask(t, s, `{"id":1,"op":"stack","project":"shop","action":"up"}`)
	if resp.OK || resp.Error != "docker compose exited with status 1: no such service" || resp.Command == "" {
		t.Errorf("answer = %+v", resp)
	}
	if got := rec.all(); len(got) != 1 || got[0].OK || got[0].Error != resp.Error || got[0].Command != resp.Command {
		t.Errorf("reports = %+v, want the failure with its command", got)
	}
}

func TestComposePlanNeverHasDashVUnlessAskedFor(t *testing.T) {
	stack := protocol.DockerStack{Project: "p", WorkingDir: "/srv/p", ConfigFiles: []string{"/srv/p/a.yml", "/srv/p/b.yml"}}
	plan, err := composePlan(stack, protocol.DockerStackRecycle, false)
	if err != nil || len(plan) != 2 {
		t.Fatalf("plan = %v, %v", plan, err)
	}
	for _, args := range plan {
		for _, arg := range args {
			if arg == "-v" {
				t.Errorf("-v in %v although it was not asked for", args)
			}
		}
	}
	if want := "docker compose -p p --project-directory /srv/p -f /srv/p/a.yml -f /srv/p/b.yml down"; displayCommand(plan[0]) != want {
		t.Errorf("down = %q, want %q", displayCommand(plan[0]), want)
	}
	if _, err := composePlan(stack, "explode", false); err == nil {
		t.Error("an unknown action should be an error")
	}
	// A name with spaces is one argument, and is quoted only for display.
	odd := protocol.DockerStack{Project: "it's", ConfigFiles: []string{"/a b/c.yml"}}
	plan, _ = composePlan(odd, protocol.DockerStackUp, false)
	if plan[0][2] != "it's" || plan[0][4] != "/a b/c.yml" {
		t.Errorf("arguments = %v, want the odd names kept whole", plan[0])
	}
	if got := displayCommand(plan[0]); !strings.Contains(got, `'it'\''s'`) || !strings.Contains(got, `'/a b/c.yml'`) {
		t.Errorf("display = %q", got)
	}
}

// -------------------------------------------------------- refusals

// With a fake engine answering 409 {"message": "..."} to a remove, the answer is
// ok:false with that exact message and the docker rm command, the change report
// carries it, and no request ever has force=true.
func TestDockersRefusalIsPassedOnUnchangedAndNothingIsForced(t *testing.T) {
	s, log, rec, _ := newTestSession(t)

	resp := ask(t, s, `{"id":4,"op":"remove","container":"web"}`)
	if resp.OK || resp.Error != dockerRefusal || resp.Command != "docker rm web" || resp.ID != 4 {
		t.Errorf("answer = %+v, want Docker's message unchanged and the docker rm command", resp)
	}
	got := rec.all()
	if len(got) != 1 || got[0].Op != protocol.DockerOpRemove || got[0].OK || got[0].Error != dockerRefusal || got[0].Command != "docker rm web" {
		t.Errorf("reports = %+v, want the refused remove, with Docker's message", got)
	}

	// Stopping works, and removing the stopped one works.
	if resp := ask(t, s, `{"id":5,"op":"stop","container":"web"}`); !resp.OK || resp.Command != "docker stop web" {
		t.Errorf("stop = %+v", resp)
	}
	if resp := ask(t, s, `{"id":6,"op":"remove","container":"shop-db-1"}`); !resp.OK || resp.Command != "docker rm shop-db-1" {
		t.Errorf("remove = %+v", resp)
	}
	// An image a container uses: Docker's refusal again, and the image is never forced.
	hex := strings.Repeat("a", 64)
	resp = ask(t, s, `{"id":7,"op":"remove_image","image":"`+hex+`"}`)
	if resp.OK || !strings.Contains(resp.Error, "image is being used by running container") || resp.Command != "docker rmi sha256:"+hex {
		t.Errorf("remove_image = %+v", resp)
	}
	// "already started" is not an error.
	if resp := ask(t, s, `{"id":8,"op":"start","container":"web"}`); !resp.OK {
		t.Errorf("start of a running container = %+v, want ok (the engine answers 304)", resp)
	}

	removes := 0
	for _, line := range log.all() {
		if strings.Contains(line, "force=true") {
			t.Errorf("a request forced something: %s", line)
		}
		if strings.HasPrefix(line, "DELETE ") {
			removes++
			if !strings.Contains(line, "force=false") {
				t.Errorf("a remove did not say force=false: %s", line)
			}
			if strings.HasPrefix(line, "DELETE /v"+maxAPIVersion+"/containers/") && !strings.Contains(line, "v=false") {
				t.Errorf("a container remove did not say v=false: %s", line)
			}
		}
	}
	if removes != 3 {
		t.Errorf("%d DELETE requests, want 3", removes)
	}
	if n := len(rec.all()); n != 5 {
		t.Errorf("%d reports, want 5 (every change that was tried, refused ones included)", n)
	}
}

func TestPruneAnswersWhatWasRemovedAndTheSpace(t *testing.T) {
	s, log, rec, _ := newTestSession(t)

	resp := ask(t, s, `{"id":1,"op":"prune","what":"containers"}`)
	if !resp.OK || resp.Command != "docker container prune -f" || resp.SpaceReclaimed != 1048576 || strings.Join(resp.Removed, ",") != "5b6c" {
		t.Errorf("containers prune = %+v", resp)
	}
	resp = ask(t, s, `{"id":2,"op":"prune","what":"images","all":true}`)
	if !resp.OK || resp.Command != "docker image prune -a -f" || resp.SpaceReclaimed != 999000000 || len(resp.Removed) != 2 {
		t.Errorf("images prune = %+v", resp)
	}
	ask(t, s, `{"id":3,"op":"prune","what":"images"}`)

	filters := []string{}
	for _, line := range log.all() {
		if strings.Contains(line, "/images/prune") {
			filters = append(filters, line)
		}
	}
	if len(filters) != 2 || !strings.Contains(filters[0], "%22false%22") || !strings.Contains(filters[1], "%22true%22") {
		t.Errorf("image prune requests = %v, want dangling=false for all and dangling=true without", filters)
	}
	if len(rec.all()) != 3 {
		t.Errorf("%d reports, want 3", len(rec.all()))
	}
}

// ------------------------------------------------------- validation

func TestRequestsAreCheckedBeforeAnythingIsSentToDocker(t *testing.T) {
	s, log, rec, _ := newTestSession(t)
	bad := []string{
		`{"id":1,"op":"remove","container":"../etc"}`,
		`{"id":2,"op":"remove","container":"web; rm -rf /"}`,
		`{"id":3,"op":"stop","container":""}`,
		`{"id":4,"op":"start","container":"-f"}`,
		`{"id":5,"op":"inspect","container":"a/b"}`,
		`{"id":6,"op":"logs","container":"web","tail":2001}`,
		`{"id":7,"op":"logs","container":"web","tail":-5}`,
		`{"id":8,"op":"remove_image","image":"sha256:short"}`,
		`{"id":9,"op":"remove_image","image":"web"}`,
		`{"id":10,"op":"remove_image","image":"sha256:` + strings.Repeat("g", 64) + `"}`,
		`{"id":11,"op":"prune","what":"volumes"}`,
		`{"id":12,"op":"prune"}`,
		`{"id":13,"op":"format-disk"}`,
		`not json`,
	}
	for _, request := range bad {
		resp := ask(t, s, request)
		if resp.OK || resp.Error == "" {
			t.Errorf("%s: %+v, want a refusal", request, resp)
		}
	}
	if lines := log.all(); len(lines) != 0 {
		t.Errorf("requests reached the engine: %v", lines)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("reports = %+v: nothing was attempted, so nothing is reported", got)
	}
	if resp := ask(t, s, `not json`); resp.Error != "invalid request" {
		t.Errorf("invalid JSON: %+v, want \"invalid request\"", resp)
	}
}

func TestPlanRequestAcceptsGoodNamesAndFillsDefaults(t *testing.T) {
	hex := strings.Repeat("0123456789abcdef", 4)
	tests := []struct {
		req  protocol.DockerRequest
		want string
	}{
		{protocol.DockerRequest{Op: "containers"}, "docker ps -a"},
		{protocol.DockerRequest{Op: "logs", Container: "web.1_a-b"}, "docker logs --tail 200 web.1_a-b"},
		{protocol.DockerRequest{Op: "logs", Container: "web", Tail: 2000}, "docker logs --tail 2000 web"},
		{protocol.DockerRequest{Op: "restart", Container: "3f2a9c"}, "docker restart 3f2a9c"},
		{protocol.DockerRequest{Op: "remove_image", Image: hex}, "docker rmi sha256:" + hex},
		{protocol.DockerRequest{Op: "remove_image", Image: "sha256:" + hex}, "docker rmi sha256:" + hex},
		{protocol.DockerRequest{Op: "prune", What: "build_cache"}, "docker builder prune -f"},
		{protocol.DockerRequest{Op: "prune", What: "images"}, "docker image prune -f"},
		{protocol.DockerRequest{Op: "stack", Project: "shop", Action: "down", Volumes: true}, ""},
	}
	for _, tc := range tests {
		req := tc.req
		got, err := planRequest(&req)
		if err != nil || got != tc.want {
			t.Errorf("planRequest(%+v) = %q, %v, want %q", tc.req, got, err, tc.want)
		}
	}
}

// ----------------------------------------------- Docker not reachable

func TestWhenDockerCannotBeUsedTheAnswerSaysWhy(t *testing.T) {
	enableDocker(t)
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	rec := &recorder{}
	s := newDockerSession(filepath.Join(dir, "nothing.sock"), rec.report)

	resp := ask(t, s, `{"id":1,"op":"containers"}`)
	if resp.OK || resp.Error != errDockerMissing.Error() {
		t.Errorf("no socket: %+v, want %q", resp, errDockerMissing)
	}
	// Nothing reached Docker, so a change is not reported either.
	if resp := ask(t, s, `{"id":2,"op":"remove","container":"web"}`); resp.OK || resp.Command != "docker rm web" {
		t.Errorf("remove without Docker: %+v", resp)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("reports = %+v, want none", got)
	}

	dockerSupported = false // not Linux
	if resp := ask(t, s, `{"id":3,"op":"containers"}`); resp.Error != errDockerUnsupported.Error() {
		t.Errorf("unsupported: %+v, want %q", resp, errDockerUnsupported)
	}
}

func TestTheHeartbeatSaysWhetherDockerAnswers(t *testing.T) {
	enableDocker(t)
	socket, _ := startFakeEngine(t, engineWith("/x/compose.yml", "/x/gone.yml"))
	if got := dockerStatusAt(socket); got != protocol.DockerOK {
		t.Errorf("a socket that answers /_ping: %q, want ok", got)
	}
	missing := filepath.Join(filepath.Dir(socket), "other.sock")
	if got := dockerStatusAt(missing); got != protocol.DockerNone {
		t.Errorf("no socket: %q, want none", got)
	}
	dockerSupported = false
	if got := dockerStatusAt(socket); got != protocol.DockerNone {
		t.Errorf("not Linux: %q, want none", got)
	}
}
