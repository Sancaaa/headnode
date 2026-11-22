package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"headnode/protocol"
)

// Docker Compose is not part of the Engine API, but every container it creates
// carries labels that say which project, directory and files made it. Stacks are
// found from those labels, and acted on with the `docker compose` CLI (D-70).
const (
	labelComposeProject     = "com.docker.compose.project"
	labelComposeService     = "com.docker.compose.service"
	labelComposeWorkingDir  = "com.docker.compose.project.working_dir"
	labelComposeConfigFiles = "com.docker.compose.project.config_files"
)

// composeOutputMax is how much of docker compose's output is kept: the end of it,
// where its error is.
const composeOutputMax = 4096

// stacksFromContainers groups containers by their Compose project. Everything the
// agent later runs comes from here, from containers that exist: the browser only
// names a project, never a directory or a file.
func stacksFromContainers(list []engineContainer) []protocol.DockerStack {
	byProject := map[string]*protocol.DockerStack{}
	for _, c := range list {
		project := c.Labels[labelComposeProject]
		if project == "" {
			continue
		}
		stack := byProject[project]
		if stack == nil {
			stack = &protocol.DockerStack{
				Project:     project,
				WorkingDir:  c.Labels[labelComposeWorkingDir],
				ConfigFiles: splitConfigFiles(c.Labels[labelComposeConfigFiles]),
			}
			byProject[project] = stack
		}
		stack.Containers = append(stack.Containers, dockerContainerName(c))
		if service := c.Labels[labelComposeService]; service != "" && !slices.Contains(stack.Services, service) {
			stack.Services = append(stack.Services, service)
		}
		stack.Total++
		if c.State == "running" {
			stack.Running++
		}
	}

	out := make([]protocol.DockerStack, 0, len(byProject))
	for _, stack := range byProject {
		sort.Strings(stack.Services)
		sort.Strings(stack.Containers)
		for _, file := range stack.ConfigFiles {
			// A compose file that was moved or deleted is shown, not hidden: Compose
			// cannot act on the stack without it.
			if _, err := os.Stat(file); err != nil {
				stack.MissingFiles = append(stack.MissingFiles, file)
			}
		}
		stack.Command = displayCommand(composeArgs(*stack))
		out = append(out, *stack)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

// splitConfigFiles reads the label that lists a project's compose files, separated by commas.
func splitConfigFiles(label string) []string {
	var files []string
	for _, file := range strings.Split(label, ",") {
		if file = strings.TrimSpace(file); file != "" {
			files = append(files, file)
		}
	}
	return files
}

// composeArgs is `docker compose -p <project> --project-directory <dir> -f <file>...`
// followed by extra, as separate arguments: nothing here ever goes through a shell
// (CLAUDE.md), so a name with spaces or quotes in it is only an odd argument.
func composeArgs(stack protocol.DockerStack, extra ...string) []string {
	args := []string{"compose", "-p", stack.Project}
	if stack.WorkingDir != "" {
		args = append(args, "--project-directory", stack.WorkingDir)
	}
	for _, file := range stack.ConfigFiles {
		args = append(args, "-f", file)
	}
	return append(args, extra...)
}

// displayCommand writes arguments the way a person would type the command, for
// showing only: it is never run, and never parsed again.
func displayCommand(args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "docker")
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\$`&|;<>()*?[]{}!#~") {
			arg = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}

// composePlan is what an action on a stack runs, as one or two argument lists.
// "recycle" is down and then up -d, and never has -v unless volumes was asked for
// (then it deletes the stack's named volumes: the dialog says so). It is the
// course's chaos test: data in a named volume must survive a down without -v.
func composePlan(stack protocol.DockerStack, action string, volumes bool) ([][]string, error) {
	down := composeArgs(stack, "down")
	if volumes {
		down = append(down, "-v")
	}
	switch action {
	case protocol.DockerStackDown:
		return [][]string{down}, nil
	case protocol.DockerStackUp:
		return [][]string{composeArgs(stack, "up", "-d")}, nil
	case protocol.DockerStackRestart:
		return [][]string{composeArgs(stack, "restart")}, nil
	case protocol.DockerStackRecycle:
		return [][]string{down, composeArgs(stack, "up", "-d")}, nil
	}
	return nil, errors.New("action must be down, up, restart or recycle")
}

// checkStackFiles refuses to run Compose for a stack whose files are gone or were
// never recorded, and says so, instead of letting Compose fail with its own,
// longer, message.
func checkStackFiles(stack protocol.DockerStack) error {
	if len(stack.ConfigFiles) == 0 {
		return fmt.Errorf("the containers of %q do not say which compose file made them, so Docker Compose cannot act on the stack from here", stack.Project)
	}
	if len(stack.MissingFiles) > 0 {
		return fmt.Errorf("the compose file %s of the stack %q no longer exists on this host, so Docker Compose cannot act on it. "+
			"Restore the file, or stop and remove its containers one by one", stack.MissingFiles[0], stack.Project)
	}
	return nil
}

// runDockerCLI runs `docker <args>` and returns the end of its output. A variable
// so a test can replace it with something that does not need Docker.
var runDockerCLI = func(ctx context.Context, args []string) (string, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return "", errors.New("the docker command was not found on this host, and Compose stacks need it (with the Compose plugin: docker compose version)")
	}
	cmd := exec.CommandContext(ctx, docker, args...)
	// One writer for both: compose prints its progress and its errors on stderr,
	// and the person wants them in the order they happened.
	output := &tailBuffer{max: composeOutputMax}
	cmd.Stdout = output
	cmd.Stderr = output
	err = cmd.Run()
	text := strings.TrimSpace(output.String())
	if err == nil {
		return text, nil
	}
	if ctx.Err() != nil {
		return text, errors.New("docker compose took too long and was stopped: " + text)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return text, fmt.Errorf("docker compose exited with status %d: %s", exit.ExitCode(), text)
	}
	return text, err
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	return strings.ToValidUTF8(string(t.buf), "�")
}
