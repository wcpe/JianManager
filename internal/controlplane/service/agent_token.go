package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Agent 写白名单与动作常量（FR-384 / FR-395，见 ADR-076/080）。
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

	// FR-396：节点与实例全生命周期扩展 action。
	AgentActionNodeGet                    = "agent.node_get"
	AgentActionNodeGetMetrics             = "agent.node_get_metrics"
	AgentActionNodeCheckDocker            = "agent.node_check_docker"
	AgentActionNodeDrain                  = "agent.node_drain"
	AgentActionNodeListArchived           = "agent.node_list_archived"
	AgentActionNodePurgeArchived          = "agent.node_purge_archived"
	AgentActionInstanceSearch             = "agent.instance_search"
	AgentActionInstanceGetEnv             = "agent.instance_get_env"
	AgentActionInstanceListCrashSnapshots = "agent.instance_list_crash_snapshots"
	AgentActionInstanceCreate             = "agent.instance_create"
	AgentActionInstanceProvisionServer    = "agent.instance_provision_server"
	AgentActionInstanceImportInspect      = "agent.instance_import_inspect"
	AgentActionInstanceImport             = "agent.instance_import"
	AgentActionInstanceClone              = "agent.instance_clone"
	AgentActionInstanceRebuild            = "agent.instance_rebuild"
	AgentActionInstanceUpdateConfig       = "agent.instance_update_config"
	AgentActionTaskGet                    = "agent.task_get"
	AgentActionInstanceSendCommand        = "agent.instance_send_command"
	AgentActionInstanceBatch              = "agent.instance_batch"
	AgentActionInstanceKill               = "agent.instance_kill"
	AgentActionInstanceDelete             = "agent.instance_delete"

	// FR-397 文件域 action（MCP 专属，不进 HTTP 契约投影）。
	AgentActionFileList                = "agent.file_list"
	AgentActionFileCheckAccess         = "agent.file_check_access"
	AgentActionFileReadText            = "agent.file_read_text"
	AgentActionFileWriteText           = "agent.file_write_text"
	AgentActionFileRename              = "agent.file_rename"
	AgentActionFileChmod               = "agent.file_chmod"
	AgentActionFileDelete              = "agent.file_delete"
	AgentActionFileVersions            = "agent.file_versions"
	AgentActionFileDiff                = "agent.file_diff"
	AgentActionFileRollback            = "agent.file_rollback"
	AgentActionFileIssueTransferTicket = "agent.file_issue_transfer_ticket"

	// AgentActionFileTransferConsume 仅作票据消费的调用流水标签，
	// 刻意不进 action 目录：它不可被授权、不可被发现，凭据是票据本身。
	AgentActionFileTransferConsume = "agent.file_transfer_consume"

	// FR-397 配置域 action。
	AgentActionConfigDiscover    = "agent.config_discover"
	AgentActionConfigRead        = "agent.config_read"
	AgentActionConfigWriteText   = "agent.config_write_text"
	AgentActionConfigWriteFields = "agent.config_write_fields"
	AgentActionConfigCrossCheck  = "agent.config_cross_check"
	AgentActionConfigVersions    = "agent.config_versions"
	AgentActionConfigDiff        = "agent.config_diff"
	AgentActionConfigRollback    = "agent.config_rollback"

	// FR-397 插件域 action。
	AgentActionPluginList            = "agent.plugin_list"
	AgentActionPluginDeployFromAsset = "agent.plugin_deploy_from_asset"
	AgentActionPluginToggle          = "agent.plugin_toggle"
	AgentActionPluginDelete          = "agent.plugin_delete"

	// FR-398：普通 Bot 舰队 action（resource=bot）。
	AgentActionBotList        = "agent.bot_list"
	AgentActionBotGet         = "agent.bot_get"
	AgentActionBotCreate      = "agent.bot_create"
	AgentActionBotSetBehavior = "agent.bot_set_behavior"
	AgentActionBotSendCommand = "agent.bot_send_command"
	AgentActionBotDelete      = "agent.bot_delete"

	// FR-398：压测模板 action（resource=none，平台级视角，见 ADR-080 附注）。
	AgentActionLoadTestTemplateList   = "agent.loadtest_template_list"
	AgentActionLoadTestTemplateGet    = "agent.loadtest_template_get"
	AgentActionLoadTestTemplateCreate = "agent.loadtest_template_create"
	AgentActionLoadTestTemplateUpdate = "agent.loadtest_template_update"
	AgentActionLoadTestTemplateDelete = "agent.loadtest_template_delete"

	// FR-398：压测运行编排与观测 action（resource=botrun）。
	AgentActionLoadTestRunCreate      = "agent.loadtest_run_create"
	AgentActionLoadTestRunList        = "agent.loadtest_run_list"
	AgentActionLoadTestRunGet         = "agent.loadtest_run_get"
	AgentActionLoadTestNodeCapacity   = "agent.loadtest_node_capacity"
	AgentActionLoadTestRunPreflight   = "agent.loadtest_run_preflight"
	AgentActionLoadTestRunStart       = "agent.loadtest_run_start"
	AgentActionLoadTestRunStop        = "agent.loadtest_run_stop"
	AgentActionLoadTestRunRetryFailed = "agent.loadtest_run_retry_failed"
	AgentActionLoadTestRunBots        = "agent.loadtest_run_bots"
	AgentActionLoadTestRunFailures    = "agent.loadtest_run_failures"
	AgentActionLoadTestRunEvents      = "agent.loadtest_run_events"
	AgentActionLoadTestRunMetrics     = "agent.loadtest_run_metrics"
	AgentActionLoadTestRunReport      = "agent.loadtest_run_report"

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
// FR-395 起生产授权以 action 目录为准；本集合继续供 FR-388 契约与兼容测试枚举。
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

