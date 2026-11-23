package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 平台管理页审查（2026-11-14）的回归用例。
//
// 每条对应用户可触发的真实操作路径，修复前必然失败；保留它们是为了不让这几类
// 「权限点给了读、却放行了写」「保存成功但数据被清空」的问题再回来。

// TestSoleSuperAdminCannotBeDisabled 唯一超管不可禁用。
func TestSoleSuperAdminCannotBeDisabled(t *testing.T) {
	db := newSuperAdminTestDB(t)
	svc := NewUserService(db)
	svc.SetPasswordCostForTest(4)
	admin, err := svc.Create("solo_admin", "Password@123", model.RolePlatformAdmin, model.UserStatusActive)
	require.NoError(t, err)

	disabled := model.UserStatusDisabled
	_, err = svc.Update(admin.ID, nil, &disabled, nil)
	require.ErrorIs(t, err, ErrSuperAdminLocked, "禁用唯一超管必须被拒绝")

	// 断言确实没落库：状态仍是启用。
	after, err := svc.GetByID(admin.ID)
	require.NoError(t, err)
	require.Equal(t, model.UserStatusActive, after.Status)
}

// TestSoleSuperAdminCannotBeDeleted 唯一超管不可删除。
func TestSoleSuperAdminCannotBeDeleted(t *testing.T) {
	db := newSuperAdminTestDB(t)
	svc := NewUserService(db)
	svc.SetPasswordCostForTest(4)
	admin, err := svc.Create("solo_admin2", "Password@123", model.RolePlatformAdmin, model.UserStatusActive)
	require.NoError(t, err)

	require.ErrorIs(t, svc.Delete(admin.ID), ErrSuperAdminLocked, "删除唯一超管必须被拒绝")
	_, err = svc.GetByID(admin.ID)
	require.NoError(t, err, "被拒绝后用户必须仍然存在")
}

// TestBindingPlatformAdminTemplateIsUnique 绑定超管模板同样受唯一性约束。
func TestBindingPlatformAdminTemplateIsUnique(t *testing.T) {
	db := newSuperAdminTestDB(t)
	users := NewUserService(db)
	users.SetPasswordCostForTest(4)
	_, err := users.Create("pa_owner", "Password@123", model.RolePlatformAdmin, model.UserStatusActive)
	require.NoError(t, err)
	member, err := users.Create("pa_member", "Password@123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)

	perm := NewPermissionService(db)
	require.NoError(t, perm.SeedSystemRoles())
	var paRole model.RoleTemplate
	require.NoError(t, db.Where("key = ?", model.RoleKeyPlatformAdmin).First(&paRole).Error)

	err = perm.BindUserRole(member.ID, paRole.ID)
	require.ErrorIs(t, err, ErrSuperAdminUnique, "已有超管时不得再绑定超管模板")

	// 断言没留下绑定：有效权限仍是普通成员。
	nodes, key, err := perm.EffectiveForUser(member.ID, member.Role)
	require.NoError(t, err)
	require.NotEqual(t, model.RoleKeyPlatformAdmin, key)
	require.NotContains(t, nodes, "user.manage")
}

// TestReplaceRoleNodesRejectsWithoutCorrupting 失败的节点替换不得留下残缺集合。
func TestReplaceRoleNodesRejectsWithoutCorrupting(t *testing.T) {
	db := newRbacHardeningDB(t)
	perm := NewPermissionService(db)
	original := []string{"instance.read", "file.read", "node.read"}
	role, err := perm.CreateRole("regress-role", "", original)
	require.NoError(t, err)

	// 重复节点属于输入冗余：应当被去重后成功写入，而不是撞唯一索引。
	_, err = perm.UpdateRole(role.ID, nil, nil, []string{"instance.read", "instance.read"}, true)
	require.NoError(t, err, "重复节点应被去重，不应失败")
	after, err := perm.RoleNodes(role.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"instance.read"}, after)
}

// TestAssistantSettingsRejectsVersionZero 行已存在时拒绝 version=0（未读设置就提交）。
func TestAssistantSettingsRejectsVersionZero(t *testing.T) {
	s := newEgressTestService(t)
	require.NoError(t, s.SaveSettings(SaveSettingsParams{
		AllowPrivateCIDRs: []string{"10.0.0.0/8"},
		SystemPrompt:      "keep-me",
		Version:           1,
	}))

	// 前端在 GET 未返回时就是发这份载荷（version=0 + 空字段）。
	off := false
	zero := SaveSettingsParams{BuiltinToolsEnabled: &off, Version: 0}
	err := s.SaveSettings(zero)
	require.ErrorIs(t, err, ErrAssistantConflict, "version=0 必须被拒绝，而不是当作跳过 CAS")

	after, err := s.GetSettings()
	require.NoError(t, err)
	require.Equal(t, "keep-me", after.SystemPrompt, "既有提示词不得被清空")
	require.Equal(t, []string{"10.0.0.0/8"}, after.AllowPrivateCIDRs, "既有放行段不得被清空")
	require.True(t, after.BuiltinToolsEnabled, "内置工具开关不得被改动")
}

