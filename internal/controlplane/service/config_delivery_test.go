package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 单测：覆盖 spec §12 与 §17 的全部验收项。
//
// 测试用纯内存 SQLite + 注入的落盘/生效实现，不触碰真实 Worker 与探针。

// newDeliveryTestDB 建库并迁移交付链路所需的全部模型。
func newDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{}, &model.InstanceConfigVersion{},
		&model.ConfigFile{}, &model.ConfigLayerVersion{},
		&model.ConfigDeliveryOrder{}, &model.ConfigDeliveryTarget{}, &model.ConfigDeliveryRollback{},
		&model.InstanceGroupNode{}, &model.InstanceGroupMember{},
		&model.AuditLog{},
	))
	return db
}

// seedDeliveryInstances 造 n 台实例，前 online 台为 RUNNING。
func seedDeliveryInstances(t *testing.T, db *gorm.DB, n, online int) []model.Instance {
	t.Helper()
	var nodeCount int64
	require.NoError(t, db.Model(&model.Node{}).Count(&nodeCount).Error)
	node := model.Node{
		Name:   fmt.Sprintf("node-%d", nodeCount+1),
		UUID:   fmt.Sprintf("node-uuid-%d", nodeCount+1),
		Status: model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(&node).Error)
	insts := make([]model.Instance, 0, n)
	for i := 0; i < n; i++ {
		status := model.InstanceStatusStopped
		if i < online {
			status = model.InstanceStatusRunning
		}
		inst := model.Instance{
			UUID:        fmt.Sprintf("inst-uuid-%d", i+1),
			NodeID:      node.ID,
			Name:        fmt.Sprintf("inst-%d", i+1),
			Type:        model.InstanceTypeMinecraftJava,
			Role:        model.InstanceRoleBackend,
			Status:      status,
			WorkDir:     fmt.Sprintf("/srv/inst-%d", i+1),
			AutoRestart: false,
		}
		require.NoError(t, db.Create(&inst).Error)
		insts = append(insts, inst)
	}
	return insts
}

// seedConfigFileWithLayer 造一个配置文件与一个分组层版本。
func seedConfigFileWithLayer(t *testing.T, db *gorm.DB, content string) *model.ConfigFile {
	t.Helper()
	file := &model.ConfigFile{Name: "server.properties", Format: model.ConfigFormatProperties}
	require.NoError(t, db.Create(file).Error)
	layer := &model.ConfigLayerVersion{
		ConfigFileID: file.ID,
		ScopeLevel:   model.ConfigScopeGroup,
		ScopeRefID:   1,
		VersionNo:    1,
		Content:      content,
		ContentHash:  hashContent(content),
	}
	require.NoError(t, db.Create(layer).Error)
	return file
}

// newDeliveryTestService 构造已注入 fake 落盘/生效的交付服务。
//
// fakeDisk 的语义：onlineInstances 里的实例落盘成功，其余落盘失败（模拟节点不可达）。
func newDeliveryTestService(t *testing.T, db *gorm.DB, failInstanceIDs map[uint]bool) (*ConfigDeliveryService, *ConfigActivationService, *[]deliveryWriteCall) {
	t.Helper()
	files := NewConfigFileService(db)
	activation := NewConfigActivationService(db, nil)
	activation.SetSleepForTest(func(time.Duration) {})
	svc := NewConfigDeliveryService(db, nil, files, activation, NewAuditService(db))
	svc.SetSleepForTest(func(time.Duration) {})

	calls := &[]deliveryWriteCall{}
	svc.SetWriteFuncForTest(func(inst *model.Instance, filePath, content, message string) deliveryWriteOutcome {
		*calls = append(*calls, deliveryWriteCall{InstanceID: inst.ID, FilePath: filePath, Content: content})
		if failInstanceIDs[inst.ID] {
			return deliveryWriteOutcome{ErrorCode: "WRITE_FAILED", ErrorMessage: "模拟落盘失败"}
		}
		// 落盘成功：同时写版本行，让回滚链路有据可依。
		prev := latestVersionPointer(db, inst.ID, filePath)
		ver := model.InstanceConfigVersion{
			InstanceID: inst.ID, FilePath: filePath, Content: content,
			ContentHash: hashContent(content), Message: message,
		}
		require.NoError(t, db.Create(&ver).Error)
		newID := ver.ID
		return deliveryWriteOutcome{
			DiskWritten:   true,
			Changed:       true,
			PrevVersionID: prev,
			NewVersionID:  &newID,
			BackupPath:    fmt.Sprintf(".jm-backup/%s.20260101T000000.000000000Z", filePath),
			BackupCreated: true,
		}
	})
	return svc, activation, calls
}

// deliveryWriteCall 记录一次落盘调用。
type deliveryWriteCall struct {
	InstanceID uint
	FilePath   string
	Content    string
}

// latestVersionPointer 取最新版本 ID 指针（无则 nil）。
func latestVersionPointer(db *gorm.DB, instanceID uint, filePath string) *uint {
	var row model.InstanceConfigVersion
	if err := db.Where("instance_id = ? AND file_path = ?", instanceID, filePath).
		Order("id desc").First(&row).Error; err != nil {
		return nil
	}
	id := row.ID
	return &id
}

