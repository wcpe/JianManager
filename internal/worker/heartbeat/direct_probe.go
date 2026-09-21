package heartbeat

import (
	"time"

	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// applyDirectProbeTimeouts 把 CP 经心跳响应下发的 MC 直探（SLP / Query）超时写入 metrics 包的
// 生效值（FR-446，spec §2.6）。值 <=0 表示 CP 未配置（老 CP 不下发）→ 清空覆盖，回退内置默认。
//
// 每拍都调用、幂等：CP 改设置后 Worker 不重启即在下一拍生效（≤1 心跳周期）。写入的是进程内
// 全局生效值，心跳时序链路与 GetInstanceMetrics 实时链路共用，保证两条链路超时语义一致。
func applyDirectProbeTimeouts(resp *workerpb.HeartbeatResponse) {
	if resp == nil {
		return
	}
	metrics.SetDirectProbeTimeouts(
		time.Duration(resp.GetDirectProbeSlpTimeoutMs())*time.Millisecond,
		time.Duration(resp.GetDirectProbeQueryTimeoutMs())*time.Millisecond,
	)
}
