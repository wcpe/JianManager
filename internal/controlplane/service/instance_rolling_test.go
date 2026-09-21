package service

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newRollingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.Node{}, &model.InstanceRollingOp{}))
	return db
}

func seedPlainInstances(t *testing.T, db *gorm.DB, n int) []uint {
	t.Helper()
	node := &model.Node{Name: "rn", Host: "127.0.0.1", GRPCPort: 9200, WSPort: 9201, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		in := &model.Instance{
			NodeID: node.ID, Name: fmt.Sprintf("ri-%d", i),
			Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect,
			StartCommand: "cmd", WorkDir: fmt.Sprintf("/tmp/ri-%d", i),
		}
		require.NoError(t, db.Create(in).Error)
		ids = append(ids, in.ID)
	}
	return ids
}

func newRollingSvcForTest(t *testing.T, db *gorm.DB, exec func(req InstanceBatchRequest, inst *model.Instance) error) *InstanceRollingService {
	t.Helper()
	batch := NewInstanceBatchService(db, nil)
	svc := NewInstanceRollingService(db, batch)
	svc.SetExecutorForTest(exec)
	return svc
}

func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%s）", timeout)
}

func waitRollingState(t *testing.T, svc *InstanceRollingService, opID uint, want model.RollingState) *model.InstanceRollingOp {
	t.Helper()
	var last *model.InstanceRollingOp
	eventually(t, 3*time.Second, func() bool {
		op, err := svc.Get(opID)
		if err != nil {
			return false
		}
		last = op
		return op.State == want
	})
	return last
}

func TestFormBatches(t *testing.T) {
	require.Nil(t, formBatches(nil, 5))
	require.Equal(t, [][]uint{{1, 2, 3, 4, 5}}, formBatches([]uint{1, 2, 3, 4, 5}, 0), "BatchSize=0 → 单批全量")
	require.Equal(t, [][]uint{{1, 2}, {3, 4}, {5}}, formBatches([]uint{1, 2, 3, 4, 5}, 2))
}

func TestSampleTargets(t *testing.T) {
	ids := []uint{5, 1, 3, 2, 4}
	require.Equal(t, ids, sampleTargets(ids, 0), "ratio<=0 → 全量")
	require.Equal(t, ids, sampleTargets(ids, 1), "ratio>=1 → 全量")
	require.Equal(t, []uint{1, 2}, sampleTargets(ids, 0.5), "按稳定序（升序 id）抽样")
	require.Equal(t, []uint{1}, sampleTargets(ids, 0.01), "至少抽 1 台")
}

func TestRollingOp_BatchSizeZeroBackwardCompatible(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 4)

	var mu sync.Mutex
	called := map[uint]int{}
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, inst *model.Instance) error {
		mu.Lock()
		called[inst.ID]++
		mu.Unlock()
		return nil
	})

	op, err := svc.Create(RollingRequest{Action: InstanceBatchRestart, IDs: ids}, nil, false, 0)
	require.NoError(t, err)
	require.Equal(t, len(ids), op.Requested)

	done := waitRollingState(t, svc, op.ID, model.RollingStateDone)
	require.Equal(t, len(ids), done.Succeeded)
	require.Equal(t, 0, done.Failed)
	mu.Lock()
	require.Len(t, called, len(ids))
	mu.Unlock()
}

func TestRollingOp_FailFastStopsSubsequentBatches(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 3)

	var mu sync.Mutex
	var order []uint
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, inst *model.Instance) error {
		mu.Lock()
		order = append(order, inst.ID)
		mu.Unlock()
		if inst.ID == ids[1] {
			return fmt.Errorf("boom")
		}
		return nil
	})

	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{BatchSize: 1, FailFast: true},
	}, nil, false, 0)
	require.NoError(t, err)

	done := waitRollingState(t, svc, op.ID, model.RollingStateDone)
	require.Equal(t, 1, done.Succeeded)
	require.Equal(t, 1, done.Failed)
	require.Len(t, done.Errors, 1)
	require.Equal(t, ids[1], done.Errors[0].InstanceID)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []uint{ids[0], ids[1]}, order, "失败即停：第三批不应执行")
	require.Equal(t, 2, done.Cursor)
}

func TestRollingOp_RatioSamplesFirstBatch(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 4)
	var node model.Node
	require.NoError(t, db.First(&node).Error)

	var mu sync.Mutex
	executed := 0
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error {
		mu.Lock()
		executed++
		mu.Unlock()
		return nil
	})

	// Ratio 灰度仅在 filter 模式有意义（spec §2.1）；ids 模式为显式指定，不抽样。
	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart,
		Filter: &InstanceBatchFilter{NodeID: &node.ID},
		Policy: model.RollingPolicy{Ratio: 0.5},
	}, nil, false, 0)
	require.NoError(t, err)
	require.Equal(t, 2, op.Requested, "灰度按比例抽样目标集")
	require.Equal(t, []uint{ids[0], ids[1]}, op.Targets)

	waitRollingState(t, svc, op.ID, model.RollingStateDone)
	mu.Lock()
	require.Equal(t, 2, executed)
	mu.Unlock()
}

