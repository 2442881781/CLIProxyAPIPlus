package searchproxy

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Default cooldowns applied to a key after a rotate-class upstream failure.
const (
	RateLimitCooldown  = time.Minute
	QuotaCooldown      = 6 * time.Hour
	InvalidKeyCooldown = 24 * time.Hour
)

// Outcome tells the proxy what to do with an upstream response.
type Outcome int

const (
	// OutcomePass returns the response to the client as-is.
	OutcomePass Outcome = iota
	// OutcomeRotate cools down the key and retries with the next one.
	OutcomeRotate
)

func (o Outcome) String() string {
	if o == OutcomeRotate {
		return "rotate"
	}
	return "pass"
}

// Classify maps an upstream status to an outcome and the cooldown for the key that produced it.
func Classify(provider string, status int, header http.Header, now time.Time) (Outcome, time.Duration) {
	switch status {
	case http.StatusTooManyRequests:
		if d, ok := parseRetryAfter(header.Get("Retry-After"), now); ok {
			return OutcomeRotate, d
		}
		return OutcomeRotate, RateLimitCooldown
	case http.StatusUnauthorized:
		return OutcomeRotate, InvalidKeyCooldown
	case http.StatusPaymentRequired:
		if provider == config.SearchProviderExa || provider == config.SearchProviderFirecrawl {
			return OutcomeRotate, QuotaCooldown
		}
	case 432, 433:
		// Tavily: 432 key/plan limit exceeded, 433 pay-as-you-go limit exceeded.
		if provider == config.SearchProviderTavily {
			return OutcomeRotate, QuotaCooldown
		}
	}
	return OutcomePass, 0
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d, true
		}
	}
	return 0, false
}
