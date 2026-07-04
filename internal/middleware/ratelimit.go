package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/jieguo-coder/mini-gateway/internal/response"
)

// RateLimiter 基于令牌桶的 HTTP 限流中间件。
type RateLimiter struct {
	mu           sync.Mutex
	limiters     map[string]*rateLimiterEntry
	defaultRate  rate.Limit
	defaultBurst int
	keyBy        string
	cleanupTTL   time.Duration
	stopCh       chan struct{}
	stopped      bool
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter 创建限流器实例。
func NewRateLimiter(defaultRate rate.Limit, defaultBurst int, keyBy string, cleanupTTL time.Duration) *RateLimiter {
	rl := &RateLimiter{
		limiters:     make(map[string]*rateLimiterEntry),
		defaultRate:  defaultRate,
		defaultBurst: defaultBurst,
		keyBy:        keyBy,
		cleanupTTL:   cleanupTTL,
		stopCh:       make(chan struct{}),
	}

	// 启动后台清理 goroutine
	go rl.cleanupLoop()

	return rl
}

// Middleware 返回标准 Middleware 签名的限流函数。
func (rl *RateLimiter) Middleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rl.extractKey(r)

			if !rl.Allow(key) {
				slog.Warn("rate limit exceeded",
					"key", key,
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
				)
				response.WriteErrorJSON(w, r, http.StatusTooManyRequests, "RATE_LIMITED",
					"Too many requests. Please try again later.")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Allow 检查指定 key 的请求是否被允许。
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, exists := rl.limiters[key]
	if !exists {
		entry = &rateLimiterEntry{
			limiter: rate.NewLimiter(rl.defaultRate, rl.defaultBurst),
		}
		rl.limiters[key] = entry
	}

	entry.lastSeen = time.Now()
	return entry.limiter.Allow()
}

// extractKey 从请求中提取限流 key。
func (rl *RateLimiter) extractKey(r *http.Request) string {
	switch rl.keyBy {
	case "ip":
		return extractClientIP(r)
	default:
		// jwt_claim:<key> — 从 context 中提取 JWT Claims
		if strings.HasPrefix(rl.keyBy, "jwt_claim:") {
			claimKey := strings.TrimPrefix(rl.keyBy, "jwt_claim:")
			claims := FromContext(r.Context())
			if claims != nil {
				if val, ok := claims.Extra[claimKey]; ok {
					return val.(string)
				}
				// 也检查标准 claims 字段
				switch claimKey {
				case "sub":
					return claims.Subject
				case "iss":
					return claims.Issuer
				}
			}
		}
		// 回退到 IP
		return extractClientIP(r)
	}
}

// extractClientIP 从请求中提取客户端 IP。
func extractClientIP(r *http.Request) string {
	// 优先使用代理传递的真实 IP
	if xff := r.Header.Get("X-Real-IP"); xff != "" {
		return xff
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i != -1 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	// 回退到 RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cleanupLoop 后台定期清理过期的 limiter 条目。
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanupTTL)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.stopCh:
			return
		}
	}
}

// cleanup 删除超过 TTL 未使用的 limiter。
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-rl.cleanupTTL)
	for key, entry := range rl.limiters {
		if entry.lastSeen.Before(cutoff) {
			delete(rl.limiters, key)
		}
	}
}

// Stop 停止后台清理 goroutine（优雅关停时调用）。
func (rl *RateLimiter) Stop() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if !rl.stopped {
		rl.stopped = true
		close(rl.stopCh)
	}
}

// LimiterCount 返回当前活跃的 limiter 数量（测试用）。
func (rl *RateLimiter) LimiterCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.limiters)
}
