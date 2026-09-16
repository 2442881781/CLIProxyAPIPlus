package management

import (
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// server-stats exposes process-level gauges from the Go runtime plus host
// gauges collected through gopsutil on every supported platform. Rates and
// CPU percentages are windowed: the handler keeps the previous sample and
// reports the delta between consecutive calls, so polling at the page
// refresh interval yields a true per-window value.

var processStartedAt = time.Now()

// Indirections over gopsutil so tests can inject fixtures.
var (
	statsCPUTimes       = cpu.Times
	statsVirtualMemory  = mem.VirtualMemory
	statsSwapMemory     = mem.SwapMemory
	statsLoadAvg        = load.Avg
	statsDiskPartitions = disk.Partitions
	statsDiskUsage      = disk.Usage
	statsDiskIOCounters = disk.IOCounters
	statsNetIOCounters  = gnet.IOCounters
	statsNewProcess     = process.NewProcess
)

// statsSample holds cumulative counters; windowed rates are computed from
// the delta against the previous sample kept on the handler.
type statsSample struct {
	at              time.Time
	processCPU      float64 // seconds
	processCPUValid bool
	hostBusy        float64 // seconds
	hostTotal       float64 // seconds
	hostCPUValid    bool
	diskRead        uint64 // bytes
	diskWrite       uint64 // bytes
	netRecv         uint64 // bytes
	netSent         uint64 // bytes
}

// collectDiskStats emits one row per physical partition device, skipping
// duplicates and partitions whose usage cannot be read.
func collectDiskStats(parts []disk.PartitionStat, usage func(string) (*disk.UsageStat, error)) []gin.H {
	seen := make(map[string]bool)
	rows := make([]gin.H, 0, len(parts))
	for _, part := range parts {
		if isDiskImageMount(part) {
			continue
		}
		if seen[part.Device] {
			continue
		}
		u, err := usage(part.Mountpoint)
		if err != nil || u == nil || u.Total <= 0 {
			continue
		}
		seen[part.Device] = true
		rows = append(rows, gin.H{
			"mount":        part.Mountpoint,
			"device":       part.Device,
			"fstype":       part.Fstype,
			"total_bytes":  int64(u.Total),
			"avail_bytes":  int64(u.Free),
			"used_percent": clampPercent(u.UsedPercent),
		})
	}
	return rows
}

// isDiskImageMount excludes read-only package images such as Snap mounts.
// SquashFS images and loop devices normally report 100% usage by design and
// are not writable filesystems that can run out of free space.
func isDiskImageMount(part disk.PartitionStat) bool {
	return strings.EqualFold(part.Fstype, "squashfs") ||
		strings.HasPrefix(part.Device, "/dev/loop") ||
		part.Mountpoint == "/snap" || strings.HasPrefix(part.Mountpoint, "/snap/")
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

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	var active, websockets int64
	if h.runtimeStats != nil {
		active, websockets = h.runtimeStats()
	}

	body := gin.H{
		"uptime_seconds":    int64(time.Since(processStartedAt).Seconds()),
		"goroutines":        runtime.NumGoroutine(),
		"heap_alloc_bytes":  int64(memStats.HeapAlloc),
		"heap_sys_bytes":    int64(memStats.HeapSys),
		"stack_inuse_bytes": int64(memStats.StackInuse),
		"gc_count":          int64(memStats.NumGC),
		"num_cpu":           runtime.NumCPU(),
		"active_requests":   active,
		"active_websockets": websockets,
	}

	sample := statsSample{at: time.Now()}

	if proc, err := statsNewProcess(int32(os.Getpid())); err == nil {
		if times, errT := proc.Times(); errT == nil {
			sample.processCPU = times.User + times.System
			sample.processCPUValid = true
			body["process_cpu_seconds"] = sample.processCPU
		}
		if fds, errF := proc.NumFDs(); errF == nil {
			body["num_fds"] = fds
		}
	}

	if times, err := statsCPUTimes(false); err == nil && len(times) > 0 {
		t := times[0]
		sample.hostTotal = t.Total()
		sample.hostBusy = t.Total() - t.Idle - t.Iowait
		sample.hostCPUValid = true
	}

	if counters, err := statsDiskIOCounters(); err == nil {
		for _, ctr := range counters {
			sample.diskRead += ctr.ReadBytes
			sample.diskWrite += ctr.WriteBytes
		}
	}

	var monthRx, monthTx, totalRx, totalTx uint64
	var monthName, netSource string
	if counters, err := statsNetIOCounters(false); err == nil && len(counters) > 0 {
		sample.netRecv = counters[0].BytesRecv
		sample.netSent = counters[0].BytesSent
		totalRx, totalTx = sample.netRecv, sample.netSent
	}
	// Prefer the vnstat daemon's authoritative monthly accounting; fall back
	// to the builtin tracker when vnstat is not installed.
	if vt, ok := queryVnstat(sample.at); ok {
		monthRx, monthTx = vt.RxBytes, vt.TxBytes
		totalRx, totalTx = vt.TotalRxBytes, vt.TotalTxBytes
		monthName, netSource = vt.Month, "vnstat"
	} else if sample.netRecv > 0 || sample.netSent > 0 {
		if h.netUsage == nil {
			h.netUsage = newNetUsageTracker(defaultNetUsagePath())
		}
		usage := h.netUsage.Observe(sample.netRecv, sample.netSent, sample.at)
		monthRx, monthTx, monthName, netSource = usage.Rx, usage.Tx, usage.Month, "builtin"
	}

	host := gin.H{"num_cpu": runtime.NumCPU()}
	if avg, err := statsLoadAvg(); err == nil {
		host["load1"] = avg.Load1
		host["load5"] = avg.Load5
		host["load15"] = avg.Load15
	}
	if vm, err := statsVirtualMemory(); err == nil {
		host["mem_total_bytes"] = int64(vm.Total)
		host["mem_available_bytes"] = int64(vm.Available)
		host["mem_used_percent"] = clampPercent(vm.UsedPercent)
	}
	if sm, err := statsSwapMemory(); err == nil && sm.Total > 0 {
		host["swap_total_bytes"] = int64(sm.Total)
		host["swap_free_bytes"] = int64(sm.Free)
		host["swap_used_percent"] = clampPercent(sm.UsedPercent)
	}

	if prev := h.lastStatsSample; prev != nil {
		if elapsed := sample.at.Sub(prev.at).Seconds(); elapsed > 0 {
			if sample.processCPUValid && prev.processCPUValid && sample.processCPU >= prev.processCPU {
				body["process_cpu_percent"] = clampPercent((sample.processCPU - prev.processCPU) / elapsed * 100)
			}
			if sample.hostCPUValid && prev.hostCPUValid {
				host["cpu_percent"] = float64(0)
				if hostDelta := sample.hostTotal - prev.hostTotal; hostDelta > 0 {
					host["cpu_percent"] = clampPercent((sample.hostBusy - prev.hostBusy) / hostDelta * 100)
				}
			}
			body["disk_io"] = gin.H{
				"read_bytes_per_sec":  rate(sample.diskRead, prev.diskRead, elapsed),
				"write_bytes_per_sec": rate(sample.diskWrite, prev.diskWrite, elapsed),
			}
			body["network"] = networkBody(sample, *prev, elapsed)
		}
	}
	if _, has := body["network"]; !has && monthName != "" {
		body["network"] = gin.H{}
	}
	if netBody, ok := body["network"].(gin.H); ok && monthName != "" {
		netBody["month"] = monthName
		netBody["month_rx_bytes"] = monthRx
		netBody["month_tx_bytes"] = monthTx
		netBody["total_rx_bytes"] = totalRx
		netBody["total_tx_bytes"] = totalTx
		netBody["source"] = netSource
	}
	h.lastStatsSample = &sample
	body["host"] = host

	if parts, err := statsDiskPartitions(false); err == nil {
		if disks := collectDiskStats(parts, statsDiskUsage); len(disks) > 0 {
			body["disks"] = disks
		}
	}

	c.JSON(http.StatusOK, body)
}

// networkBody builds the network section with windowed rates; monthly
// totals are merged in by the caller once the tracker has observed the
// latest counters.
func networkBody(sample, prev statsSample, elapsed float64) gin.H {
	return gin.H{
		"rx_bytes_per_sec": rate(sample.netRecv, prev.netRecv, elapsed),
		"tx_bytes_per_sec": rate(sample.netSent, prev.netSent, elapsed),
	}
}

// rate converts a cumulative-counter delta into a per-second value,
// tolerating counter resets (e.g. after reboot or namespace changes).
func rate(cur, prev uint64, elapsed float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / elapsed
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
