package management

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// server-stats exposes process-level gauges from the Go runtime plus host
// gauges read from /proc on Linux. CPU percentages are windowed: the handler
// keeps the previous sample and reports the delta between consecutive calls,
// so polling at the page refresh interval yields a true per-window value.

var processStartedAt = time.Now()

type cpuSample struct {
	at             time.Time
	processJiffies int64
	hostTotal      int64
	hostIdle       int64
}

// parseProcStatCPUTotal sums the aggregate "cpu " line of /proc/stat.
func parseProcStatCPUTotal(content string) int64 {
	fields := procStatCPUFields(content)
	var total int64
	for _, f := range fields {
		total += f
	}
	return total
}

// parseProcStatCPUIdle returns idle+iowait jiffies of the aggregate cpu line.
func parseProcStatCPUIdle(content string) int64 {
	fields := procStatCPUFields(content)
	if len(fields) < 5 {
		return 0
	}
	return fields[3] + fields[4]
}

func procStatCPUFields(content string) []int64 {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "cpu ") && !strings.HasPrefix(line, "cpu  ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 || parts[0] != "cpu" {
			return nil
		}
		fields := make([]int64, 0, len(parts)-1)
		for _, p := range parts[1:] {
			v, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil
			}
			fields = append(fields, v)
		}
		return fields
	}
	return nil
}

// parseProcMeminfo returns (MemTotal, MemAvailable) in bytes.
func parseProcMeminfo(content string) (int64, int64) {
	var total, avail int64
	for _, line := range strings.Split(content, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		switch parts[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	return total, avail
}

// parseProcLoadavg extracts the 1/5/15-minute load averages.
func parseProcLoadavg(content string) (float64, float64, float64) {
	parts := strings.Fields(content)
	if len(parts) < 3 {
		return 0, 0, 0
	}
	l1, _ := strconv.ParseFloat(parts[0], 64)
	l5, _ := strconv.ParseFloat(parts[1], 64)
	l15, _ := strconv.ParseFloat(parts[2], 64)
	return l1, l5, l15
}

// parseProcSelfStatCPU returns utime+stime jiffies from /proc/self/stat.
// The comm field may contain spaces and parentheses, so fields are counted
// after the last ')'.
func parseProcSelfStatCPU(content string) int64 {
	idx := strings.LastIndex(content, ")")
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(content[idx+1:])
	// fields[0] is state (field 3); utime is field 14 -> index 11, stime 15 -> 12.
	if len(fields) < 13 {
		return 0
	}
	utime, errU := strconv.ParseInt(fields[11], 10, 64)
	stime, errS := strconv.ParseInt(fields[12], 10, 64)
	if errU != nil || errS != nil {
		return 0
	}
	return utime + stime
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func countFds() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// SetRuntimeStatsProvider injects the server's live in-flight counters.
func (h *Handler) SetRuntimeStatsProvider(fn func() (activeRequests, activeWebSockets int64)) {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	h.runtimeStats = fn
}

// GetServerStats returns process and host pressure gauges.
func (h *Handler) GetServerStats(c *gin.Context) {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	var active, websockets int64
	if h.runtimeStats != nil {
		active, websockets = h.runtimeStats()
	}

	body := gin.H{
		"uptime_seconds":    int64(time.Since(processStartedAt).Seconds()),
		"goroutines":        runtime.NumGoroutine(),
		"heap_alloc_bytes":  int64(mem.HeapAlloc),
		"heap_sys_bytes":    int64(mem.HeapSys),
		"stack_inuse_bytes": int64(mem.StackInuse),
		"gc_count":          int64(mem.NumGC),
		"num_cpu":           runtime.NumCPU(),
		"active_requests":   active,
		"active_websockets": websockets,
	}

	if runtime.GOOS != "linux" {
		c.JSON(http.StatusOK, body)
		return
	}

	if fds := countFds(); fds >= 0 {
		body["num_fds"] = fds
	}

	sample := cpuSample{
		at:             time.Now(),
		processJiffies: parseProcSelfStatCPU(readFileString("/proc/self/stat")),
		hostTotal:      parseProcStatCPUTotal(readFileString("/proc/stat")),
		hostIdle:       parseProcStatCPUIdle(readFileString("/proc/stat")),
	}
	const hz = 100.0 // USER_HZ on all mainstream Linux kernels
	body["process_cpu_seconds"] = float64(sample.processJiffies) / hz

	host := gin.H{}
	l1, l5, l15 := parseProcLoadavg(readFileString("/proc/loadavg"))
	host["load1"] = l1
	host["load5"] = l5
	host["load15"] = l15
	total, avail := parseProcMeminfo(readFileString("/proc/meminfo"))
	host["mem_total_bytes"] = total
	host["mem_available_bytes"] = avail
	host["num_cpu"] = runtime.NumCPU()

	if prev := h.lastCPUSample; prev != nil {
		elapsed := sample.at.Sub(prev.at).Seconds()
		if elapsed > 0 {
			procDelta := float64(sample.processJiffies-prev.processJiffies) / hz
			body["process_cpu_percent"] = clampPercent(procDelta / elapsed * 100)
			hostDelta := sample.hostTotal - prev.hostTotal
			idleDelta := sample.hostIdle - prev.hostIdle
			if hostDelta > 0 {
				host["cpu_percent"] = clampPercent(float64(hostDelta-idleDelta) / float64(hostDelta) * 100)
			}
		}
	}
	h.lastCPUSample = &sample
	body["host"] = host

	c.JSON(http.StatusOK, body)
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
