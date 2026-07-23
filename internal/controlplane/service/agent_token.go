package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Agent 写白名单与动作常量（FR-384 / ADR-076）。
const (
	AgentWriteInstanceLife    = "instance.life"
	AgentWriteNodeMaintenance = "node.maintenance"

	AgentActionWhoami               = "agent.whoami"
	AgentActionListNodes            = "agent.list_nodes"
	AgentActionListInstances        = "agent.list_instances"
	AgentActionGetInstance          = "agent.get_instance"
	AgentActionGetInstanceMetrics   = "agent.get_instance_metrics"
	AgentActionGetInstanceLogs      = "agent.get_instance_logs"
	AgentActionInstanceStart        = "agent.instance_start"
	AgentActionInstanceStop         = "agent.instance_stop"
	AgentActionInstanceRestart      = "agent.instance_restart"
	AgentActionNodeMaintenanceEnter = "agent.node_maintenance_enter"
	AgentActionNodeMaintenanceLeave = "agent.node_maintenance_leave"

	// 硬拒绝示例 action（永不对 agent 开放；ResolveAction 显式 deny）
	AgentHardDenyUserWrite      = "user.write"
	AgentHardDenyGroupWrite     = "group.write"
	AgentHardDenyPlatformSet    = "platform.settings"
	AgentHardDenyDBBrowse       = "db.browse"
	AgentHardDenySelfUpdate     = "selfupdate"
	AgentHardDenyInstanceDelete = "instance.delete"
	AgentHardDenyNodeDelete     = "node.delete"
	AgentHardDenyInstanceKill   = "instance.kill"
	AgentHardDenyAuditDelete    = "audit.delete"
)

var (
	// ErrAgentTokenNotFound 令牌不存在。
	ErrAgentTokenNotFound = errors.New("agent token 不存在")
	// ErrAgentTokenInvalid 令牌无效（不存在/过期/已吊销）。
	ErrAgentTokenInvalid = errors.New("agent token 无效")
	// ErrAgentForbidden 策略拒绝（scope / 写白名单 / 硬拒绝）。
	ErrAgentForbidden = errors.New("agent 操作被拒绝")
)

const (
	agentTokenPlaintextPrefix = "jmat_"
	agentTokenPrefixLen       = 9
	defaultAgentTTLDays       = 90
	maxAgentTTLDays           = 365
)

// 硬拒绝 action 集合（永不 allow）。
var agentHardDeny = map[string]struct{}{
	AgentHardDenyUserWrite:      {},
	AgentHardDenyGroupWrite:     {},
	AgentHardDenyPlatformSet:    {},
	AgentHardDenyDBBrowse:       {},
	AgentHardDenySelfUpdate:     {},
	AgentHardDenyInstanceDelete: {},
	AgentHardDenyNodeDelete:     {},
	AgentHardDenyInstanceKill:   {},
	AgentHardDenyAuditDelete:    {},
	// 常见管理面路径别名
	"user.create": {}, "user.update": {}, "user.delete": {},
	"group.create": {}, "group.update": {}, "group.delete": {},
	"settings.write": {}, "db.query": {}, "system.update": {},
}

// AgentOpsAction 运维面可暴露的 action 契约行（FR-388）。
type AgentOpsAction struct {
	// Action 策略引擎动作名。
	Action string
	// Method HTTP 方法。
	Method string
	// PathTemplate 路径模板（:id 占位）。
	PathTemplate string
	// Kind read|write。
	Kind string
	// WriteAllow 写动作所需白名单项；读为空。
	WriteAllow string
	// Scope instance|node|none。
	Scope string
	// HTTPDeny 策略拒绝时期望的 HTTP 状态（运维面统一 403）。
	HTTPDeny int
}

// AgentOpsContract 返回 CLI/MCP/curl 共享的运维 action 契约表。
func AgentOpsContract() []AgentOpsAction {
	return []AgentOpsAction{
		{Action: AgentActionWhoami, Method: "GET", PathTemplate: "/api/v1/agent/whoami", Kind: "read", Scope: "none", HTTPDeny: 403},
		{Action: AgentActionListNodes, Method: "GET", PathTemplate: "/api/v1/agent/nodes", Kind: "read", Scope: "node", HTTPDeny: 403},
		{Action: AgentActionListInstances, Method: "GET", PathTemplate: "/api/v1/agent/instances", Kind: "read", Scope: "instance", HTTPDeny: 403},
		{Action: AgentActionGetInstance, Method: "GET", PathTemplate: "/api/v1/agent/instances/:id", Kind: "read", Scope: "instance", HTTPDeny: 403},
		{Action: AgentActionGetInstanceMetrics, Method: "GET", PathTemplate: "/api/v1/agent/instances/:id/metrics", Kind: "read", Scope: "instance", HTTPDeny: 403},
		{Action: AgentActionInstanceStart, Method: "POST", PathTemplate: "/api/v1/agent/instances/:id/start", Kind: "write", WriteAllow: AgentWriteInstanceLife, Scope: "instance", HTTPDeny: 403},
		{Action: AgentActionInstanceStop, Method: "POST", PathTemplate: "/api/v1/agent/instances/:id/stop", Kind: "write", WriteAllow: AgentWriteInstanceLife, Scope: "instance", HTTPDeny: 403},
		{Action: AgentActionInstanceRestart, Method: "POST", PathTemplate: "/api/v1/agent/instances/:id/restart", Kind: "write", WriteAllow: AgentWriteInstanceLife, Scope: "instance", HTTPDeny: 403},
		{Action: AgentActionNodeMaintenanceEnter, Method: "POST", PathTemplate: "/api/v1/agent/nodes/:id/maintenance/enter", Kind: "write", WriteAllow: AgentWriteNodeMaintenance, Scope: "node", HTTPDeny: 403},
		{Action: AgentActionNodeMaintenanceLeave, Method: "POST", PathTemplate: "/api/v1/agent/nodes/:id/maintenance/leave", Kind: "write", WriteAllow: AgentWriteNodeMaintenance, Scope: "node", HTTPDeny: 403},
	}
}

