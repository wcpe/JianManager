package heartbeat

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-503 心跳下发日志采集配置：存在标记（optional）语义 + 变化才应用 + 失败重试。

// presentResp 造一个「CP 已下发」的响应（存在标记非 nil 是全部字段的生效前提）。
func presentResp() *workerpb.HeartbeatResponse {
	present := true
	return &workerpb.HeartbeatResponse{
		LogAcqPresent:           &present,
		LogAcqRulesEnabled:      true,
		LogAcqRealtimeTail:      false,
		LogAcqGzImportEnabled:   true,
		LogAcqGzScanIntervalMs:  int32((45 * time.Minute) / time.Millisecond),
		LogAcqPathOverridesJson: `{"inst:1":"custom/a.log"}`,
	}
}

// TestApplyLogAcqPolicy_AppliesWhenPresent 验证下发字段被逐项映射到采集策略。
func TestApplyLogAcqPolicy_AppliesWhenPresent(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	var got ingest.AcqPolicy
	var calls int
	hb.SetLogAcqPolicyApplier(ingest.DefaultAcqPolicy(), func(p ingest.AcqPolicy) error {
		got = p
		calls++
		return nil
	})

	hb.applyLogAcqPolicy(presentResp())

	require.Equal(t, 1, calls)
	require.True(t, got.RulesEnabled)
	require.False(t, got.RealtimeTail)
	require.True(t, got.GzImportEnabled)
	require.Equal(t, 45*time.Minute, got.GzScanInterval)
	require.Equal(t, "custom/a.log", got.PathOverrides["inst:1"])
}

// TestApplyLogAcqPolicy_IgnoresWhenAbsent 验证老 CP 未下发（optional 为 nil）时不动作。
//
// 这是最关键的向后兼容判据：若把 nil 当「字段全为 false」，会把
// 「规则关闭 + 实时采集关闭 + gz 关闭」当成 CP 意图应用下去，直接停掉采集。
func TestApplyLogAcqPolicy_IgnoresWhenAbsent(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	called := 0
	hb.SetLogAcqPolicyApplier(ingest.DefaultAcqPolicy(), func(ingest.AcqPolicy) error {
		called++
		return nil
	})

	// 字段带零值但存在标记为 nil（老 CP 形状）。
	hb.applyLogAcqPolicy(&workerpb.HeartbeatResponse{LogAcqRulesEnabled: false, LogAcqRealtimeTail: false})

	require.Zero(t, called, "未下发（nil）时不得应用——否则会被零值误读成「全部关闭」")
}

// TestApplyLogAcqPolicy_DedupesUnchanged 验证同值重复下发不重复应用（CP 每拍重发）。
func TestApplyLogAcqPolicy_DedupesUnchanged(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	calls := 0
	hb.SetLogAcqPolicyApplier(ingest.DefaultAcqPolicy(), func(ingest.AcqPolicy) error {
		calls++
		return nil
	})

	hb.applyLogAcqPolicy(presentResp())
	hb.applyLogAcqPolicy(presentResp())
	require.Equal(t, 1, calls, "值未变化时不应重复应用（避免每拍写盘）")

	// 变一个字段：应再次应用。
	changed := presentResp()
	changed.LogAcqRealtimeTail = true
	hb.applyLogAcqPolicy(changed)
	require.Equal(t, 2, calls, "生效值变化时应再次应用")
}

// TestApplyLogAcqPolicy_RetriesAfterApplyFailure 验证应用失败后**不**吞掉后续重试。
//
// 契约：失败只告警、不阻断心跳，且下一拍同值下发仍会重试（否则一次瞬时写盘失败
// 会让配置永久停在旧值）。
func TestApplyLogAcqPolicy_RetriesAfterApplyFailure(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	calls := 0
	fail := true
	hb.SetLogAcqPolicyApplier(ingest.DefaultAcqPolicy(), func(ingest.AcqPolicy) error {
		calls++
		if fail {
			return errors.New("模拟持久化失败")
		}
		return nil
	})

	hb.applyLogAcqPolicy(presentResp())
	require.Equal(t, 1, calls)

	// 下一拍同值重发：因上次失败未记入生效基准，应重试。
	hb.applyLogAcqPolicy(presentResp())
	require.Equal(t, 2, calls, "上次应用失败时，同值下发必须重试")

	// 重试成功后进入正常去重。
	fail = false
	hb.applyLogAcqPolicy(presentResp())
	require.Equal(t, 3, calls)
	hb.applyLogAcqPolicy(presentResp())
	require.Equal(t, 3, calls, "成功后同值不再重复应用")
}

// TestApplyLogAcqPolicy_NoApplier 验证未注入 applier 时不 panic（向后兼容/无采集运行时）。
func TestApplyLogAcqPolicy_NoApplier(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	require.NotPanics(t, func() { hb.applyLogAcqPolicy(presentResp()) })

	var nilHB *Heartbeat
	require.NotPanics(t, func() { nilHB.applyLogAcqPolicy(presentResp()) })
}

// TestApplyLogAcqPolicy_InvalidPathOverridesJSONIsSafe 验证非法覆盖 JSON 回退为「无覆盖」，
// 而不是让整份配置应用失败（一个坏字段不该拖垮其余开关）。
func TestApplyLogAcqPolicy_InvalidPathOverridesJSONIsSafe(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	var got ingest.AcqPolicy
	hb.SetLogAcqPolicyApplier(ingest.DefaultAcqPolicy(), func(p ingest.AcqPolicy) error {
		got = p
		return nil
	})

	resp := presentResp()
	resp.LogAcqPathOverridesJson = `{not-json`
	hb.applyLogAcqPolicy(resp)

	require.Empty(t, got.PathOverrides, "非法 JSON 应回退为无覆盖")
	require.False(t, got.RealtimeTail, "其余字段仍应正常应用")
	require.True(t, got.GzImportEnabled)
}
