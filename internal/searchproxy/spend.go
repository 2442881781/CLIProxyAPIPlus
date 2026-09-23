package searchproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// spendStoreName is the file under auth-dir that keeps Exa month-to-date spend across restarts.
const spendStoreName = "search-spend.store"

// maxSpendCaptureBytes bounds how much of a relayed response is buffered to read costDollars.
const maxSpendCaptureBytes = 4 << 20

type spendRecord struct {
	Month    string  `json:"month"`
	SpentUSD float64 `json:"spent_usd"`
}

type spendFile struct {
	Version int                    `json:"version"`
	Keys    map[string]spendRecord `json:"keys"`
}

func monthKey(now time.Time) string { return now.UTC().Format("2006-01") }

func nextMonth(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

func (s *keyState) spentIn(now time.Time) float64 {
	if s.spentMonth != monthKey(now) {
		return 0
	}
	return s.spentUSD
}

// AddSpend adds USD spend to a key for the current UTC month.
func (p *Pool) AddSpend(id string, usd float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[id]
	if state == nil || usd <= 0 {
		return
	}
	month := monthKey(p.now())
	if state.spentMonth != month {
		state.spentMonth, state.spentUSD = month, 0
	}
	state.spentUSD += usd
}

// resetSpend clears the month-to-date spend of a key and reports whether it exists.
func (p *Pool) resetSpend(provider, apiKey string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[KeyID(provider, apiKey)]
	if state == nil {
		return false
	}
	state.spentMonth, state.spentUSD = monthKey(p.now()), 0
	return true
}

func (p *Pool) spendRecords() map[string]spendRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	month := monthKey(p.now())
	out := map[string]spendRecord{}
	for id, state := range p.byID {
		if state.spentMonth == month && state.spentUSD > 0 {
			out[id] = spendRecord{Month: month, SpentUSD: state.spentUSD}
		}
	}
	return out
}

// loadSpend restores persisted spend into keys that have not been loaded yet.
func (p *Pool) loadSpend(records map[string]spendRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, state := range p.byID {
		if state.spendLoaded {
			continue
		}
		state.spendLoaded = true
		if rec, ok := records[id]; ok && state.spentMonth == "" {
			state.spentMonth, state.spentUSD = rec.Month, rec.SpentUSD
		}
	}
}

// RecordSpend adds spend for a key and persists the month-to-date totals.
func (s *Service) RecordSpend(id string, usd float64) {
	if usd <= 0 {
		return
	}
	s.pool.AddSpend(id, usd)
	s.saveSpend()
}

// ResetSpend clears a key's month-to-date spend and persists the change.
func (s *Service) ResetSpend(provider, apiKey string) bool {
	if !s.pool.resetSpend(provider, apiKey) {
		return false
	}
	s.saveSpend()
	return true
}

func (s *Service) setSpendPath(authDir string) {
	path := ""
	if authDir != "" {
		path = filepath.Join(authDir, spendStoreName)
	}
	s.spendMu.Lock()
	s.spendPath = path
	s.spendMu.Unlock()
	if path == "" {
		return
	}
	records, err := readSpendFile(path)
	if err != nil {
		log.Warnf("search proxy: load %s: %v", spendStoreName, err)
	}
	s.pool.loadSpend(records)
}

func (s *Service) saveSpend() {
	s.spendMu.Lock()
	defer s.spendMu.Unlock()
	if s.spendPath == "" {
		return
	}
	if err := writeSpendFile(s.spendPath, s.pool.spendRecords()); err != nil {
		log.Warnf("search proxy: save %s: %v", spendStoreName, err)
	}
}

func readSpendFile(path string) (map[string]spendRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file spendFile
	if err = json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return file.Keys, nil
}

func writeSpendFile(path string, records map[string]spendRecord) error {
	raw, err := json.Marshal(spendFile{Version: 1, Keys: records})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// exaCost extracts costDollars.total from an Exa JSON response.
func exaCost(body []byte) float64 {
	return gjson.GetBytes(body, "costDollars.total").Float()
}

// captureBody tees up to maxSpendCaptureBytes of a response body while it is relayed.
type captureBody struct {
	io.ReadCloser
	buf       bytes.Buffer
	truncated bool
}

func (b *captureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && !b.truncated {
		if b.buf.Len()+n > maxSpendCaptureBytes {
			b.truncated = true
			b.buf.Reset()
		} else {
			b.buf.Write(p[:n])
		}
	}
	return n, err
}
