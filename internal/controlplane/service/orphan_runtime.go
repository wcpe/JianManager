package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 默认护栏：宽限 10 分钟、不自动杀（spec §2.1 / §6）。
const (
	defaultOrphanGracePeriod = 10 * time.Minute
	defaultOrphanAutoDispose = false
)

var (
	// ErrOrphanRuntimeNotFound 无主运行时记录不存在。
	ErrOrphanRuntimeNotFound = errors.New("无主运行时记录不存在")
	// ErrOrphanRuntimeNotActive 记录已终态（disposed/cancelled），不可再处置。
	ErrOrphanRuntimeNotActive = errors.New("无主运行时已终态，不可处置")
	// ErrOrphanWorkerOffline Worker 未连接，无法下发处置。
	ErrOrphanWorkerOffline = errors.New("节点未连接，无法处置无主运行时")
	// ErrOrphanDisposeFailed Worker 返回失败或 gRPC 错误。
	ErrOrphanDisposeFailed = errors.New("处置无主运行时失败")
)

// OrphanDisposeClient 抽象 CP→Worker 的处置 RPC，便于单测注入假客户端。
type OrphanDisposeClient interface {
	DisposeOrphanRuntime(ctx context.Context, nodeUUID, instanceUUID string) error
}

// poolDisposeClient 经 ClientPool 调 Worker.DisposeOrphanRuntime。
type poolDisposeClient struct {
	pool *cpgrpc.ClientPool
}

func (c *poolDisposeClient) DisposeOrphanRuntime(ctx context.Context, nodeUUID, instanceUUID string) error {
	client, ok := c.pool.Get(nodeUUID)
	if !ok || client == nil || client.Worker == nil {
		return ErrOrphanWorkerOffline
	}
	resp, err := client.Worker.DisposeOrphanRuntime(ctx, &workerpb.DisposeOrphanRuntimeRequest{
		InstanceUuid: instanceUUID,
	})
	if err != nil {
		// 老 Worker 无本方法：不视为 CP 崩溃；上层记日志并保留记录。
		if status.Code(err) == codes.Unimplemented {
			return fmt.Errorf("%w: Worker 不支持 DisposeOrphanRuntime（需升级）: %v", ErrOrphanDisposeFailed, err)
		}
		return fmt.Errorf("%w: %v", ErrOrphanDisposeFailed, err)
	}
	if resp == nil || !resp.Success {
		msg := "未知错误"
		if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		return fmt.Errorf("%w: %s", ErrOrphanDisposeFailed, msg)
	}
	return nil
}

// OrphanRuntimeTracker CP 侧无主运行时跟踪与处置（FR-326）。
//
// 每拍心跳：比对 Worker 上报 instances 与 DB instances（含软删视为无）；
// 发现 orphan → pending；宽限内 CP 又有记录 → cancelled；宽限后 confirmed，
// auto_dispose=true 才自动下发 Dispose。默认只告警/列表，管理员可手动确认。
type OrphanRuntimeTracker struct {
	db       *gorm.DB
	settings SettingsReader
	dispose  OrphanDisposeClient
	audit    *AuditService
	// now 可注入时钟，单测确定性宽限。
	now func() time.Time
	// disposeTimeout 单次处置 RPC 超时。
	disposeTimeout time.Duration
}

// NewOrphanRuntimeTracker 创建反向对账跟踪器。
// settings 为 nil 时用内置默认（grace=10m, auto_dispose=false）；pool 为 nil 时无法自动/手动 RPC 处置。
func NewOrphanRuntimeTracker(db *gorm.DB, settings SettingsReader, pool *cpgrpc.ClientPool) *OrphanRuntimeTracker {
	var dispose OrphanDisposeClient
	if pool != nil {
		dispose = &poolDisposeClient{pool: pool}
	}
	return &OrphanRuntimeTracker{
		db:             db,
		settings:       settings,
		dispose:        dispose,
		now:            time.Now,
		disposeTimeout: 30 * time.Second,
	}
}

// SetAudit 注入审计服务（手动/自动处置均记）；nil 时跳过审计。
func (t *OrphanRuntimeTracker) SetAudit(a *AuditService) { t.audit = a }

// SetDisposeClient 注入处置客户端（测试用）。
func (t *OrphanRuntimeTracker) SetDisposeClient(c OrphanDisposeClient) { t.dispose = c }

