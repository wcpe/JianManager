# API Spec — FR-024 前端对接运行时 API

> 关联 FR: FR-024 | 优先级: P0

## 概述

前端页面已完成 UI 骨架，但部分页面的数据尚未对接真实 API。本 FR 补全前端 API hooks 和页面逻辑，使所有页面能展示从 Control Plane REST API 获取的真实运行时数据。

---

## 涉及的 REST API

### 节点指标

- `GET /api/v1/nodes/:id/metrics` — 节点 CPU/内存/磁盘指标
- 关联前端：`NodesPage` 节点列表的实时指标列
- 轮询间隔：30s

### 实例操作

- `POST /api/v1/instances/:id/start` — 启动实例
- `POST /api/v1/instances/:id/stop` — 停止实例
- `POST /api/v1/instances/:id/restart` — 重启实例
- `POST /api/v1/instances/:id/kill` — 强制终止
- 关联前端：`InstanceDetailPage` 操作按钮

### 实例指标

- `GET /api/v1/instances/:id/metrics` — TPS/在线玩家/内存
- 关联前端：`InstanceDetailPage` 控制台 Tab 概览区
- 轮询间隔：10s

### 终端

- `GET /api/v1/instances/:id/terminal-token?permission=write` — 获取终端连接 token
- 响应：`{ token, wsUrl, expiresIn }`
- 关联前端：`InstanceDetailPage` 终端 Tab
- 连接：xterm.js → WebSocket(wsUrl?token=xxx)

### 文件管理

- `GET /api/v1/instances/:id/files?path=xxx` — 文件列表
- `GET /api/v1/instances/:id/files/read?path=xxx` — 读取文件
- `POST /api/v1/instances/:id/files/write` — 写入文件
- `POST /api/v1/instances/:id/files/upload` — 上传文件
- `GET /api/v1/instances/:id/files/download?path=xxx` — 下载文件
- `DELETE /api/v1/instances/:id/files` — 删除文件
- `POST /api/v1/instances/:id/files/rename` — 重命名
- 关联前端：`InstanceDetailPage` 文件 Tab

### Bot 管理

- `GET /api/v1/bots` — Bot 列表
- `POST /api/v1/bots` — 创建 Bot
- `DELETE /api/v1/bots/:id` — 删除 Bot
- `POST /api/v1/bots/:id/behavior` — 切换行为
- 关联前端：`BotsPage`

---

## 页面对接详情

### NodesPage — 节点实时指标

**现状**: 节点列表仅显示静态信息（名称、IP、状态）
**目标**: 添加 CPU/内存/磁盘使用率列，30s 自动刷新

```
┌──────────────────────────────────────────────────────────┐
│ 名称       │ IP         │ 状态    │ CPU   │ 内存   │ 磁盘  │
│ node-01   │ 10.0.0.1   │ 🟢在线  │ 65%   │ 78%   │ 42%  │
│ node-02   │ 10.0.0.2   │ 🔴离线  │ --    │ --    │ --   │
└──────────────────────────────────────────────────────────┘
```

### InstanceDetailPage — 控制台 Tab

**现状**: 无实例指标展示
**目标**: 在控制台 Tab 顶部添加 TPS/玩家/内存指标卡片

### InstanceDetailPage — 终端 Tab

**现状**: 终端 Tab 可能仅有占位
**目标**: 获取 terminal-token → 建立 WebSocket → xterm.js 双向流

### InstanceDetailPage — 文件 Tab

**现状**: 可能已有基础 UI
**目标**: 完整的文件树浏览 + CodeMirror 编辑 + 上传下载

### InstanceDetailPage — 操作按钮

**现状**: useStartInstance/useStopInstance 等 hooks 已存在
**目标**: 确保按钮调用正确，操作后自动刷新状态

### BotsPage — Bot 管理

**现状**: 页面存在但可能无后端对接
**目标**: 新建 `web/src/api/bots.ts`，对接 Bot CRUD API

---

## 错误处理

| 场景 | 前端表现 |
|---|---|
| 节点离线 | 指标列显示 `--`，状态标签为红色 |
| gRPC 调用超时 | Toast 错误提示「操作超时，请稍后重试」 |
| WebSocket 连接失败 | 终端显示「连接失败」，提供重连按钮 |
| 文件操作失败 | Toast 错误提示具体原因 |
| Bot 创建失败 | Dialog 中显示错误信息 |
