package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// Pure parsers for Linux /proc files. They take the file's text, not a path,
// so they can be tested anywhere (the Windows laptop included). The code that
// actually reads the files is in sysinfo_linux.go (D-11, D-18).

// cpuTimes is a snapshot of the counters in the first line of /proc/stat.
// The kernel counts time since boot in "ticks"; one snapshot alone says
// nothing about the current load, two snapshots do.
type cpuTimes struct {
	idle  uint64
	total uint64
}

// parseCPUTimes reads the aggregate "cpu" line of /proc/stat:
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
//
// guest and guest_nice are left out because the kernel already counts them
// inside user and nice.
func parseCPUTimes(procStat string) (cpuTimes, error) {
	line, _, _ := strings.Cut(procStat, "\n")
	fields := strings.Fields(line)
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("unexpected first line of /proc/stat: %q", line)
	}

	var t cpuTimes
	for i, f := range fields[1:9] { // user nice system idle iowait irq softirq steal
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuTimes{}, fmt.Errorf("parse /proc/stat field %d: %w", i+1, err)
		}
		t.total += n
		if i == 3 || i == 4 { // idle and iowait: the CPU was not doing work
			t.idle += n
		}
	}
	return t, nil
}

// cpuPercent is how busy the CPU was between two snapshots, 0-100.
// With prev = cpuTimes{} it gives the average since boot, which is what the
// very first heartbeat reports.
func cpuPercent(prev, cur cpuTimes) float64 {
	if cur.total <= prev.total {
		return 0 // no time has passed (or the counters went backwards)
	}
	totalDelta := cur.total - prev.total
	idleDelta := cur.idle - prev.idle
	return 100 * float64(totalDelta-idleDelta) / float64(totalDelta)
}

// parseMeminfo turns /proc/meminfo into key -> bytes ("MemTotal: 16137884 kB").
func parseMeminfo(content string) map[string]uint64 {
	values := make(map[string]uint64)
	for _, line := range strings.Split(content, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" {
			n *= 1024
		}
		values[key] = n
	}
	return values
}

// parseOSRelease returns PRETTY_NAME from /etc/os-release, for example
// "Debian GNU/Linux 12 (bookworm)", or "" if the line is missing.
func parseOSRelease(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, `"'`)
		}
	}
	return ""
}

// parseCPUModel returns the first "model name" in /proc/cpuinfo, or "" if
// there is none (some ARM kernels do not print it).
func parseCPUModel(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "model name") {
			_, value, _ := strings.Cut(line, ":")
			return strings.TrimSpace(value)
		}
	}
	return ""
}
