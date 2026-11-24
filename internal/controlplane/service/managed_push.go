package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 托管配置下发编排。
//
// ## 通道：复用既有插件桥，不新增 RPC
// 下发走 `SendPluginCommand`（`action=config_push`），与治理命令同一条 gRPC + WS 通路。
// 协议帧（`ManagedConfigPush` / `ManagedConfigApplied` / `ManagedConfigState`）全部来自
// 上游 managed_mode_frames.go，本文件只做「搬进搬出」——不重新定义帧，也不改 proto。
//
// ## baseVersion 的来源（最容易接错的一处）
// 上游注释写得很明确：`baseVersion` 是**乐观并发基线**，必须取上一次 `config_applied`
// 回报的版本，而不是 CP 自己的会话计数器。首次下发传 0。
// 所以本文件里 `configVersion`（CP 侧会话序号）与 `baseVersion`（探针侧版本）是两套数：
// 前者写在 `ManagedConfigPush.ConfigVersion`，后者取自「每实例最新状态」表的 config_version
// （或上一次成功下发的 ack 版本）。
//
// ## ACK 超时不是「下发失败」
// 下发 gRPC 返回成功只代表**帧送到了 Worker**。探针是否应用要看 `config_applied`。
// 因此目标被推进到 `pushing` 后进入等待，超时未回执记为 `timeout`——
// 与探针明确回 `failed` 区分开：前者是「不知道」，后者是「知道不行了」。

// 下发编排的默认值与边界。
const (
	// defaultManagedAckTimeout ACK 超时默认 30s（spec 口径）。
	defaultManagedAckTimeout = 30 * time.Second
	// defaultManagedFailureRateThreshold 失败率熔断阈值默认 20%。
	defaultManagedFailureRateThreshold = 20.0
	// minManagedAckTimeout 最短 ACK 超时（再短会把正常的探针磁盘写拖成超时）。
	minManagedAckTimeout = 5 * time.Second
	// maxManagedAckTimeout 最长 ACK 超时（再长会让熔断判定延迟到运维以为卡死）。
	maxManagedAckTimeout = 10 * time.Minute
	// managedPushDispatchTimeout 是单次下发 gRPC 往返超时。
	// 只覆盖「帧送到 Worker」，不含探针应用耗时——后者由 ACK 超时管。
	managedPushDispatchTimeout = 10 * time.Second
	// managedPushTickACL 是等待回执的轮询间隔。
	managedPushTickACL = 500 * time.Millisecond
)

// pluginActionConfigPush 是探针侧的配置下发动作名（协议 §3.4.1）。
const pluginActionConfigPush = "config_push"

// 托管下发相关错误。
var (
	// ErrManagedPushNotFound 下发会话不存在。
	ErrManagedPushNotFound = errors.New("托管下发会话不存在")
	// ErrManagedPushStateInvalid 会话当前状态不允许该操作。
	ErrManagedPushStateInvalid = errors.New("托管下发会话状态不允许该操作")
	// ErrManagedPushNoTargets 没有可下发的目标。
	ErrManagedPushNoTargets = errors.New("没有可下发的目标实例")
	// ErrManagedPushConfirmRequired 回滚必须显式二次确认。
	ErrManagedPushConfirmRequired = errors.New("回滚需要二次确认（不做自动回滚）")
	// ErrManagedPushReasonRequired 回滚必须给出原因。
	ErrManagedPushReasonRequired = errors.New("回滚必须填写原因")
)

// ManagedPushInput 是创建一次下发会话的请求。
type ManagedPushInput struct {
	// Scope/ScopeRefID 针对哪一层下发（global 影响全平台，需谨慎）。
	Scope      model.ManagedConfigScope `json:"scope"`
	ScopeRefID uint                     `json:"scopeRefId"`
	// InstanceIDs 显式目标；为空则按作用域自动展开（探针适用的运行中实例）。
	InstanceIDs []uint `json:"instanceIds"`
	// BatchSize 每批台数；0 = 单批全量。
	BatchSize int `json:"batchSize"`
	// FailureRateThreshold 失败率熔断阈值（百分比）；nil 用默认 20，0 = 关闭熔断。
	FailureRateThreshold *float64 `json:"failureRateThreshold"`
	// AckTimeoutSec ACK 超时（秒）；0 用默认 30。
	AckTimeoutSec int    `json:"ackTimeoutSec"`
	Reason        string `json:"reason"`
}

// ManagedPushTargetView 是目标列表的一行（前端直接渲染）。
type ManagedPushTargetView struct {
	model.ManagedPushTarget
	// BaseVersionIsStale true 表示下发时用的基线已被更高版本覆盖（提示可能版本冲突）。
	BaseVersionIsStale bool `json:"baseVersionIsStale,omitempty"`
}

// ManagedPushSessionDetail 是会话详情（含冻结载荷与进度）。
type ManagedPushSessionDetail struct {
	model.ManagedPushSession
	// Values 冻结的点分键值。
	Values map[string]any `json:"values"`
	// Removals 冻结的删除路径。
	Removals []string `json:"removals"`
	// Traced 冻结的逐键来源（下钻用）。
	Traced []ManagedTracedKey `json:"traced"`
}

// ManagedPushService 托管配置下发编排。
type ManagedPushService struct {
	db      *gorm.DB
	pool    *cpgrpc.ClientPool
	config  *ManagedConfigService
	state   *ManagedInstanceStateService
	audit   *AuditService
	tickACL time.Duration
	// wake 用于在收到 config_applied 时提前唤醒等待中的批推进循环。
	// 没有它的话，回执到了也要等满一个 tick 才被看见——界面上是「点了没反应」。
	wake chan struct{}
	// running 记录本进程内正在推进的会话（防止重复启动同一会话的推进循环）。
	running   map[uint]struct{}
	runningMu sync.Mutex
}

// NewManagedPushService 创建托管下发编排服务。
func NewManagedPushService(
	db *gorm.DB, pool *cpgrpc.ClientPool, config *ManagedConfigService, state *ManagedInstanceStateService,
) *ManagedPushService {
	return &ManagedPushService{
		db:      db,
		pool:    pool,
		config:  config,
		state:   state,
		tickACL: managedPushTickACL,
		wake:    make(chan struct{}, 1),
		running: make(map[uint]struct{}),
	}
}

// SetAudit 注入审计服务（nil 时不写审计，但主流程照常）。
func (s *ManagedPushService) SetAudit(audit *AuditService) { s.audit = audit }

// SetTickForTest 调整等待回执的轮询间隔（仅测试用）。
func (s *ManagedPushService) SetTickForTest(d time.Duration) {
	if d > 0 {
		s.tickACL = d
	}
}

