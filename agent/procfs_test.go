package agent

import "testing"

func TestParseCPUTimes(t *testing.T) {
	// idle(800) + iowait(50) = 850; total = 100+0+50+800+50+0+0+0 = 1000
	stat := "cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 50 0 25 400 25 0 0 0 0 0\n"
	got, err := parseCPUTimes(stat)
	if err != nil {
		t.Fatal(err)
	}
	if got.idle != 850 || got.total != 1000 {
		t.Errorf("got %+v, want idle=850 total=1000", got)
	}

	if _, err := parseCPUTimes("intr 12345\n"); err == nil {
		t.Error("expected an error for a first line that is not the cpu line")
	}
}

func TestCPUPercent(t *testing.T) {
	first := cpuTimes{idle: 850, total: 1000}
	second := cpuTimes{idle: 950, total: 1250}

	// Since boot: 150 busy ticks out of 1000.
	if got := cpuPercent(cpuTimes{}, first); got != 15 {
		t.Errorf("since boot = %v, want 15", got)
	}
	// Between the two samples: 250 ticks passed, 100 of them idle, so 150 busy.
	if got := cpuPercent(first, second); got != 60 {
		t.Errorf("between samples = %v, want 60", got)
	}
	// No time passed: must not divide by zero.
	if got := cpuPercent(first, first); got != 0 {
		t.Errorf("same sample twice = %v, want 0", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	content := "MemTotal:       16137884 kB\nMemAvailable:    8000000 kB\nHugePages_Total:       0\n"
	got := parseMeminfo(content)
	if got["MemTotal"] != 16137884*1024 {
		t.Errorf("MemTotal = %d", got["MemTotal"])
	}
	if got["MemAvailable"] != 8000000*1024 {
		t.Errorf("MemAvailable = %d", got["MemAvailable"])
	}
	if v, ok := got["HugePages_Total"]; !ok || v != 0 {
		t.Errorf("HugePages_Total = %d, %v; a line without a unit must not be multiplied", v, ok)
	}
}

func TestParseOSRelease(t *testing.T) {
	content := "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n"
	if got := parseOSRelease(content); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Errorf("got %q", got)
	}
	if got := parseOSRelease("ID=debian\n"); got != "" {
		t.Errorf("got %q, want empty when PRETTY_NAME is missing", got)
	}
}

func TestParseCPUModel(t *testing.T) {
	content := "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Core(TM) i5-12400\nprocessor\t: 1\nmodel name\t: Intel(R) Core(TM) i5-12400\n"
	if got := parseCPUModel(content); got != "Intel(R) Core(TM) i5-12400" {
		t.Errorf("got %q", got)
	}
}
