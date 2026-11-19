// Package agent is the code that runs on each managed host.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"headnode/protocol"
)

// Enroll registers this host with the server and saves the credentials to
// configPath.
func Enroll(serverURL, token, configPath string) error {
	// Check first: enrolling twice would leave a second, unused agent on the
	// server and we would have nowhere to keep its secret.
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("%s already exists, this host is already enrolled (delete the file to enroll again)", configPath)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	body, err := json.Marshal(protocol.EnrollRequest{
		Token:    token,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	// The default http.Client has no timeout, so a dead server would hang us forever.
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(strings.TrimRight(serverURL, "/")+"/agent/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("contact server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e) // best effort: the status code is enough if this fails
		return fmt.Errorf("server rejected enrollment (HTTP %d): %s", resp.StatusCode, e.Error)
	}

	var out protocol.EnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode server reply: %w", err)
	}

	return saveConfig(configPath, Config{
		Server:      strings.TrimRight(serverURL, "/"),
		AgentID:     out.AgentID,
		AgentSecret: out.AgentSecret,
	})
}