// Create 创建一次下发会话：合并三层 → 预检 → 冻结载荷 → 展开目标 → 落库。
//
// 刻意在创建时就冻结 values/removals：回滚要重推「上一次 completed 推送的冻结快照」，
// 若回滚时重新合并，中间任何人改过层都会让回滚推出一套从未存在过的配置。
func (s *ManagedPushService) Create(in ManagedPushInput, operatorID uint) (*model.ManagedPushSession, error) {
	if !model.ValidManagedConfigScope(in.Scope) {
		return nil, ErrManagedScopeInvalid
	}
	insts, err := s.resolveTargets(in)
	if err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return nil, ErrManagedPushNoTargets
	}

	// 逐实例合并（不同实例的 node/instance 层不同，合并结果也不同）——
	// 因此「下发的 values」实际上是**每台一份**。这里把首台的结果作为会话展示用的代表值，
	// 真正的每台 values 在推送到该台时重新取一次（见 pushOne）。
	first, err := s.config.MergeForInstance(insts[0])
	if err != nil {
		return nil, err
	}
	if len(first.ForbiddenKeys) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrManagedConfigForbidden, strings.Join(first.ForbiddenKeys, "、"))
	}
	if len(first.Values) == 0 && len(first.Removals) == 0 {
		return nil, ErrManagedConfigLayerEmpty
	}

	valuesJSON, err := json.Marshal(first.Values)
	if err != nil {
		return nil, fmt.Errorf("序列化下发载荷失败: %w", err)
	}
	removalsJSON, err := json.Marshal(first.Removals)
	if err != nil {
		return nil, fmt.Errorf("序列化删除清单失败: %w", err)
	}
	tracedJSON, err := json.Marshal(first.Traced)
	if err != nil {
		return nil, fmt.Errorf("序列化逐键来源失败: %w", err)
	}

	ackTimeout := time.Duration(in.AckTimeoutSec) * time.Second
	if ackTimeout <= 0 {
		ackTimeout = defaultManagedAckTimeout
	}
	ackTimeout = clampAckTimeout(ackTimeout)

	threshold := defaultManagedFailureRateThreshold
	if in.FailureRateThreshold != nil {
		threshold = *in.FailureRateThreshold
	}
	if threshold < 0 {
		threshold = 0
	}

	batchSize := in.BatchSize
	if batchSize < 0 {
		batchSize = 0
	}

	session := model.ManagedPushSession{
		Kind:                 model.ManagedPushKindPush,
		Scope:                in.Scope,
		ScopeRefID:           in.ScopeRefID,
		ScopeName:            resolveManagedScopeName(s.db, in.Scope, in.ScopeRefID),
		ValuesJSON:           string(valuesJSON),
		RemovalsJSON:         string(removalsJSON),
		TracedJSON:           string(tracedJSON),
		Reason:               strings.TrimSpace(in.Reason),
		BatchSize:            batchSize,
		FailureRateThreshold: threshold,
		AckTimeoutSec:        int(ackTimeout / time.Second),
		Status:               model.ManagedPushRunning,
		PlannedCount:         len(insts),
		PendingCount:         len(insts),
		CreatedBy:            operatorID,
		TotalBatches:         computeManagedBatches(len(insts), batchSize),
		CurrentBatch:         1,
	}

	err = s.createSessionTx(&session, func(tx *gorm.DB) error {
		for _, inst := range insts {
			target := model.ManagedPushTarget{
				SessionID:    session.ID,
				InstanceID:   inst.ID,
				InstanceUUID: inst.UUID,
				InstanceName: inst.Name,
				NodeID:       inst.NodeID,
				BatchNo:      managedBatchOf(targetIndexByID(insts, inst.ID), batchSize),
				State:        model.ManagedPushTargetPending,
			}
			if terr := tx.Create(&target).Error; terr != nil {
				return fmt.Errorf("创建下发目标失败: %w", terr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.recordAudit(operatorID, "managed_config.push.create", session.ID,
		fmt.Sprintf("scope=%s/%d targets=%d batch=%d threshold=%.0f%%",
			session.Scope, session.ScopeRefID, session.PlannedCount, batchSize, threshold))

	// 立即推进首批（异步）：界面提交后应当马上看到进度，而不是等人再点一次「开始」。
	s.Start(session.ID)
	return &session, nil
}

// createSessionTx 在事务里铸造全局递增的 ConfigVersion 并写入会话 + 目标行。
//
// 为何要重试而不是裸 MAX+1：config_version 有唯一索引，两个并发会话可能同时读到
// 同一个 MAX，一个提交成功另一个撞唯一约束——撞号不是业务错误，重读 MAX 再插即可。
func (s *ManagedPushService) createSessionTx(session *model.ManagedPushSession, fillTargets func(tx *gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.db.Transaction(func(tx *gorm.DB) error {
			// ConfigVersion 会话级单调递增：取当前最大值 +1。
			// 注意它**不是**探针的版本（那是 baseVersion），两者语义不同、不可互换。
			var maxVersion int64
			if qerr := tx.Model(&model.ManagedPushSession{}).
				Select("COALESCE(MAX(config_version), 0)").Scan(&maxVersion).Error; qerr != nil {
				return fmt.Errorf("查询配置版本序号失败: %w", qerr)
			}
			session.ConfigVersion = maxVersion + 1
			if cerr := tx.Create(session).Error; cerr != nil {
				return cerr
			}
			return fillTargets(tx)
		})
		if !isManagedVersionConflict(err) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("创建下发会话失败: %w", err)
	}
	return nil
}

// isManagedVersionConflict 判断错误是否为 config_version 唯一约束冲突（跨方言按错误文本判定）。
func isManagedVersionConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "config_version") &&
		(strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate") || strings.Contains(msg, "constraint"))
}

// Start 启动（或继续）会话的推进循环。
//
// 幂等：同一会话已在推进时直接返回，避免前端重复点「继续」起两条循环对同一批下发两次。
func (s *ManagedPushService) Start(sessionID uint) {
	s.runningMu.Lock()
	if _, busy := s.running[sessionID]; busy {
		s.runningMu.Unlock()
		return
	}
	s.running[sessionID] = struct{}{}
	s.runningMu.Unlock()

	go func() {
		defer func() {
			s.runningMu.Lock()
			delete(s.running, sessionID)
			s.runningMu.Unlock()
		}()
		if err := s.run(sessionID); err != nil {
			slog.Warn("托管配置下发推进中止", "session", sessionID, "err", err)
		}
	}()
}

// run 推进会话直到终态/暂停/等待放行。
func (s *ManagedPushService) run(sessionID uint) error {
	for {
		session, err := s.Get(sessionID)
		if err != nil {
			return err
		}
		if session.Status.IsTerminal() || session.Status == model.ManagedPushPaused {
			return nil
		}

		// 先看当前批是否还有未终结目标；都终结了就结算并等人工放行下一批。
		pending, err := s.pendingTargets(sessionID, session.CurrentBatch)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			next, err := s.finishBatch(session)
			if err != nil {
				return err
			}
			if !next {
				return nil
			}
			continue
		}

		// 逐个下发（批内并发由 ACK 阶段承担，这里只负责把帧送出去）。
		for _, target := range pending {
			if err := s.pushOne(session, target); err != nil {
				// 单台下发失败不终止整批：目标是逐台终结的，失败率由熔断阈值管。
				slog.Debug("托管配置下发单台失败", "session", sessionID, "instance", target.InstanceID, "err", err)
			}
		}

		// 等待本批回执（或超时）。
		if err := s.waitBatch(session); err != nil {
			return err
		}
	}
}

// waitBatch 等当前批全部终结或到达 ACK 超时。
func (s *ManagedPushService) waitBatch(session *model.ManagedPushSession) error {
	timeout := time.Duration(session.AckTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultManagedAckTimeout
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(s.tickACL)
	defer ticker.Stop()

	for {
		live, err := s.pendingTargets(session.ID, session.CurrentBatch)
		if err != nil {
			return err
		}
		if len(live) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			// 超时：把还在 pushing 的标成 timeout。
			// 只动 pushing，不动 pending——pending 说明这一批还没轮到下发（不会发生在同批内，
			// 但保守起见不做越界改写）。
			return s.expireStaleTargets(session)
		}
		select {
		case <-ticker.C:
		case <-s.wake:
			// 回执到了，立刻重查，不必等满一个 tick。
		case <-time.After(time.Until(deadline)):
			// 兜底：ticker 被调度延迟时不至于睡过头。
		}
	}
}

// pendingTargets 取某批里尚未终结的目标。
func (s *ManagedPushService) pendingTargets(sessionID uint, batchNo int) ([]model.ManagedPushTarget, error) {
	var rows []model.ManagedPushTarget
	err := s.db.Where("session_id = ? AND batch_no = ? AND state IN ?", sessionID, batchNo,
		[]model.ManagedPushTargetState{model.ManagedPushTargetPending, model.ManagedPushTargetPushing}).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询待下发目标失败: %w", err)
	}
	return rows, nil
}

// expireStaleTargets 把本批仍处 pushing 的目标判为 ACK 超时。
func (s *ManagedPushService) expireStaleTargets(session *model.ManagedPushSession) error {
	var rows []model.ManagedPushTarget
	err := s.db.Where("session_id = ? AND batch_no = ? AND state = ?",
		session.ID, session.CurrentBatch, model.ManagedPushTargetPushing).Find(&rows).Error
	if err != nil {
		return fmt.Errorf("查询超时目标失败: %w", err)
	}
	now := time.Now()
	for _, target := range rows {
		res := s.db.Model(&model.ManagedPushTarget{}).
			Where("id = ? AND state = ?", target.ID, model.ManagedPushTargetPushing).
			Updates(map[string]any{
				"state":       model.ManagedPushTargetTimeout,
				"error_kind":  model.ManagedTargetCodeAckTimeout,
				"error":       fmt.Sprintf("%d 秒内未收到探针 config_applied 回执", session.AckTimeoutSec),
				"finished_at": now,
			})
		if res.Error != nil {
			return fmt.Errorf("标记目标超时失败: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			s.recordAudit(0, "managed_config.push.timeout", session.ID,
				fmt.Sprintf("instance=%d 未在 %ds 内回执", target.InstanceID, session.AckTimeoutSec))
		}
	}
	return s.refreshCounters(session)
}

// finishBatch 结算当前批：刷新计数 → 熔断判定 → 决定是等放行还是进入下一批。
//
// 返回 true 表示「已有下一批可以立刻推进」（单批全量模式），false 表示停在这里等人工放行
// 或会话已终结。
func (s *ManagedPushService) finishBatch(session *model.ManagedPushSession) (bool, error) {
	if err := s.refreshCounters(session); err != nil {
		return false, err
	}
	fresh, err := s.Get(session.ID)
	if err != nil {
		return false, err
	}

	// 熔断判定：分母是**已终结目标**（不含 skipped）。离线跳过不是「配置推坏了」。
	tripped, observed, err := s.evaluateCircuitBreak(fresh)
	if err != nil {
		return false, err
	}
	if tripped {
		reason := fmt.Sprintf("失败率 %.1f%% 超过阈值 %.1f%%（本会话已终结的失败目标）",
			observed, fresh.FailureRateThreshold)
		// 熔断落 `failed`（终态），不是 `paused`。
		//
		// 两者都能「不再推进」，但只有 failed 表达了「本轮已经不该继续」——
		// paused 是可 Resume 的，而熔断的语义是现场保留、等人工判断是否要改动后重建会话。
		// 允许一键 Resume 会绕过那次人工判断，把触发熔断的那个配置直接推给剩余批次。
		if err := s.failByCircuitBreak(fresh.ID, reason, fresh.FailureRateThreshold, observed); err != nil {
			return false, err
		}
		s.recordAudit(0, "managed_config.push.circuit_break", fresh.ID, reason)
		return false, nil
	}

	// 还有下一批：单批全量模式（batchSize=0）会一次装完，故此时不会有下一批。
	if fresh.CurrentBatch >= fresh.TotalBatches {
		return false, s.completeFresh(fresh.ID)
	}

	// 进下一批：台数是单批全量时不会走到这里，故一律要求人工放行。
	// 逐批人工放行是刻意的：探针配置一旦推错会影响指标精度甚至采集频率，
	// 自动推进会让「先看一批效果」变成空话。
	if err := s.db.Model(&model.ManagedPushSession{}).
		Where("id = ? AND status = ?", fresh.ID, model.ManagedPushRunning).
		Updates(map[string]any{"status": model.ManagedPushAwaitingRelease}).Error; err != nil {
		return false, fmt.Errorf("标记等待放行失败: %w", err)
	}
	return false, nil
}

// Release 人工放行下一批。
func (s *ManagedPushService) Release(sessionID uint) (*model.ManagedPushSession, error) {
	session, err := s.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if session.Status != model.ManagedPushAwaitingRelease {
		return nil, fmt.Errorf("%w: 当前状态 %s，只有等待放行的会话可以放行下一批",
			ErrManagedPushStateInvalid, session.Status)
	}
	if session.CurrentBatch >= session.TotalBatches {
		return nil, fmt.Errorf("%w: 已是最后一批", ErrManagedPushStateInvalid)
	}
	next := session.CurrentBatch + 1
	res := s.db.Model(&model.ManagedPushSession{}).
		Where("id = ? AND status = ?", sessionID, model.ManagedPushAwaitingRelease).
		Updates(map[string]any{
			"status":        model.ManagedPushRunning,
			"current_batch": next,
			"pause_kind":    "",
			"pause_reason":  "",
		})
	if res.Error != nil {
		return nil, fmt.Errorf("放行下一批失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: 会话状态已被其他操作改变", ErrManagedPushStateInvalid)
	}
	s.recordAudit(0, "managed_config.push.release", sessionID, fmt.Sprintf("放行第 %d 批", next))
	s.Start(sessionID)
	return s.Get(sessionID)
}

// Pause 人工暂停。
func (s *ManagedPushService) Pause(sessionID uint, reason string) (*model.ManagedPushSession, error) {
	session, err := s.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if session.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: 会话已终结", ErrManagedPushStateInvalid)
	}
	if session.Status == model.ManagedPushPaused {
		return s.Get(sessionID)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "人工暂停"
	}
	if err := s.pause(sessionID, model.ManagedPushPauseManual, reason); err != nil {
		return nil, err
	}
	s.recordAudit(0, "managed_config.push.pause", sessionID, reason)
	return s.Get(sessionID)
}

