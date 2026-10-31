package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置分层合并单测（§5 / §11 验收）。
//
// 合并是纯函数，这里只断言输入输出，不涉及 DB / gRPC。

// mustMerge 是合并的执行包装：断言无错误并返回结果。
func mustMerge(t *testing.T, format model.ConfigFileFormat, layers []MergeLayer) *MergeResult {
	t.Helper()
	res, err := MergeConfigLayers(format, layers)
	require.NoError(t, err, "合并不应报错")
	require.NotNil(t, res)
	return res
}

// groupLayer 构造一个分组层输入。
func groupLayer(id uint, name string, versionNo int, content string) MergeLayer {
	return MergeLayer{
		Level:      model.ConfigScopeGroup,
		ScopeRefID: id,
		Name:       name,
		VersionNo:  versionNo,
		Content:    content,
	}
}

// instanceLayer 构造一个实例层输入。
func instanceLayer(id uint, name string, versionNo int, content string) MergeLayer {
	return MergeLayer{
		Level:      model.ConfigScopeInstance,
		ScopeRefID: id,
		Name:       name,
		VersionNo:  versionNo,
		Content:    content,
	}
}

// TestMergeYAML_MultiLayerOverride 验证「只在某层覆盖某键、其它层不覆盖」时各目标取到正确值（验收 1）。
func TestMergeYAML_MultiLayerOverride(t *testing.T) {
	// region1 覆盖 motd，region2 不覆盖：两个实例各自合并自己的链。
	root := groupLayer(1, "root", 1, "port: 25565\nmotd: default\n")
	region1 := groupLayer(2, "region1", 3, "motd: region1-motd\n")
	region2 := groupLayer(3, "region2", 1, "")
	inst1 := instanceLayer(101, "srv-1", 1, "")
	inst2 := instanceLayer(102, "srv-2", 1, "")

	res1 := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{root, region1, inst1})
	res2 := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{root, region2, inst2})

	require.Contains(t, res1.Content, "motd: region1-motd")
	require.Contains(t, res2.Content, "motd: default")
	// port 都来自 root。
	require.Contains(t, res1.Content, "port: 25565")
	require.Contains(t, res2.Content, "port: 25565")
	require.NotEqual(t, res1.EffectiveHash, res2.EffectiveHash, "两实例有效值不同，hash 必须不同")
}

// TestMergeYAML_NullDeletesKey 验证 yaml 下 `null` 删键（验收 2）。
func TestMergeYAML_NullDeletesKey(t *testing.T) {
	root := groupLayer(1, "root", 1, "port: 25565\ndebug: true\nmotd: hello\n")
	override := groupLayer(2, "child", 1, "debug: null\n")

	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{root, override})

	require.NotContains(t, res.Content, "debug", "null 应删掉 debug 键")
	require.Contains(t, res.Content, "port: 25565")
	require.Contains(t, res.Content, "motd: hello")

	traced, ok := res.Traced["debug"]
	require.True(t, ok, "被删除的键必须留在 traced 里")
	require.True(t, traced.Deleted, "必须标记 deleted=true")
	require.Nil(t, traced.Value)
	require.Equal(t, uint(2), traced.Source.ID)
	require.Equal(t, 1, traced.Source.VersionNo)
}

// TestMergeJSON_NullDeletesKey 验证 json 下 `null` 删键（验收 2）。
func TestMergeJSON_NullDeletesKey(t *testing.T) {
	root := groupLayer(1, "root", 1, `{"port":25565,"debug":true}`)
	override := groupLayer(2, "child", 1, `{"debug":null}`)

	res := mustMerge(t, model.ConfigFormatJSON, []MergeLayer{root, override})

	require.NotContains(t, res.Content, "debug")
	require.Contains(t, res.Content, "25565")
	require.True(t, res.Traced["debug"].Deleted)
}

// TestMergeProperties_NullDoesNotDelete 验证 properties 不支持删键（验收 2）。
//
// properties 里 `debug=null` 就是字符串 "null"，是普通值而不是删除语义。
func TestMergeProperties_NullDoesNotDelete(t *testing.T) {
	root := groupLayer(1, "root", 1, "port=25565\ndebug=true\n")
	override := groupLayer(2, "child", 1, "debug=null\n")

	res := mustMerge(t, model.ConfigFormatProperties, []MergeLayer{root, override})

	require.Contains(t, res.Content, "debug=null", "properties 的 null 是普通字符串值")
	traced := res.Traced["debug"]
	require.False(t, traced.Deleted, "properties 不支持删键，不得标记 deleted")
	require.Equal(t, "null", traced.Value)
	require.Equal(t, uint(2), traced.Source.ID, "值应来自更高优先级的层")
}

