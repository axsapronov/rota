package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// ProxyCleanupService periodically deletes dead/low-quality proxies based on
// the proxy_cleanup settings row.
type ProxyCleanupService struct {
	proxyRepo    *repository.ProxyRepository
	settingsRepo *repository.SettingsRepository
	log          *logger.Logger
	interval     time.Duration
}

// NewProxyCleanupService creates a new ProxyCleanupService.
func NewProxyCleanupService(
	proxyRepo *repository.ProxyRepository,
	settingsRepo *repository.SettingsRepository,
	log *logger.Logger,
) *ProxyCleanupService {
	return &ProxyCleanupService{
		proxyRepo:    proxyRepo,
		settingsRepo: settingsRepo,
		log:          log,
		interval:     time.Hour, // check every hour; actual run interval from settings
	}
}

// Start launches the background cleanup loop.
func (s *ProxyCleanupService) Start(ctx context.Context) {
	if err := s.SyncSettings(ctx); err != nil {
		s.log.Warn("proxy cleanup: failed to sync settings on start", "error", err)
	}

	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.run(ctx)
			}
		}
	}()
	s.log.Info("proxy cleanup service started")
}

// SyncSettings refreshes cleanup metrics/config from persisted settings without running deletion.
func (s *ProxyCleanupService) SyncSettings(ctx context.Context) error {
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	SetProxyCleanupConfig(cfg.Enabled, cfg.MaxFailedDays, cfg.MinSuccessRate, cfg.CleanupIntervalHours)
	return nil
}

func (s *ProxyCleanupService) run(ctx context.Context) {
	startedAt := time.Now()
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		s.log.Warn("proxy cleanup: failed to load settings", "error", err)
		RecordProxyCleanupRun(startedAt, 0, err)
		return
	}
	s.executeCleanup(ctx, cfg, startedAt)
}

// RunNow triggers proxy cleanup immediately via API/manual action.
func (s *ProxyCleanupService) RunNow(ctx context.Context) (int, error) {
	startedAt := time.Now()
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		RecordProxyCleanupRun(startedAt, 0, err)
		return 0, err
	}
	if !cfg.Enabled {
		err = fmt.Errorf("proxy cleanup is disabled in settings")
		RecordProxyCleanupRun(startedAt, 0, err)
		return 0, err
	}
	return s.executeCleanup(ctx, cfg, startedAt)
}

func (s *ProxyCleanupService) executeCleanup(ctx context.Context, cfg models.ProxyCleanupSettings, startedAt time.Time) (int, error) {
	SetProxyCleanupConfig(cfg.Enabled, cfg.MaxFailedDays, cfg.MinSuccessRate, cfg.CleanupIntervalHours)
	if !cfg.Enabled {
		return 0, nil
	}

	deleted, err := s.proxyRepo.DeleteDeadProxies(ctx, cfg.MaxFailedDays, cfg.MinSuccessRate)
	if err != nil {
		s.log.Error("proxy cleanup: delete failed", "error", err)
		RecordProxyCleanupRun(startedAt, 0, err)
		return 0, err
	}
	RecordProxyCleanupRun(startedAt, int64(deleted), nil)
	if deleted > 0 {
		s.log.Info("proxy cleanup: removed dead proxies", "count", deleted)
	}
	return deleted, nil
}

func (s *ProxyCleanupService) loadSettings(ctx context.Context) (models.ProxyCleanupSettings, error) {
	var cfg models.ProxyCleanupSettings
	// Defaults
	cfg.Enabled = false
	cfg.MaxFailedDays = 7
	cfg.CleanupIntervalHours = 24

	m, err := s.settingsRepo.Get(ctx, "proxy_cleanup")
	if err != nil || m == nil {
		return cfg, nil
	}
	// Marshal map back to JSON then decode into struct
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
