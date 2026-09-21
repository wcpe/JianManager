package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 实例滚动/分批/灰度编排（FR-457）。
//
// 与既有 InstanceBatchService.Batch 的差异：Batch 是一次性并发扇出（无分批/间隔/失败即停/暂停继续/灰度），
// 对 60+ 台执行重启会同时打满并发、一次失败无可挽回。本服务把编排语义独立成会话实体，持久化进度，
// 逐批推进；单实例委托仍复用 delegateBatchOne（不新增 Worker 侧语义）。

// ErrRollingOpNotFound 滚动编排会话不存在。
var ErrRollingOpNotFound = errors.New("滚动编排会话不存在")

// RollingRequest 滚动编排请求。
type RollingRequest struct {
	Action  InstanceBatchAction
	IDs     []uint
	Filter  *InstanceBatchFilter
	Command string
	Policy  model.RollingPolicy
}

// InstanceRollingService 滚动编排服务。
type InstanceRollingService struct {
	db    *gorm.DB
	batch *InstanceBatchService
	// exec 单实例委托函数，默认复用 delegateBatchOne；测试可注入口子。
	exec func(req InstanceBatchRequest, inst *model.Instance) error

	mu       sync.Mutex
	runtimes map[uint]*rollingRuntime

	// createMu 串行化「活跃编排冲突检查 + 落库」，与 mu（runtimes 映射）分离：
	// 避免 create 期间的 DB 查询长时间占用 mu、阻塞运行态注册/清理（FR-457 #I）。
	createMu sync.Mutex
}

// NewInstanceRollingService 创建滚动编排服务。
func NewInstanceRollingService(db *gorm.DB, batch *InstanceBatchService) *InstanceRollingService {
	s := &InstanceRollingService{db: db, batch: batch, runtimes: map[uint]*rollingRuntime{}}
	if batch != nil {
		s.exec = batch.delegateBatchOne
	}
	return s
}

// SetExecutorForTest 注入单实例委托口子（仅供测试，绕过 Worker RPC）。
func (s *InstanceRollingService) SetExecutorForTest(fn func(req InstanceBatchRequest, inst *model.Instance) error) {
	s.exec = fn
}

// rollingRuntime 是运行中会话的内存控制块（暂停/继续/取消的信号面）。
type rollingRuntime struct {
	mu       sync.Mutex
	cond     *sync.Cond
	state    model.RollingState
	canceled bool
}

func newRollingRuntime(state model.RollingState) *rollingRuntime {
	rt := &rollingRuntime{state: state}
	rt.cond = sync.NewCond(&rt.mu)
	return rt
}

func (rt *rollingRuntime) setPaused() {
	rt.mu.Lock()
	rt.state = model.RollingStatePaused
	rt.mu.Unlock()
}

func (rt *rollingRuntime) setRunning() {
	rt.mu.Lock()
	rt.state = model.RollingStateRunning
	rt.cond.Broadcast()
	rt.mu.Unlock()
}

func (rt *rollingRuntime) setCanceled() {
	rt.mu.Lock()
	rt.canceled = true
	rt.state = model.RollingStateCanceled
	rt.cond.Broadcast()
	rt.mu.Unlock()
}

// waitRunnable 在批边界阻塞直到恢复运行；取消时立即返回。
func (rt *rollingRuntime) waitRunnable() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for rt.state == model.RollingStatePaused && !rt.canceled {
		rt.cond.Wait()
	}
}

func (rt *rollingRuntime) isCanceled() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.canceled
}

// sleepInterruptible 批间隔等待；取消时返回 false（外层停止），暂停时提前返回 true
// （外层回到批边界 waitRunnable 阻塞），从而让 Pause 能打断批间隔。
func (rt *rollingRuntime) sleepInterruptible(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		rt.mu.Lock()
		if rt.canceled {
			rt.mu.Unlock()
			return false
		}
		if rt.state == model.RollingStatePaused {
			rt.mu.Unlock()
			return true
		}
		rt.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		step := 50 * time.Millisecond
		if remaining < step {
			step = remaining
		}
		time.Sleep(step)
	}
}