// TestMergeProperties_KeyOverride 验证 properties 键级覆盖。
func TestMergeProperties_KeyOverride(t *testing.T) {
	root := groupLayer(1, "root", 1, "server-port=25565\nmotd=hello\nmax-players=20\n")
	override := groupLayer(2, "child", 1, "motd=world\n")

	res := mustMerge(t, model.ConfigFormatProperties, []MergeLayer{root, override})

	require.Contains(t, res.Content, "server-port=25565")
	require.Contains(t, res.Content, "motd=world")
	require.Contains(t, res.Content, "max-players=20")
	require.Equal(t, uint(2), res.Traced["motd"].Source.ID)
	require.Equal(t, uint(1), res.Traced["server-port"].Source.ID)
}

// TestMerge_ArrayWholeReplacement 验证数组整体替换而非元素合并（验收 3）。
func TestMerge_ArrayWholeReplacement(t *testing.T) {
	root := groupLayer(1, "root", 1, "list:\n  - a\n  - b\n")
	override := groupLayer(2, "child", 1, "list:\n  - c\n")

	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{root, override})

	require.Contains(t, res.Content, "- c")
	require.NotContains(t, res.Content, "- a", "数组必须整体替换，不能保留低层元素")
	require.NotContains(t, res.Content, "- b")
}

// TestMerge_JSON_ArrayWholeReplacement 验证 json 数组整体替换。
func TestMerge_JSON_ArrayWholeReplacement(t *testing.T) {
	root := groupLayer(1, "root", 1, `{"list":["a","b"]}`)
	override := groupLayer(2, "child", 1, `{"list":["c"]}`)

	res := mustMerge(t, model.ConfigFormatJSON, []MergeLayer{root, override})

	require.Contains(t, res.Content, `"c"`)
	require.NotContains(t, res.Content, `"a"`)
}

// TestMerge_Deterministic 验证同组合并两次 hash 相同（验收 4）。
func TestMerge_Deterministic(t *testing.T) {
	layers := []MergeLayer{
		groupLayer(1, "root", 1, "b: 2\na: 1\nnested:\n  z: 26\n  y: 25\n"),
		groupLayer(2, "child", 2, "c: 3\nnested:\n  x: 24\n"),
		instanceLayer(9, "srv", 1, "d: 4\n"),
	}
	first := mustMerge(t, model.ConfigFormatYAML, layers)
	second := mustMerge(t, model.ConfigFormatYAML, layers)
	require.Equal(t, first.EffectiveHash, second.EffectiveHash, "同组合并两次 hash 必须相同")
	require.Equal(t, first.Content, second.Content)

	// 键顺序不同的等价输入也必须得到同一 hash（字典序输出保证）。
	alt := []MergeLayer{
		groupLayer(1, "root", 1, "a: 1\nb: 2\nnested:\n  y: 25\n  z: 26\n"),
		groupLayer(2, "child", 2, "nested:\n  x: 24\nc: 3\n"),
		instanceLayer(9, "srv", 1, "d: 4\n"),
	}
	third := mustMerge(t, model.ConfigFormatYAML, alt)
	require.Equal(t, first.EffectiveHash, third.EffectiveHash, "键输入顺序不应影响结果 hash")
}

// TestMerge_DeterministicJSON 验证 json 输出同样确定。
func TestMerge_DeterministicJSON(t *testing.T) {
	layers := []MergeLayer{
		groupLayer(1, "root", 1, `{"b":2,"a":1}`),
		groupLayer(2, "child", 1, `{"c":3}`),
	}
	first := mustMerge(t, model.ConfigFormatJSON, layers)
	second := mustMerge(t, model.ConfigFormatJSON, layers)
	require.Equal(t, first.EffectiveHash, second.EffectiveHash)

	swapped := []MergeLayer{
		groupLayer(1, "root", 1, `{"a":1,"b":2}`),
		groupLayer(2, "child", 1, `{"c":3}`),
	}
	require.Equal(t, first.EffectiveHash, mustMerge(t, model.ConfigFormatJSON, swapped).EffectiveHash)
}

// TestMerge_NumberFormatPreserved 验证数值保持原格式（§5.3）。
func TestMerge_NumberFormatPreserved(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "ratio: 1.50\nexp: 1e3\nticks: 0x10\n"),
	})
	// 数值不重新格式化：1.50 不能变 1.5。
	require.Contains(t, res.Content, "1.50")
	require.Contains(t, res.Content, "1e3")
	require.Contains(t, res.Content, "0x10")
}

