package management

import (
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// Feature: GET /v0/management/server-stats
//
//   Process gauges come from the Go runtime and the server's own counters;
//   host gauges are collected through gopsutil on every supported platform.
//   Rates (disk IO, network) and CPU percentages are windowed: the handler
//   keeps the previous sample and reports the delta between calls.

func setupServerStatsRouter(t *testing.T, active, websockets int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{}
	h.SetRuntimeStatsProvider(func() (int64, int64) { return active, websockets })
	r.GET("/server-stats", h.GetServerStats)
	return r
}

func TestServerStats_Shape(t *testing.T) {
	// Given a handler with an injected runtime-stats provider
	// When GET /server-stats is called
	// Then process fields, host gauges, and disk rows are present
	r := setupServerStatsRouter(t, 0, 0)
	rec, body := doReq(t, r, "GET", "/server-stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{
		"uptime_seconds", "goroutines", "heap_alloc_bytes", "heap_sys_bytes",
		"stack_inuse_bytes", "gc_count", "num_cpu", "active_requests", "active_websockets",
	} {
		if _, ok := body[field]; !ok {
			t.Fatalf("missing process field %q: %v", field, body)
		}
	}
	if body["num_cpu"] != float64(runtime.NumCPU()) {
		t.Fatalf("num_cpu = %v, want %d", body["num_cpu"], runtime.NumCPU())
	}

	host, hasHost := body["host"].(map[string]any)
	if !hasHost {
		t.Fatalf("host section missing: %v", body)
	}
	for _, field := range []string{"num_cpu", "mem_total_bytes", "mem_available_bytes", "mem_used_percent"} {
		if _, ok := host[field]; !ok {
			t.Fatalf("missing host field %q: %v", field, host)
		}
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		for _, field := range []string{"load1", "load5", "load15"} {
			if _, ok := host[field]; !ok {
				t.Fatalf("missing load field %q on %s: %v", field, runtime.GOOS, host)
			}
		}
	}

	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		disks, ok := body["disks"].([]any)
		if !ok || len(disks) == 0 {
			t.Fatalf("disks missing on %s: %v", runtime.GOOS, body)
		}
		first, _ := disks[0].(map[string]any)
		for _, field := range []string{"mount", "device", "total_bytes", "avail_bytes", "used_percent"} {
			if _, ok := first[field]; !ok {
				t.Fatalf("disk row missing field %q: %v", field, first)
			}
		}
	}
}

func TestServerStats_DiskDedupe(t *testing.T) {
	// Given two partitions on the same device, only one row is emitted
	parts := []disk.PartitionStat{
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "/dev/sda1", Mountpoint: "/boot", Fstype: "ext4"},
		{Device: "/dev/sdb1", Mountpoint: "/data", Fstype: "xfs"},
		{Device: "/dev/sdc1", Mountpoint: "/gone", Fstype: "ext4"},
	}
	usage := func(mount string) (*disk.UsageStat, error) {
		switch mount {
		case "/", "/boot":
			return &disk.UsageStat{Path: mount, Total: 1000, Free: 400, UsedPercent: 60}, nil
		case "/data":
			return &disk.UsageStat{Path: mount, Total: 500, Free: 100, UsedPercent: 80}, nil
		default:
			return nil, errors.New("unreadable")
		}
	}
	rows := collectDiskStats(parts, usage)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (same-device dedupe + unreadable skipped): %v", len(rows), rows)
	}
	if rows[0]["used_percent"] != float64(60) {
		t.Fatalf("used_percent = %v, want 60", rows[0]["used_percent"])
	}
	if rows[0]["device"] != "/dev/sda1" {
		t.Fatalf("device = %v, want /dev/sda1", rows[0]["device"])
	}
}

