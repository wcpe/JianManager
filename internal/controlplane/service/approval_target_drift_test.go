package service

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 验收：执行前目标漂移校验。
//
// ## 为什么这组用例必需
//
// 审批的意义是「有权限的人批准了一件**具体的事**」。若从批准到执行之间目标变了，
// 执行就等于把批准人的授权挪用到他没看过的东西上——这是审批机制最根本的失效方式。
//
// 而该能力此前**完全没接线**：`SetTargetVerifier` 只有定义、从未被调用，
// `Evidence` 在拦截层传的是 nil。也就是说「漂移即 failed 且不执行」这条验收
// 在真机上从未生效过。这组用例锁住它，避免再次退化成「写了但没人调」。

func newDriftDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:drift_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}

	// 关闭底层连接（共享内存库在最后一个连接关闭后才释放，见 testdb_test.go）。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Instance{}, &model.Node{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func seedDriftInstance(t *testing.T, db *gorm.DB, id uint, name string, nodeID uint) *model.Instance {
	t.Helper()
	inst := model.Instance{ID: id, UUID: "uuid-" + name, Name: name, NodeID: nodeID, Type: "generic", Status: "STOPPED"}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatalf("建实例失败: %v", err)
	}
	return &inst
}

// TestCaptureTargetSnapshot_Instance 采集实例快照，含节点归属与版本。
func TestCaptureTargetSnapshot_Instance(t *testing.T) {
	db := newDriftDB(t)
	inst := seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)

	snap := CaptureTargetSnapshot(svc, nil, "instance", "7")
	if snap == nil {
		t.Fatal("应采集到实例快照")
	}
	if snap["kind"] != "instance" {
		t.Fatalf("kind 应为 instance，实际 %v", snap["kind"])
	}
	if snap["nodeId"] != inst.NodeID {
		t.Fatalf("应冻结节点归属 #%d，实际 %v", inst.NodeID, snap["nodeId"])
	}
	// version 以十进制字符串冻结（防 float64 精度丢失，见实现文件头说明）
	if v, ok := snap["version"].(string); !ok || v != strconv.FormatInt(inst.UpdatedAt.UnixNano(), 10) {
		t.Fatalf("应冻结 updatedAt 纳秒版本为十进制字符串，实际 %v", snap["version"])
	}
}

// TestCaptureTargetSnapshot_MissingTargetIsNil 目标不存在时返回 nil（票据照常落库）。
//
// 申请阶段的首要职责是**记录意图**；目标不存在由后续授权与执行阶段拒绝更合适
// （那里信息更准确）。此处报错会让「删除一个刚被删掉的实例」这类操作直接 500。
func TestCaptureTargetSnapshot_MissingTargetIsNil(t *testing.T) {
	db := newDriftDB(t)
	svc := NewInstanceService(db, nil, nil)
	if snap := CaptureTargetSnapshot(svc, nil, "instance", "999"); snap != nil {
		t.Fatalf("目标不存在应返回 nil，实际 %+v", snap)
	}
}

// TestVerifyTargetDrift_NoDriftPasses 目标未变时通过。
func TestVerifyTargetDrift_NoDriftPasses(t *testing.T) {
	db := newDriftDB(t)
	seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)

	snap := CaptureTargetSnapshot(svc, nil, "instance", "7")
	verify := VerifyTargetDrift(svc, nil)
	if verify == nil {
		t.Fatal("应返回校验器")
	}
	ticket := &model.ApprovalTicket{TargetType: "instance", TargetID: "7"}
	if err := verify(ticket, snap); err != nil {
		t.Fatalf("目标未变不应报漂移: %v", err)
	}
}

// TestVerifyTargetDrift_UpdatedAfterApprovalFails 实例在申请后被修改 → 判漂移。
//
// 这是主判据：改配置、换二进制、改名都会推进 updated_at。
func TestVerifyTargetDrift_UpdatedAfterApprovalFails(t *testing.T) {
	db := newDriftDB(t)
	seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)
	snap := CaptureTargetSnapshot(svc, nil, "instance", "7")

	// 模拟「批准后有人改了实例」
	time.Sleep(2 * time.Millisecond)
	if err := db.Model(&model.Instance{}).Where("id = ?", 7).
		Update("name", "renamed-after-approval").Error; err != nil {
		t.Fatalf("改实例失败: %v", err)
	}

	verify := VerifyTargetDrift(svc, nil)
	err := verify(&model.ApprovalTicket{TargetType: "instance", TargetID: "7"}, snap)
	if err == nil {
		t.Fatal("实例在申请后被修改，应判漂移")
	}
	if !errorsIs(err, ErrApprovalTargetDrift) {
		t.Fatalf("应包装 ErrApprovalTargetDrift，实际 %v", err)
	}
}

// TestVerifyTargetDrift_NodeMigrationFails 实例被迁到别的节点 → 判漂移。
//
// 原批准隐含「在哪台机器上执行」，迁移后这个前提不成立了。
func TestVerifyTargetDrift_NodeMigrationFails(t *testing.T) {
	db := newDriftDB(t)
	seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)
	snap := CaptureTargetSnapshot(svc, nil, "instance", "7")

	if err := db.Model(&model.Instance{}).Where("id = ?", 7).
		Update("node_id", 9).Error; err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	verify := VerifyTargetDrift(svc, nil)
	err := verify(&model.ApprovalTicket{TargetType: "instance", TargetID: "7"}, snap)
	if err == nil {
		t.Fatal("实例已换节点，应判漂移")
	}
	if !errorsIs(err, ErrApprovalTargetDrift) {
		t.Fatalf("应包装 ErrApprovalTargetDrift，实际 %v", err)
	}
}