// rollingSession 是执行器的内存工作状态（游标/计数/失败明细）。
type rollingSession struct {
	opID      uint
	action    InstanceBatchAction
	command   string
	targets   []uint
	batchSize int
	interval  time.Duration
	failFast  bool
	cursor    int
	rt        *rollingRuntime

	mu        sync.Mutex
	succeeded int
	failed    int
	errors    []model.RollingError
}

// Create 解析目标、冻结为会话、异步启动执行，返回会话。
// Ratio<1 时按稳定序抽样本会话目标集（首批灰度）；BatchSize=0 时单批全量（与旧 Batch 一致）。
// authorID 记录创建者（越权修复归属）；scopeIDs/scope 为调用者可访问集合（平台管理员 scoped=false）。
func (s *InstanceRollingService) Create(req RollingRequest, scopeIDs []uint, scope bool, authorID uint) (*model.InstanceRollingOp, error) {
	if !ValidInstanceBatchAction(req.Action) {
		return nil, fmt.Errorf("不支持的滚动动作: %s", req.Action)
	}
	if req.Action == InstanceBatchCommand && req.Command == "" {
		return nil, errors.New("command 动作需指定 command")
	}
	if s.batch == nil {
		return nil, errors.New("批量服务未注入")
	}
	batchReq := InstanceBatchRequest{Action: req.Action, IDs: req.IDs, Filter: req.Filter, Command: req.Command}
	instances, skipped, err := s.batch.resolveBatchTargets(batchReq, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	if len(instances) > maxInstanceBatchTargets {
		return nil, fmt.Errorf("滚动目标数 %d 超过上限 %d", len(instances), maxInstanceBatchTargets)
	}
	targets := make([]uint, 0, len(instances))
	for _, in := range instances {
		targets = append(targets, in.ID)
	}
	// 灰度抽样按 spec §2.1 仅在 filter 模式有意义（ids 模式为运维显式指定，不抽样）。
	// 两段式灰度为「新建会话」语义：先以 ratio<1 建会话跑首批，观察无异常后以剩余目标/ratio=1 另建会话续跑。
	if req.Filter != nil {
		targets = sampleTargets(targets, req.Policy.Ratio)
	}

	// 互斥约束（spec §2.1）：任一活跃编排（pending/running/paused）与本会话目标集合存在重叠即拒绝，
	// 避免并发创建同一目标集合的编排互相打翻。校验与落库在同一把 createMu 内，防止 TOCTOU；
	// createMu 与 runtime 映射的 mu 分离，使 DB 查询不阻塞其它会话的运行态注册（FR-457 #I）。
	s.createMu.Lock()
	if err := s.ensureNoActiveConflictLocked(targets); err != nil {
		s.createMu.Unlock()
		return nil, err
	}
	op := &model.InstanceRollingOp{
		Action:           string(req.Action),
		Command:          req.Command,
		BatchSize:        req.Policy.BatchSize,
		BatchIntervalSec: req.Policy.BatchIntervalSec,
		FailFast:         req.Policy.FailFast,
		Ratio:            req.Policy.Ratio,
		CreatedBy:        authorID,
		State:            model.RollingStatePending,
		Requested:        len(targets),
		Skipped:          skipped,
	}
	raw, _ := json.Marshal(targets)
	op.TargetsJSON = string(raw)
	op.ErrorsJSON = "[]"
	createErr := s.db.Create(op).Error
	s.createMu.Unlock()
	if createErr != nil {
		return nil, fmt.Errorf("创建滚动编排会话失败: %w", createErr)
	}
	s.attachTargets(op, targets)
	// 同步置 running 再异步启动，避免响应返回 pending（与 DB/docs/API.md 的 running 对齐）。
	s.launch(s.sessionFromOp(op), targets)
	return s.Get(op.ID)
}

// ensureNoActiveConflictLocked 在持有 createMu 时检测活跃编排与目标集合的重叠（spec §2.1 幂等/互斥）。
func (s *InstanceRollingService) ensureNoActiveConflictLocked(targets []uint) error {
	if len(targets) == 0 {
		return nil
	}
	var active []model.InstanceRollingOp
	if err := s.db.Where("state IN ?", []model.RollingState{
		model.RollingStatePending, model.RollingStateRunning, model.RollingStatePaused,
	}).Find(&active).Error; err != nil {
		return fmt.Errorf("检查活跃编排失败: %w", err)
	}
	want := make(map[uint]struct{}, len(targets))
	for _, id := range targets {
		want[id] = struct{}{}
	}
	for i := range active {
		decodeRollingOp(&active[i])
		for _, id := range active[i].Targets {
			if _, ok := want[id]; ok {
				// 只回「目标存在进行中编排」，不泄露他方会话 id/state（FR-457 越权修复）。
				return errors.New("目标实例存在进行中的编排，请先等待其结束或取消")
			}
		}
	}
	return nil
}

// Get 读取会话（含解码 targets/errors）。
func (s *InstanceRollingService) Get(opID uint) (*model.InstanceRollingOp, error) {
	var op model.InstanceRollingOp
	if err := s.db.First(&op, opID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRollingOpNotFound
		}
		return nil, err
	}
	decodeRollingOp(&op)
	return &op, nil
}

