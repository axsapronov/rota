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
