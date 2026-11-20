//go:build windows

package agent

import (
	"testing"
	"time"
)

// These run on the Windows laptop and read the real system: they prove the
// kernel32 calls and the registry reads work, which no stub could.

func TestWindowsHostInfo(t *testing.T) {
	hello, err := readHostInfo()
	if err != nil {
		t.Fatal(err)
	}
	if hello.OS != "windows" || hello.Hostname == "" || hello.Arch == "" || hello.CPUCores < 1 || hello.AgentVersion != Version {
		t.Errorf("hello = %+v", hello)
	}
	if hello.Platform == "" || hello.Platform == "Windows" {
		t.Errorf("platform = %q, want the real product name from the registry", hello.Platform)
	}
	if hello.Kernel == "" || hello.CPUModel == "" {
		t.Errorf("kernel = %q, cpu model = %q, want both from the system", hello.Kernel, hello.CPUModel)
	}
	if hello.MemTotal < 512<<20 {
		t.Errorf("MemTotal = %d, want at least 512 MB", hello.MemTotal)
	}
	if hello.LANIPs == nil {
		t.Error("LANIPs is nil: it must be an empty list so the JSON is [] and not null")
	}
}

func TestWindowsHeartbeat(t *testing.T) {
	first, cur, err := readHeartbeat(cpuTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if first.MemTotal == 0 || first.MemUsed > first.MemTotal {
		t.Errorf("memory: used %d of %d", first.MemUsed, first.MemTotal)
	}
	if first.DiskTotal == 0 || first.DiskUsed > first.DiskTotal {
		t.Errorf("disk: used %d of %d", first.DiskUsed, first.DiskTotal)
	}
	if first.UptimeSec == 0 {
		t.Error("uptime is 0")
	}
	if first.CPUPercent < 0 || first.CPUPercent > 100 {
		t.Errorf("cpu = %v, want 0-100", first.CPUPercent)
	}

	// A second sample, a moment later, works from the counters of the first.
	time.Sleep(300 * time.Millisecond)
	second, next, err := readHeartbeat(cur)
	if err != nil {
		t.Fatal(err)
	}
	if next.total <= cur.total {
		t.Errorf("the CPU counters did not move: %d then %d", cur.total, next.total)
	}
	if second.CPUPercent < 0 || second.CPUPercent > 100 {
		t.Errorf("cpu = %v, want 0-100", second.CPUPercent)
	}
}
