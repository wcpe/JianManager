# API Spec — FR-037 运维控制台布局

> 关联 FR: FR-037 | 优先级: P1 | 关联 ADR: ADR-009

## 概述

FR-037 为纯前端布局重构，**不新增任何后端 endpoint**。它把现有「单侧栏导航 + 路由内容区」的主布局（`DashboardPage`）替换为「运维控制台」三段式 Shell，右侧工作区点实例开单个终端。所有数据均复用现有 API。

## 复用的后端 API

| Endpoint | 方法 | 用途 | 前端 hook |
|---|---|---|---|
| `/nodes` | GET | 节点下拉来源（全部节点 + 各节点） | `useNodes()`（`web/src/api/nodes.ts`） |
| `/instances?nodeId=<id>` | GET | 实例树数据；`nodeId` 省略时返回全部实例（前端按节点分组） | `useInstances({ nodeId })`（`web/src/api/instances.ts`） |
| `/instances/:id/terminal-token` | GET | 工作区终端一次性连接 token（仅实例运行时） | `useTerminalToken(id, perm)`（`web/src/api/terminal.ts`） |

WS 终端连接走 `tokenData.wsUrl?token=<token>`，与 `InstanceDetailPage` 终端 Tab 完全一致（见 `web/src/components/Terminal.tsx`）。

## 请求/响应结构

复用现有类型，无新增 DTO：

- `NodeInfo`（`web/src/api/nodes.ts`）— 取 `id`、`name`。
- `InstanceInfo`（`web/src/api/instances.ts`）— 取 `id`、`name`、`nodeId`、`status`。
- `TerminalTokenData`（`web/src/api/terminal.ts`）— `token`、`wsUrl`、`expiresIn`。

## 权限

无新增权限节点。沿用 `node:read`、`instance:read`、`instance:terminal` 既有约束（由各复用 endpoint 自身校验）。

## 错误码

无新增错误码。终端 token 获取失败（实例非运行态等）沿用 `useTerminalToken` 既有 `error` 透出，工作区展示「终端连接失败」占位，与实例详情页一致。

## 不做（范围外）

- 不新增 `GET /bots/summary`、`POST /bots/batch`（属 FR-038）。
- 工作区不渲染 Bot 段、实例树不挂 Bot 聚合徽标（属 FR-039）。
- 不实现分屏 / 切导播台 / 拖拽（后续阶段，仅保留禁用占位按钮）。
