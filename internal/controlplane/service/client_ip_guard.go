package service

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrInvalidIPRule IP 规则非法（CIDR/IP 解析失败或 mode 非法）。
	ErrInvalidIPRule = errors.New("非法的 IP 规则")
	// ErrIPRuleNotFound IP 规则不存在。
	ErrIPRuleNotFound = errors.New("IP 规则不存在")
)

// ClientIPGuardService 客户端分发端点 IP 防护（FR-096，见 ADR-023）。
//
// 内存缓存规则快照（CRUD 时重载），`Allowed` 无 DB 访问（热路径）；deny 始终优先，存在 allow 规则即白名单模式。
// 防护拦截用内存原子计数器（可观测、**不按请求写库**，避免攻击下自我放大）。机器码不可信，限流/封禁以 IP 为主。
type ClientIPGuardService struct {
	db *gorm.DB

	mu        sync.RWMutex
	denyNets  []*net.IPNet
	allowNets []*net.IPNet
	hasAllow  bool

	denyHits        atomic.Int64
	rateHits        atomic.Int64
	concurrencyHits atomic.Int64
}

// NewClientIPGuardService 创建 IP 防护服务并载入规则快照。
func NewClientIPGuardService(db *gorm.DB) *ClientIPGuardService {
	s := &ClientIPGuardService{db: db}
	_ = s.reload()
	return s
}

// Allowed 报告 IP 是否放行（热路径、读缓存）。deny 优先；有 allow 规则则仅 allow 命中放行。
// IP 不可解析时放行（避免误伤；不可解析的远端在 gin 下罕见）。
func (s *ClientIPGuardService) Allowed(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.denyNets {
		if n.Contains(ip) {
			return false
		}
	}
	if s.hasAllow {
		for _, n := range s.allowNets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return true
}

// MarkDeny / MarkRate / MarkConcurrency 累加防护拦截计数（中间件命中时调用）。
func (s *ClientIPGuardService) MarkDeny()        { s.denyHits.Add(1) }
func (s *ClientIPGuardService) MarkRate()        { s.rateHits.Add(1) }
func (s *ClientIPGuardService) MarkConcurrency() { s.concurrencyHits.Add(1) }

// GuardStats 防护拦截计数快照（可观测）。
type GuardStats struct {
	DenyBlocked        int64 `json:"denyBlocked"`
	RateLimited        int64 `json:"rateLimited"`
	ConcurrencyLimited int64 `json:"concurrencyLimited"`
}

// Stats 返回防护拦截计数。
func (s *ClientIPGuardService) Stats() GuardStats {
	return GuardStats{
		DenyBlocked:        s.denyHits.Load(),
		RateLimited:        s.rateHits.Load(),
		ConcurrencyLimited: s.concurrencyHits.Load(),
	}
}

// ListRules 列出全部 IP 规则。
func (s *ClientIPGuardService) ListRules() ([]model.ClientIPRule, error) {
	var rules []model.ClientIPRule
	if err := s.db.Order("created_at DESC").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("查询 IP 规则失败: %w", err)
	}
	return rules, nil
}

// AddRule 新增 IP 规则（cidr 单 IP 或网段、mode∈{deny,allow}），落库后重载缓存。
func (s *ClientIPGuardService) AddRule(cidr, mode, note string, createdBy uint) (*model.ClientIPRule, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "deny" && mode != "allow" {
		return nil, fmt.Errorf("%w: mode 须为 deny 或 allow", ErrInvalidIPRule)
	}
	if _, err := parseRuleCIDR(cidr); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidIPRule, err)
	}
	rule := &model.ClientIPRule{CIDR: strings.TrimSpace(cidr), Mode: mode, Note: note, CreatedBy: createdBy}
	if err := s.db.Create(rule).Error; err != nil {
		return nil, fmt.Errorf("创建 IP 规则失败: %w", err)
	}
	_ = s.reload()
	return rule, nil
}

// RemoveRule 删除 IP 规则并重载缓存。
func (s *ClientIPGuardService) RemoveRule(id uint) error {
	res := s.db.Delete(&model.ClientIPRule{}, id)
	if res.Error != nil {
		return fmt.Errorf("删除 IP 规则失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrIPRuleNotFound
	}
	_ = s.reload()
	return nil
}

// reload 从 DB 重建规则缓存快照。
func (s *ClientIPGuardService) reload() error {
	var rules []model.ClientIPRule
	if err := s.db.Find(&rules).Error; err != nil {
		return fmt.Errorf("加载 IP 规则失败: %w", err)
	}
	var deny, allow []*net.IPNet
	for _, r := range rules {
		n, err := parseRuleCIDR(r.CIDR)
		if err != nil {
			continue // 跳过损坏规则（不致命）。
		}
		if r.Mode == "allow" {
			allow = append(allow, n)
		} else {
			deny = append(deny, n)
		}
	}
	s.mu.Lock()
	s.denyNets = deny
	s.allowNets = allow
	s.hasAllow = len(allow) > 0
	s.mu.Unlock()
	return nil
}

// parseRuleCIDR 解析单 IP（视作 /32、/128）或 CIDR 网段。
func parseRuleCIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("空 CIDR")
	}
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("非法 IP: %s", s)
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)}, nil
}
