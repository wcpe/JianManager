package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeSettings 固定生效值，供宽限/auto 单测。
type fakeSettings map[string]string

func (f fakeSettings) EffectiveValue(key string) string { return f[key] }

// fakeDispose 记录处置调用，可注入错误。
type fakeDispose struct {
	mu      sync.Mutex
	calls   []string // nodeUUID/instanceUUID
	err     error
	callN   int
}

func (f *fakeDispose) DisposeOrphanRuntime(ctx context.Context, nodeUUID, instanceUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callN++
	f.calls = append(f.calls, nodeUUID+"/"+instanceUUID)
	return f.err
}

func (f *fakeDispose) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callN
}

func newOrphanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.OrphanRuntime{}, &model.AuditLog{}, &model.User{}, &model.Node{}))
	return db
}

func newTracker(t *testing.T, db *gorm.DB, settings SettingsReader, dispose OrphanDisposeClient) *OrphanRuntimeTracker {
	t.Helper()
	tr := NewOrphanRuntimeTracker(db, settings, nil)
	if dispose != nil {
		tr.SetDisposeClient(dispose)
	}
	return tr
}

// TestOrphan_PendingThenCancelWithinGrace 宽限内 CP 又出现记录 → cancelled，不处置。
func TestOrphan_PendingThenCancelWithinGrace(t *testing.T) {
	db := newOrphanTestDB(t)
	disp := &fakeDispose{}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tr := newTracker(t, db, fakeSettings{
		SettingKeyOrphanGracePeriod:  "10m",
		SettingKeyOrphanAutoDispose: "false",
	}, disp)
	tr.SetNow(func() time.Time { return now })

	nodeUUID := "node-1"
	instUUID := "orphan-inst-1"

	// 首次发现：Worker 上报、CP 无记录。
	tr.ObserveHeartbeat(nodeUUID, []*workerpb.InstanceState{
		{InstanceUuid: instUUID, State: "RUNNING", Pid: 4242},
	})
	items, err := tr.List("", true, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimePending, items[0].Status)
	require.Equal(t, instUUID, items[0].InstanceUUID)
	require.Equal(t, 4242, items[0].WorkerPID)
	require.Equal(t, 0, disp.n(), "宽限内不得自动处置")

	// 宽限内（+5m）CP 写入实例记录后再心跳 → 取消。
	now = now.Add(5 * time.Minute)
	require.NoError(t, db.Create(&model.Instance{
		UUID: instUUID, Name: "restored", NodeID: 1, Status: model.InstanceStatusRunning,
	}).Error)
	tr.ObserveHeartbeat(nodeUUID, []*workerpb.InstanceState{
		{InstanceUuid: instUUID, State: "RUNNING", Pid: 4242},
	})
	items, err = tr.List("", false, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimeCancelled, items[0].Status)
	require.Equal(t, 0, disp.n())
}

// TestOrphan_AfterGrace_AutoDispose 宽限后 + auto_dispose → 下发处置并 disposed。
func TestOrphan_AfterGrace_AutoDispose(t *testing.T) {
	db := newOrphanTestDB(t)
	disp := &fakeDispose{}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tr := newTracker(t, db, fakeSettings{
		SettingKeyOrphanGracePeriod:  "10m",
		SettingKeyOrphanAutoDispose: "true",
	}, disp)
	tr.SetNow(func() time.Time { return now })
	tr.SetAudit(NewAuditService(db))

	nodeUUID := "node-auto"
	instUUID := "orphan-auto-1"

	tr.ObserveHeartbeat(nodeUUID, []*workerpb.InstanceState{
		{InstanceUuid: instUUID, State: "RUNNING", Pid: 7},
	})
	require.Equal(t, 0, disp.n())

	// 宽限后仍无 CP 记录。
	now = now.Add(11 * time.Minute)
	tr.ObserveHeartbeat(nodeUUID, []*workerpb.InstanceState{
		{InstanceUuid: instUUID, State: "RUNNING", Pid: 7},
	})
	require.Equal(t, 1, disp.n())
	require.Equal(t, []string{nodeUUID + "/" + instUUID}, disp.calls)

	items, err := tr.List(string(model.OrphanRuntimeDisposed), false, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimeDisposed, items[0].Status)
	require.Equal(t, "auto", items[0].DisposeMode)
	require.NotNil(t, items[0].DisposedAt)

	// 审计：自动处置成功。
	var audits []model.AuditLog
	require.NoError(t, db.Find(&audits).Error)
	require.NotEmpty(t, audits)
	require.Equal(t, "orphan_runtime.dispose_auto", audits[0].Action)
	require.False(t, audits[0].Failed)
}