// createDraftOrder 建一张 draft 交付单（选择器命中全部实例）。
func createDraftOrder(t *testing.T, svc *ConfigDeliveryService, db *gorm.DB, file *model.ConfigFile, insts []model.Instance, mutate func(*ConfigDeliveryInput)) *model.ConfigDeliveryOrder {
	t.Helper()
	ids := make([]uint, 0, len(insts))
	for _, inst := range insts {
		ids = append(ids, inst.ID)
	}
	in := ConfigDeliveryInput{
		ConfigFileID:       file.ID,
		FilePath:           file.Name,
		TargetLayerLevel:   model.ConfigScopeGroup,
		TargetLayerScopeID: 1,
		Selector:           DeliverySelector{InstanceIDs: ids},
		BatchMode:          model.DeliveryBatchPercent,
		BatchSizes:         []int{100},
		ObserveWindowSec:   1,
		ActivationMethod:   model.ActivationPushOnly,
	}
	if mutate != nil {
		mutate(&in)
	}
	order, err := svc.Create(in, 1)
	require.NoError(t, err)
	return order
}

// approveAndStart 走完 submit → approve → start，并同步执行 Run。
func approveAndStart(t *testing.T, svc *ConfigDeliveryService, orderID uint) {
	t.Helper()
	_, err := svc.Submit(orderID)
	require.NoError(t, err)
	_, err = svc.Approve(orderID, 2)
	require.NoError(t, err)
	// 直接置 rolling 并同步 Run（Start 会起 goroutine，测试里不可控）。
	order, err := svc.Get(orderID)
	require.NoError(t, err)
	require.NoError(t, svc.transitTo(order, model.DeliveryStatusRolling))
	require.NoError(t, svc.Run(orderID))
}

// ---------------------------------------------------------------------------
// 状态机
// ---------------------------------------------------------------------------

// TestDeliveryStateMachineTransitions 校验转移表本身（合法/非法各取样）。
func TestDeliveryStateMachineTransitions(t *testing.T) {
	// spec §7 主链：draft → pending_approval → approved → rolling → observing → completed
	chain := []model.ConfigDeliveryStatus{
		model.DeliveryStatusDraft, model.DeliveryStatusPendingApproval,
		model.DeliveryStatusApproved, model.DeliveryStatusRolling,
		model.DeliveryStatusObserving, model.DeliveryStatusCompleted,
	}
	for i := 0; i < len(chain)-1; i++ {
		require.True(t, chain[i].CanTransitTo(chain[i+1]),
			"%s 应可转移到 %s", chain[i], chain[i+1])
	}
	// 熔断支路：rolling/observing → paused → rolling_back → rolled_back
	require.True(t, model.DeliveryStatusRolling.CanTransitTo(model.DeliveryStatusPaused))
	require.True(t, model.DeliveryStatusObserving.CanTransitTo(model.DeliveryStatusPaused))
	require.True(t, model.DeliveryStatusPaused.CanTransitTo(model.DeliveryStatusRollingBack))
	require.True(t, model.DeliveryStatusRollingBack.CanTransitTo(model.DeliveryStatusRolledBack))
	// 回滚本身可再回滚（§10.3）
	require.True(t, model.DeliveryStatusRolledBack.CanTransitTo(model.DeliveryStatusRollingBack))
	// 非法跳步
	require.False(t, model.DeliveryStatusDraft.CanTransitTo(model.DeliveryStatusRolling),
		"draft 不能直接 rolling")
	require.False(t, model.DeliveryStatusApproved.CanTransitTo(model.DeliveryStatusCompleted))
	require.False(t, model.DeliveryStatusCancelled.CanTransitTo(model.DeliveryStatusRolling),
		"cancelled 是终态")
	// 终态判定
	require.True(t, model.DeliveryStatusCompleted.IsTerminal())
	require.True(t, model.DeliveryStatusRolledBack.IsTerminal())
	require.False(t, model.DeliveryStatusPaused.IsTerminal(), "paused 可继续，不是终态")
	// deferred 不是目标级终态（否则补推永远没有下一次）
	require.False(t, model.DeliveryTargetDeferred.IsTerminal())
	require.True(t, model.DeliveryTargetFailed.IsTerminal())
}

// TestDeliverySubmitRequiresDraft 非 draft 不能编辑/删除。
func TestDeliverySubmitRequiresDraft(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, _ := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)

	_, err := svc.Submit(order.ID)
	require.NoError(t, err)
	// 已提交的单不能再编辑
	_, err = svc.Update(order.ID, ConfigDeliveryInput{
		ConfigFileID: file.ID, TargetLayerLevel: model.ConfigScopeGroup, TargetLayerScopeID: 1,
		Selector:  DeliverySelector{InstanceIDs: []uint{insts[0].ID}},
		BatchMode: model.DeliveryBatchCount, ActivationMethod: model.ActivationPushOnly,
	}, 1)
	require.ErrorIs(t, err, ErrDeliveryStateInvalid)
	require.ErrorIs(t, svc.Delete(order.ID), ErrDeliveryStateInvalid)
}

// ---------------------------------------------------------------------------
// spec §8 / §12：离线语义与熔断
// ---------------------------------------------------------------------------

