# 功能规格：控制台六域导航 IA + 可配置权限树

> 状态：✅ 已交付@v0.22.0　·　关联 PRD：**FR-431 / FR-432**　·　关联 ADR：**089**
> UI 原型 `preview.html` / 根目录 `index.html`：**gitignore，禁止入库**
>
> **范围澄清**：FR-431 六域侧栏与可见性；FR-432 权限树 / 角色模板 / 用户覆盖 / API 与 handler 门禁 / `/permissions` 页。  
> **不含**客户端分发页 Tab 结构与处置交互重构（那是 **FR-430**，见 `docs/specs/client-dist-ia-merge/spec.md`）。

## 1. 背景与目标

侧栏「平台管理」过载（业务 5 节 + 管理员 2 节，展开约 22 叶子）、分组轴不统一（服务器域混实例/玩家/Bot/节点/工作台）、服务端模板误入「内容与分发」、`/alerts` 有路由无导航、分发页对非管理员仍出现在侧栏但路由有守卫。

权限仅有全局角色枚举 0 组成员 / 1 组管理员 / 10 平台管理员，无法表达「只读分析 / 运维 / 管理」等岗位，也无法按角色模板或单用户配置能力。

产品定位（已确认）：**单平台多用户**，不做 SaaS / 租户 / `tenant_id`；沿用 ADR-004 用户组隔离，本次把 **能力权限树 + 角色模板 + 用户覆盖 + 导航可见性** 做清楚。

## 2. 需求（要什么）

### 2.1 导航 IA（FR-431）

- 六域顶层（业务 **URL 全部不变**）：
  1. 平台首页 `/`
  2. 服务器：`/instances` `/nodes` `/players` `/bots`
  3. 群组网络：`/networks/topology` `/networks`
  4. 工作台：`/super` `/director`
  5. 观测：`/monitor` `/logs` `/statistics` `/notifications`
  6. 客户端分发：`/client-channels` `/client-dist-ops`（按权限节点可见）
  7. 平台设置分节：身份与权限（含新 `/permissions`）/ 任务与定时 / 存储与备份 / 内容模板（自旧「内容与分发」迁出）/ 审计与设置 / Agent 接入 / 系统维护
- `/templates` 迁到平台设置；`/notifications` 主入口仅在观测；`/alerts` **维持无导航入口**（不删路由）。
- 侧栏可见性与 API 鉴权共用同一套权限节点（不再只靠 `role===10`）。
- breadcrumb 父级映射与六域一致。

### 2.2 可配置权限树（FR-432）

- **静态权限目录**（代码内能力域树，约 50 节点）：`platform` / `runtime` / `observability` / `distribution` / `agent` / `workspace`；受保护 API 必须映射到节点。
- **存储**：`roles` · `role_permissions` · `user_role_bindings` · `user_permission_overrides`。
- **生效**：`effective = 角色模板节点 ⊕ 用户覆盖`；**deny 永胜**；`users.role==10` 短路全开且不可被覆盖降权。
- **预置模板**（可改节点、系统 key 不可删）：`platform_admin` / `group_admin` / `group_operator` / `group_viewer` / `member`。
- **组级隔离不变**：实例/文件/终端等仍叠加用户组 ↔ 实例过滤（`CanAccessInstance` 等）。
- **后端**：`PermissionService`；`RequireAnyPerm` / handler `requireNodes`；`GET /api/v1/auth/me` 返回 `nodes[]`+`roleKey`；RBAC 管理 API `/api/v1/rbac/*`。
- **前端**：`hasPerm`；`navGroupsForPermissions`；`/permissions` 三栏（对象 / 树编辑 / 实时生效预览）；树上 Shift 连选、Ctrl 多选、整行可点。

### 范围内

- 权限目录 + 迁移 + seed；鉴权与 RBAC API；六域 nav-config；`/permissions` 页；i18n 中英；文档（ADR + PRD + ARCHITECTURE + API）。

### 不做（范围外）

