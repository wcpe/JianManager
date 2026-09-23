package grpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeOrphanAuditRecorder 记录落库调用。
type fakeOrphanAuditRecorder struct {
	calls []struct {
		action    string
		target    string
		detail    string
		success   bool
		errMsg    string
		targetTyp string
	}
}

func (f *fakeOrphanAuditRecorder) RecordResultSafe(_ uint, action, targetType, targetID, detail, _ string, success bool, errMsg string) {
	f.calls = append(f.calls, struct {
		action    string
		target    string
		detail    string
		success   bool
		errMsg    string
		targetTyp string
	}{action, targetID, detail, success, errMsg, targetType})
}

func newOrphanAuditFixture(t *testing.T) (*ControlPlaneHandler, *gorm.DB, *model.Node, *fakeOrphanAuditRecorder) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	// Instance 表须存在：target↔node 绑定校验会按 targetId 查实例归属。
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{UUID: "node-orphan-audit", Name: "n", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	h := NewControlPlaneHandler(db, NewClientPool())
	rec := &fakeOrphanAuditRecorder{}
	h.SetOrphanAuditRecorder(rec)
	return h, db, node, rec
}

// seedOrphanAuditInstance 造一个属于 nodeID 的实例（供 target↔node 绑定用例）。
func seedOrphanAuditInstance(t *testing.T, db *gorm.DB, uuid string, nodeID uint) {
	t.Helper()
	require.NoError(t, db.Create(&model.Instance{
		UUID:         uuid,
		NodeID:       nodeID,
		Name:         "i-" + uuid,
		Type:         model.InstanceTypeMinecraftJava,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar",
	}).Error)
}

// TestReportOrphanAudit_Records FR-455/456：携带正确身份的孤儿审计应落 CP 审计库。
func TestReportOrphanAudit_Records(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)

	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid:   node.UUID,
		NodeSecret: node.Secret,
		Action:     "orphan.scan_dispose_blocked",
		TargetId:   "inst-1",
		Detail:     `{"kind":"docker_leftover"}`,
		Success:    false,
		Error:      "容器仍 running，保守不删",
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 1)
	require.Equal(t, "orphan.scan_dispose_blocked", rec.calls[0].action)
	require.Equal(t, "inst-1", rec.calls[0].target)
	require.Equal(t, "orphan", rec.calls[0].targetTyp)
	require.False(t, rec.calls[0].success)
	require.Equal(t, "容器仍 running，保守不删", rec.calls[0].errMsg)
}

// TestReportOrphanAudit_AuthRejected 缺身份 / secret 不匹配一律拒绝，不落库。
func TestReportOrphanAudit_AuthRejected(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)

	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{NodeUuid: node.UUID})
	require.Error(t, err, "缺 node_secret 应拒绝")

	_, err = h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{NodeUuid: node.UUID, NodeSecret: "wrong"})
	require.Error(t, err, "secret 不匹配应拒绝")

	require.Empty(t, rec.calls)
}

// TestReportOrphanAudit_AllowedActions FR-456 N2 / FR-459：白名单内动作全部放行（与 spec 审计动作表一致）。
func TestReportOrphanAudit_AllowedActions(t *testing.T) {
	for _, action := range []string{
		"orphan.scan_detected",
		"orphan.scan_disposed",
		"orphan.scan_dispose_blocked",
		"orphan.dispose_blocked",
		"orphan.dispose_reaped",
		// FR-471：启动期发现孤儿但按非破坏策略只观测（重启未杀人的唯一留痕），以及外来运行时接管的成功/失败。
		"orphan.startup_detected_not_reaped",
		"orphan.foreign_runtime_detected",
		"orphan.foreign_runtime_adopt_blocked",
		"orphan.foreign_runtime_adopted",
		// FR-459 健康巡检动作。
		"health.dead_detected",
		"health.selfheal_restart",
		"health.circuit_broken",
		"health.circuit_released",
	} {
		t.Run(action, func(t *testing.T) {
			h, _, node, rec := newOrphanAuditFixture(t)
			_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
				NodeUuid: node.UUID, NodeSecret: node.Secret, Action: action, TargetId: "wd",
			})
			require.NoError(t, err)
			require.Len(t, rec.calls, 1)
			require.Equal(t, action, rec.calls[0].action)
		})
	}
}

// TestReportOrphanAudit_RejectsUnknownAction FR-456 N2：白名单外动作一律拒绝，不落库。
func TestReportOrphanAudit_RejectsUnknownAction(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)

	for _, action := range []string{"", "evil.action", "orphan.scan", "orphan.scan_disposedx"} {
		_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
			NodeUuid: node.UUID, NodeSecret: node.Secret, Action: action, TargetId: "wd",
		})
		require.Errorf(t, err, "action=%q 应被拒", action)
	}
	require.Empty(t, rec.calls, "白名单外动作不得落库")
}

