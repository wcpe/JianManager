package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 玩家管理相关错误。
var (
	ErrNoReachableBackend = errors.New("没有可达的后端子服")
	ErrInvalidBanScope    = errors.New("不支持的封禁范围")
)

// 探针治理指令 action（与探针侧 BridgePlayerGovernor 约定一致）。
const (
	pluginActionKick            = "kick"
	pluginActionBan             = "ban"
	pluginActionUnban           = "unban"
	pluginActionWhitelistAdd    = "whitelist_add"
	pluginActionWhitelistRemove = "whitelist_remove"
	pluginActionList            = "list"           // 在线玩家列表
	pluginActionWhitelistList   = "whitelist_list" // 白名单列表
)

// pluginExecTimeout 单次治理指令的 gRPC 调用超时（含 Worker 等探针回执的时间）。
// 须大于 Worker 侧 pluginCommandTimeout(5s)，留出网络与排队余量。
const pluginExecTimeout = 8 * time.Second

// PlayerService 玩家管理服务（治理能力迁移到探针）。
//
// 在线玩家、踢/封/解封、白名单均经各后端子服的 ServerProbe 探针实现（退役 RCON）：
// CP 经 Worker 的 SendPluginCommand(wait=true) 下发治理指令，探针执行平台 API 后同步回执。
// BC 跨服感知通过聚合「群组内各后端探针的 list」并标注玩家所在子服实现；封禁记录入库留档
// （model.BanRecord）。实时在线列表另由 PlayerEventService 的事件名册提供。
type PlayerService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewPlayerService 创建玩家管理服务。
func NewPlayerService(db *gorm.DB, pool *cpgrpc.ClientPool) *PlayerService {
	return &PlayerService{db: db, pool: pool}
}

// OnlinePlayer 一个在线玩家及其所在子服（跨服感知）。
type OnlinePlayer struct {
	Name         string `json:"name"`
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
}

// BackendStatus 单个后端子服的 RCON 可用性（用于优雅降级提示）。
type BackendStatus struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	Available    bool   `json:"available"`
	Error        string `json:"error,omitempty"`
}

// OnlinePlayersResult 在线玩家聚合结果。
type OnlinePlayersResult struct {
	Players  []OnlinePlayer  `json:"players"`
	Backends []BackendStatus `json:"backends"`
}

// PlayerActionResult 踢/封/解封在多后端上的执行汇总。
type PlayerActionResult struct {
	Player    string             `json:"player"`
	Action    string             `json:"action"`
	Total     int                `json:"total"`
	Succeeded int                `json:"succeeded"`
	Failed    int                `json:"failed"`
	Results   []PlayerActionItem `json:"results"`
}

// PlayerActionItem 单后端上的动作结果。
type PlayerActionItem struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	OK           bool   `json:"ok"`
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
}

// PlayerActionScope 踢/封/解封的作用范围。
// 三者互斥，按 InstanceID > NetworkID > 全部（scopeIDs）优先级解析目标后端集合。
type PlayerActionScope struct {
	// InstanceID 仅作用于单个后端子服。
	InstanceID uint
	// NetworkID 作用于一个群组内的全部后端子服。
	NetworkID uint
	// Reason 封禁原因（仅 ban 使用）。
	Reason string
}

// OnlinePlayers 聚合 scopeIDs 限定的全部后端子服的在线玩家（BC 跨服感知）。
// scopeIDs 为 nil 表示不收敛（平台管理员，全部后端）；非 nil 时仅统计交集内的实例。
func (s *PlayerService) OnlinePlayers(scopeIDs []uint, scoped bool) (*OnlinePlayersResult, error) {
	backends, err := s.reachableBackends(scopeIDs, scoped)
	if err != nil {
		return nil, err
	}

	result := &OnlinePlayersResult{Players: []OnlinePlayer{}, Backends: []BackendStatus{}}
	for _, b := range backends {
		out, available, execErr := s.execPluginCommand(&b, pluginActionList, "", nil)
		st := BackendStatus{InstanceID: b.ID, InstanceName: b.Name, Available: available}
		if !available {
			st.Error = execErr
			result.Backends = append(result.Backends, st)
			continue
		}
		result.Backends = append(result.Backends, st)
		for _, name := range parsePlayerList(out) {
			result.Players = append(result.Players, OnlinePlayer{
				Name:         name,
				InstanceID:   b.ID,
				InstanceName: b.Name,
			})
		}
	}

	sort.Slice(result.Players, func(i, j int) bool {
		if result.Players[i].Name == result.Players[j].Name {
			return result.Players[i].InstanceName < result.Players[j].InstanceName
		}
		return strings.ToLower(result.Players[i].Name) < strings.ToLower(result.Players[j].Name)
	})
	return result, nil
}

