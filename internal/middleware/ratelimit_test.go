package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	// 每秒 10 个令牌，桶容量 2（只允许 2 个突发）
	rl := NewRateLimiter(10, 2, "ip", 30*time.Second)
	defer rl.Stop()

	key := "192.168.1.1"

	// 前 2 个请求应通过（桶中有 2 个初始令牌）
	if !rl.Allow(key) {
		t.Error("request 1: expected allowed, got denied")
	}
	if !rl.Allow(key) {
		t.Error("request 2: expected allowed, got denied")
	}

	// 第 3 个请求应被拒绝（桶已空）
	if rl.Allow(key) {
		t.Error("request 3: expected denied, got allowed")
	}
}

func TestRateLimiter_Middleware_Integration(t *testing.T) {
	// 极低的速率：每秒 1 个令牌，桶容量 1
	rl := NewRateLimiter(1, 1, "ip", 30*time.Second)
	defer rl.Stop()

	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	// 第一个请求应通过
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req1.RemoteAddr = "10.0.0.1:12345"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Errorf("request 1: expected 200, got %d", rec1.Code)
	}

	// 第二个请求应立即被限流（桶容量为 1，已消耗完）
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req2.RemoteAddr = "10.0.0.1:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("request 2: expected 429, got %d", rec2.Code)
	}

	// 不同 IP 的请求应通过（独立的 limiter）
	req3 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req3.RemoteAddr = "10.0.0.2:12345"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("request 3 (different IP): expected 200, got %d", rec3.Code)
	}
}

func TestRateLimiter_ExtractIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		wantKey    string
	}{
		{
			name:       "RemoteAddr only",
			remoteAddr: "192.168.1.1:54321",
			wantKey:    "192.168.1.1",
		},
		{
			name:       "X-Real-IP takes priority",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Real-IP": "203.0.113.5"},
			wantKey:    "203.0.113.5",
		},
		{
			name:       "X-Forwarded-For first IP",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.5, 10.0.0.1"},
			wantKey:    "203.0.113.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			got := extractClientIP(req)
			if got != tt.wantKey {
				t.Errorf("expected %q, got %q", tt.wantKey, got)
			}
		})
	}
}

func TestRateLimiter_ConcurrentSafety(t *testing.T) {
	rl := NewRateLimiter(1000, 1000, "ip", 30*time.Second)
	defer rl.Stop()

	var wg sync.WaitGroup
	numGoroutines := 20
	callsPerGoroutine := 50
	key := "10.0.0.1"

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < callsPerGoroutine; i++ {
				rl.Allow(key)
			}
		}()
	}

	wg.Wait()

	// 无 data race 即为通过
	if rl.LimiterCount() != 1 {
		t.Errorf("expected 1 limiter entry, got %d", rl.LimiterCount())
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	// 使用极短的 TTL 以便快速测试
	rl := NewRateLimiter(10, 2, "ip", 50*time.Millisecond)
	defer rl.Stop()

	rl.Allow("10.0.0.1")
	rl.Allow("10.0.0.2")

	if rl.LimiterCount() != 2 {
		t.Fatalf("expected 2 limiters, got %d", rl.LimiterCount())
	}

	// 等待清理周期
	time.Sleep(150 * time.Millisecond)

	if rl.LimiterCount() != 0 {
		t.Errorf("expected 0 limiters after cleanup, got %d", rl.LimiterCount())
	}
}

func TestRateLimiter_Stop(t *testing.T) {
	rl := NewRateLimiter(10, 2, "ip", 1*time.Second)
	rl.Stop()

	// 第二次 Stop 不应 panic
	rl.Stop()

	// 确认 stopCh 已关闭（二次 Stop 是安全的）
	if !rl.stopped {
		t.Error("expected stopped=true after Stop()")
	}
}

func TestRateLimiter_JWTClaimKey(t *testing.T) {
	rl := NewRateLimiter(10, 2, "jwt_claim:sub", 30*time.Second)
	defer rl.Stop()

	// 注入 JWT Claims 到 context
	claims := &JWTCustomClaims{}
	claims.Subject = "user-42"
	ctx := context.WithValue(context.Background(), ClaimsContextKey, claims)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req = req.WithContext(ctx)

	key := rl.extractKey(req)
	if key != "user-42" {
		t.Errorf("expected key 'user-42', got %q", key)
	}
}

func TestRateLimiter_401ResponseFormat(t *testing.T) {
	rl := NewRateLimiter(1, 1, "ip", 30*time.Second)
	defer rl.Stop()

	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 消耗唯一的令牌
	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req1.RemoteAddr = "10.0.0.99:12345"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request should pass, got %d", rec1.Code)
	}

	// 第二次应被限流，验证 JSON 格式
	req2 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req2.RemoteAddr = "10.0.0.99:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec2.Code)
	}

	data := readErrorJSON(t, rec2.Result())
	errObj, ok := data["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error object in JSON response")
	}
	if errObj["code"] != "RATE_LIMITED" {
		t.Errorf("expected RATE_LIMITED, got %q", errObj["code"])
	}
}
