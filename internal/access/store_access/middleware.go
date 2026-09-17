package storeaccess

import (
	"bufio"
	"net"
	"strconv"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// GinContextAllowedAuthsKey carries the group's allowed upstream auth list
// from this middleware to the execution metadata builder.
const GinContextAllowedAuthsKey = "accessAllowedAuths"

// GroupAccessMiddleware enforces per-key and per-group rate/concurrency
// limits and exposes the group's auth allowlist on the gin context for
// downstream auth selection. It must run after the access auth middleware so
// accessMetadata is populated. Key limits are checked before group limits so
// a per-key violation reports the key's own limit.
func GroupAccessMiddleware(store *Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil {
			c.Next()
			return
		}
		meta := accessMetadataFromContext(c)
		var releases []func()
		releaseAll := func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		}
		if keyID := meta["key_id"]; keyID != "" {
			release, retryAfter, err := store.AcquireKey(keyID)
			if err != nil {
				abortRateLimited(c, retryAfter, err.Error())
				return
			}
			releases = append(releases, release)
		}
		if grp := store.GetGroup(meta["group"]); grp != nil {
			release, retryAfter, err := store.AcquireGroup(grp.Name)
			if err != nil {
				releaseAll()
				abortRateLimited(c, retryAfter, err.Error())
				return
			}
			releases = append(releases, release)
			if len(grp.AllowedAuths) > 0 {
				c.Set(GinContextAllowedAuthsKey, grp.AllowedAuths)
			}
		}
		// Byte accounting for the client leg. The provider leg is recorded by
		// the usage plugin once the executor publishes the request's usage.
		tw := &trafficWriter{ResponseWriter: c.Writer}
		c.Writer = tw
		defer func() {
			store.RecordTraffic(meta["key_id"], TrafficEvent{
				InBytes:  parseByteCount(meta["req_bytes"]) + tw.inBytes.Load(),
				OutBytes: int64(tw.Size()) + tw.outBytes.Load(),
				Model:    meta["model"],
			})
		}()
		defer releaseAll()
		c.Next()
	}
}

// parseByteCount reads a non-negative decimal byte count from access metadata.
func parseByteCount(value string) int64 {
	if value == "" {
		return 0
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// trafficWriter counts response bytes leaving the process. Writes that go
// through the gin writer are reflected by Size(); hijacked connections
// (codex responses websocket, realtime) bypass it and are counted through
// trafficConn instead.
type trafficWriter struct {
	gin.ResponseWriter
	inBytes  atomic.Int64
	outBytes atomic.Int64
}

// Hijack wraps the hijacked connection so websocket frames keep contributing
// to the key's traffic counters.
func (w *trafficWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, errHijack := w.ResponseWriter.Hijack()
	if errHijack != nil {
		return conn, rw, errHijack
	}
	counted := &trafficConn{Conn: conn, in: &w.inBytes, out: &w.outBytes}
	// net/http may hand back a buffered reader; gorilla/websocket rejects
	// connections that carry buffered handshake data, so a fresh pair over the
	// counting connection observes every later read and write.
	return counted, bufio.NewReadWriter(bufio.NewReader(counted), bufio.NewWriter(counted)), nil
}

type trafficConn struct {
	net.Conn
	in  *atomic.Int64
	out *atomic.Int64
}

func (c *trafficConn) Read(p []byte) (int, error) {
	n, errRead := c.Conn.Read(p)
	if n > 0 {
		c.in.Add(int64(n))
	}
	return n, errRead
}

func (c *trafficConn) Write(p []byte) (int, error) {
	n, errWrite := c.Conn.Write(p)
	if n > 0 {
		c.out.Add(int64(n))
	}
	return n, errWrite
}

// accessMetadataFromContext extracts the metadata map set by the store access
// provider during authentication.
func accessMetadataFromContext(c *gin.Context) map[string]string {
	raw, ok := c.Get("accessMetadata")
	if !ok {
		return nil
	}
	switch meta := raw.(type) {
	case map[string]string:
		return meta
	case map[string]any:
		out := make(map[string]string, len(meta))
		for k, v := range meta {
			if s, okStr := v.(string); okStr {
				out[k] = s
			}
		}
		return out
	}
	return nil
}
