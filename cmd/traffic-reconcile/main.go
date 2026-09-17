// Command traffic-reconcile cross-checks the proxy's per-access-key byte
// accounting against the Nginx front door's own byte counters. It exists to
// validate the client-leg figures used for bandwidth cost: the proxy counts
// request/response payload bytes, Nginx additionally counts HTTP headers, so
// the Nginx total should be slightly higher on every key.
//
// The log file must end each line with the contract tail
//
//	... $request_length $bytes_sent $ak_prefix
//
// where $ak_prefix is the first 16 characters of the presented API key (the
// same value the management API reports as key_prefix). See
// docs/traffic-accounting.md for the log_format snippet.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// nginxTimeLayout matches the default $time_local field.
const nginxTimeLayout = "02/Jan/2006:15:04:05 -0700"

type lineRecord struct {
	prefix        string
	requestLength int64
	bytesSent     int64
	at            time.Time
	hasTime       bool
}

// parseTrafficLine reads one access-log line and extracts the contract tail:
// $request_length $bytes_sent $ak_prefix, counted from the end so leading
// fields may contain spaces. The first field is parsed as $time_local when it
// matches nginx's layout.
func parseTrafficLine(line string) (lineRecord, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 3 {
		return lineRecord{}, false
	}
	prefix := fields[len(fields)-1]
	if prefix == "" || prefix == "-" {
		return lineRecord{}, false
	}
	bytesSent, errSent := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if errSent != nil || bytesSent < 0 {
		return lineRecord{}, false
	}
	requestLength, errLength := strconv.ParseInt(fields[len(fields)-3], 10, 64)
	if errLength != nil || requestLength < 0 {
		return lineRecord{}, false
	}
	out := lineRecord{
		prefix:        prefix,
		requestLength: requestLength,
		bytesSent:     bytesSent,
	}
	if at, okTime := parseNginxTime(line); okTime {
		out.at = at
		out.hasTime = true
	}
	return out, true
}

// parseNginxTime extracts $time_local from the bracketed field of a combined
// access-log line. It scans the raw line because the value itself contains a
// space (the timezone offset) and the field is not necessarily first.
func parseNginxTime(line string) (time.Time, bool) {
	start := strings.IndexByte(line, '[')
	end := strings.IndexByte(line, ']')
	if start < 0 || end <= start {
		return time.Time{}, false
	}
	at, errTime := time.Parse(nginxTimeLayout, line[start+1:end])
	if errTime != nil {
		return time.Time{}, false
	}
	return at, true
}

type keyTotals struct {
	requests      int64
	requestLength int64
	bytesSent     int64
}

func (t *keyTotals) total() int64 { return t.requestLength + t.bytesSent }

// aggregateLog folds every parsable line into per-prefix totals. When since is
// non-zero, lines whose timestamp is older (or missing) are skipped and
// reported through skipped.
func aggregateLog(r io.Reader, since time.Time) (map[string]*keyTotals, int64, error) {
	out := make(map[string]*keyTotals)
	var skipped int64
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		rec, ok := parseTrafficLine(scanner.Text())
		if !ok {
			continue
		}
		if !since.IsZero() {
			if !rec.hasTime || rec.at.Before(since) {
				skipped++
				continue
			}
		}
		totals := out[rec.prefix]
		if totals == nil {
			totals = &keyTotals{}
			out[rec.prefix] = totals
		}
		totals.requests++
		totals.requestLength += rec.requestLength
		totals.bytesSent += rec.bytesSent
	}
	if errScanner := scanner.Err(); errScanner != nil {
		return nil, skipped, errScanner
	}
	return out, skipped, nil
}

type apiDaily struct {
	InBytes  int64 `json:"in_bytes"`
	OutBytes int64 `json:"out_bytes"`
}

type apiUsage struct {
	ClientInBytes    int64               `json:"client_in_bytes"`
	ClientOutBytes   int64               `json:"client_out_bytes"`
	UpstreamInBytes  int64               `json:"upstream_in_bytes"`
	UpstreamOutBytes int64               `json:"upstream_out_bytes"`
	PeriodBytes      int64               `json:"period_bytes"`
	Daily            map[string]apiDaily `json:"daily"`
}

func (u apiUsage) clientTotal() int64 { return u.ClientInBytes + u.ClientOutBytes }

// clientSince sums the daily client-leg bytes for days at or after the given
// UTC date (YYYY-MM-DD).
func (u apiUsage) clientSince(since string) int64 {
	var total int64
	for day, row := range u.Daily {
		if day >= since {
			total += row.InBytes + row.OutBytes
		}
	}
	return total
}

type apiKey struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	KeyPrefix string   `json:"key_prefix"`
	Disabled  bool     `json:"disabled"`
	Usage     apiUsage `json:"usage"`
}

