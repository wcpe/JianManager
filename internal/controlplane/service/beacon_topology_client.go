package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Beacon 拉取侧常量（FR-444，见 ADR-090）。
const (
	// BeaconAPIPathZoneTree 区服结构树只读聚合端点（BC 集群 → 大区 → 小区）。
	BeaconAPIPathZoneTree = "/admin/v2/zone-tree"
	// BeaconAPIPathServers server 富化视图列表端点（含 zoneId / isDefaultEntry / kind）。
	BeaconAPIPathServers = "/admin/v2/servers"

	// beaconServersPageSize 是拉取 server 列表时请求的分页大小（Beacon 上限 200）。
	beaconServersPageSize = 200
	// beaconMaxPages 是分页遍历的硬上限，防止对端 total 异常导致死循环。
	beaconMaxPages = 500
	// beaconDefaultTimeout 是单次 Beacon 请求的默认超时。
	beaconDefaultTimeout = 15 * time.Second
)

// BeaconClientConfig 是 Beacon 拉取客户端配置（FR-444）。
type BeaconClientConfig struct {
	// Endpoint Beacon 基址（如 http://beacon.internal:8090），可带/不带尾斜杠。
	// 留空表示未配置协同——由调用方整体跳过，不视为错误。
	Endpoint string
	// Token 管理面凭据；非空时以 Authorization: Bearer 发送（X-Beacon-Api-Key 为其等价形态）。
	Token string
	// PullEnabled 是否允许拉取。false 时 PullTopology 返回 ErrBeaconPullDisabled。
	PullEnabled bool
	// Timeout 单次请求超时；<=0 时用 beaconDefaultTimeout。
	Timeout time.Duration
	// HTTPClient 可选自定义客户端（注入进程级出站代理用）；nil 时用带超时的默认客户端。
	HTTPClient *http.Client
}

// 拉取侧哨兵错误。Beacon 不可达/未配置/未启用时返回明确错误（不静默成功，FR-444 §4.4）。
var (
	// ErrBeaconNotConfigured 未配置 beacon.endpoint——可选协同未启用。
	ErrBeaconNotConfigured = fmt.Errorf("未配置 Beacon 协同端点（beacon.endpoint 为空）")
	// ErrBeaconPullDisabled 已配置端点但 beacon.pull-enabled=false。
	ErrBeaconPullDisabled = fmt.Errorf("Beacon 拓扑拉取未启用（beacon.pull-enabled=false）")
	// ErrBeaconUnreachable Beacon 不可达（网络错误/非 2xx/响应不可解析）。
	ErrBeaconUnreachable = fmt.Errorf("Beacon 不可达")
)

// BeaconZoneTreeZone 是 Beacon zone-tree 的小区节点（BC 集群 → 大区 → 小区）。
type BeaconZoneTreeZone struct {
	ID                uint   `json:"id"`
	Name              string `json:"name"`
	Code              string `json:"code"`
	DisplayName       string `json:"displayName"`
	Description       string `json:"description"`
	ServerCount       int    `json:"serverCount"`
	DefaultEntryCount int    `json:"defaultEntryCount"`
}

// BeaconZoneTreeRegion 是 Beacon zone-tree 的大区节点。
type BeaconZoneTreeRegion struct {
	ID          uint                 `json:"id"`
	Name        string               `json:"name"`
	Code        string               `json:"code"`
	DisplayName string               `json:"displayName"`
	Description string               `json:"description"`
	Zones       []BeaconZoneTreeZone `json:"zones"`
}

// BeaconZoneTreeCluster 是 Beacon zone-tree 的 BC 集群节点。
type BeaconZoneTreeCluster struct {
	ID          uint                   `json:"id"`
	Name        string                 `json:"name"`
	Code        string                 `json:"code"`
	DisplayName string                 `json:"displayName"`
	Description string                 `json:"description"`
	ProxyCount  int                    `json:"proxyCount"`
	Regions     []BeaconZoneTreeRegion `json:"regions"`
}

