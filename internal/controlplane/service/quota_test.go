package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// quotaStopWorker 记录停止调用并把实例状态推进到 STOPPED（模拟真实停服链路的状态回写）。
type quotaStopWorker struct {
	workerpb.WorkerServiceClient
	db         *gorm.DB
	stopCalled bool
}

func (w *quotaStopWorker) StopInstance(_ context.Context, in *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.stopCalled = true
	if w.db != nil {
		_ = w.db.Model(&model.Instance{}).Where("uuid = ?", in.InstanceUuid).
			Update("status", model.InstanceStatusStopped).Error
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// newStopRecordingPool 建带停止能力的假 Worker 连接池（按实例所属节点 UUID 注册）。
func newStopRecordingPool(t *testing.T, db *gorm.DB, inst *model.Instance) *cpgrpc.ClientPool {
	t.Helper()
	var node model.Node
	require.NoError(t, db.First(&node, inst.NodeID).Error)
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &quotaStopWorker{db: db})
	return pool
}

// quotaTestDB 配额测试库（实例 + 组配额 + 组归属 + 备份 + 任务日志等）。
func quotaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.GroupQuota{}, &model.Backup{}, &model.AlertRule{}, &model.AlertEvent{}, &model.AlertChannel{},
		&model.AuditLog{}, &model.Notification{}, &model.PlatformSetting{}, &model.ProcessMetricSnapshot{},
	))
	// 停止能力经 Worker 连接池下发，需要一条节点记录（实例 NodeID 指向它）。
	require.NoError(t, db.Create(&model.Node{UUID: "node-quota-" + t.Name(), Name: "n", Host: "h", Secret: "s"}).Error)
	return db
}

// makeQuotaInstance 造一个实例并（可选）挂到组上。
func makeQuotaInstance(t *testing.T, db *gorm.DB, name string, cpu float64, memMB, diskMB int64, groupID uint) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID: 1, Name: name, Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusRunning,
		StartCommand: "./beacon", CPULimit: cpu, MemLimitMB: memMB, DiskLimitMB: diskMB,
	}
	require.NoError(t, db.Create(inst).Error)
	if groupID != 0 {
		require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupID, InstanceID: inst.ID}).Error)
	}
	return inst
}

// TestEffectiveQuota_InstanceWins 实例级字段优先于组派生（FR-467 §2.2 优先级）。
func TestEffectiveQuota_InstanceWins(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 11, MaxStorageMB: 1024}).Error)
	inst := makeQuotaInstance(t, db, "a", 1.5, 2048, 512, 11)

	q, err := NewQuotaService(db).EffectiveQuota(inst.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, q.CPUCores)
	require.Equal(t, int64(2048), q.MemLimitMB)
	require.Equal(t, int64(512), q.DiskLimitMB)
	require.Equal(t, "instance", q.MemSource)
	require.Equal(t, "instance", q.DiskSource)
	require.Equal(t, "instance", q.CPUSource)
	require.Equal(t, EnforceModeAlert, q.EnforceMode, "未配置时默认 alert（最保守）")
}

// TestEffectiveQuota_GroupDerivedDisk 无实例级磁盘限额时按组配额余量派生（软限）。
func TestEffectiveQuota_GroupDerivedDisk(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 21, MaxStorageMB: 1000}).Error)
	inst := makeQuotaInstance(t, db, "a", 0, 0, 0, 21)
	// 组内已用 400MB（按备份大小估算，与 Create 时的口径一致）。
	require.NoError(t, db.Create(&model.Backup{
		InstanceID: inst.ID, Name: "b", FileSizeMB: 400, Status: model.BackupStatusCompleted,
	}).Error)

	q, err := NewQuotaService(db).EffectiveQuota(inst.ID)
	require.NoError(t, err)
	require.Equal(t, int64(600), q.DiskLimitMB)
	require.Equal(t, "group", q.DiskSource)
	require.Zero(t, q.MemLimitMB, "组配额没有内存维度 → 不限")
	require.Equal(t, "none", q.MemSource)
}

// TestEffectiveQuota_NoLimit 无组无实例限额 → 全部不限。
func TestEffectiveQuota_NoLimit(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 0, 0, 0)
	q, err := NewQuotaService(db).EffectiveQuota(inst.ID)
	require.NoError(t, err)
	require.Zero(t, q.CPUCores)
	require.Zero(t, q.MemLimitMB)
	require.Zero(t, q.DiskLimitMB)
	require.Equal(t, "none", q.MemSource)
	require.Equal(t, "none", q.DiskSource)
}

