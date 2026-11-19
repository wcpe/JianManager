package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManagedIdentityShape(t *testing.T) {
	s := NewManagedModeService()
	txt, err := s.BuildIdentityFile("ws://127.0.0.1:9102/ws/plugin-bridge", "inst-uuid-1", "tok-abc")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("identity = %s", strings.TrimSpace(txt))
	var m map[string]any
	if err := json.Unmarshal([]byte(txt), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schemaVersion", "provider", "endpoint", "instanceId", "credential", "capabilities", "createdAt"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺字段 %s", k)
		}
	}
	if m["schemaVersion"].(float64) != 1 {
		t.Fatal("schemaVersion 必须是 1")
	}
}

func TestTierMirrorMatchesProbe(t *testing.T) {
	if n := len(managedConfigTiers); n != 66 {
		t.Fatalf("档位表应为 66 键，实际 %d", n)
	}
	cnt := map[ManagedConfigTier]int{}
	for _, v := range managedConfigTiers {
		cnt[v]++
	}
	if cnt[ConfigTierForbidden] != 18 || cnt[ConfigTierGuarded] != 45 || cnt[ConfigTierSafe] != 3 {
		t.Fatalf("分档数量不符: F=%d G=%d S=%d", cnt[ConfigTierForbidden], cnt[ConfigTierGuarded], cnt[ConfigTierSafe])
	}
}

func TestForbiddenRejected(t *testing.T) {
	v := ValidateConfigPush(ManagedConfigPush{Values: map[string]any{"metrics.port": 9945}})
	if v.OK() {
		t.Fatal("metrics.port 必须被拒")
	}
	t.Logf("拒绝原因: %s", v.Error())
	v2 := ValidateConfigPush(ManagedConfigPush{Values: map[string]any{"collect-period-ticks": 100}})
	if !v2.OK() {
		t.Fatalf("合法键不该被拒: %s", v2.Error())
	}
}

// TestForbidden_WholeRootSegmentRejected 守住「整组结构性禁止」这条语义。
//
// `bridge` 段下有 line/chunk/subscribe/health/reload 等大量子键，**逐键登记必然漏**，
// 故它是整组禁止（managedForbiddenRoots）。而漏掉的子键仍能改监听面与凭证行为。
//
// 这个用例存在的直接原因：项目里曾有一份本地副本只做逐键判定、不认禁止根段，
// 于是 `bridge.line.max-inbound-bytes` 这类未登记的深层子键被判成「可远程写」——
// 探针仍会拒（它有自己的 FORBIDDEN_ROOTS），但 CP 的预检就此失效，两边各说各话。
// 副本已删除，此处用测试锁住上游行为，避免同类分叉再被引入。
func TestForbidden_WholeRootSegmentRejected(t *testing.T) {
	// 这些子键**都没有**逐键登记档位，只能靠禁止根段命中。
	for _, key := range []string{
		"bridge",
		"bridge.line",
		"bridge.line.max-inbound-bytes",
		"bridge.chunk.size",
		"bridge.subscribe.depth",
		"bridge.health.interval",
		"bridge.reload.enabled",
	} {
		assert.True(t, IsForbiddenConfigPath(key),
			"bridge 是整组禁止的根段，其任意深层子键都必须判禁止（未登记不代表可写）: %s", key)

		v := ValidateConfigPush(ManagedConfigPush{Values: map[string]any{key: 1}})
		assert.False(t, v.OK(), "下发预检必须整包拒绝: %s", key)
	}

	// removals 侧同口径：删掉一个禁档路径与写入它同属越界。
	v := ValidateConfigPush(ManagedConfigPush{Removals: []string{"bridge.line.max-inbound-bytes"}})
	assert.False(t, v.OK(), "removals 指向禁止根段的深层子键也必须拒绝")

	// 非禁止根段的普通深层子键不受影响，避免把判定写成「无点在就禁止」这类过宽实现。
	assert.False(t, IsForbiddenConfigPath("aggregation.window"),
		"非禁止根段的合法深层键不得被误判为禁止")
}
