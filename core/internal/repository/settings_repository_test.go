package repository

import (
	"testing"

	"github.com/alpkeskin/rota/core/internal/models"
)

// TestMapToSettingsBodyStrategyNormalization verifies the body_pattern →
// strategy migration: a stored healthcheck row carrying the legacy
// body_pattern (and no strategy) loads as the regex strategy with the same
// value, and a row with neither loads as the default ip strategy.
func TestMapToSettingsBodyStrategyNormalization(t *testing.T) {
	r := &SettingsRepository{}

	t.Run("legacy body_pattern becomes regex strategy", func(t *testing.T) {
		s, err := r.mapToSettings(map[string]map[string]any{
			"healthcheck": {
				"timeout":      60,
				"workers":      20,
				"url":          "https://api.ipify.org",
				"status":       200,
				"body_pattern": `^\d{1,3}(\.\d{1,3}){3}\s*$`,
			},
		})
		if err != nil {
			t.Fatalf("mapToSettings: %v", err)
		}
		if s.HealthCheck.Strategy != models.StrategyRegex {
			t.Fatalf("strategy = %q, want %q", s.HealthCheck.Strategy, models.StrategyRegex)
		}
		if s.HealthCheck.StrategyValue != `^\d{1,3}(\.\d{1,3}){3}\s*$` {
			t.Fatalf("strategy_value = %q, want the legacy pattern", s.HealthCheck.StrategyValue)
		}
	})

	t.Run("empty strategy resolves to ip", func(t *testing.T) {
		s, err := r.mapToSettings(map[string]map[string]any{
			"healthcheck": {
				"timeout": 60,
				"workers": 20,
				"url":     "https://api.ipify.org",
				"status":  200,
			},
		})
		if err != nil {
			t.Fatalf("mapToSettings: %v", err)
		}
		if s.HealthCheck.Strategy != models.StrategyIP {
			t.Fatalf("strategy = %q, want %q", s.HealthCheck.Strategy, models.StrategyIP)
		}
	})

	t.Run("explicit strategy is preserved", func(t *testing.T) {
		s, err := r.mapToSettings(map[string]map[string]any{
			"healthcheck": {
				"strategy":       "status",
				"strategy_value": "ignored",
			},
		})
		if err != nil {
			t.Fatalf("mapToSettings: %v", err)
		}
		if s.HealthCheck.Strategy != models.StrategyStatus {
			t.Fatalf("strategy = %q, want %q", s.HealthCheck.Strategy, models.StrategyStatus)
		}
	})
}