// Pause 暂停（阻塞在批边界）。
func (s *InstanceRollingService) Pause(opID uint) error {
	op, err := s.Get(opID)
	if err != nil {
		return err
	}
	if op.State != model.RollingStateRunning {
		return fmt.Errorf("仅运行中的编排可暂停（当前 %s）", op.State)
	}
	if rt := s.runtime(opID); rt != nil {
		rt.setPaused()
	}
	return s.setOpState(opID, model.RollingStatePaused)
}

// Resume 继续；进程重启后（内存运行态丢失）由 DB 游标续跑剩余批。
// 「查询运行态 + 占位」在 s.mu 内原子完成，避免并发两次 Resume 双推进（FR-457 竞态修复）。
func (s *InstanceRollingService) Resume(opID uint) error {
	op, err := s.Get(opID)
	if err != nil {
		return err
	}
	if op.State != model.RollingStatePaused && op.State != model.RollingStateRunning {
		return fmt.Errorf("仅暂停/运行中的编排可继续（当前 %s）", op.State)
	}
	// 原子占位：已有运行态则仅广播唤醒；否则新建 runtime 占坑后再拉起，杜绝双推进。
	rt := newRollingRuntime(model.RollingStateRunning)
	s.mu.Lock()
	existing, ok := s.runtimes[opID]
	if !ok {
		s.runtimes[opID] = rt
	}
	s.mu.Unlock()
	if ok {
		existing.setRunning()
		return s.setOpState(opID, model.RollingStateRunning)
	}
	// 内存运行态缺失（CP 重启/首次）：按 DB 游标重新拉起。
	session := s.sessionFromOp(op)
	session.cursor = op.Cursor
	session.succeeded = op.Succeeded
	session.failed = op.Failed
	session.errors = op.Errors
	session.rt = rt
	if err := s.setOpState(opID, model.RollingStateRunning); err != nil {
		s.clearRuntime(opID)
		return err
	}
	go s.run(session)
	return nil
}

// RecoverInterrupted CP 重启后把未终态（pending/running）编排置为 paused：内存运行态已丢失，
// 无法安全自动续跑，交由运维经 Resume 从 DB 游标续跑，避免状态永久停留在 running/pending。
func (s *InstanceRollingService) RecoverInterrupted() error {
	var active []model.InstanceRollingOp
	if err := s.db.Where("state IN ?", []model.RollingState{
		model.RollingStatePending, model.RollingStateRunning,
	}).Find(&active).Error; err != nil {
		return err
	}
	for i := range active {
		if err := s.setOpState(active[i].ID, model.RollingStatePaused); err != nil {
			slog.Warn("CP 重启恢复：置 paused 失败", "opId", active[i].ID, "error", err)
			continue
		}
		slog.Info("CP 重启恢复：未终态编排已置为 paused，可从游标续跑", "opId", active[i].ID, "cursor", active[i].Cursor)
	}
	return nil
}

