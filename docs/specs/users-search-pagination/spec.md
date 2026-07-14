# 功能规格：用户目录服务端搜索分页 + 候选下拉有界渲染

> 状态：草拟　·　关联 PRD：FR-336（增强 FR-003/156）　·　分支：feature/fr-336-users-search-pagination（待建）

## 1. 背景与目标

2026-07-15 验收分诊发现：用户相关链路在用户量上千时前后端全线无界。

- 后端 `GET /users` 无任何参数，`UserService.List()`（`internal/controlplane/service/user.go`）`db.Find` 全量返回，路由层（`internal/controlplane/router/user.go` `List`）直接映射为裸数组；
- 前端 `useUsers()`（`apps/control-plane-web/src/api/users.ts`）全量拉取，三处消费：用户目录页 `UsersPage.tsx`、审计页用户筛选 `AuditPage.tsx`、群组成员弹窗 `GroupMembersDialog.tsx`；
- 共享 Combobox（`packages/ui/src/components/combobox.tsx` 列表渲染段）对 `filtered` 平铺渲染全量选项，且 `filterOptions`（`packages/ui/src/lib/combobox.ts`）空查询返回全部选项——千级候选时弹窗一次挂载上千 DOM 节点；
- `GroupMembersDialog` 候选来自 `useUsers()` 全量再前端过滤。

目标：`GET /users` 支持服务端搜索 + 分页（兼容旧无参调用），共享 Combobox 渲染有界化，群组成员弹窗候选改服务端搜索驱动，用户目录页接入搜索与分页。

## 2. 需求（要什么）

- 后端 `GET /users` 增可选 query：`q`（用户名模糊）、`limit`、`offset`；**带 `limit` 时**返回分页信封 `{items,total,limit,offset}`，**不带时**保持旧裸数组（`q` 两种形态下均生效），旧调用方零改动不破。
- 共享 Combobox 渲染有界：过滤结果超上限（100）只渲染前 100 项 + 底部「还有 N 项，继续输入缩小范围」提示行；不为 packages/ui 引入虚拟化依赖。
- `GroupMembersDialog`：候选默认只显前 N（50）条 + 键入 debounce（300ms）走服务端 `q` 搜索；已选成员恒显（来自 `group.members`，与候选来源解耦）。
- `UsersPage` 用户目录：搜索框（服务端 `q`）+「共 N 条」+「加载更多」（limit 增长式，见 §3.4）。
- devmock `packages/devmock/src/handlers/domains/identity.ts` 的 `GET /users` 同步镜像 `q/limit/offset` 双形态契约。
- 范围内：上述后端/共享组件/两个消费页/devmock/i18n 键/单测与 DOM 测试。
- 不做（范围外）：
  - AuditPage 的用户筛选迁移（仍用旧裸数组全量；千级用户下该页的治理另记 FR，见 §6）；
  - Combobox 真虚拟化（@tanstack/react-virtual 或复用 app 侧 `useVirtualRows`）——量级需要时再做；
  - 用户表字段/权限模型变更；`GET /users` 权限收放（维持平台管理员路由组）。

## 3. 设计（怎么做）

### 3.1 后端（internal/controlplane/service/user.go + router/user.go）

service 层把 `List()` 改为带筛选：

```go
// UserListFilter 用户列表筛选（FR-336）。零值不限制。
type UserListFilter struct {
    Q      string // username 模糊（LIKE %q%，转义 %/_）
    Limit  int    // <=0 = 不分页返回全部
    Offset int    // 仅 Limit>0 时生效
}
// List 返回 (items, total, error)；total 与 Q 条件同源（Count 同 WHERE）。
func (s *UserService) List(f UserListFilter) ([]model.User, int64, error)
```

