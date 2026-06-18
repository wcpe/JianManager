# 实施计划 — FR-040 全局 Bot 管理页重构

> 关联 FR: FR-040 | 优先级: P1 | 状态: 🔨 in-progress | 关联 ADR: ADR-009 | 依赖: FR-038（Bot 规模化 API，已合并）

## 背景

旧 `/bots` 是扁平表格（`useBots()` 拉全部 Bot 逐行渲染 + CreateBotDialog），压测下单实例 Bot 可达上万（FR-009 容量），扁平列表撑不住，违反 ADR-009「聚合优先、永不全量铺开」。本批将 `/bots` 重写为跨实例总览与管理页：概览卡片 + 分组总览（默认按实例）+ 健康条 + 批量 + 与控制台联动。

**纯前端**，不动后端 Go，复用 FR-038 已合并的 `web/src/api/bots.ts` hooks（`useBots`/`useBotSummary`/`useBotBatch`/`useCreateBot`），未新增/修改任何 hook 或 endpoint。

## API 复用（来自 FR-038，无新增）

| 用途 | hook | 说明 |
|---|---|---|
| 全局概览计数 | `useBotSummary(params)` | 无 `groupBy` → `total` + `byStatus`，喂概览卡片（受工具栏筛选影响） |
| 分组总览 | `useBotSummary({ groupBy, ...filter })` | `groupBy` ∈ instance/node/status/behavior；每组返回 `key/label/total/online` |
| 展开窥视 | `useBots({ ...groupFilter, page, pageSize })` | 仅拉该组首页（peek 10 条），绝不全量 |
| 批量操作 | `useBotBatch()` | `set-behavior`/`stop`/`delete`，目标由 `filter`（=组筛选）指定 |
| 新建 Bot | `useCreateBot()` | 沿用旧表单 |

> 摘要分组只暴露 `online`(=connected) 与 `total`（见后端 `BotSummaryGroup`），不细分 connecting/error。

## 组件拆解

```
web/src/pages/BotsPage.tsx          # 改写：页容器 + 全部子组件（均模块内私有，仅 default export 页面）
  BotsPage                          # 状态编排：search(防抖)/nodeId/status/groupBy + 4 个 groupBy 摘要查询
  useDebounced                      # 局部防抖 hook（搜索输入，300ms）
  SummaryCards                      # 概览卡片：总计/在线/连接中/异常 + 分布（X 实例·Y 节点）
  Toolbar                           # 搜索 + 节点筛选 + 状态筛选 + 分组维度切换（实例/节点/状态/行为）
  GroupOverview                     # 分组表（key=groupBy 重挂复位展开/选择）+ 顶部批量条
  GroupRow                          # 单组行：勾选 + 标签(可展开) + 健康条 + 总数 + 操作
  HealthBar                         # 健康条：在线(绿) vs 其余(灰) 按比例铺色
  GroupBatchMenu                    # 每组批量下拉：设行为/停止/删除（目标=该组筛选）
  BatchBar                          # 顶部批量条：对已勾选多组逐组下发（聚合成功/失败计数）
  GroupPeek / PeekRow               # 展开窥视：分页拉该组首页 Bot（只读，单 Bot 详情见 FR-041）
  CreateBotDialog                   # 新建 Bot（沿用 FR-009 表单）
web/src/pages/bots-overview.ts      # 纯逻辑（无 React）：状态计数/健康条分段/分组→筛选映射/分布计数
web/src/pages/bots-overview.test.ts # vitest 单测（14 用例）
web/src/i18n/{zh,en}.json           # 新增 bots.* 键（概览/工具栏/分组/健康/批量/分页/压测入口）
```

> 命名沿用 console/instance-tree.ts 约定：逻辑模块用 kebab-case `.ts`，便于无 DOM 单测且规避大小写文件名冲突。

## 设计取舍（对齐 ADR-009 + scope-discipline）