// TestDeliveryOfflineRateBreaks 验收 1：12 实例 2 台离线（16%）→ 熔断。
func TestDeliveryOfflineRateBreaks(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 12, 10) // 2 台离线 = 16.7%
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, calls := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)

	// 批开始前先算一次离线率，确认口径
	order, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.InDelta(t, 16.67, offlineRatePercent(2, 12), 0.01)
	require.True(t, shouldBreakOnOffline(2, 12), "16%% 离线率应触发熔断")

	approveAndStart(t, svc, order.ID)
	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusPaused, got.Status, "离线率 16%% 应熔断并置 paused")
	require.Equal(t, string(model.DeliveryPauseCircuitBreak), got.PauseKind)
	require.Equal(t, BreakReasonOfflineRate, got.BreakReason)
	require.InDelta(t, 16.67, got.BreakObserved, 0.01)
	// **一台都不该下发**：批开始即熔断，比逐台 deferred 更保守。
	require.Empty(t, *calls, "熔断后不应有任何落盘")

	// 后续批保持 pending（不自动继续），当前批未下发的置 skipped
	var targets []model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).Find(&targets).Error)
	skipped, pending := 0, 0
	for _, tg := range targets {
		switch tg.Status {
		case model.DeliveryTargetSkipped:
			skipped++
			require.Equal(t, TargetCodeCircuitBreak, tg.ReasonCode)
		case model.DeliveryTargetPending:
			pending++
		}
	}
	require.Equal(t, 12, skipped)
	require.Equal(t, 0, pending)
}

// TestDeliveryDeferredBelowThreshold 验收 2/3/4：100 实例 5 台离线（5%）
// → deferred，95 台继续，失败率分母排除 deferred，上线后可补推。
func TestDeliveryDeferredBelowThreshold(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 100, 95) // 5 台离线 = 5%
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, calls := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.FailureRateThreshold = 10 // 失败率 10% 熔断
	})

	require.False(t, shouldBreakOnOffline(5, 100), "5%% 离线率不应熔断")
	approveAndStart(t, svc, order.ID)

	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusCompleted, got.Status, "离线率 5%% 应继续推完")
	require.Equal(t, 95, got.ActivatedCount)
	require.Equal(t, 5, got.DeferredCount)
	require.Equal(t, 0, got.FailedCount, "deferred 不计入失败")
	require.Len(t, *calls, 95, "只推在线目标")

	// 验收 4：失败率分母排除 deferred
	require.InDelta(t, 0.0, failureRatePercent(got.FailedCount, got.PlannedCount, got.DeferredCount), 0.001)
	// 若分母不排除 deferred，0/100 也仍是 0——换个角度验证口径：
	// 5 台 failed + 5 台 deferred 时，分母应是 95 而不是 100。
	require.InDelta(t, 5.26, failureRatePercent(5, 100, 5), 0.01)

	// 验收 3：目标上线后自动补推（最多 3 次）
	offlineInst := insts[99]
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", offlineInst.ID).
		Update("status", model.InstanceStatusRunning).Error)
	callsBefore := len(*calls)
	svc.HandleProbeConnected(offlineInst.UUID)
	require.Equal(t, callsBefore+1, len(*calls), "补推应重新下发一次")

	var target model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ? AND instance_id = ?", order.ID, offlineInst.ID).First(&target).Error)
	require.Equal(t, model.DeliveryTargetActivated, target.Status)
	require.Equal(t, 1, target.RetryCount)

	// 补推上限：把目标退回 deferred 并耗尽次数
	require.NoError(t, db.Model(&model.ConfigDeliveryTarget{}).Where("id = ?", target.ID).
		Updates(map[string]any{"status": model.DeliveryTargetDeferred, "retry_count": 3}).Error)
	svc.HandleProbeConnected(offlineInst.UUID)
	require.NoError(t, db.First(&target, target.ID).Error)
	require.Equal(t, model.DeliveryTargetFailed, target.Status, "超过 3 次补推应判 failed")
	require.Equal(t, TargetCodeRetryExhausted, target.ReasonCode)
}

// TestDeliveryAutoRetrySwitchOff 关闭 autoRetryDeferred 后不补推。
func TestDeliveryAutoRetrySwitchOff(t *testing.T) {
	db := newDeliveryTestDB(t)
	// 20 台 1 台离线（5%）：低于 10% 不熔断，专测补推开关。
	insts := seedDeliveryInstances(t, db, 20, 19)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, calls := newDeliveryTestService(t, db, nil)
	off := false
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.AutoRetryDeferred = &off
	})
	approveAndStart(t, svc, order.ID)

	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.DeferredCount)

	offlineInst := insts[19]
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", offlineInst.ID).
		Update("status", model.InstanceStatusRunning).Error)
	before := len(*calls)
	svc.HandleProbeConnected(offlineInst.UUID)
	require.Equal(t, before, len(*calls), "开关关闭时不应补推")
}

// TestDeliveryCircuitBreakAbortsPending 验收 5：提前熔断可中止未下发目标。
func TestDeliveryCircuitBreakAbortsPending(t *testing.T) {
	db := newDeliveryTestDB(t)
	// 10 台全在线 → 不触发离线熔断；失败率阈值设 20%。
	insts := seedDeliveryInstances(t, db, 10, 10)
	file := seedConfigFileWithLayer(t, db, "a=1")
	// 前 3 台固定落盘失败：第 2 台失败后失败率 = 2/10 = 20% → 熔断。
	fail := map[uint]bool{insts[0].ID: true, insts[1].ID: true, insts[2].ID: true}
	svc, _, calls := newDeliveryTestService(t, db, fail)
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.FailureRateThreshold = 20
	})
	approveAndStart(t, svc, order.ID)

	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusPaused, got.Status)
	require.Equal(t, BreakReasonFailureRate, got.BreakReason)
	require.Equal(t, 1, got.FailedBatchNo)
	// 熔断发生在第 2 台失败后：落盘调用应恰好 2 次，其余 8 台被中止。
	require.Len(t, *calls, 2, "提前熔断应中止未下发目标")

	var skipped int64
	require.NoError(t, db.Model(&model.ConfigDeliveryTarget{}).
		Where("order_id = ? AND status = ?", order.ID, model.DeliveryTargetSkipped).
		Count(&skipped).Error)
	require.Equal(t, int64(8), skipped)

	// 熔断审计 actor=system（user_id=0）+ break_reason
	var logs []model.AuditLog
	require.NoError(t, db.Where("action = ?", "config_delivery.circuit_break").Find(&logs).Error)
	require.Len(t, logs, 1)
	require.Equal(t, uint(0), logs[0].UserID, "熔断审计的 actor 应为 system（user_id=0）")
	require.Contains(t, logs[0].Detail, `"actor":"system"`)
	require.Contains(t, logs[0].Detail, `"breakReason":"FAILURE_RATE"`)
}