// TestEffectiveQuota_GroupDerivedStrictness 组归属的实际约束是「一实例一组」
// （GroupInstance.InstanceID 为单列 uniqueIndex，见 model/instance.go），
// 组配额派生按该组的额度余量计算。
//
// R21：本条注释原先与 `pickStrictestGroup`（按「多组取最严」实现）互相矛盾。
// 经判定 schema 不变量成立（唯一索引 + 唯一生产写入点在实例创建事务内），
// 多组分支不可达，该函数已删除；本测试断言单组语义仍然是「按该组额度算余量」。
func TestEffectiveQuota_GroupDerivedStrictness(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 32, MaxStorageMB: 800}).Error)
	inst := makeQuotaInstance(t, db, "a", 0, 0, 0, 32)

	q, err := NewQuotaService(db).EffectiveQuota(inst.ID)
	require.NoError(t, err)
	require.Equal(t, uint(32), q.GroupID)
	require.Equal(t, int64(800), q.DiskLimitMB)
}

// TestGroupInstance_SchemaEnforcesSingleGroupPerInstance R21 的**事实锁定**：
// 「一实例至多一组」必须由数据库约束保证，而不是靠调用方自觉。
//
// 这条断言是 R21 判定（多组分支不可达 → 删除 pickStrictestGroup）的证据基础：
// 若将来有人去掉该唯一索引以支持多组，本测试会立刻失败，提醒同步恢复
// 「多组取最严」的配额派生语义（配额是护栏，取宽会让实例在更严的组里超限而不被拦）。
func TestGroupInstance_SchemaEnforcesSingleGroupPerInstance(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 0, 0, 0)

	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 1, InstanceID: inst.ID}).Error)
	err := db.Create(&model.GroupInstance{GroupID: 2, InstanceID: inst.ID}).Error
	require.Error(t, err, "同一实例的第二条组归属必须被数据库拒绝（InstanceID 单列唯一）")

	// 归属解析仍取到第一条（且不因多组场景做任何取严比较）。
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 1, MaxStorageMB: 500}).Error)
	q, err := NewQuotaService(db).EffectiveQuota(inst.ID)
	require.NoError(t, err)
	require.Equal(t, uint(1), q.GroupID)
	require.Equal(t, int64(500), q.DiskLimitMB)
}

// TestEffectiveQuota_GroupEnforceModeOverridesDefault 组级 EnforceMode 覆盖平台默认。
func TestEffectiveQuota_GroupEnforceModeOverridesDefault(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 41, MaxStorageMB: 1000, EnforceMode: "stop"}).Error)
	inst := makeQuotaInstance(t, db, "a", 0, 0, 0, 41)

	svc := NewQuotaService(db)
	svc.SetDefaultModeReader(func() EnforceMode { return EnforceModeAlert })
	q, err := svc.EffectiveQuota(inst.ID)
	require.NoError(t, err)
	require.Equal(t, EnforceModeStop, q.EnforceMode)

	// 组未配置时回落平台默认。
	inst2 := makeQuotaInstance(t, db, "b", 0, 0, 0, 0)
	q2, err := svc.EffectiveQuota(inst2.ID)
	require.NoError(t, err)
	require.Equal(t, EnforceModeAlert, q2.EnforceMode)

	svc.SetDefaultModeReader(func() EnforceMode { return EnforceModeThrottle })
	q3, err := svc.EffectiveQuota(inst2.ID)
	require.NoError(t, err)
	require.Equal(t, EnforceModeThrottle, q3.EnforceMode)
	// 非法默认值回退 alert（不因误配而误停实例）。
	svc.SetDefaultModeReader(func() EnforceMode { return EnforceMode("bogus") })
	q4, err := svc.EffectiveQuota(inst2.ID)
	require.NoError(t, err)
	require.Equal(t, EnforceModeAlert, q4.EnforceMode)
}