- 真多租户 / Organization / `tenant_id`（不推翻 ADR-004）。
- 按单实例 ACL；组内细粒度勾选开关（非角色枚举）。
- 删除 `users.role` 列（保留兼容位）。
- 实例控制台在途批 FR-412~424 UI 重做。
- 一次做完全站写按钮 disabled（首批：实例主操作、终端输入、文件写、RBAC/用户写）。

## 3. 设计（怎么做）

### 3.1 权限节点（示意目录，实现以种子文件为准）

```text
platform: user.* group.* node.* settings.* system.* license.read audit.read rbac.*
runtime:  instance.* file.* terminal.access bot.* backup.* schedule.* task.*
observability: monitor.read log.read stats.read alert.* notification.read
distribution: template.* channel.* dist.publish dist.ops.*
agent:    agent.token.* agent.mcp.read agent.calllog.read
workspace: network.* player.* super.read director.read
```

实例级隔离仍由用户组决定，权限树只回答「能力允不允许」。

### 3.2 前端 `/permissions` 交互契约

- 左栏：角色模板 + 用户；中栏：权限树（整行可点、域全开/全关、搜索）；右栏：实时生效预览（节点数、chips、用户覆盖、侧栏将显示）。
- 选角色 → 勾选改模板；选用户 → 勒选写 allow/deny 覆盖。
- 快捷键：单击切换 · Shift 连选 · Ctrl/⌘ 多选 · Ctrl/⌘+A 全选可见 · Esc 清范围高亮。
- 原型见本目录 `preview.html`。

### 3.3 架构决策

实现前须新 ADR：**可配置权限树与六域导航 IA**（说明：保留 ADR-004 用户组模型；`users.role` 降为兼容位；有效权限算法 deny 永胜）。不在本 spec 重复 ADR 正文。

## 4. 任务拆分

- [x] T1 权限目录 + DB 迁移 + seed + effective 算法单测
- [x] T2 PermissionService + RequireAnyPerm/requireNodes + `/api/v1/auth/me` 扩展
- [x] T3 RBAC 管理 API + 集成测试
- [x] T4 前端权限 store + 六域 `nav-config` + 测试（URL 不变）
- [x] T5 `/permissions` 页（对象/树/实时预览 + 快捷键）
- [x] T6 只读/运维写操作门禁首批
- [x] T7 文档：ADR-089 + PRD FR-431/432 + ARCHITECTURE + API.md + CHANGELOG
- [x] T8 回归：go test + vitest + tsc/eslint（见 Report）

## Report

**What was built** — FR-431 六域导航 IA + FR-432 可配置权限树：后端权限目录/角色模板/用户覆盖/RequirePerm/RBAC API；前端 nav 按节点裁剪、`/permissions` 三栏编辑页、写操作门禁首批。

**Verification** — 本地：`go test` service Permission/Authz + router CrossGroupIsolation/TestRBAC PASS；`go vet`+`build` PASS；前端 `tsc -b`/eslint PASS；vitest nav-config/breadcrumb/permissions/PermissionsPage/ConsoleSidebar/UsersPage PASS。

**线上验收（2026-09-18，生产 CP）** — 部署 linux-amd64 二进制并重启服务后，平台管理员登录：六域侧栏正确（服务器/群组网络/工作台/观测/客户端分发/平台设置；`/templates` 在平台设置；无 `/alerts` 入口；`/permissions` 在身份与权限）；`GET /auth/me`、`/rbac/catalog`、`/rbac/roles` 200，系统五模板 seed；权限页选「组只读分析」树可编辑、节点计数实时变化、保存写回 API。修复验收中发现：①无绑定用户 effective 不读库内模板；②启动 seed 覆盖已改系统模板节点。创建 只读测试用户(role=3)：`roleKey=group_viewer`、14 只读节点、侧栏无工作台/分发/权限配置/节点；`GET /rbac/roles`、`POST /users` 均为 403。

