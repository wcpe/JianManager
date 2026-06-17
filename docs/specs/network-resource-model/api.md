# API Spec — FR-032 节点资源分配与群组服关系模型

> 关联 FR: FR-032 | 优先级: P0 | 状态: 📋 todo

## 概述

FR-032 将实例角色化，并引入系统分配工作目录/端口、proxy↔backend M:N 注册和 Network 软标签。该 FR 是搭建 Bukkit 子服、搭建代理和复制子服的关系模型底座。

## REST API

### GET /api/v1/networks
- **描述**: 查询群组软标签列表。
- **权限**: `instance.read`

### POST /api/v1/networks
- **描述**: 创建群组软标签。
- **权限**: `instance.write`
- **请求**: `{ "name": "survival", "description": "生存群组" }`

### PUT /api/v1/networks/:id
- **描述**: 更新群组软标签。

### DELETE /api/v1/networks/:id
- **描述**: 删除群组软标签；不删除实例与代理注册关系。

### POST /api/v1/networks/:id/members
- **描述**: 将实例加入群组软标签。
- **请求**: `{ "instanceId": 12 }`

### DELETE /api/v1/networks/:id/members/:instanceId
- **描述**: 移除群组软标签成员。

### GET /api/v1/nodes/:id/ports
- **描述**: 查询节点端口池占用与可分配范围。
- **权限**: `node.read`

### POST /api/v1/proxies/:id/registrations
- **描述**: 将 backend 注册到 proxy。
- **权限**: `instance.write`
- **请求**:
```json
{ "backendInstanceId": 21, "alias": "lobby", "priority": 0, "forcedHost": "play.example.com", "restricted": false }
```

### GET /api/v1/proxies/:id/registrations
- **描述**: 查询 proxy 下的 backend 注册。

### DELETE /api/v1/proxies/:id/registrations/:registrationId
- **描述**: 删除注册关系。

## 实例 API 扩展

`POST /api/v1/instances` 增加：
```json
{ "role": "backend", "allocateWorkDir": true, "ports": { "server": 25565, "rcon": 25575, "query": 25585 } }
```

创建对话框不再要求用户输入 `workDir`；Control Plane/Worker 在 `servers_dir` 下分配 `servers/<name-slug>-<shortid>`，前端只读展示。

## 数据模型

- `Instance.role`: `proxy | backend | universal`
- `Instance.server_port / rcon_port / query_port`
- `Network`
- `NetworkMember`: Network M:N Instance，非独占软标签
- `ProxyRegistration`: Proxy Instance M:N Backend Instance，含 alias/priority/forcedHost/restricted

## 约束

- 同节点端口唯一。
- 工作目录由系统分配，不接受任意绝对路径覆盖。
- backend 可注册到多个 proxy。
- 删除 Network 不影响实例和注册关系。
