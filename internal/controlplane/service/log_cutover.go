package service

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// legacyLogSources 是切换后停止写入 CP logs 表、并转入 Legacy 只读预算的来源集合。
// platform（control_plane）持久化路径不在此集合中，切换后仍照常入库。
var legacyLogSources = []model.LogSource{
	model.LogSourceInstance,
	model.LogSourceWorker,
}

// isLegacyLogSource 报告来源是否属于切换后受 Legacy 预算管理的集合。
func isLegacyLogSource(src model.LogSource) bool {
	return src == model.LogSourceInstance || src == model.LogSourceWorker
}

// LogCutoverWatermark 是单个 Worker 的切换水位（FR-481）。
//
// 能力确认与采集账本就绪是切换前置条件；未满足时不得原子路由切换，
// 旧路径必须继续写入 CP，避免静默丢历史。
type LogCutoverWatermark struct {
	// WorkerUUID 目标 Worker 身份。
	WorkerUUID string `json:"workerUuid"`
	// CapabilityConfirmed 新路径能力协商已确认（协议版本、查询/投递能力等）。
	CapabilityConfirmed bool `json:"capabilityConfirmed"`
	// LedgerReady 采集账本与恢复责任已就绪，可承接切换后写入。
	LedgerReady bool `json:"ledgerReady"`
	// CutoffTime 切换水位：此时间之前仍留在 CP logs 表中的该 Worker 相关行视为 Legacy。
	// 零值表示尚未切换。
	CutoffTime time.Time `json:"cutoffTime"`
	// CutoverApplied 路由是否已原子切换。仅在 CapabilityConfirmed && LedgerReady 后可为 true。
	CutoverApplied bool `json:"cutoverApplied"`
}

// Ready 报告切换前置条件是否满足。
func (w LogCutoverWatermark) Ready() bool {
	return w.CapabilityConfirmed && w.LedgerReady
}

// LogCutover 是日志入库切换开关与逐 Worker 水位登记（FR-481）。
//
// 开关与水位分离：全局 enabled 控制 CP 侧 instance/worker 入库是否继续；
// 逐 Worker watermark 用于查询覆盖标注与失败时保持旧路径的可观测状态。
type LogCutover struct {
	mu sync.RWMutex
	// enabled 全局切换开关。true 时 Ingest 对 instance/worker 来源直接拒绝；
	// platform slog 持久化不受影响。
	enabled bool
	// watermarks 逐 Worker 切换状态，key=WorkerUUID。
	watermarks map[string]LogCutoverWatermark
	// blockedCount 可观测：因切换被拒绝的入库次数（诊断用，非持久化）。
	blockedCount int64
	db           *gorm.DB
}

// NewLogCutover 创建默认关闭的切换开关。
func NewLogCutover() *LogCutover {
	return &LogCutover{
		watermarks: make(map[string]LogCutoverWatermark),
	}
}

func NewPersistentLogCutover(db *gorm.DB) *LogCutover {
	c := NewLogCutover()
	if db == nil || !db.Migrator().HasTable(&model.LogCutoverState{}) || !db.Migrator().HasTable(&model.LogCutoverWorker{}) {
		return c
	}
	c.db = db
	var state model.LogCutoverState
	if err := db.First(&state, 1).Error; err == nil {
		c.enabled = state.Enabled
	}
	var workers []model.LogCutoverWorker
	if err := db.Find(&workers).Error; err == nil {
		for _, worker := range workers {
			c.watermarks[worker.WorkerUUID] = LogCutoverWatermark{
				WorkerUUID: worker.WorkerUUID, CapabilityConfirmed: worker.CapabilityConfirmed,
				LedgerReady: worker.LedgerReady, CutoffTime: worker.CutoffTime, CutoverApplied: worker.CutoverApplied,
			}
		}
	}
	return c
}

// Enabled 报告全局切换开关是否打开。
func (c *LogCutover) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// SetEnabled 设置全局切换开关。
func (c *LogCutover) SetEnabled(on bool) {
	_ = c.SetEnabledChecked(on)
}

func (c *LogCutover) SetEnabledChecked(on bool) error {
	if c == nil {
		return fmt.Errorf("cutover 未初始化")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != nil {
		state := model.LogCutoverState{ID: 1, Enabled: on}
		if err := c.db.Save(&state).Error; err != nil {
			return err
		}
	}
	c.enabled = on
	return nil
}

// BlocksIngestSource 报告该来源在当前开关下是否应停止写入 CP logs 表。
// platform（control_plane）与未知来源恒不拦截；instance/worker 在开关打开时拦截。
func (c *LogCutover) BlocksIngestSource(src model.LogSource) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	on := c.enabled
	c.mu.RUnlock()
	if !on {
		return false
	}
	return isLegacyLogSource(src)
}