// Kick 踢出玩家：向目标后端集合的探针下发 kick 指令。
func (s *PlayerService) Kick(player string, scope PlayerActionScope, scopeIDs []uint, scoped bool) (*PlayerActionResult, error) {
	player = strings.TrimSpace(player)
	if player == "" {
		return nil, fmt.Errorf("玩家名不能为空")
	}
	targets, err := s.resolveTargets(scope, scopeIDs, scoped)
	if err != nil {
		return nil, err
	}
	return s.fanout(player, pluginActionKick, scope.Reason, targets), nil
}

// Ban 封禁玩家：向目标后端集合的探针下发 ban 指令，并写入封禁记录。
// operatorID 用于审计与解封追溯。
func (s *PlayerService) Ban(player string, scope PlayerActionScope, operatorID uint, scopeIDs []uint, scoped bool) (*PlayerActionResult, error) {
	player = strings.TrimSpace(player)
	if player == "" {
		return nil, fmt.Errorf("玩家名不能为空")
	}
	targets, err := s.resolveTargets(scope, scopeIDs, scoped)
	if err != nil {
		return nil, err
	}
	res := s.fanout(player, pluginActionBan, scope.Reason, targets)

	// 入库留档：即便部分后端 RCON 不可用，只要发起了封禁即记录（权威以服务端 banned-players 为准，
	// 本记录是平台侧台账）。范围按 scope 归类。
	if err := s.recordBan(player, scope, operatorID); err != nil {
		// 记录失败不回滚已下发的封禁命令，仅告警（命令已对在线后端生效）。
		return res, fmt.Errorf("封禁已下发但记录入库失败: %w", err)
	}
	return res, nil
}

// Unban 解封玩家：向目标后端集合的探针下发 unban 指令，并把对应封禁记录置为失效。
//
// 台账更新必须与**命令目标**同口径：命令只发给调用者可见的后端，台账却只按玩家名更新时，
// A 组解封会把 B 组同名玩家的 active 记录一并标成已解除（B 的游戏服实际没收到 unban）。
// 因此：
//   - 指定 InstanceID → 只失效该实例的记录，外加同名字的 global 记录（global 本来就涵盖它）；
//   - 指定 NetworkID  → 只失效该群组记录 + global；
//   - 未指定（当前可达集合）→ 只失效可见实例上生效的 instance 记录 + 可见群组记录 + global。
func (s *PlayerService) Unban(player string, scope PlayerActionScope, scopeIDs []uint, scoped bool) (*PlayerActionResult, error) {
	player = strings.TrimSpace(player)
	if player == "" {
		return nil, fmt.Errorf("玩家名不能为空")
	}
	targets, err := s.resolveTargets(scope, scopeIDs, scoped)
	if err != nil {
		return nil, err
	}
	res := s.fanout(player, pluginActionUnban, "", targets)

	// 解封该玩家在**同一范围**内仍生效的封禁记录（保留历史，置 Active=false + 解封时间）。
	now := time.Now()
	q := s.db.Model(&model.BanRecord{}).Where("player_name = ? AND active = ?", player, true)
	switch {
	case scope.InstanceID > 0:
		q = q.Where("scope = ? OR (scope = ? AND scope_id = ?)", model.BanScopeGlobal, model.BanScopeInstance, scope.InstanceID)
	case scope.NetworkID > 0:
		q = q.Where("scope = ? OR (scope = ? AND scope_id = ?)", model.BanScopeGlobal, model.BanScopeNetwork, scope.NetworkID)
	default:
		// 未指定范围：以权限可见集合为界。全局记录同样失效（本次命令已覆盖全部可达后端）。
		var networkIDs []uint
		if len(scopeIDs) > 0 {
			if err := s.db.Model(&model.NetworkMember{}).
				Where("instance_id IN ?", scopeIDs).
				Distinct().Pluck("network_id", &networkIDs).Error; err != nil {
				return res, fmt.Errorf("解封已下发但查询可见群组失败: %w", err)
			}
		}
		clause := "scope = ? OR (scope = ? AND scope_id IN ?)"
		args := []any{model.BanScopeGlobal, model.BanScopeInstance, scopeIDs}
		if len(networkIDs) > 0 {
			clause += " OR (scope = ? AND scope_id IN ?)"
			args = append(args, model.BanScopeNetwork, networkIDs)
		}
		q = q.Where("("+clause+")", args...)
	}
	if err := q.Updates(map[string]interface{}{"active": false, "unbanned_at": now}).Error; err != nil {
		return res, fmt.Errorf("解封已下发但更新记录失败: %w", err)
	}
	return res, nil
}