// stubQuotaMetrics 可编程的用量来源。
//
// usage/usageErr/unavailable 用于区分「采样失败」「不可用」「正常采样」三种形态（R12 测试）；
// 三者都不设置时 InstanceResourceUsage 沿用 sample + disk（保持既有用例语义）。
type stubQuotaMetrics struct {
	sample quotaSample
	disk   int64
	calls  int
	// usage 非 nil 时作为 InstanceResourceUsage 的返回样本。
	usage *quotaSample
	// usageErr 非 nil 时 InstanceResourceUsage 返回该错误（R12：失败必须可见）。
	usageErr error
	// unavailable 置位时 InstanceResourceUsage 返回「无数据」（节点离线/实例未运行）。
	unavailable bool
	// diskErr 非 nil 时 InstanceWorkDirBytes 返回该错误。
	diskErr error
}

func (s *stubQuotaMetrics) LatestProcessSample(string, time.Time) (*quotaSample, error) {
	s.calls++
	out := s.sample
	return &out, nil
}

func (s *stubQuotaMetrics) InstanceWorkDirBytes(context.Context, uint, string) (int64, error) {
	if s.diskErr != nil {
		return 0, s.diskErr
	}
	return s.disk, nil
}

// InstanceResourceUsage 返回与 LatestProcessSample 同源的样本（M-2：全树 RSS 走同一条采集）。
//
// 未设置 usage/usageErr/unavailable 时沿用 sample + disk，保持既有用例语义；
// 三种显式形态供 R12 用例断言「失败不被吞掉、不可用不冒充失败」。
func (s *stubQuotaMetrics) InstanceResourceUsage(context.Context, uint, string) (*quotaSample, bool, error) {
	s.calls++
	if s.usageErr != nil {
		return nil, false, s.usageErr
	}
	if s.unavailable {
		return nil, false, nil
	}
	if s.usage != nil {
		return s.usage, true, nil
	}
	out := s.sample
	out.DiskBytes = s.disk
	return &out, true, nil
}

// newEnforcer 建配额巡检器（不启动后台循环，测试直接调 evaluate）。
//
// 审计经 SetAudit 注入而非直接写字段（R25）：与生产接线同一条路径，
// 使「setter 漏接」这类问题在测试里也能暴露。
func newEnforcer(t *testing.T, db *gorm.DB, metrics *stubQuotaMetrics) (*QuotaEnforcer, *QuotaService) {
	t.Helper()
	quotas := NewQuotaService(db)
	e := NewQuotaEnforcer(db, quotas)
	e.SetMetrics(metrics)
	e.SetAudit(NewAuditService(db))
	e.SetSettingsReader(stubSettings{SettingKeyQuotaEnforceStreak: "3"})
	return e, quotas
}

// TestQuotaEnforcer_StreakThenFire 连续 K 次超限才处置；单次峰值不触发（抗抖动）。
func TestQuotaEnforcer_StreakThenFire(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 100, 0, 0)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 120 << 20}} // 120MB > 100MB*1.1=110MB
	e, _ := newEnforcer(t, db, metrics)

	// 第 1、2 拍：仅累计计数，不触发（Fired 未达成）。
	e.evaluate()
	e.evaluate()
	var fired int64
	require.NoError(t, db.Model(&model.AlertEvent{}).Count(&fired).Error)
	require.Zero(t, fired)

	// 第 3 拍：达成阈值 → alert 档触发告警 + 审计。
	e.evaluate()
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.quota_exceeded").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)

	// 第 4 拍：已触发后不重复触发（避免每拍刷审计）。
	e.evaluate()
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.quota_exceeded").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
	_ = inst
}

// TestQuotaEnforcer_TransientSpikeDoesNotFire 瞬时峰值（未连续 K 次）不触发。
func TestQuotaEnforcer_TransientSpikeDoesNotFire(t *testing.T) {
	db := quotaTestDB(t)
	makeQuotaInstance(t, db, "a", 0, 100, 0, 0)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 120 << 20}}
	e, _ := newEnforcer(t, db, metrics)

	e.evaluate() // 超限 1 次
	metrics.sample = quotaSample{RSSBytes: 50 << 20}
	e.evaluate() // 回落 → 计数清零
	metrics.sample = quotaSample{RSSBytes: 120 << 20}
	e.evaluate() // 又超 1 次（不足 K=3）

	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Count(&auditCount).Error)
	require.Zero(t, auditCount, "抖动序列不得触发处置")
}

// TestQuotaEnforcer_RecoverAfterStreak 回落连续 K 次解除强制状态。
func TestQuotaEnforcer_RecoverAfterStreak(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 100, 0, 0)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 120 << 20}}
	e, _ := newEnforcer(t, db, metrics)
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	require.True(t, e.enforced(inst.ID, quotaKindMem))

	metrics.sample = quotaSample{RSSBytes: 20 << 20}
	e.evaluate()
	e.evaluate()
	require.True(t, e.enforced(inst.ID, quotaKindMem), "未连续 K 次回落前不解除")
	e.evaluate()
	require.False(t, e.enforced(inst.ID, quotaKindMem), "连续 K 次正常应解除")

	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.quota_recovered").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
}

