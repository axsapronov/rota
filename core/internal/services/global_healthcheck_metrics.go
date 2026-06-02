package services

import (
	"time"

	"github.com/alpkeskin/rota/core/internal/checkstats"
)

// GlobalHealthCheckMetricsSnapshot is the API shape for orphan periodic HC metrics.
type GlobalHealthCheckMetricsSnapshot = checkstats.GlobalHealthCheckSnapshot

// GlobalHealthCheckMetrics returns orphan periodic HC run state.
func GlobalHealthCheckMetrics() GlobalHealthCheckMetricsSnapshot {
	return checkstats.GetGlobalHealthCheckSnapshot()
}

// SetGlobalHealthCheckConfig updates global periodic HC enabled state and interval.
func SetGlobalHealthCheckConfig(enabled bool, interval time.Duration) {
	checkstats.SetGlobalHealthCheckConfig(enabled, interval)
}