// SetNow 注入时钟（测试用）。
func (t *OrphanRuntimeTracker) SetNow(fn func() time.Time) {
	if fn != nil {
		t.now = fn
	}
}

// gracePeriod 生效宽限期。
func (t *OrphanRuntimeTracker) gracePeriod() time.Duration {
	if t.settings == nil {
		return defaultOrphanGracePeriod
	}
	raw := t.settings.EffectiveValue(SettingKeyOrphanGracePeriod)
	if raw == "" {
		return defaultOrphanGracePeriod
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultOrphanGracePeriod
	}
	return d
}

// autoDispose 是否自动处置（默认 false）。
func (t *OrphanRuntimeTracker) autoDispose() bool {
	if t.settings == nil {
		return defaultOrphanAutoDispose
	}
	return t.settings.EffectiveValue(SettingKeyOrphanAutoDispose) == "true"
}

// ObserveHeartbeat 心跳反向对账入口（FR-326）。
//
// 兼容：reported 为 nil 时视为「老 Worker / 未携带清单」——不创建新 pending，也不因「本拍未见」
// 推进已有记录（避免误杀）。空切片表示新 Worker 明确上报「本节点无在管实例」：可取消
// 已跟踪但仍 pending/confirmed 且本拍未再出现者（Worker 自行消失 → 视为自然消除，标 cancelled）。
func (t *OrphanRuntimeTracker) ObserveHeartbeat(nodeUUID string, reported []*workerpb.InstanceState) {
	if t == nil || t.db == nil || nodeUUID == "" {
		return
	}
	// 老 Worker：字段缺省为零值切片 nil——不启用反向对账。
	if reported == nil {
		return
	}

	now := t.now()
	grace := t.gracePeriod()
	auto := t.autoDispose()

	// Worker 本拍在管 UUID 集合 + 状态/PID 摘要。
	type snap struct {
		state string
		pid   int
	}
	seen := make(map[string]snap, len(reported))
	uuids := make([]string, 0, len(reported))
	for _, s := range reported {
		if s == nil || s.InstanceUuid == "" {
			continue
		}
		seen[s.InstanceUuid] = snap{state: s.State, pid: int(s.Pid)}
		uuids = append(uuids, s.InstanceUuid)
	}

	// CP 活跃实例 UUID（软删不计入 → 软删视为无记录）。
	cpAlive := t.cpAliveInstanceSet(uuids)

	// 1) Worker 有、CP 无 → 创建/刷新 pending|confirmed。
	for instUUID, sn := range seen {
		if _, ok := cpAlive[instUUID]; ok {
			// CP 有记录：若存在活跃 orphan 跟踪则取消。
			t.cancelIfActive(nodeUUID, instUUID, now, "CP 已出现对应实例记录")
			continue
		}
		t.upsertOrphan(nodeUUID, instUUID, sn.state, sn.pid, now, grace, auto)
	}

	// 2) 本节点活跃跟踪中、本拍 Worker 未再上报 → 自然消失，取消。
	// （空清单或清单中已不含该 UUID。）
	still := make(map[string]bool, len(seen))
	for u := range seen {
		still[u] = true
	}
	t.cancelMissingFromWorker(nodeUUID, still, now)
}

// cpAliveInstanceSet 查询 UUID 是否在 CP instances 表中且未软删。
func (t *OrphanRuntimeTracker) cpAliveInstanceSet(uuids []string) map[string]struct{} {
	out := make(map[string]struct{})
	if len(uuids) == 0 {
		return out
	}
	var found []string
	// 默认 GORM 排除 soft-deleted，符合「软删视为无」。
	if err := t.db.Model(&model.Instance{}).
		Where("uuid IN ?", uuids).
		Pluck("uuid", &found).Error; err != nil {
		slog.Warn("反向对账查询实例失败", "error", err)
		return out
	}
	for _, u := range found {
		out[u] = struct{}{}
	}
	return out
}

func (t *OrphanRuntimeTracker) cancelIfActive(nodeUUID, instanceUUID string, now time.Time, reason string) {
	var rec model.OrphanRuntime
	err := t.db.Where("node_uuid = ? AND instance_uuid = ? AND status IN ?",
		nodeUUID, instanceUUID,
		[]string{string(model.OrphanRuntimePending), string(model.OrphanRuntimeConfirmed)},
	).First(&rec).Error
	if err != nil {
		return
	}
	updates := map[string]interface{}{
		"status":     model.OrphanRuntimeCancelled,
		"last_seen_at": now,
		"last_error": "",
	}
	if err := t.db.Model(&rec).Updates(updates).Error; err != nil {
		slog.Warn("取消无主运行时跟踪失败", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "error", err)
		return
	}
	slog.Info("无主运行时跟踪已取消", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "reason", reason)
}