// enforced 读某维度的强制状态（测试辅助）。
func (e *QuotaEnforcer) enforced(instanceID uint, kind quotaKind) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.states[instanceID]
	if st == nil {
		return false
	}
	return st.Fired[kind]
}

// TestQuotaEnforcer_StopModeStopsInstance stop 档：置 statusReason 并停止实例。
func TestQuotaEnforcer_StopModeStopsInstance(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 51, MaxStorageMB: 1000, EnforceMode: "stop"}).Error)
	inst := makeQuotaInstance(t, db, "a", 0, 64, 0, 51)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 128 << 20}}
	e, _ := newEnforcer(t, db, metrics)

	pool := newStopRecordingPool(t, db, inst)
	e.SetInstanceService(NewInstanceService(db, NewGroupService(db), pool))

	for i := 0; i < 3; i++ {
		e.evaluate()
	}

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Contains(t, got.StatusReason, "配额超限已停止", "stop 档必须置 statusReason")
	require.NotEqual(t, model.InstanceStatusRunning, got.Status)

	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.quota_stopped").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
}

// TestQuotaEnforcer_CPUAlertOnly 非 docker CPU 超限只告警、不误停（spec §2.4 明确判定）。
func TestQuotaEnforcer_CPUAlertOnly(t *testing.T) {
	db := quotaTestDB(t)
	// CPU 1 核 = 100%，阈值 110%。用量 150% 持续超限。
	inst := makeQuotaInstance(t, db, "a", 1.0, 0, 0, 0)
	metrics := &stubQuotaMetrics{sample: quotaSample{CPUPercent: 150}}
	e, _ := newEnforcer(t, db, metrics)

	pool := newStopRecordingPool(t, db, inst)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	e.SetInstanceService(instSvc)

	for i := 0; i < 5; i++ {
		e.evaluate()
	}

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status, "alert 档 CPU 超限不得误停")
	require.Empty(t, got.StatusReason)

	// 告警与审计已触发（运行期强制确实生效，只是不动进程）。
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.quota_exceeded").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
}

// TestQuotaEnforcer_ThrottleOnNonDockerDegradesHonestly 非 docker throttle 档降级为告警并**明确标注无法限流**。
func TestQuotaEnforcer_ThrottleOnNonDockerDegradesHonestly(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 61, MaxStorageMB: 1000, EnforceMode: "throttle"}).Error)
	inst := makeQuotaInstance(t, db, "a", 0, 64, 0, 61)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 128 << 20}}
	e, _ := newEnforcer(t, db, metrics)

	for i := 0; i < 3; i++ {
		e.evaluate()
	}

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status, "throttle 档不得停服")

	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "instance.quota_throttle_unsupported").First(&audit).Error)
	require.True(t, audit.Failed, "降级为告警应记为「未按请求限流」")
	require.Contains(t, audit.Error, "非 docker", "审计必须明确标注能力边界，不能假装已限流")
}

// TestQuotaEnforcer_ThrottleOnDockerRecordsIntent docker throttle 档：**真的落「待收紧限额」**。
//
// M-1 缺陷现场：原实现只写一条 success=true 的审计 + 文案「下次启动生效」，
// 全仓没有任何字段/表承载这个意图、也没有启动路径读取它 → 限流永不发生。
// 本测试断言：①待收紧限额真的落库；②启动规格翻译会把它并进 cgroup 限额（取较小值）。
func TestQuotaEnforcer_ThrottleOnDockerRecordsIntent(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 71, MaxStorageMB: 1000, EnforceMode: "throttle"}).Error)
	inst := makeQuotaInstance(t, db, "a", 0, 64, 0, 71)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("process_type", model.ProcessTypeDocker).Error)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 128 << 20}}
	e, _ := newEnforcer(t, db, metrics)

	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.quota_throttled").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)

	// ① 待收紧限额必须落库（否则「已登记限流」只是幻觉）。
	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Greater(t, got.ThrottleMemLimitMB, int64(0), "必须持久化待收紧的内存限额")
	// 生效限额 64MiB × 0.9 = 57MiB（严格小于配置限额，才是真正的「收紧」）。
	require.EqualValues(t, 57, got.ThrottleMemLimitMB,
		"收紧目标是限额的 90%，必须严格低于配置限额")

	// ② 启动规格翻译消费它：生效限额 = min(配置, 待收紧)。
	require.Equal(t, effectiveMemLimitMB(&got), minPositiveInt64(got.MemLimitMB, got.ThrottleMemLimitMB))
}

