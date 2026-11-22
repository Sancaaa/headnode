package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"headnode/protocol"
)

// maxAPIVersion is the newest Docker Engine API version this code was written
// against (D-70). The agent uses the engine's own version, but never a higher one
// than this: a newer engine still answers an older version, and the shapes read
// here (names, ids, state, labels, sizes, mounts) have been the same for years.
// Raise it after reading `docker version` on a machine with a newer engine.
const maxAPIVersion = "1.47"

// dockerSocket is where the Docker Engine listens on Linux.
const dockerSocket = "/var/run/docker.sock"

const (
	dockerLogTailDefault = 200
	dockerLogTailMax     = 2000
	dockerLogMaxBytes    = 1 << 20 // what one logs answer may carry
	dockerLogReadMax     = 8 << 20 // what is read from Docker at most, so a huge line cannot fill the memory
	dockerJSONReadMax    = 32 << 20

	dockerReadTimeout   = 30 * time.Second
	dockerChangeTimeout = 90 * time.Second // stop and restart wait up to 10 seconds for the container
	dockerPruneTimeout  = 5 * time.Minute
)

// What a person is told when Docker cannot be used at all. These are the agent's
// words; when Docker itself refuses something, its words are passed on instead.
var (
	errDockerUnsupported = errors.New("Docker is not supported on this system")
	errDockerMissing     = errors.New("Docker is not installed or not running on this host")
	errDockerNoAccess    = errors.New("this agent may not use Docker: add its user to the docker group " +
		"(sudo usermod -aG docker <user>), then restart the agent")
)

// dockerError is Docker's own refusal: an HTTP error status with {"message": "..."}.
// Its text is exactly what Docker wrote, so the person reads what the CLI would
// have printed.
type dockerError struct{ message string }

func (e *dockerError) Error() string { return e.message }

// dockerClient talks to the Docker Engine API over its unix socket with plain
// net/http. No Docker SDK: it would be a large dependency for a dozen JSON calls
// (D-70).
type dockerClient struct {
	http    *http.Client
	version string // like "1.47", set by negotiate; empty until then
}

func newDockerClient(socket string) *dockerClient {
	return &dockerClient{http: &http.Client{Transport: &http.Transport{
		// The URL's host ("docker") is only a label: every connection goes to the socket.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		// One request per connection: the socket is local and cheap, and nothing idle is left behind.
		DisableKeepAlives: true,
	}}}
}

// v puts the negotiated API version in front of a path.
func (c *dockerClient) v(path string) string { return "/v" + c.version + path }

// open sends one request and returns the response when Docker accepted it. Any
// status of 400 or more becomes a *dockerError carrying Docker's message. The
// caller closes the body.
func (c *dockerClient) open(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	target := "http://docker" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readDockerError(resp)
	}
	return resp, nil
}

func readDockerError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		return &dockerError{message: parsed.Message}
	}
	if text := strings.TrimSpace(string(body)); text != "" {
		return &dockerError{message: truncateRunes(text, 500)}
	}
	return &dockerError{message: resp.Status}
}

// call sends a request and decodes the JSON answer into out. out may be nil when
// the answer does not matter (a 204 has no body at all).
func (c *dockerClient) call(ctx context.Context, method, path string, query url.Values, out any) error {
	resp, err := c.open(ctx, method, path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, dockerJSONReadMax))
		return nil
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, dockerJSONReadMax)).Decode(out)
	if errors.Is(err, io.EOF) {
		return nil // an empty body: nothing to decode
	}
	return err
}

// ------------------------------------------------------- the API version

// apiVersion is "1.47" as two numbers, so 1.9 is older than 1.47.
type apiVersion [2]int

func parseAPIVersion(text string) (apiVersion, bool) {
	major, minor, found := strings.Cut(strings.TrimSpace(text), ".")
	if !found {
		return apiVersion{}, false
	}
	a, errA := strconv.Atoi(major)
	b, errB := strconv.Atoi(minor)
	if errA != nil || errB != nil || a < 0 || b < 0 {
		return apiVersion{}, false
	}
	return apiVersion{a, b}, true
}

func (a apiVersion) less(b apiVersion) bool {
	return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
}

func (a apiVersion) String() string { return fmt.Sprintf("%d.%d", a[0], a[1]) }

// chooseAPIVersion picks the version to speak (D-70): the engine's own, but no
// higher than the newest this code was written against (capVersion), and never
// below the engine's minimum, which wins when the cap is under it (an engine that
// refuses old versions must be spoken to in a version it accepts). It returns ""
// when the engine did not say a usable version.
func chooseAPIVersion(engine, minimum, capVersion string) string {
	chosen, ok := parseAPIVersion(engine)
	if !ok {
		return ""
	}
	if limit, ok := parseAPIVersion(capVersion); ok && limit.less(chosen) {
		chosen = limit
	}
	if lowest, ok := parseAPIVersion(minimum); ok && chosen.less(lowest) {
		chosen = lowest
	}
	return chosen.String()
}