func fetchKeys(client *http.Client, baseURL, secret string) ([]apiKey, error) {
	url := strings.TrimRight(baseURL, "/") + "/v0/management/access-keys"
	req, errRequest := http.NewRequest(http.MethodGet, url, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("management API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Keys []apiKey `json:"access-keys"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("decode access-keys: %w", errUnmarshal)
	}
	return payload.Keys, nil
}

const gib = 1 << 30

func formatBytes(value int64) string {
	return fmt.Sprintf("%.2f GB", float64(value)/gib)
}

type row struct {
	prefix string
	name   string
	proxy  int64
	nginx  int64
}

func (r row) diffPercent() float64 {
	if r.proxy == 0 {
		return 0
	}
	return float64(r.nginx-r.proxy) / float64(r.proxy) * 100
}

func main() {
	logPath := flag.String("log", "-", `access log path, "-" for stdin`)
	apiBase := flag.String("api", "http://127.0.0.1:8317", "management API base URL")
	secret := flag.String("key", os.Getenv("CPA_MANAGEMENT_KEY"), "management secret key (or CPA_MANAGEMENT_KEY)")
	sinceRaw := flag.String("since", "", "only count entries at/after this date (YYYY-MM-DD, nginx local time; proxy daily rows are UTC)")
	maxDiff := flag.Float64("max-diff", 0, "exit non-zero when any key's nginx/proxy difference exceeds this percentage")
	flag.Parse()

	if strings.TrimSpace(*secret) == "" {
		fmt.Fprintln(os.Stderr, "error: management secret is required (-key or CPA_MANAGEMENT_KEY)")
		os.Exit(2)
	}
	var since time.Time
	if strings.TrimSpace(*sinceRaw) != "" {
		parsed, errSince := time.Parse("2006-01-02", strings.TrimSpace(*sinceRaw))
		if errSince != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -since value %q: %v\n", *sinceRaw, errSince)
			os.Exit(2)
		}
		since = parsed
	}

	var reader io.Reader
	if *logPath == "-" {
		reader = os.Stdin
	} else {
		file, errOpen := os.Open(*logPath)
		if errOpen != nil {
			fmt.Fprintf(os.Stderr, "error: open log: %v\n", errOpen)
			os.Exit(1)
		}
		defer func() { _ = file.Close() }()
		reader = file
	}

	nginxTotals, skipped, errAgg := aggregateLog(reader, since)
	if errAgg != nil {
		fmt.Fprintf(os.Stderr, "error: read log: %v\n", errAgg)
		os.Exit(1)
	}

	keys, errKeys := fetchKeys(&http.Client{Timeout: 30 * time.Second}, *apiBase, *secret)
	if errKeys != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", errKeys)
		os.Exit(1)
	}

	rows := make([]row, 0, len(keys))
	for _, key := range keys {
		proxyBytes := key.Usage.clientTotal()
		if !since.IsZero() {
			proxyBytes = key.Usage.clientSince(since.Format("2006-01-02"))
		}
		entry := nginxTotals[key.KeyPrefix]
		nginxBytes := int64(0)
		if entry != nil {
			nginxBytes = entry.total()
		}
		rows = append(rows, row{prefix: key.KeyPrefix, name: key.Name, proxy: proxyBytes, nginx: nginxBytes})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].nginx > rows[j].nginx })

	fmt.Printf("%-20s %-16s %14s %14s %10s\n", "KEY_PREFIX", "NAME", "PROXY", "NGINX", "DIFF")
	var totalProxy, totalNginx int64
	overLimit := false
	for _, r := range rows {
		totalProxy += r.proxy
		totalNginx += r.nginx
		diff := r.diffPercent()
		if *maxDiff > 0 && r.proxy > 0 && (diff > *maxDiff || -diff > *maxDiff) {
			overLimit = true
		}
		fmt.Printf("%-20s %-16s %14s %14s %9.2f%%\n", r.prefix, truncate(r.name, 16), formatBytes(r.proxy), formatBytes(r.nginx), diff)
	}
	fmt.Printf("%-20s %-16s %14s %14s %9.2f%%\n", "TOTAL", "", formatBytes(totalProxy), formatBytes(totalNginx), row{proxy: totalProxy, nginx: totalNginx}.diffPercent())
	if skipped > 0 {
		fmt.Printf("\nskipped %d entries without a parsable timestamp older than %s\n", skipped, since.Format("2006-01-02"))
	}

	// Prefixes present in the log but absent from the key list are requests
	// that never authenticated against a known key (revoked or invalid keys).
	var unknown []string
	for prefix, totals := range nginxTotals {
		matched := false
		for _, key := range keys {
			if key.KeyPrefix == prefix {
				matched = true
				break
			}
		}
		if !matched {
			unknown = append(unknown, fmt.Sprintf("%s (%s, %d requests)", prefix, formatBytes(totals.total()), totals.requests))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		fmt.Printf("\nunmatched log prefixes:\n  %s\n", strings.Join(unknown, "\n  "))
	}

	if overLimit {
		os.Exit(1)
	}
}

// truncate limits a display name to limit runes so multi-byte names are never
// cut in the middle of a UTF-8 sequence.
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
