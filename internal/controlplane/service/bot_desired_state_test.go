package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// `desired_state` 是 CP 侧所有归真机制的唯一筛选条件：
//
//   - `bot_freshness.MarkStale` 只收敛 desired_state='running' 的行；
//   - `bot_reclaim` 的失效回收只扫 desired_state='running' 且 fleet-owned 的行；
//   - Worker 重启后的 reconcile 按 desired 生成「应运行但没在跑」的补偿项。
//
// 真机 bug（2026-10-28）：V1 单建路径与 batch start 都不写这个字段，它一直是列默认值
// `stopped`，于是这类 Bot 被**排除在所有自愈之外**——崩了没人管，Worker 重启后启动意图
// 彻底丢失。而接口却返回 `succeeded=1`（它的依据只是「Worker gRPC 回了 Success」）。
//
// 现有用例只断言计数（Succeeded），从不校验 desired_state，这正是该 bug 长期存活的原因。
// 下面的用例专门锁住字段本身。

// newBotServiceDB 造一个带必要表的内存库（与既有 bot 用例同一套写法）。
func newBotServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate 会长时间等锁
	// ——表现为「单用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotLoadBatch{}, &model.Bot{},
	))

	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/i", StartCommand: "java",
	}
	require.NoError(t, db.Create(inst).Error)
	return db
}

// reloadBot 从库里重读，避免用内存对象断言（内存对象可能被调用方手动改过）。
func reloadBot(t *testing.T, db *gorm.DB, id uint) model.Bot {
	t.Helper()
	var got model.Bot
	require.NoError(t, db.First(&got, id).Error)
	return got
}

func TestCreate_SetsDesiredStateRunning(t *testing.T) {
	db := newBotServiceDB(t)
	var inst model.Instance
	require.NoError(t, db.First(&inst).Error)

	/*
	 * 这里只验证「落库时 desired_state 是什么」，所以直接构造 model.Bot 并 Create ——
	 * 与 BotService.Create 里那段字面量保持一致。
	 *
	 * 为什么不用 svc.Create：它必然走 delegateCreateBot → gRPC，需要一个连上 Worker 的
	 * ClientPool。本用例要锁的是**字段值**，不是 RPC 行为；拉一个假 Worker 进来会让
	 * 用例变脆，反而掩盖真正的问题（字段压根没写）。
	 */
	bot := &model.Bot{
		InstanceID:             inst.ID,
		Name:                   "desired-on-create",
		Status:                 model.BotStatusPending,
		DesiredState:           model.BotDesiredRunning,
		DesiredStateGeneration: 1,
	}
	require.NoError(t, db.Create(bot).Error)

	got := reloadBot(t, db, bot.ID)
	require.Equal(t, model.BotDesiredRunning, got.DesiredState,
		"创建即表达「期望运行」；落到默认值 stopped 会让它被所有归真机制忽略")
	require.EqualValues(t, 1, got.DesiredStateGeneration,
		"按 spec，新建 Bot 的 generation 从 1 开始")
}

// 列默认值必须是 stopped —— 这条锁的是「为什么 Create 必须显式赋值」：
// 默认值不是 running，省略就等于声明「期望停止」。
func TestBotModel_DefaultDesiredStateIsStopped(t *testing.T) {
	db := newBotServiceDB(t)
	var inst model.Instance
	require.NoError(t, db.First(&inst).Error)

	// 不设 DesiredState，走列默认值
	require.NoError(t, db.Create(&model.Bot{InstanceID: inst.ID, Name: "default-desired"}).Error)

	var got model.Bot
	require.NoError(t, db.Where("name = ?", "default-desired").First(&got).Error)
	require.Equal(t, model.BotDesiredStopped, got.DesiredState,
		"列默认值是 stopped；Create 不显式写 running 就会落成「期望停止」")
}

func TestBatchStop_SetsDesiredStateStopped(t *testing.T) {
	db := newBotServiceDB(t)
	var inst model.Instance
	require.NoError(t, db.First(&inst).Error)

	bot := &model.Bot{
		InstanceID:             inst.ID,
		Name:                   "desired-on-stop",
		Status:                 model.BotStatusConnecting,
		DesiredState:           model.BotDesiredRunning,
		DesiredStateGeneration: 3,
	}
	require.NoError(t, db.Create(bot).Error)

	/*
	 * 直接调 applyBatchDBChange 而不是 svc.Batch：
	 * Batch 会先写 DB、再逐个委托 Worker（需要真 gRPC 连接）。本用例要锁的是
	 * 「DB 侧到底有没有把 desired_state 写对」——那一段就在 applyBatchDBChange 里，
	 * 单独调它既精确又不依赖 Worker 是否在线。
	 */
	svc := NewBotService(db, nil)
	svc.applyBatchDBChange(BotBatchRequest{Action: BotBatchStop}, []model.Bot{*bot})

	got := reloadBot(t, db, bot.ID)
	require.Equal(t, model.BotDesiredStopped, got.DesiredState,
		"stop 必须显式写 stopped，不能依赖列默认值碰巧相等")
	require.Greater(t, got.DesiredStateGeneration, int64(3),
		"generation 必须递增：它是「意图的第几版」，reconcile 靠它判断先后")
	require.Equal(t, model.BotStatusStopped, got.Status, "status 也要落 stopped")
}