// TestMerge_TracedPointsToSource 验证 traced 能指出每个键来源层级与版本号（验收 5）。
func TestMerge_TracedPointsToSource(t *testing.T) {
	layers := []MergeLayer{
		groupLayer(1, "cluster", 1, "port: 25565\nmotd: from-cluster\n"),
		groupLayer(12, "region1", 3, "motd: from-region1\n"),
		instanceLayer(5, "srv-5", 2, "port: 25570\n"),
	}
	res := mustMerge(t, model.ConfigFormatYAML, layers)

	require.Equal(t, uint(12), res.Traced["motd"].Source.ID)
	require.Equal(t, "group", res.Traced["motd"].Source.Level)
	require.Equal(t, 3, res.Traced["motd"].Source.VersionNo)
	require.Equal(t, "region1", res.Traced["motd"].Source.Name)
	require.Equal(t, "from-region1", scalarNativeValueToString(res.Traced["motd"].Value))

	require.Equal(t, uint(5), res.Traced["port"].Source.ID)
	require.Equal(t, "instance", res.Traced["port"].Source.Level)
	require.Equal(t, 2, res.Traced["port"].Source.VersionNo)
}

// scalarNativeValueToString 是测试辅助：把 traced 的原生值转成字符串便于断言。
func scalarNativeValueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return strconvFormatInt(t)
	default:
		return ""
	}
}

// strconvFormatInt 小工具，避免测试文件引入 strconv 只为一次格式化。
func strconvFormatInt(v int64) string {
	b := make([]byte, 0, 20)
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// TestMerge_InstanceWithoutGroup 验证实例不属于任何分组时只应用 instance 层（验收 7）。
func TestMerge_InstanceWithoutGroup(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		instanceLayer(5, "srv-5", 1, "port: 25570\n"),
	})
	require.Contains(t, res.Content, "port: 25570")
	require.Len(t, res.Traced, 1)
	require.Equal(t, "instance", res.Traced["port"].Source.Level)
}

// TestMerge_EmptyLayerIgnored 验证空内容层视为空对象（§5.4）。
func TestMerge_EmptyLayerIgnored(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "port: 25565\n"),
		groupLayer(2, "empty", 1, ""),
		groupLayer(3, "blank", 1, "   \n\n"),
		instanceLayer(5, "srv", 1, ""),
	})
	require.Equal(t, map[string]TracedValue{
		"port": res.Traced["port"],
	}, res.Traced)
	require.Contains(t, res.Content, "25565")
	require.True(t, res.Valid)
	require.Empty(t, res.Warnings)
}

// TestMerge_SyntaxErrorLocatesLayer 验证语法错误能被定位到层与行号（验收 8）。
func TestMerge_SyntaxErrorLocatesLayer(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "port: 25565\n"),
		groupLayer(2, "broken", 4, "a: 1\n\tb: 2\n"),
		instanceLayer(5, "srv", 1, "motd: ok\n"),
	})

	require.False(t, res.Valid, "有层解析失败时结果必须标记为不可信")
	require.NotEmpty(t, res.Warnings)
	found := false
	for _, w := range res.Warnings {
		if w.Code != WarnLayerParseFailed {
			continue
		}
		found = true
		require.Equal(t, "error", w.Level)
		require.Equal(t, uint(2), w.Source.ID, "必须指出是哪一层出错")
		require.Equal(t, 4, w.Source.VersionNo)
		require.Contains(t, w.Source.Name, "broken")
		// yaml.v3 的错误信息形如 "yaml: line 2: ..."，必须带行号。
		require.Contains(t, w.Message, "line", "解析错误必须包含行号")
	}
	require.True(t, found, "必须产生 LAYER_PARSE_FAILED 告警")
	// 出错的层被跳过，其它层仍然合并。
	require.Contains(t, res.Content, "25565")
	require.Contains(t, res.Content, "motd")
}

// TestMerge_TypeConflictWarns 验证中间层类型冲突有告警而非静默（验收 9）。
func TestMerge_TypeConflictWarns(t *testing.T) {
	// 低层 profile 是标量，高层 profile 是对象。
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "profile: simple\n"),
		groupLayer(2, "child", 1, "profile:\n  level: 2\n"),
	})
	require.True(t, res.Valid, "类型冲突不应使结果不可信")
	require.NotEmpty(t, res.Warnings)
	found := false
	for _, w := range res.Warnings {
		if w.Code == WarnTypeConflict && w.Key == "profile" {
			found = true
			require.Equal(t, "warning", w.Level)
			require.Equal(t, uint(2), w.Source.ID)
		}
	}
	require.True(t, found, "类型冲突必须产生 TYPE_CONFLICT 告警")
	// 高层覆盖低层。
	require.Contains(t, res.Content, "level: 2")
	require.NotContains(t, res.Content, "profile: simple")
}

// TestMerge_TypeConflictReverse 验证「低层对象被高层标量覆盖」也有告警。
func TestMerge_TypeConflictReverse(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "profile:\n  level: 1\n"),
		groupLayer(2, "child", 1, "profile: flat\n"),
	})
	require.NotEmpty(t, res.Warnings)
	found := false
	for _, w := range res.Warnings {
		if w.Code == WarnTypeConflict && w.Key == "profile" {
			found = true
		}
	}
	require.True(t, found, "反向类型冲突也必须告警")
	require.Contains(t, res.Content, "profile: flat")
	require.NotContains(t, res.Content, "level: 1")
}