- 排序：统一显式 `ORDER BY username ASC, id ASC`（分页稳定必需；两形态一致，旧消费方仅展示顺序变化）。
- router `List`：解析 `q/limit/offset`；**以「请求是否携带 `limit` 参数」分流响应形态**——带则信封，缺则裸数组（兼容 FR-002 既有契约）。`limit` 钳制 [1,500]，`offset` 负值归 0，非法整数回 400 `INVALID_REQUEST`。items 投影字段与现状一致（`id/uuid/username/role/status/createdAt`）。
- 双形态取舍（拍板）：**推荐可选参数双形态**，否决「全量切信封+前端一次性迁移」——`/users` 有 3 个消费方（UsersPage/AuditPage/GroupMembersDialog），AuditPage 迁移非本 FR 目标；双形态在 router 层一个 `if` 的成本，换取旧调用方零风险。全站分页约定统一时可再收口。

### 3.2 共享 Combobox 有界渲染（packages/ui）

- `packages/ui/src/lib/combobox.ts` 新增纯函数（可单测）：

```ts
export const COMBOBOX_RENDER_LIMIT = 100
/** 过滤结果截前 limit 项渲染，返回隐藏数供提示行。 */
export function visibleOptions(filtered: ComboboxOption[], limit = COMBOBOX_RENDER_LIMIT):
  { visible: ComboboxOption[]; hiddenCount: number }
```

- `packages/ui/src/components/combobox.tsx`：列表渲染 `visible`；`hiddenCount > 0` 时列表底部渲染非交互提示行 `t('combobox.moreOptions', { count })`（i18n 键落在消费 app：`apps/control-plane-web/src/i18n/{zh,en}.json` 的 `combobox` 段，样式同 `noResults` 灰字）。
- 新增可选受控查询回调 `onQueryChange?: (q: string) => void`（输入变化与展开重置时触发），供服务端搜索场景（§3.3）监听内部搜索框；不传则行为完全不变。
- 拍板：**先 slice 上限 100 + 提示**，不为单组件给 packages/ui 引 @tanstack/react-virtual；app 侧 `apps/control-plane-web/src/lib/virtual-list.ts`（`useVirtualRows`）在 app 包内、packages/ui 不得反向依赖。后续量级需要再虚拟化（另记）。

### 3.3 GroupMembersDialog（apps/control-plane-web/src/components/GroupMembersDialog.tsx）

- `api/users.ts` 新增信封 hook（`useUsers()` 原样保留给旧消费方）：

```ts
export interface UserPage { items: UserInfo[]; total: number; limit: number; offset: number }
export function useUserSearch(params: { q?: string; limit?: number; offset?: number })
// queryKey: ['users', 'search', params]；GET /users?q=&limit=&offset=
```

- 弹窗内：本地 `kw` 状态 → `useDebounced(kw, 300)`（把 `BotsPage.tsx` 内的 `useDebounced` 抽到 `apps/control-plane-web/src/lib/use-debounced.ts` 共用，BotsPage 同步改引——本 FR 依赖该抽取，属允许的阻塞性变更）→ `useUserSearch({ q, limit: 50 })`。
- 候选 = `items` 过滤掉已是成员者；Combobox `options` 即该服务端子集（客户端 `filterOptions` 对子集再过滤无害）；经 `onQueryChange` 把 Combobox 内输入回传为 `kw`。
- 已选成员列表数据源不变（`group.members` 自带 `user.username`），与候选加载解耦、恒显。
- 候选被服务端截断时（`total > items.length`）在 Combobox 下方小字提示「已显示前 N / 共 total，键入缩小范围」（i18n `groups.candidateHint`）。

### 3.4 UsersPage（apps/control-plane-web/src/pages/UsersPage.tsx）

- 顶部加搜索框（`useDebounced` 300ms → 服务端 `q`）；改用 `useUserSearch({ q, limit })`。
- `limit` 初始 50，「加载更多」`+50`（offset 恒 0 的增长式单查询，同 AuditPage「加载更多扩大 pageSize」范式）；`q` 变化时 `limit` 回初始值；列表上方展示「共 total 条」。

### 3.5 devmock（packages/devmock/src/handlers/domains/identity.ts）

