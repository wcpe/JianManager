# FR-534 业务插件面板

> 状态：**已实现（状态行原为「草拟」，实现早已落地，2026-10 对账修正）**：components/views/instances/BusinessDomainStatus.tsx · 优先级：P1 · 依赖：FR-526（桥能力接入）
> 关联：ServerProbe FR-35（域状态上报）

## 1. 背景

运维需要一眼看出「这台服务器探针注册了哪些业务插件、哪些可用、哪些挂了」，而不是登录服务器翻日志。

## 2. 核心原则

> **JM 只渲染探针上报的原始信息，不解释业务语义、不建业务表。**

| 做 | 不做 |
|---|---|
| 展示域清单、健康状态、计数 | 解析 manifest 里的业务含义 |
| 展示 manifest 原文（折叠） | 按动作名做业务归类 |
| 展示最近调用时间与成败 | 统计业务指标（完成率等） |
| 缓存到 TTL 过期（默认 30s） | 持久化存储 manifest |

## 3. 数据来源

`GET /api/v1/instances/:id/business/status` → 转调探针 `core.business_status`（ServerProbe FR-35）。

返回结构见 ServerProbe spec，JM 原样透传 + 补充实例元数据。

## 4. 视图设计

### 4.1 实例列表页（汇总）

在现有实例列表增加一列「业务插件」：

| 列 | 内容 |
|---|---|
| 域数量 | `3 个` / `未注册` |
| 健康摘要 | `3/3 正常` / `2/3 降级`（降级标橙色） |

**懒加载**：列表页默认不拉详情，只在展开时拉。100+ 实例时避免 N 次请求。

### 4.2 实例详情页（明细）

```
业务插件（3）
┌──────────────────────────────────────────────────────────┐
│ activity        正常    调用 1523 次 / 失败 7 次           │
│ 最近调用 2 分钟前    manifest a3f2b8c1…  [查看]            │
├──────────────────────────────────────────────────────────┤
│ economy         降级    原因：PLUGIN_DISABLED             │
│ 从未调用                                                  │
├──────────────────────────────────────────────────────────┤
│ inventory       正常    调用 88 次 / 失败 0 次             │
└──────────────────────────────────────────────────────────┘
```

**「查看 manifest」**：展开显示原始 JSON（只读，等宽字体），标注：

> 以下内容由插件声明，平台不解释其语义。

### 4.3 空状态

```
未注册业务插件
该服务器的探针未检测到任何已注册的业务域。
检查：插件是否已安装 / 探针版本是否支持自注册（FR-32+）
```

## 5. 接口

### 5.1 单实例

```http
GET /api/v1/instances/:id/business/status
```

```json
{
  "instanceId": 5,
  "instanceName": "game-01",
  "probeConnected": true,
  "domains": [ /* ServerProbe 原样返回 */ ],
  "fetchedAt": "2026-10-23T12:00:00Z",
  "stale": false
}
```

`stale=true` 表示缓存过期但探针不可达，返回最后一次已知状态。

### 5.2 批量（列表页用）

```http
POST /api/v1/instances/business/status/batch
{ "instanceIds": [1,2,3] }
```

**限制**：单次最多 50 个实例，避免打爆探针。

### 实现补充（2026-10-25）

| 项 | 实现 |
|---|---|
| 上限 | `service.BusinessStatusBatchMaxIDs = 50`，超出返回 `400 BATCH_TOO_LARGE` + `maxCount` |
| 并发 | `BusinessStatusBatchConcurrency = 8`（上限管"能传多少"，并发管"同时打几个探针"） |
| 去重 | 服务端去重保序（前端可能从多个来源拼同一 ID，重复会产生真实探针往返） |
| 结果 | **与入参等长同序**，逐实例独立成败；不存在/无权标 `notFound=true` |
| 语义 | 与单实例完全一致（同一份缓存、同样的 `stale` 回退） |
| 路由顺序 | **必须注册在 `/instances/:id/...` 之前**，否则被通配吃成 `id="business"` |

**汇总列已实现**（2026-10-25，并行改动静置后接入）：

