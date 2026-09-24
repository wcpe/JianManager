package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-460：失效 Bot 自动回收与容量自动补足。
const (
	// defaultBotReclaimGracePeriod 默认宽限期：Bot 生命周期短，容忍一次世代迁移窗口即可（短于 FR-326 的 10m）。
	defaultBotReclaimGracePeriod = 2 * time.Minute
	// defaultBotReclaimAuto 默认自动回收：仅对 Fleet 归属 Bot 生效；V1 手动 Bot 永不自动回收。
	defaultBotReclaimAuto   = true
	botReclaimSweepInterval = 60 * time.Second
	// botReclaimFreshnessWindow 复用 FR-365 的 botFreshnessMissingWindow 口径（90s）。
	botReclaimFreshnessWindow = botFreshnessMissingWindow
	// botReclaimConnectingFirstSeenWindow N1：从未上报（last_seen_at 为空）的 connecting Bot 的
	// 判定上限（比通用新鲜度窗口宽松 10×），既容忍慢节点首次登录，又避免永久豁免 empty_epoch。
	botReclaimConnectingFirstSeenWindow = 10 * botReclaimFreshnessWindow

	// botReclaimRefillMinGapRatio 补足缺口比例阈值（spec §5）：缺口低于目标的比例时不补足，
	// 抑制连接建立中的瞬时抖动（connecting）触发过度补发。
	botReclaimRefillMinGapRatio = 0.02
	// botReclaimRefillSmallGapStreak N5：小缺口（低于比例阈值）连续 N 拍仍未收敛时仍补足。
	// 否则大 target（≥51）下「单台缺失」永久落在阈值内，容量就永久缺一格。
	botReclaimRefillSmallGapStreak = 2
	// defaultBotReclaimSnapshotTimeout 单次 Fleet 实存快照 RPC 超时（N3：防止节点假死拖住整拍巡检）。
	defaultBotReclaimSnapshotTimeout = 10 * time.Second
	// botReclaimRefillMaxAttempts 单会话连续补足尝试上限；达到后转入长冷却，避免 60s 无条件重发。
	botReclaimRefillMaxAttempts = 6
	// botReclaimRefillBaseBackoff / botReclaimRefillMaxBackoff 补足指数退避起点与上限。
	botReclaimRefillBaseBackoff = 60 * time.Second
	botReclaimRefillMaxBackoff  = 10 * time.Minute
	// botReclaimRearmCooldown 被回收 / 重新武装 Bot 的冷却窗，消除 stop+create 振荡。
	botReclaimRearmCooldown = 5 * time.Minute
	// botReclaimListLimitMax List 单次返回条数上限（nit：避免无界返回）。
	botReclaimListLimitMax = 500
	// botReclaimDetectLimit 单拍判定候选上限（nit：避免无界扫描）。
	botReclaimDetectLimit = 2000

	// botZombieSessionIdleThreshold 僵尸会话停滞阈值（FR-472，spec §2.4.1）：
	// 会话在线 Bot 数为 0 且持续无进展超过该时长即判为僵尸并收敛为 stopped。
	// 取 Bot 宽限期（2m）的 5 倍，容忍 Worker 短暂重启与世代迁移窗口，不误杀正常会话。
	botZombieSessionIdleThreshold = 10 * time.Minute
	// botReclaimSessionReapedAction 僵尸会话收敛的审计动作（FR-472）。
	botReclaimSessionReapedAction = "bot_reclaim.session_reaped"
)

// 失效判据命中说明（§2.1 条件 3）。
const (
	// BotReclaimStaleEpochMismatch worker_epoch_generation 落后于执行节点最近上报世代。
	BotReclaimStaleEpochMismatch = "epoch_mismatch"
	// BotReclaimStaleEmptyEpoch worker_epoch 为空（分片重启前残留，从未被任何世代认领）。
	BotReclaimStaleEmptyEpoch = "empty_epoch"
	// BotReclaimStaleNodeMissing executor_node_id 为空或对应节点已不存在。
	BotReclaimStaleNodeMissing = "node_missing"
)

var (
	// ErrBotReclaimNotFound 失效 Bot 回收记录不存在。
	ErrBotReclaimNotFound = errors.New("失效 Bot 回收记录不存在")
	// ErrBotReclaimNotActive 记录已终态（disposed/cancelled），不可再处置。
	ErrBotReclaimNotActive = errors.New("失效 Bot 回收记录已终态，不可处置")
	// ErrBotReclaimNotFleetOwned 非 Fleet 归属 Bot 不支持自动/手动回收（V1 手动 Bot 属用户资源）。
	ErrBotReclaimNotFleetOwned = errors.New("非 Fleet 归属 Bot 不支持回收，请使用 Bot 删除接口")
	// ErrBotReclaimWorkerOffline 执行节点未连接，无法下发停用。
	ErrBotReclaimWorkerOffline = errors.New("节点未连接，无法回收失效 Bot")
	// ErrBotReclaimDisposeFailed Worker 返回失败或 gRPC 错误。
	ErrBotReclaimDisposeFailed = errors.New("回收失效 Bot 失败")
)

// BotReclaimNodeEpoch 是判定失效所需的执行节点世代视图（真源取自容量快照）。
type BotReclaimNodeEpoch struct {
	NodeID                uint
	NodeUUID              string
	Exists                bool
	Online                bool
	WorkerEpoch           string
	WorkerEpochGeneration int64
}

// BotReclaimFleetSnapshotSource 提供执行节点 Worker 的 Fleet 实存快照。
//
// FR-460 §2.4.3 要求补足与回收都先与 Worker 快照对账；F1 亦以「Bot 是否仍在 Worker 实存中」
// 作为分片世代分叉场景下判定健康与否的真源。生产实现复用既有隧道连接池，不新增协议。
type BotReclaimFleetSnapshotSource interface {
	GetBotFleetSnapshot(ctx context.Context, nodeUUID, sessionUUID string) (*workerpb.GetBotFleetSnapshotResponse, error)
}

// botReclaimNodePresence 是某执行节点 Worker 当前实存的 Bot 集合与容量世代。
// Known=false 表示本轮未能取到快照（节点不可达 / RPC 失败），调用方须走保守兜底。
type botReclaimNodePresence struct {
	Known              bool
	Present            map[string]struct{}
	CapacityGeneration int64
}

func (p botReclaimNodePresence) contains(botUUID string) bool {
	if p.Present == nil {
		return false
	}
	_, ok := p.Present[botUUID]
	return ok
}

// BotReclaimCapacitySource 提供执行节点当前世代；生产实现包装 BotLoadCapacityDirectory。
type BotReclaimCapacitySource interface {
	NodeEpochs(ctx context.Context) (map[uint]BotReclaimNodeEpoch, error)
}

// directoryBotReclaimCapacity 包装容量目录的快照，读取节点 WorkerEpochGeneration。
type directoryBotReclaimCapacity struct {
	directory *BotLoadCapacityDirectory
}

// NewDirectoryBotReclaimCapacity 使用既有容量目录装配世代来源（不新增 RPC）。
func NewDirectoryBotReclaimCapacity(directory *BotLoadCapacityDirectory) BotReclaimCapacitySource {
	return directoryBotReclaimCapacity{directory: directory}
}

func (d directoryBotReclaimCapacity) NodeEpochs(ctx context.Context) (map[uint]BotReclaimNodeEpoch, error) {
	if d.directory == nil {
		return nil, fmt.Errorf("Bot 负载容量目录未装配")
	}
	snapshot, err := d.directory.Snapshot(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]BotReclaimNodeEpoch, len(snapshot.NodeCapacities))
	for _, capacity := range snapshot.NodeCapacities {
		out[capacity.NodeID] = BotReclaimNodeEpoch{
			NodeID: capacity.NodeID, NodeUUID: capacity.NodeUUID, Exists: true, Online: capacity.Online,
			WorkerEpoch: capacity.WorkerEpoch, WorkerEpochGeneration: capacity.WorkerEpochGeneration,
		}
	}
	return out, nil
}

// BotReclaimStopper 抽象「向执行节点下发单个 Fleet Bot 停用」；生产实现走 Fleet RPC。
//
// 生产实现使用 ApplyBotBatch(desired=stopped)——这是 CP→Worker 唯一的 Fleet 停用路径
// （Worker 的 StopBotBatch 由 applyBotBatch 内的 dispatchBotStops 触发；DeleteBot 对
// Fleet Bot 会因 legacy fleet mutation 被拒）。skipped=true 表示 Worker 报告该 Bot 已不存在/已停。
type BotReclaimStopper interface {
	StopFleetBot(ctx context.Context, nodeUUID string, assignment *workerpb.BotAssignment) (skipped bool, err error)
}

