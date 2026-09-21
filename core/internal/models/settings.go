package models

import "time"

// Settings represents system configuration
type Settings struct {
	Authentication    AuthenticationSettings    `json:"authentication"`
	Rotation          RotationSettings          `json:"rotation"`
	RateLimit         RateLimitSettings         `json:"rate_limit"`
	HealthCheck       HealthCheckSettings       `json:"healthcheck"`
	GlobalHealthCheck GlobalHealthCheckSettings `json:"global_health_check"`
	LogRetention      LogRetentionSettings      `json:"log_retention"`
	ProxyCleanup      ProxyCleanupSettings      `json:"proxy_cleanup"`
	GeoIP             GeoIPSettings             `json:"geoip"`
}

// AuthenticationSettings represents proxy server authentication configuration
// This controls authentication for incoming requests to the PROXY server (port 8000)
// NOT for dashboard/API login (which uses ROTA_ADMIN_USER/ROTA_ADMIN_PASSWORD)
type AuthenticationSettings struct {
	Enabled  bool   `json:"enabled"`  // Enable authentication for proxy requests
	Username string `json:"username"` // Username for proxy authentication
	Password string `json:"password"` // Password for proxy authentication (write-only)
}

// RotationSettings represents proxy rotation configuration
type RotationSettings struct {
	Method             string            `json:"method"`
	TimeBased          TimeBasedSettings `json:"time_based,omitempty"`
	RemoveUnhealthy    bool              `json:"remove_unhealthy"`
	Fallback           bool              `json:"fallback"`
	FallbackMaxRetries int               `json:"fallback_max_retries"`
	FollowRedirect     bool              `json:"follow_redirect"`
	Timeout            int               `json:"timeout"`
	Retries            int               `json:"retries"`
	AllowedProtocols   []string          `json:"allowed_protocols"` // ["http", "https", "socks4", "socks4a", "socks5"], empty means all
	MaxResponseTime    int               `json:"max_response_time"` // in milliseconds, 0 means no limit
	MinSuccessRate     float64           `json:"min_success_rate"`  // 0-100, 0 means no minimum
}

// TimeBasedSettings represents time-based rotation settings
type TimeBasedSettings struct {
	Interval int `json:"interval"` // in seconds
}

// RateLimitSettings represents rate limiting configuration
type RateLimitSettings struct {
	Enabled     bool `json:"enabled"`
	Interval    int  `json:"interval"` // in seconds
	MaxRequests int  `json:"max_requests"`
}

// HealthCheckSettings represents health check configuration
type HealthCheckSettings struct {
	Timeout   int      `json:"timeout"`
	Workers   int      `json:"workers"`
	URL       string   `json:"url"`
	Status    int      `json:"status"`
	Headers   []string `json:"headers"`
	StrictTLS bool     `json:"strict_tls"` // Enable real TLS certificate validation during health checks
	// Strategy selects how the health-check response body is validated after
	// the status-code check (see Strategy* constants). Empty normalizes to
	// StrategyIP when settings are loaded, so the default is "body must
	// contain an IP" — catches proxies that answer the check URL with a valid
	// status code but junk body (captive-portal HTML, login pages).
	Strategy string `json:"strategy"`
	// StrategyValue is the value for the StrategyContains / StrategyRegex
	// strategies (substring / Go regex); unused by the other strategies.
	StrategyValue string `json:"strategy_value"`
	// OrphanTTLMinutes excludes orphan proxies checked more recently than
	// now()-TTL from periodic orphan sweeps (0 disables the filter).
	OrphanTTLMinutes int `json:"orphan_ttl_minutes"`
	// IdleTTLMinutes is the same TTL filter for idle-orphan sweeps.
	IdleTTLMinutes int `json:"idle_ttl_minutes"`
}

// Health-check body-validation strategies (HealthCheckSettings.Strategy).
const (
	StrategyStatus   = "status"   // status code only, no body validation
	StrategyIP       = "ip"       // body must contain an IPv4/IPv6 address (default)
	StrategyContains = "contains" // body must contain StrategyValue as a substring
	StrategyRegex    = "regex"    // body must match the Go regex in StrategyValue
)

// Default health-check TTLs (minutes). Conservative: 6h for orphan sweeps,
// 24h for idle sweeps. 0 explicitly disables the TTL filter.
const (
	DefaultOrphanTTLMinutes = 360
	DefaultIdleTTLMinutes   = 1440
)

// GlobalHealthCheckSettings represents the scheduled global (orphan) health
// check: proxies not attached to any pool are checked on this interval.
type GlobalHealthCheckSettings struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
}

// LogRetentionSettings represents log retention and cleanup configuration
type LogRetentionSettings struct {
	Enabled              bool `json:"enabled"`                // Enable automatic log cleanup
	RetentionDays        int  `json:"retention_days"`         // Days to keep logs (7, 15, 30, 60, 90)
	CompressionAfterDays int  `json:"compression_after_days"` // Compress logs older than X days (1, 3, 7, 14)
	CleanupIntervalHours int  `json:"cleanup_interval_hours"` // How often to run cleanup (1, 6, 12, 24)
}

// ProxyCleanupSettings represents dead proxy auto-removal configuration
type ProxyCleanupSettings struct {
	Enabled              bool    `json:"enabled"`                // Enable automatic dead proxy cleanup
	MaxFailedDays        int     `json:"max_failed_days"`        // Remove proxies failed for more than N days
	MinSuccessRate       float64 `json:"min_success_rate"`       // Remove proxies with success rate below X% (0 = disabled)
	CleanupIntervalHours int     `json:"cleanup_interval_hours"` // How often to run cleanup
}

// GeoIPSettings represents GeoIP geolocation configuration
type GeoIPSettings struct {
	Provider            string    `json:"provider"`              // "ip-api" or "maxmind"
	MaxMindLicenseKey   string    `json:"maxmind_license_key"`   // MaxMind license key for auto-download
	MaxMindDBPath       string    `json:"maxmind_db_path"`       // Path to local .mmdb file
	MaxMindURL          string    `json:"maxmind_url"`           // Custom download URL (optional)
	AutoUpdate          bool      `json:"auto_update"`           // Enable automatic DB update on interval
	UpdateIntervalHours int       `json:"update_interval_hours"` // How often to update in hours (default 168 = 7 days)
	LastUpdatedAt       time.Time `json:"last_updated_at"`       // Timestamp of last successful update
}

// SettingRecord represents a settings database record
type SettingRecord struct {
	Key       string         `json:"key"`
	Value     map[string]any `json:"value"`
	UpdatedAt time.Time      `json:"updated_at"`
}
