package service

/*
 * 不可信配置不得下发（P1 回归）。
 *
 * 两条同源的静默失真：
 *   ① `MergeResult.Valid=false` 是死字段——某层 YAML 语法坏了被跳过，
 *      合并结果“少了整层覆盖”却照样落盘，服务还能起来，只是丢配置。
 *   ② properties 里无法解析的行与注释同等待遇——直接消失，连告警都没有。
 * 两者都改成：**结果不可信就报错**，不把不确定性推给回盘后的运行时。
 */

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestMergeStructured_InvalidLayerMarksResultUnusable 层语法错误 → Valid=false。
func TestMergeStructured_InvalidLayerMarksResultUnusable(t *testing.T) {
	res, err := MergeConfigLayers(model.ConfigFormatYAML, []MergeLayer{
		{Level: model.ConfigScopeLevelGroup, ScopeRefID: 1, Name: "root", VersionNo: 1,
			Content: "a: 1\n"},
		// 故意写坏：未闭合的方括号。
		{Level: model.ConfigScopeLevelInstance, ScopeRefID: 2, Name: "inst", VersionNo: 1,
			Content: "b: [1, 2\n"},
	})
	require.NoError(t, err, "语法错误不应在合并层直接报错（它要能给出带层信息的告警）")
	require.False(t, res.Valid, "有层解析失败时结果必须标为不可信")

	require.NotEmpty(t, res.Warnings)
	var found bool
	for _, w := range res.Warnings {
		if w.Code == WarnLayerParseFailed {
			found = true
			require.Equal(t, "error", w.Level)
		}
	}
	require.True(t, found, "应有一条 LAYER_PARSE_FAILED 告警")
}

// TestParseConfigContent_RejectsUnparsablePropertiesLines properties 坏行必须在保存时报错。
func TestParseConfigContent_RejectsUnparsablePropertiesLines(t *testing.T) {
	// 正常内容：= / : / 注释 / 空行都可以。
	_, err := ParseConfigContent(model.ConfigFormatProperties,
		"# comment\n!also comment\n\nkey1=value1\nkey2: value2\n")
	require.NoError(t, err, "正常 properties 不应报错")

	// 缺分隔符的数据行：必须报错，而不是静默丢掉。
	_, err = ParseConfigContent(model.ConfigFormatProperties, "key1=value1\nfoo bar\n")
	require.Error(t, err, "无法解析的数据行必须报错")
	require.Contains(t, err.Error(), "无法解析")

	// 续行（行尾反斜杠）：本合并器不拼接，也必须报错而不是静默丢。
	_, err = ParseConfigContent(model.ConfigFormatProperties, "key1=value1\nkey2=abc\\\ndef\n")
	require.Error(t, err, "续行必须报错（合并器不做多行拼接）")
}

// TestMergeProperties_DroppedLinesSetInvalid 合并时坏行也让结果不可信（不只保存时）。
//
// 两层防线：保存时 ParseConfigContent 会拦；但存量层（本改动前存的）
// 仍可能带坏行，合并阶段必须自己也能识别。
func TestMergeProperties_DroppedLinesSetInvalid(t *testing.T) {
	res, err := MergeConfigLayers(model.ConfigFormatProperties, []MergeLayer{
		{Level: model.ConfigScopeLevelGroup, ScopeRefID: 1, Name: "root", VersionNo: 1,
			Content: "a=1\nbroken line without separator\n"},
	})
	require.NoError(t, err)
	require.False(t, res.Valid, "存量的坏 properties 层也必须被识别为不可信")

	var found bool
	for _, w := range res.Warnings {
		if w.Code == WarnPropertyLineDropped {
			found = true
		}
	}
	require.True(t, found, "应有一条 PROPERTY_LINE_DROPPED 告警")

	// 注释与空行不算坏行。
	ok, err := MergeConfigLayers(model.ConfigFormatProperties, []MergeLayer{
		{Level: model.ConfigScopeLevelGroup, ScopeRefID: 1, VersionNo: 1,
			Content: "# c\n\n!x\na=1\n"},
	})
	require.NoError(t, err)
	require.True(t, ok.Valid, "注释与空行不得被当成坏行")
}

// TestEffectiveContent_RefusesInvalidChain EffectiveContent 遇到不可信合成就报错。
//
// 这是“不得下发”的实际落点：config_delivery 会把这个错误映射成
// TargetCodeMergeFailed（那条分支以前永远走不到）。
func TestEffectiveContent_RefusesInvalidChain(t *testing.T) {
	db := newConfigLayerTestDB(t)
	svc := NewConfigFileService(db)

	file := &model.ConfigFile{Name: "server", Format: model.ConfigFormatYAML}
	require.NoError(t, db.Create(file).Error)

	// 直接写一层坏内容（绕过保存校验，模拟存量数据）。
	// ContentHash 是 char(64) not null，给个占位值即可——本用例不走 hash 校验路径。
	require.NoError(t, db.Create(&model.ConfigLayerVersion{
		ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeLevelGroup, ScopeRefID: 1,
		VersionNo: 1, Content: "bad: [unclosed\n", ContentHash: strings.Repeat("0", 64),
	}).Error)

	_, _, err := svc.EffectiveContent(file.ID, 7)
	require.Error(t, err, "不可信的合成结果必须报错，不得返回内容")
	require.True(t, errors.Is(err, ErrConfigLayerInvalid), "应返回 ErrConfigLayerInvalid 便于调用方分支, got: %v", err)
}