// TestOrphan_AutoOff_OnlyList_ManualConfirm auto 关：宽限后仅 confirmed；手动确认才处置。
func TestOrphan_AutoOff_OnlyList_ManualConfirm(t *testing.T) {
	db := newOrphanTestDB(t)
	disp := &fakeDispose{}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tr := newTracker(t, db, fakeSettings{
		SettingKeyOrphanGracePeriod:  "10m",
		SettingKeyOrphanAutoDispose: "false",
	}, disp)
	tr.SetNow(func() time.Time { return now })
	tr.SetAudit(NewAuditService(db))

	nodeUUID := "node-manual"
	instUUID := "orphan-manual-1"

	tr.ObserveHeartbeat(nodeUUID, []*workerpb.InstanceState{
		{InstanceUuid: instUUID, State: "RUNNING"},
	})
	now = now.Add(11 * time.Minute)
	tr.ObserveHeartbeat(nodeUUID, []*workerpb.InstanceState{
		{InstanceUuid: instUUID, State: "RUNNING"},
	})
	require.Equal(t, 0, disp.n(), "auto 关闭不得自动杀")

	items, err := tr.List("", true, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimeConfirmed, items[0].Status)

	// 手动确认。
	out, err := tr.ConfirmDispose(items[0].UUID, 42, "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, model.OrphanRuntimeDisposed, out.Status)
	require.Equal(t, "manual", out.DisposeMode)
	require.Equal(t, 1, disp.n())

	var audits []model.AuditLog
	require.NoError(t, db.Find(&audits).Error)
	require.Equal(t, "orphan_runtime.dispose_manual", audits[0].Action)
	require.Equal(t, uint(42), audits[0].UserID)
}

// TestOrphan_OldWorkerNilReport_NoTrack 老 Worker 不上报清单（nil）→ 不启用反向对账。
func TestOrphan_OldWorkerNilReport_NoTrack(t *testing.T) {
	db := newOrphanTestDB(t)
	disp := &fakeDispose{}
	tr := newTracker(t, db, nil, disp)

	// 先有一条 pending，模拟历史。
	require.NoError(t, db.Create(&model.OrphanRuntime{
		NodeUUID: "n", InstanceUUID: "i", Status: model.OrphanRuntimePending,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}).Error)

	tr.ObserveHeartbeat("n", nil) // 老 Worker
	items, err := tr.List("", true, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimePending, items[0].Status, "nil 清单不得改写既有跟踪")
	require.Equal(t, 0, disp.n())
}

// TestOrphan_EmptyReport_CancelsActive 新 Worker 空清单 → 活跃跟踪取消（进程已不在）。
func TestOrphan_EmptyReport_CancelsActive(t *testing.T) {
	db := newOrphanTestDB(t)
	tr := newTracker(t, db, nil, &fakeDispose{})
	now := time.Now()
	tr.SetNow(func() time.Time { return now })

	require.NoError(t, db.Create(&model.OrphanRuntime{
		NodeUUID: "n", InstanceUUID: "gone", Status: model.OrphanRuntimeConfirmed,
		FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute),
	}).Error)

	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{}) // 显式空
	items, err := tr.List("", false, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimeCancelled, items[0].Status)
}

// TestOrphan_SoftDeletedInstance_IsOrphan 软删实例视为 CP 无记录 → orphan。
func TestOrphan_SoftDeletedInstance_IsOrphan(t *testing.T) {
	db := newOrphanTestDB(t)
	tr := newTracker(t, db, nil, &fakeDispose{})
	now := time.Now()
	tr.SetNow(func() time.Time { return now })

	inst := &model.Instance{UUID: "soft-1", Name: "soft", NodeID: 1, Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Delete(inst).Error) // 软删

	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{
		{InstanceUuid: "soft-1", State: "RUNNING"},
	})
	items, err := tr.List("", true, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "soft-1", items[0].InstanceUUID)
}

// TestOrphan_CPHasRecord_NoOrphan Worker 上报且 CP 有记录 → 不建 orphan。
func TestOrphan_CPHasRecord_NoOrphan(t *testing.T) {
	db := newOrphanTestDB(t)
	tr := newTracker(t, db, nil, &fakeDispose{})
	require.NoError(t, db.Create(&model.Instance{
		UUID: "known", Name: "k", NodeID: 1, Status: model.InstanceStatusRunning,
	}).Error)
	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{
		{InstanceUuid: "known", State: "RUNNING"},
	})
	items, err := tr.List("", true, 0)
	require.NoError(t, err)
	require.Empty(t, items)
}

// TestOrphan_DisposeFailure_KeepsActive 处置失败保留 confirmed 并记 lastError。
func TestOrphan_DisposeFailure_KeepsActive(t *testing.T) {
	db := newOrphanTestDB(t)
	disp := &fakeDispose{err: errors.New("boom")}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tr := newTracker(t, db, fakeSettings{
		SettingKeyOrphanGracePeriod:  "1m",
		SettingKeyOrphanAutoDispose: "true",
	}, disp)
	tr.SetNow(func() time.Time { return now })

	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{{InstanceUuid: "x", State: "RUNNING"}})
	now = now.Add(2 * time.Minute)
	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{{InstanceUuid: "x", State: "RUNNING"}})
	require.Equal(t, 1, disp.n())

	items, err := tr.List("", true, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, model.OrphanRuntimeConfirmed, items[0].Status)
	require.Contains(t, items[0].LastError, "boom")
}

// TestOrphan_DefaultSettings_NoAuto 默认 settings=nil：宽限 10m、不自动杀。
func TestOrphan_DefaultSettings_NoAuto(t *testing.T) {
	db := newOrphanTestDB(t)
	disp := &fakeDispose{}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tr := newTracker(t, db, nil, disp)
	tr.SetNow(func() time.Time { return now })

	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{{InstanceUuid: "d", State: "RUNNING"}})
	now = now.Add(11 * time.Minute)
	tr.ObserveHeartbeat("n", []*workerpb.InstanceState{{InstanceUuid: "d", State: "RUNNING"}})
	require.Equal(t, 0, disp.n())
	items, _ := tr.List("", true, 0)
	require.Equal(t, model.OrphanRuntimeConfirmed, items[0].Status)
}

// TestOrphan_ConfirmDispose_NotFound 手动处置不存在记录。
func TestOrphan_ConfirmDispose_NotFound(t *testing.T) {
	db := newOrphanTestDB(t)
	tr := newTracker(t, db, nil, &fakeDispose{})
	_, err := tr.ConfirmDispose("no-such", 1, "")
	require.ErrorIs(t, err, ErrOrphanRuntimeNotFound)
}
