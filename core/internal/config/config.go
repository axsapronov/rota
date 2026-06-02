package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration
type Config struct {
	ProxyPort int
	APIPort   int
	LogLevel  string
	Database  DatabaseConfig
	AdminUser string
	AdminPass string

	// Auth brute-force protection
	// Per-IP: after AuthIPMaxAttempts failures within AuthIPWindowMinutes,
	// that IP is blocked for AuthIPBlockMinutes.
	// Global: if total login attempts across all IPs exceed AuthGlobalMaxPerMinute
	// in a 1-minute window, the login endpoint is locked for AuthGlobalLockoutMin.
	AuthIPMaxAttempts      int // failed attempts before IP block       (AUTH_IP_MAX_ATTEMPTS, default 10)
	AuthIPWindowMinutes    int // sliding window to count attempts      (AUTH_IP_WINDOW_MINUTES, default 10)
	AuthIPBlockMinutes     int // how long to block an IP               (AUTH_IP_BLOCK_MINUTES, default 30)
	AuthGlobalMaxPerMinute int // max total attempts/min before lockout (AUTH_GLOBAL_MAX_PER_MINUTE, default 1000)
	AuthGlobalLockoutMin   int // global lockout duration in minutes    (AUTH_GLOBAL_LOCKOUT_MINUTES, default 1)

	GeoIP GeoIPConfig
}

// GeoIPConfig holds ip-api.com lookup rate limits and cache settings.
type GeoIPConfig struct {
	QueriesPerMinute     int // GEOIP_QUERIES_PER_MINUTE (default 40, max 45 on free tier)
	BatchMax             int // GEOIP_BATCH_MAX (default 100)
	MaxRetries           int // GEOIP_MAX_RETRIES (default 3)
	CacheTTLHours        int // GEOIP_CACHE_TTL_HOURS (default 24)
	NegativeCacheMinutes int // GEOIP_NEGATIVE_CACHE_MINUTES (default 5)
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

// DSN returns the database connection string
func (d *DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// Load reads configuration from environment variables
func Load() (*Config, error) {
	cfg := &Config{
		ProxyPort: getEnvAsInt("PROXY_PORT", 8000),
		APIPort:   getEnvAsInt("API_PORT", 8001),
		LogLevel:  getEnv("LOG_LEVEL", "info"),
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnvAsInt("DB_PORT", 5432),
			User:     getEnv("DB_USER", "rota"),
			Password: getEnv("DB_PASSWORD", "rota_password"),
			Name:     getEnv("DB_NAME", "rota"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		AdminUser: getEnv("ROTA_ADMIN_USER", "admin"),
		AdminPass: getEnv("ROTA_ADMIN_PASSWORD", "admin"),

		AuthIPMaxAttempts:      getEnvAsInt("AUTH_IP_MAX_ATTEMPTS", 10),
		AuthIPWindowMinutes:    getEnvAsInt("AUTH_IP_WINDOW_MINUTES", 10),
		AuthIPBlockMinutes:     getEnvAsInt("AUTH_IP_BLOCK_MINUTES", 30),
		AuthGlobalMaxPerMinute: getEnvAsInt("AUTH_GLOBAL_MAX_PER_MINUTE", 1000),
		AuthGlobalLockoutMin:   getEnvAsInt("AUTH_GLOBAL_LOCKOUT_MINUTES", 1),

		GeoIP: GeoIPConfig{
			QueriesPerMinute:     getEnvAsInt("GEOIP_QUERIES_PER_MINUTE", 40),
			BatchMax:             getEnvAsInt("GEOIP_BATCH_MAX", 100),
			MaxRetries:           getEnvAsInt("GEOIP_MAX_RETRIES", 3),
			CacheTTLHours:        getEnvAsInt("GEOIP_CACHE_TTL_HOURS", 24),
			NegativeCacheMinutes: getEnvAsInt("GEOIP_NEGATIVE_CACHE_MINUTES", 5),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.ProxyPort < 1 || c.ProxyPort > 65535 {
		return fmt.Errorf("invalid proxy port: %d", c.ProxyPort)
	}
	if c.APIPort < 1 || c.APIPort > 65535 {
		return fmt.Errorf("invalid API port: %d", c.APIPort)
	}
	if c.ProxyPort == c.APIPort {
		return fmt.Errorf("proxy port and API port cannot be the same: %d", c.ProxyPort)
	}

	validLogLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", c.LogLevel)
	}

	if c.GeoIP.QueriesPerMinute < 1 || c.GeoIP.QueriesPerMinute > 45 {
		return fmt.Errorf("invalid GEOIP_QUERIES_PER_MINUTE: %d (must be 1-45)", c.GeoIP.QueriesPerMinute)
	}
	if c.GeoIP.BatchMax < 1 || c.GeoIP.BatchMax > 100 {
		return fmt.Errorf("invalid GEOIP_BATCH_MAX: %d (must be 1-100)", c.GeoIP.BatchMax)
	}
	if c.GeoIP.MaxRetries < 0 || c.GeoIP.MaxRetries > 10 {
		return fmt.Errorf("invalid GEOIP_MAX_RETRIES: %d (must be 0-10)", c.GeoIP.MaxRetries)
	}
	if c.GeoIP.CacheTTLHours < 1 {
		return fmt.Errorf("invalid GEOIP_CACHE_TTL_HOURS: %d (must be >= 1)", c.GeoIP.CacheTTLHours)
	}
	if c.GeoIP.NegativeCacheMinutes < 0 {
		return fmt.Errorf("invalid GEOIP_NEGATIVE_CACHE_MINUTES: %d (must be >= 0)", c.GeoIP.NegativeCacheMinutes)
	}

	return nil
}

// getEnv retrieves an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAsInt retrieves an environment variable as an integer or returns a default value
func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