// TestDeliveryCircuitBreakKeepsLaterBatchesPending 熔断后后续批保持 pending。
func TestDeliveryCircuitBreakKeepsLaterBatchesPending(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 10, 10)
	file := seedConfigFileWithLayer(t, db, "a=1")
	fail := map[uint]bool{}
	for _, inst := range insts[:5] {
		fail[inst.ID] = true
	}
	svc, _, calls := newDeliveryTestService(t, db, fail)
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.BatchSizes = []int{50, 50}
		in.FailureRateThreshold = 10
	})
	approveAndStart(t, svc, order.ID)

	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusPaused, got.Status)
	require.Equal(t, 1, got.FailedBatchNo, "熔断应发生在第 1 批")
	// 第 1 批 5 台：第 1 台失败（1/10=10%）即触阈，故只落盘 1 次。
	require.Len(t, *calls, 1)

	// 第 2 批保持 pending，一个都不能被碰。
	var batch2 []model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ? AND batch_no = ?", order.ID, 2).Find(&batch2).Error)
	require.Len(t, batch2, 5)
	for _, tg := range batch2 {
		require.Equal(t, model.DeliveryTargetPending, tg.Status, "后续批应保持 pending")
	}
}

// TestDeliveryUnhealthyRateBreaks 观察窗后不健康率触阈也熔断。
func TestDeliveryUnhealthyRateBreaks(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 4, 4)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, _ := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.BatchSizes = []int{50, 50}
		in.UnhealthyRateThreshold = 50
		in.ObserveWindowSec = 1
	})
	// 让第 1 批的 2 台在观察窗里「挂掉」：观察窗是无副作用的 sleep，因此在 Run 前
	// 先把这 2 台的实例状态改成 STOPPED 是不行的（它们还没被生效）。
	// 正确做法：注入一个在 sleep 期间改库的 sleepFn。
	svc.SetSleepForTest(func(time.Duration) {
		require.NoError(t, db.Model(&model.Instance{}).
			Where("id IN ?", []uint{insts[0].ID, insts[1].ID}).
			Update("status", model.InstanceStatusStopped).Error)
	})
	approveAndStart(t, svc, order.ID)

	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusPaused, got.Status, "不健康率 100%% 应熔断")
	require.Equal(t, BreakReasonUnhealthyRate, got.BreakReason)
	require.Equal(t, 1, got.FailedBatchNo)
}

// ---------------------------------------------------------------------------
// spec §10 / §12：回滚
// ---------------------------------------------------------------------------

// TestDeliveryRollbackFullAndSubset 验收 6：整批回滚与子集回滚各自正确。
func TestDeliveryRollbackFullAndSubset(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 4, 4)
	file := seedConfigFileWithLayer(t, db, "a=1")
	// 先给每台落一版「变更前」的配置：回滚要还原的是它，而不是凭空造内容。
	for _, inst := range insts {
		require.NoError(t, db.Create(&model.InstanceConfigVersion{
			InstanceID: inst.ID, FilePath: file.Name, Content: "a=0",
			ContentHash: hashContent("a=0"),
		}).Error)
	}
	svc, _, calls := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)
	approveAndStart(t, svc, order.ID)

	got, err := svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusCompleted, got.Status)

	// --- 子集回滚：只还原 2 台，配置层版本不回退 ---
	layerVersionsBefore := countLayerVersions(t, db, file.ID)
	before := len(*calls)
	result, err := svc.Rollback(order.ID, RollbackOptions{
		InstanceIDs: []uint{insts[0].ID, insts[1].ID}, Reason: "只有这两台有问题",
	}, 3)
	require.NoError(t, err)
	require.Equal(t, "subset", result.Scope)
	require.Equal(t, 2, result.Succeeded)
	require.Equal(t, 0, result.Failed)
	require.False(t, result.LayerRolledBack, "子集回滚不回退配置版本（§10.2）")
	require.Equal(t, layerVersionsBefore, countLayerVersions(t, db, file.ID))
	require.Equal(t, before+2, len(*calls), "子集回滚只重写 2 台")

	var t0 model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ? AND instance_id = ?", order.ID, insts[0].ID).First(&t0).Error)
	require.True(t, t0.RolledBack)
	var t2 model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ? AND instance_id = ?", order.ID, insts[2].ID).First(&t2).Error)
	require.False(t, t2.RolledBack, "子集外的目标不应被回滚")

	// --- 整批回滚：4 台全回，且配置层版本回退 ---
	before = len(*calls)
	result2, err := svc.Rollback(order.ID, RollbackOptions{Reason: "全量回滚"}, 3)
	require.NoError(t, err)
	require.Equal(t, "full", result2.Scope)
	require.Equal(t, 4, result2.Succeeded)
	require.Equal(t, before+4, len(*calls), "整批回滚应重写全部 4 台")

	got, err = svc.Get(order.ID)
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusRolledBack, got.Status)

	// --- 回滚可再回滚（§10.3，验收 7）---
	before = len(*calls)
	result3, err := svc.Rollback(order.ID, RollbackOptions{Reason: "回滚错了，恢复"}, 3)
	require.NoError(t, err)
	require.True(t, result3.RollbackOfRollback, "已回滚的单再回滚应标记为回滚的回滚")
	require.Equal(t, 4, result3.Succeeded)
	require.Equal(t, before+4, len(*calls))

	var t0After model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ? AND instance_id = ?", order.ID, insts[0].ID).First(&t0After).Error)
	require.True(t, t0After.RollbackOfRollback)
	require.False(t, t0After.RolledBack, "回滚的回滚应把 rolled_back 标志清掉")

	// 回滚记录链：最后一条指向倒数第二条
	var rollbacks []model.ConfigDeliveryRollback
	require.NoError(t, db.Where("order_id = ?", order.ID).Order("id asc").Find(&rollbacks).Error)
	require.Len(t, rollbacks, 3)
	require.True(t, rollbacks[2].RollbackOfRollback)
	require.NotNil(t, rollbacks[2].RolledBackOrderID)
	require.Equal(t, rollbacks[1].ID, *rollbacks[2].RolledBackOrderID)
}

