package storeaccess

import (
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
		defer releaseAll()
		c.Next()
	}
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