// TestQuotaThrottleCPU_TightensAcrossHalfCoreDomain N-2：**CPU 维全失效域**必须真的被收紧。
//
// 缺陷现场：target 用 roundUpToHalfCore(C×0.9) **向上**取半核整倍数，而 tightenLimit 要求
// candidate < configured。于是凡是「configured 恰为半核整倍数」的限额（0.5/1.0/1.5/…/4.5
// ——运维最常用的取值）都有 target == configured → 被拒绝 → 落到「无可进一步收紧」分支，
// CPU 限流**永不发生**（M-1 原缺陷在 CPU 维以另一形态存活）。内存维直接取 0.9 不取整，故正常。
//
// 本测试逐个覆盖 0.5..4.5 半核值 + 4.75/5/8（原先仅后三者生效），断言：
// ①收紧目标**严格小于** configured 且真的落库；②启动规格翻译（effectiveCPULimit）采纳它。
func TestQuotaThrottleCPU_TightensAcrossHalfCoreDomain(t *testing.T) {
	// 0.5 与 1.0 直到 4.5 的每一个半核整倍数：全部是修复前「恒不生效」的失效域。
	limits := []float64{0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 4.75, 5.0, 8.0}
	for _, limit := range limits {
		t.Run(fmt.Sprintf("limit=%.2f", limit), func(t *testing.T) {
			db := quotaTestDB(t)
			require.NoError(t, db.Create(&model.GroupQuota{
				GroupID: 91, MaxStorageMB: 1000, EnforceMode: "throttle",
			}).Error)
			inst := makeQuotaInstance(t, db, "cpu", limit, 0, 0, 91)
			require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
				Update("process_type", model.ProcessTypeDocker).Error)
			// CPU 用量取限额的 150%：稳定越过 quotaCPUHeadroomRatio=1.1 的判定阈值。
			metrics := &stubQuotaMetrics{sample: quotaSample{CPUPercent: limit * 100 * 1.5}}
			e, _ := newEnforcer(t, db, metrics)

			var got model.Instance
			require.NoError(t, db.First(&got, inst.ID).Error)
			require.Zero(t, got.ThrottleCPULimit, "前置：尚未登记待收紧值")

			for i := 0; i < 3; i++ {
				e.evaluate()
			}
			require.NoError(t, db.First(&got, inst.ID).Error)
			require.Greater(t, got.ThrottleCPULimit, float64(0),
				"限额 %.2f 核超限后必须登记待收紧限额（修复前此处恒为 0）", limit)
			require.Less(t, got.ThrottleCPULimit, limit,
				"待收紧值必须严格小于配置限额（否则不是收紧）")
			require.InDelta(t, limit*quotaThrottleTightenRatio, got.ThrottleCPULimit, 1e-9,
				"收紧目标是限额的 90%%，对任意小数限额都成立")

			// 启动规格翻译消费它：生效限额 = min(配置, 待收紧)。
			require.Equal(t, got.ThrottleCPULimit, effectiveCPULimit(&got),
				"启动时 cgroup 限额必须采用收紧后的值")
		})
	}
}

// TestQuotaThrottleCPU_TightenNeverLowersIllegalZero N-2 反向：收紧不得产出 0/负值。
//
// 「向下取半核整倍数」是错误修法：0.50×0.9=0.45 向下取整会得到 0.0，而 0 在 docker 语义里
// 是「不限制」——比不收紧更糟（等于放开限额）。故实现直接取 90% 原值，本用例锁定该性质。
func TestQuotaThrottleCPU_TightenNeverLowersIllegalZero(t *testing.T) {
	for _, limit := range []float64{0.05, 0.1, 0.2, 0.25, 0.5} {
		target := limit * quotaThrottleTightenRatio
		require.Greater(t, target, float64(0), "限额 %.2f 的收紧目标不得为 0（0=docker 不限制）", limit)
		require.Less(t, target, limit, "限额 %.2f 的收紧目标必须严格更小", limit)
		require.Equal(t, target, tightenLimit(0, limit, target), "收紧目标必须被采纳")
	}
	// 极小限额：即使目标是 1e-9 量级，docker 侧亦按 NanoCPUs 接受（>0 且 ≤ 宿主核数）。
	require.Greater(t, tightenLimit(0, 0.01, 0.009), float64(0))
}