// poolBotReclaimStopper 经连接池用 ApplyBotBatch 下发 stopped assignment。
type poolBotReclaimStopper struct {
	pool *cpgrpc.ClientPool
}

func (s poolBotReclaimStopper) StopFleetBot(ctx context.Context, nodeUUID string, assignment *workerpb.BotAssignment) (bool, error) {
	if s.pool == nil {
		return false, ErrBotReclaimWorkerOffline
	}
	client, ok := s.pool.Get(nodeUUID)
	if !ok || client == nil || client.Worker == nil {
		return false, ErrBotReclaimWorkerOffline
	}
	identity := fmt.Sprintf("bot-reclaim|%s|%d", assignment.BotUuid, assignment.Generation)
	request := &workerpb.ApplyBotBatchRequest{
		BatchId:        stableBotLoadUUID(identity),
		IdempotencyKey: "bot-reclaim-" + stableBotLoadDigest(identity),
		Assignments:    []*workerpb.BotAssignment{assignment},
	}
	response, err := client.Worker.ApplyBotBatch(ctx, request)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrBotReclaimDisposeFailed, err)
	}
	if response == nil {
		return false, fmt.Errorf("%w: Worker 未返回回执", ErrBotReclaimDisposeFailed)
	}
	var result *workerpb.ApplyBotBatchItemResult
	for _, item := range response.Results {
		if item != nil && item.BotUuid == assignment.BotUuid {
			result = item
			break
		}
	}
	// 未返回逐项回执：分片已无该 Bot 时也可能如此，按幂等成功处理。
	if result == nil {
		return true, nil
	}
	if result.Accepted {
		return result.Skipped, nil
	}
	// 幂等：Worker 已无该 Bot 或已处于 stopped，视为回收成功。
	switch strings.ToLower(result.ErrorCode) {
	case "already_stopped", "not_found", "bot_not_found", "missing":
		return true, nil
	}
	msg := result.Error
	if msg == "" {
		msg = result.Status
	}
	return false, fmt.Errorf("%w: %s", ErrBotReclaimDisposeFailed, msg)
}

// botReclaimStale 是单条命中失效判据的 Bot 及其判据上下文。
type botReclaimStale struct {
	bot                    model.Bot
	reason                 string
	currentEpochGeneration int64
}

// BotReclaimService 失效 Bot 自动回收与容量自动补足（FR-460）。
//
// 每拍：按 workerEpoch 不匹配 + 状态判据识别失效 Fleet Bot → 宽限观察（pending）→ 宽限后
// confirmed → auto 且 Fleet 归属则下发停用并 CP 账本去账 → 触发容量补足回计划数目标。
// V1 手动 Bot（无 load_batch_id/stress_session_id）只入列表等人工确认，永不自动处置。
type BotReclaimService struct {
	db         *gorm.DB
	settings   SettingsReader
	capacities BotReclaimCapacitySource
	stopper    BotReclaimStopper
	audit      *AuditService
	resolver   *BotExecutorResolver
	// fleet 提供执行节点 Worker 的 Fleet 实存快照（FR-460 §2.4.3 对账真源）；nil 时退回保守世代比对。
	fleet BotReclaimFleetSnapshotSource
	// refill 复用执行核心的补足助手（rebuildRunningAssignment / ApplyBotLoadBatch / 结果回写）。
	refill *BotLoadExecutionService
	now    func() time.Time
	// stopTimeout 单次停用下发 RPC 超时。
	stopTimeout time.Duration
	// snapshotTimeout 单次 Fleet 实存快照 RPC 超时（N3）。
	snapshotTimeout time.Duration

	// guardMu 保护补足退避与冷却窗状态（进程内，随巡检生命周期）。
	guardMu sync.Mutex
	// refillAttempts 记录各会话连续补足尝试与下次允许时刻（指数退避）。
	refillAttempts map[uint]botReclaimRefillState
	// smallGapStreak 记录各会话「低于缺口阈值」的连续拍数（N5），连续超限后强制补足。
	smallGapStreak map[uint]int
	// rearmed 记录最近被重新武装（refill 下发 running）的 Bot 及时间，用于回收冷却窗。
	rearmed map[string]time.Time

	// sweepMu 保护单拍共享视图缓存（N3/N7）：同一拍内节点世代与 Fleet 快照各只取一次。
	sweepMu    sync.Mutex
	sweepDepth int
	sweepEpoch map[uint]BotReclaimNodeEpoch
	sweepSeen  map[string]botReclaimNodePresence
}

// botReclaimRefillState 是单会话的补足尝试状态机（attempts + 下次允许时刻）。
type botReclaimRefillState struct {
	attempts      int
	nextAllowedAt time.Time
}

// NewBotReclaimService 创建失效 Bot 回收服务。settings/stopper/execution 可为 nil（单测裁剪）。
func NewBotReclaimService(db *gorm.DB, settings SettingsReader, capacities BotReclaimCapacitySource, stopper BotReclaimStopper, execution *BotLoadExecutionService) *BotReclaimService {
	return &BotReclaimService{
		db: db, settings: settings, capacities: capacities, stopper: stopper,
		resolver: NewBotExecutorResolver(db), refill: execution, now: time.Now,
		stopTimeout: 30 * time.Second, snapshotTimeout: defaultBotReclaimSnapshotTimeout,
	}
}

// NewGRPCBotReclaimService 使用连接池与既有容量目录/执行核心装配生产实例。
func NewGRPCBotReclaimService(db *gorm.DB, settings SettingsReader, directory *BotLoadCapacityDirectory, pool *cpgrpc.ClientPool, execution *BotLoadExecutionService) *BotReclaimService {
	svc := NewBotReclaimService(db, settings, NewDirectoryBotReclaimCapacity(directory), poolBotReclaimStopper{pool: pool}, execution)
	// 复用既有隧道优先连接池做 Worker Fleet 快照对账，不新增 RPC。
	svc.fleet = poolBotFleetRuntimeClient{pool: pool}
	return svc
}

// SetAudit 注入审计服务（自动/手动回收与补足均记）；nil 时跳过审计。
func (s *BotReclaimService) SetAudit(a *AuditService) { s.audit = a }

// SetFleetSnapshotSource 注入 Worker Fleet 快照来源（对账真源，测试/裁剪场景可省略）。
func (s *BotReclaimService) SetFleetSnapshotSource(src BotReclaimFleetSnapshotSource) { s.fleet = src }

// SetNow 注入时钟（测试用）。
func (s *BotReclaimService) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// gracePeriod 生效宽限期（默认 2m）。
func (s *BotReclaimService) gracePeriod() time.Duration {
	if s.settings == nil {
		return defaultBotReclaimGracePeriod
	}
	raw := s.settings.EffectiveValue(SettingKeyBotReclaimGracePeriod)
	if raw == "" {
		return defaultBotReclaimGracePeriod
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultBotReclaimGracePeriod
	}
	return d
}

// autoReclaim 是否自动回收（默认 true，仅对 Fleet 归属 Bot 生效）。
func (s *BotReclaimService) autoReclaim() bool {
	if s.settings == nil {
		return defaultBotReclaimAuto
	}
	raw := s.settings.EffectiveValue(SettingKeyBotReclaimAuto)
	if raw == "" {
		return defaultBotReclaimAuto
	}
	return raw == "true"
}

// refillAllowed 判断某会话本轮是否允许补足（指数退避 + 尝试上限）。
func (s *BotReclaimService) refillAllowed(sessionID uint, now time.Time) bool {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	state, ok := s.refillAttempts[sessionID]
	if !ok {
		return true
	}
	return !now.Before(state.nextAllowedAt)
}

// noteRefillAttempt 记录一次补足尝试并推进指数退避；达上限后转入长冷却（10m）。
func (s *BotReclaimService) noteRefillAttempt(sessionID uint, now time.Time) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	if s.refillAttempts == nil {
		s.refillAttempts = make(map[uint]botReclaimRefillState)
	}
	state := s.refillAttempts[sessionID]
	state.attempts++
	backoff := botReclaimRefillBaseBackoff << min(state.attempts-1, 4)
	if state.attempts >= botReclaimRefillMaxAttempts || backoff > botReclaimRefillMaxBackoff {
		backoff = botReclaimRefillMaxBackoff
	}
	state.nextAllowedAt = now.Add(backoff)
	s.refillAttempts[sessionID] = state
}

