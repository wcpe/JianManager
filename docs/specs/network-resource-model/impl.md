# 实施计划 — FR-032 节点资源分配与群组服关系模型

> 关联 FR: FR-032 | 优先级: P0 | 状态: ✅ done

## 现状（复用既有）

- ✅ 端口系统分配：`service/ports.go` `allocPortsForNode`（同节点唯一）
- ✅ 工作目录系统分配：`service/workdir.go` `allocWorkDirRel`（var/servers/<slug>-<shortid>）
- ✅ 已接入 `InstanceService.Create`（MC 实例自动分配 workDir）
- ❌ 缺：role 字段、server_registrations、networks/network_members、对应服务与路由

## 任务拆解

### Phase 1: 模型 + 迁移
- [ ] `model/instance.go`：新增 `Role InstanceRole`（backend/proxy/universal，默认 universal）
- [ ] `model/registration.go`：新增 `ServerRegistration`（proxyId/backendId/alias/priority/forcedHost/restricted/enabled），唯一索引 (proxyId, alias)
- [ ] `model/network.go`：新增 `Network` + `NetworkMember`，唯一索引 (networkId, instanceId)
- [ ] `database/database.go`：AutoMigrate 注册新模型

### Phase 2: 服务层
- [ ] `service/registration.go`：`RegistrationService` CRUD（List/Create/Update/Delete），角色校验、alias 唯一、重复注册校验。预留 `applyHook`（FR-035 注入写代理配置）
- [ ] `service/network.go`：`NetworkService` CRUD + 成员增删 + `BatchAction`（start/stop/restart 经 InstanceService）
- [ ] `service/ports.go`：补 `NodePortUsage(nodeID)` 返回占用列表 + 范围
- [ ] `service/instance.go`：`CreateInstanceRequest` 增 `Role`；Create 落 role；List 支持 role 过滤
- [ ] `service/provision.go`：bukkit provision 落 `role=backend`

### Phase 3: 路由
- [ ] `router/registration.go`：`RegistrationHandler`（/proxies/:id/registrations...）
- [ ] `router/network.go`：`NetworkHandler`（/networks...）
- [ ] `router/provision.go` 或 node：`GET /nodes/:id/ports`
- [ ] `router/router.go`：在 admin 组注册三个 handler

### Phase 4: 前端
- [ ] `CreateInstanceDialog.tsx`：workDir 改只读说明（系统分配）
- [ ] `api/networks.ts` + `api/registrations.ts` + `api/ports.ts`
- [ ] 群组（Network）视图：列表/创建/成员/按标签批量启停
- [ ] 节点端口占用展示
- [ ] i18n 中英同步

### Phase 5: 测试
- [ ] `registration_test.go`：角色校验、alias 冲突、重复注册、M:N
- [ ] `network_test.go`：CRUD、成员幂等、删除不影响注册、批量动作计数
- [ ] `ports_test.go`：补占用查询用例

## 产出文件范围

| 文件 | 操作 |
|---|---|
| `internal/controlplane/model/instance.go` | 修改（role） |
| `internal/controlplane/model/registration.go` | 新增 |
| `internal/controlplane/model/network.go` | 新增 |
| `internal/controlplane/database/database.go` | 修改（migrate） |
| `internal/controlplane/service/{registration,network}.go` | 新增 |
| `internal/controlplane/service/{ports,instance,provision}.go` | 修改 |
| `internal/controlplane/router/{registration,network}.go` | 新增 |
| `internal/controlplane/router/{router,provision}.go` | 修改 |
| `web/src/api/{networks,registrations,ports}.ts` | 新增 |
| `web/src/components/CreateInstanceDialog.tsx` | 修改 |

## 验收映射（FR-032）

| 验收标准 | 实现 |
|---|---|
| 实例具备 role | Instance.Role + 创建落值 |
| 工作目录系统分配、只读展示 | 复用 allocWorkDirRel + 前端只读 |
| 端口池可查看占用 | GET /nodes/:id/ports |
| proxy↔backend M:N，每注册含 alias/priority/forced-host/restricted | server_registrations |
| Network 非独占软标签，删除不影响子服与注册 | networks + 软删除不级联 |
| 群组视图按标签筛选 + 批量操作 | NetworkService.BatchAction + 前端 |
