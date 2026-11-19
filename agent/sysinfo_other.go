//go:build !linux

package agent

import (
	"errors"

	"headnode/protocol"
)

// Only Linux is implemented (D-12). These stubs exist so the agent package
// still compiles on the Windows laptop, which means `go vet ./...` and
// `go test ./...` work there. Running the agent on Windows fails with a clear error.
var errUnsupported = errors.New("the agent only supports Linux for now")

func readHostInfo() (protocol.Hello, error) {
	return protocol.Hello{}, errUnsupported
}

func readHeartbeat(prev cpuTimes) (protocol.Heartbeat, cpuTimes, error) {
	return protocol.Heartbeat{}, prev, errUnsupported
}
