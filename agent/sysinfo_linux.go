package agent

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"headnode/protocol"
)

// readHostInfo gathers the facts for the hello message.
func readHostInfo() (protocol.Hello, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return protocol.Hello{}, fmt.Errorf("read hostname: %w", err)
	}
	hello := protocol.Hello{
		Protocol:     protocol.Version,
		AgentVersion: Version,
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		CPUCores:     runtime.NumCPU(),
		LANIPs:       lanIPs(),
	}

	// The rest is best effort: a missing file leaves its field empty instead of
	// stopping the agent from connecting at all (some minimal containers have
	// no /etc/os-release, for example).
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		hello.Platform = parseOSRelease(string(b))
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		hello.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		hello.CPUModel = parseCPUModel(string(b))
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		hello.MemTotal = parseMeminfo(string(b))["MemTotal"]
	}
	return hello, nil
}

// readHeartbeat gathers the numbers for one heartbeat. CPU needs two samples,
// so the caller passes the previous one and gets the new one back to use next time.
func readHeartbeat(prev cpuTimes) (protocol.Heartbeat, cpuTimes, error) {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return protocol.Heartbeat{}, prev, fmt.Errorf("read /proc/stat: %w", err)
	}
	cur, err := parseCPUTimes(string(stat))
	if err != nil {
		return protocol.Heartbeat{}, prev, err
	}

	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return protocol.Heartbeat{}, prev, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	mem := parseMeminfo(string(meminfo))
	// MemAvailable, not MemFree: Linux uses spare RAM as file cache, and that
	// part is given back to programs on demand, so it does not count as "used".
	memUsed := mem["MemTotal"] - mem["MemAvailable"]
	if mem["MemAvailable"] > mem["MemTotal"] {
		memUsed = 0
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err != nil {
		return protocol.Heartbeat{}, prev, fmt.Errorf("statfs /: %w", err)
	}
	// The field types differ between architectures, hence the conversions.
	blockSize := uint64(fs.Bsize)
	diskTotal := fs.Blocks * blockSize
	diskUsed := (fs.Blocks - fs.Bfree) * blockSize

	uptime, err := readUptime()
	if err != nil {
		return protocol.Heartbeat{}, prev, err
	}

	hb := protocol.Heartbeat{
		CPUPercent: float64(int(cpuPercent(prev, cur)*10+0.5)) / 10, // one decimal is plenty
		MemUsed:    memUsed,
		MemTotal:   mem["MemTotal"],
		DiskUsed:   diskUsed,
		DiskTotal:  diskTotal,
		UptimeSec:  uptime,
	}
	return hb, cur, nil
}

// readUptime reads the first number of /proc/uptime: seconds since boot.
func readUptime() (uint64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("read /proc/uptime: %w", err)
	}
	first, _, _ := strings.Cut(string(b), " ")
	seconds, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/uptime: %w", err)
	}
	return uint64(seconds), nil
}
