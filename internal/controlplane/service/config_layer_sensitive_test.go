package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newSensitiveConfigHarness 建一个带敏感字段声明的 properties 配置文件 + 一个实例作用域。
func newSensitiveConfigHarness(t *testing.T) (*ConfigFileService, model.Instance) {
	t.Helper()
	db := newConfigTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ConfigFile{}, &model.ConfigLayerVersion{}, &model.Instance{}, &model.InstanceGroupNode{}))

	inst := model.Instance{
		UUID: "inst-sensitive", Name: "sensitive-inst",
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect,
		StartCommand: "noop", Status: model.InstanceStatusStopped, WorkDir: "/srv/sensitive",
	}
	require.NoError(t, db.Create(&inst).Error)

	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{
		Name: "server.properties", Format: string(model.ConfigFormatProperties),
		SensitivePaths: []string{"rcon.password", "token"},
	}, 1)
	require.NoError(t, err)
	require.NotZero(t, file.ID)
	return svc, inst
}

const sensitiveContent = "motd=hello\nrcon.password=test-secret-before\ntoken=abcdef\n"

// TestLayerHistory_MasksSensitiveValues 历史接口不得返回敏感字段原文。
func TestLayerHistory_MasksSensitiveValues(t *testing.T) {
	svc, inst := newSensitiveConfigHarness(t)

	files, _, err := svc.List("", 1, 10)
	require.NoError(t, err)
	require.Len(t, files, 1)
	fileID := files[0].ID

	_, err = svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: sensitiveContent,
	}, 1)
	require.NoError(t, err)

	history, err := svc.LayerHistory(fileID, model.ConfigScopeInstance, inst.ID)
	require.NoError(t, err)
	require.Len(t, history, 1)
	content := history[0].Content

	require.NotContains(t, content, "test-secret-before", "历史不该返回 rcon 密码原文")
	require.NotContains(t, content, "abcdef", "历史不该返回 token 原文")
	require.Contains(t, content, "***")
	// 非敏感行保持不变。
	require.Contains(t, content, "motd=hello")

	// 内部链路仍能拿到原文（下发写盘不能写 ***）。
	raw, err := svc.LayerHistoryUnmasked(fileID, model.ConfigScopeInstance, inst.ID)
	require.NoError(t, err)
	require.Contains(t, raw[0].Content, "test-secret-before")
}

// TestSaveLayer_RestoresMaskedPlaceholder 基于遮罩文本保存时，未改动的敏感值不得被写成 ***。
func TestSaveLayer_RestoresMaskedPlaceholder(t *testing.T) {
	svc, inst := newSensitiveConfigHarness(t)
	files, _, err := svc.List("", 1, 10)
	require.NoError(t, err)
	fileID := files[0].ID

	_, err = svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: sensitiveContent,
	}, 1)
	require.NoError(t, err)

	// 模拟用户在遮罩文本上改 motd 后保存（敏感值仍是 ***）。
	masked := "motd=changed\nrcon.password=***\ntoken=***\n"
	_, err = svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: masked,
	}, 1)
	require.NoError(t, err)

	history, err := svc.LayerHistoryUnmasked(fileID, model.ConfigScopeInstance, inst.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	latest := history[0].Content
	require.Contains(t, latest, "motd=changed")
	require.Contains(t, latest, "test-secret-before", "未改动的敏感值应还原为原文而不是 ***")
	require.Contains(t, latest, "abcdef")
	require.NotContains(t, latest, maskedSecretPlaceholder)
}

// TestSaveLayer_KeepsExplicitNewSecret 用户显式改敏感值时应保留新值。
func TestSaveLayer_KeepsExplicitNewSecret(t *testing.T) {
	svc, inst := newSensitiveConfigHarness(t)
	files, _, err := svc.List("", 1, 10)
	require.NoError(t, err)
	fileID := files[0].ID

	_, err = svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: sensitiveContent,
	}, 1)
	require.NoError(t, err)

	changed := "motd=hello\nrcon.password=test-secret-after\ntoken=abcdef\n"
	_, err = svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: changed,
	}, 1)
	require.NoError(t, err)

	history, err := svc.LayerHistoryUnmasked(fileID, model.ConfigScopeInstance, inst.ID)
	require.NoError(t, err)
	require.Contains(t, history[0].Content, "test-secret-after")
}

// TestMaskSensitiveInContent_PropertiesAndYAML 遮罩启发式按格式生效。
func TestMaskSensitiveInContent_PropertiesAndYAML(t *testing.T) {
	props := maskSensitiveInContent(string(model.ConfigFormatProperties), "password=abc\nmotd=x\n", []string{"password"})
	require.Contains(t, props, "password=***")
	require.Contains(t, props, "motd=x")

	yml := maskSensitiveInContent(string(model.ConfigFormatYAML), "password: abc\nmotd: x\n", []string{"password"})
	// 结构化遮罩后按 YAML 回写，引号样式由 yaml.v3 决定（'***' 或 "***"），
	// 断言只锁「值被替换」这个语义，不锁引号字符。
	require.Contains(t, yml, "password:")
	require.NotContains(t, yml, "abc", "YAML 敏感值必须被遮罩")
	require.Contains(t, yml, "***")
	require.Contains(t, yml, "motd: x")
}