// TestApprovalDriftIgnoresHeartbeatWrites 心跳推进 updated_at 不再判成漂移。
func TestApprovalDriftIgnoresHeartbeatWrites(t *testing.T) {
	db := newDriftDB(t)
	inst := seedDriftInstance(t, db, 11, "drift-live", 5)
	instances := NewInstanceService(db, nil, nil)
	evidence := CaptureTargetSnapshot(instances, nil, "instance", "11")
	require.NotNil(t, evidence)

	// 模拟一拍真实心跳：状态写同值 + GORM 自动推进 updated_at。
	later := inst.UpdatedAt.Add(30 * time.Second)
	db.NowFunc = func() time.Time { return later }
	require.NoError(t, db.Model(&model.Instance{}).
		Where("id = ?", inst.ID).Update("status", inst.Status).Error)

	verify := VerifyTargetDrift(instances, nil)
	ticket := &model.ApprovalTicket{TargetType: "instance", TargetID: "11"}
	require.NoError(t, verify(ticket, evidence), "仅心跳推进 updated_at 不应判为漂移")
}

// TestApprovalDriftDetectsConfigChange 真正的配置变更仍必须被识别。
func TestApprovalDriftDetectsConfigChange(t *testing.T) {
	db := newDriftDB(t)
	inst := seedDriftInstance(t, db, 12, "drift-cfg", 5)
	instances := NewInstanceService(db, nil, nil)
	evidence := CaptureTargetSnapshot(instances, nil, "instance", "12")
	require.NotNil(t, evidence)

	// 改启动命令 = 批准时看到的「要做什么」变了。
	require.NoError(t, db.Model(&model.Instance{}).
		Where("id = ?", inst.ID).Update("start_command", "java -Xmx1G -jar other.jar").Error)

	verify := VerifyTargetDrift(instances, nil)
	ticket := &model.ApprovalTicket{TargetType: "instance", TargetID: "12"}
	err := verify(ticket, evidence)
	require.ErrorIs(t, err, ErrApprovalTargetDrift, "启动命令改变必须判为漂移")
}

// TestDatabaseBrowseMasksKVSecrets KV 表里的凭据必须脱敏。
func TestDatabaseBrowseMasksKVSecrets(t *testing.T) {
	db := newRbacHardeningDB(t)
	require.NoError(t, db.AutoMigrate(&model.PlatformSetting{}))
	const secret = "test-secret-regression-sentinel"
	require.NoError(t, db.Create(&model.PlatformSetting{Key: SettingKeyGitHubToken, Value: secret}).Error)
	require.NoError(t, db.Create(&model.PlatformSetting{Key: "log.level", Value: "info"}).Error)

	rows, err := NewDBBrowseService(db).TableRows("platform_settings", DBRowsParams{})
	require.NoError(t, err)

	byKey := map[string]any{}
	keyCol, valCol := "", ""
	for _, c := range rows.Columns {
		switch c.Name {
		case "key":
			keyCol = c.Name
		case "value":
			valCol = c.Name
		}
	}
	require.NotEmpty(t, keyCol)
	require.NotEmpty(t, valCol)
	for _, row := range rows.Rows {
		if k, ok := row[keyCol].(string); ok {
			byKey[k] = row[valCol]
		}
	}
	require.Equal(t, maskedValue, byKey[SettingKeyGitHubToken], "凭据值必须打码")
	require.Equal(t, "info", byKey["log.level"], "非敏感项保持原值")
}

// TestAssetDeleteBlockedByBinaryBinding 二进制版本绑定（当前/回滚点）保护资产。
func TestAssetDeleteBlockedByBinaryBinding(t *testing.T) {
	db := newRbacHardeningDB(t)
	require.NoError(t, db.AutoMigrate(&model.Asset{}, &model.InstanceBinaryBinding{}))

	asset := model.Asset{SHA256: "deadbeef", Size: 1, RelPath: "blobs/deadbeef"}
	require.NoError(t, db.Create(&asset).Error)
	require.NoError(t, db.Create(&model.InstanceBinaryBinding{
		InstanceID: 42, CurrentAssetID: 99, PreviousAssetID: asset.ID,
	}).Error)

	svc := NewAssetService(db, nil)
	err := svc.Delete(asset.ID)
	var inUse *AssetInUseError
	require.ErrorAs(t, err, &inUse)
	require.Equal(t, AssetInUseBinaryBinding, inUse.Reason)
}

