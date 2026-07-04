package router

import (
	"net/http"
	"time"
)

// Route 表示一条完整的路由规则，封装了匹配条件和后端转发所需的所有配置。
// 它直接映射 config.yaml 中 routes[] 的一条记录。
type Route struct {
	Name          string
	Method        string
	Matcher       PathMatcher
	Handler       http.Handler // 该路由对应的处理器（中间件链 → Proxy）
	Backends      []BackendConfig
	LBStrategy    string
	SkipAuth      bool
	SkipRateLimit bool
	RateLimit     *RouteRateLimitConfig
	Timeout       time.Duration
	Retry         int
	StripPrefix   string
	SetHeaders    map[string]string
}

// BackendConfig 后端服务配置
type BackendConfig struct {
	URL    string
	Weight int
}

// RouteRateLimitConfig 路由级限流配置
type RouteRateLimitConfig struct {
	Rate  int
	Burst int
}

// PathMatcher 路径匹配器接口。
type PathMatcher interface {
	// Match 判断给定路径是否匹配该规则。返回 true 表示命中。
	Match(path string) bool
}

// Router HTTP 路由器接口 — 整个网关的请求入口。
type Router interface {
	http.Handler

	// AddRoute 注册一条路由规则。
	AddRoute(route Route) error

	// Match 查找与给定请求匹配的路由。返回 nil 表示未找到。
	Match(r *http.Request) *Route

	// Routes 返回所有已注册的路由列表（调试用）。
	Routes() []Route
}
