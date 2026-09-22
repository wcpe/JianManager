package grpc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newCrashSnapshotHandler 建 handler + 一个已注册节点及其上的一个实例（FR-313 用例基座）。
func newCrashSnapshotHandler(t *testing.T) (*cpgrpc.ControlPlaneHandler, *gorm.DB, *model.Node, *model.Instance) {
	t.Helper()
	h, db, _ := newIdentityRegisterHandler(t)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.InstanceCrashSnapshot{}, &model.InstanceCrashStat{}))

	node := &model.Node{Name: "n1", Host: "10.0.0.1", Secret: "sec-1"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "smp", Type: model.InstanceTypeGeneric,
		Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDirect,
		StartCommand: "java -jar s.jar",
	}
	require.NoError(t, db.Create(inst).Error)
	return h, db, node, inst
}

// crashReq 构造一条合法上报请求。
func crashReq(node *model.Node, inst *model.Instance, occurredAt time.Time, exitCode int32) *workerpb.ReportCrashSnapshotRequest {
	return &workerpb.ReportCrashSnapshotRequest{
		NodeUuid:         node.UUID,
		NodeSecret:       node.Secret,
		InstanceUuid:     inst.UUID,
		OccurredAtUnixMs: occurredAt.UnixMilli(),
		ExitCode:         exitCode,
		Signal:           "killed",
		DurationMs:       2500,
		TailOutput:       "Error: boom\n",
	}
}

// TestReportCrashSnapshot_Persists 上报落库：字段一一对应。
func TestReportCrashSnapshot_Persists(t *testing.T) {
	h, db, node, inst := newCrashSnapshotHandler(t)
	occurred := time.Now().Truncate(time.Millisecond)

	_, err := h.ReportCrashSnapshot(context.Background(), crashReq(node, inst, occurred, 1))
	require.NoError(t, err)

	var snaps []model.InstanceCrashSnapshot
	require.NoError(t, db.Where("instance_id = ?", inst.ID).Find(&snaps).Error)
	require.Len(t, snaps, 1)
	assert.Equal(t, 1, snaps[0].ExitCode)
	assert.Equal(t, "killed", snaps[0].Signal)
	assert.Equal(t, int64(2500), snaps[0].DurationMs)
	assert.Equal(t, "Error: boom\n", snaps[0].TailOutput)
	assert.Equal(t, occurred.UnixMilli(), snaps[0].OccurredAt.UnixMilli())
}

