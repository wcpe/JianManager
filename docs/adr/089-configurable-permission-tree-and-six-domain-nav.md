# ADR-089: 可配置权限树与六域导航 IA

- **日期**: 2026-09-17
- **状态**: accepted
- **关联**: FR-431 / FR-432 · [ADR-004](004-user-group-over-multitenant.md)（不推翻）· spec `docs/specs/nav-ia-role-model/`

## 上下文

侧栏「平台管理」过载、分组轴不统一；全局角色枚举（0/1/10）无法表达「只读分析 / 运维 / 管理」等岗位，也无法按角色模板或单用户配置能力。产品定位为单平台多用户，不做 SaaS/租户。

## 决策

1. **静态权限目录**：代码内能力域树（`platform`/`runtime`/`observability`/`distribution`/`agent`/`workspace`），节点形如 `domain.action`。受保护 API 映射到节点；实例级隔离仍由用户组（ADR-004）决定。
2. **存储**：`roles`（角色模板）· `role_permissions` · `user_role_bindings` · `user_permission_overrides`。
3. **生效算法**：`effective = 角色模板节点 ⊕ 用户覆盖`；**deny 永胜**；`users.role==10` 或模板 `platform_admin` **短路全开**，用户覆盖不可降权。
4. **兼容位**：`users.role` 保留。无绑定时映射系统模板 key（10→platform_admin，1→group_admin，2→group_operator，3→group_viewer，0→member）。
5. **鉴权**：`RequirePerm(node)` + `UserAccess.HasNode`；`GET /auth/me` 返回 `nodes[]`/`roleKey`；RBAC 管理 API `/api/v1/rbac/*`。
6. **导航六域**：首页 / 服务器 / 群组网络 / 工作台 / 观测 / 客户端分发 / 平台设置；业务 URL 不变；侧栏可见性与 API 共用权限节点。

## 理由

- 单平台多用户下，可配置权限树即可支撑多岗位，无需引入 tenant_id（ADR-004 成本论仍成立）。
- 导航与 API 共用节点，消除「侧栏能看见但接口 403」的漂移。
- deny 永胜 + 平台管理员不可降权，避免覆盖配置把管理员锁死。

## 后果

- 新增四张表与 seed；启动时幂等写入系统角色模板。
- 存量 `role===10` 前端判断仍可用，但新代码应走 `hasPerm` / `ROLE_*`。
- 组成员（0）与组运维（2）在实例操作上能力重叠：本期保持 0 兼容现状。

## 备选

- 仅扩展角色枚举、不做权限树：无法按用户微调，导航仍与 API 脱节。
- 真多租户 Organization：推翻 ADR-004，成本过高，本期明确不做。
