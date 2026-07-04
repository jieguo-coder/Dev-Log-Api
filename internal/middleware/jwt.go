package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig JWT 鉴权中间件配置（与 config 包解耦）。
type JWTConfig struct {
	Secret            []byte
	Algorithm         string
	RequiredClaims    []string
	IssuerAllowlist   map[string]bool
	AudienceAllowlist map[string]bool
}

// JWTAuth 提供 JWT 验证中间件。
type JWTAuth struct {
	secret            []byte
	signingMethod     jwt.SigningMethod
	requiredClaims    []string
	issuerAllowlist   map[string]bool
	audienceAllowlist map[string]bool
}

// JWTCustomClaims 自定义 Claims 结构。
type JWTCustomClaims struct {
	jwt.RegisteredClaims
	Extra map[string]interface{} `json:"extra,omitempty"`
}

// NewJWTAuth 创建 JWT 鉴权中间件实例。
func NewJWTAuth(cfg JWTConfig) (*JWTAuth, error) {
	method := jwt.GetSigningMethod(cfg.Algorithm)
	if method == nil {
		return nil, fmt.Errorf("unsupported JWT algorithm: %q", cfg.Algorithm)
	}

	return &JWTAuth{
		secret:            cfg.Secret,
		signingMethod:     method,
		requiredClaims:    cfg.RequiredClaims,
		issuerAllowlist:   cfg.IssuerAllowlist,
		audienceAllowlist: cfg.AudienceAllowlist,
	}, nil
}

// Middleware 返回标准 Middleware 签名的 JWT 鉴权函数。
func (a *JWTAuth) Middleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. 提取 Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				a.writeUnauthorized(w, "UNAUTHORIZED", "missing or malformed Authorization header")
				return
			}

			tokenString := strings.TrimPrefix(authHeader, "Bearer ")

			// 2. 解析并验证 token
			claims := &JWTCustomClaims{}
			token, err := jwt.ParseWithClaims(tokenString, claims,
				func(t *jwt.Token) (interface{}, error) {
					// 验证签名算法
					if t.Method.Alg() != a.signingMethod.Alg() {
						return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
					}
					return a.secret, nil
				},
				jwt.WithLeeway(5*time.Second), // 5 秒时钟偏差容忍
			)

			if err != nil || !token.Valid {
				slog.Warn("jwt validation failed", "error", err, "path", r.URL.Path)
				a.writeUnauthorized(w, "UNAUTHORIZED", fmt.Sprintf("invalid token: %v", err))
				return
			}

			// 3. 校验 Claims
			if err := a.validateClaims(claims); err != nil {
				slog.Warn("jwt claims validation failed", "error", err, "path", r.URL.Path)
				a.writeUnauthorized(w, "FORBIDDEN", err.Error())
				return
			}

			// 4. 注入 Claims 到 context，放行
			ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// validateClaims 校验 JWT Claims 的必填字段和白名单。
func (a *JWTAuth) validateClaims(claims *JWTCustomClaims) error {
	// 校验必填字段
	for _, required := range a.requiredClaims {
		switch required {
		case "sub":
			if claims.Subject == "" {
				return fmt.Errorf("required claim 'sub' is missing or empty")
			}
		case "exp":
			if claims.ExpiresAt == nil {
				return fmt.Errorf("required claim 'exp' is missing")
			}
		case "iat":
			if claims.IssuedAt == nil {
				return fmt.Errorf("required claim 'iat' is missing")
			}
		case "iss":
			if claims.Issuer == "" {
				return fmt.Errorf("required claim 'iss' is missing or empty")
			}
		case "aud":
			if len(claims.Audience) == 0 {
				return fmt.Errorf("required claim 'aud' is missing or empty")
			}
		}
	}

	// 校验 issuer 白名单
	if len(a.issuerAllowlist) > 0 && claims.Issuer != "" {
		if !a.issuerAllowlist[claims.Issuer] {
			return fmt.Errorf("issuer %q is not in the allowlist", claims.Issuer)
		}
	}

	// 校验 audience 白名单
	if len(a.audienceAllowlist) > 0 && len(claims.Audience) > 0 {
		found := false
		for _, aud := range claims.Audience {
			if a.audienceAllowlist[aud] {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("audience %v not in allowlist", claims.Audience)
		}
	}

	return nil
}

// writeUnauthorized 返回统一 JSON 格式的鉴权失败响应。
func (a *JWTAuth) writeUnauthorized(w http.ResponseWriter, code, message string) {
	statusCode := http.StatusUnauthorized
	if code == "FORBIDDEN" {
		statusCode = http.StatusForbidden
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
		"request_id": "-",
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}

	json.NewEncoder(w).Encode(resp)
}

// FromContext 从 context 中提取已验证的 JWT Claims。
// 返回 nil 表示 context 中没有 claims。
func FromContext(ctx context.Context) *JWTCustomClaims {
	claims, ok := ctx.Value(ClaimsContextKey).(*JWTCustomClaims)
	if !ok {
		return nil
	}
	return claims
}
