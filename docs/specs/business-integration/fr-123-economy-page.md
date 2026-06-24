# FR-123: 经济定制页（余额 / 排行 / 转账 / 流水）

> 状态：🚧 开发中 ｜ 关联 ADR 无（消费既有读写契约，不新增架构决策）｜ 依赖 FR-119（业务掌控台 UI）、FR-121（业务写横切硬化）、FR-122（经济汇聚与多区聚合）

## 1. 目标与范围

在 FR-119 通用 manifest 驱动的 `BusinessSegment` 之上，为**经济域**提供一个**定制页**——把零散动作收敛为运营人员日常需要的四块视图：

1. **余额查询**：按玩家（+ 可选货币）查 JM 经济镜像（FR-122 `economy_balance_mirrors`），逐 node→zone 行展示（跨区不盲目求和）。
2. **排行榜**：按货币（+ 可选 zone/node 维度）取余额倒序 Top-N（mce 公开 API 无排行，**旁路**自 JM 镜像派生）。
3. **转账**：面板发起 `economy.transfer`（from→to），走二次确认 UI（FR-121 `DangerConfirm` + 写参）。
4. **流水查询**：按玩家查经济变更审计（FR-122 `economy_ledger_entries`，经 `GET /business/events?domain=economy` 通用 envelope 流），逐条入账留痕。

加扣（`economy.deposit`/`economy.withdraw`）作为转账块旁的快捷写动作，同样走二次确认。

### 非目标（明确不做）

- **不改 FR-121/122 已落地契约**——读端点（mirror/aggregate/events）、写路径（`POST /instances/:id/business`）、注入语义全部消费既有实现，不回改。
- **不新建经济镜像/审计表**——排行榜直接查 FR-122 已有的 `economy_balance_mirrors`。
- **不穿透探针查排行**——排行是 JM 侧对**自有镜像表**的派生查询（守架构不变量：CP 读自有 DB、不经探针）。
- **不实现 consume/refund/set/adjust 的专用 UI**——这些低频/语义复杂动作仍由通用 `BusinessSegment` 承载；定制页只收口高频四块（余额/排行/转账/加扣/流水）。
- **不新增带编号 ADR**——纯消费既有契约。

## 2. 排行榜实现选择（旁路）

mce 公开 API 无 leaderboard。两个候选：

- **① 后端只读端点**（选定）：新增 `GET /business/economy/leaderboard?currency=&zone=&node=&limit=`，CP 用 GORM 查 `economy_balance_mirrors` 按余额数值倒序取 Top-N。
- ② 前端取 mirror 列表后排序。

**选 ①，理由**：

1. **正确性**：`Balance` 列是 `varchar` 承载的 `BigDecimal` 字符串（FR-122 禁浮点防多币种精度失真）。纯前端字符串排序对 `"100"` / `"99.5"` / `"1000"` 给出错误顺序；前端转 `Number` 又对大额/高精度有精度损失。后端 `ORDER BY CAST(balance AS <numeric>) DESC` 是数值序、且 MySQL 用 `DECIMAL(65,18)` 保持精确。
2. **有界**：`LIMIT N` 在库侧收敛，避免把整张镜像表（可能很大）拉到浏览器只为排个序。
3. **守架构**：查询命中 CP 自有镜像表，零探针穿透，符合不变量「数据库仅 CP 读写」「排行须 JM 侧从镜像派生」。

### 2.1 跨方言数值排序

镜像 `balance` 为字符串十进制，排序须数值化。按 `db.Dialector.Name()` 选 CAST：

| 方言 | 表达式 | 说明 |
|---|---|---|
| `mysql` | `CAST(balance AS DECIMAL(65,18))` | 精确十进制序，无浮点损失 |
| 其它（`sqlite` 等） | `CAST(balance AS REAL)` | SQLite 数值化排序（dev 足够） |

非数值/空 `balance` 落到 CAST 的方言默认（MySQL→0、SQLite→0.0），不阻断查询。

## 3. 后端改动（最小）

### 3.1 service：`BusinessEventService.LeaderboardEconomy`

新增只读方法（`internal/controlplane/service/business_events.go`）：

```go
type EconomyLeaderboardQuery struct {
    Currency string // 必填：排行按单一货币（不同货币不可比）
    ZoneID   string // 可选：限定某区
    NodeUUID string // 可选：限定某节点
    Limit    int    // Top-N，复用 clampLimit（默认 100，上限 500）
}

type EconomyLeaderboardRow struct {
    Rank       int    `json:"rank"`
    PlayerName string `json:"playerName"`
    Currency   string `json:"currency"`
    NodeUUID   string `json:"nodeUuid"`
    ZoneID     string `json:"zoneId"`
    Balance    string `json:"balance"`
}

func (s *BusinessEventService) LeaderboardEconomy(q EconomyLeaderboardQuery) ([]EconomyLeaderboardRow, error)
```

- `Currency` 必填（缺省返回 error → 路由 400）：跨货币余额不可比，排行必须锚定单一货币。
- 排序 `ORDER BY CAST(balance AS <方言数值>) DESC`，`Rank` 由返回序号 1..N 赋值。
- 逐 (node, zone) 行返回（与 mirror/aggregate 同口径）：同名玩家跨区是不同账户、各占一行参与排行（不合并、不串味）。

### 3.2 router：新增 endpoint

`internal/controlplane/router/business_event.go` 追加：

