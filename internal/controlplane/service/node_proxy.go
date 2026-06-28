package service

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 节点出站代理模式（FR-185，见 ADR-043）。
const (
	// NodeProxyModeInherit 用平台全局默认代理（settings DB > control-plane.yaml > env）。
	NodeProxyModeInherit = "inherit"
	// NodeProxyModeCustom 用本节点自定义代理（ProxyURL/ProxyNoProxy）。
	NodeProxyModeCustom = "custom"
)

// NodeProxyService 管控节点级出站代理（FR-185，见 ADR-043）。
//
// 真相源 = CP DB（nodes.proxy_*）。CP 据「节点 custom ? 节点值 : 全局默认」算每节点期望代理，
// 经心跳响应下发给 Worker，Worker 运行时重建出站 client。全局默认取自 settings（注入闭包），
// 使全局代理改动即时反映到所有 inherit 节点的下发值。
type NodeProxyService struct {
	db *gorm.DB
	// globalDefault 返回平台全局默认出站代理（= settings.EffectiveProxy）。
	// 以闭包注入而非直接依赖 SettingsService，避免服务间构造耦合。
	globalDefault func() httpclient.Config
}

// NewNodeProxyService 创建节点代理服务。globalDefault 返回全局默认代理（inherit 节点用之）。
func NewNodeProxyService(db *gorm.DB, globalDefault func() httpclient.Config) *NodeProxyService {
	if globalDefault == nil {
		globalDefault = func() httpclient.Config { return httpclient.Config{} }
	}
	return &NodeProxyService{db: db, globalDefault: globalDefault}
}

// EffectiveNodeProxy 计算某节点的期望出站代理（FR-185）：
// custom → 节点自定义；inherit（或其它）→ 全局默认。
func (s *NodeProxyService) EffectiveNodeProxy(node *model.Node) httpclient.Config {
	if node != nil && node.ProxyMode == NodeProxyModeCustom {
		return httpclient.Config{URL: node.ProxyURL, NoProxy: node.ProxyNoProxy}
	}
	return s.globalDefault()
}

// NodeProxyGeneration 返回某节点期望代理的 generation（心跳下发比较用，FR-185）。
func (s *NodeProxyService) NodeProxyGeneration(node *model.Node) string {
	return httpclient.ProxyGeneration(s.EffectiveNodeProxy(node))
}

// EffectiveNodeProxyByUUID 按 UUID 算期望代理（url/noProxy）+ generation（供心跳响应下发，FR-185）。
// 满足 grpc.NodeProxyResolver 接口（返回 3 个字符串，使 grpc 包无需 import httpclient）。
// 节点不存在或查询失败时回退全局默认（仍是合理的「期望代理」），不报错——心跳不因此中断。
func (s *NodeProxyService) EffectiveNodeProxyByUUID(uuid string) (url, noProxy, generation string) {
	var node model.Node
	if err := s.db.Where("uuid = ?", uuid).First(&node).Error; err != nil {
		cfg := s.globalDefault()
		return cfg.URL, cfg.NoProxy, httpclient.ProxyGeneration(cfg)
	}
	cfg := s.EffectiveNodeProxy(&node)
	return cfg.URL, cfg.NoProxy, httpclient.ProxyGeneration(cfg)
}

// UpdateNodeProxy 更新节点代理模式/自定义值（FR-185）。
//   - mode=custom：url 必须为合法代理地址（复用 httpclient 校验），落库 url+no_proxy；
//   - mode=inherit：清空 custom 字段（避免脏数据下发），改用全局默认。
// 非法 mode / custom 空或非法 URL → ErrSettingValueInvalid（不落库）。
func (s *NodeProxyService) UpdateNodeProxy(nodeID uint, mode, url, noProxy string) (*model.Node, error) {
	mode = strings.TrimSpace(mode)
	url = strings.TrimSpace(url)

	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}

	updates := map[string]any{}
	switch mode {
	case NodeProxyModeInherit:
		// 回退全局默认：清空 custom 残留，避免「切回 inherit 却仍下发旧 custom」。
		updates["proxy_mode"] = NodeProxyModeInherit
		updates["proxy_url"] = ""
		updates["proxy_no_proxy"] = ""
	case NodeProxyModeCustom:
		if url == "" {
			return nil, fmt.Errorf("%w: 自定义代理须填写地址（否则请选继承全局）", ErrSettingValueInvalid)
		}
		// 复用 httpclient 的 URL/scheme 校验，非法即拒、不静默直连（FR-185/ADR-043）。
		if _, err := httpclient.New(httpclient.Config{URL: url}); err != nil {
			return nil, fmt.Errorf("%w: 代理地址非法（%v）", ErrSettingValueInvalid, err)
		}
		updates["proxy_mode"] = NodeProxyModeCustom
		updates["proxy_url"] = url
		updates["proxy_no_proxy"] = strings.TrimSpace(noProxy)
	default:
		return nil, fmt.Errorf("%w: 代理模式须为 inherit|custom", ErrSettingValueInvalid)
	}

	if err := s.db.Model(&node).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新节点代理失败: %w", err)
	}
	// 回填内存对象，便于调用方拿到更新后视图。
	node.ProxyMode, _ = updates["proxy_mode"].(string)
	node.ProxyURL, _ = updates["proxy_url"].(string)
	node.ProxyNoProxy, _ = updates["proxy_no_proxy"].(string)
	return &node, nil
}

// NodeProxyVw 节点出站代理对外视图（脱敏，FR-185）。
type NodeProxyVw struct {
	// Mode inherit|custom。
	Mode string `json:"mode"`
	// URL 节点自定义代理地址（脱敏；仅 custom 有意义）。
	URL string `json:"url"`
	// NoProxy 节点自定义免代理列表（仅 custom 有意义）。
	NoProxy string `json:"noProxy"`
	// EffectiveURL 当前生效代理地址（脱敏）：custom→节点值，inherit→全局默认。
	EffectiveURL string `json:"effectiveUrl"`
	// EffectiveNoProxy 当前生效免代理列表。
	EffectiveNoProxy string `json:"effectiveNoProxy"`
	// GlobalDefaultURL 平台全局默认代理地址（脱敏，供前端展示「继承自全局」）。
	GlobalDefaultURL string `json:"globalDefaultUrl"`
	// Online 节点是否在线（离线时前端标注「待下发」，下次心跳生效）。
	Online bool `json:"online"`
}

// NodeProxyView 返回节点代理对外视图（脱敏含凭据的 URL）。
func (s *NodeProxyService) NodeProxyView(nodeID uint) (*NodeProxyVw, error) {
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	eff := s.EffectiveNodeProxy(&node)
	global := s.globalDefault()
	mode := node.ProxyMode
	if mode == "" {
		mode = NodeProxyModeInherit
	}
	return &NodeProxyVw{
		Mode:             mode,
		URL:              httpclient.Sanitize(node.ProxyURL),
		NoProxy:          node.ProxyNoProxy,
		EffectiveURL:     httpclient.Sanitize(eff.URL),
		EffectiveNoProxy: eff.NoProxy,
		GlobalDefaultURL: httpclient.Sanitize(global.URL),
		Online:           node.Status == model.NodeStatusOnline,
	}, nil
}
