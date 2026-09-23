package searchproxy

import (
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

const (
	// jobAffinityTTL bounds how long a job id stays pinned to the key that created it.
	jobAffinityTTL = 24 * time.Hour
	// jobSweepThreshold triggers an expiry sweep once the store grows past this size.
	jobSweepThreshold = 1024
)

// jobCreatePaths lists, per provider, the POST endpoints that start an async job whose
// follow-up requests (/{path}/{id}/...) must use the same upstream key.
var jobCreatePaths = map[string][]string{
	config.SearchProviderTavily: {"/research"},
	config.SearchProviderExa:    {"/agent/runs", "/batches", "/research/v1"},
	config.SearchProviderFirecrawl: {
		"/v1/crawl", "/v2/crawl", "/v1/batch/scrape", "/v2/batch/scrape",
		"/v1/extract", "/v2/extract", "/v2/agent", "/v1/deep-research", "/v1/llmstxt",
	},
}

// jobIDFields are the top-level response fields that carry a created job id.
var jobIDFields = []string{"id", "request_id", "researchId", "jobId"}

func isJobCreatePath(provider, path string) bool {
	for _, p := range jobCreatePaths[provider] {
		if p == path {
			return true
		}
	}
	return false
}

func extractJobID(body []byte) string {
	for _, field := range jobIDFields {
		if v := gjson.GetBytes(body, field); v.Type == gjson.String && v.String() != "" {
			return v.String()
		}
	}
	return ""
}

type jobEntry struct {
	keyID   string
	expires time.Time
}

// jobStore remembers which upstream key created each async job.
type jobStore struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]jobEntry
}

func newJobStore(now func() time.Time) *jobStore {
	return &jobStore{now: now, entries: map[string]jobEntry{}}
}

func (j *jobStore) put(provider, jobID, keyID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	if len(j.entries) >= jobSweepThreshold {
		for k, e := range j.entries {
			if !e.expires.After(now) {
				delete(j.entries, k)
			}
		}
	}
	j.entries[provider+"\x00"+jobID] = jobEntry{keyID: keyID, expires: now.Add(jobAffinityTTL)}
}

// ownerOf returns the key id that created a job referenced by any segment of path.
func (j *jobStore) ownerOf(provider, path string) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		if e, ok := j.entries[provider+"\x00"+segment]; ok && e.expires.After(now) {
			return e.keyID, true
		}
	}
	return "", false
}