// noteBlocked 记录一次被切换拦截的入库。
func (c *LogCutover) noteBlocked() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.blockedCount++
	c.mu.Unlock()
}

// BlockedCount 返回因切换被拒绝的入库次数。
func (c *LogCutover) BlockedCount() int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blockedCount
}

// UpsertWatermark 登记或更新 Worker 切换水位的前置状态（能力确认/账本就绪）。
// 确认位按 OR 合并（能力回退需显式 ResetWatermark）；CutoverApplied 仅在 Ready 时写入，
// 否则保持旧路径语义——路由切换必须走 ApplyCutover 或带已确认条件的完整水位。
func (c *LogCutover) UpsertWatermark(w LogCutoverWatermark) {
	_ = c.UpsertWatermarkChecked(w)
}

func (c *LogCutover) UpsertWatermarkChecked(w LogCutoverWatermark) error {
	if c == nil || w.WorkerUUID == "" {
		return fmt.Errorf("worker UUID 为空")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.watermarks[w.WorkerUUID]
	if !ok {
		cur = LogCutoverWatermark{WorkerUUID: w.WorkerUUID}
	}
	if w.CapabilityConfirmed {
		cur.CapabilityConfirmed = true
	}
	if w.LedgerReady {
		cur.LedgerReady = true
	}
	// 未就绪时忽略应用意图，避免绕过前置条件静默切换。
	if w.CutoverApplied && cur.Ready() {
		cur.CutoverApplied = true
		if !w.CutoffTime.IsZero() {
			cur.CutoffTime = w.CutoffTime
		} else if cur.CutoffTime.IsZero() {
			cur.CutoffTime = time.Now()
		}
	}
	if c.db != nil {
		row := model.LogCutoverWorker{WorkerUUID: cur.WorkerUUID,
			CapabilityConfirmed: cur.CapabilityConfirmed, LedgerReady: cur.LedgerReady,
			CutoffTime: cur.CutoffTime, CutoverApplied: cur.CutoverApplied}
		if err := c.db.Save(&row).Error; err != nil {
			return err
		}
	}
	c.watermarks[w.WorkerUUID] = cur
	return nil
}

// ResetWatermark 清除某 Worker 的切换登记（能力回退/重新协商时用旧路径）。
func (c *LogCutover) ResetWatermark(workerUUID string) {
	if c == nil || workerUUID == "" {
		return
	}
	c.mu.Lock()
	if c.db != nil {
		_ = c.db.Delete(&model.LogCutoverWorker{}, "worker_uuid = ?", workerUUID).Error
	}
	delete(c.watermarks, workerUUID)
	c.mu.Unlock()
}

// Watermark 读取某 Worker 的切换水位。
func (c *LogCutover) Watermark(workerUUID string) (LogCutoverWatermark, bool) {
	if c == nil {
		return LogCutoverWatermark{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	w, ok := c.watermarks[workerUUID]
	return w, ok
}

// ApplyCutover 在前置条件满足时原子登记切换水位并标记路由已切换。
// 失败时返回错误且不修改水位——调用方应保持旧路径并上报阻断原因。
func (c *LogCutover) ApplyCutover(workerUUID string, cutoff time.Time) (LogCutoverWatermark, error) {
	if c == nil {
		return LogCutoverWatermark{}, fmt.Errorf("cutover 未初始化")
	}
	if workerUUID == "" {
		return LogCutoverWatermark{}, fmt.Errorf("worker UUID 为空，无法登记切换水位")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.watermarks[workerUUID]
	if !ok {
		w = LogCutoverWatermark{WorkerUUID: workerUUID}
	}
	if !w.Ready() {
		return w, fmt.Errorf("worker %s 未满足切换前置条件: capabilityConfirmed=%v ledgerReady=%v",
			workerUUID, w.CapabilityConfirmed, w.LedgerReady)
	}
	if cutoff.IsZero() {
		cutoff = time.Now()
	}
	w.CutoffTime = cutoff
	w.CutoverApplied = true
	if c.db != nil {
		row := model.LogCutoverWorker{WorkerUUID: w.WorkerUUID,
			CapabilityConfirmed: w.CapabilityConfirmed, LedgerReady: w.LedgerReady,
			CutoffTime: w.CutoffTime, CutoverApplied: true}
		if err := c.db.Save(&row).Error; err != nil {
			return LogCutoverWatermark{}, err
		}
	}
	c.watermarks[workerUUID] = w
	return w, nil
}

// Watermarks 返回全部 Worker 水位快照（查询覆盖标注用）。
func (c *LogCutover) Watermarks() []LogCutoverWatermark {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]LogCutoverWatermark, 0, len(c.watermarks))
	for _, w := range c.watermarks {
		out = append(out, w)
	}
	return out
}

// Cutover 报告日志服务上的切换开关。恒非 nil。
func (s *LogService) Cutover() *LogCutover {
	if s == nil {
		return nil
	}
	return s.cutover
}

// cutoverBlocked 报告该来源是否因 FR-481 切换应停止写入 CP logs。
func (s *LogService) cutoverBlocked(src model.LogSource) bool {
	if s == nil || s.cutover == nil {
		return false
	}
	if s.cutover.BlocksIngestSource(src) {
		s.cutover.noteBlocked()
		return true
	}
	return false
}

// LogCutoverStatus 是管理面 GET /api/v1/logs/cutover 的聚合快照。
type LogCutoverStatus struct {
	// Enabled 全局切换开关。
	Enabled bool `json:"enabled"`
	// BlockedCount 因切换被拒绝的入库次数（诊断用）。
	BlockedCount int64 `json:"blockedCount"`
	// Watermarks 逐 Worker 水位快照。
	Watermarks []LogCutoverWatermark `json:"watermarks"`
	// LegacyRetentionDays / LegacyMaxTotalMB Legacy 独立保留预算。
	LegacyRetentionDays int `json:"legacyRetentionDays"`
	LegacyMaxTotalMB    int `json:"legacyMaxTotalMB"`
	// PlatformPurgeExcludesLegacy 切换打开时 platform 容量/时间巡检是否排除 Legacy 来源。
	PlatformPurgeExcludesLegacy bool `json:"platformPurgeExcludesLegacy"`
}

// CutoverStatus 聚合切换状态、水位与 Legacy 预算（FR-481 管理面）。
func (s *LogService) CutoverStatus() LogCutoverStatus {
	st := LogCutoverStatus{
		Watermarks: []LogCutoverWatermark{},
	}
	if s == nil {
		return st
	}
	if s.cutover != nil {
		st.Enabled = s.cutover.Enabled()
		st.BlockedCount = s.cutover.BlockedCount()
		if ws := s.cutover.Watermarks(); len(ws) > 0 {
			st.Watermarks = ws
		}
	}
	if s.legacy != nil {
		st.LegacyRetentionDays = s.legacy.RetentionDays
		st.LegacyMaxTotalMB = s.legacy.MaxTotalMB
	}
	st.PlatformPurgeExcludesLegacy = s.platformPurgeExcludesLegacy()
	return st
}

// ApplyAdminWatermark 管理面写入逐 Worker 水位（FR-481）。
//
// wantApply=true 表示尝试登记切换水位：请求侧 mark 必须已带 CapabilityConfirmed 与 LedgerReady，
// 否则返回错误且不修改该 Worker 状态——与服务侧 ApplyCutover 前置条件一致，禁止静默绕过。
func (s *LogService) ApplyAdminWatermark(mark LogCutoverWatermark, wantApply bool) (LogCutoverWatermark, error) {
	if s == nil || s.cutover == nil {
		return LogCutoverWatermark{}, fmt.Errorf("cutover 未初始化")
	}
	if mark.WorkerUUID == "" {
		return LogCutoverWatermark{}, fmt.Errorf("worker UUID 为空，无法登记切换水位")
	}
	if wantApply && !(mark.CapabilityConfirmed && mark.LedgerReady) {
		return LogCutoverWatermark{}, fmt.Errorf(
			"worker %s 应用切换水位要求 capabilityConfirmed 与 ledgerReady", mark.WorkerUUID)
	}
	// 先登记确认位（OR 合并），再视 need 应用切换。
	if err := s.cutover.UpsertWatermarkChecked(LogCutoverWatermark{
		WorkerUUID:          mark.WorkerUUID,
		CapabilityConfirmed: mark.CapabilityConfirmed,
		LedgerReady:         mark.LedgerReady,
	}); err != nil {
		return LogCutoverWatermark{}, err
	}
	if !wantApply {
		w, _ := s.cutover.Watermark(mark.WorkerUUID)
		return w, nil
	}
	cutoff := mark.CutoffTime
	if cutoff.IsZero() {
		cutoff = time.Now()
	}
	return s.cutover.ApplyCutover(mark.WorkerUUID, cutoff)
}
