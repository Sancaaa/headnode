//go:build windows

package agent

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"headnode/protocol"
)

// System information on Windows (D-47), the counterpart of sysinfo_linux.go.
// There is no /proc: the numbers come from a few kernel32 functions and from the
// registry, which is where Windows keeps its own version and processor name.
//
// kernel32 is loaded from the system folder only (NewLazySystemDLL), so a DLL of
// the same name next to the program or in the current folder is never picked up.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// memoryStatusEx is the MEMORYSTATUSEX structure of the Windows API.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// filetime is a FILETIME: a count of 100-nanosecond steps, split in two halves.
type filetime struct {
	Low  uint32
	High uint32
}

func (f filetime) ticks() uint64 { return uint64(f.High)<<32 | uint64(f.Low) }

func readMemory() (memoryStatusEx, error) {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); ok == 0 {
		return status, fmt.Errorf("GlobalMemoryStatusEx: %w", err)
	}
	return status, nil
}

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

	// Best effort, like on Linux: a value that cannot be read leaves its field
	// empty instead of stopping the agent from connecting.
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		defer key.Close()
		hello.Platform = windowsPlatform(key)
		hello.Kernel = windowsKernel(key)
	}
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		defer key.Close()
		if name, _, err := key.GetStringValue("ProcessorNameString"); err == nil {
			hello.CPUModel = strings.Join(strings.Fields(name), " ") // the registry pads it with spaces
		}
	}
	if mem, err := readMemory(); err == nil {
		hello.MemTotal = mem.TotalPhys
	}
	return hello, nil
}

// windowsPlatform is the name shown on the host page: "Windows 11 Pro 23H2".
func windowsPlatform(key registry.Key) string {
	name, _, err := key.GetStringValue("ProductName")
	if err != nil {
		return "Windows"
	}
	// Windows 11 still says "Windows 10" in ProductName. The build number tells
	// them apart: Windows 11 starts at build 22000.
	if build, _, err := key.GetStringValue("CurrentBuild"); err == nil {
		var n int
		if _, err := fmt.Sscanf(build, "%d", &n); err == nil && n >= 22000 {
			name = strings.Replace(name, "Windows 10", "Windows 11", 1)
		}
	}
	if version, _, err := key.GetStringValue("DisplayVersion"); err == nil && version != "" {
		name += " " + version
	}
	return name
}

// windowsKernel is "10.0.22631", the version of the operating system itself.
func windowsKernel(key registry.Key) string {
	major, _, err1 := key.GetIntegerValue("CurrentMajorVersionNumber")
	minor, _, err2 := key.GetIntegerValue("CurrentMinorVersionNumber")
	build, _, err3 := key.GetStringValue("CurrentBuild")
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%s", major, minor, build)
}

// readHeartbeat gathers the numbers for one heartbeat. CPU needs two samples,
// so the caller passes the previous one and gets the new one back to use next time.
func readHeartbeat(prev cpuTimes) (protocol.Heartbeat, cpuTimes, error) {
	var idle, kernel, user filetime
	if ok, _, err := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)),
	); ok == 0 {
		return protocol.Heartbeat{}, prev, fmt.Errorf("GetSystemTimes: %w", err)
	}
	// Windows counts kernel time including the time spent idle, so the total
	// is kernel + user, and the idle part is reported separately.
	cur := cpuTimes{idle: idle.ticks(), total: kernel.ticks() + user.ticks()}

	mem, err := readMemory()
	if err != nil {
		return protocol.Heartbeat{}, prev, err
	}
	// "Available" is memory programs can have right now (free plus the cache that
	// is handed back on demand), so used is the rest, like MemAvailable on Linux.
	memUsed := uint64(0)
	if mem.AvailPhys <= mem.TotalPhys {
		memUsed = mem.TotalPhys - mem.AvailPhys
	}

	// The disk the system is installed on (almost always C:).
	root := os.Getenv("SystemDrive")
	if root == "" {
		root = "C:"
	}
	rootPtr, err := windows.UTF16PtrFromString(root + `\`)
	if err != nil {
		return protocol.Heartbeat{}, prev, err
	}
	var freeToCaller, total, free uint64
	if ok, _, err := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&freeToCaller)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)),
	); ok == 0 {
		return protocol.Heartbeat{}, prev, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", root, err)
	}
	diskUsed := uint64(0)
	if free <= total {
		diskUsed = total - free
	}

	// Milliseconds since boot.
	millis, _, _ := procGetTickCount64.Call()

	hb := protocol.Heartbeat{
		CPUPercent: float64(int(cpuPercent(prev, cur)*10+0.5)) / 10, // one decimal is plenty
		MemUsed:    memUsed,
		MemTotal:   mem.TotalPhys,
		DiskUsed:   diskUsed,
		DiskTotal:  total,
		UptimeSec:  uint64(millis) / 1000,
	}
	return hb, cur, nil
}
