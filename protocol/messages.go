// Package protocol holds the JSON shapes shared by the server and the agent.
// Keep it in sync with docs/protocol.md: a change here is a protocol change.
package protocol

import (
	"encoding/json"
	"fmt"
)

// Version is checked in the hello message. Bump it when a change would make
// old agents misbehave against a new server.
const Version = 1

// EnrollRequest is the body of POST /agent/enroll.
type EnrollRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// EnrollResponse is the reply to a successful enroll. AgentSecret is sent
// exactly once; the server keeps only its hash (D-09).
type EnrollResponse struct {
	AgentID     string `json:"agent_id"`
	AgentSecret string `json:"agent_secret"`
}

// Message types on the control connection (docs/protocol.md §3).
const (
	TypeHello     = "hello"
	TypeHeartbeat = "heartbeat"
)

// Envelope wraps every control message: {"type": "...", "data": {...}}.
// Data stays raw until the type is known, so no reflection is needed:
// decode the envelope first, then decode Data into the struct for that type.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// NewMessage encodes a typed message, ready to send as one WebSocket text frame.
func NewMessage(msgType string, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode %s data: %w", msgType, err)
	}
	return json.Marshal(Envelope{Type: msgType, Data: raw})
}

// Hello is sent once, as the first message after connecting.
type Hello struct {
	Protocol     int      `json:"protocol"`
	AgentVersion string   `json:"agent_version"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Platform     string   `json:"platform"`
	Kernel       string   `json:"kernel"`
	CPUModel     string   `json:"cpu_model"`
	CPUCores     int      `json:"cpu_cores"`
	MemTotal     uint64   `json:"mem_total"`
	LANIPs       []string `json:"lan_ips"`
}

// Heartbeat is sent every 20 seconds. Sizes are in bytes.
type Heartbeat struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskTotal  uint64  `json:"disk_total"`
	UptimeSec  uint64  `json:"uptime_sec"`
}