func (t *OrphanRuntimeTracker) upsertOrphan(nodeUUID, instanceUUID, state string, pid int, now time.Time, grace time.Duration, auto bool) {
	var rec model.OrphanRuntime
	err := t.db.Where("node_uuid = ? AND instance_uuid = ?", nodeUUID, instanceUUID).
		Order("id DESC").First(&rec).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.Warn("查询无主运行时失败", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "error", err)
		return
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 终态历史存在时仍可新建一轮 pending（Worker 再次出现）。
		rec = model.OrphanRuntime{
			NodeUUID:     nodeUUID,
			InstanceUUID: instanceUUID,
			WorkerState:  state,
			WorkerPID:    pid,
			Status:       model.OrphanRuntimePending,
			FirstSeenAt:  now,
			LastSeenAt:   now,
		}
		if err := t.db.Create(&rec).Error; err != nil {
			slog.Warn("创建无主运行时跟踪失败", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "error", err)
			return
		}
		slog.Warn("发现无主运行时（Worker 有、CP 无记录）",
			"nodeUUID", nodeUUID, "instanceUUID", instanceUUID,
			"workerState", state, "workerPid", pid, "gracePeriod", grace.String())
		return
	}

	// 已 disposed/cancelled：若再次出现则重新开一轮 pending。
	if rec.Status == model.OrphanRuntimeDisposed || rec.Status == model.OrphanRuntimeCancelled {
		rec = model.OrphanRuntime{
			NodeUUID:     nodeUUID,
			InstanceUUID: instanceUUID,
			WorkerState:  state,
			WorkerPID:    pid,
			Status:       model.OrphanRuntimePending,
			FirstSeenAt:  now,
			LastSeenAt:   now,
		}
		if err := t.db.Create(&rec).Error; err != nil {
			slog.Warn("重新跟踪无主运行时失败", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "error", err)
		}
		return
	}

	// pending / confirmed：刷新 lastSeen + 状态摘要。
	updates := map[string]interface{}{
		"last_seen_at":  now,
		"worker_state":  state,
		"worker_pid":    pid,
	}
	// 宽限期到 → confirmed。
	if rec.Status == model.OrphanRuntimePending && !now.Before(rec.FirstSeenAt.Add(grace)) {
		updates["status"] = model.OrphanRuntimeConfirmed
		rec.Status = model.OrphanRuntimeConfirmed
		slog.Warn("无主运行时宽限期已过",
			"nodeUUID", nodeUUID, "instanceUUID", instanceUUID,
			"firstSeenAt", rec.FirstSeenAt, "gracePeriod", grace.String(), "autoDispose", auto)
	}
	if err := t.db.Model(&rec).Updates(updates).Error; err != nil {
		slog.Warn("更新无主运行时失败", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "error", err)
		return
	}

	// 宽限后 + auto → 自动处置。
	if rec.Status == model.OrphanRuntimeConfirmed && auto {
		if err := t.disposeOne(&rec, "auto", 0, ""); err != nil {
			slog.Warn("自动处置无主运行时失败", "nodeUUID", nodeUUID, "instanceUUID", instanceUUID, "error", err)
		}
	}
}

func (t *OrphanRuntimeTracker) cancelMissingFromWorker(nodeUUID string, stillSeen map[string]bool, now time.Time) {
	var active []model.OrphanRuntime
	if err := t.db.Where("node_uuid = ? AND status IN ?", nodeUUID,
		[]string{string(model.OrphanRuntimePending), string(model.OrphanRuntimeConfirmed)},
	).Find(&active).Error; err != nil {
		return
	}
	for i := range active {
		if stillSeen[active[i].InstanceUUID] {
			continue
		}
		if err := t.db.Model(&active[i]).Updates(map[string]interface{}{
			"status":       model.OrphanRuntimeCancelled,
			"last_seen_at": now,
			"last_error":   "",
		}).Error; err != nil {
			continue
		}
		slog.Info("无主运行时已从 Worker 清单消失，跟踪取消",
			"nodeUUID", nodeUUID, "instanceUUID", active[i].InstanceUUID)
	}
}

