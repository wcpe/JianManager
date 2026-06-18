# 实施计划 — FR-039 控制台实例内 Bot 管理段

> 关联 FR: FR-039 | 优先级: P1 | 状态: 🔨 in-progress | 关联 ADR: ADR-009 | 依赖: FR-037（控制台布局）, FR-038（Bot 规模化 API）

## 背景

控制台 Shell（FR-037）已落地：左栏实例树常驻，点实例在工作区开终端。FR-038 已合入 Bot 规模化 API（分页 `GET /bots`、聚合 `GET /bots/summary`、批量 `POST /bots/batch`）。本 FR 在工作区为单实例补「终端 | Bot」切换，并按 ADR-009「聚合优先、永不全量铺开」呈现 Bot：实例树挂聚合徽标、工作区 Bot 段用概览卡片 + 筛选/分组 + 分页 + 批量。

本批为**纯前端**，不动后端 Go，复用既有 `web/src/api/bots.ts` hook（无新增 hook）。

## 复用的后端 API（均 FR-038 已实现）

| Endpoint | 方法 | 用途 | 复用 hook |
|---|---|---|---|
| `/bots/summary?groupBy=instance[&nodeId=]` | GET | 实例树逐行 Bot 聚合徽标（在线/总数），单次覆盖可见集 | `useBotSummary({ groupBy:'instance', nodeId })` |
| `/bots/summary?instanceId=` | GET | Bot 段概览卡片（总计/在线/连接中/异常），全量聚合 | `useBotSummary({ instanceId })` |
| `/bots?instanceId=&page=&pageSize=&status=&behavior=&q=` | GET | Bot 段分页列表（不全量） | `useBots({ instanceId, page, pageSize, ...filters })` |
| `/bots/batch` | POST | 批量设行为/停止/删除（按 ids 或 filter） | `useBotBatch()` |
| `/bots/:id/behavior` | POST | 单 Bot 行内改行为 | `useSetBotBehavior()` |
| `/bots` | POST | 新建 Bot（实例预填、节点 host 预填地址） | `useCreateBot()` |
| `/bots/:id` | DELETE | 单 Bot 删除 | `useDeleteBot()` |

> `useBots`/`useBotSummary`/`useBotBatch` 由 FR-038 提供；本批未改 `web/src/api/bots.ts`。

## 组件拆解（全部位于 `web/src/components/console/`）

```
bot-list.ts            # 纯逻辑：状态语义分桶、概览计数派生、页内分组、配置解析、地址预填、徽标索引（可单测）
bot-list.test.ts       # vitest 单测（分桶/计数/分组/解析/预填）
BotStatusDot.tsx       # Bot 状态点（在线绿/连接中琥珀/异常红/离线空心）
InstanceBotBadge.tsx   # 实例树行内聚合徽标（在线/总数），total=0 不渲染
BotSegment.tsx         # Bot 段主体：概览卡片 + 工具栏 + 分组分页列表 + 批量条 + 单行 + 新建入口
CreateBotDialog.tsx    # 控制台版新建对话框（实例预填不可改、节点 host+25565 预填可改）
WorkspacePane.tsx      # 工作区单实例面板：面包屑 + 「终端 | Bot」分段切换
```

改写：
```
InstanceTree.tsx       # 单次 useBotSummary(groupBy=instance) → 每行挂 InstanceBotBadge
Workspace.tsx          # 打开实例时渲染 WorkspacePane（取代直接 TerminalPane）
TerminalPane.tsx       # 增 hideHeader：分段模式下由 WorkspacePane 承载头部，避免双重头部
stores/console.ts      # 增 workspaceSegmentByInstance（每实例「终端/Bot」记忆）+ setWorkspaceSegment
i18n/{zh,en}.json      # 新增 bots.*（概览/筛选/分组/批量/分页/statusKind）+ console.*（分段/徽标）
```

