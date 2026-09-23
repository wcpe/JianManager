package grpc

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newDriftFixture 准备隔离内存库 + 处理器（FR-471 心跳漂移消费）。
func newDriftFixture(t *testing.T) (*ControlPlaneHandler, *gorm.DB, *model.Node) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{UUID: "node-drift", Name: "n", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	return NewControlPlaneHandler(db, NewClientPool()), db, node
}

// TestApplyRuntimeDrift_WritesObservedDrift 心跳上报外来进程：写漂移三字段。
func TestApplyRuntimeDrift_WritesObservedDrift(t *testing.T) {
	h, db, node := newDriftFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-drift", NodeID: node.ID, Name: "drift", Status: model.InstanceStatusStopped,
	}).Error)

	h.applyRuntimeDrift([]*workerpb.InstanceState{{
		InstanceUuid:   "i-drift",
		State:          string(model.InstanceStatusStopped),
		ForeignPid:     4242,
		ForeignCmdline: "tmux: ./java -jar server.jar",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-drift").First(&got).Error)
	require.EqualValues(t, 4242, got.RuntimeDriftPID)
	require.Equal(t, "tmux: ./java -jar server.jar", got.RuntimeDriftCmdline)
	require.NotNil(t, got.RuntimeDriftAt, "观测到漂移必须记录观测时刻")
}

// TestApplyRuntimeDrift_ClearsWhenGone 漂移消失（本拍无外来进程）：原值非 0 时清零。
func TestApplyRuntimeDrift_ClearsWhenGone(t *testing.T) {
	h, db, node := newDriftFixture(t)
	at := time.Now().Add(-time.Minute)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-clear", NodeID: node.ID, Name: "clear", Status: model.InstanceStatusStopped,
		RuntimeDriftPID: 999, RuntimeDriftCmdline: "old", RuntimeDriftAt: &at,
	}).Error)

	h.applyRuntimeDrift([]*workerpb.InstanceState{{
		InstanceUuid: "i-clear",
		State:        string(model.InstanceStatusStopped),
		ForeignPid:   0,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-clear").First(&got).Error)
	require.Zero(t, got.RuntimeDriftPID, "漂移消失必须清零 PID")
	require.Empty(t, got.RuntimeDriftCmdline)
	require.Nil(t, got.RuntimeDriftAt)
}

// TestApplyRuntimeDrift_RunningStateClears 上报状态属运行类：进程归受管所有，不得记为漂移，且清旧值。
func TestApplyRuntimeDrift_RunningStateClears(t *testing.T) {
	h, db, node := newDriftFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-run", NodeID: node.ID, Name: "run", Status: model.InstanceStatusStopped,
		RuntimeDriftPID: 111,
	}).Error)

	// 即便带了 ForeignPid，只要状态是 RUNNING 就不算漂移。
	h.applyRuntimeDrift([]*workerpb.InstanceState{{
		InstanceUuid: "i-run",
		State:        string(model.InstanceStatusRunning),
		ForeignPid:   222,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-run").First(&got).Error)
	require.Zero(t, got.RuntimeDriftPID, "运行类状态不得登记漂移，并应清掉历史漂移值")
}

// TestApplyRuntimeDrift_IgnoresUnknownInstance 只更新已存在实例：不存在的 UUID 不报错、不新建行。
func TestApplyRuntimeDrift_IgnoresUnknownInstance(t *testing.T) {
	h, db, node := newDriftFixture(t)

	// 不 panic、不报错即可（applyRuntimeDrift 无返回值；此处断言不越界到 FR-326 孤儿语义）。
	h.applyRuntimeDrift([]*workerpb.InstanceState{{
		InstanceUuid:   "ghost-uuid",
		State:          string(model.InstanceStatusStopped),
		ForeignPid:     777,
		ForeignCmdline: "orphan",
	}})

	var count int64
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", "ghost-uuid").Count(&count).Error)
	require.Zero(t, count, "不得为未知 UUID 创建实例（孤儿语义归 FR-326）")
	_ = node
}

// TestApplyRuntimeDrift_TruncatesCmdline 命令行超长必须截断到列宽（512 字节）以内。
func TestApplyRuntimeDrift_TruncatesCmdline(t *testing.T) {
	h, db, node := newDriftFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-long", NodeID: node.ID, Name: "long", Status: model.InstanceStatusStopped,
	}).Error)

	long := strings.Repeat("A", 2000)
	h.applyRuntimeDrift([]*workerpb.InstanceState{{
		InstanceUuid:   "i-long",
		State:          string(model.InstanceStatusStopped),
		ForeignPid:     5,
		ForeignCmdline: long,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-long").First(&got).Error)
	require.LessOrEqual(t, len(got.RuntimeDriftCmdline), 512, "落库命令行不得超过 varchar(512)")
	require.True(t, strings.HasSuffix(got.RuntimeDriftCmdline, "…"), "截断应留省略号标记")
}

// TestTruncateCmdline_UTF8Boundary 截断必须按 UTF-8 边界，不得切碎多字节字符。
func TestTruncateCmdline_UTF8Boundary(t *testing.T) {
	// 每个汉字 3 字节；构造远超上限的中文命令行。
	long := strings.Repeat("服", 300)
	got := truncateCmdline(long, 512)
	require.LessOrEqual(t, len(got), 512)
	require.True(t, strings.HasSuffix(got, "…"))
	// 去掉结尾省略号后必须是合法 UTF-8（无残缺字节）。
	require.True(t, isCleanUTF8(strings.TrimSuffix(got, "…")), "截断不得产生非法 UTF-8 序列")

	// 未超长时原样返回（不误伤正常路径）。
	short := "./java -jar server.jar"
	require.Equal(t, short, truncateCmdline(short, 512))
}

// isCleanUTF8 判断 s 是否为合法 UTF-8（不含 U+FFFD 替换字符导致的残缺）。
func isCleanUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// driftTestStream 最小心跳双向流假实现，仅用于驱动一次 Heartbeat 验证漂移消费已接入心跳链路。
type driftTestStream struct {
	grpc.ServerStream
	ctx  context.Context
	reqs []*workerpb.HeartbeatRequest
}

func (s *driftTestStream) Context() context.Context { return s.ctx }

func (s *driftTestStream) Recv() (*workerpb.HeartbeatRequest, error) {
	if len(s.reqs) == 0 {
		return nil, io.EOF
	}
	req := s.reqs[0]
	s.reqs = s.reqs[1:]
	return req, nil
}

func (s *driftTestStream) Send(*workerpb.HeartbeatResponse) error { return nil }

// TestHeartbeat_ConsumesRuntimeDrift 端到端：走真实 Heartbeat 流，验证漂移消费挂在心跳链路里。
func TestHeartbeat_ConsumesRuntimeDrift(t *testing.T) {
	h, db, node := newDriftFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-e2e", NodeID: node.ID, Name: "e2e", Status: model.InstanceStatusStopped,
	}).Error)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{nodeSecretHeader: node.Secret}))
	stream := &driftTestStream{ctx: ctx, reqs: []*workerpb.HeartbeatRequest{{
		NodeUuid: node.UUID,
		Instances: []*workerpb.InstanceState{{
			InstanceUuid:   "i-e2e",
			State:          string(model.InstanceStatusStopped),
			ForeignPid:     31337,
			ForeignCmdline: "tmux: ./start.sh",
		}},
	}}}

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF), "单拍流处理完应以 EOF 收尾")

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-e2e").First(&got).Error)
	require.EqualValues(t, 31337, got.RuntimeDriftPID, "心跳链路必须消费外来运行时漂移")
	require.Equal(t, "tmux: ./start.sh", got.RuntimeDriftCmdline)
}
