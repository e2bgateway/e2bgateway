// Package config handles configuration loading and validation for E2BGateway.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration for E2BGateway.
type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	Backends      []BackendConfig     `mapstructure:"backends"`
	Auth          AuthConfig          `mapstructure:"auth"`
	RateLimit     RateLimitConfig     `mapstructure:"rateLimit"`
	Routing       RoutingConfig       `mapstructure:"routing"`
	Cache         CacheConfig         `mapstructure:"cache"`
	Observability ObservabilityConfig `mapstructure:"observability"`
}

// ServerConfig defines HTTP/HTTPS server settings.
type ServerConfig struct {
	HTTP       HTTPConfig  `mapstructure:"http"`
	HTTPS      HTTPSConfig `mapstructure:"https"`
	EnvdDomain string      `mapstructure:"envdDomain"` // domain the SDK uses to reach envd (e.g. "e2b.example.com")
}

// HTTPConfig defines HTTP listener settings.
type HTTPConfig struct {
	Address      string        `mapstructure:"address"`
	ReadTimeout  time.Duration `mapstructure:"readTimeout"`
	WriteTimeout time.Duration `mapstructure:"writeTimeout"`
	IdleTimeout  time.Duration `mapstructure:"idleTimeout"`
}

// HTTPSConfig defines HTTPS listener settings.
type HTTPSConfig struct {
	Address  string `mapstructure:"address"`
	CertFile string `mapstructure:"certFile"`
	KeyFile  string `mapstructure:"keyFile"`
}

// BackendConfig defines a sandbox backend adapter.
type BackendConfig struct {
	Name    string                 `mapstructure:"name"`
	Type    string                 `mapstructure:"type"` // e2b-cloud, agent-sandbox, opensandbox
	Enabled bool                   `mapstructure:"enabled"`
	Config  map[string]interface{} `mapstructure:"config"`
}

// AuthConfig defines authentication settings.
type AuthConfig struct {
	Providers []AuthProviderConfig `mapstructure:"providers"`
}

// AuthProviderConfig defines a single auth provider.
type AuthProviderConfig struct {
	Type            string `mapstructure:"type"` // apikey, jwt, mtls
	SecretRef       string `mapstructure:"secretRef"`
	HeaderName      string `mapstructure:"headerName"`
	AlternateHeader string `mapstructure:"alternateHeader"`
	BearerPrefix    bool   `mapstructure:"bearerPrefix"`
	// Keys is a list of static API keys (for apikey provider).
	Keys []string `mapstructure:"keys"`
	// JWT-specific
	Issuer   string `mapstructure:"issuer"`
	JWKSURL  string `mapstructure:"jwksURL"`
	Audience string `mapstructure:"audience"`
}

// RateLimitConfig defines rate limiting settings.
type RateLimitConfig struct {
	Enabled         bool                         `mapstructure:"enabled"`
	Backend         string                       `mapstructure:"backend"` // memory, redis
	DefaultLimit    RateLimitDefaultConfig       `mapstructure:"defaultLimit"`
	Redis           RedisConfig                  `mapstructure:"redis"`
	TenantOverrides map[string]RateLimitOverride `mapstructure:"tenantOverrides"`
}

// RateLimitDefaultConfig defines default rate limits.
type RateLimitDefaultConfig struct {
	RequestsPerMinute int `mapstructure:"requestsPerMinute"`
	BurstSize         int `mapstructure:"burstSize"`
}

// RateLimitOverride defines per-tenant rate limit overrides.
type RateLimitOverride struct {
	RequestsPerMinute int `mapstructure:"requestsPerMinute"`
	BurstSize         int `mapstructure:"burstSize"`
}

