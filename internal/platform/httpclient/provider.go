package httpclient

import (
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// Provider 是「可运行时重建的出站 *http.Client 持有者」（FR-185，见 ADR-043）。
//
// 出站代理升级为运行时可热生效后，CP/Worker 不再只在启动时 httpclient.New 一次，
// 而是持有一个 Provider：各下载点每次取当前 client（Client()），保存/下发新代理后
// 经 Rebuild 原子替换持有的 client，对后续下载即时生效、无需重启。
// 内部用 atomic.Pointer 保证并发取用/替换无锁安全（多下载 goroutine 与设置保存并发）。
type Provider struct {
	cur atomic.Pointer[http.Client]
}

// NewProvider 按初始代理配置构造持有者。
// cfg 非法（不支持 scheme / 不可解析）时返回 error（同 New，供启动时 fail-fast）。
func NewProvider(cfg Config) (*Provider, error) {
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	p := &Provider{}
	p.cur.Store(c)
	return p, nil
}

// Client 返回当前持有的出站 *http.Client（并发安全）。
// 始终非 nil（NewProvider 保证初始化，Rebuild 仅在成功时替换）。
func (p *Provider) Client() *http.Client {
	return p.cur.Load()
}

// Rebuild 按新代理配置重建并原子替换持有的 client。
// cfg 非法时返回 error 且**不替换**原 client（避免半生效——保存非法配置不应让出站直连）。
func (p *Provider) Rebuild(cfg Config) error {
	c, err := New(cfg)
	if err != nil {
		return err
	}
	p.cur.Store(c)
	return nil
}

// ProxyGeneration 返回代理配置的稳定哈希「代标识」（FR-185，见 ADR-043）。
//
// 用于心跳下发时的 generation 比较：CP 据期望代理算 generation 随心跳下发，
// Worker 仅当与本地已应用代不同时才重建 client（避免每拍重建）。
// 空配置（无显式代理）返回空串，便于与「期望直连」「未下发」语义对齐：
// 二者都应得空 generation，Worker 收到空 generation 时回退本地 yaml/env、不强制重建。
// 非空配置返回 url 与 no_proxy 的 FNV-1a 十六进制哈希（url 变化或 no_proxy 变化都改变哈希）。
func ProxyGeneration(cfg Config) string {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return ""
	}
	h := fnv.New64a()
	// 以 \x00 分隔避免「url+noProxy」拼接歧义（如 "ab"+"c" 与 "a"+"bc"）。
	_, _ = h.Write([]byte(url))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSpace(cfg.NoProxy)))
	return strconv.FormatUint(h.Sum64(), 16)
}
