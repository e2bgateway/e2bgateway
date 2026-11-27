// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/e2bgateway/e2bgateway/internal/redisx"
)

// Config is the top-level configuration for E2BGateway.
type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	Backends      []BackendConfig     `mapstructure:"backends"`
	Auth          AuthConfig          `mapstructure:"auth"`
	RateLimit     RateLimitConfig     `mapstructure:"rateLimit"`
	Routing       RoutingConfig       `mapstructure:"routing"`
	Cache         CacheConfig         `mapstructure:"cache"`
	SessionStore  SessionStoreConfig  `mapstructure:"sessionStore"`
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
	Address        string        `mapstructure:"address"`
	CertFile       string        `mapstructure:"certFile"`
	KeyFile        string        `mapstructure:"keyFile"`
	ReloadInterval time.Duration `mapstructure:"reloadInterval"`
}

// BackendConfig defines a sandbox backend adapter.
type BackendConfig struct {
	Name    string         `mapstructure:"name"`
	Type    string         `mapstructure:"type"` // e2b-cloud, agent-sandbox, opensandbox
	Enabled bool           `mapstructure:"enabled"`
	Config  map[string]any `mapstructure:"config"`
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
	Issuer    string        `mapstructure:"issuer"`
	JWKS      string        `mapstructure:"jwks"`    // Public JWKS JSON, configured locally.
	JWKSURL   string        `mapstructure:"jwksURL"` // Reserved for the dynamic JWKS follow-up PR.
	Audience  string        `mapstructure:"audience"`
	ClockSkew time.Duration `mapstructure:"clockSkew"`
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
	Backend string            `mapstructure:"backend"` // memory, redis
	Memory  MemoryConfig      `mapstructure:"memory"`
	Redis   RedisConfig       `mapstructure:"redis"`
	Tiered  TieredCacheConfig `mapstructure:"tiered"`
}

// MemoryConfig defines in-memory cache settings.
type MemoryConfig struct {
	MaxSize    int           `mapstructure:"maxSize"`
	DefaultTTL time.Duration `mapstructure:"defaultTTL"`
}

// TieredCacheConfig configures the L1 (in-memory) + L2 (Redis) tiered cache
// with cross-replica invalidation (issue #113, design §15.2). Disabled by
// default; enabled without l2.address runs L1-only.
type TieredCacheConfig struct {
	Enabled bool           `mapstructure:"enabled"`
	L1      TieredL1Config `mapstructure:"l1"`
	L2      TieredL2Config `mapstructure:"l2"`
}

// TieredL1Config defines the per-pod in-memory tier.
type TieredL1Config struct {
	MaxSize    int           `mapstructure:"maxSize"`
	DefaultTTL time.Duration `mapstructure:"defaultTTL"`
}

// TieredL2Config defines the shared Redis tier and invalidation Pub/Sub.
type TieredL2Config struct {
	RedisConfig   `mapstructure:",squash"`
	TLS           bool          `mapstructure:"tls"`
	KeyPrefix     string        `mapstructure:"keyPrefix"`
	PubsubChannel string        `mapstructure:"pubsubChannel"`
	Timeout       time.Duration `mapstructure:"timeout"`
}

// validate rejects unusable tiered-cache configurations. Cached payloads
// carry no credentials, so non-loopback plaintext is a startup WARN (handled
// in server wiring), but sending a password in cleartext is rejected.
func (c *TieredCacheConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.L1.MaxSize <= 0 {
		return fmt.Errorf("cache.tiered.l1.maxSize must be > 0")
	}
	if c.L1.DefaultTTL <= 0 {
		return fmt.Errorf("cache.tiered.l1.defaultTTL must be > 0")
	}
	if c.L2.Timeout <= 0 {
		return fmt.Errorf("cache.tiered.l2.timeout must be > 0")
	}
	if c.L2.Address != "" {
		addr, schemeTLS := redisx.NormalizeRedisAddr(c.L2.Address)
		if c.L2.Password != "" && !c.L2.TLS && !schemeTLS && redisx.AddrRequiresTLS(addr) {
			return fmt.Errorf("cache.tiered.l2: password would be sent to non-loopback redis %q without TLS; set tls: true or use a rediss:// address", c.L2.Address)
		}
	}
	return nil
}

