//go:build !linux && !windows

package agent

import "headnode/protocol"

// Only Linux and Windows are implemented (D-12, D-47). These stubs keep the
// agent package compiling on any other system (a Mac, say), so `go vet ./...`
// still works there. Running the agent on one fails with a clear error.
// (errUnsupported is in shell_other.go.)

func readHostInfo() (protocol.Hello, error) {
	return protocol.Hello{}, errUnsupported
}

func readHeartbeat(prev cpuTimes) (protocol.Heartbeat, cpuTimes, error) {
	return protocol.Heartbeat{}, prev, errUnsupported
}
