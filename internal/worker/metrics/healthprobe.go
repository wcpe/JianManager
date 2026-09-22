package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// FR-459 响应维度探针：判定「实例能否对外服务」。
//
// 与存活维度（internal/worker/process 的 ProbeInstanceEvidence：看进程/容器是否还在）互补：
//   - HTTP 探针：对 ServerProbe `/metrics`（ProbePort）做**轻量** GET，短超时（复用抓取上限
//     probeScrapeTimeoutCap），返回 2xx 视为响应。不复用 ScrapeServerProbe 的解析成本——
//     健康巡检只关心「可达且 2xx」，不需解析指标。
//   - TCP 探针：对实例服务端口（MC 的 server_port / 代理端口）做短超时 connect，拨通即视为响应。
//
// 两者只读、无副作用、可并发；失败原因按 metrics.ProbeErrorCategory 归类（timeout/refused/…）。

// healthProbeTCPTimeout 是 TCP 响应探针的默认短超时。
// 取 2s：明显短于心跳节拍（30s）与探针抓取上限（5s），足以覆盖本机 loopback 的 connect，
// 又不至于把单轮巡检拖长（60+ 实例逐台 connect 的最坏成本须可控）。
const healthProbeTCPTimeout = 2 * time.Second

// HTTPHealthProbe 对 ServerProbe `/metrics`（host:port）做轻量 GET 健康探测。
// 短超时取 probeScrapeTimeoutCap（与真实抓取同源），连通且返回 2xx 视为响应；否则返回错误。
// port<=0 视为未配置探针，直接返回错误（调用方据此跳过响应维度）。
func HTTPHealthProbe(host string, port int) error {
	if port <= 0 {
		return fmt.Errorf("探针端口未配置")
	}
	url := fmt.Sprintf("http://%s:%d/metrics", host, port)
	client := &http.Client{Timeout: probeScrapeTimeoutCap}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("探针健康探测失败: %w", err)
	}
	defer resp.Body.Close()
	// 只读状态码：丢弃正文但限读，避免连接因未读尽而无法复用/关闭。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("探针健康探测返回状态 %d", resp.StatusCode)
	}
	return nil
}

// TCPHealthProbe 对 host:port 做短超时 TCP connect 健康探测，拨通即视为响应。
// timeout<=0 时取默认 healthProbeTCPTimeout；port<=0 视为未配置端口，直接返回错误。
func TCPHealthProbe(host string, port int, timeout time.Duration) error {
	if port <= 0 {
		return fmt.Errorf("服务端口未配置")
	}
	if timeout <= 0 {
		timeout = healthProbeTCPTimeout
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return fmt.Errorf("服务端口健康探测失败: %w", err)
	}
	_ = conn.Close()
	return nil
}
