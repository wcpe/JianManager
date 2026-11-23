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

// 范围②：启动进度上报的 handler 行为。
//
// 为什么不靠真机 curl 验：这条 RPC 的调用方是 Worker 内部、且发生在注册前，
// 用外部脚本复现「注册前那一刻」既脆弱又难断言（前几轮真机验证里就出现了
// 「表里有行但不是本次写入」的误判）。进程内直接调 handler，边界清楚。

func newStartupProgressDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:startup_progress_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeStartupProgress{}, &model.NodeEnrollToken{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// stubEnroll 只实现本用例需要的方法；ValidateOnly 按 valid 返回。
type stubEnroll struct{ valid bool }

func (s stubEnroll) ConsumeForNewNode(_, _ string) (string, error) { return "", nil }
func (s stubEnroll) ValidateOnly(string) bool                      { return s.valid }

// TestReportStartupProgress_FirstInstallUsesNameKey 首装（无 UUID）按名字占位落库。
//
// 这是范围②最关键的场景：**注册之前** CP 还没有该节点的记录，进度必须能落下来，
// 否则「主控台看不到正在启动的节点」这个原始问题就没解决。
func TestReportStartupProgress_FirstInstallUsesNameKey(t *testing.T) {
	db := newStartupProgressDB(t)
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: true}}

	resp, err := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name:           "acc-node",
		Phase:          "ingest_state_load",
		PhaseElapsedMs: 3200,
		TotalElapsedMs: 4100,
		PhasesDone:     5,
		PhasesTotal:    7,
		EnrollToken:    "valid-token",
	})
	if err != nil {
		t.Fatalf("不应返回 gRPC 错误: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("应接受上报，reason=%s", resp.GetReason())
	}

	var row model.NodeStartupProgress
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("进度行未落库: %v", err)
	}
	if row.Key != "name:acc-node" {
		t.Fatalf("首装应按名字占位，实际 key=%q", row.Key)
	}
	if row.Phase != "ingest_state_load" || row.PhasesDone != 5 || row.PhasesTotal != 7 {
		t.Fatalf("字段不符: phase=%q done=%d total=%d", row.Phase, row.PhasesDone, row.PhasesTotal)
	}
}

// TestReportStartupProgress_RejectsWithoutCredential 无凭据必须拒绝。
//
// 否则任何进程都能伪造「某节点正在启动」，面板展示虚假进度——
// 观测功能反而成了误导运维的注入面。
func TestReportStartupProgress_RejectsWithoutCredential(t *testing.T) {
	db := newStartupProgressDB(t)
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: true}}

	resp, _ := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name: "acc-node", Phase: "config",
	})
	if resp.GetAccepted() {
		t.Fatal("无任何凭据时应拒绝")
	}
	var n int64
	db.Model(&model.NodeStartupProgress{}).Count(&n)
	if n != 0 {
		t.Fatalf("被拒绝的上报不得落库，实际有 %d 行", n)
	}
}

// TestReportStartupProgress_InvalidEnrollTokenRejected 无效 token 拒绝且不落库。
func TestReportStartupProgress_InvalidEnrollTokenRejected(t *testing.T) {
	db := newStartupProgressDB(t)
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: false}}

	resp, _ := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name: "acc-node", Phase: "config", EnrollToken: "bad",
	})
	if resp.GetAccepted() {
		t.Fatal("无效 enrollment token 应拒绝")
	}
}

// TestReportStartupProgress_IdentityPath 已有身份（重启场景）用 UUID 作键。
func TestReportStartupProgress_IdentityPath(t *testing.T) {
	db := newStartupProgressDB(t)
	node := model.Node{UUID: "11111111-1111-1111-1111-111111111111", Name: "n1", Secret: "s1", Host: "h"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: false}}

	resp, _ := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name: "n1", Phase: "register",
		NodeUuid: node.UUID, NodeSecret: node.Secret,
	})
	if !resp.GetAccepted() {
		t.Fatalf("有效身份应接受，reason=%s", resp.GetReason())
	}
	var row model.NodeStartupProgress
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("未落库: %v", err)
	}
	if row.Key != node.UUID {
		t.Fatalf("重启场景应以 UUID 为键，实际 %q", row.Key)
	}
}

