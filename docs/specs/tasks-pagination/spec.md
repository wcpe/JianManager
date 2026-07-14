# 功能规格：任务中心列表分页信封

> 状态：草拟　·　关联 PRD：FR-337（增强 FR-183）　·　分支：feature/fr-337-tasks-pagination（待建）

## 1. 背景与目标

2026-07-15 验收分诊发现：任务中心列表在规模化下既看不到全量、也一次全挂。

- 后端 `TaskService.List`（`internal/controlplane/service/task.go`）`Order("created_at DESC, id DESC").Limit(limit)` 固定截前 limit（默认 100）返回裸 `[]model.Task`，无 Count、无 offset——第 101 条起永远不可见；
- 路由层（`internal/controlplane/router/task.go` `List`）解析 `kind/state/nodeId/keyword/since/until/limit` 后直返数组；
- 前端 `useTasks`（`apps/control-plane-web/src/api/tasks.ts`）直收数组，`TasksPage.tsx` 全量渲染、无总数无翻页；页眉任务下拉（FR-327 `TasksMenu`，`apps/control-plane-web/src/components/console/ConsoleHeader.tsx`）同吃该数组切前 8 条；
- FR-329 已有轮询：`tasksRefetchInterval` 在存在非终态任务时 2s 短轮询、全部终态停；
- devmock `packages/devmock/src/handlers/domains/observ.ts` `GET /tasks` 镜像 `slice(0, limit)` 裸数组。

目标：`GET /tasks` 切换为分页信封 `{items,total,limit,offset}`，前端可见总数、可加载更多，且不破坏 FR-329 轮询语义与 FR-327 页眉下拉。

## 2. 需求（要什么）

- 后端 `GET /tasks` 返回 `{items,total,limit,offset}`：`total` 与筛选条件（含归属隔离）同源；支持 `offset`；`limit` 缺省 100、钳制 [1,500]。
- **响应形态变更属破坏性，拍板直接切信封**（不做双形态）：前端 `useTasks` 全部消费点、devmock、测试同一变更内迁移（消费点清单见 §3.3）。
- 前端任务中心页：顶部「共 N 条」、底部「加载更多」（增长式窗口，保持既有筛选参数；筛选变化时窗口复位）。
- 保 FR-329 轮询语义：轮询刷新**已加载的整个窗口**（推荐方案，见 §3.2 拍板），启停规则（任一非终态→2s，全部终态→停）不变。
- 页眉任务下拉（FR-327）行为不回归：入口计数/平均进度/最近 8 条/`?task=` 深链。
- devmock `observ.ts` `GET /tasks` 同步信封。
- 范围内：上述后端/前端/devmock/测试/文档。
- 不做（范围外）：
  - `GET /tasks/:taskId`、`POST /tasks/:taskId/cancel` 不变；
  - 任务日志分页（TaskLog 仍整批返回）；
  - `until` 筛选的 UI 暴露（router 已支持，UI 无此项，维持现状）；
  - 跳页式页码 UI（offset 已在 API 支持，Web 端本期用增长式窗口）。

## 3. 设计（怎么做）

### 3.1 后端（internal/controlplane/service/task.go + router/task.go）

- `TaskListFilter` 增 `Offset int`；`List` 签名改为：

```go
// List 列出任务（FR-183/227/337）。返回 (items, total, error)；
// total 为同筛选（含归属隔离）的命中总数。倒序 created_at DESC, id DESC（既有稳定序）。
func (s *TaskService) List(access *UserAccess, f TaskListFilter) ([]model.Task, int64, error)
```

- 实现要点：筛选条件构建后**先 `Count(&total)` 再 `Order/Limit/Offset/Find`**（GORM 上用克隆/`Session` 避免 Count 与 Find 子句互染）；`limit <= 0 → 100`（既有缺省），上限 500；`offset < 0 → 0`。归属隔离（非平台管理员 `created_by = ?`）对 Count 与 Find 同源生效。
- router `List`：增解析 `offset`；响应 `gin.H{"items": tasks, "limit": effLimit, "offset": offset, "total": total}`（`limit` 回显钳制后生效值）。其余筛选参数解析不变。
- 信封键拍板：用 `{items,total,limit,offset}` 而非 FR-247 实例搜索的 `{items,total,page,pageSize}`——`/tasks` 既有参数就是 `limit`（FR-329/页眉复用中），且 Web 端为「加载更多」增长窗口（offset 恒 0），limit/offset 更贴語义；共同键 `items/total` 与全站一致（alerts events、notifications feed 同款）。见 §6 待定。

### 3.2 轮询与「加载更多」语义（拍板）

**推荐并采用：单查询增长 limit（offset 恒 0）**——沿用本仓 AuditPage「加载更多 = 扩大 pageSize」既有范式：

- `TasksPage` 持 `limit` 状态（初始 100）；「加载更多」`+100`，封顶 500（到顶且 `total > 500` 时提示用筛选缩小范围）；任一筛选变化时 `limit` 复位 100。
- TanStack Query 单查询 `['tasks', {filters, limit}]`，FR-329 轮询天然重取整个已加载窗口：所有已加载行的进度都会刷新，新建任务自然出现在顶部，`total` 同步更新，**零额外轮询逻辑**。
- 取舍说明（被否方案）：
  - **真 offset 追加分页**（每页独立查询追加渲染）：任务列表按创建时间倒序且持续有新任务插入头部，offset 页窗会漂移（重复/漏行）；轮询要么只刷首页（深页进度冻结）、要么 N 个页窗各自轮询（请求放大）。正确性和复杂度都劣于增长窗口。
  - **重取首页 + 「有新任务」提示**：需要新增提示态与手动合并交互，且首页外的进行中任务进度不刷新，UX 更差。
