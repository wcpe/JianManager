# API Spec — FR-113 全文索引后台化与进度

> 关联 FR: FR-113 | 优先级: P2 | 依赖: FR-074 | 关联 ADR: ADR-017, **ADR-024**

## 概述

把 FR-074 全文索引的**首建**从查询同步关键路径移出，改为后台异步构建；查询时若索引未就绪，返回 `indexing=true`，前端显示「索引中」并自动重试，直到出结果。大工作目录首次搜索不再阻塞 UI；小目录经有界快路径仍同步出结果（不退化）。设计与取舍详见 ADR-024。

边界：索引仍是 Worker 本地派生资产（不进 CP DB），查询仍经既有 gRPC `SearchFiles` 转发（守 ADR-017）。本 FR 不改索引引擎、落位、增量策略、忽略规则，仅改**首建时机**与**就绪表达**。

---

## gRPC（proto/worker.proto，加性新增，protoc 重新生成）

`SearchFilesResponse` 加一个字段（其余不变）：

```protobuf
message SearchFilesResponse {
  repeated SearchHit hits = 1;
  bool truncated = 2;     // 命中数达到 max_results 上限被截断
  bool indexing = 3;      // 索引首建未就绪：本次返回空命中，调用方应稍后重试（FR-113，ADR-024）
}
```

Worker `SearchFiles` 行为（ADR-024）：
- 索引已就绪 → 同步增量 `Update` + 查询，返回命中 + `indexing=false`。
- 未就绪 → 启动后台单飞构建 + 有界等待（200ms）：预算内完成则本次出结果（`indexing=false`）；否则返回 `indexing=true`、空命中、不阻塞。

## REST（CP，原地扩展，路径/方法不变）

### POST /api/v1/instances/:id/files/search
- 响应体加 `indexing` 字段：

```json
{ "hits": [ { "path": "plugins/config.yml", "line": 12, "snippet": "命中行片段" } ], "truncated": false, "indexing": false }
```

- `indexing=true` 时 `hits` 为空、`truncated=false`，表示索引首建中；调用方应稍后用同一查询重试。
- 鉴权、错误码、其余语义同 FR-074，不变。

## 前端（SearchPanel）

- `SearchResult` 加 `indexing: boolean`。
- 收到 `indexing=true`：进入「索引中」态——显示 `search.indexing` 文案（「索引中，首次建立索引，请稍候…」）+ 加载指示，并在约 1s 后自动重试同一查询（带请求序号 `reqSeq` 防抖竞态；用户改查询/模式则旧重试作废）。
- 收到 `indexing=false`：照常渲染命中/空结果/截断提示。

---

## 验收标准（对应 PRD FR-113）

- [ ] 首建移出查询关键路径（后台异步），查询不同步全量重建；小目录有界快路径仍同步出结果
- [ ] 查询时索引未就绪返回 `indexing=true`，前端给「索引中」进度并自动重试
- [ ] 真机：大目录首查不卡 UI、结果一致

## 任务

- [ ] proto 加 `indexing` 字段 + protoc 重生成 workerpb
- [ ] Worker `search.Index`：就绪态（`ready`/`building`/`builtCh`）+ `EnsureBuilding`/`Ready`/`WaitReady`（测试钩子 `buildStartHook`）
- [ ] Worker `SearchFiles`：未就绪后台构建 + 快路径，置 `indexing`
- [ ] 单测：未就绪首查 `indexing=true`、放行构建后就绪查询出命中；小目录首查同步出结果（既有用例不破）
- [ ] CP `SearchResult.Indexing` 透传
- [ ] 前端 `searchFiles` 类型 + `SearchPanel` 索引中态 + 自动重试 + i18n
- [ ] 文档同步：API.md、CHANGELOG
