package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// bridgeHeartbeat 构造一段带桥字段的心跳（供 ForgetInstance 行为验证）。
func bridgeHeartbeat(uuid string, queue int32, rejected int64) *workerpb.HeartbeatRequest {
	return &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:                uuid,
			ProbeAvailable:              true,
			Tps:                         20,
			BridgeMetricsPresent:        true,
			BridgeReliableQueue:         queue,
			BridgeReliableRejectedTotal: rejected,
		}},
	}
}

// TestMetric_ForgetInstance_ResetsBridgeBaseline 验证 ForgetInstance 真的清掉了差分基准。
//
// 为什么需要这个测试：这两个 map 的清理入口在生产代码里挂了钩子，但“钩子被调”
// 与“map 真被清”是两回事——接口/钩子接错时编译不报错、运行不报错，
// 只会让旧基准静静留着（每删一个实例漏一条，进程存活期间永不释放）。
//
// 行为判据：清掉基准后，下一次心跳应当**没有前值可差**，因此不产出速率样本；
// 若没清掉，第二次心跳就会差出一个速率。
func TestMetric_ForgetInstance_ResetsBridgeBaseline(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	const iid = "inst-forget"

	// 首拍建基准。
	require.NoError(t, svc.ingestHeartbeatAt(bridgeHeartbeat(iid, 5, 100), base))
	// 次拍有前值 → 应产出 rejected rate。
	require.NoError(t, svc.ingestHeartbeatAt(bridgeHeartbeat(iid, 5, 160), base.Add(30*time.Second)))

	// 清掉基准（模拟实例重启/探针重连）。
	svc.ForgetInstance(iid)

	// 再拍：无前值 → 不得有 rejected rate（该指标是差分产物）。
	require.NoError(t, svc.ingestHeartbeatAt(bridgeHeartbeat(iid, 5, 220), base.Add(60*time.Second)))

	var rateSamples int
	from, to := wideWindow(base.Add(70 * time.Second))
	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: iid, From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	for _, s := range series {
		if s.MetricKey != model.MetricBridgeRejectedRate {
			continue
		}
		for _, p := range s.Points {
			// 只统计 t+60s 那一拍之后产生的样本
			if p.TS.After(base.Add(55*time.Second)) && p.Avg != nil {
				rateSamples++
			}
		}
	}
	require.Zero(t, rateSamples, "ForgetInstance 未清掉基准：清除后的首拍不应差出速率")
}
