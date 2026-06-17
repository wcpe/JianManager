# 实施计划 — FR-032 节点资源分配与群组服关系模型

> 关联 FR: FR-032 | 优先级: P0 | 状态: 🔨 in-progress

## 任务拆解

### Phase 1: 模型
- [ ] 扩展 `Instance`：role、serverPort、queryPort、系统分配 workDir 标记。
- [ ] 新增 `Network` / `NetworkMember`。
- [ ] 新增 `ProxyRegistration`。
- [ ] 新增端口占用查询模型或服务。

### Phase 2: 资源分配
- [ ] Worker/CP 读取 `servers_dir` 配置。
- [ ] 创建实例时生成 slug + shortid 工作目录。
- [ ] 实现同节点端口池分配和冲突检测。
- [ ] 创建实例后把分配结果写回数据库。

### Phase 3: REST 路由
- [ ] 新增 `network` service/router。
- [ ] 新增 `registration` service/router。
- [ ] 新增 `GET /nodes/:id/ports`。
- [ ] 调整实例创建逻辑，不再要求前端传入 workDir。

### Phase 4: 前端
- [ ] 创建实例对话框移除 workDir 输入，改成只读提示。
- [ ] 增加 role 选择和端口占用展示。
- [ ] 增加 Network 视图筛选和批量操作入口。

## 产出文件范围

| 文件 | 操作 | 说明 |
|---|---|---|
| `internal/controlplane/model/instance.go` | 修改 | role/port 字段 |
| `internal/controlplane/model/network.go` | 新增 | Network 软标签 |
| `internal/controlplane/model/registration.go` | 新增 | proxy-backend 注册 |
| `internal/controlplane/service/instance.go` | 修改 | 资源分配 |
| `internal/controlplane/service/network.go` | 新增 | 群组软标签 |
| `internal/controlplane/service/registration.go` | 新增 | 注册关系 |
| `internal/controlplane/router/network.go` | 新增 | Network API |
| `internal/controlplane/router/registration.go` | 新增 | Registration API |
| `web/src/components/CreateInstanceDialog.tsx` | 修改 | UI 调整 |

## 风险

- 与 FR-031 共享配置写入能力，注册关系写代理配置应依赖配置引擎。
- 与 FR-034/035 共享实例创建路径，需要先保持最小稳定字段。