// TestMerge_RootNotObjectWarns 验证根节点非对象时整层忽略并告警（不静默）。
func TestMerge_RootNotObjectWarns(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "- a\n- b\n"),
		groupLayer(2, "child", 1, "port: 1\n"),
	})
	require.NotEmpty(t, res.Warnings)
	found := false
	for _, w := range res.Warnings {
		if w.Code == WarnRootNotObject {
			found = true
			require.Equal(t, uint(1), w.Source.ID)
		}
	}
	require.True(t, found, "根非对象必须告警")
	require.Contains(t, res.Content, "port")
}

// TestMerge_DeepNestedOverride 验证深层嵌套覆盖与 traced 路径。
func TestMerge_DeepNestedOverride(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "proxies:\n  velocity:\n    enabled: false\n    online-mode: true\n"),
		groupLayer(2, "child", 1, "proxies:\n  velocity:\n    enabled: true\n"),
	})
	require.Contains(t, res.Content, "enabled: true")
	require.Contains(t, res.Content, "online-mode: true", "未覆盖的兄弟键必须保留")

	require.Equal(t, uint(2), res.Traced["proxies.velocity.enabled"].Source.ID)
	require.Equal(t, uint(1), res.Traced["proxies.velocity.online-mode"].Source.ID)
}

// TestMerge_NullDeleteThenReAdd 验证「删掉又被高层重新赋值」的最终状态与溯源。
func TestMerge_NullDeleteThenReAdd(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "debug: true\n"),
		groupLayer(2, "mid", 1, "debug: null\n"),
		groupLayer(3, "leaf", 1, "debug: false\n"),
	})
	require.Contains(t, res.Content, "debug: false")
	require.False(t, res.Traced["debug"].Deleted, "最终有值时不应标记 deleted")
	require.Equal(t, uint(3), res.Traced["debug"].Source.ID)
}

// TestMerge_PropertiesDeterministic 验证 properties 合并的确定性（首次出现顺序 + 覆盖）。
func TestMerge_PropertiesDeterministic(t *testing.T) {
	layers := []MergeLayer{
		groupLayer(1, "root", 1, "a=1\nb=2\n"),
		groupLayer(2, "child", 1, "b=20\nc=3\n"),
	}
	first := mustMerge(t, model.ConfigFormatProperties, layers)
	second := mustMerge(t, model.ConfigFormatProperties, layers)
	require.Equal(t, first.EffectiveHash, second.EffectiveHash)
	// 键顺序按首次出现：a, b, c。
	require.Equal(t, "a=1\nb=20\nc=3\n", first.Content)
}

// TestMerge_UnsupportedFormat 验证不支持的格式报错而非静默返回空。
func TestMerge_UnsupportedFormat(t *testing.T) {
	_, err := MergeConfigLayers(model.ConfigFileFormat("toml"), []MergeLayer{
		groupLayer(1, "root", 1, "a = 1\n"),
	})
	require.Error(t, err, "不支持的格式必须报错")
}

// TestMerge_NoLayers 验证空链（无任何层配置）不 panic 且输出空结构。
func TestMerge_NoLayers(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, nil)
	require.True(t, res.Valid)
	require.Empty(t, res.Traced)
	require.NotEmpty(t, res.EffectiveHash, "空内容也必须给出 hash")
}

// ---------------------------------------------------------------------------
// DB 侧：作用域链解析与预览
// ---------------------------------------------------------------------------

// newConfigLayerTestDB 构造一个仅在内存中的测试库，迁移配置分层相关表与分组树表。
func newConfigLayerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.ConfigFile{},
		&model.ConfigLayerVersion{},
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
		&model.Instance{},
		&model.Node{},
	))
	return db
}

// seedInstance 造一个实例。
func seedLayerInstance(t *testing.T, db *gorm.DB, name string) model.Instance {
	t.Helper()
	inst := model.Instance{Name: name, UUID: "uuid-" + name, NodeID: 1, WorkDir: "/tmp/" + name}
	require.NoError(t, db.Create(&inst).Error)
	return inst
}

// seedGroup 造一个分组（parentID=nil 为根）。
func seedLayerGroup(t *testing.T, db *gorm.DB, name string, parentID *uint) model.InstanceGroupNode {
	t.Helper()
	g := model.InstanceGroupNode{Name: name, ParentID: parentID}
	require.NoError(t, db.Create(&g).Error)
	return g
}

