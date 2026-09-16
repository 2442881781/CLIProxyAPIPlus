package usage

import "time"

// RateWindowSeconds is the sliding window covered by a RateWindow.
const RateWindowSeconds = 60

// Rate is a point-in-time throughput snapshot: per-second averages over the
// whole window. Output tokens/sec is the LLM-conventional "TPS" figure.
// ErrorRate is failed requests / window requests; latency figures are
// averages over the events that reported them.
type Rate struct {
	RequestsPerSecond     float64 `json:"requests_per_second"`
	InputTokensPerSecond  float64 `json:"input_tokens_per_second"`
	OutputTokensPerSecond float64 `json:"output_tokens_per_second"`
	TotalTokensPerSecond  float64 `json:"total_tokens_per_second"`
	FailedPerSecond       float64 `json:"failed_per_second"`
	ErrorRate             float64 `json:"error_rate"`
	AvgLatencyMS          int64   `json:"avg_latency_ms"`
	AvgTTFTMS             int64   `json:"avg_ttft_ms"`
}

type rateBucket struct {
	sec       int64
	reqs      int64
	in        int64
	out       int64
	total     int64
	failed    int64
	latencyMS int64
	latencyN  int64
	ttftMS    int64
	ttftN     int64
}

// RateWindow is a ring of per-second buckets covering the last
// RateWindowSeconds. It is a real-time gauge: memory only, nothing persists.
// Not internally synchronized — callers must serialize access.
type RateWindow struct {
	buckets [RateWindowSeconds]rateBucket
}

// Add folds one completed request into the bucket for `now`. Events stamped
// older than the newest bucket occupying their ring slot are dropped rather
// than clobbering fresher data.
func (w *RateWindow) Add(now time.Time, input, output, total int64) {
	w.AddEvent(now, input, output, total, false, 0, 0)
}

// AddEvent is Add plus failure and latency dimensions for the same request.
func (w *RateWindow) AddEvent(now time.Time, input, output, total int64, failed bool, latency, ttft time.Duration) {
	if w == nil {
		return
	}
	sec := now.Unix()
	idx := int(sec % RateWindowSeconds)
	if idx < 0 {
		idx += RateWindowSeconds
	}
	b := &w.buckets[idx]
	if b.sec > sec {
		return // slot already holds a newer second; the event is out of window anyway
	}
	if b.sec != sec {
		*b = rateBucket{sec: sec}
	}
	b.reqs++
	b.in += input
	b.out += output
	b.total += total
	if failed {
		b.failed++
	}
	if latency > 0 {
		b.latencyMS += latency.Milliseconds()
		b.latencyN++
	}
	if ttft > 0 {
		b.ttftMS += ttft.Milliseconds()
		b.ttftN++
	}
}

// Rate averages all in-window buckets. Buckets stamped in the future or
// older than the window are ignored.
func (w *RateWindow) Rate(now time.Time) Rate {
	var r Rate
	if w == nil {
		return r
	}
	nowSec := now.Unix()
	oldest := nowSec - RateWindowSeconds
	var reqs, in, out, total, failed, latencyMS, latencyN, ttftMS, ttftN int64
	for i := range w.buckets {
		b := &w.buckets[i]
		if b.sec <= oldest || b.sec > nowSec {
			continue
		}
		reqs += b.reqs
		in += b.in
		out += b.out
		total += b.total
		failed += b.failed
		latencyMS += b.latencyMS
		latencyN += b.latencyN
		ttftMS += b.ttftMS
		ttftN += b.ttftN
	}
	r.RequestsPerSecond = float64(reqs) / RateWindowSeconds
	r.InputTokensPerSecond = float64(in) / RateWindowSeconds
	r.OutputTokensPerSecond = float64(out) / RateWindowSeconds
	r.TotalTokensPerSecond = float64(total) / RateWindowSeconds
	r.FailedPerSecond = float64(failed) / RateWindowSeconds
	if reqs > 0 {
		r.ErrorRate = float64(failed) / float64(reqs)
	}
	if latencyN > 0 {
		r.AvgLatencyMS = latencyMS / latencyN
	}
	if ttftN > 0 {
		r.AvgTTFTMS = ttftMS / ttftN
	}
	return r
}