- 成本界定：深加载后每 2s 重取 ≤500 行——行数有上限、查询走既有索引序，可接受；超出 500 的浏览诉求引导筛选（kind/state/node/时间）收窄。

### 3.3 前端迁移（破坏性同批改）

`useTasks` 消费点 grep 清单（全量）：

| 消费点 | 改法 |
|---|---|
| `apps/control-plane-web/src/api/tasks.ts` `useTasks` | 返回 `TaskPage` 信封；`TaskListParams` 增 `offset?`；`refetchInterval: (q) => tasksRefetchInterval(q.state.data?.items)` |
| `apps/control-plane-web/src/pages/TasksPage.tsx` | `page.data?.items`；「共 N 条 · 已加载 M」+「加载更多」+ 筛选复位 limit |
| `apps/control-plane-web/src/components/console/ConsoleHeader.tsx` `TasksMenu`（FR-327） | `const tasks = data?.items ?? []`，其余（slice 8 / active 计数 / avg 进度 / 深链）不变 |
| `apps/control-plane-web/src/api/tasks.polling.dom.test.tsx` | mock 响应改信封 |
| `apps/control-plane-web/src/pages/TasksPage.dom.test.tsx` | 断言随 devmock 信封更新 |
| `packages/devmock/src/handlers/domains/observ.ts` `GET /tasks` | 见 §3.4 |

```ts
/** GET /tasks 分页信封（FR-337）。 */
export interface TaskPage { items: Task[]; total: number; limit: number; offset: number }
```

- `tasksRefetchInterval(tasks)` 纯函数签名不动（仍收 `readonly Pick<Task,'state'>[] | undefined`），调用方传 `data?.items`——既有启停单测原样保绿。
- `useTask`（单任务详情）不受影响。

### 3.4 devmock（packages/devmock/src/handlers/domains/observ.ts）

`GET /tasks`：既有筛选链后改为

```ts
const offset = Math.max(0, Number(url.searchParams.get('offset') ?? '0'))
return HttpResponse.json({ items: items.slice(offset, offset + limit), total: items.length, limit, offset })
```

（`limit` 同后端钳制 [1,500]、缺省 100。）

## 4. 任务拆分

- [ ] service：`TaskListFilter.Offset` + `List` 返回 (items,total)；Count/Find 同源；单测（`service/task_test.go` 扩展：total 同筛选、归属隔离下 total 只数本人、offset 窗口、limit 钳制）
- [ ] router：解析 `offset`、信封响应；router 侧测试更新
- [ ] 前端 `api/tasks.ts`：`TaskPage` + `useTasks` 信封化 + 轮询接线（`data.items`）
- [ ] `TasksPage`：共 N 条 / 加载更多 / 筛选复位 / 封顶提示；DOM 测试
- [ ] `ConsoleHeader.TasksMenu`：消费形态迁移（行为不变）；页眉下拉 DOM 回归断言
- [ ] devmock `observ.ts`：信封镜像
- [ ] 既有测试迁移：`tasks.polling.dom.test.tsx`、`TasksPage.dom.test.tsx`
- [ ] 文档同步：PRD 状态、API.md（`GET /api/v1/tasks` 段落）、CHANGELOG（标注破坏性响应形态变更）

## 5. 验收标准

- Go 单测：同筛选 `total` 正确（kind/state/nodeId/keyword/since/until 各维 + 组合）；非平台管理员 `total` 只统计自己发起的；`offset` 翻页窗口正确、越界 offset 返回空 `items` 且 `total` 不变；`limit` 缺省 100、钳制 [1,500]。
- vitest：`useTasks` 信封消费；`tasksRefetchInterval` 启停既有单测保持绿（非终态→2s、全终态→停，轮询不空转）；TasksPage「共 N 条」与已加载数正确、「加载更多」逐窗扩大且保持筛选参数、筛选变化复位窗口；devmock 数百任务种子下翻页与总数断言。
- 页眉下拉不回归：入口 active 计数/平均进度、最近 8 条、点击 `?task=` 深链跳转（DOM 断言）。
- 真机（需用户确认）：数百任务的环境翻页可达底、总数与筛选联动正确、有在跑任务时 2s 轮询刷新进度、全部终态后网络面板无 `/tasks` 空转请求。

## 6. 风险 / 待定

- **破坏性响应形态**：一次切换无过渡，§3.3 清单覆盖仓内全部消费点；仓外脚本若直连 `/tasks` 吃裸数组会破——平台未承诺外部 API 稳定性，CHANGELOG 显式标注 breaking。
- **信封键约定分叉**：FR-247（instances/search、bots）用 `{items,total,page,pageSize}`，本 FR 与 FR-336 用 `{items,total,limit,offset}`——PRD 行「对齐 FR-247 分页约定」实指共用 `items/total` 信封习惯；如要求全站键名统一（page/pageSize），须在编码前拍板（本 FR 改法局部，代价小）。
- **增长窗口轮询成本**：深加载（≤500 行）每 2s 重取整窗——已界定可接受；若未来上限上调需重新评估（届时才考虑真 offset 分页 + 新任务提示的复杂方案）。
- **同秒并发建任务的排序稳定性**：既有 `created_at DESC, id DESC` 已兜底，无新增风险；仅在此确认分页依赖它。
- **页眉下拉 active 计数口径**：维持现状（首窗 ≤100/limit 内统计，非全库 running 数）——与改前一致不回归；如需精确全局 active 数，另加计数端点（超范围）。