// TestDeliveryRollbackSkipsOfflineTarget 离线目标回滚判失败（不静默挂起）。
func TestDeliveryRollbackSkipsOfflineTarget(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	for _, inst := range insts {
		require.NoError(t, db.Create(&model.InstanceConfigVersion{
			InstanceID: inst.ID, FilePath: file.Name, Content: "a=0",
			ContentHash: hashContent("a=0"),
		}).Error)
	}
	svc, _, _ := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)
	approveAndStart(t, svc, order.ID)

	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", insts[1].ID).
		Update("status", model.InstanceStatusStopped).Error)
	result, err := svc.Rollback(order.ID, RollbackOptions{}, 3)
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded)
	require.Equal(t, 1, result.Failed)
}

// countLayerVersions 统计某配置文件的层版本数量。
func countLayerVersions(t *testing.T, db *gorm.DB, configFileID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ?", configFileID).Count(&n).Error)
	return n
}

// ---------------------------------------------------------------------------
// spec §17：三种生效方式与回执诚实性
// ---------------------------------------------------------------------------

// TestActivationPushOnly 验收 7：push_only 只落盘，实例状态不变且不谎称已生效。
func TestActivationPushOnly(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, calls := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationPushOnly
	})
	approveAndStart(t, svc, order.ID)

	require.Len(t, *calls, 2)
	var targets []model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).Find(&targets).Error)
	for _, tg := range targets {
		require.Equal(t, model.DeliveryTargetActivated, tg.Status)
		require.True(t, tg.DiskWritten)
		require.False(t, tg.ActivateConfirmed, "push_only 没有得到任何生效确认")
		require.False(t, tg.BestEffort, "push_only 连请求都没发，不能标 best effort")
		require.Equal(t, ActivateCodePushOnly, tg.ReasonCode)
		require.Contains(t, tg.ActivationMessage, "未触发生效")
	}
	// 实例状态不变
	var still []model.Instance
	require.NoError(t, db.Where("id IN ?", []uint{insts[0].ID, insts[1].ID}).Find(&still).Error)
	for _, inst := range still {
		require.Equal(t, model.InstanceStatusRunning, inst.Status)
	}
}

// TestActivationHotReloadConfirmed 验收 1：插件成功重载 → activated + "配置已重新加载"。
func TestActivationHotReloadConfirmed(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, activation, _ := newDeliveryTestService(t, db, nil)
	activation.SetReloadSender(func(inst *model.Instance, paths []string, timeout time.Duration) ReloadReply {
		require.Equal(t, []string{"server.properties"}, paths)
		require.Equal(t, 10*time.Second, timeout, "重载超时须是独立的 10s，不复用业务命令 5s")
		return ReloadReply{
			Success:     true,
			Connected:   true,
			ReceiptJSON: `{"reloaded":["server.properties"],"failed":[],"costMs":42}`,
		}
	})
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationHotReload
	})
	approveAndStart(t, svc, order.ID)

	var targets []model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).Find(&targets).Error)
	for _, tg := range targets {
		require.Equal(t, model.DeliveryTargetActivated, tg.Status)
		require.True(t, tg.ActivateConfirmed)
		require.False(t, tg.BestEffort)
		require.Equal(t, ActivateCodeReloaded, tg.ReasonCode)
		require.Equal(t, "配置已重新加载", tg.ActivationMessage)
	}
}

// TestActivationHotReloadPluginFailed 验收 2：插件返回失败 → failed 且带插件错误码。
func TestActivationHotReloadPluginFailed(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 1, 1)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, activation, _ := newDeliveryTestService(t, db, nil)
	activation.SetReloadSender(func(*model.Instance, []string, time.Duration) ReloadReply {
		return ReloadReply{
			Success:     false,
			Connected:   true,
			ReceiptJSON: `{"reloaded":[],"failed":[{"path":"server.properties","code":"PARSE_ERROR","message":"第 3 行缩进非法"}]}`,
		}
	})
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationHotReload
	})
	approveAndStart(t, svc, order.ID)

	var tg model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).First(&tg).Error)
	require.Equal(t, model.DeliveryTargetFailed, tg.Status)
	// **落盘成功但生效失败**必须单独可辨（§16）
	require.True(t, tg.DiskWritten)
	require.False(t, tg.ActivateConfirmed)
	require.Equal(t, TargetCodeActivateFailed, tg.ReasonCode)
	require.Contains(t, tg.Reason, "落盘成功但生效失败")
	require.Contains(t, tg.Reason, "PARSE_ERROR", "应带插件提供的错误码")
	require.Contains(t, tg.Reason, "第 3 行缩进非法")
}

