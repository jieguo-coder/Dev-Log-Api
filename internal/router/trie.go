package router

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/jieguo-coder/mini-gateway/internal/response"
)

// exactMatcher 精确匹配器
type exactMatcher struct {
	path string
}

func (m *exactMatcher) Match(path string) bool {
	return path == m.path
}

// NewExactMatcher 创建精确匹配器。
func NewExactMatcher(path string) PathMatcher {
	return &exactMatcher{path: path}
}

// prefixMatcher 前缀匹配器
type prefixMatcher struct {
	prefix string
}

func (m *prefixMatcher) Match(path string) bool {
	return strings.HasPrefix(path, m.prefix)
}

// NewPrefixMatcher 创建前缀匹配器。
func NewPrefixMatcher(prefix string) PathMatcher {
	return &prefixMatcher{prefix: prefix}
}

// TrieRouter 基于索引的路由器实现。
//
// 匹配优先级（从高到低）：
//   1. 精确匹配 (exact)：/api/v1/users
//   2. 前缀匹配 (prefix，长前缀优先)：/api/v1/users/ 优先于 /api/v1/
//
// 线程安全：AddRoute 必须在服务启动前完成，运行时只读。
type TrieRouter struct {
	routes       []Route                     // 所有路由
	exactRoutes  map[string]*Route           // "METHOD:/path" → Route（精确匹配）
	prefixRoutes []prefixRouteEntry          // 前缀路由列表（按前缀长度降序）
}

type prefixRouteEntry struct {
	route  *Route
	method string
}

// NewTrieRouter 创建一个新的路由器。
func NewTrieRouter() *TrieRouter {
	return &TrieRouter{
		exactRoutes: make(map[string]*Route),
	}
}

// AddRoute 注册一条路由规则。
func (r *TrieRouter) AddRoute(route Route) error {
	r.routes = append(r.routes, route)

	switch m := route.Matcher.(type) {
	case *exactMatcher:
		key := route.Method + ":" + m.path
		r.exactRoutes[key] = &r.routes[len(r.routes)-1]
	case *prefixMatcher:
		r.prefixRoutes = append(r.prefixRoutes, prefixRouteEntry{
			route:  &r.routes[len(r.routes)-1],
			method: route.Method,
		})
	}

	// 保持前缀路由按前缀长度降序排列（长前缀优先）
	sort.SliceStable(r.prefixRoutes, func(i, j int) bool {
		return len(r.prefixRoutes[i].route.Matcher.(*prefixMatcher).prefix) >
			len(r.prefixRoutes[j].route.Matcher.(*prefixMatcher).prefix)
	})

	return nil
}

// Match 查找与给定请求匹配的路由。
func (r *TrieRouter) Match(req *http.Request) *Route {
	path := req.URL.Path

	// 1. 精确匹配优先
	exactKey := req.Method + ":" + path
	if route, ok := r.exactRoutes[exactKey]; ok {
		return route
	}
	// 也尝试通配方法 (*)
	wildKey := "*:" + path
	if route, ok := r.exactRoutes[wildKey]; ok {
		return route
	}

	// 2. 前缀匹配（已排序：长前缀优先）
	for _, entry := range r.prefixRoutes {
		if entry.method != req.Method && entry.method != "*" {
			continue
		}
		if entry.route.Matcher.Match(path) {
			return entry.route
		}
	}

	return nil
}

// Routes 返回所有已注册的路由列表。
func (r *TrieRouter) Routes() []Route {
	return r.routes
}

// ServeHTTP 实现 http.Handler — 匹配路由 → 注入 context → 调用 Handler。
func (r *TrieRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	route := r.Match(req)
	if route == nil {
		response.WriteErrorJSON(w, req, http.StatusNotFound, "ROUTE_NOT_FOUND",
			"no matching route for "+req.Method+" "+req.URL.Path)
		return
	}

	// 将 RouteConfig 注入 context，后续中间件和 Proxy 通过它获取路由配置
	ctx := SetRouteContext(req.Context(), route)
	*req = *req.WithContext(ctx)

	if route.Handler != nil {
		route.Handler.ServeHTTP(w, req)
	} else {
		response.WriteErrorJSON(w, req, http.StatusInternalServerError, "INTERNAL_ERROR",
			"route has no handler")
	}
}

// ─── Context 传递 ────────────────────────────────────────────

type routeContextKey struct{}

// SetRouteContext 将匹配到的 Route 注入 context。
func SetRouteContext(ctx context.Context, route *Route) context.Context {
	return context.WithValue(ctx, routeContextKey{}, route)
}

// RouteFromContext 从 context 中提取匹配到的 Route。
func RouteFromContext(ctx context.Context) *Route {
	route, _ := ctx.Value(routeContextKey{}).(*Route)
	return route
}
