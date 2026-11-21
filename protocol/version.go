package protocol

import (
	"strconv"
	"strings"
)

// Comparing agent versions (M10, D-66, D-67). The server uses it to say which hosts
// are behind, and the agent's `update` command uses it to see whether the server has a
// newer one, so there is one rule for both.

// ParseVersion reads "0.9.2" as [0 9 2]. Anything else (an empty text, a letter, a
// negative number, a leading zero) is not a version.
func ParseVersion(text string) ([]int, bool) {
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

// VersionOlder says whether version a is older than b, comparing number by number,
// so 0.9.2 is older than 0.10.0 (as text it would come after). A missing number
// counts as 0, so 1.2 and 1.2.0 are the same. If either is not a version the answer
// is false: a host whose version is not known is not told to update.
func VersionOlder(a, b string) bool {
	x, okA := ParseVersion(a)
	y, okB := ParseVersion(b)
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
