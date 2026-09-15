package redisqueue

import "sync/atomic"

var usageStatisticsEnabled atomic.Bool

func init() {
	usageStatisticsEnabled.Store(true)
}

// SetUsageStatisticsEnabled toggles usage monitoring and redisqueue payload collection.
// This is controlled by the config field `usage-statistics-enabled` and the corresponding management API.
// Disabling collection preserves the persisted monitor history until it is explicitly reset.
func SetUsageStatisticsEnabled(enabled bool) {
	usageStatisticsEnabled.Store(enabled)
}

// UsageStatisticsEnabled reports whether the usage plugin should collect records.
func UsageStatisticsEnabled() bool { return usageStatisticsEnabled.Load() }