// clearRefillState 清零某会话的补足退避状态（缺口已收敛时调用，避免历史退避抑制后续正常补足）。
func (s *BotReclaimService) clearRefillState(sessionID uint) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	delete(s.refillAttempts, sessionID)
	delete(s.smallGapStreak, sessionID)
}

// noteSmallGap 记录一次「低于缺口阈值」的观测并返回连续拍数（N5）。
func (s *BotReclaimService) noteSmallGap(sessionID uint) int {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	if s.smallGapStreak == nil {
		s.smallGapStreak = make(map[uint]int)
	}
	s.smallGapStreak[sessionID]++
	return s.smallGapStreak[sessionID]
}

// clearSmallGap 清零某会话的小缺口连续拍数（已决定补足或缺口已收敛）。
func (s *BotReclaimService) clearSmallGap(sessionID uint) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	delete(s.smallGapStreak, sessionID)
}

// pruneRefillState 裁剪已结束会话的退避/小缺口状态（N6：进程内 map 不得对已结束会话无限累积）。
func (s *BotReclaimService) pruneRefillState(active map[uint]struct{}) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	for id := range s.refillAttempts {
		if _, ok := active[id]; !ok {
			delete(s.refillAttempts, id)
		}
	}
	for id := range s.smallGapStreak {
		if _, ok := active[id]; !ok {
			delete(s.smallGapStreak, id)
		}
	}
}

// beginSweepView 开启单拍共享视图（N3/N7），Sweep 内节点世代与 Fleet 快照各只取一次。
func (s *BotReclaimService) beginSweepView() {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepDepth == 0 {
		// sweepEpoch 为 nil 表示「本拍尚未取过节点世代」，与「已取到空视图」区分。
		s.sweepEpoch = nil
		s.sweepSeen = make(map[string]botReclaimNodePresence)
	}
	s.sweepDepth++
}

// endSweepView 关闭单拍共享视图；最外层退出时释放缓存。
func (s *BotReclaimService) endSweepView() {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepDepth > 0 {
		s.sweepDepth--
	}
	if s.sweepDepth == 0 {
		s.sweepEpoch = nil
		s.sweepSeen = nil
	}
}

// cachedNodeEpochs 读取本拍已缓存的节点世代视图（无活动拍时返回 false）。
func (s *BotReclaimService) cachedNodeEpochs() (map[uint]BotReclaimNodeEpoch, bool) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepDepth == 0 || s.sweepEpoch == nil {
		return nil, false
	}
	return s.sweepEpoch, true
}

// storeNodeEpochs 将节点世代视图写入本拍缓存。
func (s *BotReclaimService) storeNodeEpochs(epochs map[uint]BotReclaimNodeEpoch) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepDepth > 0 {
		s.sweepEpoch = epochs
	}
}

// nodeEpochs 读取执行节点世代视图；单拍内复用（N7：避免每回收一台 Bot 追加一次容量快照）。
func (s *BotReclaimService) nodeEpochs(ctx context.Context) (map[uint]BotReclaimNodeEpoch, error) {
	if s.capacities == nil {
		return map[uint]BotReclaimNodeEpoch{}, nil
	}
	if cached, ok := s.cachedNodeEpochs(); ok {
		return cached, nil
	}
	epochs, err := s.capacities.NodeEpochs(ctx)
	if err != nil {
		// N7：失败也占位本拍缓存，避免同一拍内每回收一台 Bot 就重打一次注定失败的容量快照。
		s.storeNodeEpochs(map[uint]BotReclaimNodeEpoch{})
		return nil, err
	}
	if epochs == nil {
		epochs = map[uint]BotReclaimNodeEpoch{}
	}
	s.storeNodeEpochs(epochs)
	return epochs, nil
}

// cachedNodePresence 读取本拍已缓存的节点 Fleet 快照。
func (s *BotReclaimService) cachedNodePresence(nodeUUID string) (botReclaimNodePresence, bool) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepDepth == 0 || s.sweepSeen == nil {
		return botReclaimNodePresence{}, false
	}
	presence, ok := s.sweepSeen[nodeUUID]
	return presence, ok
}

// storeNodePresence 将节点 Fleet 快照写入本拍缓存（失败结果也缓存，避免同拍重复打同一假死节点）。
func (s *BotReclaimService) storeNodePresence(nodeUUID string, presence botReclaimNodePresence) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepDepth == 0 || s.sweepSeen == nil {
		return
	}
	s.sweepSeen[nodeUUID] = presence
}

// noteRearmed 记录被重新武装（refill 下发 running）的 Bot，进入回收冷却窗。
func (s *BotReclaimService) noteRearmed(uuids []string, now time.Time) {
	if len(uuids) == 0 {
		return
	}
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	if s.rearmed == nil {
		s.rearmed = make(map[string]time.Time)
	}
	for _, uuid := range uuids {
		if uuid != "" {
			s.rearmed[uuid] = now
		}
	}
	if len(s.rearmed) > 4096 {
		for uuid, at := range s.rearmed {
			if now.Sub(at) > botReclaimRearmCooldown*4 {
				delete(s.rearmed, uuid)
			}
		}
	}
}

// isRecentlyRearmed 判断 Bot 是否仍在重新武装冷却窗内（冷却窗内不判失效，消除 stop+create 振荡）。
func (s *BotReclaimService) isRecentlyRearmed(botUUID string, now time.Time) bool {
	if botUUID == "" {
		return false
	}
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	at, ok := s.rearmed[botUUID]
	return ok && now.Sub(at) < botReclaimRearmCooldown
}

// Sweep 执行一轮巡检：判定失效 → 宽限状态机 → 自动处置 → 容量补足。
// 各阶段独立推进，前段失败不阻断后段（便于容量 RPC 抖动时仍能补足）。
func (s *BotReclaimService) Sweep(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	now := s.now().UTC()
	// N3/N7：本拍内节点世代与 Fleet 快照各只取一次，供判定/回收/补足三阶段复用。
	s.beginSweepView()
	defer s.endSweepView()
	stale, err := s.detectStale(ctx, now)
	if err != nil {
		slog.Warn("失效 Bot 判定失败", "error", err)
	} else if err := s.applyGrace(ctx, stale, now); err != nil {
		slog.Warn("失效 Bot 宽限推进失败", "error", err)
	}
	if err := s.disposeConfirmed(ctx); err != nil {
		slog.Warn("失效 Bot 自动回收失败", "error", err)
	}
	// FR-472：先收敛僵尸会话，再补足——僵尸会话从根上不该进入补足扫描（spec §2.4.1）。
	if err := s.reapZombieSessions(ctx, now); err != nil {
		slog.Warn("僵尸压测会话收敛失败", "error", err)
	}
	if err := s.refillRunningSessions(ctx); err != nil {
		slog.Warn("Bot 容量补足失败", "error", err)
	}
	return nil
}

