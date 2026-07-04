package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-key-for-unit-tests"

// newTestJWTAuth 创建测试用的 JWTAuth 实例。
func newTestJWTAuth() *JWTAuth {
	auth, _ := NewJWTAuth(JWTConfig{
		Secret:    []byte(testSecret),
		Algorithm: "HS256",
		RequiredClaims: []string{"sub", "exp"},
	})
	return auth
}

// createValidToken 生成一个有效的 JWT token 用于测试。
func createValidToken(t *testing.T, sub string, exp time.Time) string {
	t.Helper()
	claims := &JWTCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("failed to create test token: %v", err)
	}
	return tokenString
}

// createExpiredToken 生成一个已过期的 JWT token。
func createExpiredToken(t *testing.T, sub string) string {
	t.Helper()
	return createValidToken(t, sub, time.Now().Add(-1*time.Hour))
}

// createTokenWithWrongSecret 使用不同密钥签名 token（模拟伪造）。
func createTokenWithWrongSecret(t *testing.T, sub string) string {
	t.Helper()
	claims := &JWTCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte("wrong-secret-key"))
	if err != nil {
		t.Fatalf("failed to create test token: %v", err)
	}
	return tokenString
}

// readErrorJSON 读取错误响应 JSON。
func readErrorJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("failed to parse JSON: %v\nBody: %s", err, string(body))
	}
	return data
}

// echoHandler 用于测试的简单 handler，回显 context 中的 Claims。
func echoHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := FromContext(r.Context())
		if claims == nil {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"claims": null}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sub": claims.Subject,
			"exp": claims.ExpiresAt.Time.Unix(),
		})
	})
}

// ─── 测试 1：合法 Token → 放行 + Context 中有 Claims ─────────

func TestJWTAuth_ValidToken(t *testing.T) {
	auth := newTestJWTAuth()
	handler := auth.Middleware()(echoHandler(t))

	token := createValidToken(t, "user-123", time.Now().Add(1*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	defer resp.Body.Close()

	var data map[string]interface{}
	json.Unmarshal(body, &data)

	if data["sub"] != "user-123" {
		t.Errorf("expected sub 'user-123', got %v", data["sub"])
	}
}

// ─── 测试 2：无 Token → 401 ─────────────────────────────────

func TestJWTAuth_MissingToken(t *testing.T) {
	auth := newTestJWTAuth()
	handler := auth.Middleware()(echoHandler(t))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	// 不设置 Authorization header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	data := readErrorJSON(t, resp)
	errObj := data["error"].(map[string]interface{})
	if errObj["code"] != "UNAUTHORIZED" {
		t.Errorf("expected code UNAUTHORIZED, got %q", errObj["code"])
	}
}

// ─── 测试 3：错误格式的 Authorization Header → 401 ──────────

func TestJWTAuth_MalformedHeader(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty header", ""},
		{"no Bearer prefix", "token-abc123"},
		{"Basic auth instead of Bearer", "Basic dXNlcjpwYXNz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := newTestJWTAuth()
			handler := auth.Middleware()(echoHandler(t))

			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			if tt.value != "" {
				req.Header.Set("Authorization", tt.value)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Result().StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", rec.Result().StatusCode)
			}
		})
	}
}

// ─── 测试 4：过期 Token → 401 ───────────────────────────────

func TestJWTAuth_ExpiredToken(t *testing.T) {
	auth := newTestJWTAuth()
	handler := auth.Middleware()(echoHandler(t))

	token := createExpiredToken(t, "user-123")

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	data := readErrorJSON(t, resp)
	errObj := data["error"].(map[string]interface{})
	if errObj["code"] != "UNAUTHORIZED" {
		t.Errorf("expected code UNAUTHORIZED, got %q", errObj["code"])
	}
}

// ─── 测试 5：签名错误的伪造 Token → 401 ─────────────────────

func TestJWTAuth_WrongSignature(t *testing.T) {
	auth := newTestJWTAuth()
	handler := auth.Middleware()(echoHandler(t))

	token := createTokenWithWrongSecret(t, "attacker")

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for forged token, got %d", resp.StatusCode)
	}

	data := readErrorJSON(t, resp)
	errObj := data["error"].(map[string]interface{})
	if errObj["code"] != "UNAUTHORIZED" {
		t.Errorf("expected code UNAUTHORIZED, got %q", errObj["code"])
	}
}

// ─── 测试 6：从 Context 取 Claims 的便捷函数 ─────────────────

func TestJWTAuth_FromContext(t *testing.T) {
	auth := newTestJWTAuth()

	var capturedClaims *JWTCustomClaims
	handler := auth.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedClaims = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	token := createValidToken(t, "ctx-test-user", time.Now().Add(1*time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Result().StatusCode)
	}
	if capturedClaims == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if capturedClaims.Subject != "ctx-test-user" {
		t.Errorf("expected sub 'ctx-test-user', got %q", capturedClaims.Subject)
	}
}

// ─── 测试 7：空 Context 的 FromContext → nil ─────────────────

func TestJWTAuth_FromContext_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	claims := FromContext(req.Context())
	if claims != nil {
		t.Errorf("expected nil from empty context, got %v", claims)
	}
}

// ─── 测试 8：Claims 缺少 sub → 403 ──────────────────────────

func TestJWTAuth_MissingRequiredClaim(t *testing.T) {
	auth, _ := NewJWTAuth(JWTConfig{
		Secret:    []byte(testSecret),
		Algorithm: "HS256",
		RequiredClaims: []string{"sub", "exp", "iss"}, // 要求 iss
	})

	handler := auth.Middleware()(echoHandler(t))

	// 生成不带 iss 的 token
	claims := &JWTCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte(testSecret))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d", resp.StatusCode)
	}

	data := readErrorJSON(t, resp)
	errObj := data["error"].(map[string]interface{})
	if errObj["code"] != "FORBIDDEN" {
		t.Errorf("expected code FORBIDDEN, got %q", errObj["code"])
	}
	if errObj["message"] != "insufficient permissions" {
		t.Errorf("expected generic message, got %q", errObj["message"])
	}
}