// WhitelistResult 白名单查询结果。
type WhitelistResult struct {
	InstanceID uint     `json:"instanceId"`
	Available  bool     `json:"available"`
	Players    []string `json:"players"`
	Error      string   `json:"error,omitempty"`
}

// Whitelist 查询单个后端子服的白名单（探针 whitelist_list）。
func (s *PlayerService) Whitelist(instanceID uint) (*WhitelistResult, error) {
	b, err := s.backendByID(instanceID)
	if err != nil {
		return nil, err
	}
	out, available, execErr := s.execPluginCommand(b, pluginActionWhitelistList, "", nil)
	res := &WhitelistResult{InstanceID: instanceID, Available: available, Players: []string{}}
	if !available {
		res.Error = execErr
		return res, nil
	}
	res.Players = parseWhitelist(out)
	return res, nil
}

// WhitelistAction 对单个后端子服的白名单增/删（探针 whitelist_add|whitelist_remove）。
func (s *PlayerService) WhitelistAction(instanceID uint, action, player string) (*PlayerActionItem, error) {
	var pluginAction string
	switch action {
	case "add":
		pluginAction = pluginActionWhitelistAdd
	case "remove":
		pluginAction = pluginActionWhitelistRemove
	default:
		return nil, fmt.Errorf("不支持的白名单操作: %s", action)
	}
	player = strings.TrimSpace(player)
	if player == "" {
		return nil, fmt.Errorf("玩家名不能为空")
	}
	b, err := s.backendByID(instanceID)
	if err != nil {
		return nil, err
	}
	out, available, execErr := s.execPluginCommand(b, pluginAction, player, nil)
	item := &PlayerActionItem{InstanceID: b.ID, InstanceName: b.Name, OK: available, Output: out}
	if !available {
		item.Error = execErr
	}
	return item, nil
}

// BanFilter 封禁记录查询过滤。
//
// ScopeIDs/Scoped 是「调用者可见实例集合」，用于把台账收敛到调用者有权限的范围：
// global 记录人人可见（它的作用对象本来就是全体可达后端），instance 记录只对可见实例可见，
// network 记录先展开为其成员实例再与可见集合求交。
type BanFilter struct {
	PlayerName *string
	ActiveOnly bool
	Limit      int
	// ScopeIDs 调用者可访问的实例 ID 集合；Scoped=false（平台管理员）时不收敛。
	ScopeIDs []uint
	Scoped   bool
}

