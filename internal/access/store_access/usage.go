package storeaccess

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// usagePlugin counts token usage per access key on the shared usage bus. The
// record's APIKey field carries the client-presented key (the provider
// principal), so a store lookup attributes usage without extra plumbing.
type usagePlugin struct {
	store *Store
}

func (p *usagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || p.store == nil {
		return
	}
	key := strings.TrimSpace(record.APIKey)
	if key == "" {
		return
	}
	// Attribute the client-requested (alias) model when present so the
	// breakdown matches what the customer asked for.
	model := strings.TrimSpace(record.Alias)
	if model == "" {
		model = strings.TrimSpace(record.Model)
	}
	p.store.RecordUsage(key, UsageEvent{
		Tokens:  record.Detail.TotalTokens,
		Failed:  record.Failed,
		Model:   model,
		AuthID:  strings.TrimSpace(record.AuthID),
		Latency: record.Latency,
		TTFT:    record.TTFT,
	})
}

// startUsageFeed registers the counter plugin on the default usage manager and
// launches a periodic flush so token counters reach disk without per-request
// writes.
func startUsageFeed(store *Store) {
	usage.RegisterNamedPlugin("store-access-keys", &usagePlugin{store: store})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := store.Flush(); err != nil {
				log.Warnf("access key store flush: %v", err)
			}
		}
	}()
}
