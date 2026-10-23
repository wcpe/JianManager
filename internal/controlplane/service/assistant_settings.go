package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 助手全局设置（FR-514）：默认模型 + 出站准入放行段。
//
// 两者同属 `assistant_settings` 单行，因此也共用**一个** GET/PUT 端点，形态与
// `/api/v1/settings` 同构（单例资源整体读写）。
//
// 为什么不是 settings/default-model 与 settings/egress 两个动作端点：同一个「设置」
// 概念，全仓既有做法是单例资源整体 GET/PUT；拆成两个动作端点会让同一张表出现两种写法，
// 且前端要维护两套乐观并发。代价是「设默认模型」与「改放行段」的校验耦合在一次请求里
// ——但两者本就共享同一个 version，分开写反而让并发语义更难讲清。

// AssistantSettingsView 助手全局设置的对外视图。
type AssistantSettingsView struct {
	// DefaultModelID 可空：null 表示未设置默认模型。
	DefaultModelID *uint `json:"defaultModelId"`
	// AllowPrivateCIDRs 已放行的私网网段（规范化后的字符串，如 "10.0.0.0/8"）。
	AllowPrivateCIDRs []string `json:"allowPrivateCidrs"`
	Version           int      `json:"version"`
	// InsecureAllowed **只读回显**：当前部署是否接受 http:// 明文目标（仅 dev 部署为真）。
	// 放进视图是为了避免「改了 CIDR 却发现 http 仍不通」这类难以自查的困惑。
	InsecureAllowed bool `json:"insecureAllowed"`
}

// SaveSettingsParams 整体替换参数。
//
// **整体替换语义**（与 PUT /api/v1/settings 一致）：未提供的字段等于置空。
// 因此 DefaultModelID 不传即「清空默认模型」，AllowPrivateCIDRs 传空即
// 「不放行任何私网段」。好处是「请求体即最终状态」，不必去区分
// 「字段省略」与「显式 null」——那种区分在 JSON 里本就难以可靠表达。
type SaveSettingsParams struct {
	DefaultModelID    *uint
	AllowPrivateCIDRs []string
	// Version 为 0 表示跳过并发校验（供初始化与测试使用），非 0 必须与当前值一致。
	Version int
}

// GetSettings 读取助手全局设置。
func (s *AssistantService) GetSettings() (*AssistantSettingsView, error) {
	var row model.AssistantSetting
	if err := s.db.First(&row, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 尚未初始化不算错误：空放行段与无默认模型本身都是合法状态。
			return &AssistantSettingsView{AllowPrivateCIDRs: []string{}, InsecureAllowed: s.insecureAllowed()}, nil
		}
		return nil, fmt.Errorf("查询助手设置失败: %w", err)
	}
	cidrs, err := parseCIDRList(row.AllowPrivateCIDRs)
	if err != nil {
		// 库里存着非法值（例如有人手改了库）：如实回报而不是假装空——
		// 假装空会让「今天内网网关全断了」失去唯一的线索。
		return nil, fmt.Errorf("已保存的放行段无法解析: %w", err)
	}
	return &AssistantSettingsView{
		DefaultModelID:    row.DefaultModelID,
		AllowPrivateCIDRs: cidrs,
		Version:           row.Version,
		InsecureAllowed:   s.insecureAllowed(),
	}, nil
}

// SaveSettings 整体替换助手全局设置（带 version 乐观并发）。
//
// 校验分两类，都在**同一事务内**完成，避免「校验时可用、写入时已被停用」的窗口：
//   - 默认模型：必须存在，且自身与所属供应商都已启用；
//   - 放行段：逐项必须是合法 CIDR，非法项**拒绝并点名是哪一项**（静默丢弃会让
//     「我明明填了 10.0.0.0/8」变成保存成功、行为不变、无提示的难查故障）。
//
// 放行段变化后立即重建出站工厂：`AllowPrivate` 在 `NewGuard` 时被编译进拨号器，
// 不重建就是「保存了但没生效」，而管理员的预期是「点了保存就生效」。
func (s *AssistantService) SaveSettings(p SaveSettingsParams) error {
	normalized, err := normalizeCIDRList(p.AllowPrivateCIDRs)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化放行段失败: %w", err)
	}

	var before []string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var row model.AssistantSetting
		if ferr := tx.First(&row, 1).Error; ferr != nil {
			if errors.Is(ferr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: 助手设置尚未初始化", ErrAssistantInvalid)
			}
			return fmt.Errorf("查询助手设置失败: %w", ferr)
		}
		if p.Version != 0 && p.Version != row.Version {
			return ErrAssistantConflict
		}
		// 供事务提交后判断「放行段是否真的变了」，避免每次都重建 Guard。
		if prev, perr := parseCIDRList(row.AllowPrivateCIDRs); perr == nil {
			before = prev
		}

		if err := validateDefaultModel(tx, p.DefaultModelID); err != nil {
			return err
		}

		// Where 带 version 做 CAS：两个管理员同时保存时只有一个赢家。
		res := tx.Model(&model.AssistantSetting{}).
			Where("id = ? AND version = ?", 1, row.Version).
			Updates(map[string]any{
				"default_model_id":    p.DefaultModelID,
				"allow_private_cidrs": string(payload),
				"version":             row.Version + 1,
			})
		if res.Error != nil {
			return fmt.Errorf("保存助手设置失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrAssistantConflict
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 只在放行段真的变化时重建：默认模型变更与出站准入无关，
	// 无谓重建会丢掉既有的空闲连接。
	if !slices.Equal(before, normalized) {
		s.reloadEgressFromStrings(normalized)
	}
	return nil
}

// validateDefaultModel 校验默认模型可用性（nil 合法，表示清空）。
func validateDefaultModel(tx *gorm.DB, modelID *uint) error {
	if modelID == nil {
		return nil
	}
	var m model.AssistantModel
	if err := tx.First(&m, *modelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: 模型不存在", ErrAssistantInvalid)
		}
		return fmt.Errorf("查询模型失败: %w", err)
	}
	if !m.Enabled {
		return fmt.Errorf("%w: 不能把已停用的模型设为默认", ErrAssistantInvalid)
	}
	var p model.AssistantProvider
	if err := tx.First(&p, m.ProviderID).Error; err != nil {
		return fmt.Errorf("查询供应商失败: %w", err)
	}
	if !p.Enabled {
		return fmt.Errorf("%w: 该模型的供应商已停用", ErrAssistantInvalid)
	}
	return nil
}

// ApplyStoredEgress 启动时把库里的放行段装配进 Guard。
//
// 装配层在 SetEgress/SetEgressFactory 之后调用；解析失败只返回错误，不静默清空——
// 静默清空会让「昨天还能连的内网网关今天全断了」，且现场没有任何线索。
func (s *AssistantService) ApplyStoredEgress() error {
	view, err := s.GetSettings()
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

// reloadEgressFromStrings 规范化后的字符串 → Guard 重建。
func (s *AssistantService) reloadEgressFromStrings(cidrs []string) {
	prefixes, err := toPrefixes(cidrs)
	if err != nil {
		// 走到这里说明 normalize 与 parse 不一致（代码缺陷），
		// 保留旧 Guard 而不是清空——避免把缺陷放大成「内网全断」。
		return
	}
	s.reloadEgress(prefixes)
}

// reloadEgress 用放行段重建出站工厂。
func (s *AssistantService) reloadEgress(prefixes []netip.Prefix) {
	if s.egressFactory == nil {
		return
	}
	s.egress = s.egressFactory(prefixes)
}

// insecureAllowed 回显装配层是否放开了 http 明文。
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
