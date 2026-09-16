package storeaccess

import (
	"sort"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// KeyRateRow is one key's live throughput gauge for admin dashboards.
type KeyRateRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	Group     string `json:"group,omitempty"`
	coreusage.Rate
}

// keyRateWindowLocked returns (creating if needed) the key's sliding window.
// Caller must hold s.mu.
func (s *Store) keyRateWindowLocked(id string) *coreusage.RateWindow {
	if s.keyTPS == nil {
		s.keyTPS = make(map[string]*coreusage.RateWindow)
	}
	w := s.keyTPS[id]
	if w == nil {
		w = &coreusage.RateWindow{}
		s.keyTPS[id] = w
	}
	return w
}

// KeyRate returns the key's current rate gauge. The second return is false
// when the key id is unknown.
func (s *Store) KeyRate(id string) (coreusage.Rate, bool) {
	if s == nil {
		return coreusage.Rate{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.keys[id]
	if !ok {
		return coreusage.Rate{}, false
	}
	return s.keyTPS[entry.ID].Rate(s.nowTime()), true
}

// KeyRates returns every key's live rate, ordered by total tokens/sec
// descending (ties by id). Keys without recent activity report a zero rate.
func (s *Store) KeyRates() []KeyRateRow {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	rows := make([]KeyRateRow, 0, len(s.keys))
	for _, entry := range s.keys {
		rows = append(rows, KeyRateRow{
			ID:        entry.ID,
			Name:      entry.Name,
			KeyPrefix: entry.KeyPrefix,
			Group:     entry.Group,
			Rate:      s.keyTPS[entry.ID].Rate(s.nowTime()),
		})
	}
	s.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalTokensPerSecond != rows[j].TotalTokensPerSecond {
			return rows[i].TotalTokensPerSecond > rows[j].TotalTokensPerSecond
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}
