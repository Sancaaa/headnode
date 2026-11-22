package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestParseChecksum(t *testing.T) {
	good := strings.Repeat("a", 64)
	upper := strings.Repeat("B", 64)
	sums := good + "  headnode-agent-linux-amd64\n" +
		upper + " *headnode-agent-windows-amd64.exe\r\n"

	if got, err := parseChecksum(sums, "headnode-agent-linux-amd64"); err != nil || got != good {
		t.Errorf("linux-amd64: got %q (%v), want %q", got, err, good)
	}
	if got, err := parseChecksum(sums, "headnode-agent-windows-amd64.exe"); err != nil || got != strings.ToLower(upper) {
		t.Errorf("windows-amd64.exe: got %q (%v), want %q", got, err, strings.ToLower(upper))
	}
	if _, err := parseChecksum(sums, "headnode-agent-linux-arm64"); err == nil {
		t.Error("missing entry should fail closed")
	}
	if _, err := parseChecksum("not-hex  headnode-agent-linux-amd64\n", "headnode-agent-linux-amd64"); err == nil {
		t.Error("invalid hex should fail closed")
	}
	if _, err := parseChecksum(strings.Repeat("z", 64)+"  headnode-agent-linux-amd64\n", "headnode-agent-linux-amd64"); err == nil {
		t.Error("non-hex characters should fail closed")
	}
	dup := good + "  headnode-agent-linux-amd64\n" + good + "  headnode-agent-linux-amd64\n"
	if _, err := parseChecksum(dup, "headnode-agent-linux-amd64"); err == nil {
		t.Error("duplicate entry should fail closed")
	}
}

func TestDownloadVerified(t *testing.T) {
	binary := []byte("fake agent binary payload")
	validSum := sha256Hex(binary)
	wrongSum := sha256Hex([]byte("tampered payload"))

	var sumsBody string
	var sumsStatus int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download/SHA256SUMS":
			w.WriteHeader(sumsStatus)
			w.Write([]byte(sumsBody))
		case "/download/headnode-agent-linux-amd64":
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Happy path: matching digest succeeds and writes the file.
	sumsStatus = http.StatusOK
	sumsBody = validSum + "  headnode-agent-linux-amd64\n"
	dst := filepath.Join(t.TempDir(), "agent.new")
	if err := downloadVerified(srv.URL, "headnode-agent-linux-amd64", dst); err != nil {
		t.Fatalf("matching checksum failed: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != string(binary) {
		t.Errorf("downloaded file = %q (%v), want %q", got, err, binary)
	}

	// Missing SHA256SUMS (404): fails closed, nothing written.
	sumsStatus = http.StatusNotFound
	sumsBody = ""
	dstMissing := filepath.Join(t.TempDir(), "agent.missing")
	if err := downloadVerified(srv.URL, "headnode-agent-linux-amd64", dstMissing); err == nil {
		t.Error("missing SHA256SUMS should fail closed")
	}
	if _, err := os.Lstat(dstMissing); err == nil {
		t.Error("missing SHA256SUMS should not leave a file behind")
	}

	// SHA256SUMS has no entry for target binary: fails closed.
	sumsStatus = http.StatusOK
	sumsBody = validSum + "  headnode-agent-linux-arm64\n"
	dstNoEntry := filepath.Join(t.TempDir(), "agent.noentry")
	if err := downloadVerified(srv.URL, "headnode-agent-linux-amd64", dstNoEntry); err == nil {
		t.Error("SHA256SUMS without entry for target binary should fail closed")
	}
	if _, err := os.Lstat(dstNoEntry); err == nil {
		t.Error("missing entry should not leave a file behind")
	}

	// Mismatched checksum: fails closed AND removes the unverified download.
	sumsStatus = http.StatusOK
	sumsBody = wrongSum + "  headnode-agent-linux-amd64\n"
	dstBad := filepath.Join(t.TempDir(), "agent.bad")
	if err := downloadVerified(srv.URL, "headnode-agent-linux-amd64", dstBad); err == nil {
		t.Error("mismatched checksum should fail closed")
	}
	if _, err := os.Lstat(dstBad); err == nil {
		t.Error("mismatched download was not removed")
	}
}