## 设计取舍（对齐 ADR-009 + 现有代码）

- **聚合优先**：概览卡片与实例树徽标的计数取自 `GET /bots/summary`（后端聚合，覆盖全量而非当前页），避免按分页数据低估在线数；列表用 `GET /bots` 分页（`pageSize=50`）。分组（`groupBots`）只作用于「当前页」已加载数据，不拉全量。
- **分段记忆**：每实例「终端/Bot」选择存 Zustand（`workspaceSegmentByInstance`，按实例 id 记忆），与 FR-037「客户端 UI 状态用 Zustand」一致，不进 URL，缺省终端。
- **状态语义分桶**：后端 status（connected/connecting/disconnected/error）映射为 online/connecting/error/offline 四类，驱动概览卡片语义色、状态点、按状态分组。未知值兜底 offline。
- **批量作用域**：有勾选 → 按 `ids`（选中集）；无勾选 → 按 `filter`（当前筛选集，跨所有分页），并在批量条提示影响范围，停止/删除二次确认。整组表头另带「设本组行为」按当前页该组 ids 批量。
- **新建预填**：实例 id 由工作区当前实例预填且锁定；连接地址用「实例所在节点 host + 25565」预填且可改（用户改过即以其输入为准，未改则跟随节点 host 解析刷新——用派生值而非 effect 同步，规避 `react-hooks/set-state-in-effect`）。FR-032 端口分配落地后可替换为实际 server-port。
- **头部归一**：分段切换 + 面包屑由 `WorkspacePane` 统一承载，`TerminalPane` 在此场景 `hideHeader` 只渲染终端区，避免双重工具栏。

## 任务拆解

- [x] `bot-list.ts` + `bot-list.test.ts`（纯逻辑 + 单测）
- [x] `stores/console.ts` 增每实例分段状态
- [x] `BotStatusDot.tsx` / `InstanceBotBadge.tsx`
- [x] `BotSegment.tsx`（概览卡片 + 工具栏 + 分组分页 + 批量条 + 单行）
- [x] `CreateBotDialog.tsx`（实例/地址预填）
- [x] `WorkspacePane.tsx`（终端 | Bot 切换）+ `Workspace.tsx`/`TerminalPane.tsx` 接线
- [x] `InstanceTree.tsx` 挂聚合徽标
- [x] zh/en i18n 新增 `bots.*` / `console.*` 键
- [x] `npx tsc --noEmit` / `eslint`（本批文件）/ `vite build` / `vitest` 通过

## 验证结果

- `tsc --noEmit`：通过（exit 0）
- `eslint`（FR-039 新增/改写文件）：通过（exit 0）；仓库既有文件的 lint 报错为存量，未在本批范围内修改
- `vite build`（含 `tsc -b`）：通过（exit 0）
- `vitest run`（bot-list + instance-tree）：17 通过

## 不做（范围外，见 scope-discipline）

- 全局 `/bots` 页重构（FR-040）——若复用本批 Bot 列表/摘要组件，组件已置于 `components/console/`，但不假设 FR-040 存在
- Bot 实时遥测/单 Bot 详情面板（FR-041）——本段 Bot 状态仅来自 REST，无血量/位置/聊天流
- 压测会话编排 UI（FR-042）
- 不修改 `web/src/api/bots.ts` 及任何后端 Go 代码

## 开放问题

- **server-port 预填**：当前用节点 host + 默认 25565；FR-032 端口分配落地后应改用实例实际 server-port（已在 `suggestBotServer` 注释标注）。
- **重连语义**：FR-039 验收单 Bot 行要求「设行为/停止/重连/删除」。后端批量动作枚举为 set-behavior/start/stop/delete，未单列「重连」。本批将「重连」映射为 `start`（重新上线），单行已提供 设行为（行内 Select）/重连/停止/删除四项。若后续需要「断开后重连」区别于「冷启动」语义，再由后端补动作。
