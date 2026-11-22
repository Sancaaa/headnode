package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// dockerSupported says whether this agent may talk to Docker at all: only Linux
// (D-70). A variable, not a constant, so a test on another system can switch it on.
var dockerSupported = runtime.GOOS == "linux"

// maxDockerRequests is how many requests of one session run at the same time. A
// `stack up` that pulls images takes minutes, and a request for the container list
// must not wait behind it; a flood of requests waits instead of piling up.
const maxDockerRequests = 8

// composeTimeout bounds one docker compose call (pulling an image can be slow).
const composeTimeout = 10 * time.Minute

var (
	// A container is named or identified by text like this. Docker's own rule for
	// names; ids are hex, so they fit too. Nothing else is ever put in a request path.
	dockerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	// An image is identified by its id: sha256: and 64 hex characters, or the 64 alone.
	dockerImagePattern = regexp.MustCompile(`^(sha256:)?[0-9a-f]{64}$`)
)

// refusedError is a request the agent turned down before it asked Docker for
// anything: a bad name, an unknown stack, Docker not reachable. Nothing was
// attempted, so it is not reported as a change (like a read-only files session).
// Docker's own refusals are not this: they are changes that were tried.
type refusedError struct{ message string }

func (e *refusedError) Error() string { return e.message }

func refuse(format string, args ...any) error {
	return &refusedError{message: fmt.Sprintf(format, args...)}
}

// runDocker serves a Docker tab's session: the browser sends one JSON request per
// text frame and the agent answers each with one JSON answer carrying the same id
// (protocol.md §4), the way a files session does. report is told about every
// change that was tried (D-70); it may be nil.
func runDocker(ctx context.Context, conn *websocket.Conn, socket string, report func(protocol.DockerChange)) error {
	session := newDockerSession(socket, report)
	slog.Info("docker session started")

	slots := make(chan struct{}, maxDockerRequests)
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return nil // the browser left, or the agent is stopping: a normal end
		}
		if kind != websocket.MessageText {
			continue // only text frames carry requests
		}
		slots <- struct{}{} // a request is handled on its own goroutine, up to maxDockerRequests at once
		go func() {
			defer func() { <-slots }()
			// The agent's own lifetime, not the session's: closing the tab in the
			// middle of a `compose down` must not kill it half way.
			reply, err := json.Marshal(session.handle(ctx, data))
			if err != nil {
				slog.Warn("could not encode a docker answer", "err", err)
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			defer cancel()
			// Several goroutines may write to one WebSocket. If the browser is gone, this fails, and that is fine.
			if err := conn.Write(writeCtx, websocket.MessageText, reply); err != nil {
				slog.Debug("could not send a docker answer", "err", err)
			}
		}()
	}
}

// dockerReporter returns the function a docker session calls for every change. It
// adds the session id, so the server can find the person the session was opened
// for, and sends the report on the control connection, as changeReporter does for
// files. A report that cannot be sent is logged and dropped.
func dockerReporter(ctx context.Context, control *websocket.Conn, sessionID string) func(protocol.DockerChange) {
	return func(change protocol.DockerChange) {
		change.SessionID = sessionID
		if err := send(ctx, control, protocol.TypeDockerChange, change); err != nil {
			slog.Warn("could not report a docker change", "session", sessionID, "op", change.Op, "err", err)
		}
	}
}

// dockerSession is the state of one Docker tab's session.
type dockerSession struct {
	client *dockerClient
	report func(protocol.DockerChange)

	mu sync.Mutex // only one request negotiates the API version
}

func newDockerSession(socket string, report func(protocol.DockerChange)) *dockerSession {
	return &dockerSession{client: newDockerClient(socket), report: report}
}

