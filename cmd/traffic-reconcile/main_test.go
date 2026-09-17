package main

import (
	"strings"
	"testing"
	"time"
)

// Feature: traffic reconciliation against the Nginx front door
//
//   The client-leg byte counters are the basis of bandwidth cost. This tool
//   cross-checks them against Nginx's own $request_length + $bytes_sent so an
//   operator can prove the accounting is sane for a given month.

func TestParseTrafficLineExtractsContractTail(t *testing.T) {
	// Given a combined-format line whose request field contains spaces
	// Then the last three tokens are request_length, bytes_sent and prefix
	line := `10.0.0.1 - - [17/Sep/2026:16:19:00 +0800] "POST /v1/chat/completions HTTP/1.1" 200 1234 512 sk-cpa-0123456789abcdef`
	rec, ok := parseTrafficLine(line)
	if !ok {
		t.Fatal("valid line must parse")
	}
	if rec.prefix != "sk-cpa-0123456789abcdef" {
		t.Fatalf("prefix = %q", rec.prefix)
	}
	if rec.requestLength != 1234 {
		t.Fatalf("request length = %d", rec.requestLength)
	}
	if rec.bytesSent != 512 {
		t.Fatalf("bytes sent = %d", rec.bytesSent)
	}
	if !rec.hasTime || !rec.at.Equal(time.Date(2026, 9, 17, 16, 19, 0, 0, time.FixedZone("", 8*3600))) {
		t.Fatalf("timestamp = %v hasTime=%v", rec.at, rec.hasTime)
	}
}

func TestParseTrafficLineRejectsUnusableLines(t *testing.T) {
	// Given malformed or anonymous entries
	// Then they are skipped instead of polluting the totals
	for _, line := range []string{
		"",
		"too short",
		`[17/Sep/2026:16:19:00 +0800] "GET / HTTP/1.1" 200 512 1234 -`,
		`[17/Sep/2026:16:19:00 +0800] "GET / HTTP/1.1" 200 512 nope sk-cpa-x`,
		`[17/Sep/2026:16:19:00 +0800] "GET / HTTP/1.1" 200 512 -12 sk-cpa-x`,
	} {
		if _, ok := parseTrafficLine(line); ok {
			t.Fatalf("line must be rejected: %q", line)
		}
	}
}

func TestAggregateLogGroupsByPrefixAndFiltersSince(t *testing.T) {
	log := strings.Join([]string{
		`10.0.0.1 - - [16/Sep/2026:23:59:00 +0800] "POST /v1/responses HTTP/1.1" 200 100 200 sk-cpa-aaaaaaaaaaaaaaaa`,
		`10.0.0.1 - - [17/Sep/2026:10:00:00 +0800] "POST /v1/responses HTTP/1.1" 200 300 400 sk-cpa-aaaaaaaaaaaaaaaa`,
		`10.0.0.2 - - [17/Sep/2026:11:00:00 +0800] "POST /v1/messages HTTP/1.1" 200 500 600 sk-cpa-bbbbbbbbbbbbbbbb`,
	}, "\n")

	all, skipped, errAgg := aggregateLog(strings.NewReader(log), time.Time{})
	if errAgg != nil {
		t.Fatalf("aggregate: %v", errAgg)
	}
	if skipped != 0 || len(all) != 2 {
		t.Fatalf("all-time groups = %d skipped=%d", len(all), skipped)
	}
	if got := all["sk-cpa-aaaaaaaaaaaaaaaa"]; got.total() != 1000 || got.requests != 2 {
		t.Fatalf("prefix a totals: %+v", got)
	}

	since := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	recent, _, errRecent := aggregateLog(strings.NewReader(log), since)
	if errRecent != nil {
		t.Fatalf("aggregate since: %v", errRecent)
	}
	if got := recent["sk-cpa-aaaaaaaaaaaaaaaa"]; got == nil || got.total() != 700 || got.requests != 1 {
		t.Fatalf("filtered prefix a totals: %+v", got)
	}
	if _, ok := recent["sk-cpa-bbbbbbbbbbbbbbbb"]; !ok {
		t.Fatal("prefix b must survive the filter")
	}
}

func TestAPIUsageClientSinceSumsDailyRows(t *testing.T) {
	usage := apiUsage{
		ClientInBytes:  100,
		ClientOutBytes: 200,
		Daily: map[string]apiDaily{
			"2026-08-31": {InBytes: 10, OutBytes: 20},
			"2026-09-01": {InBytes: 30, OutBytes: 40},
			"2026-09-02": {InBytes: 50, OutBytes: 60},
		},
	}
	if got := usage.clientTotal(); got != 300 {
		t.Fatalf("all-time client total = %d", got)
	}
	if got := usage.clientSince("2026-09-01"); got != 180 {
		t.Fatalf("client since 2026-09-01 = %d", got)
	}
}

func TestTruncateKeepsRunesIntact(t *testing.T) {
	// Given a multi-byte display name longer than the column budget
	// Then truncation never splits a UTF-8 sequence
	if got := truncate("阿尔法团队", 3); got != "阿尔法" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("alice", 16); got != "alice" {
		t.Fatalf("short name must pass through: %q", got)
	}
}