// Cancel 取消后续批。
func (s *InstanceRollingService) Cancel(opID uint) error {
	op, err := s.Get(opID)
	if err != nil {
		return err
	}
	if op.State == model.RollingStateDone || op.State == model.RollingStateCanceled {
		return nil
	}
	if rt := s.runtime(opID); rt != nil {
		rt.setCanceled()
	}
	return s.setOpState(opID, model.RollingStateCanceled)
}

func (s *InstanceRollingService) launch(session *rollingSession, targets []uint) {
	rt := newRollingRuntime(model.RollingStateRunning)
	s.mu.Lock()
	s.runtimes[session.opID] = rt
	s.mu.Unlock()
	session.rt = rt
	s.setOpState(session.opID, model.RollingStateRunning)
	go s.run(session)
}

func (s *InstanceRollingService) run(session *rollingSession) {
	defer s.clearRuntime(session.opID)
	batches := formBatches(session.targets, session.batchSize)
	for {
		session.rt.waitRunnable()
		if session.rt.isCanceled() {
			s.finish(session, model.RollingStateCanceled)
			return
		}
		if session.cursor >= len(batches) {
			s.finish(session, model.RollingStateDone)
			return
		}
		batch := batches[session.cursor]
		failed := s.runBatch(session, batch)
		session.cursor++
		s.persistProgress(session)
		if session.failFast && failed {
			s.finish(session, model.RollingStateDone)
			return
		}
		if session.cursor < len(batches) && session.interval > 0 {
			if !session.rt.sleepInterruptible(session.interval) {
				s.finish(session, model.RollingStateCanceled)
				return
			}
		}
	}
}

// runBatch 执行一批：批内仍用 instanceBatchConcurrency 有界并发；返回本批是否有失败。
func (s *InstanceRollingService) runBatch(session *rollingSession, batch []uint) bool {
	insts, err := s.loadTargets(batch)
	if err != nil {
		slog.Error("滚动批加载目标失败", "opId", session.opID, "error", err)
		return false
	}
	req := InstanceBatchRequest{Action: session.action, Command: session.command}
	var (
		wg     sync.WaitGroup
		sem    = make(chan struct{}, instanceBatchConcurrency)
		failed atomic.Bool
	)
	for i := range insts {
		inst := insts[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.exec(req, &inst); err != nil {
				failed.Store(true)
				session.recordError(inst.ID, err)
			} else {
				session.recordSuccess()
			}
		}()
	}
	wg.Wait()
	return failed.Load()
}

func (s *InstanceRollingService) loadTargets(ids []uint) ([]model.Instance, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var insts []model.Instance
	if err := s.db.Preload("Node").Where("id IN ?", ids).Find(&insts).Error; err != nil {
		return nil, err
	}
	// 保持目标集顺序（稳定序）。
	byID := make(map[uint]model.Instance, len(insts))
	for _, in := range insts {
		byID[in.ID] = in
	}
	out := make([]model.Instance, 0, len(ids))
	for _, id := range ids {
		if in, ok := byID[id]; ok {
			out = append(out, in)
		}
	}
	return out, nil
}

func (s *InstanceRollingService) sessionFromOp(op *model.InstanceRollingOp) *rollingSession {
	return &rollingSession{
		opID:      op.ID,
		action:    InstanceBatchAction(op.Action),
		command:   op.Command,
		targets:   append([]uint(nil), op.Targets...),
		batchSize: op.BatchSize,
		interval:  time.Duration(op.BatchIntervalSec) * time.Second,
		failFast:  op.FailFast,
		cursor:    op.Cursor,
	}
}

