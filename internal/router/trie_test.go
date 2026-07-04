package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// mkExact 创建精确匹配路由
func mkExact(name, method, path string) Route {
	return Route{
		Name:    name,
		Method:  method,
		Matcher: NewExactMatcher(path),
	}
}

// mkPrefix 创建前缀匹配路由
func mkPrefix(name, method, prefix string) Route {
	return Route{
		Name:    name,
		Method:  method,
		Matcher: NewPrefixMatcher(prefix),
	}
}

func TestRouter_ExactMatch(t *testing.T) {
	r := NewTrieRouter()
	r.AddRoute(mkExact("health", "GET", "/healthz"))
	r.AddRoute(mkExact("users", "POST", "/api/v1/users"))

	tests := []struct {
		method string
		path   string
		want   string // route name
		nilOk  bool
	}{
		{"GET", "/healthz", "health", false},
		{"POST", "/api/v1/users", "users", false},
		{"GET", "/api/v1/users", "", true},  // method 不对
		{"GET", "/unknown", "", true},        // 路径不对
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		got := r.Match(req)
		if tt.nilOk {
			if got != nil {
				t.Errorf("%s %s: expected nil, got %s", tt.method, tt.path, got.Name)
			}
		} else {
			if got == nil {
				t.Fatalf("%s %s: expected %s, got nil", tt.method, tt.path, tt.want)
			}
			if got.Name != tt.want {
				t.Errorf("%s %s: expected %s, got %s", tt.method, tt.path, tt.want, got.Name)
			}
		}
	}
}

func TestRouter_PrefixMatch(t *testing.T) {
	r := NewTrieRouter()
	r.AddRoute(mkPrefix("users", "GET", "/api/v1/users/"))
	r.AddRoute(mkPrefix("orders", "GET", "/api/v1/orders/"))

	tests := []struct {
		path  string
		want  string
		nilOk bool
	}{
		{"/api/v1/users/profile", "users", false},
		{"/api/v1/users/123", "users", false},
		{"/api/v1/orders/list", "orders", false},
		{"/api/v2/users/profile", "", true}, // 不匹配
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		got := r.Match(req)
		if tt.nilOk {
			if got != nil {
				t.Errorf("%s: expected nil, got %s", tt.path, got.Name)
			}
		} else {
			if got == nil {
				t.Fatalf("%s: expected %s, got nil", tt.path, tt.want)
			}
			if got.Name != tt.want {
				t.Errorf("%s: expected %s, got %s", tt.path, tt.want, got.Name)
			}
		}
	}
}

func TestRouter_LongestPrefixFirst(t *testing.T) {
	r := NewTrieRouter()
	// 注册顺序：短前缀在前，长前缀在后
	r.AddRoute(mkPrefix("api-short", "GET", "/api/"))
	r.AddRoute(mkPrefix("users-long", "GET", "/api/v1/users/"))
	r.AddRoute(mkPrefix("api-v1", "GET", "/api/v1/"))

	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/users/profile", "users-long"}, // 应命中长前缀
		{"/api/v1/orders", "api-v1"},             // /api/v1/ 优于 /api/
		{"/api/v2/info", "api-short"},             // 只有 /api/ 匹配
		{"/api/", "api-short"},                    // /api/ 匹配
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		got := r.Match(req)
		if got == nil {
			t.Fatalf("%s: expected %s, got nil", tt.path, tt.want)
		}
		if got.Name != tt.want {
			t.Errorf("%s: expected %s, got %s", tt.path, tt.want, got.Name)
		}
	}
}

func TestRouter_ExactTakesPriorityOverPrefix(t *testing.T) {
	r := NewTrieRouter()
	// 同时注册精确匹配和前缀匹配
	r.AddRoute(mkPrefix("users-prefix", "GET", "/api/v1/users/"))
	r.AddRoute(mkExact("users-exact", "GET", "/api/v1/users"))

	// 精确路径 /api/v1/users 应命中精确匹配而非前缀匹配
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	got := r.Match(req)
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.Name != "users-exact" {
		t.Errorf("expected 'users-exact', got %s", got.Name)
	}

	// 前缀子路径 /api/v1/users/123 应命中前缀匹配
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/users/123", nil)
	got2 := r.Match(req2)
	if got2 == nil {
		t.Fatal("expected prefix match, got nil")
	}
	if got2.Name != "users-prefix" {
		t.Errorf("expected 'users-prefix', got %s", got2.Name)
	}
}

func TestRouter_ServeHTTP_RouteContext(t *testing.T) {
	r := NewTrieRouter()
	r.AddRoute(mkExact("test", "GET", "/api/test"))

	var captured *Route
	// 用一个简单的 handler 捕获 context 中的 Route
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		captured = RouteFromContext(req.Context())
		w.WriteHeader(http.StatusOK)
	})

	// 模拟 ServeHTTP 的流程
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		route := r.Match(req)
		if route == nil {
			http.NotFound(w, req)
			return
		}
		ctx := SetRouteContext(req.Context(), route)
		handler.ServeHTTP(w, req.WithContext(ctx))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if captured == nil {
		t.Fatal("expected Route in context, got nil")
	}
	if captured.Name != "test" {
		t.Errorf("expected route name 'test', got %q", captured.Name)
	}
}

func TestRouter_ServeHTTP_404(t *testing.T) {
	r := NewTrieRouter()
	r.AddRoute(mkExact("home", "GET", "/"))

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter_WildcardMethod(t *testing.T) {
	r := NewTrieRouter()
	r.AddRoute(mkExact("catch-all", "*", "/api/public"))

	// GET 命中通配
	reqGet := httptest.NewRequest(http.MethodGet, "/api/public", nil)
	if got := r.Match(reqGet); got == nil || got.Name != "catch-all" {
		t.Errorf("GET: expected 'catch-all', got %v", got)
	}

	// POST 也命中通配
	reqPost := httptest.NewRequest(http.MethodPost, "/api/public", nil)
	if got := r.Match(reqPost); got == nil || got.Name != "catch-all" {
		t.Errorf("POST: expected 'catch-all', got %v", got)
	}
}

func TestRouter_EmptyRoutes(t *testing.T) {
	r := NewTrieRouter()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	if got := r.Match(req); got != nil {
		t.Errorf("expected nil for empty router, got %v", got)
	}
}