**线上验收（2026-09-18）** — `.tmp/perm-acceptance.py` 对 **57/57** 权限节点全量回归：
- 空权限用户 nodes=0，业务 API 全 403；
- 每节点单独授予后 `/auth/me` 含该节点，代表 API 非 403（57 PASS / 0 FAIL）；
- **超级管理员唯一**：二次创建 role=10 → 403；降级唯一超管 → 403；清空 platform_admin 模板仍返回 57 节点 + locked；deny 覆盖 / 绑非超管模板 → 403。
- 本地单测：`super_admin_test.go`、`permission_gate_test.go`、`permission_catalog_test.go` 全绿。

**三层门禁（实现契约）** — ① 路由 `RequireAnyPerm`（能否进路由族）→ ② handler `requireNodes` 写/危险操作（`instance.operate|delete`、`file.write`、`node.manage`、`backup.write`…）→ ③ 用户组 `CanAccessInstance` 等资源隔离。仅有 `*.read` 的角色不得完成写操作。详见 `docs/ARCHITECTURE.md` §4.1。

**评审修复** — 第一轮 R-01~R-08（node 读写拆分、实例/文件/定时/模板写门禁、组列表、超管模板短路）；第二轮 R2-01~03（Kill/Command/Update、备份全族+范围、配置 Rollback/读路径）+ R-07b；第三轮 🟡（错误脱敏、覆盖事务、CreateRole key、ErrRoleSystem、RoleNodes 错误、CrossCheck 读门禁、备份死代码、ARCHITECTURE 三层表述）。回归：`write_gate_test.go`、`permission_hardening_test.go` 绿；线上 57/57 节点 + viewer 写路径 403。

**评审修复（第四轮 F-01~F-07）** — `requirePlatformAdmin` 语义纠正为 `node.manage`（不再=node.read）；用户/设置/网络/告警/备份存储/JDK 等平台域写路径补写节点；分发发布 `Publish*`=`channel.write|dist.publish`、IP 规则与安全处置写=`dist.ops.write`、频道管理写与读分离（F-07）；UsersPage 写操作改 `hasPerm('user.manage')`；JDK/制品/存储只读改 `node.read|node.manage`。回归：`platform_write_gate_test.go` + 既有 WriteGate/RBAC 全绿；线上 viewer 对上述写 API 全 403。

**评审修复（第五轮 F5-01~F5-04）** — Network AddMembers/RemoveMember/Actions 与 Registration 写路径挂 `network.manage`；组成员增删叠加 `group.member.write`（deny 永胜生效）；前端 permissions store 测试与「未加载拒绝」实现对齐；OverviewPage 平台观测区改 `hasPerm(monitor.read|node.read)`；JDK/制品/存储只读放宽；恢复 embed `dist/.gitignore`。

**未提交** — 按用户此前要求，本地 git 仍未 commit。

## 5. 验收标准

1. 权限树节点与 API 路由映射可追溯；effective 算法单测覆盖 deny 永胜与平台管理员短路。
2. 六域侧栏与 `flatNavItems` 一致；非授权域不渲染；业务 path 与既有测试中的 URL 不变。
3. `/permissions`：角色树勾选保存、复制系统模板、用户覆盖、effective 预览；树上 Shift/Ctrl 行为与原型一致。
4. `group_viewer` 对实例主操作/终端输入/文件写不可用（隐藏或 disabled 且可测）。
5. i18n 中英完整；`/alerts` 仍无导航入口；分发/系统页可见性与权限节点一致。

## 6. 风险 / 待定

- 存量页面多处硬编码 `role === 10`：允许随触达替换，不要求一次全站扫，但 **新代码必须走 `ROLE_*` / `hasPerm`**。
- 组成员（0）与组运维（2）在实例操作上能力重叠：本期保持 0 兼容现状；语义收敛可后续 FR。
- 本地 git 历史清理：`docs/compose/` 为 compose-next 非正规路径，已迁出；历史重写范围见清理说明（仅未 push 提交）。
