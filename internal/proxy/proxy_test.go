package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockBackend 创建一个返回固定响应和路径回显的测试后端。
func mockBackend(responseBody string, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		// 回显请求路径和自定义 Header，方便验证
		resp := map[string]interface{}{
			"path":       r.URL.Path,
			"host":       r.Host,
			"x-gateway":  r.Header.Get("X-Gateway-Name"),
			"x-route":    r.Header.Get("X-Route-Name"),
			"body":       responseBody,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// parseBackendURL 将 httptest server URL 解析为 Backend。
func parseBackendURL(raw string) *Backend {
	return &Backend{URL: mustParseURL(raw), Weight: 1, Healthy: true}
}

// readJSONBody 读取响应体并解析为 map。
func readJSONBody(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("failed to parse JSON body: %v\nBody: %s", err, string(body))
	}
	return data
}

// ─── 测试 1：加权轮询生效 ───────────────────────────────────

func TestProxy_RoundRobinRouting(t *testing.T) {
	be1 := mockBackend("backend-1", 0)
	defer be1.Close()
	be2 := mockBackend("backend-2", 0)
	defer be2.Close()

	backends := []*Backend{
		parseBackendURL(be1.URL),
		parseBackendURL(be2.URL),
	}

	proxy := NewReverseProxy(backends, NewRoundRobinLoadBalancer(), 5*time.Second, "", nil)

	// 发送 4 个请求，交替命中 be1 → be2 → be1 → be2
	responses := []string{}
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)

		data := readJSONBody(t, rec.Result())
		responses = append(responses, data["body"].(string))
	}

	expected := []string{"backend-1", "backend-2", "backend-1", "backend-2"}
	for i, exp := range expected {
		if responses[i] != exp {
			t.Errorf("call %d: expected %q, got %q", i, exp, responses[i])
		}
	}
}

// ─── 测试 2：strip_prefix 路径重写 ──────────────────────────

func TestProxy_StripPrefix(t *testing.T) {
	be := mockBackend("ok", 0)
	defer be.Close()

	backends := []*Backend{parseBackendURL(be.URL)}

	proxy := NewReverseProxy(
		backends,
		NewRoundRobinLoadBalancer(),
		5*time.Second,
		"/api/v1/users", // strip_prefix
		map[string]string{},
	)

	tests := []struct {
		name       string
		requestURL string
		wantPath   string
	}{
		{
			name:       "strip prefix from path",
			requestURL: "/api/v1/users/profile",
			wantPath:   "/profile",
		},
		{
			name:       "strip prefix at root",
			requestURL: "/api/v1/users",
			wantPath:   "/",
		},
		{
			name:       "no matching prefix",
			requestURL: "/api/v1/orders/list",
			wantPath:   "/api/v1/orders/list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.requestURL, nil)
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, req)

			data := readJSONBody(t, rec.Result())
			if data["path"] != tt.wantPath {
				t.Errorf("expected path %q, got %q", tt.wantPath, data["path"])
			}
		})
	}
}

// ─── 测试 3：自定义 Header 注入 ─────────────────────────────