// TestResolveScopeChain_DeepestPath 验证从实例向上遍历分组树得到正确的作用域链。
func TestResolveScopeChain_DeepestPath(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "srv")
	root := seedLayerGroup(t, db, "cluster", nil)
	region := seedLayerGroup(t, db, "region", &root.ID)
	zone := seedLayerGroup(t, db, "zone", &region.ID)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: zone.ID, InstanceID: inst.ID}).Error)

	svc := NewConfigFileService(db)
	sc, err := svc.resolveScopeChain(inst.ID)
	require.NoError(t, err)
	require.Len(t, sc.Chain, 4, "三级分组 + 实例层")
	require.Equal(t, uint(root.ID), sc.Chain[0].ID)
	require.Equal(t, "group", sc.Chain[0].Level)
	require.Equal(t, uint(region.ID), sc.Chain[1].ID)
	require.Equal(t, uint(zone.ID), sc.Chain[2].ID)
	require.Equal(t, uint(inst.ID), sc.Chain[3].ID)
	require.Equal(t, "instance", sc.Chain[3].Level)
}

// TestResolveScopeChain_NoGroup 验证实例不属于任何分组时链里只有实例层（验收 7）。
func TestResolveScopeChain_NoGroup(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "lonely")
	svc := NewConfigFileService(db)
	sc, err := svc.resolveScopeChain(inst.ID)
	require.NoError(t, err)
	require.Len(t, sc.Chain, 1)
	require.Equal(t, "instance", sc.Chain[0].Level)
	require.Equal(t, uint(inst.ID), sc.Chain[0].ID)
}

// TestResolveScopeChain_MultiGroupPicksDeepest 验证属于多个分组时取最深一条，且同深度确定。
func TestResolveScopeChain_MultiGroupPicksDeepest(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "multi")
	shallow := seedLayerGroup(t, db, "shallow", nil)
	deepRoot := seedLayerGroup(t, db, "deep-root", nil)
	deep := seedLayerGroup(t, db, "deep", &deepRoot.ID)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: shallow.ID, InstanceID: inst.ID}).Error)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: deep.ID, InstanceID: inst.ID}).Error)

	svc := NewConfigFileService(db)
	sc, err := svc.resolveScopeChain(inst.ID)
	require.NoError(t, err)
	// 取最深的 deep 链（deep-root → deep），而不是 shallow。
	require.Len(t, sc.Chain, 3)
	require.Equal(t, uint(deepRoot.ID), sc.Chain[0].ID)
	require.Equal(t, uint(deep.ID), sc.Chain[1].ID)
}

// TestResolveScopeChain_CycleDefense 验证脏数据成环时不死循环。
func TestResolveScopeChain_CycleDefense(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "cyc")
	a := seedLayerGroup(t, db, "a", nil)
	b := seedLayerGroup(t, db, "b", &a.ID)
	// 人为制造环：a.parent = b。
	require.NoError(t, db.Model(&model.InstanceGroupNode{}).Where("id = ?", a.ID).Update("parent_id", b.ID).Error)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: b.ID, InstanceID: inst.ID}).Error)

	svc := NewConfigFileService(db)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.resolveScopeChain(inst.ID)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("存在环时解析作用域链不得死循环")
	}
}

// TestEffectivePreview_EndToEnd 验证预览：多层覆盖 + traced + hash。
func TestEffectivePreview_EndToEnd(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "srv")
	root := seedLayerGroup(t, db, "cluster", nil)
	region := seedLayerGroup(t, db, "region", &root.ID)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: region.ID, InstanceID: inst.ID}).Error)

	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{Name: "server.properties", Format: "properties"}, 1)
	require.NoError(t, err)

	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "group", ScopeID: root.ID, Content: "server-port=25565\nmotd=cluster\n"}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "group", ScopeID: region.ID, Content: "motd=region\n"}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: "server-port=25570\n"}, 1)
	require.NoError(t, err)

	preview, err := svc.Effective(file.ID, inst.ID)
	require.NoError(t, err)
	require.Contains(t, preview.Content, "server-port=25570")
	require.Contains(t, preview.Content, "motd=region")
	require.Equal(t, uint(inst.ID), preview.Traced["server-port"].Source.ID)
	require.Equal(t, uint(region.ID), preview.Traced["motd"].Source.ID)
	require.NotEmpty(t, preview.EffectiveHash)
	// 同一输入两次预览 hash 必须相同。
	again, err := svc.Effective(file.ID, inst.ID)
	require.NoError(t, err)
	require.Equal(t, preview.EffectiveHash, again.EffectiveHash)
}