// TestReportCrashSnapshot_PrunesToFive 连崩 6 次只留最近 5 条（K=5 滚动修剪，spec §5）。
func TestReportCrashSnapshot_PrunesToFive(t *testing.T) {
	h, db, node, inst := newCrashSnapshotHandler(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	for i := 0; i < 6; i++ {
		_, err := h.ReportCrashSnapshot(context.Background(), crashReq(node, inst, base.Add(time.Duration(i)*time.Minute), int32(i)))
		require.NoError(t, err)
	}

	var snaps []model.InstanceCrashSnapshot
	require.NoError(t, db.Where("instance_id = ?", inst.ID).Order("occurred_at ASC").Find(&snaps).Error)
	require.Len(t, snaps, 5, "第 6 次上报后应只保留最近 5 条")
	// 最旧一条（exitCode=0，即第 1 次）被滚动删除，留下第 2~6 次。
	assert.Equal(t, 1, snaps[0].ExitCode, "最旧的第 1 条应被修剪")
	assert.Equal(t, 5, snaps[4].ExitCode)
}

// TestReportCrashSnapshot_AuthRejects 节点身份校验：缺身份 Unauthenticated、
// 错 secret PermissionDenied、他人实例 PermissionDenied、实例不存在 NotFound。
func TestReportCrashSnapshot_AuthRejects(t *testing.T) {
	h, db, node, inst := newCrashSnapshotHandler(t)

	// 缺身份。
	_, err := h.ReportCrashSnapshot(context.Background(), &workerpb.ReportCrashSnapshotRequest{InstanceUuid: inst.UUID})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// secret 不匹配。
	req := crashReq(node, inst, time.Now(), 1)
	req.NodeSecret = "wrong"
	_, err = h.ReportCrashSnapshot(context.Background(), req)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// 实例不存在。
	req = crashReq(node, inst, time.Now(), 1)
	req.InstanceUuid = "no-such-instance"
	_, err = h.ReportCrashSnapshot(context.Background(), req)
	assert.Equal(t, codes.NotFound, status.Code(err))

	// 实例属于另一节点：节点只能上报自己的实例。
	other := &model.Node{Name: "n2", Host: "10.0.0.2", Secret: "sec-2"}
	require.NoError(t, db.Create(other).Error)
	req = crashReq(other, inst, time.Now(), 1)
	_, err = h.ReportCrashSnapshot(context.Background(), req)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// 全部被拒后不应有任何落库。
	var count int64
	require.NoError(t, db.Model(&model.InstanceCrashSnapshot{}).Count(&count).Error)
	assert.Zero(t, count)
}

// TestReportCrashSnapshot_ClassifiesAndStats 入库时同步分类并 upsert 统计（FR-470 §2.2）。
func TestReportCrashSnapshot_ClassifiesAndStats(t *testing.T) {
	h, db, node, inst := newCrashSnapshotHandler(t)
	req := crashReq(node, inst, time.Now(), 1)
	req.Signal = ""
	req.TailOutput = "java.lang.OutOfMemoryError: Java heap space\n\tat a.B.c(B.java:1)"

	_, err := h.ReportCrashSnapshot(context.Background(), req)
	require.NoError(t, err)

	var snap model.InstanceCrashSnapshot
	require.NoError(t, db.Where("instance_id = ?", inst.ID).First(&snap).Error)
	assert.Equal(t, "oom", snap.RootCause)
	assert.NotEmpty(t, snap.Signature)
	assert.NotEmpty(t, snap.Evidence)
	assert.GreaterOrEqual(t, snap.Confidence, 0.7)

	var stat model.InstanceCrashStat
	require.NoError(t, db.Where("instance_id = ?", inst.ID).First(&stat).Error)
	assert.Equal(t, 1, stat.Count)
	assert.Equal(t, "oom", stat.RootCause)
	assert.Equal(t, snap.Signature, stat.Signature)
}

// TestReportCrashSnapshot_TrendSurvivesPrune 趋势不受 K=5 裁剪影响（FR-470 spec §4 验收 ④）。
func TestReportCrashSnapshot_TrendSurvivesPrune(t *testing.T) {
	h, db, node, inst := newCrashSnapshotHandler(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	for i := 0; i < 7; i++ {
		req := crashReq(node, inst, base.Add(time.Duration(i)*time.Minute), 1)
		req.Signal = ""
		req.TailOutput = "java.net.BindException: Address already in use"
		_, err := h.ReportCrashSnapshot(context.Background(), req)
		require.NoError(t, err)
	}

	var snapCount int64
	require.NoError(t, db.Model(&model.InstanceCrashSnapshot{}).Where("instance_id = ?", inst.ID).Count(&snapCount).Error)
	assert.EqualValues(t, 5, snapCount, "快照仍按 K=5 裁剪")

	var stat model.InstanceCrashStat
	require.NoError(t, db.Where("instance_id = ? AND root_cause = ?", inst.ID, "port_in_use").First(&stat).Error)
	assert.Equal(t, 7, stat.Count, "统计应完整记录 7 次，不受快照裁剪影响")
}

// TestReportCrashSnapshot_ClassifyFailureStillPersists m-8：分类/统计失败不得阻断崩溃现场落库。
//
// 缺陷现场：分类与统计在同一事务内且任一失败即 return → 事务回滚，现场（tail_output）
// 一起丢失并返回 Internal。崩溃现场稀缺且不可重放，为了趋势统计的一张表丢掉原始证据代价过大。
//
// 修复后的正确行为（比「整体降级为 unknown」更保守）：
//   - 上报成功（现场完整落库）；
//   - **分类结果照常写入**（分类是纯函数，不会失败）；
//   - 仅趋势统计未计入，可由 Reclassify 幂等补跑。
//
// 注入方式：删除统计表，使统计 upsert 失败。
func TestReportCrashSnapshot_ClassifyFailureStillPersists(t *testing.T) {
	h, db, node, inst := newCrashSnapshotHandler(t)
	require.NoError(t, db.Migrator().DropTable(&model.InstanceCrashStat{}))

	req := crashReq(node, inst, time.Now().Truncate(time.Millisecond), 1)
	req.TailOutput = "java.lang.OutOfMemoryError: Java heap space"
	_, err := h.ReportCrashSnapshot(context.Background(), req)
	require.NoError(t, err, "统计失败不得让上报整体失败（现场必须留下）")

	var snap model.InstanceCrashSnapshot
	require.NoError(t, db.Where("instance_id = ?", inst.ID).First(&snap).Error)
	assert.Equal(t, "java.lang.OutOfMemoryError: Java heap space", snap.TailOutput, "原始现场必须完整保留")
	assert.Equal(t, "oom", snap.RootCause, "分类是纯函数，其结果必须被保留（不该一并降级）")
	assert.NotEmpty(t, snap.Evidence, "证据行必须落库")
	assert.Greater(t, snap.Confidence, 0.0, "置信度必须落库")
}