// TestActivationBestEffortNotReportedAsApplied 验收 3：BEST_EFFORT 不被标成「已生效」。
//
// 这是整个最容易做错的地方：Bukkit 的 reloadConfig() 是 void 返回，
// 插件吞了异常探针也无从得知。记 activated 是为了让流程能继续，
// 但文案与 activate_confirmed 必须明确「未提供确认」。
func TestActivationBestEffortNotReportedAsApplied(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 1, 1)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, activation, _ := newDeliveryTestService(t, db, nil)
	activation.SetReloadSender(func(*model.Instance, []string, time.Duration) ReloadReply {
		return ReloadReply{
			Success:     true,
			Connected:   true,
			ReceiptJSON: `{"bestEffort":true,"reloaded":[],"failed":[]}`,
		}
	})
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationHotReload
	})
	approveAndStart(t, svc, order.ID)

	var tg model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).First(&tg).Error)
	require.Equal(t, model.DeliveryTargetActivated, tg.Status, "best effort 仍记 activated（§14.1）")
	require.True(t, tg.BestEffort)
	require.False(t, tg.ActivateConfirmed, "best effort 绝不能标记为已确认生效")
	require.Equal(t, ActivateCodeBestEffort, tg.ReasonCode)
	require.Equal(t, "已请求重载，插件未提供确认", tg.ActivationMessage)
	// 文案不得出现「已生效」「已重新加载」这类断言
	require.NotContains(t, tg.ActivationMessage, "已生效")
	require.NotContains(t, tg.ActivationMessage, "已重新加载")
}

// TestActivationTimeoutMarksFailedAndCooldown 验收 4：超时 → failed(TIMEOUT) + 进冷静期。
func TestActivationTimeoutMarksFailedAndCooldown(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 1, 1)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, activation, _ := newDeliveryTestService(t, db, nil)
	sendCount := 0
	activation.SetReloadSender(func(*model.Instance, []string, time.Duration) ReloadReply {
		sendCount++
		return ReloadReply{Connected: true, TimedOut: true, Error: "context deadline exceeded"}
	})
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationHotReload
	})
	approveAndStart(t, svc, order.ID)

	var tg model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).First(&tg).Error)
	require.Equal(t, model.DeliveryTargetFailed, tg.Status)
	require.Equal(t, ActivateCodeTimeout, tg.ReasonCode)
	require.Contains(t, tg.ActivationMessage, "重载超时")
	require.Contains(t, tg.ActivationMessage, "可能未生效", "超时只能说可能未生效，不能断言已生效")
	require.True(t, tg.DiskWritten, "超时场景磁盘已写入，这个事实必须留下")

	// 进熔断名单：短时间内再问会被拒（不自动重试，避免反复卡服）
	require.True(t, activation.CoolingDown(insts[0].UUID, file.Name))
	out := activation.HotReload(&insts[0], order, file.Name)
	require.Equal(t, ActivateCodeCooldown, out.Code)
	require.True(t, out.CoolingDown)
	require.Equal(t, 1, sendCount, "冷静期内不应再次下发")
}

// TestActivationRestartSuccess 验收 5：restart 正常启停 → activated。
func TestActivationRestartSuccess(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 1, 1)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, activation, calls := newDeliveryTestService(t, db, nil)
	restarted := 0
	activation.SetRestarter(func(inst *model.Instance) error {
		restarted++
		// 模拟重启后实例回到 RUNNING
		return db.Model(&model.Instance{}).Where("id = ?", inst.ID).
			Update("status", model.InstanceStatusRunning).Error
	})
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationRestart
	})
	approveAndStart(t, svc, order.ID)

	require.Equal(t, 1, restarted, "restart 方式应触发一次启停")
	require.Len(t, *calls, 1, "restart 方式同样要落盘")
	var tg model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).First(&tg).Error)
	require.Equal(t, model.DeliveryTargetActivated, tg.Status)
	require.True(t, tg.ActivateConfirmed)
	require.Contains(t, tg.ActivationMessage, "已重启")
}

// TestActivationRestartStartFailed 验收 6：启动失败 → failed（计入熔断）。
func TestActivationRestartStartFailed(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, activation, _ := newDeliveryTestService(t, db, nil)
	activation.SetRestarter(func(inst *model.Instance) error {
		// 「关了没起来」：最危险的失败模式，必须判失败。
		_ = db.Model(&model.Instance{}).Where("id = ?", inst.ID).
			Update("status", model.InstanceStatusStopped).Error
		return nil
	})
	// 关掉观察窗避免 sleep 对判定的影响；把等待健康回归压到极短。
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.ActivationMethod = model.ActivationRestart
		in.BatchSizes = []int{50, 50}
		in.FailureRateThreshold = 100 // 不熔断，专测失败判定
		in.ActivateTimeoutSec = 1
	})
	// 时钟直接推进到超时，避免真的等 1 秒。
	activation.sleepFn = func(time.Duration) {}
	svcSleep := svc.sleepFn
	svc.SetSleepForTest(func(d time.Duration) { svcSleep(d) })
	approveAndStart(t, svc, order.ID)

	var targets []model.ConfigDeliveryTarget
	require.NoError(t, db.Where("order_id = ?", order.ID).Find(&targets).Error)
	for _, tg := range targets {
		require.Equal(t, model.DeliveryTargetFailed, tg.Status)
		require.Equal(t, ActivateCodeActivateTimeout, tg.ReasonCode)
		require.False(t, tg.ActivateConfirmed)
		require.True(t, tg.DiskWritten, "落盘是成功的，只有生效失败")
	}
}