// TestRollingOp_IdsModeIgnoresRatio 覆盖 spec §2.1：ids 模式不抽样（Ratio 仅 filter 模式有意义）。
func TestRollingOp_IdsModeIgnoresRatio(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 4)
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error { return nil })

	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{Ratio: 0.5},
	}, nil, false, 0)
	require.NoError(t, err)
	require.Equal(t, len(ids), op.Requested, "ids 模式显式指定目标，Ratio 不生效")
	waitRollingState(t, svc, op.ID, model.RollingStateDone)
}

// TestRollingOp_ConcurrentCreateConflictRejected 覆盖 spec §2.1：并发创建同一目标集合的编排应被拒绝（互斥）。
func TestRollingOp_ConcurrentCreateConflictRejected(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 3)

	release := make(chan struct{})
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error {
		<-release // 首批阻塞，保持编活跃
		return nil
	})

	op1, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{BatchSize: 1},
	}, nil, false, 0)
	require.NoError(t, err)
	// 目标集合重叠 → 拒绝。
	_, err = svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: []uint{ids[0]},
	}, nil, false, 0)
	require.Error(t, err, "活跃编排目标重叠应被拒绝")

	close(release)
	waitRollingState(t, svc, op1.ID, model.RollingStateDone)

	// 终态后可再次创建。
	op3, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: []uint{ids[0]},
	}, nil, false, 0)
	require.NoError(t, err)
	require.NotZero(t, op3.ID)
}

// TestRollingOp_RecoverInterruptedMarksPaused 覆盖 CP 重启恢复：未终态编排置 paused，可经 Resume 续跑。
func TestRollingOp_RecoverInterruptedMarksPaused(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	op := &model.InstanceRollingOp{
		Action: string(InstanceBatchRestart), State: model.RollingStateRunning,
		TargetsJSON: "[1,2]", ErrorsJSON: "[]", Requested: len(ids), BatchSize: 1,
	}
	require.NoError(t, db.Create(op).Error)

	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error { return nil })
	require.NoError(t, svc.RecoverInterrupted())

	got, err := svc.Get(op.ID)
	require.NoError(t, err)
	require.Equal(t, model.RollingStatePaused, got.State)

	// Resume 从游标续跑至终态。
	require.NoError(t, svc.Resume(op.ID))
	waitRollingState(t, svc, op.ID, model.RollingStateDone)
}

// TestRollingOp_PauseInterruptsBatchInterval 覆盖：Pause 应打断批间隔等待，而非等到间隔结束。
func TestRollingOp_PauseInterruptsBatchInterval(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error { return nil })

	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{BatchSize: 1, BatchIntervalSec: 5},
	}, nil, false, 0)
	require.NoError(t, err)

	// 首批完成进入批间隔后暂停；若暂停不能打断等待，State 会长时间停留在 running。
	eventually(t, 2*time.Second, func() bool {
		cur, err := svc.Get(op.ID)
		return err == nil && cur.Succeeded >= 1
	})
	require.NoError(t, svc.Pause(op.ID))
	paused, err := svc.Get(op.ID)
	require.NoError(t, err)
	require.Equal(t, model.RollingStatePaused, paused.State)

	// 取消收尾，避免第二次间隔等待拖慢测试。
	require.NoError(t, svc.Cancel(op.ID))
	waitRollingState(t, svc, op.ID, model.RollingStateCanceled)
}

func TestRollingOp_PauseResume(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 3)

	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 2 {
			<-release // 第二批阻塞，制造可观测的暂停窗口
		}
		return nil
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{BatchSize: 1},
	}, nil, false, 0)
	require.NoError(t, err)

	eventually(t, 2*time.Second, func() bool { return count() == 2 })
	require.NoError(t, svc.Pause(op.ID))
	paused, err := svc.Get(op.ID)
	require.NoError(t, err)
	require.Equal(t, model.RollingStatePaused, paused.State)

	close(release) // 放行第二批
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, 2, count(), "暂停期间不应推进后续批")

	require.NoError(t, svc.Resume(op.ID))
	waitRollingState(t, svc, op.ID, model.RollingStateDone)
	require.Equal(t, 3, count())
}

func TestRollingOp_Cancel(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 3)

	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 2 {
			<-release
		}
		return nil
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{BatchSize: 1},
	}, nil, false, 0)
	require.NoError(t, err)

	eventually(t, 2*time.Second, func() bool { return count() == 2 })
	require.NoError(t, svc.Cancel(op.ID))
	close(release)

	waitRollingState(t, svc, op.ID, model.RollingStateCanceled)
	require.Equal(t, 2, count(), "取消后不应执行后续批")
}

func TestRollingOp_BatchIntervalHonored(t *testing.T) {
	db := newRollingTestDB(t)
	ids := seedPlainInstances(t, db, 2)

	var mu sync.Mutex
	var stamps []time.Time
	svc := newRollingSvcForTest(t, db, func(_ InstanceBatchRequest, _ *model.Instance) error {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		return nil
	})

	op, err := svc.Create(RollingRequest{
		Action: InstanceBatchRestart, IDs: ids,
		Policy: model.RollingPolicy{BatchSize: 1, BatchIntervalSec: 1},
	}, nil, false, 0)
	require.NoError(t, err)
	waitRollingState(t, svc, op.ID, model.RollingStateDone)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, stamps, 2)
	require.GreaterOrEqual(t, stamps[1].Sub(stamps[0]), 900*time.Millisecond, "批间应等待 BatchInterval")
}