// TestEffectivePreview_SchemaFailureReported 验证预览会报告合并结果不满足 schema。
func TestEffectivePreview_SchemaFailureReported(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "srv")

	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{
		Name:       "app.json",
		Format:     "json",
		SchemaJSON: `{"type":"object","properties":{"port":{"type":"integer","minimum":25565,"maximum":25599}},"required":["port"]}`,
	}, 1)
	require.NoError(t, err)
	// 分别保存时该层原文合法（port 在范围内），但实例层把 port 设成超范围值。
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: `{"port":1}`}, 1)
	require.Error(t, err, "保存层时就应拒绝超范围值")

	// 用合法值保存，然后改 schema 使其不再满足（模拟 schema 收紧后的存量数据）。
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: `{"port":25570}`}, 1)
	require.NoError(t, err)
	_, err = svc.Update(file.ID, ConfigFileUpdateInput{
		Name: "app.json", Format: "json", Description: strPtr("d"),
		SchemaJSON: strPtr(`{"type":"object","properties":{"port":{"type":"integer","maximum":100}}}`),
	}, nil)
	require.NoError(t, err)

	preview, err := svc.Effective(file.ID, inst.ID)
	require.NoError(t, err)
	require.NotEmpty(t, preview.SchemaIssues, "预览必须报告不满足 schema 的问题")
	require.Equal(t, "port", preview.SchemaIssues[0].Path)
}

// TestSensitiveMask 验证敏感键在预览中脱敏，但 hash 仍基于明文（§8）。
func TestSensitiveMask(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "srv")
	svc := NewConfigFileService(db)
	paths := []string{"token"}
	file, err := svc.Create(ConfigFileInput{Name: "auth.properties", Format: "properties"}, 1)
	require.NoError(t, err)
	_, err = svc.Update(file.ID, ConfigFileUpdateInput{Name: "auth.properties", Format: "properties"}, &paths)
	require.NoError(t, err)
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: "token=secret123\nport=1\n"}, 1)
	require.NoError(t, err)

	preview, err := svc.Effective(file.ID, inst.ID)
	require.NoError(t, err)
	require.NotContains(t, preview.Content, "secret123", "敏感值不得出现在预览内容里")
	require.Contains(t, preview.Content, "token=***")
	require.Equal(t, "***", preview.Traced["token"].Value)

	// 下发用的内容必须是明文（hash 也基于明文）。
	content, hash, err := svc.EffectiveContent(file.ID, inst.ID)
	require.NoError(t, err)
	require.Contains(t, content, "secret123", "下发内容必须是明文")
	require.Equal(t, hashContent(content), hash)
}

// TestSaveLayer_RejectsUnknownScope 验证悬空作用域引用被拒绝（fail-closed）。
func TestSaveLayer_RejectsUnknownScope(t *testing.T) {
	db := newConfigLayerTestDB(t)
	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{Name: "x.yml", Format: "yaml"}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "group", ScopeID: 9999, Content: "a: 1\n"}, 1)
	require.ErrorIs(t, err, ErrConfigScopeRefNotFound)
}

// TestLayerVersionChain_AppendOnly 验证版本链只追加，回退也是追加。
func TestLayerVersionChain_AppendOnly(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "srv")
	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{Name: "c.yml", Format: "yaml"}, 1)
	require.NoError(t, err)

	v1, err := svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: "a: 1\n"}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, v1.VersionNo)

	v2, err := svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: "a: 2\n"}, 1)
	require.NoError(t, err)
	require.Equal(t, 2, v2.VersionNo)

	// 回退到 v1：必须生成 v3，且 v1/v2 的内容不被改写。
	v3, err := svc.RollbackLayer(file.ID, model.ConfigScopeInstance, inst.ID, v1.ID, 1, "")
	require.NoError(t, err)
	require.Equal(t, 3, v3.VersionNo)
	require.Equal(t, v1.Content, v3.Content)
	require.NotNil(t, v3.BasedOnVersionID)
	require.Equal(t, v1.ID, *v3.BasedOnVersionID)

	history, err := svc.LayerHistory(file.ID, model.ConfigScopeInstance, inst.ID)
	require.NoError(t, err)
	require.Len(t, history, 3, "历史必须完整保留")
	require.Equal(t, 3, history[0].VersionNo, "按版本号倒序")
	require.Equal(t, "a: 2\n", history[1].Content)
	require.Equal(t, "a: 1\n", history[2].Content)
}

// TestCreate_RejectsDuplicateName 验证 name 全局唯一（含软删）。
func TestCreate_RejectsDuplicateName(t *testing.T) {
	db := newConfigLayerTestDB(t)
	svc := NewConfigFileService(db)
	first, err := svc.Create(ConfigFileInput{Name: "dup.yml", Format: "yaml"}, 1)
	require.NoError(t, err)
	_, err = svc.Create(ConfigFileInput{Name: "dup.yml", Format: "yaml"}, 1)
	require.ErrorIs(t, err, ErrConfigFileExists)

	require.NoError(t, svc.Delete(first.ID))
	_, err = svc.Create(ConfigFileInput{Name: "dup.yml", Format: "yaml"}, 1)
	require.ErrorIs(t, err, ErrConfigFileExists, "软删的记录仍占用名字，避免与历史版本混淆")
}