// TestReportOrphanAudit_RejectsOverlongAction action 超长直接拒绝（纵深防御）。
func TestReportOrphanAudit_RejectsOverlongAction(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)
	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret,
		Action: strings.Repeat("a", maxOrphanAuditActionLen+1), TargetId: "wd",
	})
	require.Error(t, err)
	require.Empty(t, rec.calls)
}

// TestReportOrphanAudit_TruncatesAndWrapsDetail FR-456 N2/N3：
// detail 超限按 rune 边界截断（不留非法 UTF-8），且落库 detail 携带 nodeUuid 便于按节点溯源。
func TestReportOrphanAudit_TruncatesAndWrapsDetail(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)

	huge := `{"cmdline":"` + strings.Repeat("中", maxOrphanAuditDetailLen) + `"}`
	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret,
		Action: "orphan.scan_detected", TargetId: "wd", Detail: huge,
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 1)

	got := rec.calls[0].detail
	require.LessOrEqual(t, len(got), maxOrphanAuditDetailLen+128, "detail 必须被截断")
	require.True(t, utf8.ValidString(got), "截断后仍须是合法 UTF-8")

	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &obj), "detail 外层须是合法 JSON")
	require.Equal(t, node.UUID, obj["nodeUuid"], "detail 须携带 nodeUuid（N3）")
	require.Contains(t, obj, "payload")
}

// TestReportOrphanAudit_TruncatesTarget targetId 超长被截断（不放任任意长度入审计库）。
func TestReportOrphanAudit_TruncatesTarget(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)
	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret,
		Action: "orphan.scan_detected", TargetId: strings.Repeat("/very/long", 200),
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 1)
	require.LessOrEqual(t, len(rec.calls[0].target), maxOrphanAuditTargetLen)
}

// TestReportOrphanAudit_TargetNodeBinding FR-456 N2：targetId 解析为**他节点**实例时拒绝；
// 解析为本节点实例时放行；解析不到（direct 孤儿的工作目录 target）也不拦。
func TestReportOrphanAudit_TargetNodeBinding(t *testing.T) {
	h, db, node, rec := newOrphanAuditFixture(t)
	other := &model.Node{UUID: "node-other", Name: "o", Secret: "s2", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(other).Error)
	seedOrphanAuditInstance(t, db, "inst-mine", node.ID)
	seedOrphanAuditInstance(t, db, "inst-theirs", other.ID)

	// 他节点实例 → 拒绝。
	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret, Action: "orphan.scan_detected", TargetId: "inst-theirs",
	})
	require.Error(t, err, "target 属他节点实例应拒绝")
	require.Empty(t, rec.calls)

	// 本节点实例 → 放行。
	_, err = h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret, Action: "orphan.scan_detected", TargetId: "inst-mine",
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 1)

	// 非实例 UUID（direct 孤儿的工作目录）→ 不做绑定，放行。
	_, err = h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret, Action: "orphan.scan_detected", TargetId: "/srv/servers/survival-abc",
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 2)
}

// FR-459 复审项 11：health.* 动作的审计 targetType 应为 health（而非孤儿处置的 orphan），
// 使健康自愈/熔断在审计面可按类型正确归类；orphan.* 仍为 orphan。
func TestReportOrphanAudit_HealthActionTargetType(t *testing.T) {
	h, _, node, rec := newOrphanAuditFixture(t)

	_, err := h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid:   node.UUID,
		NodeSecret: node.Secret,
		Action:     "health.circuit_broken",
		TargetId:   "inst-health",
		Detail:     `{"operator":"auto","kind":"health"}`,
		Success:    true,
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 1)
	require.Equal(t, "health", rec.calls[0].targetTyp, "health.* 动作应归 health targetType")

	// 新增的「自愈重启达上限」动作同样在白名单内且归 health。
	_, err = h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid:   node.UUID,
		NodeSecret: node.Secret,
		Action:     "health.selfheal_exhausted",
		TargetId:   "inst-health",
		Detail:     `{"operator":"auto"}`,
		Success:    true,
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 2)
	require.Equal(t, "health", rec.calls[1].targetTyp)
	require.Equal(t, "health.selfheal_exhausted", rec.calls[1].action)

	// orphan.* 不受影响。
	_, err = h.ReportOrphanAudit(context.Background(), &workerpb.ReportOrphanAuditRequest{
		NodeUuid:   node.UUID,
		NodeSecret: node.Secret,
		Action:     "orphan.dispose_reaped",
		TargetId:   "inst-health",
		Success:    true,
	})
	require.NoError(t, err)
	require.Len(t, rec.calls, 3)
	require.Equal(t, "orphan", rec.calls[2].targetTyp)
}