// RedisConfig defines Redis connection settings.
type RedisConfig struct {
	Address  string `mapstructure:"address"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// RoutingConfig defines request routing settings.
type RoutingConfig struct {
	DefaultBackend string            `mapstructure:"defaultBackend"`
	Strategy       string            `mapstructure:"strategy"` // static, template-based, weighted, priority
	Strategies     []RoutingStrategy `mapstructure:"strategies"`
	HealthCheck    HealthCheckConfig `mapstructure:"healthCheck"`
	Failover       FailoverConfig    `mapstructure:"failover"`
}

// RoutingStrategy defines a named routing strategy with rules.
type RoutingStrategy struct {
	Name  string        `mapstructure:"name"`
	Rules []RoutingRule `mapstructure:"rules"`
}

// RoutingRule defines a single routing rule.
type RoutingRule struct {
	Tenant   string `mapstructure:"tenant"`
	Template string `mapstructure:"template"`
	Backend  string `mapstructure:"backend"`
}

// HealthCheckConfig defines backend health check settings.
type HealthCheckConfig struct {
	Interval           time.Duration `mapstructure:"interval"`
	Timeout            time.Duration `mapstructure:"timeout"`
	UnhealthyThreshold int           `mapstructure:"unhealthyThreshold"`
	HealthyThreshold   int           `mapstructure:"healthyThreshold"`
}

// FailoverConfig defines failover settings.
type FailoverConfig struct {
	Enabled bool     `mapstructure:"enabled"`
	Chain   []string `mapstructure:"chain"`
}

// CacheConfig defines caching settings.
type CacheConfig struct {
	Backend string       `mapstructure:"backend"` // memory, redis
	Memory  MemoryConfig `mapstructure:"memory"`
	Redis   RedisConfig  `mapstructure:"redis"`
}

// MemoryConfig defines in-memory cache settings.
type MemoryConfig struct {
	MaxSize    int           `mapstructure:"maxSize"`
	DefaultTTL time.Duration `mapstructure:"defaultTTL"`
}

// ObservabilityConfig defines observability settings.
type ObservabilityConfig struct {
	OTel    OTelConfig    `mapstructure:"otel"`
	Metrics MetricsConfig `mapstructure:"metrics"`
	Logging LoggingConfig `mapstructure:"logging"`
}

// OTelConfig defines OpenTelemetry settings.
type OTelConfig struct {
	Enabled          bool    `mapstructure:"enabled"`
	ServiceNamespace string  `mapstructure:"serviceNamespace"`
	Endpoint         string  `mapstructure:"endpoint"`
	Insecure         bool    `mapstructure:"insecure"`
	SamplingRatio    float64 `mapstructure:"samplingRatio"`
}

// MetricsConfig defines metrics settings.
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Path    string `mapstructure:"path"`
	Prefix  string `mapstructure:"prefix"`
}

// LoggingConfig defines logging settings.
type LoggingConfig struct {
	Level    string `mapstructure:"level"`
	Format   string `mapstructure:"format"`
	AuditLog bool   `mapstructure:"auditLog"`
}

// Load reads configuration from file, environment variables, and defaults.
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set defaults
	setDefaults(v)

	// Read config file
	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
	}

	// Environment variable overrides
	v.SetEnvPrefix("E2BGW")
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.http.address", "0.0.0.0:8080")
	v.SetDefault("server.http.readTimeout", "30s")
	v.SetDefault("server.http.writeTimeout", "300s")
	v.SetDefault("server.http.idleTimeout", "120s")
	v.SetDefault("rateLimit.enabled", true)
	v.SetDefault("rateLimit.backend", "memory")
	v.SetDefault("rateLimit.defaultLimit.requestsPerMinute", 100)
	v.SetDefault("rateLimit.defaultLimit.burstSize", 20)
	v.SetDefault("routing.strategy", "static")
	v.SetDefault("cache.backend", "memory")
	v.SetDefault("cache.memory.maxSize", 1000)
	v.SetDefault("cache.memory.defaultTTL", "30s")
	v.SetDefault("observability.metrics.enabled", true)
	v.SetDefault("observability.metrics.path", "/metrics")
	v.SetDefault("observability.metrics.prefix", "e2bgw")
	v.SetDefault("observability.logging.level", "info")
	v.SetDefault("observability.logging.format", "json")
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.Server.HTTP.Address == "" {
		return fmt.Errorf("server.http.address is required")
	}
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one backend must be configured")
	}
	enabledBackends := 0
	for _, b := range c.Backends {
		if b.Enabled {
			enabledBackends++
		}
		if b.Name == "" {
			return fmt.Errorf("backend name is required")
		}
		if b.Type == "" {
			return fmt.Errorf("backend type is required for %q", b.Name)
		}
	}
	if enabledBackends == 0 {
		return fmt.Errorf("at least one backend must be enabled")
	}
	if c.Routing.DefaultBackend == "" && enabledBackends > 0 {
		// Auto-select first enabled backend
		for _, b := range c.Backends {
			if b.Enabled {
				c.Routing.DefaultBackend = b.Name
				break
			}
		}
	}
	return nil
}

// LoadFromEnv is a convenience function for testing.
func LoadFromEnv() (*Config, error) {
	configPath := os.Getenv("E2BGW_CONFIG_FILE")
	return Load(configPath)
}