// AgentOpsAction 运维面可暴露的 action 契约行（FR-388 / FR-395）。
type AgentOpsAction struct {
	// Action 策略引擎动作名。
	Action string
	// Method HTTP 方法。
	Method string
	// PathTemplate 路径模板（:id 占位）。
	PathTemplate string
	// Kind read|write。
	Kind string
	// WriteAllow 写动作所需 V1 白名单项；读为空。
	WriteAllow string
	// Capability 写动作/读动作所需 V2 能力；whoami 为空。
	Capability string
	// Scope instance|node|none。
	Scope string
	// HTTPDeny 策略拒绝时期望的 HTTP 状态（运维面统一 403）。
	HTTPDeny int
}

// AgentOpsContract 返回 CLI/MCP/curl 共享的运维 action 契约表。
// 由 action 目录投影，仅包含带 HTTP 投影的条目。
func AgentOpsContract() []AgentOpsAction {
	items := make([]AgentOpsAction, 0, len(agentActionCatalog))
	for _, d := range agentActionCatalog {
		if !d.HTTPInContract {
			continue
		}
		kind := d.Operation
		if kind == AgentOperationDestructive {
			kind = AgentOperationWrite
		}
		items = append(items, AgentOpsAction{
			Action:       d.Action,
			Method:       d.HTTPMethod,
			PathTemplate: d.HTTPPath,
			Kind:         kind,
			WriteAllow:   d.V1WriteAllow,
			Capability:   d.V2Capability,
			Scope:        d.ResourceType,
			HTTPDeny:     403,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].PathTemplate == items[j].PathTemplate {
			return items[i].Method < items[j].Method
		}
		return items[i].PathTemplate < items[j].PathTemplate
	})
	return items
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
	TokenID uint
	Name    string
	// TokenPrefix 明文前缀（如 jmat_ab12），供 MCP 会话列表展示；不泄露全文。
	TokenPrefix       string
	PolicyVersion     int
	ScopedInstanceIDs []uint
	ScopedNodeIDs     []uint
	WriteAllowlist    []string
	Capabilities      []string
}

// AgentTokenService Agent Token 管理与策略引擎（FR-384 / FR-395，见 ADR-076/080）。
type AgentTokenService struct {
	db *gorm.DB
}

// NewAgentTokenService 创建服务。
func NewAgentTokenService(db *gorm.DB) *AgentTokenService {
	return &AgentTokenService{db: db}
}