// negotiate asks the engine which versions it speaks (the one path that has no
// version in front) and remembers the one to use.
func (c *dockerClient) negotiate(ctx context.Context) error {
	var info struct {
		APIVersion    string `json:"ApiVersion"`
		MinAPIVersion string `json:"MinAPIVersion"`
	}
	if err := c.call(ctx, http.MethodGet, "/version", nil, &info); err != nil {
		return err
	}
	version := chooseAPIVersion(info.APIVersion, info.MinAPIVersion, maxAPIVersion)
	if version == "" {
		return errors.New("Docker did not say which API version it speaks")
	}
	c.version = version
	return nil
}

// friendlyDockerError turns a failure to reach Docker into words for a person.
// Docker's own refusals pass through unchanged.
func friendlyDockerError(err error) error {
	var refusal *dockerError
	switch {
	case err == nil, errors.As(err, &refusal):
		return err
	case errors.Is(err, os.ErrPermission):
		return errDockerNoAccess
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("Docker did not answer in time")
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return errDockerMissing // the socket is not there, or nobody listens on it
	}
	return err
}

// dockerStatusAt is the heartbeat's `docker` value: whether the socket answers a
// /_ping within a second, and if not, whether that is because this agent's user
// may not open it (M12, D-70).
func dockerStatusAt(socket string) string {
	if !dockerSupported {
		return protocol.DockerNone
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := newDockerClient(socket).open(ctx, http.MethodGet, "/_ping", nil)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return protocol.DockerNoAccess
		}
		return protocol.DockerNone
	}
	resp.Body.Close()
	return protocol.DockerOK
}

// ------------------------------------------------------------ containers

// engineContainer is the part of one entry of GET /containers/json that this code
// reads. Only long-stable fields (D-70).
type engineContainer struct {
	ID      string `json:"Id"`
	Names   []string
	Image   string
	ImageID string
	Created int64
	Ports   []struct {
		IP          string
		PrivatePort int
		PublicPort  int
		Type        string
	}
	Labels map[string]string
	State  string
	Status string
	Mounts []struct {
		Type string
		Name string
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

const (
	labelRevision = "org.opencontainers.image.revision"
)

func (c *dockerClient) rawContainers(ctx context.Context) ([]engineContainer, error) {
	var list []engineContainer
	err := c.call(ctx, http.MethodGet, c.v("/containers/json"), url.Values{"all": {"1"}}, &list)
	return list, err
}

// dockerContainerName is the name Docker shows. A container linked to another also
// has names like "/other/alias"; the plain "/name" is the real one.
func dockerContainerName(c engineContainer) string {
	for _, name := range c.Names {
		if strings.Count(name, "/") == 1 {
			return strings.TrimPrefix(name, "/")
		}
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

func dockerPorts(c engineContainer) []string {
	var out []string
	for _, p := range c.Ports {
		if p.PublicPort > 0 {
			ip := p.IP
			if ip == "" {
				ip = "0.0.0.0"
			}
			out = append(out, fmt.Sprintf("%s:%d->%d/%s", ip, p.PublicPort, p.PrivatePort, p.Type))
		} else {
			out = append(out, fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
		}
	}
	return out
}

func (c *dockerClient) containers(ctx context.Context) ([]protocol.DockerContainer, error) {
	raw, err := c.rawContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.DockerContainer, 0, len(raw))
	for _, rc := range raw {
		out = append(out, protocol.DockerContainer{
			ID:       rc.ID,
			Name:     dockerContainerName(rc),
			Image:    rc.Image,
			ImageID:  rc.ImageID,
			State:    rc.State,
			Status:   rc.Status,
			Created:  time.Unix(rc.Created, 0).UTC(),
			Ports:    dockerPorts(rc),
			Project:  rc.Labels[labelComposeProject],
			Service:  rc.Labels[labelComposeService],
			Revision: rc.Labels[labelRevision], // a container carries its image's labels
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// inspectRaw is the body of GET /containers/{id}/json.
func (c *dockerClient) inspectRaw(ctx context.Context, container string) ([]byte, error) {
	resp, err := c.open(ctx, http.MethodGet, c.v("/containers/"+container+"/json"), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, dockerJSONReadMax))
}

// hideEnvironment replaces the value of every environment variable with
// "(hidden)": the names show what is configured, the values (passwords, tokens)
// stay on the host. Numbers are kept as they were written (UseNumber), so nothing
// else in the document changes.
func hideEnvironment(raw []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("read the container's description: %w", err)
	}
	if config, ok := doc["Config"].(map[string]any); ok {
		if env, ok := config["Env"].([]any); ok {
			for i, entry := range env {
				text, _ := entry.(string)
				name, _, _ := strings.Cut(text, "=")
				env[i] = name + "=(hidden)"
			}
		}
	}
	return json.Marshal(doc)
}

func (c *dockerClient) inspect(ctx context.Context, container string) (json.RawMessage, error) {
	raw, err := c.inspectRaw(ctx, container)
	if err != nil {
		return nil, err
	}
	return hideEnvironment(raw)
}

// -------------------------------------------------------------------- logs

// demuxLogs reads the stream the Engine API sends for a container that has no
// TTY: stdout and stderr in frames of an 8-byte header (stream, three zero bytes,
// the size as a big-endian number) and the bytes. io.ReadFull and io.CopyN keep
// going over short reads, so a frame split anywhere is read whole. The two
// streams come out together, in the order they arrived. A stream cut short (the
// read limit) returns what was read, without an error.
func demuxLogs(r io.Reader) ([]byte, error) {
	var out bytes.Buffer
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return out.Bytes(), nil
			}
			return out.Bytes(), err
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, err := io.CopyN(&out, r, size); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return out.Bytes(), nil
			}
			return out.Bytes(), err
		}
	}
}

// logs returns the last `tail` lines of a container's output, at most 1 MB.
func (c *dockerClient) logs(ctx context.Context, container string, tail int) (text string, truncated bool, err error) {
	raw, err := c.inspectRaw(ctx, container)
	if err != nil {
		return "", false, err
	}
	// Without a TTY the output is multiplexed (see demuxLogs); with one it is raw.
	var info struct {
		Config struct{ Tty bool }
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", false, fmt.Errorf("read the container's description: %w", err)
	}

	query := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)}}
	resp, err := c.open(ctx, http.MethodGet, c.v("/containers/"+container+"/logs"), query)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	limited := &io.LimitedReader{R: resp.Body, N: dockerLogReadMax}
	var out []byte
	if info.Config.Tty {
		out, err = io.ReadAll(limited)
	} else {
		out, err = demuxLogs(limited)
	}
	if err != nil {
		return "", false, err
	}
	truncated = limited.N == 0
	if len(out) > dockerLogMaxBytes {
		out, truncated = out[len(out)-dockerLogMaxBytes:], true
	}
	return strings.ToValidUTF8(string(out), "�"), truncated, nil
}

