package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 网关全局配置
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	JWT       JWTConfig       `yaml:"jwt"`
	Routes    []RouteConfig   `yaml:"routes"`
	Logging   LoggingConfig   `yaml:"logging"`
}

// ServerConfig HTTP 服务器配置
type ServerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	Enabled         bool          `yaml:"enabled"`
	DefaultRate     int           `yaml:"default_rate"`
	DefaultBurst    int           `yaml:"default_burst"`
	KeyBy           string        `yaml:"key_by"`
	CleanupInterval time.Duration `yaml:"cleanup_interval"`
}

// JWTConfig JWT 鉴权配置
type JWTConfig struct {
	Enabled   bool      `yaml:"enabled"`
	Secret    string    `yaml:"secret"`
	Algorithm string    `yaml:"algorithm"`
	Claims    ClaimsCfg `yaml:"claims"`
}

// ClaimsCfg JWT Claims 校验配置
type ClaimsCfg struct {
	Required         []string `yaml:"required"`
	IssuerAllowlist  []string `yaml:"issuer_allowlist"`
	AudienceAllowlist []string `yaml:"audience_allowlist"`
}

// RouteConfig 单条路由规则配置
type RouteConfig struct {
	Name          string            `yaml:"name"`
	Method        string            `yaml:"method"`
	Path          PathMatch         `yaml:"path"`
	Backends      []BackendConfig   `yaml:"backends"`
	LBStrategy    string            `yaml:"lb_strategy"`
	SkipAuth      bool              `yaml:"skip_auth"`
	SkipRateLimit bool              `yaml:"skip_rate_limit"`
	RateLimit     *RouteRateLimit   `yaml:"rate_limit,omitempty"`
	Timeout       time.Duration     `yaml:"timeout"`
	Retry         int               `yaml:"retry"`
	StripPrefix   string            `yaml:"strip_prefix"`
	SetHeaders    map[string]string `yaml:"set_headers"`
}

// PathMatch 路径匹配规则
type PathMatch struct {
	Type  string `yaml:"type"` // "exact" | "prefix" | "regex"
	Value string `yaml:"value"`
}

// BackendConfig 后端服务配置
type BackendConfig struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

// RouteRateLimit 路由级限流配置
type RouteRateLimit struct {
	Rate  int `yaml:"rate"`
	Burst int `yaml:"burst"`
}

// LoggingConfig 日志配置
type LoggingConfig struct {
	Level   string `yaml:"level"`
	Format  string `yaml:"format"`
	LogBody bool   `yaml:"log_body"`
}

// LoadConfig 从指定路径加载 YAML 配置文件，返回解析后的 Config。
//
// 支持 ${ENV_VAR} 格式的环境变量占位符替换。
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	// 替换环境变量占位符: ${VAR_NAME} → os.Getenv("VAR_NAME")
	content := expandEnvVars(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %q: %w", path, err)
	}

	// 设置默认值
	cfg.applyDefaults()

	// 校验必填字段
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// expandEnvVars 替换字符串中的 ${VAR_NAME} 占位符为对应的环境变量值。
// 若环境变量不存在，则保留原占位符不变。
func expandEnvVars(s string) string {
	var result strings.Builder
	result.Grow(len(s))

	i := 0
	for i < len(s) {
		// 查找 ${ 起始标记
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			// 查找 } 结束标记
			end := strings.IndexByte(s[i+2:], '}')
			if end == -1 {
				result.WriteByte(s[i])
				i++
				continue
			}
			varName := s[i+2 : i+2+end]
			envVal := os.Getenv(varName)
			result.WriteString(envVal)
			i += 2 + end + 1 // 跳过 ${VAR_NAME}
			continue
		}
		result.WriteByte(s[i])
		i++
	}

	return result.String()
}

// applyDefaults 填充配置的默认值。
func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 30 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 30 * time.Second
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 120 * time.Second
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 10 * time.Second
	}

	if c.RateLimit.KeyBy == "" {
		c.RateLimit.KeyBy = "ip"
	}
	if c.RateLimit.DefaultRate == 0 {
		c.RateLimit.DefaultRate = 100
	}
	if c.RateLimit.DefaultBurst == 0 {
		c.RateLimit.DefaultBurst = 200
	}
	if c.RateLimit.CleanupInterval == 0 {
		c.RateLimit.CleanupInterval = 60 * time.Second
	}

	if c.JWT.Algorithm == "" {
		c.JWT.Algorithm = "HS256"
	}

	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	// 路由默认值
	for i := range c.Routes {
		if c.Routes[i].LBStrategy == "" {
			c.Routes[i].LBStrategy = "round_robin"
		}
		if c.Routes[i].Timeout == 0 {
			c.Routes[i].Timeout = 30 * time.Second
		}
	}
}

// validate 校验配置的必填字段和合法性。
func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", c.Server.Port)
	}

	if c.RateLimit.KeyBy != "ip" && !strings.HasPrefix(c.RateLimit.KeyBy, "jwt_claim:") {
		return fmt.Errorf("rate_limit.key_by must be 'ip' or 'jwt_claim:<key>', got %q", c.RateLimit.KeyBy)
	}

	validAlgorithms := map[string]bool{
		"HS256": true, "HS384": true, "HS512": true,
		"RS256": true, "ES256": true,
	}
	if !validAlgorithms[c.JWT.Algorithm] {
		return fmt.Errorf("jwt.algorithm %q is not supported", c.JWT.Algorithm)
	}

	for i, route := range c.Routes {
		if route.Name == "" {
			return fmt.Errorf("routes[%d].name is required", i)
		}
		if route.Method == "" {
			return fmt.Errorf("routes[%d].method is required", i)
		}
		if route.Path.Type == "" {
			return fmt.Errorf("routes[%d].path.type is required", i)
		}
		if route.Path.Type != "exact" && route.Path.Type != "prefix" && route.Path.Type != "regex" {
			return fmt.Errorf("routes[%d].path.type must be 'exact', 'prefix', or 'regex', got %q", i, route.Path.Type)
		}
		if route.Path.Value == "" {
			return fmt.Errorf("routes[%d].path.value is required", i)
		}
		if len(route.Backends) == 0 {
			return fmt.Errorf("routes[%d].backends must have at least one entry", i)
		}
		for j, be := range route.Backends {
			if be.URL == "" {
				return fmt.Errorf("routes[%d].backends[%d].url is required", i, j)
			}
			if be.Weight <= 0 {
				return fmt.Errorf("routes[%d].backends[%d].weight must be > 0, got %d", i, j, be.Weight)
			}
		}
	}

	return nil
}
