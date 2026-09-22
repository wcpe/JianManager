package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// hashFileStub 在 binaryWorkerStub 之上补 HashFile——磁盘内容比对（FR-468 §2.5）的唯一入口。
type hashFileStub struct {
	*binaryWorkerStub
	resp *workerpb.HashFileResponse
	err  error
	// calls 记录实际发出的 HashFile 次数，供「根本没读盘」断言。
	calls int
	// lastPath 记录请求的路径，供「取的是启动命令指向的文件」断言。
	lastPath string
}

func (h *hashFileStub) HashFile(_ context.Context, in *workerpb.HashFileRequest, _ ...grpc.CallOption) (*workerpb.HashFileResponse, error) {
	h.calls++
	h.lastPath = in.Path
	if h.err != nil {
		return nil, h.err
	}
	return h.resp, nil
}

// newStoppedBoundInstance 搭一个已停止、带制品库绑定的二进制实例（磁盘比对的前提条件）。
func newStoppedBoundInstance(t *testing.T) (*BinaryVersionService, *model.Instance, *hashFileStub) {
	t.Helper()
	worker := &hashFileStub{binaryWorkerStub: &binaryWorkerStub{}}
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, worker.binaryWorkerStub)
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)

	// 用同一个 stub 重建连接池条目（harness 已按 node.UUID 注册了 binaryWorkerStub）。
	// 直接换掉池内的客户端，使 HashFile 走本 stub。
	pool := bv.provision.pool
	pool.SetWorkerClientForTest(node.UUID, worker)
	require.NoError(t, svc.db.First(inst, inst.ID).Error)
	return bv, inst, worker
}

// TestBinaryVersion_DiskUnreadableIsNotDrift N-4：**「不可读」不得被标成版本漂移**。
//
// 缺陷现场：diskBinaryDigest 对「文件过大」与「无法读取」都返回 checked=true + 非空 reason，
// 而 View 只要 reason != "" 就置 DriftDetected=true → 前端渲染成「检测到版本漂移」，
// 与 DiskSHA256Checked 自身注释「未校验 ≠ 漂移」直接矛盾。
func TestBinaryVersion_DiskUnreadableIsNotDrift(t *testing.T) {
	bv, inst, worker := newStoppedBoundInstance(t)
	worker.resp = &workerpb.HashFileResponse{Error: "no such file or directory"}

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.Equal(t, 1, worker.calls, "停止态实例应真的发起一次磁盘比对")
	require.True(t, view.DiskSHA256Checked, "读过盘（尝试过）→ checked=true")
	require.Empty(t, view.DiskSHA256, "未算出摘要")
	require.False(t, view.DriftDetected, "「不可读」不是漂移，不得置 DriftDetected")
	require.Empty(t, view.DriftReason, "原因不得写进 DriftReason（那会被前端渲染成漂移）")
	require.Contains(t, view.DiskCheckSkippedReason, "无法读取")
	require.Contains(t, view.DiskCheckSkippedReason, "本次未能比对内容")
}

// TestBinaryVersion_DiskTooLargeIsNotDrift N-4：**「文件太大、本次没比对」不得被标成漂移**。
func TestBinaryVersion_DiskTooLargeIsNotDrift(t *testing.T) {
	bv, inst, worker := newStoppedBoundInstance(t)
	worker.resp = &workerpb.HashFileResponse{TooLarge: true}

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.True(t, view.DiskSHA256Checked)
	require.Empty(t, view.DiskSHA256)
	require.False(t, view.DriftDetected, "「超过上限未比对」不是漂移")
	require.Empty(t, view.DriftReason)
	require.Contains(t, view.DiskCheckSkippedReason, "超过")
	require.Contains(t, view.DiskCheckSkippedReason, "未做磁盘内容比对")
}

// TestBinaryVersion_DiskDigestMismatchIsDrift N-4：**真漂移**（已成功算出摘要且与绑定不符）必须报出。
//
// 与上面两条构成三态对照：只有这一条才是漂移，前两条只是「本次未比对」。
func TestBinaryVersion_DiskDigestMismatchIsDrift(t *testing.T) {
	bv, inst, worker := newStoppedBoundInstance(t)
	worker.resp = &workerpb.HashFileResponse{Sha256: strings.Repeat("a", 64)}

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.True(t, view.DiskSHA256Checked)
	require.Equal(t, strings.Repeat("a", 64), view.DiskSHA256, "已算出摘要")
	require.True(t, view.DriftDetected, "摘要与绑定不符才是漂移")
	require.Contains(t, view.DriftReason, "与绑定记录不一致")
	require.Empty(t, view.DiskCheckSkippedReason, "真漂移不得同时挂「未比对」提示")
}

// TestBinaryVersion_DiskDigestMatchIsClean 三态的另一侧：摘要一致 → 既无漂移也无跳过原因。
func TestBinaryVersion_DiskDigestMatchIsClean(t *testing.T) {
	bv, inst, worker := newStoppedBoundInstance(t)
	var binding model.InstanceBinaryBinding
	require.NoError(t, bv.db.Where("instance_id = ?", inst.ID).First(&binding).Error)
	require.NotEmpty(t, binding.CurrentSHA256)
	worker.resp = &workerpb.HashFileResponse{Sha256: strings.ToUpper(binding.CurrentSHA256)}

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.True(t, view.DiskSHA256Checked)
	require.False(t, view.DriftDetected, "摘要一致（大小写不敏感）→ 无漂移")
	require.Empty(t, view.DriftReason)
	require.Empty(t, view.DiskCheckSkippedReason)
}

// TestBinaryVersion_DiskChannelErrorLeavesUnchecked 通道异常：压根没读盘 → checked=false，
// 且不得借 DriftReason 冒充漂移（「取不到闸」不等于「有异常」）。
func TestBinaryVersion_DiskChannelErrorLeavesUnchecked(t *testing.T) {
	bv, inst, worker := newStoppedBoundInstance(t)
	worker.err = context.DeadlineExceeded

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.False(t, view.DiskSHA256Checked, "通道异常 → 本次没读盘")
	require.False(t, view.DriftDetected)
	require.Empty(t, view.DriftReason)
	require.Empty(t, view.DiskCheckSkippedReason, "根本没读盘不写「已读过但跳过」")
}