// IssueAgentTokenRequest 签发请求。
// WriteAllowlistProvided / CapabilitiesProvided 用于区分「字段缺省」与「显式空数组」。
type IssueAgentTokenRequest struct {
	Name                   string
	ScopedInstanceIDs      []uint
	ScopedNodeIDs          []uint
	WriteAllowlist         []string
	WriteAllowlistProvided bool
	Capabilities           []string
	CapabilitiesProvided   bool
	// PolicyVersion 1=V1，2=V2；0 视为 V1。
	PolicyVersion int
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

	policyVersion := req.PolicyVersion
	if policyVersion == 0 {
		policyVersion = AgentPolicyVersionV1
	}
	if policyVersion != AgentPolicyVersionV1 && policyVersion != AgentPolicyVersionV2 {
		return nil, "", fmt.Errorf("policyVersion 仅支持 1 或 2")
	}

	var allow []string
	var caps []string
	switch policyVersion {
	case AgentPolicyVersionV1:
		if req.CapabilitiesProvided {
			return nil, "", fmt.Errorf("V1 Token 不得提交 capabilities")
		}
		if req.WriteAllowlistProvided {
			allow = req.WriteAllowlist
			if allow == nil {
				allow = []string{}
			}
		} else {
			// 默认写白名单：仅生命周期 + 维护态
			allow = []string{AgentWriteInstanceLife, AgentWriteNodeMaintenance}
		}
		caps = []string{}
	case AgentPolicyVersionV2:
		if req.WriteAllowlistProvided {
			return nil, "", fmt.Errorf("V2 Token 不得提交 writeAllowlist")
		}
		if !req.CapabilitiesProvided {
			return nil, "", fmt.Errorf("V2 Token 必须显式提交 capabilities（可为空数组）")
		}
		if err := ValidateAgentCapabilities(req.Capabilities); err != nil {
			return nil, "", err
		}
		caps = append([]string(nil), req.Capabilities...)
		allow = []string{}
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
	capsJSON, err := json.Marshal(caps)
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
		PolicyVersion:     policyVersion,
		Capabilities:      string(capsJSON),
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

// PrincipalByID 按 Token ID 实时重建主体（FR-397 票据消费重验用）：
// 吊销或过期一律返回 ErrAgentTokenInvalid，无需票据撤销列表。
func (s *AgentTokenService) PrincipalByID(tokenID uint) (*AgentPrincipal, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("agent token service 未初始化")
	}
	var tok model.AgentToken
	err := s.db.First(&tok, tokenID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAgentTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("查询 agent token 失败: %w", err)
	}
	if tok.Revoked || !time.Now().Before(tok.ExpiresAt) {
		return nil, ErrAgentTokenInvalid
	}
	return principalFromToken(&tok)
}

// ResolveAction 兼容包装：将显式 instanceID/nodeID 视为可信目标。
// 生产 HTTP/MCP 应优先走 Authorize + CP 可信目标解析；本函数保留 V1 测试与旧调用方。
func ResolveAction(p *AgentPrincipal, action string, instanceID, nodeID uint) error {
	if p == nil {
		return ErrAgentForbidden
	}
	if IsAgentHardDeny(action) {
		return ErrAgentForbidden
	}
	d, ok := DescribeAgentAction(action)
	if !ok {
		return ErrAgentForbidden
	}
	// 无具体目标的发现类 action（whoami/list）走 CanDiscover。
	if d.Action == AgentActionWhoami || d.Action == AgentActionListInstances || d.Action == AgentActionListNodes {
		_, err := CanDiscover(p, action)
		return err
	}
	target := AgentTrustedTarget{ResourceType: d.ResourceType}
	switch d.ResourceType {
	case AgentResourceNone:
		// 无目标
	case AgentResourceNode:
		target.NodeID = nodeID
	case AgentResourceInstance:
		// 兼容旧调用：仅传 instanceID 时，V1 只看显式实例 scope；
		// V2 若未提供 nodeID，仅在显式实例 scope 命中时放行，不猜测归属。
		target.InstanceID = instanceID
		target.NodeID = nodeID
	}
	_, err := Authorize(p, action, target)
	return err
}

func principalFromToken(tok *model.AgentToken) (*AgentPrincipal, error) {
	var instIDs, nodeIDs []uint
	var allow []string
	var caps []string
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
	policyVersion := tok.PolicyVersion
	if policyVersion == 0 {
		policyVersion = AgentPolicyVersionV1
	}
	if policyVersion == AgentPolicyVersionV2 {
		if tok.Capabilities != "" {
			if err := json.Unmarshal([]byte(tok.Capabilities), &caps); err != nil {
				return nil, fmt.Errorf("解析 capabilities 失败: %w", err)
			}
		}
		if caps == nil {
			caps = []string{}
		}
	} else {
		caps = []string{}
	}
	return &AgentPrincipal{
		TokenID:           tok.ID,
		Name:              tok.Name,
		TokenPrefix:       tok.TokenPrefix,
		PolicyVersion:     policyVersion,
		ScopedInstanceIDs: instIDs,
		ScopedNodeIDs:     nodeIDs,
		WriteAllowlist:    allow,
		Capabilities:      caps,
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
