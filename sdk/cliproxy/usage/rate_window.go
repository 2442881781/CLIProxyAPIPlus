package usage

import "time"

// RateWindowSeconds is the sliding window covered by a RateWindow.
const RateWindowSeconds = 60

// Rate is a point-in-time throughput snapshot: per-second averages over the
// whole window. Output tokens/sec is the LLM-conventional "TPS" figure.
type Rate struct {
	RequestsPerSecond     float64 `json:"requests_per_second"`
	InputTokensPerSecond  float64 `json:"input_tokens_per_second"`
	OutputTokensPerSecond float64 `json:"output_tokens_per_second"`
	TotalTokensPerSecond  float64 `json:"total_tokens_per_second"`
}

type rateBucket struct {
	sec   int64
	reqs  int64
	in    int64
	out   int64
	total int64
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
	var reqs, in, out, total int64
	for i := range w.buckets {
		b := &w.buckets[i]
		if b.sec <= oldest || b.sec > nowSec {
			continue
		}
		reqs += b.reqs
		in += b.in
		out += b.out
		total += b.total
	}
	r.RequestsPerSecond = float64(reqs) / RateWindowSeconds
	r.InputTokensPerSecond = float64(in) / RateWindowSeconds
	r.OutputTokensPerSecond = float64(out) / RateWindowSeconds
	r.TotalTokensPerSecond = float64(total) / RateWindowSeconds
	return r
}