// TestTranslateReloadReceipt 回执翻译的四种形态（含各种不完整回执）。
func TestTranslateReloadReceipt(t *testing.T) {
	// reloaded 明确 → 确认
	out := translateReloadReceipt(`{"reloaded":["server.properties"]}`, "server.properties", true, "")
	require.True(t, out.Confirmed)
	require.False(t, out.BestEffort)

	// applied 也算确认
	out = translateReloadReceipt(`{"applied":["./server.properties"]}`, "server.properties", true, "")
	require.True(t, out.Confirmed)

	// 插件自述 bestEffort → 不算确认
	out = translateReloadReceipt(`{"bestEffort":true}`, "server.properties", true, "")
	require.True(t, out.BestEffort)
	require.False(t, out.Confirmed)
	require.Equal(t, "已请求重载，插件未提供确认", out.Message)

	// 空回执但 success=true → 仍需 best effort（不能因为 success 就写「已重载」）
	out = translateReloadReceipt("", "server.properties", true, "")
	require.True(t, out.BestEffort)
	require.False(t, out.Confirmed)

	// 回执无法解析 → best effort
	out = translateReloadReceipt("not json", "server.properties", true, "")
	require.True(t, out.BestEffort)
	require.Equal(t, ActivateCodeReceiptUnparsed, out.Code)

	// failed 优先于 bestEffort
	out = translateReloadReceipt(`{"bestEffort":true,"failed":[{"path":"server.properties","code":"PARSE_ERROR"}]}`,
		"server.properties", false, "")
	require.False(t, out.Confirmed)
	require.False(t, out.BestEffort)
	require.Equal(t, ActivateCodePluginFailed, out.Code)
	require.Contains(t, out.Message, "PARSE_ERROR")

	// reloaded 里没有目标路径 → 不算确认
	out = translateReloadReceipt(`{"reloaded":["other.yml"]}`, "server.properties", true, "")
	require.False(t, out.Confirmed)
	require.True(t, out.BestEffort)
}

// ---------------------------------------------------------------------------
// 分批
// ---------------------------------------------------------------------------

// TestComputeBatchSizes 分批换算（percent / count / 空）。
func TestComputeBatchSizes(t *testing.T) {
	require.Equal(t, []int{100}, computeBatchSizes(100, model.DeliveryBatchPercent, []int{100}))
	require.Equal(t, []int{10, 30, 60}, computeBatchSizes(100, model.DeliveryBatchPercent, []int{10, 30, 60}))
	// 12 台按 [10,30,60]：1 / 3 / 8（余数并入最后一批）
	require.Equal(t, []int{1, 3, 8}, computeBatchSizes(12, model.DeliveryBatchPercent, []int{10, 30, 60}))
	// count 模式：不足部分并入最后一批
	require.Equal(t, []int{2, 3}, computeBatchSizes(5, model.DeliveryBatchCount, []int{2, 3}))
	require.Equal(t, []int{2, 8}, computeBatchSizes(10, model.DeliveryBatchCount, []int{2, 30}))
	// 空配置 = 单批全量
	require.Equal(t, []int{7}, computeBatchSizes(7, model.DeliveryBatchCount, nil))

	// 批号分配
	batches := assignBatches(12, model.DeliveryBatchPercent, []int{10, 30, 60})
	require.Len(t, batches, 12)
	require.Equal(t, 1, batches[0])
	require.Equal(t, 2, batches[1])
	require.Equal(t, 3, batches[11])
}

// TestDeliverySelectionRejectsImplicitAll 选择器不允许隐式全量。
func TestDeliverySelectionRejectsImplicitAll(t *testing.T) {
	db := newDeliveryTestDB(t)
	seedDeliveryInstances(t, db, 3, 3)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	_, err := svc.Create(ConfigDeliveryInput{
		ConfigFileID: file.ID, TargetLayerLevel: model.ConfigScopeGroup,
		TargetLayerScopeID: 1, BatchMode: model.DeliveryBatchPercent,
		ActivationMethod: model.ActivationPushOnly,
	}, 1)
	require.Error(t, err, "空选择器必须报错，不能隐式选中全部实例")
	require.Contains(t, err.Error(), "隐式全量")
}