`GET /users` handler 镜像后端契约：解析 `q`（`username.toLowerCase().includes`）；带 `limit` → `{ items: rows.slice(offset, offset + limit), total: rows.length, limit, offset }`，缺 `limit` → 裸数组；排序 username 升序。同文件 `/audit` 已有双形态先例可对照。DOM 测试可经 `db('users')` 注入千级种子。

## 4. 任务拆分

- [ ] service：`UserListFilter` + `List(f)` 返回 (items,total)，q 转义、统一排序；service/router 单测（`router/user_test.go` 扩展）
- [ ] router：`q/limit/offset` 解析、双形态分流、钳制与 400 语义
- [ ] packages/ui：`visibleOptions` 纯函数 + Combobox 有界渲染 + `onQueryChange` prop；lib 单测 + 组件 DOM 测试（千级选项 DOM 有界）
- [ ] i18n：`combobox.moreOptions`、`groups.candidateHint`（zh/en；ui-museum 如渲染 Combobox 同步补键）
- [ ] 前端 api：`UserPage`/`useUserSearch`；抽 `useDebounced` 到 `lib/use-debounced.ts`（BotsPage 改引）
- [ ] GroupMembersDialog：服务端搜索候选 + 截断提示 + 已选恒显；DOM 测试（千级用户有界/搜索命中/增删成员回归）
- [ ] UsersPage：搜索框 + 共 N 条 + 加载更多；DOM 测试
- [ ] devmock identity.ts：`GET /users` 双形态镜像
- [ ] 文档同步：PRD 状态、API.md（`GET /api/v1/users` 段落）、CHANGELOG

## 5. 验收标准

- Go 单测：`q` 模糊命中；`limit/offset` 翻页窗口正确且 `total` 与条件同源；无 `limit` 时返回裸数组（含带 `q`）；`limit` 钳制 [1,500]、非法参数 400。
- vitest：`visibleOptions` ≤100 + hiddenCount 正确；Combobox 传入 1000 选项时列表 DOM 子节点 ≤ 100 + 1 提示行。
- vitest DOM（devmock 千级用户种子）：GroupMembersDialog 打开后候选请求带 `limit=50`、弹窗 DOM 节点有界；键入关键字 debounce 后仅渲染服务端命中项；已选成员恒显；添加/移除成员不回归。
- vitest DOM：UsersPage 搜索命中、「共 N 条」正确、「加载更多」逐窗扩大、搜索变化重置窗口。
- 旧调用方不破：AuditPage 用户筛选下拉照常（裸数组）；既有全部 users 相关测试保持绿。
- 真机（需用户确认）：真实 CP 上验证用户目录搜索 + 群组成员弹窗键入搜索命中；旧版前端/脚本无参调用 `GET /users` 行为不变。

## 6. 风险 / 待定

- **双形态分流以「是否带 `limit`」为准**：调用方带 `limit` 却期望裸数组会拿到信封——API.md 写明；若后续全站统一分页约定（见 FR-337 §6 同议题），此端点可随批收口为信封-only。
- **LIKE 大小写语义跨库不一致**（SQLite 默认 ASCII 不敏感、MySQL 依 collation）：验收只承诺「子串命中」，不承诺大小写语义统一；如需统一再加 `LOWER()` 归一（性能代价另评）。
- **Combobox 上限 100 是拍板值**：不引虚拟化依赖的前提下的折中；候选超 100 依赖「继续输入缩小范围」。需要滚动看全量的场景出现时再上虚拟化（届时评估把 `useVirtualRows` 下沉 packages/ui 或引 @tanstack/react-virtual）。
- **AuditPage 仍全量拉 `/users`**：千级用户下审计页同样有界化需求——超出本 FR，交付时记新 FR（📋）。
- **packages/ui 的 i18n 键宿主**：键实际落在消费 app；ui-museum 等其他消费方若渲染 Combobox 需各自补键，缺键时 i18next 回退键名（可接受的降级）。
- **权限不变待确认**：`GET /users` 维持平台管理员路由组（`router.go` admin 组 `RequireRole`）；群组管理员场景的候选选择（FR-003 语义）如需放开另行拍板，本 FR 不动权限。
