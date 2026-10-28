package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-513：主机级观测的 API 契约。
//
// 核心验收点（spec §5 验收 2）：**null 与 0 必须可分**。
// 老 Worker 不报这些字段时是 null，前端渲染 `--`；上报了、读数确实是 0 时是 0。
// 把「不知道」画成 0 会让运维以为机器空闲，是这一类功能最容易犯的错。

func newHostObsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:hostobs_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}

	// 关闭底层连接（共享内存库在最后一个连接关闭后才释放，见 testdb_test.go）。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(
		&model.Node{}, &model.HostMountSnapshot{}, &model.HostProcessSnapshot{},
		&model.ProcessMetricSnapshot{}, &model.Instance{},
	); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// TestAttributionNode_NullWhenNotReported 老 Worker 未上报时新字段全为 nil。
func TestAttributionNode_NullWhenNotReported(t *testing.T) {
	node := model.Node{
		UUID: "u-null", Name: "n", Status: model.NodeStatusOnline,
		CPUCores: 4, MemoryMB: 8192, LastHeartbeat: ptrTime(time.Now()),
	}
	item := attributionNode(node, ResourceFreshnessFresh)

	if item.Load5Pct != nil || item.Load15Pct != nil {
		t.Fatal("未上报负载三档时应为 nil，不能填 0")
	}
	if item.ProcsRunning != nil || item.ProcsTotal != nil {
		t.Fatal("未上报进程数时应为 nil")
	}
	if item.CPUBreakdown != nil {
		t.Fatal("未上报 CPU 构成时应为 nil")
	}
}

// TestAttributionNode_ZeroIsNotNil 上报了 0 就必须下发 0，不能被当成「没上报」。
func TestAttributionNode_ZeroIsNotNil(t *testing.T) {
	zero := int32(0)
	zeroLoad := 0.0
	node := model.Node{
		UUID: "u-zero", Name: "n", Status: model.NodeStatusOnline,
		CPUCores: 4, MemoryMB: 8192, LastHeartbeat: ptrTime(time.Now()),
		ProcsRunning: &zero, ProcsTotal: &zero,
		LoadAvg5: &zeroLoad, LoadAvg15: &zeroLoad,
	}
	item := attributionNode(node, ResourceFreshnessFresh)

	if item.ProcsRunning == nil || *item.ProcsRunning != 0 {
		t.Fatal("上报 0 应下发 0（非 nil）")
	}
	if item.Load5Pct == nil || *item.Load5Pct != 0 {
		t.Fatal("负载 0 应下发 0（非 nil）")
	}
}

// TestAttributionNode_LoadNormalizedByCores 负载三档按核数归一，与 LoadPct 同口径。
func TestAttributionNode_LoadNormalizedByCores(t *testing.T) {
	l5, l15 := 8.0, 4.0
	node := model.Node{
		UUID: "u-load", Name: "n", Status: model.NodeStatusOnline,
		CPUCores: 4, MemoryMB: 8192, LastHeartbeat: ptrTime(time.Now()),
		LoadAvg5: &l5, LoadAvg15: &l15,
	}
	item := attributionNode(node, ResourceFreshnessFresh)

	// 8/4*100 = 200%
	if item.Load5Pct == nil || *item.Load5Pct != 200 {
		t.Fatalf("Load5Pct 应为 200，实际 %v", item.Load5Pct)
	}
	// 4/4*100 = 100%
	if item.Load15Pct == nil || *item.Load15Pct != 100 {
		t.Fatalf("Load15Pct 应为 100，实际 %v", item.Load15Pct)
	}
}

// TestAttributionNode_StaleSkipsHostFields 过期快照不带主机级字段。
//
// 过期数据的磁盘/负载没有参考价值，带出去只会让人按旧数据决策。
func TestAttributionNode_StaleSkipsHostFields(t *testing.T) {
	cpu := float32(0.5)
	running := int32(10)
	node := model.Node{
		UUID: "u-stale", Name: "n", Status: model.NodeStatusOffline,
		CPUCores: 4, ProcsRunning: &running,
		CPUUser: &cpu, CPUIdle: &cpu, CPUSystem: &cpu,
	}
	item := attributionNode(node, ResourceFreshnessStale)

	if item.ProcsRunning != nil || item.CPUBreakdown != nil {
		t.Fatal("过期快照不应下发主机级字段")
	}
}

// TestAttributionNode_BreakdownNeedsMajorComponents CPU 构成只在主分量齐全时构造。
//
// 只填一半会让前端把缺失分量画成 0，得到一条「加起来不足 100%」的假构成条。
func TestAttributionNode_BreakdownNeedsMajorComponents(t *testing.T) {
	onlyUser := float32(0.3)
	node := model.Node{
		UUID: "u-partial", Name: "n", Status: model.NodeStatusOnline,
		CPUCores: 4, MemoryMB: 8192, LastHeartbeat: ptrTime(time.Now()),
		CPUUser: &onlyUser, // 缺 idle / system
	}
	item := attributionNode(node, ResourceFreshnessFresh)
	if item.CPUBreakdown != nil {
		t.Fatal("主分量不全时不应构造构成条")
	}
}

// TestFreshHostMounts_SortedByUsage 挂载点按使用率降序（最满的排前面）。
func TestFreshHostMounts_SortedByUsage(t *testing.T) {
	db := newHostObsDB(t)
	svc := NewMetricService(db)
	now := time.Now()
	rows := []model.HostMountSnapshot{
		{NodeUUID: "u1", Mountpoint: "/", UsedPct: 30, SampledAt: now},
		{NodeUUID: "u1", Mountpoint: "/var", UsedPct: 95, SampledAt: now},
		{NodeUUID: "u1", Mountpoint: "/data", UsedPct: 60, SampledAt: now},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("建快照失败: %v", err)
		}
	}

	got, err := svc.freshHostMounts(now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	list := got["u1"]
	if len(list) != 3 {
		t.Fatalf("应有 3 个挂载点，实际 %d", len(list))
	}
	if list[0].Mountpoint != "/var" || list[1].Mountpoint != "/data" || list[2].Mountpoint != "/" {
		t.Fatalf("应按使用率降序 /var(95),/data(60),/(30)，实际 %+v", list)
	}
}

// TestFreshHostMounts_ExcludesStale 过期挂载点不出现在结果里。
func TestFreshHostMounts_ExcludesStale(t *testing.T) {
	db := newHostObsDB(t)
	svc := NewMetricService(db)
	now := time.Now()
	if err := db.Create(&model.HostMountSnapshot{
		NodeUUID: "u2", Mountpoint: "/", UsedPct: 50, SampledAt: now.Add(-24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("建快照失败: %v", err)
	}

	got, err := svc.freshHostMounts(now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got["u2"]) != 0 {
		t.Fatalf("过期挂载点不应返回，实际 %+v", got["u2"])
	}
}
