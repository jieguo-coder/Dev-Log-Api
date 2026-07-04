package proxy

import (
	"net/url"
	"sync"
)

// Backend 表示一个后端服务实例。
type Backend struct {
	// URL 后端服务地址，如 "http://localhost:9001"
	URL *url.URL
	// Weight 负载均衡权重
	Weight int
	// Healthy 健康状态（预留）
	Healthy bool
}

// LoadBalancer 负载均衡策略接口。
// 实现必须保证线程安全。
type LoadBalancer interface {
	// Next 从后端列表中选取一个后端。
	Next(backends []*Backend) *Backend
}

// RoundRobinLoadBalancer 加权轮询负载均衡。
//
// 算法：按权重比例分配请求。例如权重 [5, 1] 的后端列表，
// 每 6 个请求中有 5 个路由到 backend[0]，1 个路由到 backend[1]。
//
// 并发安全：使用 sync.Mutex 保护计数器状态。
type RoundRobinLoadBalancer struct {
	mu      sync.Mutex
	current int
}

// NewRoundRobinLoadBalancer 创建一个新的加权轮询负载均衡器。
func NewRoundRobinLoadBalancer() *RoundRobinLoadBalancer {
	return &RoundRobinLoadBalancer{}
}

// Next 实现 LoadBalancer 接口，按加权轮询策略选取下一个后端。
// 若列表为空则返回 nil。
func (lb *RoundRobinLoadBalancer) Next(backends []*Backend) *Backend {
	if len(backends) == 0 {
		return nil
	}

	lb.mu.Lock()
	defer lb.mu.Unlock()

	// 计算总权重
	totalWeight := 0
	for _, b := range backends {
		totalWeight += b.Weight
	}

	// 若所有权重均为 0，降级为简单轮询，避免除零 panic
	if totalWeight == 0 {
		idx := lb.current % len(backends)
		lb.current++
		return backends[idx]
	}

	// 按权重范围定位：将 current 映射到 [0, totalWeight) 区间
	pos := lb.current % totalWeight
	lb.current++

	for _, b := range backends {
		if pos < b.Weight {
			return b
		}
		pos -= b.Weight
	}

	// 防御：不应到达此处，回退到首个后端
	return backends[0]
}
