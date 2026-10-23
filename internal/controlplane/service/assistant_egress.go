package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 出站准入的私网放行段（FR-514 spec §4.3 / §6.2 缺口 8）。
//
// 这是唯一一处「管理员可以在页面上扩大出站可达范围」的入口，因此三件事必须同时成立，
// 缺一不可：
//  1. 只有平台管理员能写（路由层 `requireAssistantAdmin`）；
//  2. 写入必留审计（`assistant.settings.egress`）；
//  3. metadata 段**永远拒绝**，即便被显式放行也不生效——见 protected.go 的实现，
//     那里把云元数据地址单列且不受 AllowPrivate 影响。这条不因本入口而改变。

// AssistantEgressView 出站准入设置的对外视图。
type AssistantEgressView struct {
	// AllowPrivateCIDRs 已放行的私网网段（规范化后的字符串，如 "10.0.0.0/8"）。
	AllowPrivateCIDRs []string `json:"allowPrivateCidrs"`
	Version           int      `json:"version"`
	// InsecureAllowed 由装配层决定（dev 环境才允许 http 明文），只读回显，
	// 让管理员知道「我方当前是否接受 http:// 目标」，避免他改了 CIDR 却发现 http 仍不通。
	InsecureAllowed bool `json:"insecureAllowed"`
}

// GetEgress 读取出站准入设置。
func (s *AssistantService) GetEgress() (*AssistantEgressView, error) {
	var row model.AssistantSetting
	if err := s.db.First(&row, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 尚未初始化时不报错：空放行段本身就是合法状态。
			return &AssistantEgressView{AllowPrivateCIDRs: []string{}, Version: 0}, nil
		}
		return nil, fmt.Errorf("查询助手设置失败: %w", err)
	}
	cidrs, err := parseCIDRList(row.AllowPrivateCIDRs)
	if err != nil {
		// 库里存着非法值（例如有人手改了库）：如实回报而不是假装空。
		return nil, fmt.Errorf("已保存的放行段无法解析: %w", err)
	}
	return &AssistantEgressView{
		AllowPrivateCIDRs: cidrs,
		Version:           row.Version,
		InsecureAllowed:   s.insecureAllowed(),
	}, nil
}

// SetEgressAllowPrivate 写入放行段并**立即重建出站工厂**（带 version 乐观并发）。
//
// 重建是必需的：AllowPrivate 在 NewGuard 时被编译进拨号器，不重建就等于「保存了但没生效」，
// 而管理员会以为已经放行——这类「保存成功却行为不变」是最难自查的故障形态。
func (s *AssistantService) SetEgressAllowPrivate(cidrs []string, version int) error {
	normalized, err := normalizeCIDRList(cidrs)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化放行段失败: %w", err)
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var row model.AssistantSetting
		if ferr := tx.First(&row, 1).Error; ferr != nil {
			if errors.Is(ferr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: 助手设置尚未初始化", ErrAssistantInvalid)
			}
			return fmt.Errorf("查询助手设置失败: %w", ferr)
		}
		if row.Version != version {
			return ErrAssistantConflict
		}
		// Where 带 version 做 CAS：两个管理员同时保存时只有一个赢家。
		res := tx.Model(&model.AssistantSetting{}).
			Where("id = ? AND version = ?", 1, version).
			Updates(map[string]any{
				"allow_private_cidrs": string(payload),
				"version":             version + 1,
			})
		if res.Error != nil {
			return fmt.Errorf("保存放行段失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrAssistantConflict
		}
		return nil
	})
	if err != nil {
		return err
	}

	prefixes, err := toPrefixes(normalized)
	if err != nil {
		return err
	}
	s.reloadEgress(prefixes)
	return nil
}

// ApplyStoredEgress 启动时把库里的放行段装配进 Guard。
//
// 装配层在 SetEgress/SetEgressFactory 之后调用；解析失败只返回错误，不静默清空——
// 静默清空会让「昨天还能连的内网网关今天全断了」，且现场没有任何线索。
func (s *AssistantService) ApplyStoredEgress() error {
	view, err := s.GetEgress()
	if err != nil {
		return err
	}
	prefixes, err := toPrefixes(view.AllowPrivateCIDRs)
	if err != nil {
		return err
	}
	s.reloadEgress(prefixes)
	return nil
}

// reloadEgress 用放行段重建出站工厂。
func (s *AssistantService) reloadEgress(prefixes []netip.Prefix) {
	if s.egressFactory == nil {
		return
	}
	s.egress = s.egressFactory(prefixes)
}

// insecureAllowed 回显装配层是否放开了 http 明文（读 Guard 的实际行为无法反查，
// 故由 service 侧记录；当前仅用于视图展示）。
func (s *AssistantService) insecureAllowed() bool { return s.allowInsecure }

// SetAllowInsecure 由装配层告知「http 明文是否被放行」，供视图回显。
func (s *AssistantService) SetAllowInsecure(v bool) { s.allowInsecure = v }

// ── 放行段解析 ─────────────────────────────────────────────────────────

// normalizeCIDRList 校验并规范化放行段。
//
// 拒绝而不是跳过非法项：静默丢弃会让「我明明填了 10.0.0.0/8」变成最难查的一类问题
// （保存成功、行为不变、无任何提示）。错误信息必须指出**是哪一项**。
func normalizeCIDRList(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("%w: 不是合法的 CIDR「%s」（示例：10.0.0.0/8 或 192.168.1.0/24）",
				ErrAssistantInvalid, item)
		}
		// 规范化：掩掉主机位，避免 10.0.0.1/8 与 10.0.0.0/8 被当成两段。
		prefix = prefix.Masked()
		key := prefix.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out, nil
}

// parseCIDRList 解析库里的 JSON 数组文本。
func parseCIDRList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	if items == nil {
		return []string{}, nil
	}
	return items, nil
}

// toPrefixes 把字符串 CIDR 转成 netip.Prefix（供 Guard 使用）。
func toPrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("放行段「%s」无法解析: %w", c, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
