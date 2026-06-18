# 实现任务 — Bot 规模化后端 API（FR-038）

> 关联 FR：FR-038。状态：开发中。

## 任务清单

### 文档
- [x] 编写 `docs/specs/bot-scale-api/api.md`（Gate 3：endpoint/error/权限/类型映射齐备）
- [x] 同步 `docs/API.md` Bot 章节（`GET /bots` 扩展、`GET /bots/summary` 新增、`POST /bots/batch` 新增）

### 后端 — Service 层
- [x] `AuthzService.AccessibleInstanceIDs(access)`：返回可访问实例 ID 集合（平台管理员返回 `nil` 表示不收敛），跨组隔离下沉为 SQL 谓词
- [x] `BotListQuery` + `BotService.ListPaged`：分页 + instanceId/nodeId/status/behavior/q 过滤，返回 items + total（nodeId 经 instances 联表）
- [x] `BotService.Summary`：全局 total + byStatus；groupBy=instance|node|status|behavior 的 COUNT/GROUP BY 聚合（不序列化 Bot 行）
- [x] `BotService.Batch`：解析目标（ids 或 filter）→ 鉴权收敛 → 按节点分片 → 有界并发委托既有 per-bot RPC → 计数

### 后端 — Router 层
- [x] `GET /bots` 改造为分页响应 `{items,total,page,pageSize}`（保留 `bot:read` + 隔离）
- [x] `GET /bots/summary`（`bot:read`）
- [x] `POST /bots/batch`（`bot:manage`）
- [x] 注册路由

### 前端类型/Hook（最小改动）
- [x] `web/src/api/bots.ts`：`useBots` 适配分页响应；新增 `BotListResponse`/`BotSummary`/`BotBatchRequest`/`BotBatchResult` 类型与 `useBotSummary`/`useBotBatch` hook

### 测试
- [x] 分页：page/pageSize 边界、总数正确
- [x] 过滤：instanceId/nodeId/status/behavior/q
- [x] 摘要：全局 byStatus、各 groupBy 计数
- [x] 批量：happy（全成功）、部分失败（Worker 未连接）、ids 越权剔除计入 skipped、缺参 400
- [x] 鉴权隔离：组成员只见有权实例下 Bot；越权 id 不泄露存在性

### 验证
- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./internal/controlplane/...`
- [x] `cd web && npx tsc --noEmit`（改了 bots.ts）

## 设计决策记录

- **不新增批量 gRPC**：Worker 既有 per-bot `CreateBot/DeleteBot/SetBotBehavior` 已是最小委托单元；Control Plane 负责按节点分片 + 有界并发，符合 FR「复用既有 per-bot RPC」的取向，避免 proto 膨胀与 Worker 侧重复实现。
- **start/stop 语义**：Worker 模型为 create/delete + set-behavior，无独立 start/stop。映射为 `start`→`CreateBot`（重建连接）、`stop`→`DeleteBot`（断连但保留 DB 行，置 `status=stopped`）。
- **隔离下沉 SQL**：用 `AccessibleInstanceIDs` 一次性取可见实例集合做 `IN` 谓词，取代原 `List` 的应用层逐条 `CanAccessInstance` 循环（O(n) gRPC/DB 往返，在万级不可接受）。