// TestQuotaThrottle_TightensOneWay 收紧是单向的：登记值不得放宽显式配置的限额。
func TestQuotaThrottle_TightensOneWay(t *testing.T) {
	// 配置 2 核、待收紧 3 核 → 不采纳（不能放宽），保持原值（0=无待收紧项）。
	require.Equal(t, float64(0), tightenLimit(0, 2, 3))
	// 配置 0（未设限）、目标 1.5 核 → 采纳。
	require.Equal(t, 1.5, tightenLimit(0, 0, 1.5))
	// 已有更严的 1 核、目标 1.5 核 → 保持 1 核（只收紧不放宽）。
	require.Equal(t, 1.0, tightenLimit(1, 4, 1.5))
	// 目标非法（<=0）→ 不改动。
	require.Equal(t, 0.5, tightenLimit(0.5, 0, 0))

	require.Equal(t, int64(128), tightenLimitInt64(0, 0, 128))
	// 配置 64MiB 已比目标 256MiB 更严 → 不采纳目标，保持原值（0=不新增待收紧项）。
	require.Equal(t, int64(0), tightenLimitInt64(0, 64, 256))

	// 生效限额合并：两边都设时取小。
	inst := &model.Instance{MemLimitMB: 1024, ThrottleMemLimitMB: 512}
	require.Equal(t, int64(512), effectiveMemLimitMB(inst))
	inst2 := &model.Instance{CPULimit: 1.5, ThrottleCPULimit: 3}
	require.Equal(t, 1.5, effectiveCPULimit(inst2))
	require.Equal(t, 1.5, effectiveCPULimit(&model.Instance{ThrottleCPULimit: 1.5}))
}

// TestQuotaEnforcer_RecoverResolvesActiveAlert s-3：恢复必须**真正解除**活跃告警。
//
// 缺陷现场：Fire 时标了 Resolvable=true，但恢复路径只写审计、从不调 dispatcher.Resolve
// → 告警永久停留在活跃，且「再次超限」也不会重新告警（活跃事件仍在，分发器认为已告哨过）。
func TestQuotaEnforcer_RecoverResolvesActiveAlert(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 64, 0, 0)
	// 配额告警规则（实例级目标）。
	require.NoError(t, db.Create(&model.AlertRule{
		Name: "配额告警", Enabled: true, TriggerType: model.AlertTriggerQuotaExceeded,
		TargetID: &inst.ID, Level: model.AlertLevelWarn,
	}).Error)

	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 128 << 20}}
	e, _ := newEnforcer(t, db, metrics)
	e.SetDispatcher(NewAlertDispatcher(db))

	// 3 拍超限 → Fire。
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	var activeCount int64
	require.NoError(t, db.Model(&model.AlertEvent{}).
		Where("target_id = ? AND resolved = ?", inst.ID, false).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount, "超限应产生一条活跃告警")

	// 用量回落 → 连续 3 拍正常后 Recovered → Resolve。
	metrics.sample = quotaSample{RSSBytes: 8 << 20}
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	require.NoError(t, db.Model(&model.AlertEvent{}).
		Where("target_id = ? AND resolved = ?", inst.ID, false).Count(&activeCount).Error)
	require.EqualValues(t, 0, activeCount, "回落必须真正解除活跃告警（s-3）")

	// 再次超限必须能重新告警（验证 Resolve 后不是「永久静默」）。
	metrics.sample = quotaSample{RSSBytes: 128 << 20}
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	require.NoError(t, db.Model(&model.AlertEvent{}).
		Where("target_id = ? AND resolved = ?", inst.ID, false).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount, "恢复后再次超限必须重新产生活跃告警")
}

// TestQuotaEnforcer_DiskDimension 磁盘维度参与判定（三档全支持）。
func TestQuotaEnforcer_DiskDimension(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 0, 100, 0) // 仅磁盘限额 100MB
	metrics := &stubQuotaMetrics{disk: 150 << 20}
	e, _ := newEnforcer(t, db, metrics)

	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	require.True(t, e.enforced(inst.ID, quotaKindDisk))

	var audit model.AuditLog
	require.NoError(t, db.Where("action = ?", "instance.quota_exceeded").First(&audit).Error)
	require.Contains(t, audit.Detail, "disk")
}

