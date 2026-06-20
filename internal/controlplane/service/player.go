package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// 玩家管理相关错误（FR-054）。
var (
	ErrNoReachableBackend = errors.New("没有可达的后端子服")
	ErrInvalidBanScope    = errors.New("不支持的封禁范围")
)

// rconExecTimeout 单次 RCON 命令的 gRPC 调用超时。
// RCON 文本协议同步往返，5s 足够；放大会拖慢跨多后端的聚合。
const rconExecTimeout = 5 * time.Second

// PlayerService 玩家管理服务（FR-054）。
//
// 在线玩家、踢/封/解封、白名单均经各后端子服的 RCON 实现：CP 持有 RCON 凭据，
// 经 Worker 的 ExecRconCommand 下发命令。BC 跨服感知通过聚合「群组内各后端的 list」
// 并标注玩家所在子服实现；封禁记录入库留档（model.BanRecord）。
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
	Player    string                `json:"player"`
	Action    string                `json:"action"`
	Total     int                   `json:"total"`
	Succeeded int                   `json:"succeeded"`
	Failed    int                   `json:"failed"`
	Results   []PlayerActionItem    `json:"results"`
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
		out, available, execErr := s.execRcon(&b, "list")
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

// Kick 踢出玩家：向目标后端集合下发 RCON `kick <name> [reason]`。
func (s *PlayerService) Kick(player string, scope PlayerActionScope, scopeIDs []uint, scoped bool) (*PlayerActionResult, error) {
	player = strings.TrimSpace(player)
	if player == "" {
		return nil, fmt.Errorf("玩家名不能为空")
	}
	targets, err := s.resolveTargets(scope, scopeIDs, scoped)
	if err != nil {
		return nil, err
	}
	cmd := "kick " + player
	if r := strings.TrimSpace(scope.Reason); r != "" {
		cmd += " " + r
	}
	return s.fanout(player, "kick", cmd, targets), nil
}

// Ban 封禁玩家：向目标后端集合下发 RCON `ban <name> [reason]`，并写入封禁记录。
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
	cmd := "ban " + player
	if r := strings.TrimSpace(scope.Reason); r != "" {
		cmd += " " + r
	}
	res := s.fanout(player, "ban", cmd, targets)

	// 入库留档：即便部分后端 RCON 不可用，只要发起了封禁即记录（权威以服务端 banned-players 为准，
	// 本记录是平台侧台账）。范围按 scope 归类。
	if err := s.recordBan(player, scope, operatorID); err != nil {
		// 记录失败不回滚已下发的封禁命令，仅告警（命令已对在线后端生效）。
		return res, fmt.Errorf("封禁已下发但记录入库失败: %w", err)
	}
	return res, nil
}

// Unban 解封玩家：向目标后端集合下发 RCON `pardon <name>`，并把对应封禁记录置为失效。
func (s *PlayerService) Unban(player string, scope PlayerActionScope, scopeIDs []uint, scoped bool) (*PlayerActionResult, error) {
	player = strings.TrimSpace(player)
	if player == "" {
		return nil, fmt.Errorf("玩家名不能为空")
	}
	targets, err := s.resolveTargets(scope, scopeIDs, scoped)
	if err != nil {
		return nil, err
	}
	res := s.fanout(player, "unban", "pardon "+player, targets)

	// 解封该玩家在本平台仍生效的封禁记录（保留历史，置 Active=false + 解封时间）。
	now := time.Now()
	if err := s.db.Model(&model.BanRecord{}).
		Where("player_name = ? AND active = ?", player, true).
		Updates(map[string]interface{}{"active": false, "unbanned_at": now}).Error; err != nil {
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

// Whitelist 查询单个后端子服的白名单（RCON `whitelist list`）。
func (s *PlayerService) Whitelist(instanceID uint) (*WhitelistResult, error) {
	b, err := s.backendByID(instanceID)
	if err != nil {
		return nil, err
	}
	out, available, execErr := s.execRcon(b, "whitelist list")
	res := &WhitelistResult{InstanceID: instanceID, Available: available, Players: []string{}}
	if !available {
		res.Error = execErr
		return res, nil
	}
	res.Players = parseWhitelist(out)
	return res, nil
}

// WhitelistAction 对单个后端子服的白名单增/删（RCON `whitelist add|remove <name>`）。
func (s *PlayerService) WhitelistAction(instanceID uint, action, player string) (*PlayerActionItem, error) {
	if action != "add" && action != "remove" {
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
	out, available, execErr := s.execRcon(b, "whitelist "+action+" "+player)
	item := &PlayerActionItem{InstanceID: b.ID, InstanceName: b.Name, OK: available, Output: out}
	if !available {
		item.Error = execErr
	}
	return item, nil
}

// BanFilter 封禁记录查询过滤。
type BanFilter struct {
	PlayerName *string
	ActiveOnly bool
	Limit      int
}

// ListBans 查询封禁记录（可查询 FR-054 验收）。
func (s *PlayerService) ListBans(filter BanFilter) ([]model.BanRecord, error) {
	var bans []model.BanRecord
	q := s.db.Model(&model.BanRecord{}).Preload("Operator")
	if filter.PlayerName != nil && *filter.PlayerName != "" {
		q = q.Where("player_name LIKE ?", "%"+*filter.PlayerName+"%")
	}
	if filter.ActiveOnly {
		q = q.Where("active = ?", true)
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

// fanout 向目标后端集合逐一下发同一条 RCON 命令，汇总结果（单点失败不中断）。
func (s *PlayerService) fanout(player, action, command string, targets []model.Instance) *PlayerActionResult {
	res := &PlayerActionResult{Player: player, Action: action, Total: len(targets), Results: []PlayerActionItem{}}
	for i := range targets {
		t := targets[i]
		out, available, execErr := s.execRcon(&t, command)
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

// execRcon 经 Worker 的 ExecRconCommand 在指定实例上执行一条 RCON 命令。
// 返回 (输出, 是否可用, 错误信息文本)。任何失败均归为「不可用」，由调用方优雅降级。
func (s *PlayerService) execRcon(inst *model.Instance, command string) (output string, available bool, errMsg string) {
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		return "", false, "节点不存在"
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return "", false, "节点未连接"
	}

	ctx, cancel := context.WithTimeout(context.Background(), rconExecTimeout)
	defer cancel()

	resp, err := client.Worker.ExecRconCommand(ctx, &workerpb.ExecRconCommandRequest{
		InstanceUuid: inst.UUID,
		Command:      command,
		RconPort:     int32(inst.RCONPort),
		RconPassword: inst.RCONPassword,
	})
	if err != nil {
		return "", false, "RCON 调用失败"
	}
	if !resp.Available {
		return "", false, fallbackMsg(resp.Error)
	}
	return resp.Output, true, ""
}

// fallbackMsg 兜底错误文案。
func fallbackMsg(s string) string {
	if strings.TrimSpace(s) == "" {
		return "RCON 不可用"
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
