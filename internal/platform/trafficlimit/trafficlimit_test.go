package trafficlimit

import (
	"testing"
)

// TestDefaultsAreValid 默认预算必须自洽（否则未配置的部署启动即异常）。
func TestDefaultsAreValid(t *testing.T) {
	d := Defaults()
	if err := d.Validate(); err != nil {
		t.Fatalf("默认预算应合法: %v", err)
	}
	if d.Global.Unlimited() {
		t.Fatal("全局桶不得为不限流")
	}
	// 分类之和不得超过全局（否则全局桶毫无意义，实际放行量由分类决定）。
	sum := d.Read.RatePerSecond + d.Write.RatePerSecond + d.LogQuery.RatePerSecond
	if sum > d.Global.RatePerSecond {
		t.Fatalf("分类速率之和 %d 超过全局 %d", sum, d.Global.RatePerSecond)
	}
	if d.Read.RatePerSecond <= d.Write.RatePerSecond {
		t.Fatal("只读预算应高于写操作预算（管理台读多写少）")
	}
}

// TestSpecValidate 覆盖非法组合与「显式不限流」的边界。
func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"正数合法", Spec{RatePerSecond: 10, Capacity: 20}, false},
		{"同时为零=不限流", Spec{}, false},
		{"只有速率为零", Spec{RatePerSecond: 0, Capacity: 20}, true},
		{"只有容量为零", Spec{RatePerSecond: 10, Capacity: 0}, true},
		{"负数", Spec{RatePerSecond: -1, Capacity: 20}, true},
		{"速率超界", Spec{RatePerSecond: MaxRatePerSecond + 1, Capacity: 20}, true},
		{"容量超界", Spec{RatePerSecond: 10, Capacity: MaxCapacity + 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate("测试组")
			if tc.wantErr && err == nil {
				t.Fatal("应判为非法")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("应判为合法: %v", err)
			}
		})
	}
}

// TestNormalizeFallsBackOnInvalid 脏值（历史落库/并发写坏）回落默认，不能让入口变成全拒或全放。
func TestNormalizeFallsBackOnInvalid(t *testing.T) {
	d := Defaults()
	got := Settings{
		Read:     Spec{RatePerSecond: 5, Capacity: 0},  // 配对非法 ⇒ 回落默认
		Write:    Spec{},                               // 显式不限流 ⇒ 保留
		LogQuery: Spec{RatePerSecond: -3, Capacity: 9}, // 负数 ⇒ 回落默认
		Global:   Spec{},                               // 不限流 ⇒ 全局必须回落默认
	}.Normalize()

	if got.Read != d.Read {
		t.Fatalf("非法只读预算应回落默认: %+v", got.Read)
	}
	if !got.Write.Unlimited() {
		t.Fatalf("显式关闭的写预算应保留: %+v", got.Write)
	}
	if got.LogQuery != d.LogQuery {
		t.Fatalf("负数预算应回落默认: %+v", got.LogQuery)
	}
	if got.Global != d.Global {
		t.Fatalf("全局桶不得被关掉，应回落默认: %+v", got.Global)
	}
}

// TestSpecForClassMapping 守住「分类 → 预算」的映射：写操作不得吃到只读预算。
func TestSpecForClassMapping(t *testing.T) {
	s := Settings{
		Read:     Spec{RatePerSecond: 60, Capacity: 80},
		Write:    Spec{RatePerSecond: 10, Capacity: 20},
		LogQuery: Spec{RatePerSecond: 7, Capacity: 9},
		Global:   Spec{RatePerSecond: 90, Capacity: 160},
	}
	if got := SpecForClass(s, ClassRead); got != s.Read {
		t.Fatalf("read 分类映射错: %+v", got)
	}
	if got := SpecForClass(s, ClassWrite); got != s.Write {
		t.Fatalf("写操作必须使用写预算（曾被只读预算覆盖过，是真实缺陷）: %+v", got)
	}
	if got := SpecForClass(s, ClassLogQuery); got != s.LogQuery {
		t.Fatalf("日志查询分类映射错: %+v", got)
	}
	if got := SpecForClass(s, "未知分类"); got != s.Write {
		t.Fatalf("未知分类应保守归入写预算: %+v", got)
	}
}

// TestBucketShardingConcurrent 分片后不同 key 并发互不阻塞，且各自判定仍严格等于容量。
//
// 这是「超高并发」改造的核心回归：旧实现一把全局锁，所有 IP 排队；
// 分片后 64 个不同 key 应能并行推进，且每个 key 的放行次数仍严格等于容量。
// 限流器本体属于 middleware 包，故本用例在那一侧覆盖，此处只验纯数据层。
