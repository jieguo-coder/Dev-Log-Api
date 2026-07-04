package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"

	"github.com/jieguo-coder/mini-gateway/internal/config"
	"github.com/jieguo-coder/mini-gateway/internal/middleware"
	"github.com/jieguo-coder/mini-gateway/internal/proxy"
	"github.com/jieguo-coder/mini-gateway/internal/response"
	"github.com/jieguo-coder/mini-gateway/internal/router"
)

func TestE2E_FullPipeline(t *testing.T) {
	// ─── 1. Mock backend ──────────────────────────────────────
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"backend_path": r.URL.Path,
			"x_gateway":    r.Header.Get("X-Gateway-Name"),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer backend.Close()

	// ─── 2. 写临时 config ─────────────────────────────────────
	jwtSecret := "e2e-secret-key-123456"
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "e2e_config.yaml")
	cfgYAML := `
server:
  port: 0
rate_limit:
  enabled: true
  default_rate: 100
  default_burst: 100
  key_by: "ip"
  cleanup_interval: 60s
jwt:
  enabled: true
  secret: "` + jwtSecret + `"
  algorithm: "HS256"
  claims:
    required:
      - sub
      - exp
routes:
  - name: "e2e"
    method: "*"
    path:
      type: "prefix"
      value: "/api/"
    backends:
      - url: "` + backend.URL + `"
        weight: 1
    strip_prefix: "/api"
    set_headers:
      X-Gateway-Name: "e2e-gateway"
    timeout: 5s
logging:
  level: "error"
`
	os.WriteFile(cfgPath, []byte(cfgYAML), 0644)

	// ─── 3. 加载配置并组装网关 handler ────────────────────────
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	handler := assembleGateway(t, cfg, jwtSecret)

	// ─── 4. 无 Token → 401 ───────────────────────────────────
	req1 := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Errorf("no token: expected 401, got %d", rec1.Code)
	}

	// ─── 5. 合法 Token → 200 + 代理成功 ──────────────────────
	token := createE2EToken(t, jwtSecret, "user-42", time.Now().Add(1*time.Hour))
	req2 := httptest.NewRequest(http.MethodGet, "/api/hello?q=1", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		body, _ := io.ReadAll(rec2.Result().Body)
		t.Fatalf("valid token: expected 200, got %d. Body: %s", rec2.Code, string(body))
	}

	var data map[string]any
	json.NewDecoder(rec2.Result().Body).Decode(&data)

	if data["backend_path"] != "/hello" {
		t.Errorf("strip_prefix: expected /hello, got %v", data["backend_path"])
	}
	if data["x_gateway"] != "e2e-gateway" {
		t.Errorf("set_headers: expected 'e2e-gateway', got %v", data["x_gateway"])
	}

	// ─── 6. X-Request-Id ──────────────────────────────────────
	if rec2.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header missing")
	}

	// ─── 7. 过期 Token → 401 ─────────────────────────────────
	expToken := createE2EToken(t, jwtSecret, "user-42", time.Now().Add(-1*time.Hour))
	req3 := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	req3.Header.Set("Authorization", "Bearer "+expToken)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Errorf("expired token: expected 401, got %d", rec3.Code)
	}

	// ─── 8. 404 路由不匹配 ────────────────────────────────────
	req4 := httptest.NewRequest(http.MethodGet, "/unknown/path", nil)
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusNotFound {
		t.Errorf("unknown path: expected 404, got %d", rec4.Code)
	}
}

// assembleGateway 手动组装网关 handler，与 main() 保持相同逻辑。
func assembleGateway(t *testing.T, cfg *config.Config, jwtSecret string) http.Handler {
	t.Helper()

	jwtAuth, err := middleware.NewJWTAuth(middleware.JWTConfig{
		Secret:    []byte(jwtSecret),
		Algorithm: cfg.JWT.Algorithm,
		RequiredClaims: cfg.JWT.Claims.Required,
	})
	if err != nil {
		t.Fatalf("create jwt: %v", err)
	}

	rateLimiter := middleware.NewRateLimiter(
		rate.Limit(cfg.RateLimit.DefaultRate),
		cfg.RateLimit.DefaultBurst,
		cfg.RateLimit.KeyBy,
		cfg.RateLimit.CleanupInterval,
	)
	t.Cleanup(func() { rateLimiter.Stop() })

	trieRouter := router.NewTrieRouter()

	for _, rc := range cfg.Routes {
		backends := make([]*proxy.Backend, 0)
		for _, bc := range rc.Backends {
			u, _ := url.Parse(bc.URL)
			backends = append(backends, &proxy.Backend{URL: u, Weight: bc.Weight, Healthy: true})
		}

		lb := proxy.NewRoundRobinLoadBalancer()
		rp := proxy.NewReverseProxy(backends, lb, rc.Timeout, rc.StripPrefix, rc.SetHeaders)

		// Rate Limiter → JWT Auth → Proxy
		var mws []middleware.Middleware
		if !rc.SkipRateLimit {
			mws = append(mws, rateLimiter.Middleware())
		}
		if !rc.SkipAuth {
			mws = append(mws, jwtAuth.Middleware())
		}
		handler := middleware.Chain(mws...)(rp)

		var matcher router.PathMatcher
		if rc.Path.Type == "exact" {
			matcher = router.NewExactMatcher(rc.Path.Value)
		} else {
			matcher = router.NewPrefixMatcher(rc.Path.Value)
		}

		trieRouter.AddRoute(router.Route{
			Name:    rc.Name,
			Method:  rc.Method,
			Matcher: matcher,
			Handler: handler,
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := generateRequestID()
		w.Header().Set("X-Request-Id", reqID)
		trieRouter.ServeHTTP(w, response.SetRequestID(r, reqID))
	})
}

func createE2EToken(t *testing.T, secret, sub string, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": sub,
		"exp": exp.Unix(),
		"iat": time.Now().Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}
