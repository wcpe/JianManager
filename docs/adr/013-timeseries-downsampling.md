# ADR-013: 分级降采样时序指标存储

- **日期**: 2026-06-20
- **状态**: accepted
- **上下文**: FR-010 已采集节点/实例实时指标（实例指标自 2026-06-21 起经 ServerProbe 富化，见 ADR-014），但只存当前值；FR-060 要在面板上回看数月/季度的历史曲线（节点 CPU/内存/磁盘/网络；实例 TPS/MSPT/堆/线程/CPU；分世界已加载区块/实体/方块实体）。这需要时序存储，但平台是「单二进制 + SQLite/MySQL」自包含部署（ADR-001/005），不可引入 Prometheus/InfluxDB 等外部 TSDB；同时 Worker 不得直连 DB（架构不变量），持久化必须落在 Control Plane。
- **决策**:
  1. **CP 端分级降采样（RRD 式）**：三档分表——`metric_sample_raw`（原始 30s，留 ~48h）→ `metric_rollup_5m`（留 ~30d）→ `metric_rollup_1h`（留 ≥1 年）。每个 rollup 档存 `avg/min/max/last(+count)`，曲线可画包络、聚合逐级下卷。
  2. **窄表 + 序列维度表**：`metric_series` 登记序列身份 `(node_uuid, instance_id?, scope, metric_key, world?)`；样本/卷积表只存 `(series_id, ts, value…)`。对新增指标键、分世界（世界数动态）与未来指标天然可扩展，不改表结构。
  3. **采集经心跳上报、CP 落库**：Worker 在 30s 心跳负载里附带「节点指标 + 每实例 ServerProbe 快照」——节点指标沿用既有 collector；实例指标复用 `ScrapeServerProbe`（ADR-014）抓本机探针 `/metrics`（TPS/MSPT/堆/线程/CPU/uptime/分世界），探针不可用回退 RCON（TPS/在线）。CP 收到即写 raw。Worker 不碰 DB。
  4. **后台聚合 + TTL 清理复用 scheduler**：周期任务每 5min 卷 raw→5m、每 1h 卷 5m→1h，并按各档 TTL 删除过期行。SQLite 与 MySQL 同构（仅 DDL/方言差异）。
  5. **缺测语义**：采集源不可达（探针未部署且 RCON 不可用）写 `NULL`（或不写样本），查询返回断点，前端断线渲染，不补 `0`/`-1` 假值。
- **理由**:
  - 分级降采样是面板类时序的标准解法：近端高清排障、远端低频留存，存储量级可控（数月/季度可行）。
  - 窄表 + 维度表换来可扩展性（分世界数量动态、指标种类会增），代价是行数较多——以 `(series_id, ts)` 复合索引 + TTL 清理在中小型规模内可接受。
  - 复用既有 scheduler 做聚合/清理，不引入新常驻组件，契合单二进制。
  - 实例富指标已由 ServerProbe（ADR-014）现成提供（含分世界），本 ADR 只负责把它「沉淀为时序」，无需自采深度指标、无需插件桥。
- **后果**:
  - 细化 FR-010（实时）为 FR-060（时序历史）；新增 `/metrics/series`、`/metrics/overview`。
  - 扩展 gRPC `Heartbeat` 负载（追加每实例 ServerProbe 快照 `repeated InstanceMetricSample`，向后兼容），须同步 `proto/worker.proto` 与 ARCHITECTURE 通信/数据模型章节。
  - 与 ADR-014（ServerProbe 监控探针）协作：探针为实例指标源，本 ADR 为其历史留存层。
- **替代方案**:
  - 外部 TSDB（Prometheus/InfluxDB/VictoriaMetrics）— 与单二进制自包含相悖，运维负担重，否决（未来大规模可作为可选后端）。
  - 单表只留原始、不降采样 — 数月留存数据量爆炸、查询慢，否决。
  - 宽表（每时刻一行、指标列固定）— 行数少但对分世界（动态）与未来扩展不友好，否决。
  - 让 ServerProbe 自身本地历史文件（`MetricHistoryFile`/`LocalFileMetricStore`）作历史源 — 那是单实例本地、分散在各 Worker 且与平台 DB 脱节，无法跨节点聚合 / RBAC / 统一留存，否决；平台集中时序仍由 CP 持有。
