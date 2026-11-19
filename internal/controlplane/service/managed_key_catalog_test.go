package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManagedKeyCatalog_CoversEveryTierKey 验证元数据表与定档表**完全一致**。
//
// 两张表职责不同（一张管"能不能远程写"，一张管"界面上怎么编"），但键集合必须相同：
//   - 定档表有、元数据缺 → 界面上看不见，用户会以为"这个键不能配"；
//   - 元数据有、定档表缺 → 界面上能编辑，下发了却被拒，用户白填。
//
// 两者任一情况都是真实缺陷，故用双向比对钉住。
func TestManagedKeyCatalog_CoversEveryTierKey(t *testing.T) {
	tierKeys := map[string]bool{}
	for k := range managedConfigTiers {
		tierKeys[k] = true
	}
	for k := range managedReservedSafeKeys {
		tierKeys[k] = true
	}
	for k := range managedReservedGuardedKeys {
		tierKeys[k] = true
	}

	var missingInCatalog, extraInCatalog []string
	for k := range tierKeys {
		if _, ok := managedKeyCatalog[k]; !ok {
			missingInCatalog = append(missingInCatalog, k)
		}
	}
	for k := range managedKeyCatalog {
		if !tierKeys[k] {
			extraInCatalog = append(extraInCatalog, k)
		}
	}

	assert.Empty(t, missingInCatalog, "定档表有但元数据缺失（界面上会看不见这些键）")
	assert.Empty(t, extraInCatalog, "元数据有但定档表无（能编辑却会被拒绝）")
	assert.Equal(t, len(tierKeys), len(managedKeyCatalog), "两表键数必须相同")
}

// TestManagedKeyCatalog_EveryKeyIsWellFormed 验证每个键的元数据都能被前端直接用。
//
// 检查的是"渲染所需的最小信息"：分组、标题、说明、类型。缺任何一个，
// 前端只能显示一个裸键名或退回 JSON，等于没做可视化。
func TestManagedKeyCatalog_EveryKeyIsWellFormed(t *testing.T) {
	for key, meta := range managedKeyCatalog {
		t.Run(key, func(t *testing.T) {
			assert.NotEmpty(t, meta.Group, "缺少分组")
			assert.NotEmpty(t, meta.Title, "缺少中文标题")
			assert.NotEmpty(t, meta.Description, "缺少中文说明")
			assert.NotEmpty(t, meta.Kind, "缺少编辑类型")

			// 数值型必须给出边界或明确无界；bool/string 不应有 Min/Max。
			switch meta.Kind {
			case ManagedKeyBool, ManagedKeyString, ManagedKeyStringList, ManagedKeyObject:
				assert.Nil(t, meta.Min, "非数值类型不应有 Min")
				assert.Nil(t, meta.Max, "非数值类型不应有 Max")
			case ManagedKeyInt, ManagedKeyFloat:
				if meta.Min != nil && meta.Max != nil {
					assert.LessOrEqual(t, *meta.Min, *meta.Max, "Min 不应大于 Max")
				}
			default:
				t.Fatalf("未知的编辑类型: %s", meta.Kind)
			}

			// 枚举型必须真的给出候选值，否则前端会渲染成自由输入。
			if len(meta.Options) > 0 {
				assert.Contains(t, []ManagedKeyKind{ManagedKeyString, ManagedKeyInt},
					meta.Kind, "带可选值的键应是标量类型")
			}

			// 对象型必须声明子字段，否则前端无法渲染结构化编辑器。
			if meta.Kind == ManagedKeyObject {
				assert.NotEmpty(t, meta.Fields, "object 类型必须给出子字段结构")
			}
		})
	}
}

// TestManagedKeyCatalog_TierComesFromTierTable 验证档位取自定档表而非元数据自述。
//
// 档位是**安全属性**，只能有一个真源。元数据表若自己写档位，
// 两边漂移时就会出现"界面说能改、下发时被拒"（或更糟：界面说不能改、实际能改）。
func TestManagedKeyCatalog_TierComesFromTierTable(t *testing.T) {
	for key := range managedKeyCatalog {
		meta, ok := ManagedKeyMetaOf(key)
		require.True(t, ok, "元数据应可查到: %s", key)

		tier, known := ManagedConfigTierOf(key)
		if !known {
			assert.Equal(t, ConfigTierForbidden.WireName(), meta.Tier,
				"未收录定档表的键必须按禁止处理: %s", key)
			continue
		}
		assert.Equal(t, tier.WireName(), meta.Tier, "档位应与定档表一致: %s", key)
	}
}

// TestManagedKeyCatalog_ForbiddenKeysAreLocalGroup 验证禁止档键都归到"仅本地可改"分组。
//
// 界面按分组渲染。禁止键若混进普通分组，用户会以为它们能改，
// 点进去却只看到只读提示——分组本身就是第一道认知护栏。
func TestManagedKeyCatalog_ForbiddenKeysAreLocalGroup(t *testing.T) {
	for key := range managedConfigTiers {
		if !IsForbiddenConfigPath(key) {
			continue
		}
		meta, ok := ManagedKeyMetaOf(key)
		require.True(t, ok, key)
		assert.Equal(t, ManagedGroupLocal, meta.Group,
			"禁止档键应归入「仅本地可改」分组: %s", key)
	}
}