// TestJDKDeleteBlockedByMajorVersionBinding 只声明大版本的实例也算占用托管 JDK。
func TestJDKDeleteBlockedByMajorVersionBinding(t *testing.T) {
	db := newRbacHardeningDB(t)
	require.NoError(t, db.AutoMigrate(&model.NodeJDK{}, &model.Instance{}))

	jdk := model.NodeJDK{NodeID: 7, Vendor: "temurin", Version: "21.0.1", Arch: "x64",
		MajorVersion: 21, Path: "/jdks/temurin-21", Managed: true}
	require.NoError(t, db.Create(&jdk).Error)
	// jdk_id=0 + java_major_version=21：生产里由 resolveJDKPath 按大版本选中这个 JDK。
	require.NoError(t, db.Create(&model.Instance{
		UUID: "u-major", Name: "major-inst", NodeID: 7, Type: "generic",
		Status: model.InstanceStatusStopped, JDKID: 0, JavaMajorVersion: 21,
	}).Error)

	svc := NewJDKService(db, nil)
	_, err := svc.Delete(7, jdk.ID)
	require.ErrorIs(t, err, ErrJDKInUse, "大版本绑定也算在用")
}

// TestApprovalDriftLegacyEvidenceNotBlocked 升级前签发的票据（evidence 里是纳秒
// version、没有 versionKind）不得被判漂移——否则上线瞬间存量待审票据全失败。
func TestApprovalDriftLegacyEvidenceNotBlocked(t *testing.T) {
	db := newDriftDB(t)
	inst := seedDriftInstance(t, db, 21, "legacy-ticket", 5)
	instances := NewInstanceService(db, nil, nil)

	// 手工构造旧格式 evidence：version 是 updated_at 纳秒，无 versionKind。
	legacy := map[string]any{
		"kind":       "instance",
		"id":         inst.ID,
		"name":       inst.Name,
		"nodeId":     inst.NodeID,
		"version":    strconv.FormatInt(inst.UpdatedAt.UnixNano(), 10),
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
	}
	// 即便目标随后被改，旧票据也只按「无法校验」放行，不会出现永久失败。
	require.NoError(t, db.Model(&model.Instance{}).
		Where("id = ?", inst.ID).Update("start_command", "changed").Error)

	verify := VerifyTargetDrift(instances, nil)
	ticket := &model.ApprovalTicket{TargetType: "instance", TargetID: "21"}
	require.NoError(t, verify(ticket, legacy), "旧口径 evidence 应放行而非判漂移")
}

// TestFingerprintCoversBinaryBinding 指纹必须覆盖二进制绑定（换 jar 属于目标替换）。
func TestFingerprintCoversBinaryBinding(t *testing.T) {
	db := newDriftDB(t)
	require.NoError(t, db.AutoMigrate(&model.InstanceBinaryBinding{}))
	inst := seedDriftInstance(t, db, 31, "bind-fp", 5)
	instances := NewInstanceService(db, nil, nil)

	before := CaptureTargetSnapshot(instances, nil, "instance", "31")
	require.NotNil(t, before)
	require.NoError(t, db.Create(&model.InstanceBinaryBinding{
		InstanceID: inst.ID, CurrentAssetID: 7, CurrentSHA256: "aaa",
	}).Error)
	afterBind := CaptureTargetSnapshot(instances, nil, "instance", "31")
	require.NotNil(t, afterBind)
	require.NotEqual(t, before["version"], afterBind["version"], "新增绑定必须改变指纹")

	// 换版本（模拟升级）也要变。
	require.NoError(t, db.Model(&model.InstanceBinaryBinding{}).Where("instance_id = ?", inst.ID).
		Update("current_asset_id", 9).Error)
	afterUpgrade := CaptureTargetSnapshot(instances, nil, "instance", "31")
	require.NotEqual(t, afterBind["version"], afterUpgrade["version"], "换资产必须改变指纹")

	// 无变化时稳定。
	again := CaptureTargetSnapshot(instances, nil, "instance", "31")
	require.Equal(t, afterUpgrade["version"], again["version"], "同一状态指纹必须稳定")
}

// TestCreateRoleDedupesNodes 新建角色时重复节点按集合收下，不整单失败。
func TestCreateRoleDedupesNodes(t *testing.T) {
	db := newRbacHardeningDB(t)
	perm := NewPermissionService(db)
	role, err := perm.CreateRole("dup-role", "", []string{"instance.read", "instance.read", "node.read"})
	require.NoError(t, err, "重复节点应被去重，不应失败")
	nodes, err := perm.RoleNodes(role.ID)
	require.NoError(t, err)
	require.Len(t, nodes, 2)
}
