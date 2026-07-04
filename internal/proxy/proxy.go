package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

// Proxy 反向代理接口。
// 负责将请求转发到选定的后端服务，并将响应写回客户端。
type Proxy interface {
	http.Handler

	// SetTransport 设置底层 HTTP Transport（用于连接池、TLS 配置等）。
	SetTransport(rt http.RoundTripper)

	// SetTimeout 设置单次代理请求的超时时间。
	SetTimeout(d time.Duration)
}

// ReverseProxy 基于标准库 net/http/httputil.ReverseProxy 的代理实现。
//
// 每个实例绑定一条路由规则，封装了该路由的后端列表、负载均衡策略、
// 路径重写和自定义 Header 注入逻辑。
type ReverseProxy struct {
	rp           *httputil.ReverseProxy
	loadBalancer LoadBalancer
	backends     []*Backend
	timeout      time.Duration
	stripPrefix  string
	setHeaders   map[string]string
}

// NewReverseProxy 创建一个反向代理实例。
// backends: 后端服务列表
// lb: 负载均衡策略
// timeout: 代理请求超时时间
// stripPrefix: 转发前去除的路径前缀
// setHeaders: 转发时注入的自定义请求头
func NewReverseProxy(backends []*Backend, lb LoadBalancer, timeout time.Duration, stripPrefix string, setHeaders map[string]string) *ReverseProxy {
	p := &ReverseProxy{
		loadBalancer: lb,
		backends:     backends,
		timeout:      timeout,
		stripPrefix:  stripPrefix,
		setHeaders:   setHeaders,
	}

	// 构建 httputil.ReverseProxy，注入自定义 Director
	p.rp = &httputil.ReverseProxy{
		Director:     p.director,
		ErrorHandler: p.errorHandler,
	}

	return p
}

// director 是 httputil.ReverseProxy 的 Director 函数。
// 在每次代理请求前被调用，负责：
//   1. 后端寻址：通过负载均衡器选择目标后端，改写 req.URL
//   2. 路径重写：剔除 strip_prefix
//   3. Header 注入：写入 set_headers 中的自定义头
func (p *ReverseProxy) director(req *http.Request) {
	// 1. 后端寻址
	backend := p.loadBalancer.Next(p.backends)
	if backend == nil {
		// 无可用后端，由 ErrorHandler 统一处理
		return
	}

	req.URL.Scheme = backend.URL.Scheme
	req.URL.Host = backend.URL.Host

	// 2. 路径重写：剔除 strip_prefix（要求前缀后必须紧跟 "/" 或到达末尾）
	if p.stripPrefix != "" {
		path := req.URL.Path
		if path == p.stripPrefix {
			// 精确命中前缀末尾：/api/v1/users → /
			req.URL.Path = "/"
		} else if after, ok := strings.CutPrefix(path, p.stripPrefix); ok && strings.HasPrefix(after, "/") {
			// 前缀匹配：/api/v1/users/profile → /profile
			req.URL.Path = after
		}
		// 否则不匹配（如 /api/v1/users-admin），保留原路径不变
	}

	// 3. Header 注入
	for k, v := range p.setHeaders {
		req.Header.Set(k, v)
	}

	// 设置 Host 头指向目标后端
	req.Host = backend.URL.Host
}

// errorHandler 统一处理代理过程中的错误，返回 JSON 格式的错误响应。
func (p *ReverseProxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("proxy error",
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)

	statusCode, errorCode := classifyProxyError(err)
	writeErrorJSON(w, statusCode, errorCode, err.Error())
}

// classifyProxyError 根据错误类型映射 HTTP 状态码和业务错误码。
// 使用 errors.Is / errors.As 进行类型断言，而非字符串匹配。
func classifyProxyError(err error) (int, string) {
	// 超时：context.DeadlineExceeded（httputil 通过 context 超时取消请求）
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "GATEWAY_TIMEOUT"
	}

	// 连接层错误：*net.OpError（连接拒绝、DNS 解析失败等）
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return http.StatusBadGateway, "BAD_GATEWAY"
	}

	// 其余归类为内部错误（包括未知的传输层错误）
	return http.StatusInternalServerError, "INTERNAL_ERROR"
}

// writeErrorJSON 按 SPEC 第 5.2 节格式写入 JSON 错误响应。
func writeErrorJSON(w http.ResponseWriter, statusCode int, errorCode, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    errorCode,
			"message": message,
		},
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	// 从 context 中尝试提取 request_id（如果 Router 已注入）
	// 当前 MVP 阶段 Router 尚未实现，使用占位符
	resp["request_id"] = "-"

	json.NewEncoder(w).Encode(resp)
}

// SetTransport 设置底层 HTTP Transport。
func (p *ReverseProxy) SetTransport(rt http.RoundTripper) {
	p.rp.Transport = rt
}

// SetTimeout 设置单次代理请求的超时时间。
func (p *ReverseProxy) SetTimeout(d time.Duration) {
	p.timeout = d
}

// ServeHTTP 实现 http.Handler，执行反向代理逻辑。
//
// 流程：
//   1. 对请求 context 注入超时控制
//   2. 委托给 httputil.ReverseProxy 执行代理
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.timeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
		defer cancel()
		r = r.WithContext(ctx)
	}

	// 若没有可用后端，直接返回 502
	if len(p.backends) == 0 {
		writeErrorJSON(w, http.StatusBadGateway, "BAD_GATEWAY", "no backend available")
		return
	}

	p.rp.ServeHTTP(w, r)
}

// 确保 ReverseProxy 实现了 Proxy 接口
var _ Proxy = (*ReverseProxy)(nil)
