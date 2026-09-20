package handlers

import (
	"testing"

	"github.com/alpkeskin/rota/core/internal/models"
)

// validBaseSettings returns a settings payload that passes all other
// validations, so only the global health check interval is under test.
func validBaseSettings(intervalMinutes int) *models.Settings {
	return &models.Settings{
		Rotation: models.RotationSettings{
			Timeout: 90,
			Retries: 3,
		},
		HealthCheck: models.HealthCheckSettings{
			Timeout: 60,
			Workers: 20,
		},
		GlobalHealthCheck: models.GlobalHealthCheckSettings{
			IntervalMinutes: intervalMinutes,
		},
	}
}

func TestValidateSettings_healthCheckTTLs(t *testing.T) {
	h := &SettingsHandler{}

	cases := []struct {
		name      string
		orphanTTL int
		idleTTL   int
		wantErr   bool
	}{
		{"negative orphan", -1, 1440, true},
		{"negative idle", 360, -1, true},
		{"zero disables filter", 0, 0, false},
		{"defaults", 360, 1440, false},
		{"maximum", 10080, 10080, false},
		{"above maximum orphan", 10081, 1440, true},
		{"above maximum idle", 360, 10081, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validBaseSettings(30)
			s.HealthCheck.OrphanTTLMinutes = c.orphanTTL
			s.HealthCheck.IdleTTLMinutes = c.idleTTL
			err := h.validateSettings(s)
			if c.wantErr && err == nil {
				t.Errorf("expected an error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateSettings_globalHealthCheckInterval(t *testing.T) {
	h := &SettingsHandler{}

	cases := []struct {
		interval int
		wantErr  bool
	}{
		{0, true},     // below minimum
		{1, false},    // minimum
		{30, false},   // default
		{1440, false}, // maximum
		{1441, true},  // above maximum
	}
	for _, c := range cases {
		err := h.validateSettings(validBaseSettings(c.interval))
		if c.wantErr && err == nil {
			t.Errorf("interval %d: expected an error, got nil", c.interval)
		}
		if !c.wantErr && err != nil {
			t.Errorf("interval %d: unexpected error: %v", c.interval, err)
		}
	}
}
