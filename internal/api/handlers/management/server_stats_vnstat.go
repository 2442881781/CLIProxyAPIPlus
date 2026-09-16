package management

import (
	"encoding/json"
	"os/exec"
	"time"
)

// vnstat integration: when the vnstat daemon is installed on the host it is
// the authoritative source for monthly traffic — it counts independently of
// this process, so totals survive restarts, crashes and upgrades. Without
// vnstat the endpoint falls back to the builtin counter-delta tracker.

// statsVnstatJSON runs vnstat --json m and returns raw output. Kept as a
// package var so tests can inject fixtures.
var statsVnstatJSON = func() ([]byte, error) {
	return exec.Command("vnstat", "--json", "m").Output()
}

type vnstatTraffic struct {
	Month        string // "2006-01"
	RxBytes      uint64
	TxBytes      uint64
	TotalRxBytes uint64 // all-time, since vnstat started tracking
	TotalTxBytes uint64
}

// queryVnstat returns the authoritative traffic totals when vnstat is
// installed and answers; ok=false on any failure.
func queryVnstat(now time.Time) (vnstatTraffic, bool) {
	out, err := statsVnstatJSON()
	if err != nil {
		return vnstatTraffic{}, false
	}
	return parseVnstatMonthly(out, now)
}

// parseVnstatMonthly sums the current calendar month's rx/tx across every
// monitored interface. vnstat reports KiB; results are returned in bytes.
func parseVnstatMonthly(data []byte, now time.Time) (vnstatTraffic, bool) {
	var doc struct {
		Interfaces []struct {
			Name    string `json:"name"`
			Traffic struct {
				Total struct {
					Rx uint64 `json:"rx"`
					Tx uint64 `json:"tx"`
				} `json:"total"`
				Month []struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
					} `json:"date"`
					Rx uint64 `json:"rx"`
					Tx uint64 `json:"tx"`
				} `json:"month"`
			} `json:"traffic"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Interfaces) == 0 {
		return vnstatTraffic{}, false
	}
	var out vnstatTraffic
	out.Month = now.Format("2006-01")
	for _, iface := range doc.Interfaces {
		out.TotalRxBytes += iface.Traffic.Total.Rx * 1024
		out.TotalTxBytes += iface.Traffic.Total.Tx * 1024
		for _, m := range iface.Traffic.Month {
			if m.Date.Year == now.Year() && int(now.Month()) == m.Date.Month {
				out.RxBytes += m.Rx * 1024
				out.TxBytes += m.Tx * 1024
			}
		}
	}
	return out, true
}