// TestManagedKeyCatalog_SortIsStable 验证目录排序稳定（前端直接渲染，不自己再排）。
func TestManagedKeyCatalog_SortIsStable(t *testing.T) {
	first := ManagedKeyCatalog()
	second := ManagedKeyCatalog()
	require.Equal(t, len(first), len(second))
	for i := range first {
		assert.Equal(t, first[i].Key, second[i].Key, "两次调用顺序应一致")
	}
	// 分组优先、组内按键名。
	for i := 1; i < len(first); i++ {
		if first[i-1].Group == first[i].Group {
			assert.LessOrEqual(t, first[i-1].Key, first[i].Key, "同组内按键名升序")
		} else {
			assert.Less(t, string(first[i-1].Group), string(first[i].Group), "分组升序")
		}
	}
}

// TestManagedKeyCatalog_AlertRulesHasRuleStructure 钉住 alert.rules 的规则字段集。
//
// 它是唯一需要专用编辑器的键：值是"规则名 → {threshold, sustain-cycles, level, enabled}"。
// 字段集若与探针 config.yml 漂移，规则编辑器会产出探针不认的结构。
func TestManagedKeyCatalog_AlertRulesHasRuleStructure(t *testing.T) {
	meta, ok := ManagedKeyMetaOf("alert.rules")
	require.True(t, ok)
	require.Equal(t, ManagedKeyObject, meta.Kind)

	fields := map[string]ManagedKeyMeta{}
	for _, f := range meta.Fields {
		fields[f.Key] = f
	}
	for _, want := range []string{"threshold", "sustain-cycles", "level", "enabled"} {
		assert.Contains(t, fields, want, "alert.rules 缺少子字段: %s", want)
	}
	assert.Equal(t, ManagedKeyFloat, fields["threshold"].Kind)
	assert.Equal(t, ManagedKeyInt, fields["sustain-cycles"].Kind)
	assert.Equal(t, []string{"WARN", "CRITICAL"}, fields["level"].Options, "级别只有两种取值")
	assert.Equal(t, ManagedKeyBool, fields["enabled"].Kind)
}

// TestManagedKeyCatalog_EveryGroupHasLabel 验证每个用到的分组都有中文名。
//
// 前端用 labels[group] 渲染分组标题。缺标签的表现是界面出现一个**没有标题的空组**——
// 用户看到一堆字段却不知道它们属于什么，且很难判断是前端 bug 还是配置问题。
func TestManagedKeyCatalog_EveryGroupHasLabel(t *testing.T) {
	labels := ManagedKeyGroupLabels()
	used := map[ManagedKeyGroup]bool{}
	for _, meta := range ManagedKeyCatalog() {
		used[meta.Group] = true
	}
	for group := range used {
		assert.NotEmpty(t, labels[group], "分组缺少中文名: %s", group)
	}
	// 反向：定义了标签却没有任何键使用，说明分组名写错或键漏登记。
	for group := range labels {
		assert.True(t, used[group], "分组定义了标签但没有键使用: %s", group)
	}
}

// TestManagedKeyCatalog_TierIsWireNameNotChinese 钉住接口字段 `tier` 必须是英文枚举。
//
// ## 回归的真实缺陷
// 早期元数据接口把 `ConfigTier.String()`（中文"禁止/危险/安全"）直接放进 JSON 的 `tier`。
// 前端按 `tier === 'forbidden'` 判断只读，条件永远不成立——于是**禁止档键渲染出了可编辑控件**：
// 用户在界面上改了 bridge.* 键，下发时才被探针拒，而界面从未提示它不可远程写。
//
// 这类缺陷在单元测试与接口层都看不出来（值确实传了、也确实非空），
// 只有把"字段取值必须是什么"写死才拦得住。
func TestManagedKeyCatalog_TierIsWireNameNotChinese(t *testing.T) {
	allowed := map[string]bool{
		ConfigTierForbidden.WireName(): true,
		ConfigTierGuarded.WireName():   true,
		ConfigTierSafe.WireName():      true,
	}
	for _, meta := range ManagedKeyCatalog() {
		assert.True(t, allowed[meta.Tier],
			"tier 必须是英文枚举（forbidden/guarded/safe），实际=%q，键=%s", meta.Tier, meta.Key)
	}

	// 禁止档必须能被前端用 'forbidden' 精确命中——这是只读渲染的唯一依据。
	for key := range managedConfigTiers {
		if !IsForbiddenConfigPath(key) {
			continue
		}
		meta, ok := ManagedKeyMetaOf(key)
		require.True(t, ok, key)
		assert.Equal(t, "forbidden", meta.Tier, "禁止档键的 tier 必须是 forbidden: %s", key)
	}
}