// SessionStoreConfig defines the shared data-plane session store (envd access
// tokens + sandbox→backend routing). type: redis is required to run more than
// one gateway replica; type: memory (default) keeps per-process state and
// single-replica behavior.
type SessionStoreConfig struct {
	Type       string        `mapstructure:"type"` // memory | redis
	Addr       string        `mapstructure:"addr"`
	Password   string        `mapstructure:"password"`
	DB         int           `mapstructure:"db"`
	KeyPrefix  string        `mapstructure:"keyPrefix"`
	EnableTLS  bool          `mapstructure:"tls"`
	RoutingTTL time.Duration `mapstructure:"routingTTL"`
	// AllowPlaintext explicitly permits a non-loopback redis WITHOUT TLS.
	// The store carries envd bearer tokens, so the default is fail-closed;
	// set this only on trusted networks (e.g. in-cluster redis isolated by
	// NetworkPolicy). Using it logs a loud warning at startup. The
	// password-requires-TLS rule is NOT waived by this flag.
	AllowPlaintext bool `mapstructure:"allowPlaintext"`
}

// validate normalizes defaults in place and rejects unusable session-store
// configurations before the server starts.
func (c *SessionStoreConfig) validate() error {
	switch c.Type {
	case "":
		c.Type = "memory"
	case "memory":
	case "redis":
		if c.Addr == "" {
			return fmt.Errorf("sessionStore: addr is required when type is redis")
		}
		if c.DB < 0 {
			return fmt.Errorf("sessionStore: db must be >= 0")
		}
		addr, schemeTLS := redisx.NormalizeRedisAddr(c.Addr)
		if _, err := redisx.TLSConfig(addr, c.EnableTLS || schemeTLS, c.Password != ""); err != nil {
			return fmt.Errorf("sessionStore: %w", err)
		}
		// Stricter than the warm-pool rule (CWE-319): the session store
		// carries envd bearer tokens, so ANY non-loopback redis requires TLS
		// regardless of password.
		if !c.EnableTLS && !schemeTLS && !c.AllowPlaintext && redisx.AddrRequiresTLS(addr) {
			return fmt.Errorf("sessionStore: redis addr %q is not loopback: enable TLS (tls: true, or a rediss:// addr) — the store carries envd bearer tokens; on a trusted network (e.g. in-cluster with NetworkPolicy) set allowPlaintext: true to opt out explicitly", c.Addr)
		}
	default:
		return fmt.Errorf("sessionStore: unsupported type %q (want memory or redis)", c.Type)
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "e2bgw:session:"
	}
	if c.RoutingTTL <= 0 {
		c.RoutingTTL = 24 * time.Hour
	}
	return nil
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
	v.SetDefault("server.https.reloadInterval", "30s")
	v.SetDefault("rateLimit.enabled", true)
	v.SetDefault("rateLimit.backend", "memory")
	v.SetDefault("rateLimit.defaultLimit.requestsPerMinute", 100)
	v.SetDefault("rateLimit.defaultLimit.burstSize", 20)
	v.SetDefault("routing.strategy", "static")
	v.SetDefault("sessionStore.type", "memory")
	v.SetDefault("sessionStore.keyPrefix", "e2bgw:session:")
	v.SetDefault("sessionStore.routingTTL", "24h")
	v.SetDefault("cache.backend", "memory")
	v.SetDefault("cache.memory.maxSize", 1000)
	v.SetDefault("cache.memory.defaultTTL", "30s")
	v.SetDefault("cache.tiered.enabled", false)
	v.SetDefault("cache.tiered.l1.maxSize", 10000)
	v.SetDefault("cache.tiered.l1.defaultTTL", "30s")
	v.SetDefault("cache.tiered.l2.keyPrefix", "e2bgw:cache:")
	v.SetDefault("cache.tiered.l2.pubsubChannel", "e2bgw:cache:invalidate")
	v.SetDefault("cache.tiered.l2.timeout", "200ms")
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
	if err := c.validateHTTPS(); err != nil {
		return err
	}
	enabledBackends, err := c.validateBackends()
	if err != nil {
		return err
	}
	for _, provider := range c.Auth.Providers {
		if err := provider.Validate(); err != nil {
			return err
		}
	}
	if err := c.SessionStore.validate(); err != nil {
		return err
	}
	if err := c.Cache.Tiered.validate(); err != nil {
		return err
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

// validateBackends verifies that at least one backend is enabled and every
// configured backend has a name and type. It returns the number of enabled
// backends.
func (c *Config) validateBackends() (int, error) {
	if len(c.Backends) == 0 {
		return 0, fmt.Errorf("at least one backend must be configured")
	}
	enabledBackends := 0
	for _, b := range c.Backends {
		if b.Enabled {
			enabledBackends++
		}
		if b.Name == "" {
			return 0, fmt.Errorf("backend name is required")
		}
		if b.Type == "" {
			return 0, fmt.Errorf("backend type is required for %q", b.Name)
		}
	}
	if enabledBackends == 0 {
		return 0, fmt.Errorf("at least one backend must be enabled")
	}
	return enabledBackends, nil
}

// minReloadInterval is the smallest non-zero server.https.reloadInterval.
// Smaller values are almost always a unit mistake: a bare YAML integer such as
// `reloadInterval: 30` decodes as 30ns and would re-read the certificate files
// in a busy loop.
const minReloadInterval = 10 * time.Millisecond

// validateHTTPS rejects partial HTTPS configurations: the listener address,
// certificate file, and key file must be configured together. A negative
// reloadInterval is rejected even without a listener, and a configured HTTPS
// listener must use a reloadInterval of 0 (disabled) or at least
// minReloadInterval.
func (c *Config) validateHTTPS() error {
	httpsCfg := c.Server.HTTPS
	if httpsCfg.ReloadInterval < 0 {
		return fmt.Errorf("server.https.reloadInterval must not be negative")
	}
	if (httpsCfg.Address != "" || httpsCfg.CertFile != "" || httpsCfg.KeyFile != "") &&
		(httpsCfg.Address == "" || httpsCfg.CertFile == "" || httpsCfg.KeyFile == "") {
		return fmt.Errorf("server.https.address, certFile, and keyFile must be set together")
	}
	if interval := httpsCfg.ReloadInterval; httpsCfg.Address != "" && httpsCfg.CertFile != "" && httpsCfg.KeyFile != "" &&
		interval > 0 && interval < minReloadInterval {
		return fmt.Errorf("server.https.reloadInterval must be 0 (disabled) or at least %s, got %s; "+
			`use a duration string such as "30s" (bare integers are read as nanoseconds)`,
			minReloadInterval, interval)
	}
	return nil
}

// Validate rejects unusable auth configurations before the server starts.
func (c AuthProviderConfig) Validate() error {
	switch c.Type {
	case "apikey":
		if len(c.Keys) > 0 && c.SecretRef != "" {
			return fmt.Errorf("apikey provider: keys and secretRef are mutually exclusive; set only one")
		}
		return nil
	case "jwt":
		return c.validateJWT()
	default:
		return fmt.Errorf("unsupported auth provider type %q", c.Type)
	}
}

func (c AuthProviderConfig) validateJWT() error {
	if c.Issuer == "" || c.Audience == "" || strings.TrimSpace(c.JWKS) == "" {
		return fmt.Errorf("jwt provider requires issuer, audience, and static jwks")
	}
	if c.JWKSURL != "" {
		return fmt.Errorf("jwt jwksURL is not supported with static jwks")
	}
	if c.ClockSkew < 0 || c.ClockSkew > 5*time.Minute {
		return fmt.Errorf("jwt clockSkew must be between 0 and 5m")
	}
	if strings.TrimSpace(c.Issuer) != c.Issuer || strings.TrimSpace(c.Audience) != c.Audience {
		return fmt.Errorf("jwt issuer and audience must not contain surrounding whitespace")
	}
	return nil
}

// LoadFromEnv is a convenience function for testing.
func LoadFromEnv() (*Config, error) {
	configPath := os.Getenv("E2BGW_CONFIG_FILE")
	return Load(configPath)
}