// TestVerifyTargetDrift_TargetDeletedFails 目标被删 → 判漂移。
func TestVerifyTargetDrift_TargetDeletedFails(t *testing.T) {
	db := newDriftDB(t)
	seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)
	snap := CaptureTargetSnapshot(svc, nil, "instance", "7")

	if err := db.Delete(&model.Instance{}, 7).Error; err != nil {
		t.Fatalf("删实例失败: %v", err)
	}

	verify := VerifyTargetDrift(svc, nil)
	err := verify(&model.ApprovalTicket{TargetType: "instance", TargetID: "7"}, snap)
	if err == nil {
		t.Fatal("目标已删除，应判漂移")
	}
}

// TestVerifyTargetDrift_NoEvidenceDoesNotBlock 无快照（旧票据）时不阻断。
//
// 这是**有意的取舍**：宁可放过一次无法校验的执行，也不要让上线瞬间
// 堆积一批「批准了但永远失败」的存量票。取舍原因写在实现文件头。
func TestVerifyTargetDrift_NoEvidenceDoesNotBlock(t *testing.T) {
	db := newDriftDB(t)
	seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)

	verify := VerifyTargetDrift(svc, nil)
	if err := verify(&model.ApprovalTicket{TargetType: "instance", TargetID: "7"}, nil); err != nil {
		t.Fatalf("无快照不应阻断: %v", err)
	}
	if err := verify(&model.ApprovalTicket{TargetType: "instance", TargetID: "7"}, map[string]any{}); err != nil {
		t.Fatalf("空快照不应阻断: %v", err)
	}
}

// TestVerifyTargetDrift_JSONRoundTrip 快照经 JSON 往返后仍能正确判定。
//
// 这条守的是一个真实的坑：落库时 evidence 被序列化成 JSON 文本，
// 读出来再 Unmarshal 成 map[string]any 后，**所有数字都变成 float64**。
// 若实现里直接断言 int64/uint，真实路径必然失败——而用构造好的 map 写的用例
// 发现不了这一点（它们传的就是 int64）。
func TestVerifyTargetDrift_JSONRoundTrip(t *testing.T) {
	db := newDriftDB(t)
	inst := seedDriftInstance(t, db, 7, "acc-inst", 3)
	svc := NewInstanceService(db, nil, nil)

	raw := CaptureTargetSnapshot(svc, nil, "instance", "7")
	blob, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var restored map[string]any
	if err := json.Unmarshal(blob, &restored); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	// 确认前提：往返后 version 是**字符串**。
	// 这是有意的落库格式：纳秒时间戳（约 1.8e18）超出 float64 精确整数上限 2^53，
	// 若存成数字，往返一次就被舍入，目标没变也会被判漂移 → 每张票据执行永久失败。
	// 不要把它「简化」成数字。
	if _, ok := restored["version"].(string); !ok {
		t.Fatalf("前提不成立：往返后 version 应为 string（防精度丢失），实际 %T", restored["version"])
	}

	verify := VerifyTargetDrift(svc, nil)
	ticket := &model.ApprovalTicket{TargetType: "instance", TargetID: "7"}

	// 未变 → 通过
	if err := verify(ticket, restored); err != nil {
		t.Fatalf("JSON 往返后目标未变应通过，实际 %v", err)
	}

	// 变了 → 必须也能判出漂移
	time.Sleep(2 * time.Millisecond)
	if err := db.Model(&model.Instance{}).Where("id = ?", 7).Update("name", "x").Error; err != nil {
		t.Fatalf("改实例失败: %v", err)
	}
	if err := verify(ticket, restored); err == nil {
		t.Fatal("JSON 往返后仍应能判出漂移")
	}
	_ = inst
}

// TestVerifyTargetDrift_NanosecondVersionSurvivesFloat64Trap 守住 version 的精度：
// 数字形态的纳秒 version 经 JSON 往返必然失真，因此落库格式必须是字符串。
// 本用例直接证明「数字会坏、字符串不会」，防止后来者把字符串改回数字。
func TestVerifyTargetDrift_NanosecondVersionSurvivesFloat64Trap(t *testing.T) {
	const nano = int64(1_760_000_000_123_456_789) // 2025-10 量级，远超 2^53

	// 数字形态：往返后与真值不等（float64 舍入）——这正是不该存数字的原因
	blob, err := json.Marshal(map[string]any{"version": nano})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var asNumber map[string]any
	if err := json.Unmarshal(blob, &asNumber); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if got := toVersionFromAny(asNumber["version"]); got == nano {
		t.Fatalf("前提不成立：数字形态应丢精度，实际无损（%d）", got)
	}

	// 字符串形态：往返后与真值完全相等
	blob, err = json.Marshal(map[string]any{"version": strconv.FormatInt(nano, 10)})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var asString map[string]any
	if err := json.Unmarshal(blob, &asString); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if got := toVersionFromAny(asString["version"]); got != nano {
		t.Fatalf("字符串形态应无损，期望 %d，实际 %d", nano, got)
	}
}

// TestVerifyTargetDrift_NilServicesReturnsNil 两个服务都不可用时不注入校验器。
func TestVerifyTargetDrift_NilServicesReturnsNil(t *testing.T) {
	if v := VerifyTargetDrift(nil, nil); v != nil {
		t.Fatal("服务全不可用时应返回 nil（装配层据此跳过注入）")
	}
}

// errorsIs 是 errors.Is 的薄封装，避免本文件额外导入 errors 与既有导入冲突。
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