// reapZombieSessions 收敛停滞的压测会话（FR-472，spec §2.4.1）。
//
// 会话终结原本依赖显式 Stop 调用；CP 重启或 bot-worker 死亡后该路径丢失，会话永久停留 running，
// 于是每拍被 refillRunningSessions 当作补足目标反复重建期望行并失败（失败落在事务内 → 持续写盘）。
// 进程内退避兜不住这一点（随进程清零，重启即全量重试），故此处按**数据库可见的事实**判定僵尸。
//
// 判定：status=running 且在线 Bot 数为 0 且 UpdatedAt 早于 now-阈值。
// 仍持有在线 Bot 的会话永不被收敛（反向由 TestZombieSession_HealthySessionUntouched 守护）。
func (s *BotReclaimService) reapZombieSessions(ctx context.Context, now time.Time) error {
	candidates, err := s.findZombieSessions(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for i := range candidates {
		if err := s.reapZombieSession(ctx, &candidates[i], now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// findZombieSessions 查出候选僵尸会话：running 且在线 Bot 数为 0 且长时间无进展。
func (s *BotReclaimService) findZombieSessions(ctx context.Context, now time.Time) ([]model.BotStressSession, error) {
	var candidates []model.BotStressSession
	// 先用「状态 + 停滞时长」做粗筛（走索引，代价有界），再逐个精确核对在线 Bot 数。
	cutoff := now.Add(-s.zombieIdleThreshold())
	if err := s.db.WithContext(ctx).
		Where("status = ? AND deleted_at IS NULL AND updated_at < ?", model.BotStressSessionRunning, cutoff).
		Limit(botReclaimDetectLimit).
		Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("查询僵尸压测会话失败: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	zombies := make([]model.BotStressSession, 0, len(candidates))
	for i := range candidates {
		var online int64
		if err := s.db.WithContext(ctx).Model(&model.Bot{}).
			Where("stress_session_id = ? AND deleted_at IS NULL AND desired_state = ? AND status = ?",
				candidates[i].ID, model.BotDesiredRunning, model.BotStatusConnected).
			Count(&online).Error; err != nil {
			return nil, fmt.Errorf("统计僵尸会话在线 Bot 数失败: %w", err)
		}
		// 仍持有在线 Bot 的会话是活的，绝不能收敛。
		if online > 0 {
			continue
		}
		zombies = append(zombies, candidates[i])
	}
	return zombies, nil
}

// reapZombieSession 把单个僵尸会话收敛为 stopped 并写审计。
//
// 不复用 Stop 路径：僵尸会话的 Worker 侧舰队早已不存在，走停机流程只会再失败一次。
// 此处只做状态收敛——它足以把该会话移出补足扫描，这正是终止写放大的充分条件。
func (s *BotReclaimService) reapZombieSession(ctx context.Context, session *model.BotStressSession, now time.Time) error {
	if session == nil {
		return nil
	}
	stalled := now.Sub(session.UpdatedAt).Round(time.Second)
	reason := fmt.Sprintf("僵尸会话自动收敛：%s 内无进展且在线 Bot 数为 0（FR-472）", stalled)
	updates := map[string]any{
		"status":     model.BotStressSessionStopped,
		"last_error": reason,
		"ended_at":   now,
	}
	res := s.db.WithContext(ctx).Model(&model.BotStressSession{}).
		Where("id = ? AND status = ?", session.ID, model.BotStressSessionRunning).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("收敛僵尸会话失败: %w", res.Error)
	}
	// 条件更新：若该拍内已被其他路径终结（如运维手动 Stop），不重复审计。
	if res.RowsAffected == 0 {
		return nil
	}
	slog.Info("僵尸压测会话已收敛为终态",
		"sessionId", session.ID, "sessionUuid", session.UUID,
		"instanceId", session.InstanceID, "stalled", stalled.String())
	if s.audit != nil {
		detail := fmt.Sprintf(`{"sessionId":%d,"stalledSeconds":%d,"onlineBots":0}`,
			session.ID, int64(stalled.Seconds()))
		s.audit.RecordResultSafe(0, botReclaimSessionReapedAction, "bot_stress_session",
			session.UUID, detail, "", true, "")
	}
	return nil
}

// zombieIdleThreshold 僵尸会话停滞阈值（可由设置项覆盖，缺省 10 分钟）。
func (s *BotReclaimService) zombieIdleThreshold() time.Duration {
	if s.settings == nil {
		return botZombieSessionIdleThreshold
	}
	raw := s.settings.EffectiveValue(SettingKeyBotZombieSessionIdleThreshold)
	if raw == "" {
		return botZombieSessionIdleThreshold
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return botZombieSessionIdleThreshold
	}
	return d
}

// botReclaimScan 是单拍失效判定所需的执行节点世代 + Worker 实存视图。
type botReclaimScan struct {
	epochs            map[uint]BotReclaimNodeEpoch
	epochFromCapacity bool
	// presence 节点 → 该节点 Worker 当前实存 Bot 集合（缺省 Known=false，走保守兜底）。
	presence map[uint]botReclaimNodePresence
	// cutoff 新鲜度窗口边界（now - 90s），用于 F2「新 Bot 首拍不判失效」。
	cutoff time.Time
	// connectingCutoff 从未上报 connecting Bot 的判定上限（now - 15m，N1）。
	connectingCutoff time.Time
}

// detectStale 命中 §2.1 判据的失效 Bot 集合（SQL 粗筛 + 内存世代/实存精判）。
func (s *BotReclaimService) detectStale(ctx context.Context, now time.Time) ([]botReclaimStale, error) {
	// N7：单拍内复用节点世代视图（Sweep 已缓存时不再追加容量快照 RPC）。
	epochs, err := s.nodeEpochs(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询执行节点世代失败: %w", err)
	}
	cutoff := now.Add(-botReclaimFreshnessWindow)
	var bots []model.Bot
	if err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("desired_state = ?", model.BotDesiredRunning).
		// F7：仅 Fleet 归属 Bot 进入回收判定（V1 手动 Bot 无 batch/session，属用户资源，
		// 既不能自动处置也无法经回收端点处置，纳入只会产生无法收敛的死条目）。括号必须显式，
		// 否则 OR 会破坏与其它 AND 条件的优先级。
		Where("(load_batch_id IS NOT NULL OR stress_session_id IS NOT NULL)").
		Where("status IN ?", []model.BotStatus{model.BotStatusError, model.BotStatusDisconnected, model.BotStatusConnecting}).
		Where("last_seen_at IS NULL OR last_seen_at < ?", cutoff).
		Order("id ASC").
		Limit(botReclaimDetectLimit).
		Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询失效 Bot 候选失败: %w", err)
	}
	scan := botReclaimScan{
		epochs: epochs, epochFromCapacity: s.capacities != nil,
		cutoff: cutoff, connectingCutoff: now.Add(-botReclaimConnectingFirstSeenWindow),
	}
	if s.fleet != nil && len(bots) > 0 {
		scan.presence = s.nodePresences(ctx, bots, epochs)
	}
	out := make([]botReclaimStale, 0, len(bots))
	for i := range bots {
		// 冷却窗：刚被补足（重新武装）的 Bot 不立即再次判定失效，消除 stop+create 振荡。
		if s.isRecentlyRearmed(bots[i].UUID, now) {
			continue
		}
		reason, ok := staleBotReclaimReason(&bots[i], scan)
		if !ok {
			continue
		}
		entry := botReclaimStale{bot: bots[i], reason: reason}
		if bots[i].ExecutorNodeID != nil {
			entry.currentEpochGeneration = epochs[*bots[i].ExecutorNodeID].WorkerEpochGeneration
		}
		out = append(out, entry)
	}
	return out, nil
}

// nodePresences 逐节点取 Worker 实存快照，构建 Bot UUID 集合与容量世代。
// 取不到快照的节点标记 Known=false，判定侧退回保守兜底（不因节点抖动而误回收）。
func (s *BotReclaimService) nodePresences(ctx context.Context, bots []model.Bot, epochs map[uint]BotReclaimNodeEpoch) map[uint]botReclaimNodePresence {
	out := make(map[uint]botReclaimNodePresence)
	for i := range bots {
		nodeID := botReclaimNodeID(&bots[i])
		if nodeID == 0 {
			continue
		}
		if _, done := out[nodeID]; done {
			continue
		}
		node, ok := epochs[nodeID]
		if !ok || !node.Exists || node.NodeUUID == "" {
			continue
		}
		out[nodeID] = s.fetchNodePresence(ctx, node.NodeUUID, "")
	}
	return out
}

// currentNodeGeneration 返回 Bot 所属执行节点的当前 Worker 世代号；未知时返回 0。
func (s *BotReclaimService) currentNodeGeneration(ctx context.Context, bot *model.Bot) int64 {
	if bot == nil || bot.ExecutorNodeID == nil {
		return 0
	}
	// N7：复用本拍节点世代视图，避免「每回收一台就追加一次容量快照」。
	epochs, err := s.nodeEpochs(ctx)
	if err != nil {
		return 0
	}
	node, ok := epochs[*bot.ExecutorNodeID]
	if !ok || !node.Exists {
		return 0
	}
	return node.WorkerEpochGeneration
}

// staleBotReclaimReason 判定单条 Bot 的世代失效原因（§2.1 条件 3）。
// epochFromCapacity=false（未装配世代来源）时只判空 epoch/空节点，保守避免误回收。
func staleBotReclaimReason(bot *model.Bot, scan botReclaimScan) (string, bool) {
	if bot == nil || bot.DesiredState != model.BotDesiredRunning {
		return "", false
	}
	switch bot.Status {
	case model.BotStatusError, model.BotStatusDisconnected, model.BotStatusConnecting:
	default:
		return "", false
	}
	if bot.WorkerEpoch == "" {
		if bot.LastSeenAt == nil && bot.Status == model.BotStatusConnecting {
			// N1：从未上报（last_seen_at 为空）的 connecting Bot 也必须受上限约束，否则
			// 「create 已被接受但从未上报」的 Bot 永久豁免 empty_epoch：既不被回收也不被补足
			// （补足侧同样跳过 connecting），容量永久缺一格；新鲜度归真只看 last_seen_at 也会漏过它。
			// 上限用更宽松的 connectingCutoff，容忍节点首次登录较慢。
			if !botReclaimAgedOut(bot, scan.connectingCutoff) {
				return "", false
			}
			// 仍在执行节点 Worker 实存中则视为健康（F1：实存是真源），只判「实存中确实不存在」的僵尸。
			if bot.ExecutorNodeID != nil {
				if presence, ok := scan.presence[*bot.ExecutorNodeID]; ok && presence.Known && presence.contains(bot.UUID) {
					return "", false
				}
			}
			return BotReclaimStaleEmptyEpoch, true
		}
		// F2：新 Bot 首拍（创建/首发事件未超新鲜度窗口）不判失效，
		// 否则「刚创建 → 立即判失效 → 误回收 → 补足重建」形成 stop+create 振荡。
		if !botReclaimAgedOut(bot, scan.cutoff) {
			return "", false
		}
		return BotReclaimStaleEmptyEpoch, true
	}
	if bot.ExecutorNodeID == nil {
		return BotReclaimStaleNodeMissing, true
	}
	if !scan.epochFromCapacity {
		return "", false
	}
	node, ok := scan.epochs[*bot.ExecutorNodeID]
	if !ok || !node.Exists {
		return BotReclaimStaleNodeMissing, true
	}
	return botReclaimEpochReason(bot, node, scan.presence[*bot.ExecutorNodeID])
}

// botReclaimEpochReason 判定世代落后（§2.1 条件 3 第三款）。
//
// F1：节点级 WorkerEpochGeneration 是各分片世代号的 **max**（worker sharded.go 聚合），
// 而每分片 Manager 独立自增、Bot 记录的是其所属分片的世代号。生产多分片下任一分片独立
// 崩溃自愈都会让节点 max 领先于健康 Bot 的分片世代 → 健康/瞬时断线 Bot 被误判 epoch_mismatch。
// 因此世代落后只是「嫌疑」，真源取 Worker 实存快照：仍在实存中的 Bot 一律视为健康。
func botReclaimEpochReason(bot *model.Bot, node BotReclaimNodeEpoch, presence botReclaimNodePresence) (string, bool) {
	if bot.WorkerEpochGeneration >= node.WorkerEpochGeneration {
		return "", false
	}
	if presence.Known {
		if presence.contains(bot.UUID) {
			return "", false
		}
		return BotReclaimStaleEpochMismatch, true
	}
	// 快照不可用：保守兜底。
	// 多分片（节点 epoch 形如 "<分片epoch>:<分片数>"）无法把 Bot 分片世代与节点聚合世代逐一对齐，
	// 保守不判失效；单分片时节点 epoch 即该子进程世代标识，Bot 记录的 epoch 与之相同即视为同世代健康。
	if strings.Contains(node.WorkerEpoch, ":") {
		return "", false
	}
	if node.WorkerEpoch != "" && bot.WorkerEpoch == node.WorkerEpoch {
		return "", false
	}
	return BotReclaimStaleEpochMismatch, true
}

// botReclaimAgedOut 判断 Bot 是否已超新鲜度窗口（F2：新 Bot 首拍不判失效）。
func botReclaimAgedOut(bot *model.Bot, cutoff time.Time) bool {
	if bot.LastSeenAt != nil {
		return bot.LastSeenAt.Before(cutoff)
	}
	return !bot.CreatedAt.IsZero() && bot.CreatedAt.Before(cutoff)
}

// applyGrace 对失效集合推进 pending/confirmed 状态机，并取消已被新世代认领的跟踪。
func (s *BotReclaimService) applyGrace(ctx context.Context, stale []botReclaimStale, now time.Time) error {
	grace := s.gracePeriod()
	seen := make(map[uint]struct{}, len(stale))
	for i := range stale {
		seen[stale[i].bot.ID] = struct{}{}
		if err := s.observeStale(ctx, &stale[i], now, grace); err != nil {
			slog.Warn("记录失效 Bot 失败", "botId", stale[i].bot.ID, "error", err)
		}
	}
	return s.cancelResolved(ctx, seen, now)
}

func (s *BotReclaimService) observeStale(ctx context.Context, stale *botReclaimStale, now time.Time, grace time.Duration) error {
	bot := &stale.bot
	var rec model.FleetBotReclaim
	err := s.db.WithContext(ctx).Where("bot_id = ?", bot.ID).Order("id DESC").First(&rec).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) ||
		rec.Status == model.FleetBotReclaimDisposed || rec.Status == model.FleetBotReclaimCancelled {
		// 终态历史存在时仍可新建一轮 pending（Bot 再次失效）。
		return s.db.WithContext(ctx).Create(&model.FleetBotReclaim{
			BotID: bot.ID, BotUUID: bot.UUID, NodeID: botReclaimNodeID(bot),
			SessionID: botReclaimSessionID(bot), BatchID: botReclaimBatchID(bot), FleetOwned: isFleetOwnedBot(bot),
			StaleReason: stale.reason, ObservedEpoch: bot.WorkerEpoch,
			ObservedEpochGeneration: bot.WorkerEpochGeneration, CurrentEpochGeneration: stale.currentEpochGeneration,
			Status: model.FleetBotReclaimPending, FirstSeenAt: now, LastSeenAt: now,
		}).Error
	}
	updates := map[string]any{
		"last_seen_at":              now,
		"stale_reason":              stale.reason,
		"observed_epoch":            bot.WorkerEpoch,
		"observed_epoch_generation": bot.WorkerEpochGeneration,
		"current_epoch_generation":  stale.currentEpochGeneration,
		"fleet_owned":               isFleetOwnedBot(bot),
	}
	if rec.Status == model.FleetBotReclaimPending && !now.Before(rec.FirstSeenAt.Add(grace)) {
		updates["status"] = model.FleetBotReclaimConfirmed
		slog.Warn("失效 Bot 宽限期已过", "botId", bot.ID, "botUuid", bot.UUID,
			"staleReason", stale.reason, "firstSeenAt", rec.FirstSeenAt, "gracePeriod", grace.String(),
			"fleetOwned", isFleetOwnedBot(bot))
	}
	return s.db.WithContext(ctx).Model(&rec).Updates(updates).Error
}

// cancelResolved 将本拍未见（世代已追平/被新世代认领）的活跃跟踪标记 cancelled（镜像 FR-326）。
func (s *BotReclaimService) cancelResolved(ctx context.Context, seen map[uint]struct{}, now time.Time) error {
	var rows []model.FleetBotReclaim
	if err := s.db.WithContext(ctx).
		Where("status IN ?", []string{string(model.FleetBotReclaimPending), string(model.FleetBotReclaimConfirmed)}).
		Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if _, ok := seen[rows[i].BotID]; ok {
			continue
		}
		if err := s.db.WithContext(ctx).Model(&rows[i]).Updates(map[string]any{
			"status": model.FleetBotReclaimCancelled, "last_seen_at": now, "last_error": "",
		}).Error; err != nil {
			slog.Warn("取消失效 Bot 跟踪失败", "reclaimUuid", rows[i].UUID, "error", err)
		}
	}
	return nil
}

// disposeConfirmed 对已确认且 Fleet 归属的失效 Bot 下发停用（auto=true 时）。
func (s *BotReclaimService) disposeConfirmed(ctx context.Context) error {
	if !s.autoReclaim() {
		return nil
	}
	var rows []model.FleetBotReclaim
	if err := s.db.WithContext(ctx).
		Where("status = ? AND fleet_owned = ?", model.FleetBotReclaimConfirmed, true).
		Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := s.disposeOne(ctx, &rows[i], "auto", 0, ""); err != nil {
			slog.Warn("自动回收失效 Bot 失败", "reclaimUuid", rows[i].UUID, "botId", rows[i].BotID, "error", err)
		}
	}
	return nil
}