// Resume 从**人工暂停**恢复。
//
// 恢复后**重试本批未终结的目标**：暂停时可能有目标还停在 pending。
//
// 熔断不能走这条：熔断是终态（`failByCircuitBreak` 置 failed），恢复只认 paused。
// 这是刻意的——熔断说明「按当前配置继续推是危险的」，要推进应改动配置后重建会话，
// 而不是把同一份配置一键续推给剩余批次。`break_*` 现场在恢复后仍保留，便于事后核对。
func (s *ManagedPushService) Resume(sessionID uint) (*model.ManagedPushSession, error) {
	session, err := s.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if session.Status != model.ManagedPushPaused {
		return nil, fmt.Errorf("%w: 当前状态 %s，只有暂停的会话可以恢复",
			ErrManagedPushStateInvalid, session.Status)
	}
	res := s.db.Model(&model.ManagedPushSession{}).
		Where("id = ? AND status = ?", sessionID, model.ManagedPushPaused).
		Updates(map[string]any{
			"status":       model.ManagedPushRunning,
			"pause_kind":   "",
			"pause_reason": "",
			// 保留 break_* 现场：恢复不等于抹掉「上次为什么停」，运维还要看得到。
		})
	if res.Error != nil {
		return nil, fmt.Errorf("恢复会话失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: 会话状态已被其他操作改变", ErrManagedPushStateInvalid)
	}
	s.recordAudit(0, "managed_config.push.resume", sessionID, "")
	s.Start(sessionID)
	return s.Get(sessionID)
}

