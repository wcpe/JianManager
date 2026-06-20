# 实施计划 — FR-060 时序监控与历史曲线

> 关联 FR: FR-060 | 优先级: P1 | 状态: 📋 todo | 关联 ADR: ADR-013（分级降采样）；ADR-014（ServerProbe 实例指标源）

## 背景

FR-010 只存当前值。指标已在流动：节点经心跳、实例富指标经 ServerProbe（`ScrapeServerProbe`→`ProbeSnapshot`：TPS/MSPT/堆/线程/CPU/uptime/分世界）。本 FR 把它们沉淀为时序，CP 端分级降采样（ADR-013），前端三级历史曲线。Worker 不碰 DB——经心跳上报、CP 落库。深度/分世界指标 ServerProbe 现成提供，无需插件桥（FR-103 已退役）。

## 设计要点
- **节点采集**：沿用 `internal/worker/metrics/collector.go` 心跳指标（CPU/内存/磁盘/网络累计字节）；CP 据相邻累计字节算网络速率。
- **实例采集**：Worker 心跳 tick 对每个 RUNNING 实例 `ScrapeServerProbe(localhost, ProbePort, token)`，探针不可用回退 RCON（`metrics/rcon.go`，仅 TPS/在线），缺测写 NULL。
- **传输**：扩展 `HeartbeatRequest` 追加 `repeated InstanceMetricSample`（含分世界 `WorldSample`），向后兼容。
- **存储**：`metric_series` 维度表 + 三档样本/卷积窄表，`(series_id, ts)` 索引。入库 = upsert series + 写 raw。
- **聚合/清理**：复用 scheduler——每 5min 卷 raw→5m、每 1h 卷 5m→1h、每小时按 TTL 清理（raw 48h/5m 30d/1h ≥400d）。SQLite/MySQL 同构。
- **查询**：`/metrics/series` 按 range 自动选档；`/metrics/overview` 跨节点聚合 + 迷你趋势。

## 任务拆解

### 数据模型与迁移
- [ ] `internal/controlplane/model/metric.go`：`MetricSeries`+`MetricSampleRaw`+`MetricRollup5m`+`MetricRollup1h` + scope/metric_key 常量；唯一索引。
- [ ] `database.AutoMigrate` 注册四模型。

### Worker 采集
- [ ] `internal/worker/metrics/`：心跳路径对 RUNNING 实例并发 `ScrapeServerProbe`（回退 RCON），汇成每实例快照。
- [ ] 心跳发送处装入 `instance_metrics`（节点指标沿用既有字段；网络速率所需累计字节已在心跳）。

### proto
- [ ] `proto/worker.proto`：`WorldSample`+`InstanceMetricSample`+`HeartbeatRequest.instance_metrics`；重生成 `workerpb`。

### CP 入库 + 聚合
- [ ] `internal/controlplane/service/metric.go`：`Ingest`（节点 + 每实例 + 分世界 → upsert series + 写 raw；网络速率据相邻累计算）、`QuerySeries`（选档取数）、`Overview`（聚合）。
- [ ] `internal/controlplane/grpc/handler.go`：`Heartbeat` 处理调用 `metric.Ingest`。
- [ ] `metric_rollup.go`（或并入 scheduler）：5m/1h 聚合 + TTL 清理，启动注册到 scheduler。

### 路由 `/metrics`
- [ ] `internal/controlplane/router/metrics.go`：`GET /metrics/series`、`GET /metrics/overview`（RBAC 读校验）；`router.go` 注册、`Services.Metric` 接线。

### 前端
- [ ] `web/src/api/metrics.ts`：`useMetricSeries`、`useMetricOverview`（扩展现有文件）。
- [ ] 复用 FR-061 `TimeSeriesChart`+`RangePicker`（见「依赖/顺序」）。
- [ ] `OverviewPage`（聚合曲线）、节点详情（节点曲线 + 其上实例对比）、实例详情（实例曲线 + 分世界曲线）。
- [ ] `web/src/i18n/{zh,en}.json`：`metrics.*` 键。

### 测试
- [ ] `metric_test.go`（service）：ingest/upsert、5m/1h 聚合、TTL 清理、自动选档、缺测(NULL)、网络速率推导、分世界多序列。
- [ ] `metrics_test.go`（router）：series happy/越权(403)/非法参数(400)、overview。

## 验证
- [ ] `go build ./...`、`go vet ./...` 通过
- [ ] `go test ./internal/controlplane/... ./internal/worker/...` 通过
- [ ] `cd web && npx tsc --noEmit && npm run lint && npm run build` 通过
- [ ] CP 重启后历史不丢、过期清理（脚本/人工核验）

## 依赖 / 顺序
- **后端独立可先行**（采集/存储/聚合/API 与样式无关）。
- **前端图表依赖 FR-061** 的 `TimeSeriesChart`/`RangePicker`——前端排在 FR-061 Phase 2 之后。

## 不做（范围外，见 scope-discipline）
- 不自采 ServerProbe 之外的深度指标；不复活插件桥（FR-103 已退役）。
- 告警与时序联动（沿用 FR-011 实时阈值）。
- 指标导出/下载、外部 TSDB 后端。
- 不用 ServerProbe 自身本地历史文件作源（分散、无法跨节点聚合，见 ADR-013 替代方案）。

## 开放问题
- 网络速率在 CP 端据相邻心跳累计字节算（首拍无速率；节点重启计数器回绕需钳制）。
- 大量实例时单次心跳负载偏大、每 tick 多实例并发抓探针的开销；本期不优化（记 backlog）。
