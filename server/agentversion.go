package server

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Which agents are behind (M10, D-66). The build scripts write the version of the
// agents they build to VERSION, next to the programs. The server compares it with
// the version each agent sent in its hello, and marks the older ones, so the
// dashboard can say "update available". It only says it: the update is run by hand
// on each machine.

// latestAgentVersion is the version in the agents folder's VERSION file, or "" if
// there is none (no agents folder, no file, or not a version): then nothing is
// marked outdated, which is not an error. It is read every time it is needed, not
// once at start, because the programs can be rebuilt without restarting the server
// (D-40) and this must follow them.
func (s *Server) latestAgentVersion() string {
	if s.agentDir == "" {
		return ""
	}
	file, err := os.Open(filepath.Join(s.agentDir, "VERSION"))
	if err != nil {
		return ""
	}
	defer file.Close()
	text, err := io.ReadAll(io.LimitReader(file, 64)) // a version is a few characters
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(text))
	if _, ok := parseVersion(version); !ok {
		return ""
	}
	return version
}

// parseVersion reads "0.9.2" as [0 9 2]. Anything else (an empty text, a letter, a
// negative number) is not a version.
func parseVersion(text string) ([]int, bool) {
	if text == "" {
		return nil, false
	}
	var numbers []int
	for _, part := range strings.Split(text, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || part != strconv.Itoa(n) {
			return nil, false
		}
		numbers = append(numbers, n)
	}
	return numbers, true
}

// versionOlder says whether version a is older than b, comparing number by number,
// so 0.9.2 is older than 0.10.0 (as text it would come after). A missing number
// counts as 0, so 1.2 and 1.2.0 are the same. If either is not a version the answer
// is false: a host whose version is not known is not told to update.
func versionOlder(a, b string) bool {
	x, okA := parseVersion(a)
	y, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := 0; i < max(len(x), len(y)); i++ {
		var xi, yi int
		if i < len(x) {
			xi = x[i]
		}
		if i < len(y) {
			yi = y[i]
		}
		if xi != yi {
			return xi < yi
		}
	}
	return false
}
