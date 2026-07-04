package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_Success(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("testdata", "valid_config.yaml"))
	if err != nil {
		t.Fatalf("expected successful load, got error: %v", err)
	}

	// Server
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Server.ReadTimeout != 10*time.Second {
		t.Errorf("expected read_timeout 10s, got %v", cfg.Server.ReadTimeout)
	}

	// RateLimit
	if cfg.RateLimit.DefaultRate != 50 {
		t.Errorf("expected default_rate 50, got %d", cfg.RateLimit.DefaultRate)
	}
	if cfg.RateLimit.KeyBy != "ip" {
		t.Errorf("expected key_by 'ip', got %q", cfg.RateLimit.KeyBy)
	}

	// JWT
	if cfg.JWT.Secret != "test-secret-key" {
		t.Errorf("expected secret 'test-secret-key', got %q", cfg.JWT.Secret)
	}
	if cfg.JWT.Algorithm != "HS256" {
		t.Errorf("expected algorithm HS256, got %q", cfg.JWT.Algorithm)
	}
	if len(cfg.JWT.Claims.Required) != 2 {
		t.Errorf("expected 2 required claims, got %d", len(cfg.JWT.Claims.Required))
	}

	// Routes
	if len(cfg.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(cfg.Routes))
	}
	if cfg.Routes[0].Name != "test-service" {
		t.Errorf("expected route name 'test-service', got %q", cfg.Routes[0].Name)
	}
	if cfg.Routes[0].Timeout != 5*time.Second {
		t.Errorf("expected route timeout 5s, got %v", cfg.Routes[0].Timeout)
	}

	// Logging
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected log level 'debug', got %q", cfg.Logging.Level)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig(filepath.Join("testdata", "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "failed to read config file") {
		t.Errorf("error should mention 'failed to read config file', got: %v", err)
	}
}

func TestLoadConfig_MalformedYAML(t *testing.T) {
	_, err := LoadConfig(filepath.Join("testdata", "malformed.yaml"))
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse config file") {
		t.Errorf("error should mention 'failed to parse config file', got: %v", err)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	// 使用最小配置，验证默认值填充
	tmpDir := t.TempDir()
	minimalCfg := `
server:
  port: 3000
rate_limit:
  key_by: "ip"
jwt:
  algorithm: "HS256"
routes:
  - name: "min"
    method: "*"
    path:
      type: "exact"
      value: "/min"
    backends:
      - url: "http://localhost:9000"
        weight: 1
logging: {}
`
	path := filepath.Join(tmpDir, "minimal.yaml")
	if err := os.WriteFile(path, []byte(minimalCfg), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("expected successful load, got: %v", err)
	}

	// 默认值检查
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected default host '0.0.0.0', got %q", cfg.Server.Host)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("expected default read_timeout 30s, got %v", cfg.Server.ReadTimeout)
	}
	if cfg.RateLimit.DefaultRate != 100 {
		t.Errorf("expected default rate 100, got %d", cfg.RateLimit.DefaultRate)
	}
	if cfg.RateLimit.DefaultBurst != 200 {
		t.Errorf("expected default burst 200, got %d", cfg.RateLimit.DefaultBurst)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected default log level 'info', got %q", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("expected default log format 'json', got %q", cfg.Logging.Format)
	}
	if cfg.Routes[0].LBStrategy != "round_robin" {
		t.Errorf("expected default lb_strategy 'round_robin', got %q", cfg.Routes[0].LBStrategy)
	}
}

func TestLoadConfig_EnvVarExpansion(t *testing.T) {
	os.Setenv("TEST_JWT_SECRET", "env-secret-value")
	os.Setenv("TEST_DB_URL", "postgres://localhost:5432/db")
	defer func() {
		os.Unsetenv("TEST_JWT_SECRET")
		os.Unsetenv("TEST_DB_URL")
	}()

	tmpDir := t.TempDir()
	cfgContent := `
server:
  port: 8080
rate_limit:
  key_by: "ip"
jwt:
  secret: "${TEST_JWT_SECRET}"
  algorithm: "HS256"
routes:
  - name: "env-test"
    method: "GET"
    path:
      type: "exact"
      value: "/test"
    backends:
      - url: "http://localhost:9000"
        weight: 1
logging: {}
`
	path := filepath.Join(tmpDir, "env_config.yaml")
	if err := os.WriteFile(path, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("expected successful load, got: %v", err)
	}

	if cfg.JWT.Secret != "env-secret-value" {
		t.Errorf("expected JWT secret 'env-secret-value' from env var, got %q", cfg.JWT.Secret)
	}
}

func TestLoadConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		errText string
	}{
		{
			name: "invalid port",
			yaml: `
server:
  port: 99999
rate_limit:
  key_by: "ip"
jwt:
  algorithm: "HS256"
routes:
  - name: "test"
    method: "GET"
    path:
      type: "exact"
      value: "/"
    backends:
      - url: "http://localhost:9000"
        weight: 1
`,
			errText: "server.port",
		},
		{
			name: "invalid key_by",
			yaml: `
server:
  port: 8080
rate_limit:
  key_by: "invalid_key"
jwt:
  algorithm: "HS256"
routes:
  - name: "test"
    method: "GET"
    path:
      type: "exact"
      value: "/"
    backends:
      - url: "http://localhost:9000"
        weight: 1
`,
			errText: "rate_limit.key_by",
		},
		{
			name: "invalid jwt algorithm",
			yaml: `
server:
  port: 8080
rate_limit:
  key_by: "ip"
jwt:
  algorithm: "INVALID_ALGO"
routes:
  - name: "test"
    method: "GET"
    path:
      type: "exact"
      value: "/"
    backends:
      - url: "http://localhost:9000"
        weight: 1
`,
			errText: "jwt.algorithm",
		},
		{
			name: "route missing name",
			yaml: `
server:
  port: 8080
rate_limit:
  key_by: "ip"
jwt:
  algorithm: "HS256"
routes:
  - method: "GET"
    path:
      type: "exact"
      value: "/"
    backends:
      - url: "http://localhost:9000"
        weight: 1
`,
			errText: "routes[0].name",
		},
		{
			name: "route missing backends",
			yaml: `
server:
  port: 8080
rate_limit:
  key_by: "ip"
jwt:
  algorithm: "HS256"
routes:
  - name: "test"
    method: "GET"
    path:
      type: "exact"
      value: "/"
    backends: []
`,
			errText: "routes[0].backends",
		},
		{
			name: "route invalid path type",
			yaml: `
server:
  port: 8080
rate_limit:
  key_by: "ip"
jwt:
  algorithm: "HS256"
routes:
  - name: "test"
    method: "GET"
    path:
      type: "fuzzy"
      value: "/"
    backends:
      - url: "http://localhost:9000"
        weight: 1
`,
			errText: "routes[0].path.type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("failed to write temp config: %v", err)
			}

			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("error should contain %q, got: %v", tt.errText, err)
			}
		})
	}
}