```
GET /business/economy/leaderboard?currency=&zone=&node=&limit=
```

- 权限 `instance:read`（复用 `requireRead`，与既有读端点同口径，平台级只读不绑单实例）。
- `currency` 缺失 → 400 `INVALID_REQUEST`。
- 响应：`{ "currency": "...", "rows": [ {rank,playerName,currency,nodeUuid,zoneId,balance}... ] }`。

### 3.3 不动的部分

- 不改 proto、不改 model、不改写路径、不改 FR-122 既有三端点。

## 4. 前端改动

### 4.1 形态：实例控制台新增 `economy` 分段

与 FR-119 `business` 分段同范式（`WorkspacePane` Tabs + `useConsoleStore` 的 `WorkspaceSegment` 枚举）。新增 `economy` 分段，渲染 `EconomySegment`。理由：

- 写路径是实例级（`POST /instances/:id/business` 需 `:id`），定制页天然挂实例控制台与既有 IA 一致。
- 读端点虽平台级，但定制页以「玩家 + 货币」为主线查询，逐 node→zone 行已自带区分维度，无需强绑实例节点。

### 4.2 读 API client：`web/src/api/economy.ts`（新增）

封装 FR-122 三读端点 + FR-123 排行端点：

| 函数 | 端点 | 用途 |
|---|---|---|
| `fetchEconomyMirror(params)` | `GET /business/economy/mirror` | 余额查询（逐 node→zone） |
| `fetchEconomyLeaderboard(params)` | `GET /business/economy/leaderboard` | 排行 Top-N |
| `fetchEconomyEvents(params)` | `GET /business/events?domain=economy` | 流水（解析经济 envelope payload 的 data 段） |

转账/加扣**复用** `@/api/business.ts` 的 `dispatchBusiness`（write 参 + `operationId`），不另写写 client。

### 4.3 组件：`web/src/components/console/EconomySegment.tsx`（新增）

四块子视图（Tabs 或并列卡片）：

1. **余额**：玩家名 + 货币输入 → 查询 → `Table`（玩家/货币/节点/区/余额）。
2. **排行**：货币（必填）+ zone/node（可选）+ Top-N → `Table`（名次/玩家/节点/区/余额）。
3. **转账**：from / to / 货币 / 金额 + 原因 → 点「转账」弹 `DangerConfirm`（scope=group）→ 确认后 `dispatchBusiness(..., {write:true, operationId:crypto.randomUUID(), reason})`，展示结果信封；旁置「加 / 扣」快捷（player/货币/金额，同样二次确认）。
4. **流水**：玩家名（+ 可选货币）→ 查询 → `Table`（时间/玩家/货币/类型/变更额/变更后余额/区/账本号）。

- **金额禁浮点**：金额输入按字符串原样下发（payload `amount` 字符串），不 `parseFloat`。
- **taskId 不暴露**：转账/加扣的 `taskId` 由 CP 注入（payload `putIfAbsent`），前端**不**在 payload 写 `taskId`，只传顶层 `operationId`。payload 仅含业务字段（transfer: `{from,to,currency,amount}`；deposit/withdraw: `{player,currency,amount}`）。
- 失败/降级：沿用既有 `BusinessResult.available=false` + `error` 展示；查询失败显式错误态。

### 4.4 纯函数抽离（便于单测、避免 fast-refresh 警告）

`web/src/components/console/economy-view.ts`：把「经济 envelope payload → 流水行」「金额展示格式化」「排行行映射」等纯逻辑抽成独立模块单测（与 `business-actions.ts`、`audit-filters.ts` 同范式）。

### 4.5 i18n

`web/src/i18n/zh.json` / `en.json` 新增 `economy.*` 命名空间（tab/标题/四块标签/字段标签/按钮/确认文案/空态/错误），zh 与 en 同步完整。

### 4.6 主题

复用既有 shadcn/ui 组件（`Table`/`Input`/`Button`/`Tabs`/`Card`/`Badge`）+ CSS 变量，暗/亮色随主题自动适配（不引入硬编码色值，绿/红状态用 `text-green-600 dark:text-green-400` 既有范式）。

## 5. 验收映射（PRD FR-123）

| 验收标准 | 实现 | 证据 |
|---|---|---|
| 余额查询 + 排行（旁路）+ 转账 + 流水 | EconomySegment 四块 + 后端排行端点 | 单测 + 构建 |
| 面板发起转账/加扣走二次确认 UI | 复用 `DangerConfirm` + `dispatchBusiness` 写参 | 组件 + business.test 既有覆盖 |
| i18n + 暗/亮色 | `economy.*` 命名空间 zh/en + CSS 变量 | 文件 diff |
| 真机：对真 mce 查余额排行流水、发起转账生效 | **待真机**（单栈 M2 收口统一真机） | 如实标待真机 |

## 6. 测试

- **后端（Go）**：`business_events_test.go` 追加 `TestLeaderboardEconomy_*`——按余额数值倒序（验 `"1000" > "100" > "99.9"` 字符串序被纠正）、`currency` 必填、zone/node 过滤、Limit 收敛、跨区同名玩家各占一行。
- **前端（vitest）**：`economy-view.test.ts`——经济 envelope→流水行解析（含坏 data 降级）、金额字符串原样不丢精度、排行行映射 rank 赋值；`business.test.ts` 既有写参覆盖复用。