func (s *BotReclaimService) disposeOne(ctx context.Context, rec *model.FleetBotReclaim, mode string, userID uint, ip string) error {
	if rec.Status == model.FleetBotReclaimDisposed {
		return nil // 幂等：已处置。
	}
	if rec.Status != model.FleetBotReclaimPending && rec.Status != model.FleetBotReclaimConfirmed {
		return ErrBotReclaimNotActive
	}
	var bot model.Bot
	if err := s.db.WithContext(ctx).First(&bot, rec.BotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Bot 已不存在（分片早已清掉）：幂等收敛为 disposed，并补审计（F6：终态路径也须留痕）。
			s.recordDisposeAudit(userID, ip, nil, rec, mode, true, "Bot 记录已不存在，视为已回收")
			return s.finishDispose(ctx, rec, mode, userID, ip, "Bot 记录已不存在，视为已回收")
		}
		return err
	}
	if !isFleetOwnedBot(&bot) {
		return ErrBotReclaimNotFleetOwned
	}
	nodeUUID := s.resolveExecutorNodeUUID(ctx, &bot)
	if s.stopper == nil || nodeUUID == "" {
		msg := "执行节点未连接，待节点回归后重试"
		s.markDisposeError(ctx, rec, msg)
		s.recordDisposeAudit(userID, ip, &bot, rec, mode, false, msg)
		return ErrBotReclaimWorkerOffline
	}
	generation := bot.DesiredStateGeneration
	if generation <= 0 {
		generation = 1
	}
	rpcCtx, cancel := context.WithTimeout(ctx, s.stopTimeout)
	defer cancel()
	skipped, err := s.stopper.StopFleetBot(rpcCtx, nodeUUID, &workerpb.BotAssignment{
		BotUuid: bot.UUID, SessionUuid: s.sessionUUIDForBot(ctx, &bot), Generation: generation,
		DesiredState: "stopped", ConfigHash: bot.ConfigHash,
	})
	if err != nil {
		s.markDisposeError(ctx, rec, err.Error())
		s.recordDisposeAudit(userID, ip, &bot, rec, mode, false, err.Error())
		return err
	}
	// 账本去账：desired_state/status → stopped、清 worker_epoch，并修正批次 connected_count。
	// F8：同时把 worker_epoch_generation 抬升到节点当前世代，使旧世代（分片重启前）的迟到事件
	// 被 classifyRuntimeEpoch 判为 stale 而丢弃，避免 stopped 被翻回 connected。
	reclaimEpochGeneration := bot.WorkerEpochGeneration
	if current := s.currentNodeGeneration(ctx, &bot); current > reclaimEpochGeneration {
		reclaimEpochGeneration = current
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]any{
			"desired_state": model.BotDesiredStopped, "status": model.BotStatusStopped,
			"worker_epoch": "", "worker_epoch_generation": reclaimEpochGeneration, "last_error": "",
		}).Error; err != nil {
			return err
		}
		if bot.LoadBatchID != nil {
			return recountBotReclaimBatch(tx, *bot.LoadBatchID)
		}
		return nil
	}); err != nil {
		// F6：账本事务失败也须留痕（否则「回收失败无审计」）。
		s.markDisposeError(ctx, rec, err.Error())
		s.recordDisposeAudit(userID, ip, &bot, rec, mode, false, err.Error())
		return err
	}
	slog.Info("失效 Bot 已回收", "botUuid", bot.UUID, "nodeUuid", nodeUUID, "mode", mode,
		"reclaimUuid", rec.UUID, "skipped", skipped, "staleReason", rec.StaleReason)
	s.recordDisposeAudit(userID, ip, &bot, rec, mode, true, "")
	return s.finishDispose(ctx, rec, mode, userID, ip, "")
}