// TestUpdate_FormatImmutable 验证 format 创建后不可改。
func TestUpdate_FormatImmutable(t *testing.T) {
	db := newConfigLayerTestDB(t)
	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{Name: "f.yml", Format: "yaml"}, 1)
	require.NoError(t, err)
	_, err = svc.Update(file.ID, ConfigFileUpdateInput{Name: "f.yml", Format: "json"}, nil)
	require.ErrorIs(t, err, ErrConfigFormatImmutable)
}

// TestDeleteLayer_RemovesAllVersions 验证删除某作用域全部版本。
func TestDeleteLayer_RemovesAllVersions(t *testing.T) {
	db := newConfigLayerTestDB(t)
	inst := seedLayerInstance(t, db, "srv")
	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{Name: "d.yml", Format: "yaml"}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: "a: 1\n"}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(file.ID, SaveLayerInput{ScopeLevel: "instance", ScopeID: inst.ID, Content: "a: 2\n"}, 1)
	require.NoError(t, err)

	require.NoError(t, svc.DeleteLayer(file.ID, model.ConfigScopeInstance, inst.ID))
	_, err = svc.LayerHistory(file.ID, model.ConfigScopeInstance, inst.ID)
	require.ErrorIs(t, err, ErrConfigLayerNotFound)

	// 再删一次应报未找到而不是静默成功。
	require.ErrorIs(t, svc.DeleteLayer(file.ID, model.ConfigScopeInstance, inst.ID), ErrConfigLayerNotFound)
}

// TestMerge_JSONSyntaxErrorHasLine 验证 JSON 语法错误也能定位到行号（验收 8）。
//
// encoding/json 只给字节 Offset，这里断言已换算成行号——否则运维看到
// "invalid character 'b'" 无法判断是哪个配置文件哪一行。
func TestMerge_JSONSyntaxErrorHasLine(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatJSON, []MergeLayer{
		groupLayer(1, "root", 1, "{\n  \"a\": 1,\n  \"b\": bad\n}"),
	})
	require.False(t, res.Valid)
	require.NotEmpty(t, res.Warnings)
	require.Equal(t, WarnLayerParseFailed, res.Warnings[0].Code)
	require.Contains(t, res.Warnings[0].Message, "第 3 行", "必须换算成行号，而不是只给字节偏移")
}

// TestMerge_YAMLSyntaxErrorHasLine 验证 YAML 语法错误带行号。
func TestMerge_YAMLSyntaxErrorHasLine(t *testing.T) {
	res := mustMerge(t, model.ConfigFormatYAML, []MergeLayer{
		groupLayer(1, "root", 1, "a: 1\nb: : :\n"),
	})
	require.False(t, res.Valid)
	require.NotEmpty(t, res.Warnings)
	require.Contains(t, res.Warnings[0].Message, "line", "yaml.v3 的错误自带 line 信息，必须透传")
}