// Cancel 取消会话。
func (s *ManagedPushService) Cancel(sessionID uint, reason string) (*model.ManagedPushSession, error) {
	session, err := s.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if session.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: 会话已终结", ErrManagedPushStateInvalid)
	}
	now := time.Now()
	if err := s.db.Model(&model.ManagedPushSession{}).
		Where("id = ?", sessionID).
		Updates(map[string]any{
			"status":        model.ManagedPushCancelled,
			"pause_reason":  strings.TrimSpace(reason),
			"finished_at":   now,
			"pending_count": 0,
		}).Error; err != nil {
		return nil, fmt.Errorf("取消会话失败: %w", err)
	}
	// 未终结的目标标 skipped（取消不是失败，不该计入失败率历史）。
	if err := s.db.Model(&model.ManagedPushTarget{}).
		Where("session_id = ? AND state IN ?", sessionID,
			[]model.ManagedPushTargetState{model.ManagedPushTargetPending, model.ManagedPushTargetPushing}).
		Updates(map[string]any{
			"state":       model.ManagedPushTargetSkipped,
			"error_kind":  model.ManagedTargetCodeCancelledByUser,
			"error":       "会话被人工取消",
			"finished_at": now,
		}).Error; err != nil {
		return nil, fmt.Errorf("标记取消目标失败: %w", err)
	}
	if err := s.refreshCountersByID(sessionID); err != nil {
		return nil, err
	}
	s.recordAudit(0, "managed_config.push.cancel", sessionID, strings.TrimSpace(reason))
	return s.Get(sessionID)
}

// ---------------------------------------------------------------------------
// 下发与回执
// ---------------------------------------------------------------------------

// baseVersionFor 取该实例应当使用的乐观并发基线。
//
// 优先级（两处都是「探针自己报过的版本」，绝不使用 CP 的会话计数器）：
//  1. 每实例最新状态表的 config_version（config_state 上报，最新鲜）；
//  2. 该实例上一次成功下发（applied）目标的 ack 版本；
//  3. 都没有 → 0（首次下发）。
func (s *ManagedPushService) baseVersionFor(inst model.Instance) int64 {
	if s.state != nil {
		if st, ok, err := s.state.Get(inst.ID); err == nil && ok {
			return st.ConfigVersion
		}
	}
	var last model.ManagedPushTarget
	err := s.db.Where("instance_id = ? AND state = ? AND ack_version IS NOT NULL",
		inst.ID, model.ManagedPushTargetApplied).
		Order("id DESC").First(&last).Error
	if err == nil && last.AckVersion != nil {
		return *last.AckVersion
	}
	return 0
}