// BeaconZoneTree 是 GET /admin/v2/zone-tree 的响应。
type BeaconZoneTree struct {
	NamespaceID     uint                    `json:"namespaceId"`
	Clusters        []BeaconZoneTreeCluster `json:"clusters"`
	UnassignedCount int                     `json:"unassignedCount"`
}

// BeaconServerView 是 Beacon server 资产的富化视图（GET /admin/v2/servers 的 items 元素）。
// 只声明拉取侧用到的字段：归属（bcClusterId/zoneId）、默认入口（isDefaultEntry）、
// 生命周期（lifecycleStatus/effectiveActive/tombstone）与在线态。
type BeaconServerView struct {
	ID              uint    `json:"id"`
	NamespaceID     uint    `json:"namespaceId"`
	ServerID        string  `json:"serverId"`
	DisplayName     string  `json:"displayName"`
	Kind            string  `json:"kind"`
	BCClusterID     *uint   `json:"bcClusterId"`
	BCClusterName   *string `json:"bcClusterName"`
	ZoneID          *uint   `json:"zoneId"`
	ZoneName        *string `json:"zoneName"`
	RegionName      *string `json:"regionName"`
	PendingZoneID   *uint   `json:"pendingZoneId"`
	IsDefaultEntry  bool    `json:"isDefaultEntry"`
	Draining        bool    `json:"draining"`
	Lifecycle       string  `json:"lifecycle"`
	LifecycleStatus string  `json:"lifecycleStatus"`
	// EffectiveActive 标识 server 处于生效态；墓碑/归档记录不应参与建树。
	EffectiveActive bool `json:"effectiveActive"`
	// Tombstone 非 nil 表示已永久删除；此类记录不参与建树。
	Tombstone *struct {
		At     string `json:"at"`
		Reason string `json:"reason"`
	} `json:"tombstone,omitempty"`
	Online   bool `json:"online"`
	Assigned bool `json:"assigned"`
}

// BeaconTopology 是一次全量拉取的不可变快照（FR-444 §4.4：先全量拉取到内存）。
// 拉取阶段不触碰本地库，映射写入阶段才以事务落地。
type BeaconTopology struct {
	// NamespaceID 本次拉取的 namespace（0 表示全部）。
	NamespaceID uint
	// Clusters BC 集群树（含大区与小区）。
	Clusters []BeaconZoneTreeCluster
	// Servers server 富化视图（含归属与默认入口）。
	Servers []BeaconServerView
}

// BeaconClient 是 Beacon 管理面只读客户端（FR-444）。仅实现拉取所需的两个 GET 端点。
type BeaconClient struct {
	cfg    BeaconClientConfig
	client *http.Client
	// provider 是运行时出站持有者（FR-185/ADR-043）：非 nil 时每次请求取当前 client，
	// 使平台设置里改出站代理后 Beacon 调用即时生效，无需重启。
	provider func() *http.Client
}

// NewBeaconClient 构造 Beacon 拉取客户端。endpoint 为空返回 nil（调用方据此整体跳过协同，
// 不视为错误——可选协同，绝非依赖）。
func NewBeaconClient(cfg BeaconClientConfig) *BeaconClient {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = beaconDefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	return &BeaconClient{cfg: cfg, client: client}
}

// SetHTTPClientProvider 注入「取当前出站 *http.Client」的提供者（FR-185 Provider.Client）。
// 注入后优先于构造期客户端，使运行时代理变更对后续拉取立即生效。
func (c *BeaconClient) SetHTTPClientProvider(fn func() *http.Client) {
	if c == nil {
		return
	}
	c.provider = fn
}

// httpClient 返回本次请求使用的客户端。
func (c *BeaconClient) httpClient() *http.Client {
	if c.provider != nil {
		if cur := c.provider(); cur != nil {
			return cur
		}
	}
	return c.client
}

// Endpoint 返回归一化（去尾斜杠）后的 Beacon 基址。
func (c *BeaconClient) Endpoint() string { return c.cfg.Endpoint }