- **聚合优先**：默认渲染「分组行」而非单个 Bot；逐条 Bot 只在展开某组时分页窥视（peek 10 条/页）。上万 Bot 永不逐行铺开。
- **概览卡片状态映射**：在线=`byStatus.connected`，连接中=`byStatus.connecting`，异常=`byStatus.error`（后端 `model.BotStatus`：pending/connecting/connected/error/stopped；`disconnected` 非后端真实状态，弃用）。
- **健康条诚实呈现**：摘要分组只给 `online`(=connected)+`total`，无法细分；故健康条为「在线 vs 其余」两段，其余含连接中/异常/已停止。详细分段待 FR-041 单 Bot 遥测。
- **分组维度查询一次取齐**：4 个 groupBy 摘要（instance/node/status/behavior）无条件并发查询，分组维度切换时直接切数据源，不触发条件 hook（满足 React Hooks 规则），并复用 instance/node 摘要算分布。
- **「在控制台打开」联动**：仅实例分组有该入口 → `useConsoleStore.openInstance(instanceId)` + `navigate('/')`，回到控制台并在工作区打开该实例。控制台工作区内的 per-instance Bot 段属 FR-039（未做），当前落点为该实例终端面板。
- **批量目标=筛选**：每组批量 / 顶部多选批量都通过 `groupFilter(dim, group, baseFilter)` 生成精确 `filter` 调 `useBotBatch`；多组用 `mutateAsync` 逐组下发并聚合计数（后端批量按单一 filter 收敛，多组需多次调用）。
- **压测入口占位**：顶部「压测」按钮 `disabled` + tooltip 指向 FR-042，不建会话 UI（范围外）。
- **维度切换复位**：`GroupOverview` 以 `key={groupBy}` 重挂自然复位 `expanded`/`selected`，避免 effect 内 `setState`（项目 react-hooks 规则）。

## 任务拆解

### 纯逻辑 + 单测
- [x] `bots-overview.ts`：`statusCounts` / `healthSegments` / `toListParams` / `groupFilter` / `distribution` + 枚举常量
- [x] `bots-overview.test.ts`：14 用例（状态计数/健康条分段含越界 clamp/筛选剔空/分组键叠加/分布计数）

### 页面组件
- [x] `BotsPage` 编排 + `useDebounced` + 4 个 groupBy 摘要查询
- [x] `SummaryCards` / `Toolbar` / `GroupOverview` / `GroupRow` / `HealthBar`
- [x] `GroupBatchMenu` / `BatchBar`（per-group + 多选聚合批量）
- [x] `GroupPeek` / `PeekRow`（分页窥视，只读）
- [x] `CreateBotDialog`（沿用旧表单 + `useCreateBot`）

### i18n
- [x] zh/en 新增 `bots.*` 键（概览/分布/工具栏/分组维度/状态/健康/批量/分页/压测入口）

### 验证
- [x] `tsc --noEmit` 通过（0 错误）
- [x] `eslint`（仅本批文件）0 错误；全量遗留错误均在未触达文件
- [x] `vite build` / `tsc -b` 通过
- [x] `vitest run bots-overview.test.ts` 14/14 通过

## 产出文件范围

| 文件 | 操作 |
|---|---|
| `web/src/pages/BotsPage.tsx` | 改写为聚合优先总览页 |
| `web/src/pages/bots-overview.ts` + `.test.ts` | 新增（纯逻辑 + 单测） |
| `web/src/i18n/{zh,en}.json` | 新增 `bots.*` 键 |
| `docs/specs/bots-overview/impl.md` | 本规格文档 |

## 不做（范围外，见 scope-discipline）

- 控制台工作区内 per-instance Bot 段（FR-039）
- 压测会话编排 UI（FR-042，仅占位入口）
- Bot 实时遥测 / 单 Bot 详情面板（FR-041，窥视行只读）
- 修改 `web/src/api/bots.ts` 任何 hook 或后端 Go

## 开放问题

- **「在控制台打开」落点**：FR-039 的 per-instance Bot 段尚未实现，当前 `openInstance` 落到该实例的终端面板（控制台工作区）。FR-039 落地后，TerminalPane 增「终端 | Bot」切换时可进一步把落点定向到 Bot 段，无需改本页联动逻辑。
- **健康条分段粒度**：受限于摘要分组仅 `online`+`total`，当前为两段。若后续需 connecting/error 分段，需扩展 FR-038 摘要（`byStatus` 下沉到分组级），属后端范围。
- **批量多组语义**：多选批量按组逐次调用后端（非单次原子），结果为各组聚合计数；如需单次大批量，可在后端支持「多 filter / id 并集」，属后端范围。
