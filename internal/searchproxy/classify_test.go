package searchproxy

import (
	"net/http"
	"testing"
	"time"
)

// Feature: Classify upstream responses into pass / rotate-and-cooldown
//   Decides whether a failed upstream response should switch to another key,
//   and for how long the failing key is cooled down.

type classifyCase struct {
	status   int
	outcome  Outcome
	cooldown time.Duration
}

func runClassifyCases(t *testing.T, provider string, cases []classifyCase) {
	t.Helper()
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		outcome, cooldown := Classify(provider, tc.status, http.Header{}, now)
		if outcome != tc.outcome || cooldown != tc.cooldown {
			t.Errorf("%s %d = (%v, %v), want (%v, %v)", provider, tc.status, outcome, cooldown, tc.outcome, tc.cooldown)
		}
	}
}

// Scenario Outline: Tavily response classification
//
//	Given a tavily upstream response with status <status>
//	Then the outcome is <outcome> with cooldown <cooldown>
//	Examples:
//	  | status | outcome | cooldown                       |
//	  | 200    | pass    | none                           |
//	  | 429    | rotate  | Retry-After, else rate default |
//	  | 432    | rotate  | quota default                  |
//	  | 433    | rotate  | quota default                  |
//	  | 401    | rotate  | invalid-key default            |
//	  | 403    | pass    | none (URL not supported)       |
//	  | 400    | pass    | none                           |
//	  | 500    | pass    | none                           |
func TestClassifyTavilyResponses(t *testing.T) {
	runClassifyCases(t, "tavily", []classifyCase{
		{200, OutcomePass, 0},
		{429, OutcomeRotate, RateLimitCooldown},
		{432, OutcomeRotate, QuotaCooldown},
		{433, OutcomeRotate, QuotaCooldown},
		{401, OutcomeRotate, InvalidKeyCooldown},
		{403, OutcomePass, 0},
		{400, OutcomePass, 0},
		{500, OutcomePass, 0},
	})
}

// Scenario Outline: Exa response classification
//
//	Examples:
//	  | status | outcome | cooldown                       |
//	  | 429    | rotate  | Retry-After, else rate default |
//	  | 402    | rotate  | quota default                  |
//	  | 401    | rotate  | invalid-key default            |
//	  | 403    | pass    | none (feature disabled)        |
//	  | 503    | pass    | none (service overloaded)      |
func TestClassifyExaResponses(t *testing.T) {
	runClassifyCases(t, "exa", []classifyCase{
		{429, OutcomeRotate, RateLimitCooldown},
		{402, OutcomeRotate, QuotaCooldown},
		{401, OutcomeRotate, InvalidKeyCooldown},
		{403, OutcomePass, 0},
		{503, OutcomePass, 0},
	})
}

// Scenario Outline: Firecrawl response classification
//
//	Examples:
//	  | status | outcome | cooldown                       |
//	  | 429    | rotate  | Retry-After, else rate default |
//	  | 402    | rotate  | quota default                  |
//	  | 401    | rotate  | invalid-key default            |
//	  | 403    | pass    | none (scope / prompt guard)    |
func TestClassifyFirecrawlResponses(t *testing.T) {
	runClassifyCases(t, "firecrawl", []classifyCase{
		{429, OutcomeRotate, RateLimitCooldown},
		{402, OutcomeRotate, QuotaCooldown},
		{401, OutcomeRotate, InvalidKeyCooldown},
		{403, OutcomePass, 0},
	})
	if RateLimitCooldown != time.Minute || QuotaCooldown != 6*time.Hour || InvalidKeyCooldown != 24*time.Hour {
		t.Fatalf("cooldown defaults = %v/%v/%v, want 1m/6h/24h", RateLimitCooldown, QuotaCooldown, InvalidKeyCooldown)
	}
}

// Scenario: Retry-After in seconds and HTTP-date forms are both honored
//
//	Given a 429 with "Retry-After: 30" or an HTTP-date 30s ahead
//	Then the cooldown is 30s
func TestClassifyHonorsRetryAfterForms(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	for _, value := range []string{"30", now.Add(30 * time.Second).Format(http.TimeFormat)} {
		header := http.Header{}
		header.Set("Retry-After", value)
		outcome, cooldown := Classify("exa", http.StatusTooManyRequests, header, now)
		if outcome != OutcomeRotate || cooldown != 30*time.Second {
			t.Errorf("Retry-After %q = (%v, %v), want (rotate, 30s)", value, outcome, cooldown)
		}
	}
}