func TestServerStats_RuntimeCounters(t *testing.T) {
	// Given the injected provider returns active_requests=7, active_websockets=3
	// Then the response echoes exactly those values
	r := setupServerStatsRouter(t, 7, 3)
	rec, body := doReq(t, r, "GET", "/server-stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["active_requests"] != float64(7) {
		t.Fatalf("active_requests = %v, want 7", body["active_requests"])
	}
	if body["active_websockets"] != float64(3) {
		t.Fatalf("active_websockets = %v, want 3", body["active_websockets"])
	}
}

func TestServerStats_WindowRates(t *testing.T) {
	// Given injected gopsutil counters that advance between two calls
	// Then the second call reports windowed rates and cpu percentages
	var call int
	origCPU, origDisk, origNet := statsCPUTimes, statsDiskIOCounters, statsNetIOCounters
	t.Cleanup(func() {
		statsCPUTimes, statsDiskIOCounters, statsNetIOCounters = origCPU, origDisk, origNet
	})
	statsCPUTimes = func(percpu bool) ([]cpu.TimesStat, error) {
		base := float64(call) * 10
		return []cpu.TimesStat{{CPU: "cpu-total", User: base, Idle: base, Iowait: 0}}, nil
	}
	statsDiskIOCounters = func(names ...string) (map[string]disk.IOCountersStat, error) {
		return map[string]disk.IOCountersStat{
			"sda": {Name: "sda", ReadBytes: uint64(call) * 1000, WriteBytes: uint64(call) * 2000},
		}, nil
	}
	statsNetIOCounters = func(pernic bool) ([]gnet.IOCountersStat, error) {
		return []gnet.IOCountersStat{{BytesRecv: uint64(call) * 3000, BytesSent: uint64(call) * 4000}}, nil
	}

	r := setupServerStatsRouter(t, 0, 0)

	call = 1
	_, first := doReq(t, r, "GET", "/server-stats", "")
	if _, ok := first["disk_io"]; ok {
		t.Fatalf("first call must not report windowed rates: %v", first)
	}

	call = 2
	time.Sleep(50 * time.Millisecond)
	_, second := doReq(t, r, "GET", "/server-stats", "")

	diskIO, ok := second["disk_io"].(map[string]any)
	if !ok {
		t.Fatalf("disk_io missing on second call: %v", second)
	}
	if v, _ := diskIO["read_bytes_per_sec"].(float64); v <= 0 {
		t.Fatalf("read_bytes_per_sec = %v, want > 0", v)
	}
	if v, _ := diskIO["write_bytes_per_sec"].(float64); v <= 0 {
		t.Fatalf("write_bytes_per_sec = %v, want > 0", v)
	}

	network, ok := second["network"].(map[string]any)
	if !ok {
		t.Fatalf("network missing on second call: %v", second)
	}
	if v, _ := network["rx_bytes_per_sec"].(float64); v <= 0 {
		t.Fatalf("rx_bytes_per_sec = %v, want > 0", v)
	}

	host, _ := second["host"].(map[string]any)
	if pct, ok := host["cpu_percent"].(float64); !ok || pct <= 0 || pct > 100 {
		// busy delta == total delta in the fake (idle == user == base),
		// so cpu percent must sit in (0, 100]
		t.Fatalf("host.cpu_percent = %v (ok=%v), want in (0,100]", pct, ok)
	}
}

func TestServerStats_CPUWindowLive(t *testing.T) {
	// With real gopsutil collectors, a second call reports a sane cpu percent
	r := setupServerStatsRouter(t, 0, 0)

	_, first := doReq(t, r, "GET", "/server-stats", "")
	if _, ok := first["process_cpu_seconds"].(float64); !ok {
		t.Fatalf("process_cpu_seconds missing: %v", first)
	}

	_, second := doReq(t, r, "GET", "/server-stats", "")
	pct, ok := second["process_cpu_percent"].(float64)
	if !ok || pct < 0 {
		t.Fatalf("process_cpu_percent = %v (ok=%v), want non-negative number", second["process_cpu_percent"], ok)
	}
	if _, ok := second["host"].(map[string]any)["cpu_percent"].(float64); !ok {
		t.Fatalf("host.cpu_percent missing on second call: %v", second["host"])
	}
}