func TestProxy_SetHeaders(t *testing.T) {
	be := mockBackend("ok", 0)
	defer be.Close()

	backends := []*Backend{parseBackendURL(be.URL)}

	proxy := NewReverseProxy(
		backends,
		NewRoundRobinLoadBalancer(),
		5*time.Second,
		"",
		map[string]string{
			"X-Gateway-Name": "mini-gateway",
			"X-Route-Name":   "test-route",
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	data := readJSONBody(t, rec.Result())

	if data["x-gateway"] != "mini-gateway" {
		t.Errorf("expected X-Gateway-Name 'mini-gateway', got %q", data["x-gateway"])
	}
	if data["x-route"] != "test-route" {
		t.Errorf("expected X-Route-Name 'test-route', got %q", data["x-route"])
	}
}

// ─── 测试 4：超时控制 → 504 Gateway Timeout ──────────────────

func TestProxy_Timeout(t *testing.T) {
	be := mockBackend("ok", 200*time.Millisecond) // 后端处理需要 200ms
	defer be.Close()

	backends := []*Backend{parseBackendURL(be.URL)}

	// 设置极短超时 10ms，后端 200ms → 必然超时
	proxy := NewReverseProxy(
		backends,
		NewRoundRobinLoadBalancer(),
		10*time.Millisecond,
		"",
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/slow", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("expected 504 Gateway Timeout, got %d", resp.StatusCode)
	}

	// 验证 JSON 错误格式
	data := readJSONBody(t, resp)
	errObj, ok := data["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'error' object in response")
	}
	if errObj["code"] != "GATEWAY_TIMEOUT" {
		t.Errorf("expected error code GATEWAY_TIMEOUT, got %q", errObj["code"])
	}
}

// ─── 测试 5：502 Bad Gateway（后端不可达）─────────────────────

func TestProxy_BadGateway(t *testing.T) {
	be := mockBackend("ok", 0)
	backends := []*Backend{parseBackendURL(be.URL)}
	be.Close() // 立即关闭后端

	proxy := NewReverseProxy(backends, NewRoundRobinLoadBalancer(), 5*time.Second, "", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway, got %d", resp.StatusCode)
	}

	data := readJSONBody(t, resp)
	errObj, ok := data["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'error' object in response")
	}
	if errObj["code"] != "BAD_GATEWAY" {
		t.Errorf("expected error code BAD_GATEWAY, got %q", errObj["code"])
	}
}

// ─── 测试 6：空后端列表 → 502 ────────────────────────────────

func TestProxy_NoBackends(t *testing.T) {
	proxy := NewReverseProxy(nil, NewRoundRobinLoadBalancer(), 5*time.Second, "", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway, got %d", resp.StatusCode)
	}
}

// ─── 测试 7：SetTransport / SetTimeout ───────────────────────

func TestProxy_SetTransportAndTimeout(t *testing.T) {
	be := mockBackend("ok", 0)
	defer be.Close()

	backends := []*Backend{parseBackendURL(be.URL)}
	proxy := NewReverseProxy(backends, NewRoundRobinLoadBalancer(), 5*time.Second, "", nil)

	// SetTransport
	customTransport := &http.Transport{
		MaxIdleConns: 10,
	}
	proxy.SetTransport(customTransport)
	if proxy.rp.Transport != customTransport {
		t.Error("SetTransport did not set the transport")
	}

	// SetTimeout
	proxy.SetTimeout(30 * time.Second)
	if proxy.timeout != 30*time.Second {
		t.Errorf("SetTimeout: expected 30s, got %v", proxy.timeout)
	}
}

// ─── 测试 8：路径前缀不匹配时的 trim 边界 ─────────────────────

func TestProxy_StripPrefix_NoPartialMatch(t *testing.T) {
	be := mockBackend("ok", 0)
	defer be.Close()

	backends := []*Backend{parseBackendURL(be.URL)}

	// stripPrefix="/api/v1/users"，不应误删 "/api/v1/users-admin"
	proxy := NewReverseProxy(
		backends,
		NewRoundRobinLoadBalancer(),
		5*time.Second,
		"/api/v1/users",
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users-admin/profile", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	data := readJSONBody(t, rec.Result())
	// 不应部分匹配 — path 应保持不变
	if data["path"] != "/api/v1/users-admin/profile" {
		t.Errorf("expected path unchanged, got %q", data["path"])
	}
}

// ─── 测试 9：请求体透传 ──────────────────────────────────────

func TestProxy_RequestBodyPassthrough(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"received_body": string(body),
		})
	}))
	defer be.Close()

	backends := []*Backend{parseBackendURL(be.URL)}
	proxy := NewReverseProxy(backends, NewRoundRobinLoadBalancer(), 5*time.Second, "", nil)

	reqBody := `{"name": "test", "value": 42}`
	req := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	data := readJSONBody(t, rec.Result())
	if data["received_body"] != reqBody {
		t.Errorf("expected request body %q, got %q", reqBody, data["received_body"])
	}
}
