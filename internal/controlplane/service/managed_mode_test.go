package service

import (
	"encoding/json"
	"strings"
	"testing"
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
