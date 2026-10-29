package ingest

// 回收相关用例的共享夹具（2026-10-29 从已删的补段链文件里抢救出来）。
//
// 为什么单独一个文件：原 `reclaim_backfill_test.go` / `selfheal_reclaim_backfill_test.go` /
// `sweep_lagging_rolling_test.go` 里的**补段链用例**随受管恢复分段机制一并删除，
// 但 `primeConfirmedWALProjection` 这个助手仍被三个**仍然存在**的用例引用：
//   - `reclaim_rolling_test.go`（滚动回收）
//   - `sweep_lagging_rolling_test.go`（兜底环轮转窗口）
//   - `selfheal_stall_backoff_test.go`（自愈退避）
//
// 它本身不引用任何已删符号（不建段、不推进状态机），语义是「先真实投递一批，
// 让 VL 里有可见行」——这是新语义下 `releaseRecovery` 的 `Delivery >= to` 闸所需要的
// 真实前提（它不伪造 ACK，只把已提交正文真的发出去）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// primeConfirmedWALProjection 把 `through` 之前的已耐久条目真实投递一次，
// 并核验 VL 侧确实产生了对应的可见行。
//
// 为什么需要“VL 里真的有行”：新语义下 `releaseRecovery` 只做两件事——确认
// `Delivery >= to` 且 REQUEST_DONE 证据覆盖 `[reclaim, to)`，然后触发一次 `TryReclaim`。
// 而 `m.deliver` 走的是完整生产投递路径（含写入 + 逐字段校验），因此它同时产生
// “请求结果”与“可见副本”两个事实。夹具用它建立真实前提，而不是伪造水位。
func primeConfirmedWALProjection(t *testing.T, m *Manager, source SourceConfig, p *pipeline.Pipeline, through uint64) {
	t.Helper()
	var events []logtypes.Event
	seen := make(map[string]string)
	for _, entry := range p.WAL().Snapshot() {
		if !entry.Durable || entry.Event.Record.End > through {
			continue
		}
		if hash, duplicate := seen[entry.Event.EventID]; duplicate {
			require.Equal(t, hash, entry.Event.CanonicalHash, "本夹具只允许重复的相同正文，不能吞身份变体")
			continue
		}
		seen[entry.Event.EventID] = entry.Event.CanonicalHash
		events = append(events, entry.Event)
	}
	require.NotEmpty(t, events, "夹具必须先有真实已提交正文")
	_, err := m.deliver(source, events, false)
	require.NoError(t, err)
	require.Len(t, m.landedVisibleInVL(source, events), len(events), "ACK之外还须有当前已发布的逐字段证据")
}
