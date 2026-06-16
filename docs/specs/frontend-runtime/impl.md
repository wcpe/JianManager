# 实施计划 — FR-024 前端对接运行时 API

> 关联 FR: FR-024 | 优先级: P0 | 状态: 🔨 in-progress

## 背景

前端 14 个页面已完成 UI 骨架，API hooks 层已有 11 个文件覆盖大部分业务实体。但部分页面的数据对接不完整：节点列表缺少实时指标、终端 Tab 未连接 WebSocket、文件 Tab 可能仅占位、Bot 页面缺少 API client。本 FR 补全这些对接。

**前提**: FR-023（gRPC 真实实现）已完成，Control Plane REST API 能通过 gRPC 获取 Worker 数据。

---

## 任务拆解

### Phase 1: 补全 API hooks

- [ ] 新建 `web/src/api/bots.ts`
  - `BotInfo` 接口（uuid, name, instanceId, status, behavior, config）
  - `useBots(instanceId?)` — GET /api/v1/bots
  - `useBot(id)` — GET /api/v1/bots/:id
  - `useCreateBot()` — POST /api/v1/bots
  - `useDeleteBot()` — DELETE /api/v1/bots/:id
  - `useSetBotBehavior()` — POST /api/v1/bots/:id/behavior
- [ ] 新建 `web/src/api/terminal.ts`
  - `useTerminalToken(instanceId, permission)` — GET /api/v1/instances/:id/terminal-token
- [ ] 检查 `web/src/api/nodes.ts` 是否需要补充指标查询
- [ ] 检查 `web/src/api/files.ts` 是否已存在，若无则新建

### Phase 2: NodesPage 实时指标

- [ ] 修改 `web/src/pages/NodesPage.tsx`
  - 为每个在线节点调用 `useNodeMetrics(nodeId)` (30s 轮询)
  - 表格新增 CPU/内存/磁盘列
  - 离线节点指标显示 `--`

### Phase 3: InstanceDetailPage 对接

- [ ] 控制台 Tab: 添加 TPS/玩家/内存指标卡片
  - 调用 `useInstanceMetrics(instanceId)` (10s 轮询)
  - 仅 RUNNING 状态显示
- [ ] 终端 Tab: WebSocket 终端
  - 获取 terminal-token
  - 建立 WebSocket 连接
  - xterm.js 双向流（stdin/stdout/stderr）
  - 断线重连 + 连接失败提示
- [ ] 文件 Tab: 文件管理
  - 文件树浏览（目录导航）
  - CodeMirror 编辑器
  - 保存/上传/下载/删除/重命名
- [ ] 操作按钮: 确保 start/stop/restart/kill 调用正确
  - 操作中显示 loading 状态
  - 操作成功后 invalidate query 刷新

### Phase 4: BotsPage 对接

- [ ] 修改 `web/src/pages/BotsPage.tsx`
  - 使用 `useBots()` 获取列表
  - 创建 Bot 对话框（选择实例 + 配置）
  - 删除 Bot 确认
  - 行为切换下拉
  - 状态实时显示

### Phase 5: 仪表盘数据对接

- [ ] 检查 `DashboardPage.tsx` / `OverviewPage.tsx`
  - 确保概览卡片使用真实 API 数据
  - 节点资源概览使用指标数据

---

## 产出文件范围

| 文件 | 操作 | 说明 |
|---|---|---|
| `web/src/api/bots.ts` | 新增 | Bot API hooks |
| `web/src/api/terminal.ts` | 新增/修改 | 终端 token hooks |
| `web/src/api/files.ts` | 新增/修改 | 文件管理 hooks |
| `web/src/pages/NodesPage.tsx` | 修改 | 添加实时指标列 |
| `web/src/pages/InstanceDetailPage.tsx` | 修改 | 终端/文件/指标对接 |
| `web/src/pages/BotsPage.tsx` | 修改 | Bot CRUD 对接 |
| `web/src/pages/DashboardPage.tsx` | 修改 | 数据源对接 |
| `web/src/pages/OverviewPage.tsx` | 修改 | 数据源对接 |
| `web/src/components/terminal/` | 新增/修改 | xterm.js 组件 |
| `web/src/components/file-manager/` | 新增/修改 | 文件管理组件 |

---

## 依赖

- FR-023（gRPC 真实实现）— 前置完成，确保 REST API 能获取 Worker 数据
- 已有 API hooks：nodes.ts, instances.ts, metrics.ts, schedules.ts 等

---

## 风险

| 风险 | 应对方案 |
|---|---|
| WebSocket 终端连接跨域 | Worker WS 端口需配置 CORS 或通过 CP 反代 |
| xterm.js 组件与现有终端逻辑冲突 | 检查现有组件，复用或替换 |
| 文件管理组件复杂度高 | 分步实现：先浏览+编辑，再上传下载 |
| Bot API 后端尚未实现 | 前端先写 API hooks，后端由 FR-021 负责 |
