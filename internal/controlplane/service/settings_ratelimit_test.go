package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/trafficlimit"
)

// newRateLimitSettingsSvc 建一个最小可用的设置服务（内存库 + 可识别基线）。
func newRateLimitSettingsSvc(t *testing.T) *SettingsService {
	t.Helper()
	return NewSettingsService(newSettingsTestDB(t), testConfig())
}

// TestRateLimitSettings_DefaultsAndOverride 默认值来自 trafficlimit，DB 覆盖优先。
func TestRateLimitSettings_DefaultsAndOverride(t *testing.T) {
	svc := newRateLimitSettingsSvc(t)

	got := svc.RateLimitSettings()
	require.Equal(t, trafficlimit.Defaults(), got, "无覆盖时应取出厂默认")

	require.NoError(t, svc.Update(map[string]string{
		SettingKeyRateLimitReadRate:     "120",
		SettingKeyRateLimitReadCapacity: "160",
	}))
	got = svc.RateLimitSettings()
	require.Equal(t, 120, got.Read.RatePerSecond)
	require.Equal(t, 160, got.Read.Capacity)
	// 其余组不受影响。
	require.Equal(t, trafficlimit.Defaults().Write, got.Write)
}

// TestRateLimitSettings_PairValidation 速率与容量必须配套；全局组不得置 0。
func TestRateLimitSettings_PairValidation(t *testing.T) {
	svc := newRateLimitSettingsSvc(t)

	cases := []struct {
		name   string
		values map[string]string
	}{
		{"只改速率为 0", map[string]string{SettingKeyRateLimitReadRate: "0"}},
		{"只改容量为 0", map[string]string{SettingKeyRateLimitWriteCapacity: "0"}},
		{"全局速率为 0", map[string]string{SettingKeyRateLimitGlobalRate: "0"}},
		{"全局容量为 0", map[string]string{SettingKeyRateLimitGlobalCap: "0"}},
		{"负数", map[string]string{SettingKeyRateLimitReadRate: "-1"}},
		{"非整数", map[string]string{SettingKeyRateLimitReadRate: "1.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, svc.Update(tc.values), "应拒绝非法限流配置")
		})
	}

	// 同时为 0（分类不限流）是合法的；全局始终不允许。
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyRateLimitReadRate:     "0",
		SettingKeyRateLimitReadCapacity: "0",
	}))
	require.True(t, svc.RateLimitSettings().Read.Unlimited(), "只读预算应被显式关闭")
}

// TestRateLimitSettings_ApplierInvoked 保存后回调热更新。
func TestRateLimitSettings_ApplierInvoked(t *testing.T) {
	svc := newRateLimitSettingsSvc(t)

	var applied []trafficlimit.Settings
	svc.SetRateLimitApplier(func(s trafficlimit.Settings) { applied = append(applied, s) })

	require.NoError(t, svc.Update(map[string]string{SettingKeyRateLimitWriteRate: "50"}))
	require.Len(t, applied, 1, "保存限流键应触发一次热更新回调")
	require.Equal(t, 50, applied[0].Write.RatePerSecond)
}

// TestRateLimitKeysWritableAndCategorized 八个键必须可写且展示在可编辑区。
func TestRateLimitKeysWritableAndCategorized(t *testing.T) {
	svc := newRateLimitSettingsSvc(t)

	view, err := svc.Get()
	require.NoError(t, err)
	for _, key := range []string{
		SettingKeyRateLimitReadRate, SettingKeyRateLimitReadCapacity,
		SettingKeyRateLimitWriteRate, SettingKeyRateLimitWriteCapacity,
		SettingKeyRateLimitLogRate, SettingKeyRateLimitLogCapacity,
		SettingKeyRateLimitGlobalRate, SettingKeyRateLimitGlobalCap,
	} {
		item, ok := findItem(view.Editable, key)
		require.True(t, ok, "%s 应出现在可编辑区", key)
		require.True(t, item.Editable)
		require.True(t, item.EffectiveImmediately, "%s 保存后应即时生效", key)
		require.NotEmpty(t, item.Value)
	}
}

// TestRateLimitSettings_ConcurrentUpdatesRemainValid 验证配对校验、事务落库与热应用不会跨写混读。
func TestRateLimitSettings_ConcurrentUpdatesRemainValid(t *testing.T) {
	svc := newRateLimitSettingsSvc(t)

	var mu sync.Mutex
	var applied []trafficlimit.Settings
	svc.SetRateLimitApplier(func(settings trafficlimit.Settings) {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, settings)
	})

	const rounds = 200
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = svc.Update(map[string]string{
				SettingKeyRateLimitReadRate:     "0",
				SettingKeyRateLimitReadCapacity: "0",
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = svc.Update(map[string]string{SettingKeyRateLimitReadRate: "2"})
		}()
	}
	close(start)
	wg.Wait()

	got := svc.RateLimitSettings()
	require.NoError(t, got.Validate(), "并发保存后整组预算必须仍然满足配对约束")
	mu.Lock()
	defer mu.Unlock()
	for _, settings := range applied {
		require.NoError(t, settings.Validate(), "热应用回调不能看到跨写混合预算")
	}
}