// TestEffectivePreview_Performance 验证规模要求：1000 实例、50 配置文件时单实例预览 < 50ms（验收 10）。
//
// 真实瓶颈是 DB 查询次数：作用域链解析读全量分组树（1 次）+ 成员关系（1 次）+ 每层 head（N 次）。
// 这里造 1000 实例 / 50 文件 / 5 层的作用域链，断言单次预览耗时在预算内。
func TestEffectivePreview_Performance(t *testing.T) {
	db := newConfigLayerTestDB(t)
	node := model.Node{Name: "perf-node", UUID: "uuid-perf-node", Host: "127.0.0.1", GRPCPort: 9090}
	require.NoError(t, db.Create(&node).Error)

	// 5 层分组树：root → l1 → l2 → l3 → l4。
	root := seedLayerGroup(t, db, "perf-root", nil)
	l1 := seedLayerGroup(t, db, "perf-l1", &root.ID)
	l2 := seedLayerGroup(t, db, "perf-l2", &l1.ID)
	l3 := seedLayerGroup(t, db, "perf-l3", &l2.ID)
	l4 := seedLayerGroup(t, db, "perf-l4", &l3.ID)
	chainGroups := []uint{root.ID, l1.ID, l2.ID, l3.ID, l4.ID}

	// 1000 个实例，都挂在最深分组下。
	insts := make([]model.Instance, 0, 1000)
	for i := 0; i < 1000; i++ {
		insts = append(insts, model.Instance{
			Name: fmt.Sprintf("perf-%d", i), UUID: fmt.Sprintf("uuid-perf-%d", i),
			NodeID: node.ID, WorkDir: "/tmp/perf",
		})
	}
	require.NoError(t, db.CreateInBatches(&insts, 200).Error)
	members := make([]model.InstanceGroupMember, 0, len(insts))
	for _, in := range insts {
		members = append(members, model.InstanceGroupMember{GroupID: l4.ID, InstanceID: in.ID})
	}
	require.NoError(t, db.CreateInBatches(&members, 200).Error)

	svc := NewConfigFileService(db)
	// 50 个配置文件，每个在 5 个分组层各有一份版本。
	files := make([]model.ConfigFile, 0, 50)
	for i := 0; i < 50; i++ {
		files = append(files, model.ConfigFile{
			Name: fmt.Sprintf("perf/config-%d.yml", i), Format: model.ConfigFormatYAML, CreatedBy: 1,
		})
	}
	require.NoError(t, db.CreateInBatches(&files, 50).Error)
	layers := make([]model.ConfigLayerVersion, 0, 250)
	for _, f := range files {
		for gi, gid := range chainGroups {
			layers = append(layers, model.ConfigLayerVersion{
				ConfigFileID: f.ID, ScopeLevel: model.ConfigScopeGroup, ScopeRefID: gid,
				VersionNo: 1, Content: fmt.Sprintf("layer%d: %d\n", gi, gi), ContentHash: hashContent("x"),
			})
		}
	}
	require.NoError(t, db.CreateInBatches(layers, 100).Error)

	// 预热一次（首查会建连接池）。
	_, err := svc.Effective(files[0].ID, insts[0].ID)
	require.NoError(t, err)

	start := time.Now()
	const rounds = 20
	for i := 0; i < rounds; i++ {
		_, err := svc.Effective(files[i%len(files)].ID, insts[i%len(insts)].ID)
		require.NoError(t, err)
	}
	elapsed := time.Since(start) / rounds
	require.Less(t, elapsed, 50*time.Millisecond, "单实例预览应 < 50ms，实测 %v", elapsed)
}

// TestConfigFileUpdate_CanClearFields 验证 description / schemaJson 可被显式清空。
//
// 回归背景（前端接入时发现的后端缺陷）：旧实现用 `if in.Description != ""` 判断是否更新，
// 于是「传空串」与「不传」无法区分——用户在前端清空描述，接口返回 200，但值没变，
// 表现为「清不空」。改用指针表达三态：nil=不改、&""=清空、&"x"=改成 x。
func TestConfigFileUpdate_CanClearFields(t *testing.T) {
	db := newConfigLayerTestDB(t)
	svc := NewConfigFileService(db)

	file, err := svc.Create(ConfigFileInput{
		Name: "clear-me.yml", Format: "yaml",
		Description: "初始描述",
		SchemaJSON:  `{"type":"object"}`,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, "初始描述", file.Description)
	require.NotEmpty(t, file.SchemaJSON)

	// 1) 只传空串 → 两个字段都被清空。
	updated, err := svc.Update(file.ID, ConfigFileUpdateInput{
		Description: strPtr(""),
		SchemaJSON:  strPtr(""),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", updated.Description, "描述应被清空")
	assert.Equal(t, "", updated.SchemaJSON, "schema 应被清空")

	// 2) 两个字段都不传（nil）→ 保持清空后的状态，不应报错也不应被"恢复"。
	again, err := svc.Update(file.ID, ConfigFileUpdateInput{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", again.Description, "nil 表示不改，应保持空")
	assert.Equal(t, "", again.SchemaJSON, "nil 表示不改，应保持空")

	// 3) 传非空 → 正常改成新值。
	filled, err := svc.Update(file.ID, ConfigFileUpdateInput{
		Description: strPtr("新描述"),
		SchemaJSON:  strPtr(`{"type":"object","required":["a"]}`),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "新描述", filled.Description)
	assert.Contains(t, filled.SchemaJSON, "required")
}

// TestConfigFileUpdate_ClearSchemaSkipsValidation 清空 schema 不应走合法性校验。
//
// 语义：schema 为空 = 不做 schema 校验；把它清空却报「schema 非法」自相矛盾。
func TestConfigFileUpdate_ClearSchemaSkipsValidation(t *testing.T) {
	db := newConfigLayerTestDB(t)
	svc := NewConfigFileService(db)
	file, err := svc.Create(ConfigFileInput{Name: "no-schema.yml", Format: "yaml"}, 1)
	require.NoError(t, err)

	_, err = svc.Update(file.ID, ConfigFileUpdateInput{SchemaJSON: strPtr("")}, nil)
	require.NoError(t, err, "清空 schema 不该触发校验失败")

	// 但传非法的非空 schema 仍必须被拒。
	_, err = svc.Update(file.ID, ConfigFileUpdateInput{SchemaJSON: strPtr("{not-json")}, nil)
	require.Error(t, err, "非法 schema 仍应被拒绝")
}