// ensure makes sure Docker can be used and the API version is chosen. It is tried
// again on the next request when it failed, so a person who fixes the problem (the
// socket appears, the agent is restarted) only has to press Refresh.
func (d *dockerSession) ensure(ctx context.Context) error {
	if !dockerSupported {
		return errDockerUnsupported
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client.version != "" {
		return nil
	}
	negotiateCtx, cancel := context.WithTimeout(ctx, dockerReadTimeout)
	defer cancel()
	return friendlyDockerError(d.client.negotiate(negotiateCtx))
}

// handle decodes one request and returns its answer. It never fails: an error
// becomes {"ok":false,"error":"..."} for that request, so one bad request cannot
// end the session. A change that was tried is reported, Docker's refusal included.
func (d *dockerSession) handle(ctx context.Context, data []byte) protocol.DockerResponse {
	var req protocol.DockerRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return protocol.DockerResponse{Error: "invalid request"}
	}
	resp, err := d.run(ctx, &req)
	if err != nil {
		resp = protocol.DockerResponse{Command: resp.Command, Error: err.Error()}
	} else {
		resp.OK = true
	}
	resp.ID = req.ID

	var refusal *refusedError
	if d.report != nil && protocol.IsDockerChange(req.Op) && !errors.As(err, &refusal) {
		d.report(protocol.DockerChange{Op: req.Op, Command: resp.Command, OK: err == nil, Error: resp.Error})
	}
	return resp
}

// planRequest checks what the browser sent, fills in defaults, and returns the
// docker command the request stands for. The agent checks everything, because it
// trusts nobody: a name that is not a plain name never reaches a request path.
// It returns "" for a stack action, whose command depends on the stack.
func planRequest(req *protocol.DockerRequest) (string, error) {
	switch req.Op {
	case protocol.DockerOpContainers:
		return "docker ps -a", nil
	case protocol.DockerOpImages:
		return "docker images", nil
	case protocol.DockerOpVolumes:
		return "docker volume ls", nil
	case protocol.DockerOpNetworks:
		return "docker network ls", nil
	case protocol.DockerOpStacks:
		return "docker compose ls", nil

	case protocol.DockerOpInspect:
		if err := checkContainerName(req.Container); err != nil {
			return "", err
		}
		return "docker inspect " + req.Container, nil
	case protocol.DockerOpLogs:
		if err := checkContainerName(req.Container); err != nil {
			return "", err
		}
		if req.Tail == 0 {
			req.Tail = dockerLogTailDefault
		}
		if req.Tail < 1 || req.Tail > dockerLogTailMax {
			return "", refuse("tail must be a number from 1 to %d", dockerLogTailMax)
		}
		return fmt.Sprintf("docker logs --tail %d %s", req.Tail, req.Container), nil
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart:
		if err := checkContainerName(req.Container); err != nil {
			return "", err
		}
		return "docker " + req.Op + " " + req.Container, nil
	case protocol.DockerOpRemove:
		if err := checkContainerName(req.Container); err != nil {
			return "", err
		}
		return "docker rm " + req.Container, nil

	case protocol.DockerOpRemoveImage:
		if !dockerImagePattern.MatchString(req.Image) {
			return "", refuse("image must be an image id: sha256: and 64 hex characters")
		}
		if !strings.HasPrefix(req.Image, "sha256:") {
			req.Image = "sha256:" + req.Image
		}
		return "docker rmi " + req.Image, nil

	case protocol.DockerOpPrune:
		switch req.What {
		case protocol.DockerPruneContainers:
			return "docker container prune -f", nil
		case protocol.DockerPruneImages:
			if req.All {
				return "docker image prune -a -f", nil
			}
			return "docker image prune -f", nil
		case protocol.DockerPruneBuildCache:
			return "docker builder prune -f", nil
		}
		return "", refuse("what must be containers, images or build_cache")

	case protocol.DockerOpStack:
		if req.Project == "" {
			return "", refuse("project is required")
		}
		switch req.Action {
		case protocol.DockerStackDown, protocol.DockerStackRecycle:
		case protocol.DockerStackUp, protocol.DockerStackRestart:
			if req.Volumes {
				return "", refuse("volumes only goes with down or recycle")
			}
		default:
			return "", refuse("action must be down, up, restart or recycle")
		}
		return "", nil
	}
	return "", refuse("unknown operation")
}

func checkContainerName(name string) error {
	if len(name) > 200 || !dockerNamePattern.MatchString(name) {
		return refuse("container must be a container name or id")
	}
	return nil
}

