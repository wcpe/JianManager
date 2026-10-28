package grpc

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-513：心跳落库的列名正确性。
//
// ## 为什么需要这组用例
//
// 这类缺陷的失败方式很隐蔽：写错列名时 GORM 报 `no such column: ...`，
// 而心跳路径**只记 WARN 不阻断**（观测数据不该让存活判定失败）——
// 于是表现成「数据一直不落库，但日志里只有一行 WARN，界面上什么都看不出来」。
// 真机排查时正是这样一轮一轮翻日志才定位到 cpu_softirq / pid 两个列名。
//
// 因此这里用**真实的 AutoMigrate 表结构**跑 upsert，让列名不匹配当场暴露。

func newHostSnapshotDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:hostsnap_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Node{}, &model.HostMountSnapshot{}, &model.HostProcessSnapshot{},
	); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// TestUpsertHostSnapshots_WritesBothTables 两张快照表都能写入（列名正确）。
func TestUpsertHostSnapshots_WritesBothTables(t *testing.T) {
	db := newHostSnapshotDB(t)
	h := &ControlPlaneHandler{db: db}

	req := &workerpb.HeartbeatRequest{
		NodeUuid: "11111111-1111-1111-1111-111111111111",
		HostMounts: []*workerpb.HostMountUsage{
			{Mountpoint: "/", TotalBytes: 100, UsedBytes: 60, UsedPct: 60},
			{Mountpoint: "/var", TotalBytes: 200, UsedBytes: 190, UsedPct: 95},
		},
		HostTopProcesses: []*workerpb.HostProcessSample{
			{Pid: 42, Comm: "java", CpuPct: 88.5, RssBytes: 1024},
		},
	}

	if err := h.upsertHostSnapshots(req); err != nil {
		t.Fatalf("写入应成功（列名不匹配会在这里暴露）: %v", err)
	}

	var mounts []model.HostMountSnapshot
	if err := db.Find(&mounts).Error; err != nil {
		t.Fatalf("查询挂载点失败: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("应有 2 条挂载点，实际 %d", len(mounts))
	}

	var procs []model.HostProcessSnapshot
	if err := db.Find(&procs).Error; err != nil {
		t.Fatalf("查询进程失败: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("应有 1 条进程快照，实际 %d", len(procs))
	}
	if procs[0].PID != 42 || procs[0].Comm != "java" {
		t.Fatalf("进程字段不符: %+v", procs[0])
	}
}

// TestUpsertHostSnapshots_IdempotentUpsert 重复上报只更新、不新增行。
//
// 用 upsert 而非「先删后插」：先删后插在两次心跳之间有一瞬间查询为空，
// 前端轮询正好落在那个窗口就会看到数据闪一下消失。
func TestUpsertHostSnapshots_IdempotentUpsert(t *testing.T) {
	db := newHostSnapshotDB(t)
	h := &ControlPlaneHandler{db: db}
	uuid := "22222222-2222-2222-2222-222222222222"

	first := &workerpb.HeartbeatRequest{
		NodeUuid: uuid,
		HostMounts: []*workerpb.HostMountUsage{
			{Mountpoint: "/", TotalBytes: 100, UsedBytes: 50, UsedPct: 50},
		},
		HostTopProcesses: []*workerpb.HostProcessSample{
			{Pid: 7, Comm: "nginx", CpuPct: 10},
		},
	}
	if err := h.upsertHostSnapshots(first); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	second := &workerpb.HeartbeatRequest{
		NodeUuid: uuid,
		HostMounts: []*workerpb.HostMountUsage{
			{Mountpoint: "/", TotalBytes: 100, UsedBytes: 80, UsedPct: 80},
		},
		HostTopProcesses: []*workerpb.HostProcessSample{
			{Pid: 7, Comm: "nginx", CpuPct: 30},
		},
	}
	if err := h.upsertHostSnapshots(second); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}

	var mountCount, procCount int64
	db.Model(&model.HostMountSnapshot{}).Count(&mountCount)
	db.Model(&model.HostProcessSnapshot{}).Count(&procCount)
	if mountCount != 1 || procCount != 1 {
		t.Fatalf("应各保留 1 行，实际挂载点 %d 进程 %d", mountCount, procCount)
	}

	var m model.HostMountSnapshot
	db.First(&m)
	if m.UsedPct != 80 {
		t.Fatalf("挂载点应被更新为 80，实际 %v", m.UsedPct)
	}
	var p model.HostProcessSnapshot
	db.First(&p)
	if p.CPUPct != 30 {
		t.Fatalf("进程 CPU 应被更新为 30，实际 %v", p.CPUPct)
	}
}

// TestUpsertHostSnapshots_SkipsInvalidRows 缺关键字段的行被跳过，不产生垃圾数据。
func TestUpsertHostSnapshots_SkipsInvalidRows(t *testing.T) {
	db := newHostSnapshotDB(t)
	h := &ControlPlaneHandler{db: db}

	req := &workerpb.HeartbeatRequest{
		NodeUuid: "33333333-3333-3333-3333-333333333333",
		HostMounts: []*workerpb.HostMountUsage{
			{Mountpoint: "", UsedPct: 50}, // 缺挂载点路径
			{Mountpoint: "/", UsedPct: 60},
		},
		HostTopProcesses: []*workerpb.HostProcessSample{
			{Pid: 0, Comm: "invalid"}, // PID 0 无效
			{Pid: 9, Comm: "ok"},
		},
	}
	if err := h.upsertHostSnapshots(req); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	var mounts, procs int64
	db.Model(&model.HostMountSnapshot{}).Count(&mounts)
	db.Model(&model.HostProcessSnapshot{}).Count(&procs)
	if mounts != 1 || procs != 1 {
		t.Fatalf("无效行应被跳过，实际挂载点 %d 进程 %d", mounts, procs)
	}
}

// TestUpsertHostSnapshots_EmptyRequestIsNoop 空请求不报错也不写数据。
func TestUpsertHostSnapshots_EmptyRequestIsNoop(t *testing.T) {
	db := newHostSnapshotDB(t)
	h := &ControlPlaneHandler{db: db}

	if err := h.upsertHostSnapshots(&workerpb.HeartbeatRequest{NodeUuid: "x"}); err != nil {
		t.Fatalf("空上报不应报错: %v", err)
	}
	if err := h.upsertHostSnapshots(nil); err != nil {
		t.Fatalf("nil 请求不应报错: %v", err)
	}
	var mounts, procs int64
	db.Model(&model.HostMountSnapshot{}).Count(&mounts)
	db.Model(&model.HostProcessSnapshot{}).Count(&procs)
	if mounts != 0 || procs != 0 {
		t.Fatal("空上报不应写入任何行")
	}
}

// TestNodeHostColumnsMatchModel 节点表里 FR-513 的列必须都能被 GORM 识别。
//
// 这条守的正是本轮真机踩到的坑：往 updates map 里写了一个不存在的列名
// （cpu_softirq vs 实际 cpu_soft_irq），GORM 直接报 SQL 错误，
// 而心跳路径只记 WARN——数据静默不落库。
func TestNodeHostColumnsMatchModel(t *testing.T) {
	db := newHostSnapshotDB(t)
	node := model.Node{UUID: "44444444-4444-4444-4444-444444444444", Name: "n", Host: "h", Secret: "s"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("建节点失败: %v", err)
	}

	zerof := float32(0.25)
	zeroi := int32(3)
	// 与 handler 里 updates map 的键**逐字一致**：任何拼写不符都会在这里报 no such column。
	updates := map[string]any{
		"load_avg5": 1.5, "load_avg15": 1.0,
		"procs_running": zeroi, "procs_total": zeroi,
		"cpu_user": zerof, "cpu_nice": zerof, "cpu_system": zerof,
		"cpu_idle": zerof, "cpu_irq": zerof, "cpu_soft_irq": zerof, "cpu_steal": zerof,
	}
	if err := db.Model(&model.Node{}).Where("uuid = ?", node.UUID).Updates(updates).Error; err != nil {
		t.Fatalf("更新失败（列名与模型不一致）: %v", err)
	}

	var got model.Node
	if err := db.Where("uuid = ?", node.UUID).First(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.ProcsTotal == nil || *got.ProcsTotal != 3 {
		t.Fatalf("procs_total 应落为 3，实际 %v", got.ProcsTotal)
	}
	if got.LoadAvg15 == nil || *got.LoadAvg15 != 1.0 {
		t.Fatalf("load_avg15 应落为 1.0，实际 %v", got.LoadAvg15)
	}
	_ = time.Now
}