| 位置 | 改动 |
|---|---|
| `lib/instances/instance-view-prefs.ts` | 新增可选列 `business`（**默认关**，与 metrics/heartbeat 同理避免横向溢出） |
| `components/views/instances/InstanceTableParts.tsx` | 表头 `case 'business'` + `present` 集合纳入 |
| `components/views/instances/InstanceRowView.tsx` | 新增 `businessStatus` prop、`cell_business`、CELLS 映射、`present` 纳入、**memo 比较器加入 `sameBusinessStatus`** |
| `components/views/instances/PaginatedInstanceTable.tsx` | 列宽 110px |
| `pages/InstancesPage.tsx` | `useBusinessStatusBatch` 取数（**仅列开启时才取**，切片 50）、`COLUMN_LABEL_KEY` 补键、传给行组件 |
| `lib/instances/business-summary.ts` | `summarizeBusiness` + `sameBusinessStatus` 纯函数（13 个单测） |

**三种状态严格区分**（最容易做错的地方）：

| 状态 | 显示 | 含义 |
|---|---|---|
| `businessStatus === undefined` | `--` | 列未开启 / 批量未返回 / 该台不在批里。**不等于没装插件** |
| 0 个域 | 「未注册」 | 探针明确上报没有域 |
| N 个域 | 「2/3 正常」 | 有降级时用琥珀色 + `stale` 时 title 提示 |

**为什么默认关**：该文件已有先例——五列全开时表格实测宽 1604px、容器仅 974px，
「状态」被截、「操作」被挤出视口。业务列再加 110px 只会更糟，故与 metrics/heartbeat
一样走「按需勾选」。

## 6. 缓存策略

| 项 | 值 | 理由 |
|---|---|---|
| TTL | 30s | 面板刷新频率 |
| 失效 | 探针断连时立即失效 | 避免显示过时数据 |
| 存储 | 内存（不落库） | 这是运行态信息 |
| 探针不可达 | 返回最后已知 + `stale=true` | 比返回空更有用 |

## 7. 权限

| 角色 | 权限 |
|---|---|
| 管理员 | 全部实例 |
| 普通用户 | 仅授权实例 |
| Agent Token | 受 scope 约束 |

**复用现有实例级权限**，不引入新权限模型。

## 8. 验收

### 功能

1. 有域时正确显示数量、状态、计数
2. 无域时显示空状态与排查提示
3. 降级域有醒目标记与原因
4. manifest 可展开，内容原样
5. 探针不可达时 `stale=true` 且显示最后已知

### 性能

6. 100 实例列表页不因该功能变慢（懒加载验证）
7. 单实例查询 < 200ms（含探针往返）

### 清洁度

8. 无业务数据落库（检查表结构）
9. 面板不解析 manifest 内容（code review）

## 9. 待定

1. 是否需要「跨实例汇总视图」（如"哪些服的 activity 域挂了"）——当前不做，避免长出业务语义
2. 缓存 TTL 30s 是否合适
3. `stale` 状态的最长保留时间（当前：一直保留直到探针恢复）

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-534 业务插件面板（feat，需 spec，spec

**需求补充**：`docs/specs/business-plugin-panel/spec.md`，依赖 FR-526）：JM 前端新增「业务插件」视图，**按实例展示该服探针注册了哪些业务域**（域数量、每域健康状态、manifest 摘要、最近调用时间与成败计数、降级原因），数据来源为 ServerProbe 的 `core.business_status`（见 ServerProbe FR-35）。**关键约束**：JM 只**渲染**探针上报的原始信息，不解释业务语义、不建业务表、不缓存 manifest 内容超过必要 TTL；manifest 以折叠原文形式展示（业务台自己解读）。目的：运维能一眼看出「这台服装了哪些业务插件、哪些挂了」，排障不必进服务器。验收：100+ 实例下按实例查询不拖慢列表页（懒加载 + 分页）；域为 0 时明确显示「未注册业务插件」；某域降级时前端有醒目标记与原因；面板不产生任何业务数据落库。

**交付状态**：✅ 已交付@v0.24.0

### FR-534

业务插件可视化

✅ 代码完成（2026-10-25）：**后端** `GET /instances/:id/business/status`（30s 缓存 + `stale` 回退 + 探针连入时清缓存）+ `POST /instances/business/status/batch`（≤50 台、并发 8、去重保序、逐实例独立成败、路由前置避免被 `:id` 通配吃掉）。**前端**：① 详情页 `BusinessDomainStatus`（受控视图 + 接线层 + 挂载于业务卡片，9 个 DOM 用例）；② **列表页汇总列**（可选列 `business` 默认关，`summarizeBusiness` 纯函数区分「无数据 `--`」「未注册」「N/M 正常」三态，13 个单测）；③ 批量 API 层 `useBusinessStatusBatch`（仅列开启才取数）。**默认关的理由**：该表已有先例——五列全开实测 1604px vs 容器 974px，会把「状态/操作」挤出视口。

