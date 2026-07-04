package proxy

import (
	"net/url"
	"sync"
	"testing"
)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestRoundRobin_EqualWeights(t *testing.T) {
	backends := []*Backend{
		{URL: mustParseURL("http://localhost:9001"), Weight: 1, Healthy: true},
		{URL: mustParseURL("http://localhost:9002"), Weight: 1, Healthy: true},
		{URL: mustParseURL("http://localhost:9003"), Weight: 1, Healthy: true},
	}

	lb := NewRoundRobinLoadBalancer()

	// 3 轮循环，每轮应该依次返回 9001 → 9002 → 9003
	for round := 0; round < 3; round++ {
		for i, expectedPort := range []string{"9001", "9002", "9003"} {
			got := lb.Next(backends)
			if got == nil {
				t.Fatalf("round %d, call %d: expected non-nil backend", round, i)
			}
			if got.URL.Port() != expectedPort {
				t.Errorf("round %d, call %d: expected port %s, got %s",
					round, i, expectedPort, got.URL.Port())
			}
		}
	}
}

func TestRoundRobin_WeightedDistribution(t *testing.T) {
	backends := []*Backend{
		{URL: mustParseURL("http://localhost:9001"), Weight: 5, Healthy: true},
		{URL: mustParseURL("http://localhost:9002"), Weight: 1, Healthy: true},
	}

	lb := NewRoundRobinLoadBalancer()

	counts := map[string]int{}
	totalCalls := 12 // 权重比 5:1 → 预期 10:2

	for i := 0; i < totalCalls; i++ {
		b := lb.Next(backends)
		if b == nil {
			t.Fatal("expected non-nil backend")
		}
		counts[b.URL.Port()]++
	}

	// 9001 (weight 5) 应获取约 10 次
	if counts["9001"] != 10 {
		t.Errorf("port 9001 (weight 5): expected 10, got %d", counts["9001"])
	}
	// 9002 (weight 1) 应获取约 2 次
	if counts["9002"] != 2 {
		t.Errorf("port 9002 (weight 1): expected 2, got %d", counts["9002"])
	}
}

func TestRoundRobin_EmptyList(t *testing.T) {
	lb := NewRoundRobinLoadBalancer()

	got := lb.Next(nil)
	if got != nil {
		t.Errorf("expected nil for nil backends, got %v", got)
	}

	got = lb.Next([]*Backend{})
	if got != nil {
		t.Errorf("expected nil for empty backends, got %v", got)
	}
}

func TestRoundRobin_SingleBackend(t *testing.T) {
	backends := []*Backend{
		{URL: mustParseURL("http://localhost:9001"), Weight: 3, Healthy: true},
	}

	lb := NewRoundRobinLoadBalancer()

	for i := 0; i < 10; i++ {
		got := lb.Next(backends)
		if got == nil {
			t.Fatal("expected non-nil backend")
		}
		if got.URL.Port() != "9001" {
			t.Errorf("call %d: expected port 9001, got %s", i, got.URL.Port())
		}
	}
}

func TestRoundRobin_ConcurrentSafety(t *testing.T) {
	backends := []*Backend{
		{URL: mustParseURL("http://localhost:9001"), Weight: 1, Healthy: true},
		{URL: mustParseURL("http://localhost:9002"), Weight: 1, Healthy: true},
	}

	lb := NewRoundRobinLoadBalancer()

	var wg sync.WaitGroup
	numGoroutines := 50
	callsPerGoroutine := 100

	// 收集所有选中的后端 URL
	results := make(chan string, numGoroutines*callsPerGoroutine)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < callsPerGoroutine; i++ {
				b := lb.Next(backends)
				if b != nil {
					results <- b.URL.Port()
				}
			}
		}()
	}

	wg.Wait()
	close(results)

	// 统计分布
	counts := map[string]int{}
	total := 0
	for port := range results {
		counts[port]++
		total++
	}

	if total != numGoroutines*callsPerGoroutine {
		t.Errorf("expected %d total calls, got %d", numGoroutines*callsPerGoroutine, total)
	}

	// 等权重下两个后端应各获取约 50%
	ratio9001 := float64(counts["9001"]) / float64(total)
	if ratio9001 < 0.45 || ratio9001 > 0.55 {
		t.Errorf("port 9001 ratio should be ~0.5, got %.3f (%d/%d)", ratio9001, counts["9001"], total)
	}
}

func TestRoundRobin_DifferentWeights(t *testing.T) {
	backends := []*Backend{
		{URL: mustParseURL("http://a.local"), Weight: 7, Healthy: true},
		{URL: mustParseURL("http://b.local"), Weight: 2, Healthy: true},
		{URL: mustParseURL("http://c.local"), Weight: 1, Healthy: true},
	}

	lb := NewRoundRobinLoadBalancer()

	counts := map[string]int{}
	totalCalls := 100 // 权重比 7:2:1 → 预期 70:20:10

	for i := 0; i < totalCalls; i++ {
		b := lb.Next(backends)
		if b == nil {
			t.Fatal("expected non-nil backend")
		}
		counts[b.URL.Host]++
	}

	if counts["a.local"] != 70 {
		t.Errorf("a.local (weight 7): expected 70, got %d", counts["a.local"])
	}
	if counts["b.local"] != 20 {
		t.Errorf("b.local (weight 2): expected 20, got %d", counts["b.local"])
	}
	if counts["c.local"] != 10 {
		t.Errorf("c.local (weight 1): expected 10, got %d", counts["c.local"])
	}
}
