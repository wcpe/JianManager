package ws

import (
	"encoding/json"
	"log/slog"
)

// 托管模式（ServerProbe PR#64）经桥上的指标推送处理。
//
// ## 为什么必须在 Worker 侧落住
// 托管模式的核心承诺之一是**零监听**：探针不再开 `/metrics` HTTP 端点。
// 而 Worker 的采集编排（internal/worker/metrics 的 CollectInstanceTelemetry）默认走
// 「HTTP 抓 /metrics」那条路径——托管模式下它必然失败，面板上的 TPS/MSPT/玩家数会全空。
// 所以推送帧到了必须被接住，否则托管=“监控全瞎”，比不启用还差。
//
// ## 只转发、不解析业务语义
// 本层只做三件事：把帧里的 `payload`/`instance` 取出来、按实例记录最新一份、
// 推给注入者。字段含义、单位换算、缺失语义都归 metric 包管（谁消费谁解释）。
// 这样探针的 `metrics` 键集演进（新增指标）不需要改桥层。

// metricSnapshotEventName 是托管模式指标推送帧的 event 子类型。
const metricSnapshotEventName = "metric_snapshot"

// MetricSnapshotFrame 是一帧托管模式指标推送里 Worker 关心的部分。
//
// 帧形如：
//
//	{"type":"event","event":"metric_snapshot","instance":"<uuid>",
//	 "dedupKey":"metric:<uuid>:<ts>",
//	 "payload":{"ts":<epoch秒>,"sequence":<n>,"metrics":{...}}}
//
// 注意载荷在 `payload` 下而不是 `data`——与 command_result 的信封不同，
// 这里不能复用 parseBridgeEventData。
//
// @property InstanceUUID 帧自报的实例（缺省时用连接上的实例）。
// @property TS 采样时刻（epoch **秒**）。
// @property Sequence 探针进程内单调序号（同一秒内多帧靠它区分，不能拿去重键当唯一标识）。
// @property Metrics 原始 metrics 对象（原样保留，交由消费方解释）。
// @property Raw 原始载荷文本（排障与演进兼容用）。
type MetricSnapshotFrame struct {
	InstanceUUID string
	TS           int64
	Sequence     int64
	Metrics      json.RawMessage
	Raw          string
}

// metricSnapshotPayload 是 metric_snapshot 帧 `payload` 的解析形态。
type metricSnapshotPayload struct {
	TS       int64           `json:"ts"`
	Sequence int64           `json:"sequence"`
	Metrics  json.RawMessage `json:"metrics"`
}

// metricSnapshotEnvelope 是整帧的解析形态（只取 Worker 需要的字段）。
type metricSnapshotEnvelope struct {
	Type     string                `json:"type"`
	Event    string                `json:"event"`
	Instance string                `json:"instance"`
	Payload  metricSnapshotPayload `json:"payload"`
}

// MetricSnapshotHandler 接收托管模式推送的指标帧，由 Worker 注入以接入采集链路。
//
// **实现必须非阻塞**：它在桥的读循环上被同步调用，慢实现会拖住整条连接
// （包括 command_result 回执与心跳）。需要落盘/上行的处理请自己起 goroutine 或丢进队列。
type MetricSnapshotHandler func(MetricSnapshotFrame)

// SetMetricSnapshotHandler 注入指标帧回调。nil 表示不接收（独立模式就无需注入）。
func (s *PluginBridgeServer) SetMetricSnapshotHandler(h MetricSnapshotHandler) {
	s.metricHandler = h
}

// parseMetricSnapshot 把一帧 metric_snapshot 解析为结构化结果。
//
// 解不开的帧返回 ok=false 并记 DEBUG：指标是 best-effort 可丢通道，
// 一帧坏数据不能影响连接（与 event 帧的容错策略一致）。
//
// @param session 当前会话（帧未自报实例时回退到它）。
// @param msg 已解析的上行帧。
// @param msgBytes 原始帧文本（放入 Raw 供排障）。
// @return 解析结果与是否有效。
func parseMetricSnapshot(session *PluginSession, msg bridgeMessage, msgBytes []byte) (MetricSnapshotFrame, bool) {
	var env metricSnapshotEnvelope
	if err := json.Unmarshal(msgBytes, &env); err != nil {
		slog.Debug("解析 metric_snapshot 帧失败", "instanceId", session.InstanceID, "error", err)
		return MetricSnapshotFrame{}, false
	}
	instance := env.Instance
	if instance == "" {
		instance = session.InstanceID
	}
	return MetricSnapshotFrame{
		InstanceUUID: instance,
		TS:           env.Payload.TS,
		Sequence:     env.Payload.Sequence,
		Metrics:      env.Payload.Metrics,
		Raw:          string(msgBytes),
	}, true
}

// emitMetricSnapshot 把指标帧交给注入者（未注入则静默丢弃）。
func (s *PluginBridgeServer) emitMetricSnapshot(frame MetricSnapshotFrame) {
	if s.metricHandler == nil {
		return
	}
	s.metricHandler(frame)
}