// finishDispose 将回收记录置 disposed（清 last_error）。
func (s *BotReclaimService) finishDispose(ctx context.Context, rec *model.FleetBotReclaim, mode string, userID uint, ip string, note string) error {
	disposedAt := s.now().UTC()
	if err := s.db.WithContext(ctx).Model(rec).Updates(map[string]any{
		"status": model.FleetBotReclaimDisposed, "disposed_at": disposedAt, "dispose_mode": mode, "last_error": "",
	}).Error; err != nil {
		return err
	}
	rec.Status = model.FleetBotReclaimDisposed
	rec.DisposeMode = mode
	rec.DisposedAt = &disposedAt
	if note != "" {
		slog.Info("失效 Bot 回收收敛", "reclaimUuid", rec.UUID, "botUuid", rec.BotUUID, "note", note)
	}
	return nil
}

func (s *BotReclaimService) markDisposeError(ctx context.Context, rec *model.FleetBotReclaim, msg string) {
	if len(msg) > 512 {
		msg = msg[:512]
	}
	_ = s.db.WithContext(ctx).Model(rec).Update("last_error", msg).Error
}

func (s *BotReclaimService) recordDisposeAudit(userID uint, ip string, bot *model.Bot, rec *model.FleetBotReclaim, mode string, success bool, errMsg string) {
	if s.audit == nil {
		return
	}
	action := "bot_reclaim.dispose_manual"
	if mode == "auto" {
		action = "bot_reclaim.dispose_auto"
	}
	botUUID := rec.BotUUID
	if bot != nil {
		botUUID = bot.UUID
	}
	detail := fmt.Sprintf(`{"botUuid":%q,"reclaimUuid":%q,"nodeId":%d,"sessionId":%d,"batchId":%d,"staleReason":%q,"mode":%q}`,
		botUUID, rec.UUID, rec.NodeID, rec.SessionID, rec.BatchID, rec.StaleReason, mode)
	s.audit.RecordResultSafe(userID, action, "bot_reclaim", rec.UUID, detail, ip, success, errMsg)
}

func (s *BotReclaimService) resolveExecutorNodeUUID(ctx context.Context, bot *model.Bot) string {
	node, _, err := s.resolver.Resolve(bot)
	if err != nil || node == nil {
		return ""
	}
	return node.UUID
}

func (s *BotReclaimService) sessionUUIDForBot(ctx context.Context, bot *model.Bot) string {
	if bot.StressSessionID != nil {
		var session model.BotStressSession
		if err := s.db.WithContext(ctx).Select("uuid").First(&session, *bot.StressSessionID).Error; err == nil {
			return session.UUID
		}
	}
	if bot.LoadBatchID != nil {
		var batch model.BotLoadBatch
		if err := s.db.WithContext(ctx).Select("stress_session_id").First(&batch, *bot.LoadBatchID).Error; err == nil {
			var session model.BotStressSession
			if err := s.db.WithContext(ctx).Select("uuid").First(&session, batch.StressSessionID).Error; err == nil {
				return session.UUID
			}
		}
	}
	return ""
}

func recountBotReclaimBatch(tx *gorm.DB, batchID uint) error {
	return tx.Model(&model.BotLoadBatch{}).Where("id = ?", batchID).Update("connected_count",
		gorm.Expr("(SELECT COUNT(*) FROM bots WHERE bots.load_batch_id = bot_load_batches.id AND bots.status = ? AND bots.deleted_at IS NULL)",
			model.BotStatusConnected)).Error
}