// TestDeliverySelectionByGroupAndTag 分组子树 + 标签收敛。
func TestDeliverySelectionByGroupAndTag(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 4, 4)
	seedConfigFileWithLayer(t, db, "a=1")
	root := model.InstanceGroupNode{Name: "cluster", UUID: "g-root"}
	require.NoError(t, db.Create(&root).Error)
	child := model.InstanceGroupNode{Name: "region1", UUID: "g-child", ParentID: &root.ID}
	require.NoError(t, db.Create(&child).Error)
	for _, inst := range insts[:3] {
		require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: child.ID, InstanceID: inst.ID}).Error)
	}
	// 给前 2 台打 tag
	require.NoError(t, db.Model(&model.Instance{}).Where("id IN ?", []uint{insts[0].ID, insts[1].ID}).
		Update("tags", `["prod","web"]`).Error)

	svc, _, _ := newDeliveryTestService(t, db, nil)
	// 根分组 + 含子树 → 3 台
	got, err := svc.resolveSelector(DeliverySelector{GroupIDs: []uint{root.ID}, IncludeDescendants: true})
	require.NoError(t, err)
	require.Len(t, got, 3)
	// 根分组不含子树 → 0 台（成员挂在子节点上）
	got, err = svc.resolveSelector(DeliverySelector{GroupIDs: []uint{root.ID}})
	require.NoError(t, err)
	require.Len(t, got, 0)
	// 子树 + tag=prod → 2 台
	got, err = svc.resolveSelector(DeliverySelector{
		GroupIDs: []uint{root.ID}, IncludeDescendants: true, Tags: []string{"prod"},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

// TestDeliveryObserveExcludesDeferredFromFailureRate 观察数据的失败率分母排除 deferred。
func TestDeliveryObserveExcludesDeferredFromFailureRate(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 20, 19)
	file := seedConfigFileWithLayer(t, db, "a=1")
	fail := map[uint]bool{insts[0].ID: true, insts[1].ID: true}
	svc, _, _ := newDeliveryTestService(t, db, fail)
	order := createDraftOrder(t, svc, db, file, insts, func(in *ConfigDeliveryInput) {
		in.FailureRateThreshold = 100 // 不熔断，专测口径
	})
	approveAndStart(t, svc, order.ID)

	observe, err := svc.Observe(order.ID)
	require.NoError(t, err)
	require.Equal(t, 2, observe.Measured.Failed)
	require.Equal(t, 1, observe.Measured.Deferred)
	// 分母应为 20-1 = 19，故 2/19 = 10.53%（而不是 2/20 = 10%）
	require.InDelta(t, 10.53, observe.Measured.FailureRate, 0.01)
	require.False(t, observe.Measured.FailureRate == 10.0, "分母必须排除 deferred")
	require.NotEmpty(t, observe.Batches)
}

// TestDeliveryImpactPreview 影响预览逐台比对。
func TestDeliveryImpactPreview(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 3, 2) // 1 台离线
	file := seedConfigFileWithLayer(t, db, "a=1")
	setInstanceLayer(t, db, file.ID, insts[0].ID, "a=1") // 该实例已有历史版本
	svc, _, _ := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)

	impact, err := svc.Impact(order.ID)
	require.NoError(t, err)
	require.Equal(t, 3, impact.Planned)
	require.Equal(t, 2, impact.Online)
	require.Equal(t, 1, impact.Offline)
	require.InDelta(t, 33.33, impact.OfflineRate, 0.01)
	// 1/3 = 33% ≥ 10% → 按离线率会熔断（预览要能提前告知这件事）
	require.True(t, impact.WouldCircuitBreak)
	require.True(t, shouldBreakOnOffline(1, 3))
	// 没有历史版本的实例一律算「会变」，不能被误判为 unchanged
	require.Equal(t, 0, impact.Unchanged)
	require.Equal(t, 3, impact.Changed)
}

// setInstanceLayer 给某实例写一条实例层版本（让 Effective 命中它）。
func setInstanceLayer(t *testing.T, db *gorm.DB, configFileID, instanceID uint, content string) {
	t.Helper()
	require.NoError(t, db.Create(&model.ConfigLayerVersion{
		ConfigFileID: configFileID,
		ScopeLevel:   model.ConfigScopeInstance,
		ScopeRefID:   instanceID,
		VersionNo:    1,
		Content:      content,
		ContentHash:  hashContent(content),
	}).Error)
}

// TestDeliveryInvalidBatchSizes 非法分批参数被拒。
func TestDeliveryInvalidBatchSizes(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, _ := newDeliveryTestService(t, db, nil)
	_, err := svc.Create(ConfigDeliveryInput{
		ConfigFileID: file.ID, TargetLayerLevel: model.ConfigScopeGroup, TargetLayerScopeID: 1,
		Selector:         DeliverySelector{InstanceIDs: []uint{insts[0].ID}},
		BatchMode:        model.DeliveryBatchPercent,
		BatchSizes:       []int{60, 60},
		ActivationMethod: model.ActivationPushOnly,
	}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "不能超过 100")
}

// TestDeliveryCancelSkipsPending 终止把未下发目标置 skipped。
func TestDeliveryCancelSkipsPending(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 4, 4)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, _ := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)
	_, err := svc.Submit(order.ID)
	require.NoError(t, err)
	_, err = svc.Approve(order.ID, 2)
	require.NoError(t, err)

	cancelled, err := svc.Cancel(order.ID, "运维决定放弃")
	require.NoError(t, err)
	require.Equal(t, model.DeliveryStatusCancelled, cancelled.Status)
	var skipped int64
	require.NoError(t, db.Model(&model.ConfigDeliveryTarget{}).
		Where("order_id = ? AND status = ?", order.ID, model.DeliveryTargetSkipped).Count(&skipped).Error)
	require.Equal(t, int64(4), skipped)
	require.Equal(t, TargetCodeCancelled, func() string {
		var tg model.ConfigDeliveryTarget
		require.NoError(t, db.Where("order_id = ?", order.ID).First(&tg).Error)
		return tg.ReasonCode
	}())
}

// TestDeliveryJSONRoundTrip 交付单的 JSON 字段可透出且不泄露 selector 原文列。
func TestDeliveryJSONRoundTrip(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 2, 2)
	file := seedConfigFileWithLayer(t, db, "a=1")
	svc, _, _ := newDeliveryTestService(t, db, nil)
	order := createDraftOrder(t, svc, db, file, insts, nil)
	raw, err := json.Marshal(order)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"plannedCount":2`)
	require.NotContains(t, string(raw), "selectorJSON", "选择器原文列不应外泄")
}
