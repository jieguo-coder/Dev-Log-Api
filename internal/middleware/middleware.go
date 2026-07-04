package middleware

import (
	"context"
	"net/http"
)

// Middleware 表示一个可组合的 HTTP 中间件。
// 遵循标准库的 func(http.Handler) http.Handler 契约。
type Middleware func(http.Handler) http.Handler

// Chain 将多个中间件按顺序组合成一个 http.Handler。
// 执行顺序（洋葱模型）：mw[0] → mw[1] → ... → mw[n-1] → final
func Chain(middlewares ...Middleware) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		h := final
		for i := len(middlewares) - 1; i >= 0; i-- {
			h = middlewares[i](h)
		}
		return h
	}
}

// ─── Context Keys ───────────────────────────────────────────

type contextKey string

const (
	// ClaimsContextKey 用于在 context 中存储 JWT Claims。
	ClaimsContextKey contextKey = "gateway:jwt_claims"

	// RouteConfigContextKey 用于在 context 中存储匹配到的路由配置。
	// Router 在匹配成功后注入，后续 Middleware 通过它获取路由配置。
	RouteConfigContextKey contextKey = "gateway:route_config"
)

// WithRouteConfig 将路由配置注入 context。
func WithRouteConfig(ctx context.Context, cfg interface{}) context.Context {
	return context.WithValue(ctx, RouteConfigContextKey, cfg)
}

// RouteConfigFromContext 从 context 中提取路由配置。
func RouteConfigFromContext(ctx context.Context) interface{} {
	return ctx.Value(RouteConfigContextKey)
}
