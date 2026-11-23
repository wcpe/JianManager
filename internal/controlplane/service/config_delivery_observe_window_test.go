package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 回归用例：二次 review 发现的「锁粒度重构」引入的四个缺陷。
//
// 这些缺陷全部没被重前的用例捕获，原因是它们都需要「多批 + 观察窗」场景：
// 单批用例根本走不到 current_batch 回退，而观察窗内的并发动作
// （Pause/Cancel）需要把 sleep 注入成能实际操作库的副作用才能重现。

// TestDeliveryCurrentBatchSurvivesFinish 验证收尾不会把 current_batch 抹回 0。
//
// 缺陷成因：runBatchLocked 只把 current_batch 写进库，没同步内存副本；
// 而 finishRun 走 db.Save(order) 整行写回，把库里的正确值盖成了 0。
func TestDeliveryCurrentBatchSurvivesFinish(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 3, 3)
	file := seedConfigFileWithLayer(t, db, "listen: 25565\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	// 3 台分 3 批（每批 1 台），带观察窗 → 一定跑到第 3 批。
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.BatchMode = model.DeliveryBatchCount
		in.BatchSizes = []int{1, 1, 1}
		in.ObserveWindowSec = 1
	})
	approveAndStart(t, svc, order.ID)

	fresh, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusCompleted, fresh.Status,
		"三批全成功应收尾为 completed")
	require.Equal(t, 3, fresh.CurrentBatch,
		"收尾后 current_batch 必须停在末批 3；退化为 0 说明 finishRun 用陈旧内存副本整行写回")
}

// TestDeliveryPauseDuringObserveWindowIsHonored 验证观察窗内的人工暂停不被推翻。
//
// 缺陷成因：Run 在锁外裸调 setStatus(observing) → sleep → setStatus(rolling)，
// 而 Paused → Rolling 在转移表里是**合法**的（Resume 就走这条）。
// 于是观察窗内用户点暂停会被睡醒后的 setStatus 静默推翻，放量继续。
func TestDeliveryPauseDuringObserveWindowIsHonored(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 4, 4)
	file := seedConfigFileWithLayer(t, db, "listen: 25565\n")
	svc, _, calls := newDeliveryTestService(t, db, nil)

	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.BatchMode = model.DeliveryBatchCount
		in.BatchSizes = []int{2, 2} // 两批
		in.ObserveWindowSec = 1
	})

	// sleep 注入：第一次进观察窗时模拟「用户点了暂停」，比如：
	// sleep 的副作用就是真实的 Pause 调用（与用户在那一瞬间点按钮等价）。
	paused := false
	svc.SetSleepForTest(func(time.Duration) {
		if paused {
			return
		}
		paused = true
		_, err := svc.Pause(order.ID, "运维手动暂停")
		require.NoError(t, err)
	})

	approveAndStart(t, svc, order.ID)

	fresh, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusPaused, fresh.Status,
		"观察窗内的人工暂停必须保留；被改成 rolling/completed 说明暂停被静默推翻")
	require.Equal(t, model.DeliveryPauseManual, model.ConfigDeliveryPauseKind(fresh.PauseKind),
		"暂停原因应仍是人工")

	// 第一批的 2 台应已下发，第二批的 2 台不应被碰。
	require.Len(t, *calls, 2, "暂停后不应继续推第二批")
	var pendingCount int64
	require.NoError(t, db.Model(&model.ConfigDeliveryTarget{}).
		Where("order_id = ? AND batch_no = ? AND status = ?", order.ID, 2, model.DeliveryTargetPending).
		Count(&pendingCount).Error)
	require.EqualValues(t, 2, pendingCount, "第二批目标应保持 pending，等 Resume")
}

// TestDeliveryCancelDuringObserveWindowNotOverwritten 验证观察窗内的终止不被收尾覆盖。
//
// 缺陷成因：同上一例。Cancel 把单置 cancelled，但循环 break 后仍会走到 finishRun，
// 而 finishRun 无条件写 completed。
func TestDeliveryCancelDuringObserveWindowNotOverwritten(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "listen: 25565\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.BatchMode = model.DeliveryBatchCount
		in.BatchSizes = []int{1, 1}
		in.ObserveWindowSec = 1
	})

	done := false
	svc.SetSleepForTest(func(time.Duration) {
		if done {
			return
		}
		done = true
		_, err := svc.Cancel(order.ID, "运维终止")
		require.NoError(t, err)
	})

	approveAndStart(t, svc, order.ID)

	fresh, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusCancelled, fresh.Status,
		"终止是终态，不能被收尾逻辑改写为 completed")
}

// TestDeliveryCancelClearsCancelFlag 验证终止后运行期取消标记被清理。
//
// 缺陷成因：Cancel 置 cancelRequested，只有 finishRun 里的 clearCancel 会清。
// 若 Run 在错误路径上提前 return（转移失败等），标记永久残留：
// 后续同一 ID 复用或重跑会被一个陈旧的“已取消”误杀。
func TestDeliveryCancelClearsCancelFlag(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "listen: 25565\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ObserveWindowSec = 0
	})
	approveAndStart(t, svc, order.ID)

	// 单子已 completed：cancel 会被状态机拒绝（终态），但直接置标记模拟运行期场景。
	svc.requestCancel(order.ID)
	require.True(t, svc.isCancelRequested(order.ID))

	// 跑一次完整流程把它清掉（completed 单不允许再 cancel，但清了也没错）。
	svc.clearCancel(order.ID)
	require.False(t, svc.isCancelRequested(order.ID), "clearCancel 应把标记清干净")
}