// List 列出无主运行时。status 空=全部；activeOnly 时仅 pending+confirmed。
func (t *OrphanRuntimeTracker) List(status string, activeOnly bool, limit int) ([]model.OrphanRuntime, error) {
	if limit <= 0 {
		limit = 100
	}
	q := t.db.Model(&model.OrphanRuntime{}).Order("first_seen_at DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	} else if activeOnly {
		q = q.Where("status IN ?", []string{
			string(model.OrphanRuntimePending), string(model.OrphanRuntimeConfirmed),
		})
	}
	var items []model.OrphanRuntime
	if err := q.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询无主运行时列表失败: %w", err)
	}
	if items == nil {
		items = []model.OrphanRuntime{}
	}
	return items, nil
}

// Get 按 UUID 取单条。
func (t *OrphanRuntimeTracker) Get(orphanUUID string) (*model.OrphanRuntime, error) {
	var rec model.OrphanRuntime
	if err := t.db.Where("uuid = ?", orphanUUID).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrphanRuntimeNotFound
		}
		return nil, fmt.Errorf("查询无主运行时失败: %w", err)
	}
	return &rec, nil
}

// ConfirmDispose 管理员手动确认处置（auto 关闭时的主路径）。
// userID/ip 写入审计；成功将状态置 disposed。
func (t *OrphanRuntimeTracker) ConfirmDispose(orphanUUID string, userID uint, ip string) (*model.OrphanRuntime, error) {
	rec, err := t.Get(orphanUUID)
	if err != nil {
		return nil, err
	}
	if rec.Status != model.OrphanRuntimePending && rec.Status != model.OrphanRuntimeConfirmed {
		return nil, ErrOrphanRuntimeNotActive
	}
	if err := t.disposeOne(rec, "manual", userID, ip); err != nil {
		return rec, err
	}
	return t.Get(orphanUUID)
}

func (t *OrphanRuntimeTracker) disposeOne(rec *model.OrphanRuntime, mode string, userID uint, ip string) error {
	if t.dispose == nil {
		err := fmt.Errorf("%w: 未配置 Worker 连接池", ErrOrphanDisposeFailed)
		t.markDisposeError(rec, err.Error())
		t.recordDisposeAudit(userID, ip, rec, mode, false, err.Error())
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.disposeTimeout)
	defer cancel()
	err := t.dispose.DisposeOrphanRuntime(ctx, rec.NodeUUID, rec.InstanceUUID)
	if err != nil {
		t.markDisposeError(rec, err.Error())
		t.recordDisposeAudit(userID, ip, rec, mode, false, err.Error())
		return err
	}
	now := t.now()
	if err := t.db.Model(rec).Updates(map[string]interface{}{
		"status":       model.OrphanRuntimeDisposed,
		"disposed_at":  now,
		"dispose_mode": mode,
		"last_error":   "",
	}).Error; err != nil {
		return fmt.Errorf("更新处置状态失败: %w", err)
	}
	slog.Info("无主运行时已处置",
		"nodeUUID", rec.NodeUUID, "instanceUUID", rec.InstanceUUID,
		"mode", mode, "orphanUUID", rec.UUID)
	t.recordDisposeAudit(userID, ip, rec, mode, true, "")
	return nil
}

func (t *OrphanRuntimeTracker) markDisposeError(rec *model.OrphanRuntime, msg string) {
	if len(msg) > 512 {
		msg = msg[:512]
	}
	_ = t.db.Model(rec).Update("last_error", msg).Error
}

func (t *OrphanRuntimeTracker) recordDisposeAudit(userID uint, ip string, rec *model.OrphanRuntime, mode string, success bool, errMsg string) {
	if t.audit == nil {
		return
	}
	// 自动处置无登录用户：用 0 表示系统；审计表 user_id 非空，系统动作记 0。
	action := "orphan_runtime.dispose_manual"
	if mode == "auto" {
		action = "orphan_runtime.dispose_auto"
	}
	detail := fmt.Sprintf(`{"orphanUuid":%q,"nodeUuid":%q,"instanceUuid":%q,"mode":%q}`,
		rec.UUID, rec.NodeUUID, rec.InstanceUUID, mode)
	_ = t.audit.RecordResult(userID, action, "orphan_runtime", rec.UUID, detail, ip, success, errMsg)
}
