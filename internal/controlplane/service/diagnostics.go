package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 连通性测试超时：测试本身必须快失败，避免「先测再下载」的测试反而长时间挂起（FR-229）。
const (
	httpReachabilityTimeout = 10 * time.Second
	nodePingTimeout         = 5 * time.Second
)

// ErrInvalidTestURL 测试 URL 非法（仅允许公网 http/https 绝对 URL）。
var ErrInvalidTestURL = errors.New("invalid test url")

var (
	lookupHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	blockedPublicTestPrefixes = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
)

// DiagnosticsService 提供「先测后用」的连通性探测（FR-229）：
//   - 出站 HTTP 可达性：经 CP 当前出站客户端（含已配置代理，FR-185）GET 目标 URL，
//     供「代理设置测试」与「JDK 下载源测试」复用——用户先确认能通再下载，避免下载长卡死。
//   - 节点存活：经 gRPC 调用 Worker 轻量 GetVersion 主动探活（不读心跳缓存），
//     供 JDK 一键下载前「测试节点是否存活」，避免对离线/卡顿节点发起会卡死的下载。
//
// 注：出站 HTTP 测试从 CP 侧发起（经 CP 出站代理），反映「CP 能否到达该源」；
// Worker 侧实际下载的可达性差异（如 Worker 独立代理）不在本测试范围（避免新增 Worker RPC）。
type DiagnosticsService struct {
	db       *gorm.DB
	pool     *cpgrpc.ClientPool
	outbound func() *http.Client
}

// NewDiagnosticsService 创建连通性测试服务。出站客户端经 SetHTTPClientProvider 注入。
func NewDiagnosticsService(db *gorm.DB, pool *cpgrpc.ClientPool) *DiagnosticsService {
	return &DiagnosticsService{db: db, pool: pool}
}

// SetHTTPClientProvider 注入「取当前出站 *http.Client」的提供者（FR-185 Provider.Client），
// 使测试随运行时代理变更即时生效（与各下载点一致）。
func (s *DiagnosticsService) SetHTTPClientProvider(fn func() *http.Client) {
	s.outbound = fn
}

// HTTPReachabilityResult 出站 HTTP 可达性测试结果。
type HTTPReachabilityResult struct {
	OK        bool   `json:"ok"`               // 是否成功收到 HTTP 响应（能连上即 ok，不看状态码语义）
	Status    int    `json:"status,omitempty"` // HTTP 状态码（连上才有）
	LatencyMs int64  `json:"latencyMs"`        // 往返毫秒
	Error     string `json:"error,omitempty"`  // 失败原因（连接/超时/DNS 等）
}

// NodePingResult 节点存活探测结果。
type NodePingResult struct {
	Alive     bool   `json:"alive"`
	LatencyMs int64  `json:"latencyMs"`
	Version   string `json:"version,omitempty"` // Worker 版本（存活时）
	Os        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Error     string `json:"error,omitempty"`
}

// TestHTTPReachability 经出站客户端 GET 目标 URL，返回是否可达 + 状态码 + 往返耗时。
// 仅允许解析到公网地址的 http/https 绝对 URL，并拒绝重定向，避免该诊断入口成为内网探测通道。
func (s *DiagnosticsService) TestHTTPReachability(ctx context.Context, rawURL string) (*HTTPReachabilityResult, error) {
	u, targetAddr, err := validatePublicTestURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if s.outbound == nil {
		return nil, fmt.Errorf("出站客户端未初始化")
	}
	reqCtx, cancel := context.WithTimeout(ctx, httpReachabilityTimeout)
	defer cancel()
	req, err := newPinnedPublicRequest(reqCtx, u, targetAddr)
	if err != nil {
		return nil, err
	}
	client := safeDiagnosticsClient(s.outbound(), u.Hostname())
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return &HTTPReachabilityResult{OK: false, LatencyMs: elapsed, Error: err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return &HTTPReachabilityResult{OK: true, Status: resp.StatusCode, LatencyMs: elapsed}, nil
}

func validatePublicTestURL(ctx context.Context, rawURL string) (*url.URL, netip.Addr, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, netip.Addr{}, ErrInvalidTestURL
	}
	host := u.Hostname()
	if host == "" {
		return nil, netip.Addr{}, ErrInvalidTestURL
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(addr) {
			return nil, netip.Addr{}, ErrInvalidTestURL
		}
		return u, addr.Unmap(), nil
	}
	addrs, err := lookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, netip.Addr{}, ErrInvalidTestURL
	}
	for _, addr := range addrs {
		if !isPublicAddr(addr) {
			return nil, netip.Addr{}, ErrInvalidTestURL
		}
	}
	return u, addrs[0].Unmap(), nil
}

func newPinnedPublicRequest(ctx context.Context, u *url.URL, addr netip.Addr) (*http.Request, error) {
	pinned := *u
	port := u.Port()
	pinned.Host = addr.String()
	if port != "" {
		pinned.Host = net.JoinHostPort(addr.String(), port)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pinned.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Host = u.Host
	return req, nil
}

func safeDiagnosticsClient(base *http.Client, serverName string) *http.Client {
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	baseTransport, ok := transport.(*http.Transport)
	if !ok {
		return &client
	}
	cloned := baseTransport.Clone()
	if cloned.TLSClientConfig == nil {
		cloned.TLSClientConfig = &tls.Config{ServerName: serverName}
	} else {
		cloned.TLSClientConfig = cloned.TLSClientConfig.Clone()
		cloned.TLSClientConfig.ServerName = serverName
	}
	client.Transport = cloned
	return &client
}

func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedPublicTestPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// PingNode 经 gRPC 调用 Worker GetVersion 主动探活。
// 节点不存在→ErrNodeNotFound；未连接/调用失败→返回 Alive=false（非 error，便于前端按结果显示离线）。
func (s *DiagnosticsService) PingNode(ctx context.Context, nodeID uint) (*NodePingResult, error) {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	client, ok := s.pool.Get(n.UUID)
	if !ok {
		return &NodePingResult{Alive: false, Error: "节点未连接"}, nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, nodePingTimeout)
	defer cancel()
	start := time.Now()
	resp, err := client.Worker.GetVersion(reqCtx, &workerpb.GetVersionRequest{})
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return &NodePingResult{Alive: false, LatencyMs: elapsed, Error: err.Error()}, nil
	}
	return &NodePingResult{Alive: true, LatencyMs: elapsed, Version: resp.Version, Os: resp.Os, Arch: resp.Arch}, nil
}