// --------------------------------------------------------- changes: containers

// containerAction is `docker start|stop|restart|rm`. A container that is already
// in the wanted state is answered 304 by the Engine, which open() does not treat
// as an error, so it is not one here either.
//
// force is never sent as true (D-70): Docker refuses to remove a running
// container, and its message is what the person reads.
func (c *dockerClient) containerAction(ctx context.Context, op, container string) error {
	switch op {
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart:
		return c.call(ctx, http.MethodPost, c.v("/containers/"+container+"/"+op), nil, nil)
	case protocol.DockerOpRemove:
		// v=false: the container's anonymous volumes stay. The dialog says so.
		return c.call(ctx, http.MethodDelete, c.v("/containers/"+container),
			url.Values{"force": {"false"}, "v": {"false"}}, nil)
	}
	return errors.New("unknown operation")
}

// -------------------------------------------------------------------- images

func (c *dockerClient) images(ctx context.Context) ([]protocol.DockerImage, error) {
	var list []struct {
		ID       string `json:"Id"`
		RepoTags []string
		Size     int64
		Created  int64
	}
	if err := c.call(ctx, http.MethodGet, c.v("/images/json"), nil, &list); err != nil {
		return nil, err
	}
	// Which containers use which image: the list calls do not say (the field that
	// does is deprecated), and the containers carry their image's id.
	all, err := c.rawContainers(ctx)
	if err != nil {
		return nil, err
	}
	users := map[string][]string{} // image id -> containers
	for _, rc := range all {
		users[rc.ImageID] = append(users[rc.ImageID], dockerContainerName(rc))
	}
	out := make([]protocol.DockerImage, 0, len(list))
	for _, img := range list {
		var tags []string
		for _, tag := range img.RepoTags {
			if tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		used := users[img.ID]
		sort.Strings(used)
		out = append(out, protocol.DockerImage{
			ID: img.ID, Tags: tags, Size: img.Size, Created: time.Unix(img.Created, 0).UTC(),
			UsedBy: used, Dangling: len(tags) == 0,
		})
	}
	// The biggest first: it shows what a multi-stage build saves (D-70).
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out, nil
}

// removeImage is `docker rmi <id>`. noprune and force are false: Docker refuses an
// image a container uses, and says so.
func (c *dockerClient) removeImage(ctx context.Context, id string) ([]string, error) {
	var steps []map[string]string
	err := c.call(ctx, http.MethodDelete, c.v("/images/"+id),
		url.Values{"force": {"false"}, "noprune": {"false"}}, &steps)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, step := range steps {
		if v := step["Untagged"]; v != "" {
			removed = append(removed, "Untagged: "+v)
		}
		if v := step["Deleted"]; v != "" {
			removed = append(removed, "Deleted: "+v)
		}
	}
	return removed, nil
}

// ------------------------------------------------------------------- volumes

// anonymousVolume matches the name Docker gives a volume nobody named: 64 hex characters.
var anonymousVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *dockerClient) volumes(ctx context.Context) ([]protocol.DockerVolume, error) {
	var list struct {
		Volumes []struct {
			Name       string
			Driver     string
			Mountpoint string
			Labels     map[string]string
		}
	}
	if err := c.call(ctx, http.MethodGet, c.v("/volumes"), nil, &list); err != nil {
		return nil, err
	}
	raw, err := c.rawContainers(ctx)
	if err != nil {
		return nil, err
	}
	users := map[string][]string{} // volume name -> containers
	for _, rc := range raw {
		for _, mount := range rc.Mounts {
			if mount.Type == "volume" && mount.Name != "" {
				users[mount.Name] = append(users[mount.Name], dockerContainerName(rc))
			}
		}
	}
	out := make([]protocol.DockerVolume, 0, len(list.Volumes))
	for _, vol := range list.Volumes {
		used := users[vol.Name]
		sort.Strings(used)
		out = append(out, protocol.DockerVolume{
			Name: vol.Name, Driver: vol.Driver, Mountpoint: vol.Mountpoint,
			Anonymous: anonymousVolume.MatchString(vol.Name), UsedBy: used,
			Project: vol.Labels[labelComposeProject],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ------------------------------------------------------------------ networks

func (c *dockerClient) networks(ctx context.Context) ([]protocol.DockerNetwork, error) {
	var list []struct {
		ID     string `json:"Id"`
		Name   string
		Driver string
		Scope  string
	}
	if err := c.call(ctx, http.MethodGet, c.v("/networks"), nil, &list); err != nil {
		return nil, err
	}
	raw, err := c.rawContainers(ctx)
	if err != nil {
		return nil, err
	}
	// The network list does not say who is on a network; every container does say
	// which networks it is on, with its address there.
	members := map[string][]protocol.DockerNetworkMember{}
	for _, rc := range raw {
		for network, endpoint := range rc.NetworkSettings.Networks {
			members[network] = append(members[network],
				protocol.DockerNetworkMember{Name: dockerContainerName(rc), Address: endpoint.IPAddress})
		}
	}
	out := make([]protocol.DockerNetwork, 0, len(list))
	for _, n := range list {
		in := members[n.Name]
		sort.Slice(in, func(i, j int) bool { return in[i].Name < in[j].Name })
		out = append(out, protocol.DockerNetwork{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Containers: in})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// --------------------------------------------------------------------- prune

// prune runs one of the three prunes and returns what Docker says it removed and
// how much space that freed. images with all=true removes every image no
// container uses; without it, only dangling ones (no tag).
func (c *dockerClient) prune(ctx context.Context, what string, all bool) (removed []string, reclaimed uint64, err error) {
	var path string
	query := url.Values{}
	switch what {
	case protocol.DockerPruneContainers:
		path = "/containers/prune"
	case protocol.DockerPruneImages:
		path = "/images/prune"
		dangling := "true"
		if all {
			dangling = "false"
		}
		query.Set("filters", `{"dangling":["`+dangling+`"]}`)
	case protocol.DockerPruneBuildCache:
		path = "/build/prune"
	default:
		return nil, 0, errors.New("unknown operation")
	}

	var result struct {
		ContainersDeleted []string
		ImagesDeleted     []struct{ Untagged, Deleted string }
		CachesDeleted     []string
		SpaceReclaimed    uint64
	}
	if err := c.call(ctx, http.MethodPost, c.v(path), query, &result); err != nil {
		return nil, 0, err
	}
	removed = append(removed, result.ContainersDeleted...)
	for _, img := range result.ImagesDeleted {
		if img.Untagged != "" {
			removed = append(removed, "Untagged: "+img.Untagged)
		}
		if img.Deleted != "" {
			removed = append(removed, "Deleted: "+img.Deleted)
		}
	}
	removed = append(removed, result.CachesDeleted...)
	return removed, result.SpaceReclaimed, nil
}

// truncateRunes shortens text to at most n characters, without cutting one in half.
func truncateRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "..."
}
