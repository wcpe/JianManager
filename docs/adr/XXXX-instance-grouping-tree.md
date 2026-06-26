# ADR-XXXX: 实例组织分组树（多级嵌套 + 实例-组 M:N）

> 占位编号 `XXXX`：批 3 并行含多个新 ADR，真号由 sdd-parallel-develop 落地时按入 main 顺序统一分配（当前 max=032）。

- **日期**: 2026-06-26
- **状态**: accepted（随 FR-165 落地）
- **上下文**: 实例规模化（1000+）需要**文件夹式的组织视图**用于人为归类、折叠、批量操作。系统已有两种语义不同的「组」，都不适配组织视图：
  - **用户组**（ADR-004，表 `groups`/`group_members`/`group_quota` + `group_instances`）：承载 RBAC 与配额，`group_instances` 是「用户组拥有哪些实例」，扁平、与权限耦合。
  - **网络/proxy 群组**（ADR-007，`networks`/`server_registrations`）：proxy↔backend 的 M:N 运行时拓扑软标签，是**部署关系**不是组织归类。
  复用任一都会把「组织归类」与「权限/部署」语义搅在一起。

## 决策

**新增独立的「实例组织分组树」——多级嵌套（自引用 `parent_id` 邻接表）+ 实例-组 M:N，与用户组、网络群组三者正交，仅 CP 读写。**

1. **两张新表**（CP 独有，守 ADR 数据所有权）：
   - `instance_group_nodes`：`id, uuid, name, parent_id(自引用,NULL=根), sort, timestamps, deleted_at`。邻接表表达树。
   - `instance_group_members`：`id, group_id, instance_id, created_at`，UNIQUE `(group_id, instance_id)`。一实例可属多组（M:N）。
2. **正交边界**：组织分组**不承载** RBAC / 配额（那是用户组）、**不表达** proxy↔backend（那是网络群组）。三者各管各的，互不复用表。
3. **树操作约束**：移动子树防环（不能移到自身子孙下）；删非空组默认**拒删**（提示先清空），不级联删实例（仅成员关系）。
4. **计数**：分组节点挂「子树聚合实例数」，M:N 下按实例**去重**计数。

## 理由
- 组织归类是独立关注点，正交于权限（ADR-004）与部署（ADR-007）；混用会让三种语义互相污染。
- 邻接表（`parent_id`）对「树不深、频繁增删改」足够，避免闭包表/嵌套集的写放大。
- M:N（实例可属多组）契合「一个实例既在『生存服』又在『亚洲区』」的真实归类。

## 后果
- 新增 `instance_group_nodes` + `instance_group_members` 两表 + migration；CP 加 `InstanceGroupService`（树 CRUD + 成员 + 防环 + 去重计数）+ 只读/写 API。
- 前端新增分组树（左）+ 复用批 2 列表骨架（右）+ 拖实例入组 + 按组筛选。
- 与 ADR-031（可寻址）协同：选中组 / 展开态可进 URL。

## 关系
- **ADR-004（用户组替代多租户）**：本树与用户组正交，不复用 `group_instances`、不加 `tenant_id`。
- **ADR-007（MC 群组服 M:N）**：本树与网络群组正交，组织归类 ≠ proxy↔backend 部署。
- **FR-165（实例多级嵌套分组）**：本 ADR 的落地 FR。