// TestQuotaEnforcer_NoLimitsSkipped 无任何限额的实例不做采样（避免全平台白做开销）。
func TestQuotaEnforcer_NoLimitsSkipped(t *testing.T) {
	db := quotaTestDB(t)
	makeQuotaInstance(t, db, "a", 0, 0, 0, 0)
	metrics := &stubQuotaMetrics{sample: quotaSample{RSSBytes: 1 << 30}}
	e, _ := newEnforcer(t, db, metrics)

	e.evaluate()
	require.Zero(t, metrics.calls, "无限额实例不应被采样")
}

// TestQuotaEnforcer_Status 配额状态视图：限额来源 + 实时用量 + 支持性。
func TestQuotaEnforcer_Status(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 81, MaxStorageMB: 1000}).Error)
	inst := makeQuotaInstance(t, db, "a", 1.5, 512, 0, 81)
	metrics := &stubQuotaMetrics{sample: quotaSample{CPUPercent: 42, RSSBytes: 256 << 20}, disk: 300 << 20}
	e, _ := newEnforcer(t, db, metrics)

	status, err := e.Status(inst.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, status.CPUCores)
	require.Equal(t, int64(512), status.MemLimitMB)
	require.Equal(t, int64(1000), status.DiskLimitMB)
	require.Equal(t, "group", status.DiskSource)
	require.Equal(t, "instance", status.MemSource)
	require.Equal(t, "alert", status.EnforceMode)
	require.Equal(t, 42.0, status.CPUPercent)
	require.Equal(t, int64(256<<20), status.RSSBytes)
	require.Equal(t, int64(300<<20), status.DiskBytes)
	require.False(t, status.SupportedThrottle, "daemon 模式不支持内核级限流")

	_, err = e.Status(999999)
	require.ErrorIs(t, err, ErrQuotaInstanceNotFound)
}

// TestQuotaEnforcer_StatusNotRunning 未运行实例不取实时用量并给出说明。
func TestQuotaEnforcer_StatusNotRunning(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "a", 0, 128, 0, 0)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)
	metrics := &stubQuotaMetrics{}
	e, _ := newEnforcer(t, db, metrics)

	status, err := e.Status(inst.ID)
	require.NoError(t, err)
	require.Contains(t, status.SampleNote, "未运行")
	require.Zero(t, metrics.calls)
}

// TestQuotaEnforceModeValidation 档位枚举校验。
func TestQuotaEnforceModeValidation(t *testing.T) {
	require.True(t, ValidEnforceMode("alert"))
	require.True(t, ValidEnforceMode("throttle"))
	require.True(t, ValidEnforceMode("stop"))
	require.False(t, ValidEnforceMode(""))
	require.False(t, ValidEnforceMode("delete"))
}

