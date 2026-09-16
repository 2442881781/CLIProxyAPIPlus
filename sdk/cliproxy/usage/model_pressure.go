package usage

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ModelPressureEvent is one completed request folded into a model window.
type ModelPressureEvent struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	Failed       bool
	Latency      time.Duration
	TTFT         time.Duration
}

// ModelPressureRow is the pressure snapshot for one client-facing model.
type ModelPressureRow struct {
	Model    string `json:"model"`
	InFlight int64  `json:"in_flight"`
	Rate
}

type modelPressureEntry struct {
	inFlight int64
	win      RateWindow
}

// PressureScope ends an in-flight count exactly once.
type PressureScope struct {
	once    sync.Once
	release func()
}

// End decrements the gauge; safe to call multiple times.
func (s *PressureScope) End() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.release != nil {
			s.release()
		}
	})
}

// ModelPressureTracker tracks exact in-flight counts and a completed-request
// window per client-facing model name. Process-local and memory-only.
type ModelPressureTracker struct {
	mu     sync.Mutex
	now    func() time.Time
	models map[string]*modelPressureEntry
}

// NewModelPressureTracker returns an empty tracker.
func NewModelPressureTracker() *ModelPressureTracker {
	return &ModelPressureTracker{
		now:    time.Now,
		models: make(map[string]*modelPressureEntry),
	}
}

func normalizePressureModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

// Begin increments the model's in-flight gauge and returns the scope that
// decrements it. One client request maps to exactly one scope regardless of
// how many credentials the conductor retries internally.
func (t *ModelPressureTracker) Begin(model string) *PressureScope {
	if t == nil {
		return &PressureScope{}
	}
	model = normalizePressureModel(model)
	t.mu.Lock()
	entry, ok := t.models[model]
	if !ok {
		entry = &modelPressureEntry{}
		t.models[model] = entry
	}
	entry.inFlight++
	t.mu.Unlock()
	return &PressureScope{release: func() { t.decrement(model) }}
}

func (t *ModelPressureTracker) decrement(model string) {
	t.mu.Lock()
	if entry, ok := t.models[model]; ok && entry.inFlight > 0 {
		entry.inFlight--
	}
	t.mu.Unlock()
}

// InFlight reports the current exact in-flight count for a model.
func (t *ModelPressureTracker) InFlight(model string) int64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry, ok := t.models[normalizePressureModel(model)]; ok {
		return entry.inFlight
	}
	return 0
}

// Observe folds one completed request into the model's 60s window.
func (t *ModelPressureTracker) Observe(model string, ev ModelPressureEvent) {
	if t == nil {
		return
	}
	model = normalizePressureModel(model)
	t.mu.Lock()
	entry, ok := t.models[model]
	if !ok {
		entry = &modelPressureEntry{}
		t.models[model] = entry
	}
	entry.win.AddEvent(t.now(), ev.InputTokens, ev.OutputTokens, ev.TotalTokens, ev.Failed, ev.Latency, ev.TTFT)
	t.mu.Unlock()
}

// Snapshot lists every model that has in-flight requests or a non-empty
// window, ordered by in-flight desc then total tokens/sec desc.
func (t *ModelPressureTracker) Snapshot() []ModelPressureRow {
	if t == nil {
		return nil
	}
	now := t.now()
	t.mu.Lock()
	rows := make([]ModelPressureRow, 0, len(t.models))
	for model, entry := range t.models {
		rate := entry.win.Rate(now)
		idle := entry.inFlight == 0 && rate.RequestsPerSecond == 0
		if idle {
			continue
		}
		rows = append(rows, ModelPressureRow{Model: model, InFlight: entry.inFlight, Rate: rate})
	}
	t.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InFlight != rows[j].InFlight {
			return rows[i].InFlight > rows[j].InFlight
		}
		if rows[i].TotalTokensPerSecond != rows[j].TotalTokensPerSecond {
			return rows[i].TotalTokensPerSecond > rows[j].TotalTokensPerSecond
		}
		return rows[i].Model < rows[j].Model
	})
	return rows
}

var defaultModelPressure atomic.Pointer[ModelPressureTracker]

func init() {
	defaultModelPressure.Store(NewModelPressureTracker())
	RegisterNamedPlugin("model-pressure", modelPressurePlugin{})
}

// DefaultModelPressure returns the process-wide tracker fed by the usage bus
// and the conductor execution funnel.
func DefaultModelPressure() *ModelPressureTracker {
	return defaultModelPressure.Load()
}

// SetDefaultModelPressureForTesting swaps the default tracker and returns a
// restore function suitable for t.Cleanup.
func SetDefaultModelPressureForTesting(tracker *ModelPressureTracker) func() {
	prev := defaultModelPressure.Swap(tracker)
	return func() { defaultModelPressure.Store(prev) }
}

// modelPressurePlugin feeds completed usage records into the tracker so the
// completed-request window follows the same event stream as other sinks.
type modelPressurePlugin struct{}

func (modelPressurePlugin) HandleUsage(_ context.Context, record Record) {
	DefaultModelPressure().Observe(modelPressureKey(record), ModelPressureEvent{
		InputTokens:  record.Detail.InputTokens,
		OutputTokens: record.Detail.OutputTokens,
		TotalTokens:  record.Detail.TotalTokens,
		Failed:       record.Failed,
		Latency:      record.Latency,
		TTFT:         record.TTFT,
	})
}

// modelPressureKey prefers the client-facing alias so pressure aggregates the
// way customers see the model.
func modelPressureKey(record Record) string {
	if alias := strings.TrimSpace(record.Alias); alias != "" {
		return alias
	}
	return record.Model
}