// Enabled 报告是否已配置协同端点。
func (c *BeaconClient) Enabled() bool { return c != nil && c.cfg.Endpoint != "" }

// PullTopology 一次性拉取「区服结构树 + server 归属」到内存，不触碰本地任何数据。
// namespaceID 为 0 表示不限 namespace（Beacon 侧全量）。
// Beacon 不可达、鉴权失败或响应不可解析时返回包装了 ErrBeaconUnreachable 的错误。
func (c *BeaconClient) PullTopology(ctx context.Context, namespaceID uint) (*BeaconTopology, error) {
	if c == nil || c.cfg.Endpoint == "" {
		return nil, ErrBeaconNotConfigured
	}
	if !c.cfg.PullEnabled {
		return nil, ErrBeaconPullDisabled
	}

	tree, err := c.fetchZoneTree(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	servers, err := c.fetchAllServers(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	// 树与 server 列表均已在内存：此后校验/映射不再发起任何外部请求，
	// 保证「拉取失败 = 本地零改动」（FR-444 §4.4）。
	if tree == nil {
		tree = &BeaconZoneTree{NamespaceID: namespaceID}
	}
	return &BeaconTopology{NamespaceID: namespaceID, Clusters: tree.Clusters, Servers: servers}, nil
}

// fetchZoneTree 拉取区服结构树。
func (c *BeaconClient) fetchZoneTree(ctx context.Context, namespaceID uint) (*BeaconZoneTree, error) {
	q := url.Values{}
	if namespaceID != 0 {
		q.Set("namespaceId", strconv.FormatUint(uint64(namespaceID), 10))
	}
	var out BeaconZoneTree
	if err := c.getJSON(ctx, BeaconAPIPathZoneTree, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// fetchAllServers 分页遍历 server 列表直至取满 total（上限 beaconMaxPages）。
func (c *BeaconClient) fetchAllServers(ctx context.Context, namespaceID uint) ([]BeaconServerView, error) {
	all := make([]BeaconServerView, 0, beaconServersPageSize)
	for page := 1; page <= beaconMaxPages; page++ {
		q := url.Values{}
		if namespaceID != 0 {
			q.Set("namespaceId", strconv.FormatUint(uint64(namespaceID), 10))
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(beaconServersPageSize))

		var resp struct {
			Items []BeaconServerView `json:"items"`
			Total int64              `json:"total"`
		}
		if err := c.getJSON(ctx, BeaconAPIPathServers, q, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		// 空页或已取满 total 即收敛；total 缺失（0）但页内有数据时靠空页退出。
		if len(resp.Items) == 0 || (resp.Total > 0 && int64(len(all)) >= resp.Total) {
			break
		}
		if len(resp.Items) < beaconServersPageSize {
			break
		}
	}
	return all, nil
}

// getJSON 发起带鉴权的 GET 并解析 JSON。
// 非 2xx、网络错误与解析失败统一包装为 ErrBeaconUnreachable（附原错误供排障）。
func (c *BeaconClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	endpoint := c.cfg.Endpoint + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: 构造请求失败: %v", ErrBeaconUnreachable, err)
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(c.cfg.Token); token != "" {
		// Beacon 管理面接受 Authorization: Bearer <凭据>（API 密钥或登录令牌两种形态）。
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: 请求 %s 失败: %v", ErrBeaconUnreachable, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("%w: 读取 %s 响应失败: %v", ErrBeaconUnreachable, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %s 返回 HTTP %d: %s", ErrBeaconUnreachable, path, resp.StatusCode, truncateBeaconBody(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: 解析 %s 响应失败: %v", ErrBeaconUnreachable, path, err)
	}
	return nil
}

// truncateBeaconBody 截断错误响应正文，避免把大段 HTML 错误页塞进审计与日志。
func truncateBeaconBody(body []byte) string {
	const max = 256
	s := strings.TrimSpace(string(body))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