// TestReportStartupProgress_WrongSecretRejected 身份不符拒绝（防伪造）。
func TestReportStartupProgress_WrongSecretRejected(t *testing.T) {
	db := newStartupProgressDB(t)
	node := model.Node{UUID: "22222222-2222-2222-2222-222222222222", Name: "n2", Secret: "right", Host: "h"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: true}}

	resp, _ := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name: "n2", Phase: "config", NodeUuid: node.UUID, NodeSecret: "wrong",
	})
	if resp.GetAccepted() {
		t.Fatal("secret 不匹配应拒绝")
	}
}

// TestReportStartupProgress_UpsertKeepsSingleRow 重复上报只更新、不新增行。
//
// 进度是「当前状态」而非历史：每次阶段切换上报一次，若不 upsert，
// 一个启动过程会留下 7 行，面板无法判断哪一行是当前状态。
func TestReportStartupProgress_UpsertKeepsSingleRow(t *testing.T) {
	db := newStartupProgressDB(t)
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: true}}

	phases := []string{"config", "dataroot", "identity", "ingest_state_load"}
	for i, p := range phases {
		resp, _ := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
			Name: "acc-node", Phase: p, EnrollToken: "valid-token",
			PhasesDone: int32(i + 1), PhasesTotal: 7,
		})
		if !resp.GetAccepted() {
			t.Fatalf("阶段 %s 应被接受，reason=%s", p, resp.GetReason())
		}
	}
	var n int64
	db.Model(&model.NodeStartupProgress{}).Count(&n)
	if n != 1 {
		t.Fatalf("同一节点多次上报应只保留 1 行，实际 %d 行", n)
	}
	var row model.NodeStartupProgress
	db.First(&row)
	if row.Phase != "ingest_state_load" {
		t.Fatalf("应保留最后一次阶段，实际 %q", row.Phase)
	}
}

// TestReportStartupProgress_HeartbeatPhaseClears 终态阶段删除进度行。
//
// 这条守的是一个真实的时序 bug：Worker 的 heartbeat 上报发生在**注册成功之后**，
// 若仍写入，会把注册时清理掉的进度行重新写回来，
// 导致已上线的节点在面板里永久显示「启动中 · heartbeat」。
func TestReportStartupProgress_HeartbeatPhaseClears(t *testing.T) {
	db := newStartupProgressDB(t)
	node := model.Node{UUID: "33333333-3333-3333-3333-333333333333", Name: "n3", Secret: "s3", Host: "h"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	h := &ControlPlaneHandler{db: db, enroll: stubEnroll{valid: true}}

	// 先写一条进行中的进度
	if _, err := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name: "n3", Phase: "register", NodeUuid: node.UUID, NodeSecret: node.Secret,
	}); err != nil {
		t.Fatalf("前置上报失败: %v", err)
	}
	var before int64
	db.Model(&model.NodeStartupProgress{}).Count(&before)
	if before != 1 {
		t.Fatalf("前置应有 1 行，实际 %d", before)
	}

	// 终态上报：应删除
	resp, _ := h.ReportStartupProgress(t.Context(), &workerpb.ReportStartupProgressRequest{
		Name: "n3", Phase: "heartbeat", NodeUuid: node.UUID, NodeSecret: node.Secret,
	})
	if !resp.GetAccepted() {
		t.Fatalf("终态上报应被接受，reason=%s", resp.GetReason())
	}
	var after int64
	db.Model(&model.NodeStartupProgress{}).Count(&after)
	if after != 0 {
		t.Fatalf("终态应清理进度行，实际剩 %d 行", after)
	}
}

// TestNodeStartupProgress_IsStale 陈旧判定：Worker 崩溃时进度行不会被清理，
// 必须能判「启动中断」，否则面板永远显示「启动中」。
func TestNodeStartupProgress_IsStale(t *testing.T) {
	now := time.Now()
	fresh := model.NodeStartupProgress{UpdatedAt: now.Add(-time.Minute)}
	if fresh.IsStale(now) {
		t.Fatal("1 分钟前的进度不应判陈旧")
	}
	old := model.NodeStartupProgress{UpdatedAt: now.Add(-10 * time.Minute)}
	if !old.IsStale(now) {
		t.Fatal("10 分钟前的进度应判陈旧")
	}
	if (model.NodeStartupProgress{}).IsStale(now) != true {
		t.Fatal("零值 UpdatedAt 应判陈旧")
	}
}
