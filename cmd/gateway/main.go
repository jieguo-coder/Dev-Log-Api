package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/time/rate"

	"github.com/jieguo-coder/mini-gateway/internal/config"
	"github.com/jieguo-coder/mini-gateway/internal/middleware"
	"github.com/jieguo-coder/mini-gateway/internal/proxy"
	"github.com/jieguo-coder/mini-gateway/internal/response"
	"github.com/jieguo-coder/mini-gateway/internal/router"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to gateway configuration file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", "error", err, "path", *configPath)
		os.Exit(1)
	}
	updateLogLevel(cfg.Logging.Level)
	slog.Info("configuration loaded", "routes", len(cfg.Routes))

	// ─── 创建全局中间件实例 ──────────────────────────────────
	var jwtAuth *middleware.JWTAuth
	if cfg.JWT.Enabled {
		jwtAuth, err = middleware.NewJWTAuth(middleware.JWTConfig{
			Secret:            []byte(cfg.JWT.Secret),
			Algorithm:         cfg.JWT.Algorithm,
			RequiredClaims:    cfg.JWT.Claims.Required,
			IssuerAllowlist:   toSet(cfg.JWT.Claims.IssuerAllowlist),
			AudienceAllowlist: toSet(cfg.JWT.Claims.AudienceAllowlist),
		})
		if err != nil {
			slog.Error("failed to create JWT middleware", "error", err)
			os.Exit(1)
		}
	}

	var rateLimiter *middleware.RateLimiter
	if cfg.RateLimit.Enabled {
		rateLimiter = middleware.NewRateLimiter(
			rate.Limit(cfg.RateLimit.DefaultRate),
			cfg.RateLimit.DefaultBurst,
			cfg.RateLimit.KeyBy,
			cfg.RateLimit.CleanupInterval,
		)
		defer rateLimiter.Stop()
	}

	// ─── 为每条路由创建 Proxy + 组装中间件链 ─────────────────
	trieRouter := router.NewTrieRouter()
	jwtBeforeRL := strings.Contains(cfg.RateLimit.KeyBy, "jwt_claim")

	for _, rc := range cfg.Routes {
		backends := buildBackends(rc)
		if len(backends) == 0 {
			slog.Warn("skipping route with no valid backends", "route", rc.Name)
			continue
		}

		lb := proxy.NewRoundRobinLoadBalancer()
		rp := proxy.NewReverseProxy(backends, lb, rc.Timeout, rc.StripPrefix, rc.SetHeaders)

		// 动态组装中间件链
		var mws []middleware.Middleware
		if jwtBeforeRL {
			addIf(!rc.SkipAuth && jwtAuth != nil, &mws, jwtAuth.Middleware())
			addIf(!rc.SkipRateLimit && rateLimiter != nil, &mws, rateLimiter.Middleware())
		} else {
			addIf(!rc.SkipRateLimit && rateLimiter != nil, &mws, rateLimiter.Middleware())
			addIf(!rc.SkipAuth && jwtAuth != nil, &mws, jwtAuth.Middleware())
		}

		handler := middleware.Chain(mws...)(rp)

		// 构造 Router 路由
		rt := router.Route{
			Name:          rc.Name,
			Method:        rc.Method,
			Matcher:       buildMatcher(rc.Path.Type, rc.Path.Value),
			Handler:       handler,
			SkipAuth:      rc.SkipAuth,
			SkipRateLimit: rc.SkipRateLimit,
			Timeout:       rc.Timeout,
			Retry:         rc.Retry,
			StripPrefix:   rc.StripPrefix,
			SetHeaders:    rc.SetHeaders,
		}
		if rc.RateLimit != nil {
			rt.RateLimit = &router.RouteRateLimitConfig{Rate: rc.RateLimit.Rate, Burst: rc.RateLimit.Burst}
		}

		trieRouter.AddRoute(rt)
		slog.Info("route registered", "name", rc.Name, "method", rc.Method, "path", rc.Path.Value)
	}

	// ─── 顶层 Handler：注入 request_id → Router ──────────────
	mainHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := generateRequestID()
		w.Header().Set("X-Request-Id", reqID)
		trieRouter.ServeHTTP(w, response.SetRequestID(r, reqID))
	})

	// ─── 启动 Server + 优雅关停 ──────────────────────────────
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mainHandler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	errChan := make(chan error, 1)
	go func() {
		slog.Info("gateway server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("server listen failed: %w", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("received shutdown signal", "signal", sig.String())
	case err := <-errChan:
		slog.Error("server fatal error, shutting down", "error", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}
	slog.Info("server exited gracefully")
}

// ─── Helpers ──────────────────────────────────────────────────

func updateLogLevel(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func toSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

func addIf(cond bool, mws *[]middleware.Middleware, mw middleware.Middleware) {
	if cond {
		*mws = append(*mws, mw)
	}
}

func buildBackends(rc config.RouteConfig) []*proxy.Backend {
	var out []*proxy.Backend
	for _, bc := range rc.Backends {
		u, err := url.Parse(bc.URL)
		if err != nil {
			slog.Warn("invalid backend URL", "url", bc.URL, "route", rc.Name, "error", err)
			continue
		}
		out = append(out, &proxy.Backend{URL: u, Weight: bc.Weight, Healthy: true})
	}
	return out
}

func buildMatcher(matchType, value string) router.PathMatcher {
	switch matchType {
	case "exact":
		return router.NewExactMatcher(value)
	case "prefix":
		return router.NewPrefixMatcher(value)
	default:
		return router.NewPrefixMatcher(value)
	}
}

func generateRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
