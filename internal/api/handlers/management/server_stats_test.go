package management

import (
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Feature: GET /v0/management/server-stats
//
//   Process-level gauges come from the Go runtime and the server's own
//   counters; host-level gauges are read from /proc on Linux and degrade to
//   omitted fields elsewhere. CPU percentages are windowed: the server keeps
//   the previous sample and reports the delta between consecutive calls.

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
	// Then the response carries process fields and a host object on linux
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
	if runtime.GOOS == "linux" {
		if !hasHost {
			t.Fatalf("host section missing on linux: %v", body)
		}
		for _, field := range []string{"load1", "load5", "load15", "mem_total_bytes", "mem_available_bytes"} {
			if _, ok := host[field]; !ok {
				t.Fatalf("missing host field %q: %v", field, host)
			}
		}
		if _, ok := body["num_fds"]; !ok {
			t.Fatalf("num_fds missing on linux: %v", body)
		}
	} else if hasHost {
		t.Fatalf("host section should be omitted on %s: %v", runtime.GOOS, host)
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

func TestServerStats_CPUWindow(t *testing.T) {
	// Given two consecutive calls, process_cpu_seconds never decreases
	// and the second call reports windowed cpu percentages on linux
	if runtime.GOOS != "linux" {
		t.Skip("cpu window metrics are linux-only")
	}
	r := setupServerStatsRouter(t, 0, 0)

	_, first := doReq(t, r, "GET", "/server-stats", "")
	cpu1, _ := first["process_cpu_seconds"].(float64)

	// burn a little CPU so the window delta is observable
	deadline := time.Now().Add(20 * time.Millisecond)
	for time.Now().Before(deadline) {
	}

	_, second := doReq(t, r, "GET", "/server-stats", "")
	cpu2, _ := second["process_cpu_seconds"].(float64)
	if cpu2 < cpu1 {
		t.Fatalf("process_cpu_seconds decreased: %v -> %v", cpu1, cpu2)
	}
	pct, ok := second["process_cpu_percent"].(float64)
	if !ok || pct < 0 {
		t.Fatalf("process_cpu_percent = %v (ok=%v), want non-negative number", second["process_cpu_percent"], ok)
	}
	host, _ := second["host"].(map[string]any)
	if _, ok := host["cpu_percent"].(float64); !ok {
		t.Fatalf("host.cpu_percent missing on second call: %v", host)
	}
}

func TestServerStats_ProcParsing(t *testing.T) {
	// Given fixture contents, parsers extract jiffies/mem/load; malformed input yields zeros
	stat := "cpu  100 0 200 1600 50 0 30 0 0 0\ncpu0 50 0 100 800 25 0 15 0 0 0\n"
	jiffies := parseProcStatCPUTotal(stat)
	if jiffies != 1980 {
		t.Fatalf("cpu total jiffies = %v, want 1980", jiffies)
	}
	if idle := parseProcStatCPUIdle(stat); idle != 1650 {
		t.Fatalf("cpu idle jiffies = %v, want 1650", idle)
	}
	if parseProcStatCPUTotal("garbage") != 0 {
		t.Fatal("malformed stat should yield 0")
	}

	meminfo := "MemTotal:       16384000 kB\nMemFree:         1024000 kB\nMemAvailable:    8192000 kB\n"
	total, avail := parseProcMeminfo(meminfo)
	if total != 16384000*1024 || avail != 8192000*1024 {
		t.Fatalf("meminfo = (%v, %v)", total, avail)
	}
	if total2, _ := parseProcMeminfo("garbage"); total2 != 0 {
		t.Fatal("malformed meminfo should yield 0")
	}

	l1, l5, l15 := parseProcLoadavg("0.42 1.23 4.56 1/234 5678")
	if l1 != 0.42 || l5 != 1.23 || l15 != 4.56 {
		t.Fatalf("loadavg = (%v, %v, %v)", l1, l5, l15)
	}

	self := "12345 (cli-proxy-api) S 1 12345 12345 0 -1 4194304 1000 0 0 0 14 6 0 0 20 0 10 0 999"
	if jiffies := parseProcSelfStatCPU(self); jiffies != 20 {
		t.Fatalf("self stat cpu = %v, want 20 (utime 14 + stime 6)", jiffies)
	}
}