// AgentHardDenyList 返回硬拒绝 action 列表（排序无关，供契约/测试枚举）。
func AgentHardDenyList() []string {
	out := make([]string, 0, len(agentHardDeny))
	for k := range agentHardDeny {
		out = append(out, k)
	}
	return out
}

// IsAgentHardDeny 判断 action 是否在硬拒绝集合。
func IsAgentHardDeny(action string) bool {
	_, ok := agentHardDeny[action]
	return ok
}

// AgentPrincipal 鉴权后注入上下文的 agent 主体。
type AgentPrincipal struct {
	TokenID           uint
	Name              string
	// TokenPrefix 明文前缀（如 jmat_ab12），供 MCP 会话列表展示；不泄露全文。
	TokenPrefix       string
	ScopedInstanceIDs []uint
	ScopedNodeIDs     []uint
	WriteAllowlist    []string
}

// AgentTokenService Agent Token 管理与策略引擎（FR-384，见 ADR-076）。
type AgentTokenService struct {
	db *gorm.DB
}

// NewAgentTokenService 创建服务。
func NewAgentTokenService(db *gorm.DB) *AgentTokenService {
	return &AgentTokenService{db: db}
}

// IssueAgentTokenRequest 签发请求。
type IssueAgentTokenRequest struct {
	Name              string
	ScopedInstanceIDs []uint
	ScopedNodeIDs     []uint
	WriteAllowlist    []string
	// TTLDays 有效天数；<=0 默认 90，上限 365。
	TTLDays   int
	CreatedBy uint
}

// Issue 签发 Agent Token：落库只存 hash，明文一次性返回。
func (s *AgentTokenService) Issue(req IssueAgentTokenRequest) (*model.AgentToken, string, error) {
	name := req.Name
	if name == "" {
		return nil, "", fmt.Errorf("name 不能为空")
	}
	ttlDays := req.TTLDays
	if ttlDays <= 0 {
		ttlDays = defaultAgentTTLDays
	}
	if ttlDays > maxAgentTTLDays {
		ttlDays = maxAgentTTLDays
	}
	// 默认写白名单：仅生命周期 + 维护态
	allow := req.WriteAllowlist
	if allow == nil {
		allow = []string{AgentWriteInstanceLife, AgentWriteNodeMaintenance}
	}
	instJSON, err := json.Marshal(req.ScopedInstanceIDs)
	if err != nil {
		return nil, "", err
	}
	nodeJSON, err := json.Marshal(req.ScopedNodeIDs)
	if err != nil {
		return nil, "", err
	}
	allowJSON, err := json.Marshal(allow)
	if err != nil {
		return nil, "", err
	}

	plaintext, hash, prefix, err := generateAgentToken()
	if err != nil {
		return nil, "", err
	}
	tok := &model.AgentToken{
		Name:              name,
		TokenHash:         hash,
		TokenPrefix:       prefix,
		ScopedInstanceIDs: string(instJSON),
		ScopedNodeIDs:     string(nodeJSON),
		WriteAllowlist:    string(allowJSON),
		ExpiresAt:         time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour),
		CreatedBy:         req.CreatedBy,
	}
	if err := s.db.Create(tok).Error; err != nil {
		return nil, "", fmt.Errorf("签发 agent token 失败: %w", err)
	}
	return tok, plaintext, nil
}

// List 列出全部 agent token 元数据（无明文）。
func (s *AgentTokenService) List() ([]model.AgentToken, error) {
	var tokens []model.AgentToken
	if err := s.db.Order("created_at DESC").Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("查询 agent token 失败: %w", err)
	}
	return tokens, nil
}

// Revoke 吊销 token。
func (s *AgentTokenService) Revoke(id uint) error {
	var tok model.AgentToken
	err := s.db.First(&tok, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrAgentTokenNotFound
	}
	if err != nil {
		return fmt.Errorf("查询 agent token 失败: %w", err)
	}
	if err := s.db.Model(&tok).Update("revoked", true).Error; err != nil {
		return fmt.Errorf("吊销 agent token 失败: %w", err)
	}
	return nil
}

