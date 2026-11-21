package protocol

import "testing"

func TestVersionOlder(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.9.2", "0.10.0", true}, // as text "0.9.2" comes after "0.10.0": numbers are compared, not letters
		{"0.10.0", "0.9.2", false},
		{"0.9.2", "0.9.2", false},
		{"1.2", "1.2.0", false}, // a missing number is 0
		{"1.2", "1.2.1", true},
		{"9.9.9", "10.0.0", true},
		{"", "1.0.0", false}, // a host whose version is not known is not told to update
		{"1.0.0", "", false},
		{"0.9.x", "1.0.0", false},
		{"v1.0.0", "1.0.1", false},
		{"1..0", "1.0.1", false},
		{"-1.0", "1.0", false},
		{"01.0", "1.0", false}, // no leading zeros: "01" is not how a version is written
	}
	for _, tc := range tests {
		if got := VersionOlder(tc.a, tc.b); got != tc.want {
			t.Errorf("VersionOlder(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