// ListBans 查询封禁记录（可查询验收）。
//
// 可见性口径与 Unban 的台账更新保持一致：调用者看不到的实例上的封禁，不得通过列表泄露。
func (s *PlayerService) ListBans(filter BanFilter) ([]model.BanRecord, error) {
	var bans []model.BanRecord
	q := s.db.Model(&model.BanRecord{}).Preload("Operator")
	if filter.PlayerName != nil && *filter.PlayerName != "" {
		q = q.Where("player_name LIKE ?", "%"+*filter.PlayerName+"%")
	}
	if filter.ActiveOnly {
		q = q.Where("active = ?", true)
	}
	if filter.Scoped {
		q = s.scopeBanQuery(q, filter.ScopeIDs)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if err := q.Order("created_at DESC").Limit(limit).Find(&bans).Error; err != nil {
		return nil, fmt.Errorf("查询封禁记录失败: %w", err)
	}
	return bans, nil
}

// scopeBanQuery 把封禁台账收敛到可见实例范围。
//
// 三种 scope 的可见判据：
//   - global：本身不指向具体实例，对任何能看见台账的人可见；
//   - instance：ScopeID 必须在可见实例集合内；
//   - network：群组可见性按「至少包含一个可见实例」判——组内一个实例都看不到时，
//     这条记录对调用者不可观测，也就不应列出。
func (s *PlayerService) scopeBanQuery(q *gorm.DB, scopeIDs []uint) *gorm.DB {
	if len(scopeIDs) == 0 {
		return q.Where("scope = ?", model.BanScopeGlobal)
	}
	var networkIDs []uint
	if err := s.db.Model(&model.NetworkMember{}).
		Where("instance_id IN ?", scopeIDs).
		Distinct().Pluck("network_id", &networkIDs).Error; err != nil {
		// 查不出网络归属时宁可只给 global，不把可能越界的记录放出去。
		return q.Where("scope = ?", model.BanScopeGlobal)
	}
	clause := "(scope = ? OR (scope = ? AND scope_id IN ?)"
	args := []any{model.BanScopeGlobal, model.BanScopeInstance, scopeIDs}
	if len(networkIDs) > 0 {
		clause += " OR (scope = ? AND scope_id IN ?)"
		args = append(args, model.BanScopeNetwork, networkIDs)
	}
	clause += ")"
	return q.Where(clause, args...)
}

// recordBan 按范围写入封禁记录。
func (s *PlayerService) recordBan(player string, scope PlayerActionScope, operatorID uint) error {
	rec := &model.BanRecord{
		PlayerName: player,
		Reason:     strings.TrimSpace(scope.Reason),
		OperatorID: operatorID,
		Active:     true,
	}
	switch {
	case scope.InstanceID > 0:
		rec.Scope = model.BanScopeInstance
		rec.ScopeID = scope.InstanceID
	case scope.NetworkID > 0:
		rec.Scope = model.BanScopeNetwork
		rec.ScopeID = scope.NetworkID
	default:
		rec.Scope = model.BanScopeGlobal
	}
	if err := s.db.Create(rec).Error; err != nil {
		return fmt.Errorf("写入封禁记录失败: %w", err)
	}
	return nil
}

// resolveTargets 按 scope 解析目标后端集合，并与权限可见集合求交。
func (s *PlayerService) resolveTargets(scope PlayerActionScope, scopeIDs []uint, scoped bool) ([]model.Instance, error) {
	switch {
	case scope.InstanceID > 0:
		b, err := s.backendByID(scope.InstanceID)
		if err != nil {
			return nil, err
		}
		if scoped && !containsUint(scopeIDs, b.ID) {
			return nil, ErrNoReachableBackend
		}
		return []model.Instance{*b}, nil
	case scope.NetworkID > 0:
		return s.networkBackends(scope.NetworkID, scopeIDs, scoped)
	default:
		return s.reachableBackends(scopeIDs, scoped)
	}
}

// reachableBackends 返回全部 role=backend 且运行中的实例，按权限集合收敛。
// 仅运行中的实例 RCON 才监听；非运行的直接排除，避免无谓连接超时。
func (s *PlayerService) reachableBackends(scopeIDs []uint, scoped bool) ([]model.Instance, error) {
	q := s.db.Model(&model.Instance{}).
		Where("role = ? AND status = ?", model.InstanceRoleBackend, model.InstanceStatusRunning)
	if scoped {
		if len(scopeIDs) == 0 {
			return []model.Instance{}, nil
		}
		q = q.Where("id IN ?", scopeIDs)
	}
	var insts []model.Instance
	if err := q.Order("name asc").Find(&insts).Error; err != nil {
		return nil, fmt.Errorf("查询后端子服失败: %w", err)
	}
	return insts, nil
}

// networkBackends 返回一个群组内 role=backend 且运行中的实例，按权限集合收敛。
func (s *PlayerService) networkBackends(networkID uint, scopeIDs []uint, scoped bool) ([]model.Instance, error) {
	var memberIDs []uint
	if err := s.db.Model(&model.NetworkMember{}).
		Where("network_id = ?", networkID).
		Pluck("instance_id", &memberIDs).Error; err != nil {
		return nil, fmt.Errorf("查询群组成员失败: %w", err)
	}
	if len(memberIDs) == 0 {
		return []model.Instance{}, nil
	}
	q := s.db.Model(&model.Instance{}).
		Where("role = ? AND status = ? AND id IN ?", model.InstanceRoleBackend, model.InstanceStatusRunning, memberIDs)
	if scoped {
		if len(scopeIDs) == 0 {
			return []model.Instance{}, nil
		}
		q = q.Where("id IN ?", scopeIDs)
	}
	var insts []model.Instance
	if err := q.Order("name asc").Find(&insts).Error; err != nil {
		return nil, fmt.Errorf("查询群组后端失败: %w", err)
	}
	return insts, nil
}

// backendByID 按 ID 取后端实例（不限制状态，供白名单查询等使用）。
func (s *PlayerService) backendByID(instanceID uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	return &inst, nil
}

// fanout 向目标后端集合逐一下发同一治理 action（探针），汇总结果（单点失败不中断）。
func (s *PlayerService) fanout(player, action, reason string, targets []model.Instance) *PlayerActionResult {
	res := &PlayerActionResult{Player: player, Action: action, Total: len(targets), Results: []PlayerActionItem{}}
	for i := range targets {
		t := targets[i]
		out, available, execErr := s.execPluginCommand(&t, action, player, &reason)
		item := PlayerActionItem{InstanceID: t.ID, InstanceName: t.Name, OK: available, Output: out}
		if available {
			res.Succeeded++
		} else {
			item.Error = execErr
			res.Failed++
		}
		res.Results = append(res.Results, item)
	}
	return res
}

// execPluginCommand 经 Worker 的 SendPluginCommand(wait=true) 在指定实例的探针上执行一条治理/查询指令。
// target 为目标玩家名（list/whitelist_list 时为空），reason 仅 kick/ban 用（nil 表示不带）。
// 返回 (输出, 是否可用, 错误信息文本)。任何失败（节点未连/探针未连/超时/执行失败）均归为「不可用」，
// 由调用方优雅降级（取代退役的 RCON 路径）。
func (s *PlayerService) execPluginCommand(inst *model.Instance, action, target string, reason *string) (output string, available bool, errMsg string) {
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		return "", false, "节点不存在"
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return "", false, "节点未连接"
	}

	cmd := &workerpb.PluginCommand{
		Action:    action,
		Target:    target,
		RequestId: uuid.NewString(),
	}
	if reason != nil {
		cmd.Reason = strings.TrimSpace(*reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pluginExecTimeout)
	defer cancel()

	resp, err := client.Worker.SendPluginCommand(ctx, &workerpb.SendPluginCommandRequest{
		InstanceUuid: inst.UUID,
		Command:      cmd,
		Wait:         true,
	})
	if err != nil {
		return "", false, "探针指令调用失败"
	}
	if !resp.Success {
		return "", false, fallbackMsg(resp.Error)
	}
	return resp.Output, true, ""
}

// fallbackMsg 兜底错误文案。
func fallbackMsg(s string) string {
	if strings.TrimSpace(s) == "" {
		return "探针不可用"
	}
	return s
}

// containsUint 判断切片是否含某 ID。
func containsUint(ids []uint, id uint) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// parsePlayerList 从 vanilla `list` 输出解析在线玩家名。
// 典型格式："There are 2 of a max of 20 players online: alice, bob"。
// 兼容无玩家、含颜色代码、玩家名带前缀（如 "alice (uuid)"）的常见变体。
func parsePlayerList(raw string) []string {
	cleaned := cleanColors(raw)
	idx := strings.LastIndex(cleaned, ":")
	if idx < 0 {
		return []string{}
	}
	listPart := strings.TrimSpace(cleaned[idx+1:])
	if listPart == "" {
		return []string{}
	}
	players := []string{}
	for _, p := range strings.Split(listPart, ",") {
		name := strings.TrimSpace(p)
		// 去掉可能附带的 "(uuid)" 等括注，仅取首段为玩家名。
		if sp := strings.IndexByte(name, ' '); sp > 0 {
			name = name[:sp]
		}
		if name != "" {
			players = append(players, name)
		}
	}
	return players
}

// parseWhitelist 从 `whitelist list` 输出解析白名单玩家名。
// 典型格式："There are 2 whitelisted players: alice, bob"。
func parseWhitelist(raw string) []string {
	cleaned := cleanColors(raw)
	idx := strings.LastIndex(cleaned, ":")
	if idx < 0 {
		return []string{}
	}
	listPart := strings.TrimSpace(cleaned[idx+1:])
	if listPart == "" {
		return []string{}
	}
	players := []string{}
	for _, p := range strings.Split(listPart, ",") {
		name := strings.TrimSpace(p)
		if name != "" {
			players = append(players, name)
		}
	}
	return players
}

// cleanColors 去除 Minecraft 颜色代码（§x）。
// 复制自 metrics 包的同名私有逻辑，避免为单一字符串处理跨包暴露内部函数。
func cleanColors(s string) string {
	if !strings.ContainsRune(s, '§') {
		return s
	}
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '§' && i+1 < len(runes) {
			i++ // 跳过颜色码字符
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}