// refillRunningSessions 对每个 running 会话按 planned_count 目标补足缺失/被回收的 Bot。
func (s *BotReclaimService) refillRunningSessions(ctx context.Context) error {
	if s.refill == nil || s.db == nil {
		return nil
	}
	var sessions []model.BotStressSession
	// N8：必须带 Instance 关联。补足预扫描会按计划重建期望行并逐字段比对（含含 InstanceUuid 的
	// ConfigHash），缺 Instance 时会算出与落库值不同的 ConfigHash → materialize 直接失败，
	// 补足整条路径退化为空转（每拍仅打一条 WARN）。
	if err := s.db.WithContext(ctx).
		Where("status = ? AND deleted_at IS NULL", model.BotStressSessionRunning).
		Preload("Instance").
		Find(&sessions).Error; err != nil {
		return fmt.Errorf("查询 running Bot 压测会话失败: %w", err)
	}
	var errs []error
	// N6：裁剪已结束会话的进程内状态，避免退避/小缺口 map 无界累积。
	active := make(map[uint]struct{}, len(sessions))
	for i := range sessions {
		active[sessions[i].ID] = struct{}{}
	}
	s.pruneRefillState(active)
	for i := range sessions {
		if botLoadStopIntentRecorded(sessions[i].LastError) {
			continue
		}
		if err := s.refillSession(ctx, &sessions[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// refillSession 把单个会话的在线 Bot 补回 Σ planned_count：重建缺失 ordinal、重新武装被回收 Bot。
func (s *BotReclaimService) refillSession(ctx context.Context, session *model.BotStressSession) error {
	plan, err := decodeStartAllocationPlan(session)
	if err != nil {
		return nil // 无服务端计划的会话（历史 V1）不补足。
	}
	// N8：期望行比对依赖 session.Instance——InstanceUuid 参与 ConfigHash，缺失会让按计划重建的
	// 期望行与落库行不一致，materialize 直接失败。此处兜底补齐，避免调用方漏 Preload。
	if session.Instance.ID == 0 && session.InstanceID != 0 {
		var instance model.Instance
		if err := s.db.WithContext(ctx).First(&instance, session.InstanceID).Error; err == nil {
			session.Instance = instance
		}
	}
	config, err := parseBotLoadConnectionConfig(session.Config)
	if err != nil {
		return nil
	}
	cohortAssignments, cohortJSON, cohortBudgetMS, err := prepareBotLoadScenarioAssignments(session)
	if err != nil {
		return nil
	}
	prepared := &botLoadStartPreparation{
		session: session, plan: plan, config: config,
		cohortAssignments: cohortAssignments, cohortJSON: cohortJSON, cohortBudgetMS: cohortBudgetMS,
	}
	var batches []model.BotLoadBatch
	if err := s.db.WithContext(ctx).Where("stress_session_id = ?", session.ID).Find(&batches).Error; err != nil {
		return fmt.Errorf("查询 Bot 负载批次失败: %w", err)
	}
	if len(batches) == 0 {
		return nil
	}
	batchesByOrdinal := make(map[int]model.BotLoadBatch, len(batches))
	target := 0
	for _, batch := range batches {
		batchesByOrdinal[batch.Ordinal] = batch
		target += batch.PlannedCount
	}
	if target <= 0 {
		return nil
	}
	var actual int64
	if err := s.db.WithContext(ctx).Model(&model.Bot{}).
		Where("stress_session_id = ? AND deleted_at IS NULL AND desired_state = ? AND status = ?",
			session.ID, model.BotDesiredRunning, model.BotStatusConnected).
		Count(&actual).Error; err != nil {
		return fmt.Errorf("统计 Bot 负载在线数失败: %w", err)
	}
	if int(actual) >= target {
		s.clearRefillState(session.ID) // 缺口已收敛：清零退避，避免历史退避抑制后续正常补足。
		return nil
	}
	// F4：缺口比例阈值（spec §5）——缺口低于目标比例时不补足，抑制连接建立中的瞬时抖动。
	// N5：比例阈值须向下取整并至少有 1 台的绝对下限，否则大 target（≥51）下「单台缺失」永远落在
	// 阈值内（1/51 < 2%），容量永久缺一格；仍被阈值拦下的小缺口，连续 N 拍未收敛后强制补足。
	gap := target - int(actual)
	if gap < refillMinGap(target) {
		if s.noteSmallGap(session.ID) < botReclaimRefillSmallGapStreak {
			return nil
		}
	}
	s.clearSmallGap(session.ID)
	// F4：指数退避 + 尝试上限，避免每拍无条件重发造成 stop+create 振荡。
	if !s.refillAllowed(session.ID, s.now().UTC()) {
		return nil
	}
	expected, err := expectedBotLoadBots(prepared, batchesByOrdinal)
	if err != nil {
		return err
	}
	// 幂等补齐计划内全部 Bot 行（OnConflict DoNothing），重建被物理删除的缺失 ordinal。
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return materializeBotLoadBots(tx, session.ID, expected)
	}); err != nil {
		return err
	}
	dispatched, err := s.refillDispatch(ctx, session, expected)
	// N6：只对真正发生派发（或真实下发失败）的尝试消耗退避预算；0 派发（Worker 实存中已存在）
	// 不是一次真实尝试，不该烧掉预算。
	if dispatched > 0 || err != nil {
		s.noteRefillAttempt(session.ID, s.now().UTC())
	}
	// F6：0 派发 / 失败路径也须留痕（此前仅在派发>0 时记审计）。
	if dispatched == 0 || err != nil {
		s.recordRefillAudit(session, dispatched, target, err)
	}
	return err
}

// refillMinGap 计算补足所需的最小缺口台数（N5）：比例阈值向下取整，且至少 1 台。
func refillMinGap(target int) int {
	if target <= 0 {
		return 1
	}
	minGap := int(float64(target) * botReclaimRefillMinGapRatio)
	if minGap < 1 {
		minGap = 1
	}
	return minGap
}

// botRefillGroup 是同一执行节点上待重新武装的 Bot 及其本拍实存快照。
type botRefillGroup struct {
	nodeUUID string
	presence botReclaimNodePresence
	bots     []model.Bot
}

// refillDispatch 对「当前非在线/连接中且不在 Worker 实存中」的计划内 Bot 重新下发 running assignment。
//
// 选路（F3，spec §2.4.3）：仅处理未在连接/已连接中的 Bot，且经 Worker 快照对账确认其实存中确实缺失，
// 避免与 ReconcileBotFleetSnapshot 重复派发与抖振；被回收（desired=stopped）或重建（pending）的 Bot
// 一并恢复 desired=running 后下发，使 Worker 与 CP 同时回到目标集合。
//
// N2：CP 账本重置（worker_epoch / 事件序号 / 世代）只在「确认将被派发」之后执行——若 Bot 仍在
// Worker 实存中（CP 显示 error 但 Worker 实际健康），不得清掉其世代与 last_event_seq，
// 否则会把去重基线清空却不重派发，使迟到的旧世代事件得以翻状态。
//
// N3：本拍按执行节点只取一次实存快照，分派阶段复用。
//
// N4：重新武装时递增 desired_state_generation，使重派发的 assignment 载荷、幂等键都变化，
// 同一 Bot 1h 内再次掉线时不会被 Worker 的幂等缓存去重成 no-op。
func (s *BotReclaimService) refillDispatch(ctx context.Context, session *model.BotStressSession, expected []model.Bot) (int, error) {
	var bots []model.Bot
	if err := s.db.WithContext(ctx).
		Where("stress_session_id = ? AND deleted_at IS NULL", session.ID).Find(&bots).Error; err != nil {
		return 0, fmt.Errorf("查询补足候选 Bot 失败: %w", err)
	}
	now := s.now().UTC()
	groups := make(map[string]*botRefillGroup, 4)
	order := make([]string, 0, 4)
	for i := range bots {
		bot := bots[i]
		if bot.Status == model.BotStatusConnected || bot.Status == model.BotStatusConnecting {
			continue // 不重复派发：在线/连接中的不动。
		}
		if s.isRecentlyRearmed(bot.UUID, now) {
			continue // 冷却窗：刚被补足的 Bot 不重复下发。
		}
		nodeUUID := s.resolveExecutorNodeUUID(ctx, &bot)
		if nodeUUID == "" {
			continue
		}
		group, ok := groups[nodeUUID]
		if !ok {
			// N3：节点实存快照按节点取一次，供本地过滤与后续派发复用。
			group = &botRefillGroup{nodeUUID: nodeUUID, presence: s.fetchNodePresence(ctx, nodeUUID, "")}
			groups[nodeUUID] = group
			order = append(order, nodeUUID)
		}
		// F3 §2.4.3：Worker 实存中已有该 Bot → 不重置世代、不重复派发（避免与 reconcile 打架）。
		if group.presence.Known && group.presence.contains(bot.UUID) {
			continue
		}
		group.bots = append(group.bots, bot)
	}
	var errs []error
	dispatched := 0
	for _, nodeUUID := range order {
		group := groups[nodeUUID]
		if len(group.bots) == 0 {
			continue
		}
		// N2：只对「确实将派发」的 Bot 恢复 CP desired=running，避免 reconcile 视其为 stopped 再次停用；
		// 同时重置 worker_epoch/世代/事件序号建立新基线（F8 回收侧抬升世代后仍需接受重建后的新事件）。
		updates := map[string]any{
			"desired_state": model.BotDesiredRunning, "worker_epoch": "",
			"worker_epoch_generation": 0, "last_event_seq": 0,
		}
		// N4：仅在有「实存中确实缺失」的正向证据时递增 desired_state_generation——世代推进会让
		// assignment 载荷与幂等键都变化，使同一 Bot 再次掉线后的重派发不被 Worker 的 1h 幂等缓存
		// 去重成 no-op。快照不可用（Known=false）时不推进，避免与 Worker 世代分叉。
		bumpGeneration := group.presence.Known
		if bumpGeneration {
			updates["desired_state_generation"] = gorm.Expr("CASE WHEN desired_state_generation < 1 THEN 1 ELSE desired_state_generation + 1 END")
		}
		ids := make([]uint, 0, len(group.bots))
		for i := range group.bots {
			ids = append(ids, group.bots[i].ID)
		}
		if err := s.db.WithContext(ctx).Model(&model.Bot{}).Where("id IN ?", ids).Updates(updates).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if bumpGeneration {
			for i := range group.bots {
				generation := group.bots[i].DesiredStateGeneration + 1
				if generation < 1 {
					generation = 1
				}
				group.bots[i].DesiredStateGeneration = generation
			}
		}
		n, err := s.dispatchRefillAssignments(ctx, session, group.nodeUUID, group.presence, group.bots)
		dispatched += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	if dispatched > 0 {
		s.recordRefillAudit(session, dispatched, len(expected), nil)
	}
	return dispatched, errors.Join(errs...)
}

// dispatchRefillAssignments 按执行节点下发重新武装的 running assignment；复用调用方已取的实存快照，
// 并用快照容量世代作为 reconcile 世代（恢复幂等键稳定）。派发结果（而非尝试）才计入 dispatched。
func (s *BotReclaimService) dispatchRefillAssignments(ctx context.Context, session *model.BotStressSession, nodeUUID string, presence botReclaimNodePresence, bots []model.Bot) (int, error) {
	items := make([]botLoadReconcileItem, 0, len(bots))
	rearmed := make([]string, 0, len(bots))
	for i := range bots {
		bot := bots[i]
		assignment, err := s.refill.rebuildRunningAssignment(ctx, session, &bot)
		if err != nil || assignment == nil {
			continue
		}
		items = append(items, botLoadReconcileItem{
			assignment: assignment, bot: &bot, mode: botLoadReconcileRunning,
		})
		rearmed = append(rearmed, bot.UUID)
	}
	if len(items) == 0 {
		return 0, nil
	}
	var errs []error
	dispatched := 0
	generation := presence.CapacityGeneration // 快照不可用时为 0（沿用旧行为）。
	for start := 0; start < len(items); start += maxBotLoadBatchSize {
		end := min(start+maxBotLoadBatchSize, len(items))
		chunk := items[start:end]
		request := buildBotLoadReconcileRequest(session.UUID, generation, chunk)
		response, rpcErr := s.refill.applyBotLoadBatch(ctx, nodeUUID, request)
		if err := s.refill.persistBotLoadReconcileResult(ctx, chunk, request, response, rpcErr); err != nil {
			errs = append(errs, err)
			continue
		}
		dispatched += len(chunk)
	}
	s.noteRearmed(rearmed, s.now().UTC())
	return dispatched, errors.Join(errs...)
}

// fetchNodePresence 取单节点 Worker Fleet 实存快照；失败时返回 Known=false（调用方走保守兜底）。
//
// N3：RPC 带独立超时，避免节点假死把整拍巡检挂住；同一拍内同一节点复用缓存，不再重复发起快照。
func (s *BotReclaimService) fetchNodePresence(ctx context.Context, nodeUUID, sessionUUID string) botReclaimNodePresence {
	if s.fleet == nil || nodeUUID == "" {
		return botReclaimNodePresence{}
	}
	if sessionUUID == "" {
		if cached, ok := s.cachedNodePresence(nodeUUID); ok {
			return cached
		}
	}
	timeout := s.snapshotTimeout
	if timeout <= 0 {
		timeout = defaultBotReclaimSnapshotTimeout
	}
	rpcCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshot, err := s.fleet.GetBotFleetSnapshot(rpcCtx, nodeUUID, sessionUUID)
	if err != nil || snapshot == nil {
		slog.Warn("取执行节点 Fleet 快照失败", "nodeUuid", nodeUUID, "error", err)
		s.storeNodePresence(nodeUUID, botReclaimNodePresence{})
		return botReclaimNodePresence{}
	}
	present := make(map[string]struct{}, len(snapshot.Bots))
	for _, runtime := range snapshot.Bots {
		if runtime != nil && runtime.BotUuid != "" {
			present[runtime.BotUuid] = struct{}{}
		}
	}
	out := botReclaimNodePresence{Known: true, Present: present, CapacityGeneration: snapshot.CapacityGeneration}
	s.storeNodePresence(nodeUUID, out)
	return out
}

func (s *BotReclaimService) recordRefillAudit(session *model.BotStressSession, refilled, target int, err error) {
	if s.audit == nil || session == nil {
		return
	}
	errMsg := ""
	success := err == nil && refilled > 0
	if err != nil {
		errMsg = err.Error()
	}
	detail := fmt.Sprintf(`{"sessionUuid":%q,"sessionId":%d,"refilled":%d,"target":%d,"success":%t}`,
		session.UUID, session.ID, refilled, target, success)
	s.audit.RecordResultSafe(0, "bot_reclaim.refill", "bot_stress_session", session.UUID, detail, "", success, errMsg)
}

// List 列出失效 Bot 回收记录。status 空=全部；activeOnly 时仅 pending+confirmed。
func (s *BotReclaimService) List(status string, activeOnly bool, limit int) ([]model.FleetBotReclaim, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > botReclaimListLimitMax {
		limit = botReclaimListLimitMax // 上限：避免无界返回拖垮巡检/接口。
	}
	q := s.db.Model(&model.FleetBotReclaim{}).Order("first_seen_at DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	} else if activeOnly {
		q = q.Where("status IN ?", []string{
			string(model.FleetBotReclaimPending), string(model.FleetBotReclaimConfirmed),
		})
	}
	var items []model.FleetBotReclaim
	if err := q.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询失效 Bot 列表失败: %w", err)
	}
	if items == nil {
		items = []model.FleetBotReclaim{}
	}
	return items, nil
}

// Get 按 UUID 取单条。
func (s *BotReclaimService) Get(reclaimUUID string) (*model.FleetBotReclaim, error) {
	var rec model.FleetBotReclaim
	if err := s.db.Where("uuid = ?", reclaimUUID).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotReclaimNotFound
		}
		return nil, fmt.Errorf("查询失效 Bot 记录失败: %w", err)
	}
	return &rec, nil
}

