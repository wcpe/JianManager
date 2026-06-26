# 功能规格：FR-167 跨实例超级工作台

> 状态：草拟（主控自审）　·　关联 PRD：FR-167　·　依赖：**FR-166（已 master `a0400b3`）**、FR-163/164　·　批 3 第二批
> 关联 ADR：复用 `ADR-034`（可组合卡片工作区）——跨实例是其画布的作用域扩展，**不新增 ADR**。

## 1. 背景与目标
FR-166 已落「单实例可组合卡片画布」（`WorkspaceCanvas` + `stores/workspace.ts` + `workspace-card`/`workspace-preset`）。本 FR 把画布作用域从「限当前实例」扩展为**跨实例超级工作台**：任意实例的卡片自由拼合（如 4 个不同实例终端拼监看墙）+ 左侧「实例库」拖拽添加 + 作「集群」域独立入口。design §9。P2。

## 2. 需求（要什么）
### 范围内
- **跨实例画布**：卡片携带 `instanceId`，画布不再限单实例；同一画布可并存多个实例的卡（监看墙）。复用 FR-166 卡壳/拖拽/调size/全屏/惰性挂载/预设机制。
- **实例库面板**（左侧可收起）：搜索实例 + 实例可展开看其功能（终端/资源/插件/监控/Bot/状态 + JBIS）；**拖实例** = 加该实例默认卡组，**拖功能** = 加单卡，**多选批量拖** = 一次拼监看墙。HTML5 原生 DnD，放置区高亮 + 松手落位。
- **超级工作台入口**：「集群」域独立路由（如 `/super`）+ 侧栏「集群」组 nav 链接。
- **跨实例预设**：命名保存跨实例画布布局（个人级 localStorage，复用 `workspace-preset` 序列化，扩展为携 instanceId 的卡）。

### 不做（范围外）
- 导播台多预设预热 / 瞬切 / 定时轮播 / 并发上限 / 非激活降频（**FR-168**）。
- 后端预设同步。

## 3. 设计（怎么做）
- **扩 `stores/workspace.ts`**：卡模型已含/补 `instanceId`；新增「超级工作台」作用域（与 FR-166 单实例作用域并存，单实例 = 限当前 id 的视图，超级 = 不限）。预设序列化（`lib/workspace-preset.ts`）扩为携 `instanceId`（向后兼容单实例预设）。纯逻辑（跨实例卡去重、拖拽 payload 解析）下沉 `.ts` + vitest。
- **新增 `InstanceLibrary` 组件**：搜索 + 实例列表（展开看功能）+ 拖拽源（拖实例/功能/多选）。复用既有 `useInstances`、`workspace-card` 目录。
- **路由 + 入口**：`Workspace.tsx` 加 `<Route path="super">`（指向 `SuperWorkbenchPage` 或复用 `WorkspaceCanvas` 的超级作用域）；`ConsoleSidebar`「集群」组加「超级工作台」链接（design §7 已把它列入集群域）。
- **复用 FR-166**：`WorkspaceCanvas`/`WorkspaceCard`/`WorkspaceCardBody`/`WorkspaceToolbar` 尽量复用；卡片渲染按 `instanceId` 取数（终端 WS / metrics 等已按实例）。
- i18n 中/英；视觉复用批 2/FR-166 卡壳 + 双主题 token。
- 复用 `ADR-034`，不新增 ADR；如发现需推翻 034 决策再停下报告。

## 4. 任务拆分
- [ ] 测试先行：跨实例预设/拖拽 payload 纯逻辑（`workspace-preset` 扩 instanceId、`lib/instance-library` 拖拽解析）红→绿
- [ ] 扩 `stores/workspace.ts` 跨实例作用域 + 卡携 instanceId
- [ ] `InstanceLibrary` 面板（搜索 + 展开功能 + 拖实例/功能/多选）
- [ ] 超级工作台路由 `/super` + 侧栏「集群」组入口
- [ ] 跨实例预设保存/切换（localStorage）
- [ ] `docs/ARCHITECTURE.md`（工作区跨实例段）+ i18n 中/英
- [ ] 前端 `tsc/lint/vitest/build` 全绿 + 真机

## 5. 验收标准
- 同一画布并存多实例卡（4 个不同实例终端拼监看墙稳定）；实例库拖实例/拖功能/多选批量拖均落位。
- 超级工作台从「集群」域入口可达；跨实例预设保存 + 切换还原（localStorage 持久）。
- 复用 FR-166 卡壳/惰性挂载（未在画布的卡不建 WS）；i18n 中/英；暗/亮 + 双主题正常。
- 前端 `tsc/lint/vitest/build` 全绿。**真机由用户确认**（4 终端监看墙 + 实例库拖拽 + 跨实例预设）。

## 6. 风险 / 待定
- 多实例终端 WS 并存的连接数（本 FR 仅「同时打开多个」，**并发上限/非激活降频留 FR-168**）；本 FR 沿用 FR-166 惰性挂载（未在画布不建 WS）。
- 路由 `/super` 与单实例 `/instances/:id` 画布共用 `WorkspaceCanvas`，作用域区分要清晰（store 里区分 scope）。
