package storeaccess

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimit bounds per-key downstream usage. RPM/TPM are token buckets (burst
// up to the limit, then refill smoothly); RPD is a persisted per-UTC-day
// request counter; MaxConcurrency caps simultaneous in-flight requests. On a
// key, a field of 0 inherits the group's per-key default, >0 overrides it,
// and -1 forces unlimited. On a group's PerKeyLimits, <=0 means no default.
type RateLimit struct {
	RPM            int `json:"rpm,omitempty"`
	TPM            int `json:"tpm,omitempty"`
	RPD            int `json:"rpd,omitempty"`
	MaxConcurrency int `json:"max_concurrency,omitempty"`
}

// merge returns the limit set where each unset (0) field inherits the group
// per-key default. Explicit -1 values survive and read as unlimited.
func (r RateLimit) merge(defaults RateLimit) RateLimit {
	out := r
	if out.RPM == 0 {
		out.RPM = defaults.RPM
	}
	if out.TPM == 0 {
		out.TPM = defaults.TPM
	}
	if out.RPD == 0 {
		out.RPD = defaults.RPD
	}
	if out.MaxConcurrency == 0 {
		out.MaxConcurrency = defaults.MaxConcurrency
	}
	return out
}

// unlimited reports whether the resolved limit set imposes no restrictions.
func (r RateLimit) unlimited() bool {
	return r.RPM <= 0 && r.TPM <= 0 && r.RPD <= 0 && r.MaxConcurrency <= 0
}

// tokenBucket is a lazily-initialized token bucket. Capacity equals the limit;
// tokens refill at limit/window per second. The bucket may go negative when a
// post-hoc charge exceeds the balance, which simply delays the next admission.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// refill tops up the bucket from elapsed time and returns the per-second rate.
func (b *tokenBucket) refill(now time.Time, limit int64, window time.Duration) float64 {
	cap := float64(limit)
	rate := cap / window.Seconds()
	if b.last.IsZero() {
		b.tokens = cap
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += rate * elapsed
		if b.tokens > cap {
			b.tokens = cap
		}
	}
	b.last = now
	return rate
}

// take consumes one token. On rejection it returns the seconds until the next
// token becomes available.
func (b *tokenBucket) take(now time.Time, limit int64, window time.Duration) (bool, float64) {
	rate := b.refill(now, limit, window)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, (1 - b.tokens) / rate
}

// admit checks the balance without consuming a unit. Used for TPM where the
// real cost is only known once the upstream response completes.
func (b *tokenBucket) admit(now time.Time, limit int64, window time.Duration) (bool, float64) {
	rate := b.refill(now, limit, window)
	if b.tokens >= 1 {
		return true, 0
	}
	return false, (1 - b.tokens) / rate
}

// charge debits actual usage after a request completes; the balance may go
// negative, blocking admission until the deficit refills.
func (b *tokenBucket) charge(now time.Time, amount int64, limit int64, window time.Duration) {
	b.refill(now, limit, window)
	b.tokens -= float64(amount)
}

// keyRateState holds a key's in-memory rate buckets and in-flight counter.
type keyRateState struct {
	rpm      tokenBucket
	tpm      tokenBucket
	inflight int64
}

// effectiveLimitsLocked resolves a key's rate limits against its group's
// per-key defaults. Caller must hold s.mu.
func (s *Store) effectiveLimitsLocked(entry *AccessKey) RateLimit {
	limits := entry.RateLimit
	if grp, ok := s.groups[entry.Group]; ok && grp != nil {
		limits = limits.merge(grp.PerKeyLimits)
	}
	return limits
}

// keyStateLocked returns the rate state for a key, creating it on demand.
// Caller must hold s.mu.
func (s *Store) keyStateLocked(id string) *keyRateState {
	if s.keyRate == nil {
		s.keyRate = make(map[string]*keyRateState)
	}
	st := s.keyRate[id]
	if st == nil {
		st = &keyRateState{}
		s.keyRate[id] = st
	}
	return st
}

// EffectiveRateLimit resolves a key's rate limits against its group's per-key
// defaults, for display and self-service lookups.
func (s *Store) EffectiveRateLimit(entry *AccessKey) RateLimit {
	if s == nil || entry == nil {
		return RateLimit{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveLimitsLocked(entry)
}

func (s *Store) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// AcquireKey checks a key's effective rate limits and, when allowed, registers
// the request (RPM token, RPD count, concurrency slot). The returned release
// func frees the concurrency slot and must be called when the request
// finishes; it is nil on rejection. retryAfter reports the seconds until
// admission is expected to succeed again.
func (s *Store) AcquireKey(id string) (release func(), retryAfter float64, err error) {
	if s == nil || id == "" {
		return func() {}, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[id]
	if !ok {
		return func() {}, 0, nil
	}
	limits := s.effectiveLimitsLocked(entry)
	if limits.unlimited() {
		return func() {}, 0, nil
	}
	st := s.keyStateLocked(id)
	now := s.nowTime()

	// Non-consuming checks first so a rejection does not burn other budgets.
	if limits.TPM > 0 {
		if ok, ra := st.tpm.admit(now, int64(limits.TPM), time.Minute); !ok {
			return nil, ra, fmt.Errorf("key %q token rate limit exceeded (%d tpm)", entry.KeyPrefix, limits.TPM)
		}
	}
	dayKey := ""
	if limits.RPD > 0 {
		dayKey = now.UTC().Format("2006-01-02")
		if entry.Usage.DayKey != dayKey {
			entry.Usage.DayKey = dayKey
			entry.Usage.DayRequests = 0
		}
		if entry.Usage.DayRequests >= int64(limits.RPD) {
			midnight := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
			return nil, midnight.Sub(now).Seconds(), fmt.Errorf("key %q daily request limit exceeded (%d rpd)", entry.KeyPrefix, limits.RPD)
		}
	}
	if limits.MaxConcurrency > 0 && st.inflight >= int64(limits.MaxConcurrency) {
		return nil, 1, fmt.Errorf("key %q concurrency limit exceeded (%d)", entry.KeyPrefix, limits.MaxConcurrency)
	}
	if limits.RPM > 0 {
		if ok, ra := st.rpm.take(now, int64(limits.RPM), time.Minute); !ok {
			return nil, ra, fmt.Errorf("key %q rate limit exceeded (%d rpm)", entry.KeyPrefix, limits.RPM)
		}
	}
	if limits.RPD > 0 {
		entry.Usage.DayRequests++
		s.dirty = true
	}
	if limits.MaxConcurrency > 0 {
		st.inflight++
		var once sync.Once
		return func() {
			once.Do(func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if st.inflight > 0 {
					st.inflight--
				}
			})
		}, 0, nil
	}
	return func() {}, 0, nil
}

// chargeKeyTPM debits recorded token usage from the key's TPM bucket. Caller
// must hold s.mu.
func (s *Store) chargeKeyTPMLocked(entry *AccessKey, tokens int64, now time.Time) {
	if tokens <= 0 {
		return
	}
	limits := s.effectiveLimitsLocked(entry)
	if limits.TPM <= 0 {
		return
	}
	s.keyStateLocked(entry.ID).tpm.charge(now, tokens, int64(limits.TPM), time.Minute)
}

// abortRateLimited writes a 429 in the error envelope native to the route:
// Claude shape for /v1/messages, Gemini shape for /v1beta/*, OpenAI shape
// everywhere else. Retry-After is emitted when positive.
func abortRateLimited(c *gin.Context, retryAfter float64, msg string) {
	if retryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter))))
	}
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	switch {
	case strings.Contains(path, "/v1/messages"):
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"type":  "error",
			"error": gin.H{"type": "rate_limit_error", "message": msg},
		})
	case strings.HasPrefix(path, "/v1beta"):
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"code": 429, "message": msg, "status": "RESOURCE_EXHAUSTED"},
		})
	default:
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{
				"message": msg,
				"type":    "rate_limit_error",
				"param":   nil,
				"code":    "rate_limit_exceeded",
			},
		})
	}
}