// TestSaveLayer_RejectsStaleExpectedHead 乐观并发：基于旧 head 的保存必须被拒，
// 否则两人同时编辑同一层时，后保存者会静默把前者的整层内容覆盖掉。
func TestSaveLayer_RejectsStaleExpectedHead(t *testing.T) {
	svc, inst := newSensitiveConfigHarness(t)

	files, _, err := svc.List("", 1, 10)
	require.NoError(t, err)
	require.Len(t, files, 1)
	fileID := files[0].ID

	v1, err := svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: "motd=v1\n",
	}, 1)
	require.NoError(t, err)

	// A 与 B 都基于 v1 编辑；A 先保存成功。
	v2, err := svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: "motd=v2\n",
		ExpectedHeadVersionID: v1.ID,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, 2, v2.VersionNo)

	// B 带着已被取代的 expectedHead 再存：必须拒，且不生成新版本。
	_, err = svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: "motd=b-wins\n",
		ExpectedHeadVersionID: v1.ID,
	}, 2)
	require.ErrorIs(t, err, ErrConfigLayerConflict)

	history, err := svc.LayerHistory(fileID, model.ConfigScopeInstance, inst.ID)
	require.NoError(t, err)
	require.Len(t, history, 2, "冲突的保存不应写入新版本")
	require.Contains(t, history[0].Content, "v2")
}

// TestSaveLayer_AcceptsMatchingExpectedHead 带着最新 head 的保存照常成功（不误伤正常路径）。
func TestSaveLayer_AcceptsMatchingExpectedHead(t *testing.T) {
	svc, inst := newSensitiveConfigHarness(t)
	files, _, err := svc.List("", 1, 10)
	require.NoError(t, err)
	fileID := files[0].ID

	v1, err := svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: "motd=v1\n",
	}, 1)
	require.NoError(t, err)

	v2, err := svc.SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeInstance), ScopeID: inst.ID, Content: "motd=v2\n",
		ExpectedHeadVersionID: v1.ID,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, 2, v2.VersionNo)
}

// TestMaskSensitiveInContent_NestedPaths 点分路径必须命中嵌套结构。
//
// 原实现只把「行里的末段键名」与整条路径比较，`database.password` 配
// `password: xxx` 永远对不上，嵌套凭据就在 LayerHistory/Effective 里明文返回。
func TestMaskSensitiveInContent_NestedPaths(t *testing.T) {
	yml := "server:\n  port: 8080\ndatabase:\n  host: db\n  password: s3cret\n"
	got := maskSensitiveInContent(string(model.ConfigFormatYAML), yml, []string{"database.password"})
	require.NotContains(t, got, "s3cret", "嵌套 YAML 凭据必须被遮罩")
	require.Contains(t, got, "***")
	require.Contains(t, got, "db", "同级非敏感值不受影响")
	require.Contains(t, got, "8080")

	js := `{"database":{"host":"db","password":"s3cret"},"motd":"hello"}`
	gotJSON := maskSensitiveInContent(string(model.ConfigFormatJSON), js, []string{"database.password"})
	require.NotContains(t, gotJSON, "s3cret")
	require.Contains(t, gotJSON, "hello")
}

// TestMaskSensitiveInContent_ArrayIndexPath 数组下标路径也要能定位。
func TestMaskSensitiveInContent_ArrayIndexPath(t *testing.T) {
	js := `{"servers":[{"pass":"a"},{"pass":"b"}]}`
	got := maskSensitiveInContent(string(model.ConfigFormatJSON), js, []string{"servers.1.pass"})
	require.NotContains(t, got, `"b"`, "命中下标的值必须被遮罩")
	require.Contains(t, got, `"a"`, "未命中的兄弟项不受影响")
}

// TestMaskSensitiveInContent_UnparsableFallsBack 解析不了时仍要按末段键名兜底，
// 绝不能因为「解析失败」就当没有敏感项。
func TestMaskSensitiveInContent_UnparsableFallsBack(t *testing.T) {
	broken := "this: [is not\n  valid yaml: json\npassword: s3cret\n"
	got := maskSensitiveInContent(string(model.ConfigFormatYAML), broken, []string{"database.password"})
	require.NotContains(t, got, "s3cret", "解析失败也必须按末段键名遮罩")
}

// TestRestoreMaskedInContent_NestedPathRoundTrip 嵌套路径「遮罩 → 保存」必须能还原。
//
// mask 改成结构化按路径遮罩后，若 restore 仍按整条路径匹配，嵌套那一行还原不回来，
// 用户保存时真实密码会被 `***` 静默覆盖——这是数据损坏，不是展示问题。
func TestRestoreMaskedInContent_NestedPathRoundTrip(t *testing.T) {
	head := "database:\n  host: db\n  password: s3cret\n"
	masked := maskSensitiveInContent(string(model.ConfigFormatYAML), head, []string{"database.password"})
	require.NotContains(t, masked, "s3cret", "前置：嵌套值必须已被遮罩")
	require.Contains(t, masked, "***")

	restored := restoreMaskedInContent(string(model.ConfigFormatYAML), masked, head, []string{"database.password"})
	require.Contains(t, restored, "s3cret", "保存时必须还原成真实值，不能把 *** 写回库")
	require.NotContains(t, restored, "***")
}