func (s *InstanceRollingService) persistProgress(session *rollingSession) {
	session.mu.Lock()
	raw, _ := json.Marshal(session.errors)
	succeeded, failed := session.succeeded, session.failed
	session.mu.Unlock()
	err := s.db.Model(&model.InstanceRollingOp{}).Where("id = ?", session.opID).Updates(map[string]any{
		"cursor":      session.cursor,
		"succeeded":   succeeded,
		"failed":      failed,
		"errors_json": string(raw),
	}).Error
	if err != nil {
		slog.Warn("滚动编排进度落库失败", "opId", session.opID, "error", err)
	}
}

func (s *InstanceRollingService) finish(session *rollingSession, state model.RollingState) {
	session.mu.Lock()
	raw, _ := json.Marshal(session.errors)
	succeeded, failed := session.succeeded, session.failed
	session.mu.Unlock()
	err := s.db.Model(&model.InstanceRollingOp{}).Where("id = ?", session.opID).Updates(map[string]any{
		"state":       state,
		"succeeded":   succeeded,
		"failed":      failed,
		"errors_json": string(raw),
	}).Error
	if err != nil {
		slog.Warn("滚动编排终态落库失败", "opId", session.opID, "state", state, "error", err)
	}
}

func (s *InstanceRollingService) setOpState(opID uint, state model.RollingState) error {
	return s.db.Model(&model.InstanceRollingOp{}).Where("id = ?", opID).Update("state", state).Error
}

func (s *InstanceRollingService) runtime(opID uint) *rollingRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimes[opID]
}

func (s *InstanceRollingService) clearRuntime(opID uint) {
	s.mu.Lock()
	delete(s.runtimes, opID)
	s.mu.Unlock()
}

func (s *InstanceRollingService) attachTargets(op *model.InstanceRollingOp, targets []uint) {
	op.Targets = targets
}

func (s *rollingSession) recordSuccess() {
	s.mu.Lock()
	s.succeeded++
	s.mu.Unlock()
}

func (s *rollingSession) recordError(instanceID uint, err error) {
	s.mu.Lock()
	s.failed++
	if len(s.errors) < maxInstanceBatchErrors {
		s.errors = append(s.errors, model.RollingError{InstanceID: instanceID, Error: err.Error()})
	}
	s.mu.Unlock()
}

// formBatches 按批大小切分目标；batchSize<=0 时单批全量（向后兼容旧 Batch()）。
func formBatches(targets []uint, batchSize int) [][]uint {
	if len(targets) == 0 {
		return nil
	}
	if batchSize <= 0 {
		return [][]uint{targets}
	}
	out := make([][]uint, 0, (len(targets)+batchSize-1)/batchSize)
	for i := 0; i < len(targets); i += batchSize {
		end := i + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		out = append(out, targets[i:end])
	}
	return out
}

// sampleTargets 按稳定序（升序 id）抽样比例子集；ratio<=0 或 >=1 返回全量（至少 1 台）。
func sampleTargets(targets []uint, ratio float64) []uint {
	if ratio <= 0 || ratio >= 1 || len(targets) == 0 {
		return targets
	}
	sorted := append([]uint(nil), targets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := int(math.Floor(float64(len(sorted)) * ratio))
	if n < 1 {
		n = 1
	}
	return append([]uint(nil), sorted[:n]...)
}

func decodeRollingOp(op *model.InstanceRollingOp) {
	if op.TargetsJSON != "" {
		_ = json.Unmarshal([]byte(op.TargetsJSON), &op.Targets)
	}
	if op.ErrorsJSON != "" {
		_ = json.Unmarshal([]byte(op.ErrorsJSON), &op.Errors)
	}
	if op.Targets == nil {
		op.Targets = []uint{}
	}
	if op.Errors == nil {
		op.Errors = []model.RollingError{}
	}
}