// run does what one request asks. The returned answer carries the command even
// when the error is not nil.
func (d *dockerSession) run(ctx context.Context, req *protocol.DockerRequest) (protocol.DockerResponse, error) {
	var resp protocol.DockerResponse
	command, err := planRequest(req)
	if err != nil {
		return resp, err
	}
	resp.Command = command
	if err := d.ensure(ctx); err != nil {
		return resp, &refusedError{message: err.Error()} // nothing reached Docker
	}
	if req.Op == protocol.DockerOpStack {
		return d.runStack(ctx, req, resp)
	}

	timeout := dockerReadTimeout
	switch req.Op {
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart, protocol.DockerOpRemove, protocol.DockerOpRemoveImage:
		timeout = dockerChangeTimeout
	case protocol.DockerOpPrune:
		timeout = dockerPruneTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch req.Op {
	case protocol.DockerOpContainers:
		resp.Containers, err = d.client.containers(ctx)
	case protocol.DockerOpInspect:
		resp.Inspect, err = d.client.inspect(ctx, req.Container)
	case protocol.DockerOpLogs:
		resp.Logs, resp.Truncated, err = d.client.logs(ctx, req.Container, req.Tail)
	case protocol.DockerOpImages:
		resp.Images, err = d.client.images(ctx)
	case protocol.DockerOpVolumes:
		resp.Volumes, err = d.client.volumes(ctx)
	case protocol.DockerOpNetworks:
		resp.Networks, err = d.client.networks(ctx)
	case protocol.DockerOpStacks:
		var raw []engineContainer
		if raw, err = d.client.rawContainers(ctx); err == nil {
			resp.Stacks = stacksFromContainers(raw)
		}
	case protocol.DockerOpStart, protocol.DockerOpStop, protocol.DockerOpRestart, protocol.DockerOpRemove:
		err = d.client.containerAction(ctx, req.Op, req.Container)
	case protocol.DockerOpRemoveImage:
		resp.Removed, err = d.client.removeImage(ctx, req.Image)
	case protocol.DockerOpPrune:
		resp.Removed, resp.SpaceReclaimed, err = d.client.prune(ctx, req.What, req.All)
	}
	return resp, friendlyDockerError(err)
}

// runStack runs a Compose action on a stack. The browser named the project; the
// directory and files come from the labels of containers that exist right now, so
// the browser cannot make the agent run Compose on a file of its choosing.
func (d *dockerSession) runStack(ctx context.Context, req *protocol.DockerRequest, resp protocol.DockerResponse) (protocol.DockerResponse, error) {
	listCtx, cancel := context.WithTimeout(ctx, dockerReadTimeout)
	raw, err := d.client.rawContainers(listCtx)
	cancel()
	if err != nil {
		return resp, &refusedError{message: friendlyDockerError(err).Error()}
	}
	var stack *protocol.DockerStack
	stacks := stacksFromContainers(raw)
	for i := range stacks {
		if stacks[i].Project == req.Project {
			stack = &stacks[i]
		}
	}
	if stack == nil {
		return resp, refuse("there is no Compose stack named %q on this host", req.Project)
	}
	if err := checkStackFiles(*stack); err != nil {
		return resp, &refusedError{message: err.Error()}
	}
	plan, err := composePlan(*stack, req.Action, req.Volumes)
	if err != nil {
		return resp, &refusedError{message: err.Error()}
	}
	commands := make([]string, len(plan))
	for i, args := range plan {
		commands[i] = displayCommand(args)
	}
	resp.Command = strings.Join(commands, " && ")

	runCtx, cancelRun := context.WithTimeout(ctx, composeTimeout)
	defer cancelRun()
	var output []string
	for _, args := range plan { // a recycle stops at a failed down: it does not bring the stack up again over it
		text, err := runDockerCLI(runCtx, args)
		if text != "" {
			output = append(output, text)
		}
		if err != nil {
			return resp, err
		}
	}
	resp.Output = strings.Join(output, "\n")
	return resp, nil
}