// TestQuotaEnforcer_RestoreClearsThrottleOnRecover R7 复现：超限解除后必须**清零**待收紧限额。
//
// 缺陷现场：`ThrottleCPULimit` / `ThrottleMemLimitMB` 全仓唯一写入点是 throttleForQuota 的
// **收紧**值（quota_enforce.go:477/484），**无任何清零路径**。于是一旦某实例超限过一次，
// 之后每次启动都取 `minPositiveFloat(CPULimit, ThrottleCPULimit)`（instance.go:1679/1684）
// → 实例被永久钉在 90% 硬顶，即使运维扩容/清理使超限早已解除。更糟的是配额视图
// （quota_enforce.go:703-704 回显 ThrottleCPULimit）在已解除时仍显示旧值，
// 运维看到「无超限」+「有限额」自相矛盾。
//
// 本用例走完整链路：超限 → 登记收紧 → 解除（恢复分支）→ 断言两字段已清零。
func TestQuotaEnforcer_RestoreClearsThrottleOnRecover(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 72, MaxStorageMB: 1000, EnforceMode: "throttle"}).Error)
	// 2 核 / 64MiB 限额；CPU 与内存两维都会超限（用量取限额的 150%）。
	inst := makeQuotaInstance(t, db, "a", 2, 64, 0, 72)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("process_type", model.ProcessTypeDocker).Error)
	metrics := &stubQuotaMetrics{sample: quotaSample{CPUPercent: 300, RSSBytes: 96 << 20}}
	e, _ := newEnforcer(t, db, metrics)

	// ① 超限 3 拍 → 登记收紧（两维）。
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	var tightened model.Instance
	require.NoError(t, db.First(&tightened, inst.ID).Error)
	require.Greater(t, tightened.ThrottleCPULimit, float64(0), "前置：CPU 待收紧值已登记")
	require.Greater(t, tightened.ThrottleMemLimitMB, int64(0), "前置：内存待收紧值已登记")
	// 收紧值确实生效（启动规格翻译采纳它）。
	require.Equal(t, tightened.ThrottleCPULimit, effectiveCPULimit(&tightened))

	// ② 用量回落（模拟运维扩容/清理）→ 连续 3 拍正常后 Recovered。
	metrics.sample = quotaSample{CPUPercent: 10, RSSBytes: 8 << 20}
	for i := 0; i < 3; i++ {
		e.evaluate()
	}

	// ③ 断言：解除即清零（修复前此处仍是收紧值 → 实例被永久钉在 90%）。
	var recovered model.Instance
	require.NoError(t, db.First(&recovered, inst.ID).Error)
	require.Zero(t, recovered.ThrottleCPULimit,
		"超限解除后必须清零待收紧值（否则永久钉在 90%% 硬顶）")
	require.Zero(t, recovered.ThrottleMemLimitMB, "同维度独立清理")
	// 生效限额自然回落到运维配置的限额，无需改 effective* 逻辑。
	require.Equal(t, float64(2), effectiveCPULimit(&recovered), "清零后生效限额回落到配置值")
	require.EqualValues(t, 64, effectiveMemLimitMB(&recovered))

	// ④ 恢复审计仍要写（清零是附加语义，不得取代既有 s-3 恢复链路）。
	var recoveredAudits int64
	require.NoError(t, db.Model(&model.AuditLog{}).
		Where("action = ? AND failed = ?", "instance.quota_recovered", false).Count(&recoveredAudits).Error)
	require.EqualValues(t, 2, recoveredAudits, "CPU 与内存两维各写一条恢复审计")

	// ⑤ 配额视图不得再回显已解除的旧限额（否则运维看到自相矛盾的视图）。
	status, err := e.Status(inst.ID)
	require.NoError(t, err)
	require.Zero(t, status.ThrottleCPULimit, "已解除的收紧值不得继续出现在配额视图")
	require.Zero(t, status.ThrottleMemLimitMB)
}

// TestQuotaEnforcer_ClearThrottlePerDimension R7 部分恢复：按维度**独立**清零。
//
// 只清了 CPU 但内存仍超限时，内存的待收紧值必须保留——否则「清了不该清的」会把
// 仍在生效的内存防护一起撤掉，等于用修复动作制造新缺陷。
func TestQuotaEnforcer_ClearThrottlePerDimension(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 73, MaxStorageMB: 1000, EnforceMode: "throttle"}).Error)
	inst := makeQuotaInstance(t, db, "a", 2, 64, 0, 73)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("process_type", model.ProcessTypeDocker).Error)
	// 前置：两维均已登记待收紧值（先超限再回落，模拟「曾超限」的历史状态）。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Updates(map[string]any{"throttle_cpu_limit": 1.8, "throttle_mem_limit_mb": 57}).Error)
	metrics := &stubQuotaMetrics{sample: quotaSample{CPUPercent: 10, RSSBytes: 96 << 20}} // CPU 正常、内存超限
	e, _ := newEnforcer(t, db, metrics)

	// 先制造 CPU 维的已触发状态（CPU 超限 3 拍）。
	metrics.sample = quotaSample{CPUPercent: 300, RSSBytes: 8 << 20}
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	// 内存维也超限 3 拍（此时 CPU 已回落，内存触发）。
	metrics.sample = quotaSample{CPUPercent: 10, RSSBytes: 96 << 20}
	for i := 0; i < 3; i++ {
		e.evaluate()
	}
	// 拉长 CPU 正常拍数，使其达到恢复阈值 → 只清 CPU。
	for i := 0; i < 3; i++ {
		e.evaluate()
	}

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Zero(t, got.ThrottleCPULimit, "CPU 已恢复 → 只清 CPU 的待收紧值")
	require.Greater(t, got.ThrottleMemLimitMB, int64(0),
		"内存仍超限 → 必须保留内存待收紧值（部分恢复不得误清仍在生效的维度）")
}
