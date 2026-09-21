package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestMetricsFetchTimeout_CoversWorstCaseSerialProbeChain 实时指标链路预算须覆盖 Worker 侧
// 串行 `探针(≤5s)→SLP(t)→Query(t)` 的最坏时延，否则详情页被 gRPC DROP（FR-446 复审 N2）。
func TestMetricsFetchTimeout_CoversWorstCaseSerialProbeChain(t *testing.T) {
	svc := &InstanceService{}
	inst := &model.Instance{ProbePort: 8123, ServerPort: 25565, QueryPort: 25566}

	got := svc.metricsFetchTimeout(inst)
	worst := probeScrapeTimeoutCap + defaultDirectProbeTimeout + defaultDirectProbeTimeout
	assert.Greater(t, got, worst, "预算必须严格大于最坏时延，否则被 DROP")

	// 未配置的来源不占用预算。
	assert.Equal(t, realtimeMetricsBudgetMargin+probeScrapeTimeoutCap,
		svc.metricsFetchTimeout(&model.Instance{ProbePort: 8123}))
	assert.Equal(t, realtimeMetricsBudgetMargin+defaultDirectProbeTimeout,
		svc.metricsFetchTimeout(&model.Instance{ServerPort: 25565}))
	assert.Equal(t, realtimeMetricsBudgetMargin, svc.metricsFetchTimeout(&model.Instance{}))

	// 生效超时取自平台设置；读侧同样钳制到共享上界（FR-446 复审 N3 / NEW-ISSUE A）。
	svc.SetSettingsReader(stubSettings{
		SettingKeyDirectProbeSLPTimeout:   "5s",
		SettingKeyDirectProbeQueryTimeout: "45s", // 超上界 → 钳制为 maxDirectProbeTimeout
	})
	assert.Equal(t, realtimeMetricsBudgetMargin+probeScrapeTimeoutCap+5*time.Second+maxDirectProbeTimeout,
		svc.metricsFetchTimeout(inst))
}