// ConfirmReclaim 管理员手动确认回收（auto 关闭或 V1 场景的主路径）。
func (s *BotReclaimService) ConfirmReclaim(reclaimUUID string, userID uint, ip string) (*model.FleetBotReclaim, error) {
	rec, err := s.Get(reclaimUUID)
	if err != nil {
		return nil, err
	}
	if rec.Status != model.FleetBotReclaimPending && rec.Status != model.FleetBotReclaimConfirmed {
		return nil, ErrBotReclaimNotActive
	}
	if err := s.disposeOne(context.Background(), rec, "manual", userID, ip); err != nil {
		return rec, err
	}
	return s.Get(reclaimUUID)
}

func botReclaimNodeID(bot *model.Bot) uint {
	if bot.ExecutorNodeID == nil {
		return 0
	}
	return *bot.ExecutorNodeID
}

func botReclaimSessionID(bot *model.Bot) uint {
	if bot.StressSessionID == nil {
		return 0
	}
	return *bot.StressSessionID
}

func botReclaimBatchID(bot *model.Bot) uint {
	if bot.LoadBatchID == nil {
		return 0
	}
	return *bot.LoadBatchID
}

// BotReclaimSweeper 周期性巡检失效 Bot 并自动回收/补足；Stop 后释放 goroutine。
type BotReclaimSweeper struct {
	service  *BotReclaimService
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewBotReclaimSweeper 创建默认 60 秒周期的失效 Bot 巡检器。
func NewBotReclaimSweeper(service *BotReclaimService) *BotReclaimSweeper {
	return &BotReclaimSweeper{service: service, interval: botReclaimSweepInterval}
}

// Start 启动后台巡检；重复 Start 幂等。
func (s *BotReclaimSweeper) Start() {
	if s == nil || s.service == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	interval := s.interval
	if interval <= 0 {
		interval = botReclaimSweepInterval
	}
	go s.loop(ctx, interval, done)
}

func (s *BotReclaimSweeper) loop(ctx context.Context, interval time.Duration, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.service.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("失效 Bot 巡检失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Stop 取消巡检并等待 goroutine 退出。
func (s *BotReclaimSweeper) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