// pushOne 向一台实例下发 config_push。
//
// 流程：取合并结果 → 组装上游帧 → 经 SendPluginCommand 下发 → 目标推进到 pushing。
// **不在这里等回执**：回执由 ConsumeConfigApplied 异步写回（探针可能几秒后才应用完）。
func (s *ManagedPushService) pushOne(session *model.ManagedPushSession, target model.ManagedPushTarget) error {
	var inst model.Instance
	if err := s.db.First(&inst, target.InstanceID).Error; err != nil {
		s.finishTarget(target, model.ManagedPushTargetSkipped,
			model.ManagedTargetCodeInstanceOffline, "实例不存在，已跳过")
		return nil
	}
	// 不适用探针 / 未运行：显式跳过，不占用下发名额也不计入失败率。
	if !model.IsProbeApplicable(inst.Type, inst.Role) {
		s.finishTarget(target, model.ManagedPushTargetSkipped,
			model.ManagedTargetCodeProbeNotApplicable, "该实例不适用 ServerProbe（代理/Beacon/二进制），已跳过")
		return nil
	}
	if inst.Status != model.InstanceStatusRunning {
		s.finishTarget(target, model.ManagedPushTargetSkipped,
			model.ManagedTargetCodeInstanceOffline, fmt.Sprintf("实例未运行（当前 %s），已跳过", inst.Status))
		return nil
	}

	// 载荷来源分两种，必须分清：
	//   - 回滚会话：重推源会话**当时对这台机器实际发出去的那份配置内容**（PushedPayloadJSON）。
	//     不能重新合并——两次推送之间只要有人改过 global 层，重新合并出的就是一份
	//     从未在任何机器上生效过的配置，而「回到上一次推送的状态」这句话也就不成立了。
	//   - 常规会话：按该实例的 node/instance 层现场合并（各台不同，不能复用会话级代表值）。
	//
	// 注意回滚只复用**内容**（values/removals），不复用版本号：configVersion 必须是本次
	// 回滚会话新铸的版本，baseVersion 必须是该实例当前的真实基线。若把源载荷的旧版本号
	// 原样发出去，探针会按旧版本回执，而 CP 正按新版本等它——回执永远对不上，最终全批超时。
	var raw []byte
	base := s.baseVersionFor(inst)
	if session.Kind == model.ManagedPushKindRollback && target.PushedPayloadJSON != "" {
		var frozen ManagedConfigPush
		if uerr := json.Unmarshal([]byte(target.PushedPayloadJSON), &frozen); uerr != nil {
			s.finishTarget(target, model.ManagedPushTargetFailed,
				model.ManagedTargetCodeApplyFailed, "回滚载荷损坏: "+uerr.Error())
			return nil
		}
		payload := ManagedConfigPush{
			ConfigVersion: session.ConfigVersion,
			BaseVersion:   base,
			Values:        frozen.Values,
			Removals:      frozen.Removals,
			Reason:        session.Reason,
		}
		if payload.Values == nil {
			payload.Values = map[string]any{}
		}
		built, berr := payload.BuildConfigPushPayload()
		if berr != nil {
			s.finishTarget(target, model.ManagedPushTargetFailed,
				model.ManagedTargetCodeApplyFailed, berr.Error())
			return nil
		}
		raw = built
	} else {
		merged, err := s.config.MergeForInstance(inst)
		if err != nil {
			s.finishTarget(target, model.ManagedPushTargetFailed,
				model.ManagedTargetCodeApplyFailed, "合并配置层失败: "+err.Error())
			return nil
		}
		if len(merged.ForbiddenKeys) > 0 {
			s.finishTarget(target, model.ManagedPushTargetRejected,
				model.ManagedTargetCodeRejected, "含禁止远程写入的键: "+strings.Join(merged.ForbiddenKeys, "、"))
			return nil
		}

		payload := ManagedConfigPush{
			ConfigVersion: session.ConfigVersion,
			BaseVersion:   base,
			Values:        merged.Values,
			Removals:      merged.Removals,
			Reason:        session.Reason,
		}
		if payload.Values == nil {
			payload.Values = map[string]any{}
		}
		// 下发前复校：上游的禁档校验是最后一道闸，宁可在这里拒也不让探针整包拒。
		if v := ValidateConfigPush(payload); !v.OK() {
			s.finishTarget(target, model.ManagedPushTargetRejected,
				model.ManagedTargetCodeRejected, v.Error())
			return nil
		}

		built, berr := payload.BuildConfigPushPayload()
		if berr != nil {
			s.finishTarget(target, model.ManagedPushTargetFailed,
				model.ManagedTargetCodeApplyFailed, berr.Error())
			return nil
		}
		raw = built
	}

	// 标记 pushing + 记录本次用的基线（回执到达时要按基线判定冲突）。
	// 同时留存载荷原文——下一次回滚要重推的就是它。
	now := time.Now()
	res := s.db.Model(&model.ManagedPushTarget{}).
		Where("id = ? AND state = ?", target.ID, model.ManagedPushTargetPending).
		Updates(map[string]any{
			"state":               model.ManagedPushTargetPushing,
			"base_version":        base,
			"desired_version":     session.ConfigVersion,
			"pushed_at":           now,
			"pushed_payload_json": string(raw),
			"attempts":            gorm.Expr("attempts + 1"),
		})
	if res.Error != nil {
		return fmt.Errorf("标记下发中失败: %w", res.Error)
	}

	if err := s.sendConfigPush(inst, string(raw)); err != nil {
		// 通道层面就送不到（节点未连/探针未连）：这不是探针拒绝，是「没送到」。
		s.finishTarget(target, model.ManagedPushTargetFailed,
			model.ManagedTargetCodeProbeUnreachable, err.Error())
		return nil
	}
	return nil
}

// sendConfigPush 经既有 SendPluginCommand 下发一帧 config_push（wait=false）。
//
// 用 `wait=false` 而不是 true：探针的 `config_applied` 是**异步**回执（应用可能跨越多个 tick），
// 同步等待会把它拖成 gRPC 超时，于是「已应用」被误报成「未回执」。
// 真正的确认走 ConsumeConfigApplied。
func (s *ManagedPushService) sendConfigPush(inst model.Instance, payloadJSON string) error {
	if s.pool == nil {
		return errors.New("节点连接池未就绪")
	}
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		return errors.New("节点不存在")
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return errors.New("节点未连接")
	}

	ctx, cancel := context.WithTimeout(context.Background(), managedPushDispatchTimeout)
	defer cancel()
	resp, err := client.Worker.SendPluginCommand(ctx, &workerpb.SendPluginCommandRequest{
		InstanceUuid: inst.UUID,
		Command: &workerpb.PluginCommand{
			Action:      pluginActionConfigPush,
			PayloadJson: payloadJSON,
			RequestId:   uuid.NewString(),
			Reason:      "托管配置下发",
		},
		Wait: false,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return errors.New("下发超时")
		}
		return fmt.Errorf("下发调用失败: %w", err)
	}
	if resp != nil && !resp.Success {
		msg := strings.TrimSpace(resp.Error)
		if msg == "" {
			msg = "探针未连接"
		}
		return errors.New(msg)
	}
	return nil
}

// finishTarget 终结一个目标并刷新会话计数。
func (s *ManagedPushService) finishTarget(target model.ManagedPushTarget, state model.ManagedPushTargetState, code, message string) {
	now := time.Now()
	if err := s.db.Model(&model.ManagedPushTarget{}).Where("id = ?", target.ID).
		Updates(map[string]any{
			"state":       state,
			"error_kind":  code,
			"error":       truncateManagedText(message, 1000),
			"finished_at": now,
		}).Error; err != nil {
		slog.Warn("终结下发目标失败", "target", target.ID, "err", err)
	}
	if err := s.refreshCountersByID(target.SessionID); err != nil {
		slog.Warn("刷新下发计数失败", "session", target.SessionID, "err", err)
	}
	s.signalWake()
}

