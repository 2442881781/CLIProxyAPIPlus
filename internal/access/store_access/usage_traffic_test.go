package storeaccess

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// newEdgeTestRouter builds a router whose request carries the given access
// metadata and whose final handler runs body before the response recorder is
// returned to the caller.
func newEdgeTestRouter(t *testing.T, s *Store, meta map[string]string, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/*path", func(c *gin.Context) {
		if meta != nil {
			c.Set("accessMetadata", meta)
		}
		c.Next()
	}, GroupAccessMiddleware(s), handler)
	return router
}

// Feature: per-key traffic accounting (bandwidth cost)
//
//   Operators need to know how much bandwidth each access key costs the host.
//   The figures come from two independent measurement points: the HTTP edge
//   (client leg: request body read + response bytes written, including
//   hijacked websocket connections) and the executor transport wrapper
//   (provider leg). Their sum approximates the host interface traffic that the
//   key is responsible for, so both legs must land in the same counters.

func TestTraffic_ClientAndUpstreamLegsSumIntoTotal(t *testing.T) {
	// Given a key whose provider leg is reported by the usage plugin
	// And whose client leg is reported by the HTTP edge
	// Then totals, period counters and dimension rows carry both legs
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-traffic", AccessKey{Name: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-traffic", UsageEvent{
		Tokens: 10, Model: "gpt-x", AuthID: "auth-a",
		UpstreamInBytes: 5000, UpstreamOutBytes: 1200,
	})
	if !s.RecordTraffic(entry.ID, TrafficEvent{InBytes: 1100, OutBytes: 4800, Model: "gpt-x"}) {
		t.Fatal("RecordTraffic must accept a known key id")
	}

	got := s.Get(entry.ID)
	if got.Usage.ClientInBytes != 1100 || got.Usage.ClientOutBytes != 4800 {
		t.Fatalf("client leg: %+v", got.Usage)
	}
	if got.Usage.UpstreamInBytes != 5000 || got.Usage.UpstreamOutBytes != 1200 {
		t.Fatalf("provider leg: %+v", got.Usage)
	}
	if got.Usage.TotalBytes() != 12100 || got.Usage.ClientBytes() != 5900 {
		t.Fatalf("byte totals: %+v", got.Usage)
	}
	if got.Usage.PeriodBytes != 12100 {
		t.Fatalf("period bytes: %d", got.Usage.PeriodBytes)
	}
	if got.Usage.Requests != 1 {
		t.Fatalf("traffic must not increment requests: %d", got.Usage.Requests)
	}
	model := got.Usage.Models["gpt-x"]
	if model.InBytes != 6100 || model.OutBytes != 6000 {
		t.Fatalf("model row must sum both legs: %+v", model)
	}
	if model.Tokens != 10 || model.Requests != 1 {
		t.Fatalf("model token counters must be untouched: %+v", model)
	}
	auth := got.Usage.Auths["auth-a"]
	if auth.InBytes != 5000 || auth.OutBytes != 1200 {
		t.Fatalf("auth row carries the provider leg: %+v", auth)
	}

	summary := TrafficSummary(got.Usage)
	if summary["total"] != int64(12100) || summary["client_total"] != int64(5900) || summary["upstream_total"] != int64(6200) {
		t.Fatalf("summary: %+v", summary)
	}
	usage := UsageSummary(got.Usage, true, true, coreusage.Rate{})
	if _, ok := usage["bytes"]; !ok {
		t.Fatal("admin summary must expose traffic")
	}
	if _, ok := UsageSummary(got.Usage, false, false, coreusage.Rate{})["bytes"]; ok {
		t.Fatal("self-service summary must not expose traffic")
	}
}

func TestTraffic_UnattributableEventsAreIgnored(t *testing.T) {
	// Given an unknown key id or a zero-byte event
	// Then nothing is recorded
	s, _ := newRateLimitTestStore(t)
	if s.RecordTraffic("missing", TrafficEvent{InBytes: 1}) {
		t.Fatal("unknown key must not be recorded")
	}
	entry, err := s.Create("sk-cpa-zero", AccessKey{Name: "z"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.RecordTraffic(entry.ID, TrafficEvent{}) {
		t.Fatal("empty event must not be recorded")
	}
}

func TestTraffic_PeriodWindowResetsPeriodBytes(t *testing.T) {
	// Given a monthly quota key with recorded traffic
	// When the UTC month rolls over
	// Then the period counter resets while the all-time total keeps growing
	s, clk := newRateLimitTestStore(t)
	s.now = clk.now
	entry, err := s.Create("sk-cpa-window", AccessKey{Name: "w", Quota: Quota{Period: "monthly"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordTraffic(entry.ID, TrafficEvent{InBytes: 100, OutBytes: 400})
	clk.t = clk.t.Add(26 * 24 * time.Hour)
	s.RecordTraffic(entry.ID, TrafficEvent{InBytes: 10, OutBytes: 40})

	got := s.Get(entry.ID)
	if got.Usage.PeriodBytes != 50 {
		t.Fatalf("period bytes after rollover = %d", got.Usage.PeriodBytes)
	}
	if got.Usage.TotalBytes() != 550 {
		t.Fatalf("total bytes = %d", got.Usage.TotalBytes())
	}
	if len(got.Usage.Daily) != 2 {
		t.Fatalf("daily rows: %+v", got.Usage.Daily)
	}
}

func TestTraffic_ByteQuotaBlocksOverLimitKey(t *testing.T) {
	// Given a key with a monthly byte limit
	// Then consumption below the limit passes and crossing it blocks admission
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-bytequota", AccessKey{Name: "q", Quota: Quota{ByteLimit: 1000, Period: "monthly"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordTraffic(entry.ID, TrafficEvent{InBytes: 400, OutBytes: 400})
	if got := s.Get(entry.ID); got.QuotaExceeded(time.Now()) {
		t.Fatal("under the limit must not exceed")
	}
	s.RecordUsage("sk-cpa-bytequota", UsageEvent{Tokens: 0, UpstreamInBytes: 200, UpstreamOutBytes: 0})
	if got := s.Get(entry.ID); !got.QuotaExceeded(time.Now()) {
		t.Fatal("crossing the byte limit must exceed")
	}
	// The provider leg counts toward the same budget.
	if got := s.Get(entry.ID); got.Usage.PeriodBytes != 1000 {
		t.Fatalf("period bytes = %d", got.Usage.PeriodBytes)
	}
}

func TestUsageTop_RankByBytes(t *testing.T) {
	// Given keys with different traffic
	// Then by=bytes ranks them and the month window reads daily rows
	s, _ := newRateLimitTestStore(t)
	small, _ := s.Create("sk-cpa-b1", AccessKey{Name: "small"})
	big, _ := s.Create("sk-cpa-b2", AccessKey{Name: "big"})
	s.RecordTraffic(small.ID, TrafficEvent{InBytes: 10, OutBytes: 10})
	s.RecordTraffic(big.ID, TrafficEvent{InBytes: 500, OutBytes: 500})

	top := s.UsageTop("bytes", "all", 5)
	if len(top) != 2 || top[0].ID != big.ID || top[0].Bytes != 1000 || top[1].Bytes != 20 {
		t.Fatalf("bytes rank: %+v", top)
	}
	month := s.UsageTop("bytes", "month", 5)
	if len(month) != 2 || month[0].Bytes != 1000 {
		t.Fatalf("month bytes rank: %+v", month)
	}
}

func TestGroupUsage_SumsTraffic(t *testing.T) {
	// Given group members with client and provider leg traffic
	// Then group totals and daily rows carry the byte counters
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "plan-t"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	entry, err := s.Create("sk-cpa-gt", AccessKey{Name: "g", Group: "plan-t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordTraffic(entry.ID, TrafficEvent{InBytes: 100, OutBytes: 200, Model: "m"})
	s.RecordUsage("sk-cpa-gt", UsageEvent{Tokens: 1, Model: "m", UpstreamInBytes: 300, UpstreamOutBytes: 50})

	view, err := s.GroupUsage("plan-t")
	if err != nil {
		t.Fatalf("group usage: %v", err)
	}
	if view.Totals.InBytes != 400 || view.Totals.OutBytes != 250 {
		t.Fatalf("group totals: %+v", view.Totals)
	}
	if view.Models["m"].InBytes != 400 || view.Models["m"].OutBytes != 250 {
		t.Fatalf("group model row: %+v", view.Models["m"])
	}
	grp := s.GetGroup("plan-t")
	if grp == nil || grp.Usage.TotalBytes() != 650 {
		t.Fatalf("group counters: %+v", grp)
	}
}

// Feature: HTTP edge byte counting
//
//   The middleware must attribute the request body bytes read during
//   authentication and the response bytes written by the handler to the key,
//   and it must keep counting after a connection is hijacked for websockets.

func TestGroupAccessMiddleware_RecordsClientBytes(t *testing.T) {
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-edge", AccessKey{Name: "e"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ginRouter := newEdgeTestRouter(t, s, map[string]string{
		"key_id":    entry.ID,
		"req_bytes": "1234",
		"model":     "gpt-edge",
	}, func(c *gin.Context) {
		_, _ = c.Writer.WriteString("hello world")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	ginRouter.ServeHTTP(rec, req)

	got := s.Get(entry.ID)
	if got.Usage.ClientInBytes != 1234 {
		t.Fatalf("client in bytes = %d", got.Usage.ClientInBytes)
	}
	if got.Usage.ClientOutBytes != int64(len("hello world")) {
		t.Fatalf("client out bytes = %d", got.Usage.ClientOutBytes)
	}
	if got.Usage.TotalBytes() != 1234+int64(len("hello world")) {
		t.Fatalf("total bytes = %d", got.Usage.TotalBytes())
	}
	if got.Usage.PeriodBytes != got.Usage.TotalBytes() {
		t.Fatalf("period bytes = %d", got.Usage.PeriodBytes)
	}
	if got.Usage.Models["gpt-edge"].OutBytes != int64(len("hello world")) {
		t.Fatalf("model row: %+v", got.Usage.Models["gpt-edge"])
	}
	if got.Usage.Requests != 0 {
		t.Fatalf("edge accounting must not count requests: %d", got.Usage.Requests)
	}
}

func TestTrafficWriter_HijackCountsBothDirections(t *testing.T) {
	// Given a hijacked connection (websocket upgrade shape)
	// When the client sends bytes and the server writes bytes back
	// Then both directions are counted by the traffic writer
	var counted [2]int64
	done := make(chan struct{})
	ginRouter := newEdgeTestRouter(t, nil, nil, func(c *gin.Context) {
		defer close(done)
		tw := &trafficWriter{ResponseWriter: c.Writer}
		conn, rw, errHijack := tw.Hijack()
		if errHijack != nil {
			t.Errorf("hijack: %v", errHijack)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, errWrite := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); errWrite != nil {
			t.Errorf("handshake write: %v", errWrite)
			return
		}
		if errFlush := rw.Flush(); errFlush != nil {
			t.Errorf("handshake flush: %v", errFlush)
			return
		}
		buf := make([]byte, 64)
		n, errRead := rw.Read(buf)
		if errRead != nil {
			t.Errorf("frame read: %v", errRead)
			return
		}
		if _, errWrite := conn.Write(buf[:n]); errWrite != nil {
			t.Errorf("frame write: %v", errWrite)
			return
		}
		counted[0], counted[1] = tw.inBytes.Load(), tw.outBytes.Load()
	})
	server := httptest.NewServer(ginRouter)
	defer server.Close()

	conn, errDial := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	if _, errWrite := conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n\r\n")); errWrite != nil {
		t.Fatalf("request write: %v", errWrite)
	}
	statusLine, errRead := reader.ReadString('\n')
	if errRead != nil || !strings.Contains(statusLine, "101") {
		t.Fatalf("handshake response = %q err=%v", statusLine, errRead)
	}
	for {
		line, errLine := reader.ReadString('\n')
		if errLine != nil {
			t.Fatalf("handshake headers: %v", errLine)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, errWrite := conn.Write([]byte("ping")); errWrite != nil {
		t.Fatalf("frame write: %v", errWrite)
	}
	echo := make([]byte, 4)
	if _, errReadFull := readFull(reader, echo); errReadFull != nil {
		t.Fatalf("echo read: %v", errReadFull)
	}
	<-done
	if string(echo) != "ping" {
		t.Fatalf("echo = %q", echo)
	}
	if counted[0] != 4 {
		t.Fatalf("hijacked inbound bytes = %d, want 4", counted[0])
	}
	// 5-byte handshake status line + "ping" echo; the counting must not miss
	// the handler's own writes.
	if counted[1] < 4 {
		t.Fatalf("hijacked outbound bytes = %d", counted[1])
	}
}

func readFull(reader *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, errRead := reader.Read(buf[total:])
		total += n
		if errRead != nil {
			return total, errRead
		}
	}
	return total, nil
}

func TestUsageSummary_SelfServiceHidesTraffic(t *testing.T) {
	// Given a key with byte counters on the totals and on every dimension
	// When the summary is built for a self-service caller
	// Then no byte figure appears anywhere and the live entry stays untouched
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-self", AccessKey{Name: "self"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordTraffic(entry.ID, TrafficEvent{InBytes: 11, OutBytes: 22, Model: "gpt-self"})
	s.RecordUsage("sk-cpa-self", UsageEvent{
		Tokens: 7, Model: "gpt-self", AuthID: "auth-self",
		UpstreamInBytes: 33, UpstreamOutBytes: 44,
	})

	got := s.Get(entry.ID)
	if got.Usage.TotalBytes() != 110 {
		t.Fatalf("setup: total bytes = %d", got.Usage.TotalBytes())
	}
	encoded, errMarshal := json.Marshal(UsageSummary(got.Usage, false, false, coreusage.Rate{}))
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	for _, forbidden := range []string{"bytes", "in_bytes", "out_bytes", "client_in", "upstream_in"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("self-service summary leaks %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"total_tokens":7`) {
		t.Fatalf("token totals must survive: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"tokens":7`) {
		t.Fatalf("dimension token rows must survive: %s", encoded)
	}

	// Admin summaries keep the figures, and the live entry was never mutated.
	admin, _ := json.Marshal(UsageSummary(got.Usage, true, true, coreusage.Rate{}))
	if !strings.Contains(string(admin), `"total":110`) {
		t.Fatalf("admin summary must expose traffic: %s", admin)
	}
	after := s.Get(entry.ID)
	if after.Usage.TotalBytes() != 110 || after.Usage.Models["gpt-self"].InBytes != 44 || after.Usage.Models["gpt-self"].OutBytes != 66 {
		t.Fatalf("live usage mutated: %+v", after.Usage)
	}
}

func TestUsage_WithoutTrafficDeepCopiesDimensions(t *testing.T) {
	// Given usage with byte counters in a dimension map
	// When the stripped copy is edited
	// Then the original maps are untouched
	source := Usage{
		ClientInBytes: 5,
		Models:        map[string]DimUsage{"m": {Tokens: 9, InBytes: 7, OutBytes: 8}},
	}
	stripped := source.WithoutTraffic()
	if stripped.ClientInBytes != 0 || stripped.Models["m"].InBytes != 0 || stripped.Models["m"].OutBytes != 0 {
		t.Fatalf("strip failed: %+v", stripped)
	}
	if stripped.Models["m"].Tokens != 9 {
		t.Fatalf("token counters must survive: %+v", stripped.Models)
	}
	stripped.Models["m"] = DimUsage{Tokens: 100}
	if source.Models["m"].Tokens != 9 || source.Models["m"].InBytes != 7 {
		t.Fatalf("source map mutated: %+v", source.Models)
	}
	if source.ClientInBytes != 5 {
		t.Fatalf("source totals mutated: %+v", source)
	}
}
