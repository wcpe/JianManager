# 功能规格：FR-165 实例多级嵌套分组

> 状态：草拟（待审核）　·　关联 PRD：FR-165　·　依赖：FR-163/164（已 master）　·　批 3 第一批 / worktree A
> 关联 ADR：`ADR-033`（实例组织分组树，占位号，落地统一分配）

## 1. 背景与目标
实例规模化（1000+）需要**文件夹式的组织视图**。现有两种「组」都不适配：用户组（ADR-004，配额/RBAC）、网络群组（ADR-007，proxy↔backend 软标签）。本 FR 新增**实例组织分组树**（多级嵌套 + 实例可属多组），与上述二者正交。design §4.4：左树 + 右列表。P2。

## 2. 需求（要什么）
### 范围内
- **后端分组树模型**：自引用 `parent_id` 邻接表（`instance_group_nodes`）+ 实例-组 **M:N**（`instance_group_members`）。CRUD：建组 / 嵌套子组 / 改名 / 删除（级联或拒删非空，见设计）/ 移动（改 parent）/ 排序。
- **前端左树**：分组树（新建组 / 嵌套子组 / 折叠优先——折叠只渲染分组头 / 选中），每节点挂**实例计数**（含子孙聚合）。
- **前端右列表**：复用批 2 列表骨架（`ViewToggle`/工作台卡）显示**选中组**的实例 + 面包屑（组路径）+ **批量「标记入组」**（多选实例 → 加入某组）。
- **拖实例入组**：从右列表拖实例到左树某组 = 加入该组。
- **按组筛选**：`InstancesPage` 既有「分组」selector 接通分组树（按组/子树过滤）。
- 实例属多组（M:N）；删组不删实例（仅解绑成员关系）。

### 不做（范围外）
- 工作区卡片画布 / 实例库拖拽到画布（FR-166/167）。
- 不动用户组（ADR-004）/ 网络群组（ADR-007）；不复用 `group_instances`（那是用户组↔实例）。
- 分组级权限 / 配额（组织视图不承载 RBAC）。

## 3. 设计（怎么做）
- **数据模型**（CP 独有，ADR-033）：
  - `instance_group_nodes`：`id, uuid, name, parent_id(自引用,可空=根), sort, created_at, updated_at, deleted_at`；索引 `parent_id`。
  - `instance_group_members`：`id, group_id, instance_id, created_at`；UNIQUE `(group_id, instance_id)`，索引各列。
  - 删非空组：默认**拒删**（有子组或成员时），或显式 `cascade` 参数（spec 选拒删 + 提示先清空，避免误删）。移动子树防环（不能移到自己子孙下）。
- **后端**：`model` + GORM migration（database.go AutoMigrate）+ `InstanceGroupService`（树 CRUD + 成员增删 + 防环 + 计数聚合）+ API endpoints（见下）。纯逻辑（防环 `wouldCreateCycle`、子树计数 `subtreeCounts`）下沉可测。
- **API**（`docs/API.md` 同步）：
  - `GET /instance-groups`（返回树 + 各组成员计数）
  - `POST /instance-groups`（`{name, parentId?}`，`instance.manage`/`group`权限）
  - `PUT /instance-groups/:id`（改名 / 移动 parent，防环）
  - `DELETE /instance-groups/:id`（非空拒删）
  - `POST /instance-groups/:id/members` / `DELETE …/members`（批量加/移除实例）
- **前端**：`InstanceGroupTree` 组件（左树，复用既有 `InstanceTree` 折叠/选中模式）+ 右列表（批 2 工作台卡 + 面包屑 + 批量标记）+ 拖拽（HTML5 DnD，轻量，不引第三方）+ `api/instanceGroups.ts`。按组筛选接 `InstancesPage`。i18n 中/英。
- 引用 `ADR-033`（实例组织分组树），勿在此重复 ADR 正文。

## 4. 任务拆分
- [ ] 写 ADR-033（实例组织分组树）
- [ ] 后端：model + migration + `InstanceGroupService`（防环/计数纯函数 + 测试红→绿）+ API endpoints + 权限
- [ ] `docs/API.md` + `docs/ARCHITECTURE.md`（数据模型）同步
- [ ] 前端：`api/instanceGroups.ts` + `InstanceGroupTree` + 右列表（批量标记/面包屑）+ 拖实例入组 + `InstancesPage` 按组筛选
- [ ] i18n 中/英；PRD FR-165 → 开发中（worktree 内）
- [ ] 后端 `go build/vet/test`、前端 `tsc/lint/vitest/build` 全绿 + 真机

## 5. 验收标准
- 建组 / 嵌套子组 / 改名 / 移动（防环）/ 删非空被拒；拖实例入组；批量标记入组；按组（含子树）筛选实例。
- 后端分组树 CRUD + 成员 M:N，删组不删实例；防环与子树计数有单测。
- **1000+ 实例、深嵌套折叠不卡**（折叠只渲染分组头）。
- i18n 中/英；暗/亮 + 双主题正常。**真机由用户确认**（建组/嵌套/拖入/批量/筛选 + 后端持久）。

## 6. 风险 / 待定
- 实例属多组（M:N）下「按组筛选」的去重 + 计数聚合语义（子树聚合是否去重同一实例）——spec 取**去重计数**。
- 删非空组策略：取**拒删 + 提示**（不级联，防误删）；如需级联留参数。