// ConsumeConfigApplied 消费一条 `config_applied` 回执，更新目标状态。
//
// ## 匹配规则（按优先级，取最新的一条待回执目标）
// 探针的回执只带 configVersion，不带 CP 的 session/target id。因此按
// 「该实例 → pushing 目标 → configVersion 匹配」定位；找不到匹配时**丢弃并记日志**，
// 而不是随便挑一条改掉——错改一条会让对账矩阵永久说谎。
func (s *ManagedPushService) ConsumeConfigApplied(inst model.Instance, applied ManagedConfigApplied) {
	var target model.ManagedPushTarget
	err := s.db.Where("instance_id = ? AND state = ? AND desired_version = ?",
		inst.ID, model.ManagedPushTargetPushing, applied.ConfigVersion).
		Order("id DESC").First(&target).Error
	if err != nil {
		// 可能是超时后才到达的回执（目标已变 timeout）——仍要把它记下来供矩阵解释。
		var late model.ManagedPushTarget
		if lerr := s.db.Where("instance_id = ? AND desired_version = ?", inst.ID, applied.ConfigVersion).
			Order("id DESC").First(&late).Error; lerr == nil && late.State == model.ManagedPushTargetTimeout {
			target = late
		} else {
			slog.Debug("托管配置回执无匹配目标，已忽略",
				"instance", inst.UUID, "configVersion", applied.ConfigVersion, "result", applied.Result)
			return
		}
	}

	now := time.Now()
	updates := map[string]any{"finished_at": now}
	switch applied.Result {
	case ConfigApplyResultApplied:
		version := applied.ConfigVersion
		updates["state"] = model.ManagedPushTargetApplied
		updates["ack_version"] = version
		updates["error"] = ""
		updates["error_kind"] = ""
		if raw, err := json.Marshal(applied.Effective); err == nil {
			updates["effective_json"] = string(raw)
		}
	case ConfigApplyResultRejected:
		// `conflict` 不是 result 取值，它是 rejected 的 error 前缀。
		// 判错会导致「版本冲突」被当成「协议错误」处理——两者的运维动作完全不同。
		code := model.ManagedTargetCodeRejected
		if isManagedConflictError(applied.Error) {
			code = model.ManagedTargetCodeVersionConflict
		}
		updates["state"] = model.ManagedPushTargetRejected
		updates["error_kind"] = code
		updates["error"] = truncateManagedText(applied.Error, 1000)
	default: // failed 及未知取值一律按失败处理（宁可报错也不假装成功）
		updates["state"] = model.ManagedPushTargetFailed
		updates["error_kind"] = model.ManagedTargetCodeApplyFailed
		updates["error"] = truncateManagedText(defaultString(applied.Error, "探针未给出失败原因"), 1000)
	}

	if err := s.db.Model(&model.ManagedPushTarget{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
		slog.Warn("写入下发回执失败", "target", target.ID, "err", err)
		return
	}
	if err := s.refreshCountersByID(target.SessionID); err != nil {
		slog.Warn("刷新下发计数失败", "session", target.SessionID, "err", err)
	}
	s.signalWake()
}

// ConsumeConfigState 消费一条 `config_state` 周期上报，落「每实例最新状态」。
func (s *ManagedPushService) ConsumeConfigState(inst model.Instance, state ManagedConfigState) {
	if s.state == nil {
		return
	}
	if err := s.state.UpsertFromReport(inst, state); err != nil {
		slog.Warn("写入托管状态失败", "instance", inst.UUID, "err", err)
	}
}

// ---------------------------------------------------------------------------
// 回滚
// ---------------------------------------------------------------------------

// ManagedRollbackInput 是回滚请求。
type ManagedRollbackInput struct {
	// SourceSessionID 被回滚的那次 completed 会话。
	SourceSessionID uint `json:"sourceSessionId"`
	// InstanceIDs 子集（空 = 整批）。
	InstanceIDs []uint `json:"instanceIds"`
	// Reason 回滚原因（必填）。
	Reason string `json:"reason"`
	// Confirmed 二次确认（必须为 true）。
	Confirmed bool `json:"confirmed"`
}

// Rollback 重推上一次 completed 会话的**冻结快照**。
//
// ## 为什么必须是冻结快照而不是「按现在的层重新合并」
// 回滚的语义是「回到那时那套配置」。若重新合并，中间任何人改过层都会让回滚推出一套
// 从未在任何时刻存在过的配置——运维看到的「已回滚」与真实状态不符。
//
// ## 为什么不做自动回滚
// 探针配置错可能只是「指标不准」，而自动回滚会在没有任何人决策的情况下再次改动全平台。
// 因此必须是「原因 + 二次确认」的显式动作，且失败率熔断仍然生效。
func (s *ManagedPushService) Rollback(in ManagedRollbackInput, operatorID uint) (*model.ManagedPushSession, error) {
	if !in.Confirmed {
		return nil, ErrManagedPushConfirmRequired
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, ErrManagedPushReasonRequired
	}
	source, err := s.Get(in.SourceSessionID)
	if err != nil {
		return nil, err
	}
	if source.Status != model.ManagedPushCompleted {
		return nil, fmt.Errorf("%w: 只能回滚已完成的会话（当前 %s）",
			ErrManagedPushStateInvalid, source.Status)
	}

	// 目标集合：源会话的目标（可按 instanceIds 收窄）。
	q := s.db.Where("session_id = ?", source.ID)
	if len(in.InstanceIDs) > 0 {
		q = q.Where("instance_id IN ?", in.InstanceIDs)
	}
	var sourceTargets []model.ManagedPushTarget
	if err := q.Order("id ASC").Find(&sourceTargets).Error; err != nil {
		return nil, fmt.Errorf("查询源会话目标失败: %w", err)
	}
	if len(sourceTargets) == 0 {
		return nil, ErrManagedPushNoTargets
	}

	now := time.Now()
	session := model.ManagedPushSession{
		Kind: model.ManagedPushKindRollback,
		// 冻结源会话的载荷：values/removals/traced 原样搬过来，一个字节都不重算。
		Scope:                source.Scope,
		ScopeRefID:           source.ScopeRefID,
		ScopeName:            source.ScopeName,
		ValuesJSON:           source.ValuesJSON,
		RemovalsJSON:         source.RemovalsJSON,
		TracedJSON:           source.TracedJSON,
		Reason:               reason,
		BatchSize:            0, // 回滚一次全量：分批会延长「配置不一致」的窗口
		FailureRateThreshold: source.FailureRateThreshold,
		AckTimeoutSec:        source.AckTimeoutSec,
		Status:               model.ManagedPushRunning,
		PlannedCount:         len(sourceTargets),
		PendingCount:         len(sourceTargets),
		TotalBatches:         1,
		CurrentBatch:         1,
		SourceSessionID:      &source.ID,
		CreatedBy:            operatorID,
		StartedAt:            &now,
	}

	err = s.createSessionTx(&session, func(tx *gorm.DB) error {
		for _, src := range sourceTargets {
			target := model.ManagedPushTarget{
				SessionID:    session.ID,
				InstanceID:   src.InstanceID,
				InstanceUUID: src.InstanceUUID,
				InstanceName: src.InstanceName,
				NodeID:       src.NodeID,
				BatchNo:      1,
				State:        model.ManagedPushTargetPending,
				// 带上源目标**当时实际发出去**的载荷：回滚要重推的就是它。
				// 空值说明该目标从未真正下发过（pending/skipped），此时回滚按现场重新合并，
				// 与「没推过的东西无所谓回到哪个状态」一致。
				PushedPayloadJSON: src.PushedPayloadJSON,
			}
			if terr := tx.Create(&target).Error; terr != nil {
				return fmt.Errorf("创建回滚目标失败: %w", terr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.recordAudit(operatorID, "managed_config.push.rollback", session.ID,
		fmt.Sprintf("source=%d targets=%d reason=%s", source.ID, len(sourceTargets), reason))
	s.Start(session.ID)
	return &session, nil
}

// ---------------------------------------------------------------------------
// 查询与结算
// ---------------------------------------------------------------------------

// Get 读一个会话（含解码后的冻结载荷）。
func (s *ManagedPushService) Get(sessionID uint) (*model.ManagedPushSession, error) {
	var session model.ManagedPushSession
	err := s.db.First(&session, sessionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrManagedPushNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询下发会话失败: %w", err)
	}
	return &session, nil
}

// Detail 读会话详情（含冻结载荷）。
func (s *ManagedPushService) Detail(sessionID uint) (*ManagedPushSessionDetail, error) {
	session, err := s.Get(sessionID)
	if err != nil {
		return nil, err
	}
	out := &ManagedPushSessionDetail{ManagedPushSession: *session, Removals: []string{}, Traced: []ManagedTracedKey{}}
	if session.ValuesJSON != "" {
		if jerr := json.Unmarshal([]byte(session.ValuesJSON), &out.Values); jerr != nil {
			out.Values = map[string]any{}
		}
	}
	if out.Values == nil {
		out.Values = map[string]any{}
	}
	if session.RemovalsJSON != "" {
		_ = json.Unmarshal([]byte(session.RemovalsJSON), &out.Removals)
	}
	if session.TracedJSON != "" {
		_ = json.Unmarshal([]byte(session.TracedJSON), &out.Traced)
	}
	return out, nil
}

// List 分页列出会话。
func (s *ManagedPushService) List(status, keyword string, page, pageSize int) ([]model.ManagedPushSession, int64, error) {
	q := s.db.Model(&model.ManagedPushSession{})
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("scope_name LIKE ? OR reason LIKE ? OR operator LIKE ?", like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计下发会话失败: %w", err)
	}
	page, pageSize = normalizeManagedPage(page, pageSize)
	var rows []model.ManagedPushSession
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("查询下发会话失败: %w", err)
	}
	return rows, total, nil
}

// Targets 分页列出某会话的目标。
func (s *ManagedPushService) Targets(sessionID uint, state string, batchNo, page, pageSize int) ([]ManagedPushTargetView, int64, error) {
	q := s.db.Model(&model.ManagedPushTarget{}).Where("session_id = ?", sessionID)
	if state = strings.TrimSpace(state); state != "" {
		q = q.Where("state = ?", state)
	}
	if batchNo > 0 {
		q = q.Where("batch_no = ?", batchNo)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计下发目标失败: %w", err)
	}
	page, pageSize = normalizeManagedPage(page, pageSize)
	var rows []model.ManagedPushTarget
	if err := q.Order("batch_no ASC, id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("查询下发目标失败: %w", err)
	}
	out := make([]ManagedPushTargetView, 0, len(rows))
	for _, row := range rows {
		out = append(out, ManagedPushTargetView{ManagedPushTarget: row})
	}
	return out, total, nil
}

// TargetInstanceIDs 返回会话全部目标的实例 ID（回滚前的可见性整包校验用）。
func (s *ManagedPushService) TargetInstanceIDs(sessionID uint) ([]uint, error) {
	var ids []uint
	if err := s.db.Model(&model.ManagedPushTarget{}).
		Where("session_id = ?", sessionID).
		Order("id ASC").
		Pluck("instance_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("查询会话目标实例失败: %w", err)
	}
	return ids, nil
}

// refreshCounters 用会话 ID 刷新计数。
func (s *ManagedPushService) refreshCounters(session *model.ManagedPushSession) error {
	if err := s.refreshCountersByID(session.ID); err != nil {
		return err
	}
	fresh, err := s.Get(session.ID)
	if err != nil {
		return err
	}
	*session = *fresh
	return nil
}

// refreshCountersByID 重新统计目标状态并写回会话。
//
// 每次目标状态变化都重算而不是「增量加减」：增量加减在并发回执下极易算错，
// 而目标表规模有限（一次会话的目标数就是实例数），全量重算的代价可接受且永远正确。
func (s *ManagedPushService) refreshCountersByID(sessionID uint) error {
	var counts []struct {
		State string
		Count int
	}
	if err := s.db.Model(&model.ManagedPushTarget{}).
		Select("state, COUNT(*) AS count").
		Where("session_id = ?", sessionID).
		Group("state").Scan(&counts).Error; err != nil {
		return fmt.Errorf("统计下发目标状态失败: %w", err)
	}
	byState := make(map[string]int, len(counts))
	for _, row := range counts {
		byState[row.State] = row.Count
	}
	updates := map[string]any{
		"applied_count": byState[string(model.ManagedPushTargetApplied)],
		"failed_count": byState[string(model.ManagedPushTargetFailed)] +
			byState[string(model.ManagedPushTargetRejected)],
		"timeout_count": byState[string(model.ManagedPushTargetTimeout)],
		"skipped_count": byState[string(model.ManagedPushTargetSkipped)],
		"pending_count": byState[string(model.ManagedPushTargetPending)] +
			byState[string(model.ManagedPushTargetPushing)],
	}
	if err := s.db.Model(&model.ManagedPushSession{}).Where("id = ?", sessionID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("更新下发计数失败: %w", err)
	}
	return nil
}

// evaluateCircuitBreak 判定失败率是否触阈。
//
// 分母是**已终结目标**（含失败/拒绝/超时，**不含 skipped**）。
// 把离线跳过的机器算进分母会稀释失败率，让真正推坏的配置一直达不到阈值。
func (s *ManagedPushService) evaluateCircuitBreak(session *model.ManagedPushSession) (bool, float64, error) {
	if session.FailureRateThreshold <= 0 {
		return false, 0, nil
	}
	var rows []model.ManagedPushTarget
	if err := s.db.Where("session_id = ?", session.ID).Find(&rows).Error; err != nil {
		return false, 0, fmt.Errorf("查询下发目标失败: %w", err)
	}
	terminated, failures := 0, 0
	for _, row := range rows {
		if row.State == model.ManagedPushTargetSkipped {
			continue
		}
		if !row.State.IsTerminal() {
			continue
		}
		terminated++
		if row.State.IsFailure() {
			failures++
		}
	}
	if terminated == 0 {
		return false, 0, nil
	}
	observed := float64(failures) / float64(terminated) * 100
	return observed >= session.FailureRateThreshold, observed, nil
}

// pause 写人工暂停现场。
//
// 只用于**人工**暂停。熔断不走这里——熔断是终态（`failByCircuitBreak`），
// 因为它表达「本轮不该继续」，而 paused 会被 Resume 一键绕过那次判断。
func (s *ManagedPushService) pause(sessionID uint, kind, reason string) error {
	updates := map[string]any{
		"status":       model.ManagedPushPaused,
		"pause_kind":   kind,
		"pause_reason": truncateManagedText(reason, 500),
	}
	if err := s.db.Model(&model.ManagedPushSession{}).Where("id = ?", sessionID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("暂停会话失败: %w", err)
	}
	return nil
}

// failByCircuitBreak 把会话标 failed 并写熔断现场（终态，不可 Resume）。
//
// 为什么要单独一个函数而不是复用 pause：两者只差一个 status，但**可恢复性相反**。
// paused 能被 Resume 一键续跑，而熔断恰恰要拦住「直接续推剩余批次」这个动作——
// 触发熔断的配置很可能就是问题本身，续推等于把已验证的坏配置扩散到更多机器。
//
// 现场字段（break_reason/break_threshold/break_observed）必须留存：
// 运维要据此判断「是配置有问题，还是这批机器环境有问题」。
func (s *ManagedPushService) failByCircuitBreak(sessionID uint, reason string, threshold, observed float64) error {
	now := time.Now()
	updates := map[string]any{
		"status":          model.ManagedPushFailed,
		"pause_kind":      model.ManagedPushPauseCircuitBreak,
		"pause_reason":    truncateManagedText(reason, 500),
		"break_reason":    BreakReasonFailureRate,
		"break_threshold": threshold,
		"break_observed":  observed,
		"finished_at":     now,
	}
	if err := s.db.Model(&model.ManagedPushSession{}).
		Where("id = ? AND status IN ?", sessionID,
			[]model.ManagedPushSessionStatus{model.ManagedPushRunning, model.ManagedPushAwaitingRelease}).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("标记熔断失败: %w", err)
	}
	return nil
}

// RecoverInterrupted 启动时收口上次进程遗留的推进中会话。
//
// 【为何必须有】推进循环完全由内存 goroutine 驱动（`running` map + `run`）。
// 进程重启后那个 goroutine 已经不在了，但会话在库里仍是 `running`：
//   - `run` 不会自己醒来，`pendingTargets` 里那批 `pending` 永远没人推；
//   - `expireStaleTargets` 只扫 `pushing`，碰不到 `pending`；
//   - `Release` 要求 `awaiting_release`，而它停在 `running`，人工也放行不了。
//
// 结果是页面显示「正在推」，实际上什么都不再发生，只能靠运维手动 Cancel 才脱离该态。
//
// 【处理方向】把 `running` 落成 `paused`（`pause_kind=restart`），而不是 completed/failed：
//   - 不标 completed：本批还有未终结目标，标完成是谎报。
//   - 不标 failed：进程重启不是配置或探针的问题，标失败会诱导人去排查一个不存在的缺陷。
//   - paused 是**可人工续推**的最近形态：运维看一眼现场，点 Resume 即从当前批继续。
//
// `awaiting_release` **不动**：它本就是静止态（等人工放行），重启后 Release 依然可用，
// 改动它反而会丢掉「已经推完一批、该看效果了」这个进度事实。
//
// 幂等：只选 `status=running`，重复调用不会再改动任何行。
func (s *ManagedPushService) RecoverInterrupted() (int64, error) {
	res := s.db.Model(&model.ManagedPushSession{}).
		Where("status = ?", model.ManagedPushRunning).
		Updates(map[string]any{
			"status":       model.ManagedPushPaused,
			"pause_kind":   model.ManagedPushPauseRestart,
			"pause_reason": "进程重启时该会话仍在推进中，已暂停；请核对当前批进度后决定是否继续",
		})
	if res.Error != nil {
		return 0, fmt.Errorf("收口推进中会话失败: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Warn("托管配置下发：已收口上次进程遗留的推进中会话",
			"sessions", res.RowsAffected,
			"note", "置为暂停而非完成/失败：进度未终结，需人工确认是否续推")
	}
	return res.RowsAffected, nil
}

// completeFresh 把会话标 completed（幂等：已终态时直接返回）。
func (s *ManagedPushService) completeFresh(sessionID uint) error {
	now := time.Now()
	if err := s.db.Model(&model.ManagedPushSession{}).
		Where("id = ? AND status IN ?", sessionID,
			[]model.ManagedPushSessionStatus{model.ManagedPushRunning, model.ManagedPushAwaitingRelease}).
		Updates(map[string]any{"status": model.ManagedPushCompleted, "finished_at": now}).Error; err != nil {
		return fmt.Errorf("标记会话完成失败: %w", err)
	}
	return nil
}

// signalWake 唤醒等待中的批推进循环（非阻塞，缓冲 1）。
func (s *ManagedPushService) signalWake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// resolveTargets 解析下发目标。
func (s *ManagedPushService) resolveTargets(in ManagedPushInput) ([]model.Instance, error) {
	q := s.db.Model(&model.Instance{})
	if len(in.InstanceIDs) > 0 {
		q = q.Where("id IN ?", in.InstanceIDs)
	} else {
		switch in.Scope {
		case model.ManagedScopeGlobal:
			// 全平台
		case model.ManagedScopeNode:
			if in.ScopeRefID == 0 {
				return nil, fmt.Errorf("%w: node 层必须指定节点", ErrManagedScopeRefInvalid)
			}
			q = q.Where("node_id = ?", in.ScopeRefID)
		case model.ManagedScopeInstance:
			if in.ScopeRefID == 0 {
				return nil, fmt.Errorf("%w: instance 层必须指定实例", ErrManagedScopeRefInvalid)
			}
			q = q.Where("id = ?", in.ScopeRefID)
		default:
			return nil, ErrManagedScopeInvalid
		}
	}
	var rows []model.Instance
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询目标实例失败: %w", err)
	}
	return rows, nil
}

// recordAudit 写审计（失败不阻断主流程）。
func (s *ManagedPushService) recordAudit(userID uint, action string, sessionID uint, detail string) {
	if s.audit == nil {
		return
	}
	s.audit.RecordSafe(userID, action, "managed_config_push",
		fmt.Sprintf("%d", sessionID), detail, "")
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// clampAckTimeout 把 ACK 超时钳到有意义区间。
func clampAckTimeout(d time.Duration) time.Duration {
	if d < minManagedAckTimeout {
		return minManagedAckTimeout
	}
	if d > maxManagedAckTimeout {
		return maxManagedAckTimeout
	}
	return d
}

// computeManagedBatches 按每批台数算总批数；batchSize<=0 表示单批全量（1 批）。
func computeManagedBatches(total, batchSize int) int {
	if total <= 0 {
		return 0
	}
	if batchSize <= 0 {
		return 1
	}
	batches := total / batchSize
	if total%batchSize != 0 {
		batches++
	}
	return batches
}

// managedBatchOf 按目标序号（0 起）算它属于第几批（1 起）。
func managedBatchOf(index, batchSize int) int {
	if batchSize <= 0 {
		return 1
	}
	return index/batchSize + 1
}

// targetIndexByID 返回实例在目标列表中的序号（找不到时返回 0）。
func targetIndexByID(insts []model.Instance, id uint) int {
	// 列表长度就是实例数（一次会话的目标即全部待推实例），线性扫描可接受。
	for i, inst := range insts {
		if inst.ID == id {
			return i
		}
	}
	return 0
}

// normalizeManagedPage 归一化分页参数。
func normalizeManagedPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

// truncateManagedText 按字节上限截断文本（用于 varchar 列，避免写库失败）。
func truncateManagedText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	// 按 rune 边界回退，不切断多字节字符。
	for limit > 0 && !utf8StartByte(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}

// utf8StartByte 判断 b 是否是多字节序列的起始字节。
func utf8StartByte(b byte) bool { return b&0xC0 != 0x80 }

// isManagedConflictError 判定 rejected 的 error 是否表示版本冲突。
//
// 上游注释强调：`conflict` **不是** result 取值，而是 rejected 的 error 前缀。
// 这里按前缀匹配，并容忍大小写——探针侧文字可能带 "Version conflict" 等变体。
func isManagedConflictError(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "conflict") || strings.Contains(lower, "conflict")
}

// defaultString 返回首个非空串。
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// sortedKeys 返回 map 的键并按字典序排序（日志与调试用）。
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