// Authenticate 校验明文 token，返回主体；失败返回 ErrAgentTokenInvalid。
func (s *AgentTokenService) Authenticate(plaintext string) (*AgentPrincipal, error) {
	if plaintext == "" {
		return nil, ErrAgentTokenInvalid
	}
	hash := sha256Hex(plaintext)
	var tok model.AgentToken
	err := s.db.Where("token_hash = ?", hash).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAgentTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("查询 agent token 失败: %w", err)
	}
	if tok.Revoked || !time.Now().Before(tok.ExpiresAt) {
		return nil, ErrAgentTokenInvalid
	}
	p, err := principalFromToken(&tok)
	if err != nil {
		return nil, err
	}
	// 异步更新 last_used 可后续优化；MVP 同步轻量更新
	now := time.Now()
	_ = s.db.Model(&tok).Update("last_used_at", &now).Error
	return p, nil
}

// ResolveAction 策略真源：硬拒绝 / 写白名单 / scope。
// instanceID、nodeID 为 0 表示该动作不绑定资源。
func ResolveAction(p *AgentPrincipal, action string, instanceID, nodeID uint) error {
	if p == nil {
		return ErrAgentForbidden
	}
	if _, hard := agentHardDeny[action]; hard {
		return ErrAgentForbidden
	}
	// 写动作
	switch action {
	case AgentActionInstanceStart, AgentActionInstanceStop, AgentActionInstanceRestart:
		if !containsStr(p.WriteAllowlist, AgentWriteInstanceLife) {
			return ErrAgentForbidden
		}
		if instanceID == 0 || !agentContainsUint(p.ScopedInstanceIDs, instanceID) {
			return ErrAgentForbidden
		}
		return nil
	case AgentActionNodeMaintenanceEnter, AgentActionNodeMaintenanceLeave:
		if !containsStr(p.WriteAllowlist, AgentWriteNodeMaintenance) {
			return ErrAgentForbidden
		}
		if nodeID == 0 || !agentContainsUint(p.ScopedNodeIDs, nodeID) {
			return ErrAgentForbidden
		}
		return nil
	case AgentActionGetInstance, AgentActionGetInstanceMetrics, AgentActionGetInstanceLogs:
		if instanceID == 0 || !agentContainsUint(p.ScopedInstanceIDs, instanceID) {
			return ErrAgentForbidden
		}
		return nil
	case AgentActionListInstances:
		// 列表：至少有一个实例 scope 才允许（避免空 token 扫全站）
		if len(p.ScopedInstanceIDs) == 0 {
			return ErrAgentForbidden
		}
		return nil
	case AgentActionListNodes:
		if len(p.ScopedNodeIDs) == 0 {
			return ErrAgentForbidden
		}
		return nil
	case AgentActionWhoami:
		return nil
	default:
		// 未知动作默认拒绝
		return ErrAgentForbidden
	}
}

func principalFromToken(tok *model.AgentToken) (*AgentPrincipal, error) {
	var instIDs, nodeIDs []uint
	var allow []string
	if tok.ScopedInstanceIDs != "" {
		if err := json.Unmarshal([]byte(tok.ScopedInstanceIDs), &instIDs); err != nil {
			return nil, fmt.Errorf("解析 scoped_instance_ids 失败: %w", err)
		}
	}
	if tok.ScopedNodeIDs != "" {
		if err := json.Unmarshal([]byte(tok.ScopedNodeIDs), &nodeIDs); err != nil {
			return nil, fmt.Errorf("解析 scoped_node_ids 失败: %w", err)
		}
	}
	if tok.WriteAllowlist != "" {
		if err := json.Unmarshal([]byte(tok.WriteAllowlist), &allow); err != nil {
			return nil, fmt.Errorf("解析 write_allowlist 失败: %w", err)
		}
	}
	return &AgentPrincipal{
		TokenID:           tok.ID,
		Name:              tok.Name,
		TokenPrefix:       tok.TokenPrefix,
		ScopedInstanceIDs: instIDs,
		ScopedNodeIDs:     nodeIDs,
		WriteAllowlist:    allow,
	}, nil
}

func generateAgentToken() (plaintext, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("生成 agent token 失败: %w", err)
	}
	plaintext = agentTokenPlaintextPrefix + base64.RawURLEncoding.EncodeToString(b)
	hash = sha256Hex(plaintext)
	prefix = plaintext
	if len(prefix) > agentTokenPrefixLen {
		prefix = prefix[:agentTokenPrefixLen]
	}
	return plaintext, hash, prefix, nil
}

func agentContainsUint(ss []uint, v uint) bool {
	for _, x := range ss {
		if x == v {
			return true
		}
	}
	return false
}

func containsStr(ss []string, v string) bool {
	for _, x := range ss {
		if x == v {
			return true
		}
	}
	return false
}
