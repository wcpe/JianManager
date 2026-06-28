package httpclient

import (
	"sync"
	"testing"
)

// TestNewProvider_NonNilClient 持有者初始化后 Client() 返回非 nil 的 *http.Client。
func TestNewProvider_NonNilClient(t *testing.T) {
	p, err := NewProvider(Config{})
	if err != nil {
		t.Fatalf("NewProvider 失败: %v", err)
	}
	if p.Client() == nil {
		t.Fatal("Client() 不应为 nil")
	}
}

// TestNewProvider_InvalidConfigFailFast 非法代理配置应在初始化即报错（fail-fast）。
func TestNewProvider_InvalidConfigFailFast(t *testing.T) {
	if _, err := NewProvider(Config{URL: "ftp://example:1080"}); err == nil {
		t.Fatal("非法 scheme 应返回 error")
	}
}

// TestProvider_RebuildReplacesClient Rebuild 成功后 Client() 返回新实例（持有者被替换）。
func TestProvider_RebuildReplacesClient(t *testing.T) {
	p, err := NewProvider(Config{})
	if err != nil {
		t.Fatalf("NewProvider 失败: %v", err)
	}
	old := p.Client()

	if err := p.Rebuild(Config{URL: "http://127.0.0.1:7890"}); err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	got := p.Client()
	if got == old {
		t.Fatal("Rebuild 后应替换为新的 *http.Client 实例")
	}
	if got == nil {
		t.Fatal("Rebuild 后 Client() 不应为 nil")
	}
}

// TestProvider_RebuildInvalidKeepsOld Rebuild 传非法配置应返回 error 且不替换原 client（避免半生效）。
func TestProvider_RebuildInvalidKeepsOld(t *testing.T) {
	p, err := NewProvider(Config{URL: "http://127.0.0.1:7890"})
	if err != nil {
		t.Fatalf("NewProvider 失败: %v", err)
	}
	old := p.Client()

	if err := p.Rebuild(Config{URL: "://bad"}); err == nil {
		t.Fatal("非法配置 Rebuild 应返回 error")
	}
	if p.Client() != old {
		t.Fatal("Rebuild 失败时应保留原 client，不得替换")
	}
}

// TestProvider_ConcurrentAccess 并发 Client()/Rebuild 不触发竞态（go test -race 校验）。
func TestProvider_ConcurrentAccess(t *testing.T) {
	p, err := NewProvider(Config{})
	if err != nil {
		t.Fatalf("NewProvider 失败: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if c := p.Client(); c == nil {
				t.Error("并发 Client() 返回 nil")
			}
		}()
		go func(n int) {
			defer wg.Done()
			cfg := Config{}
			if n%2 == 0 {
				cfg.URL = "http://127.0.0.1:7890"
			}
			_ = p.Rebuild(cfg)
		}(i)
	}
	wg.Wait()
	if p.Client() == nil {
		t.Fatal("并发收尾后 Client() 不应为 nil")
	}
}

// TestProxyGeneration_StableAndSensitive 同配置哈希稳定、不同配置哈希不同。
func TestProxyGeneration_StableAndSensitive(t *testing.T) {
	a := ProxyGeneration(Config{URL: "http://127.0.0.1:7890", NoProxy: "localhost"})
	b := ProxyGeneration(Config{URL: "http://127.0.0.1:7890", NoProxy: "localhost"})
	if a != b {
		t.Fatalf("同配置哈希应一致：%q vs %q", a, b)
	}
	if a == "" {
		t.Fatal("非空配置哈希不应为空串")
	}

	// url 变化 → 哈希变化。
	if ProxyGeneration(Config{URL: "http://127.0.0.1:7890"}) == ProxyGeneration(Config{URL: "socks5://127.0.0.1:1080"}) {
		t.Fatal("url 不同应得不同哈希")
	}
	// no_proxy 变化 → 哈希变化。
	if ProxyGeneration(Config{URL: "http://p:1", NoProxy: "a"}) == ProxyGeneration(Config{URL: "http://p:1", NoProxy: "b"}) {
		t.Fatal("no_proxy 不同应得不同哈希")
	}
}

// TestProxyGeneration_EmptyIsEmpty 空配置哈希为空串（语义「无显式代理」，便于与「未下发」区分）。
func TestProxyGeneration_EmptyIsEmpty(t *testing.T) {
	if g := ProxyGeneration(Config{}); g != "" {
		t.Fatalf("空配置哈希应为空串，得 %q", g)
	}
}
