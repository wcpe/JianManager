# API Reference — JianManager

> 本文档始终反映当前 API 状态，原地更新。每个 endpoint 标注关联的 FR。

---

## 认证

### GET /api/v1/setup/status
- **描述**: 查询系统是否需要初始化（首次启动引导）
- **关联 FR**: FR-017
- **权限**: 无需认证
- **响应** (200):
  ```json
  { "setupRequired": true }
  ```

### POST /api/v1/setup
- **描述**: 创建初始管理员账号（仅首次启动可用）
- **关联 FR**: FR-017
- **权限**: 无需认证，setupRequired=true 时可用
- **请求**:
  ```json
  { "username": "string", "password": "string" }
  ```
- **响应** (201):
  ```json
  { "accessToken": "string", "refreshToken": "string", "expiresIn": 900 }
  ```
- **错误**: 409 管理员已存在 | 400 参数校验失败

### POST /api/v1/auth/register
- **描述**: 用户注册
- **关联 FR**: FR-001
- **请求**:
  ```json
  { "username": "string", "password": "string" }
  ```
- **响应** (201):
  ```json
  { "id": "uuid", "username": "string", "createdAt": "datetime" }
  ```
- **错误**: 409 username 已存在

### POST /api/v1/auth/login
- **描述**: 用户登录
- **关联 FR**: FR-001
- **请求**:
  ```json
  { "username": "string", "password": "string" }
  ```
- **响应** (200):
  ```json
  { "accessToken": "string", "refreshToken": "string", "expiresIn": 900 }
  ```
- **错误**: 401 用户名或密码错误

### POST /api/v1/auth/refresh
- **描述**: 刷新 Access Token
- **关联 FR**: FR-001
- **请求**:
  ```json
  { "refreshToken": "string" }
  ```
- **响应** (200):
  ```json
  { "accessToken": "string", "refreshToken": "string", "expiresIn": 900 }
  ```
- **错误**: 401 refreshToken 无效或已过期

---

## 用户

### GET /api/v1/users
- **描述**: 用户列表（平台管理员）
- **关联 FR**: FR-002
- **权限**: `user.read`
- **响应** (200): `[{ id, uuid, username, role, status, createdAt }]`

### GET /api/v1/users/:id
- **描述**: 用户详情
- **关联 FR**: FR-002
- **权限**: `user.read`

### PUT /api/v1/users/:id
- **描述**: 更新用户（角色/状态）
- **关联 FR**: FR-002
- **权限**: `user.write`

### DELETE /api/v1/users/:id
- **描述**: 删除用户
- **关联 FR**: FR-002
- **权限**: `user.delete`

---

## 用户组

### GET /api/v1/groups
- **描述**: 用户组列表
- **关联 FR**: FR-003
- **权限**: `group.read`

### POST /api/v1/groups
- **描述**: 创建用户组
- **关联 FR**: FR-003
- **权限**: `group.create`
- **请求**:
  ```json
  { "name": "string", "description": "string" }
  ```

### GET /api/v1/groups/:id
- **描述**: 用户组详情（含成员列表和配额）
- **关联 FR**: FR-003

### PUT /api/v1/groups/:id
- **描述**: 更新用户组
- **关联 FR**: FR-003
- **权限**: `group.write`

### DELETE /api/v1/groups/:id
- **描述**: 删除用户组
- **关联 FR**: FR-003
- **权限**: `group.delete`

### POST /api/v1/groups/:id/members
- **描述**: 添加组成员
- **关联 FR**: FR-003
- **请求**: `{ "userId": "int", "role": 0 }`

### DELETE /api/v1/groups/:id/members/:userId
- **描述**: 移除组成员
- **关联 FR**: FR-003

### PUT /api/v1/groups/:id/quota
- **描述**: 更新组配额（平台管理员）
- **关联 FR**: FR-003
- **请求**: `{ "maxInstances": 10, "maxBots": 50, "maxStorageMb": 10240 }`

### GET /api/v1/groups/:id/quota
- **描述**: 查询组配额及当前用量（组成员可查看本组，组管理员/平台管理员同）
- **关联 FR**: FR-003
- **权限**: `group:quota:read`（本组可访问）
- **响应**:
  ```json
  {
    "groupId": 1,
    "maxInstances": 10,
    "maxBots": 50,
    "maxStorageMb": 10240,
    "usedInstances": 3,
    "usedBots": 15,
    "usedStorageMb": 2100
  }
  ```
- **错误**: 404 用户组不存在或无权访问

---

## 节点

### GET /api/v1/nodes
- **描述**: 节点列表
- **关联 FR**: FR-004
- **权限**: `node.read`
- **响应**: `[{ id, uuid, name, host, status, os, cpuCores, memoryMb, lastHeartbeat }]`

### GET /api/v1/nodes/:id
- **描述**: 节点详情（含资源使用率）
- **关联 FR**: FR-004

### DELETE /api/v1/nodes/:id
- **描述**: 删除节点（离线时）
- **关联 FR**: FR-004
- **权限**: `node.delete`

### GET /api/v1/nodes/:id/metrics
- **描述**: 节点指标（CPU/内存/磁盘时间序列）
- **关联 FR**: FR-010

---

## 实例

### GET /api/v1/instances
- **描述**: 实例列表（按当前用户权限过滤）
- **关联 FR**: FR-005
- **权限**: `instance.read`
- **Query**: `?nodeId=xxx&groupId=yyy&status=RUNNING`

### POST /api/v1/instances
- **描述**: 创建实例
- **关联 FR**: FR-005
- **权限**: `instance.create`
- **请求**:
  ```json
  {
    "nodeId": 1,
    "name": "Survival Server",
    "type": "minecraft_java",
    "processType": "daemon",
    "startCommand": "java -Xmx2G -jar paper.jar nogui",
    "workDir": "/servers/survival",
    "autoStart": false,
    "autoRestart": true,
    "groupId": 1
  }
  ```

### GET /api/v1/instances/:id
- **描述**: 实例详情
- **关联 FR**: FR-005

### PUT /api/v1/instances/:id
- **描述**: 更新实例配置
- **关联 FR**: FR-005
- **权限**: `instance.write`

### DELETE /api/v1/instances/:id
- **描述**: 删除实例（需先停止）
- **关联 FR**: FR-005
- **权限**: `instance.delete`

### POST /api/v1/instances/:id/start
- **描述**: 启动实例
- **关联 FR**: FR-005
- **权限**: `instance.operate`

### POST /api/v1/instances/:id/stop
- **描述**: 停止实例
- **关联 FR**: FR-005
- **权限**: `instance.operate`

### POST /api/v1/instances/:id/restart
- **描述**: 重启实例
- **关联 FR**: FR-005
- **权限**: `instance.operate`

### POST /api/v1/instances/:id/kill
- **描述**: 强制终止实例
- **关联 FR**: FR-005
- **权限**: `instance.operate`

### POST /api/v1/instances/:id/command
- **描述**: 向实例发送命令
- **关联 FR**: FR-005
- **请求**: `{ "command": "say hello" }`

### GET /api/v1/instances/:id/metrics
- **描述**: 实例指标（TPS/玩家/内存）
- **关联 FR**: FR-010

---

## 终端

### GET /api/v1/instances/:id/terminal-token
- **描述**: 签发一次性终端连接 token
- **关联 FR**: FR-007
- **权限**: `instance.terminal`
- **Query**: `?permission=write` 或 `?permission=read`
- **响应**:
  ```json
  {
    "token": "one-time-token",
    "wsUrl": "ws://worker-node:9101/ws/terminal?token=xxx",
    "expiresIn": 30
  }
  ```

---

## 文件管理

### GET /api/v1/instances/:id/files
- **描述**: 文件列表
- **关联 FR**: FR-008
- **权限**: `instance.file`
- **Query**: `?path=/plugins`

### GET /api/v1/instances/:id/files/read
- **描述**: 读取文件内容
- **关联 FR**: FR-008
- **Query**: `?path=plugins/essentials/config.yml`

### POST /api/v1/instances/:id/files/write
- **描述**: 写入文件内容
- **关联 FR**: FR-008
- **请求**: `{ "path": "string", "content": "string" }`

### POST /api/v1/instances/:id/files/upload
- **描述**: 文件上传（multipart）
- **关联 FR**: FR-008

### GET /api/v1/instances/:id/files/download
- **描述**: 文件下载（流式）
- **关联 FR**: FR-008
- **Query**: `?path=plugins/essentials/config.yml`

### DELETE /api/v1/instances/:id/files
- **描述**: 删除文件
- **关联 FR**: FR-008
- **请求**: `{ "path": "string" }`

### POST /api/v1/instances/:id/files/rename
- **描述**: 重命名文件
- **关联 FR**: FR-008
- **请求**: `{ "oldPath": "string", "newPath": "string" }`

---

## Bot

### GET /api/v1/bots
- **描述**: Bot 列表
- **关联 FR**: FR-009
- **权限**: `bot.read`
- **Query**: `?instanceId=xxx&status=connected`

### POST /api/v1/bots
- **描述**: 创建 Bot
- **关联 FR**: FR-009
- **权限**: `bot.create`
- **请求**:
  ```json
  {
    "instanceId": 1,
    "name": "GuardBot",
    "config": {
      "server": "mc.example.com",
      "port": 25565,
      "auth": "offline"
    },
    "behavior": "guard"
  }
  ```

### DELETE /api/v1/bots/:id
- **描述**: 删除 Bot
- **关联 FR**: FR-009

### GET /api/v1/bots/:id
- **描述**: Bot 详情（位置/血量/背包等）
- **关联 FR**: FR-009

### POST /api/v1/bots/:id/behavior
- **描述**: 切换 Bot 行为模式
- **关联 FR**: FR-009
- **请求**: `{ "behavior": "follow", "target": "PlayerName" }`

### POST /api/v1/bots/:id/command
- **描述**: 向 Bot 发送命令
- **关联 FR**: FR-009
- **请求**: `{ "command": "/tp 0 64 0" }`

### POST /api/v1/bots/stress-test
- **描述**: 创建压测会话
- **关联 FR**: FR-009
- **请求**:
  ```json
  {
    "instanceId": 1,
    "count": 50,
    "config": { "server": "mc.example.com", "port": 25565, "auth": "offline" }
  }
  ```

---

## 定时任务

### GET /api/v1/schedules
- **描述**: 定时任务列表
- **关联 FR**: FR-012
- **Query**: `?instanceId=xxx`

### POST /api/v1/schedules
- **描述**: 创建定时任务
- **关联 FR**: FR-012
- **请求**:
  ```json
  {
    "instanceId": 1,
    "name": "Daily Restart",
    "cronExpr": "0 4 * * *",
    "action": "restart"
  }
  ```

### PUT /api/v1/schedules/:id
- **描述**: 更新定时任务
- **关联 FR**: FR-012

### DELETE /api/v1/schedules/:id
- **描述**: 删除定时任务
- **关联 FR**: FR-012

---

## 备份

### GET /api/v1/instances/:id/backups
- **描述**: 实例备份列表
- **关联 FR**: FR-013

### POST /api/v1/instances/:id/backups
- **描述**: 创建备份
- **关联 FR**: FR-013
- **请求**: `{ "name": "string" }`

### POST /api/v1/backups/:id/restore
- **描述**: 恢复备份
- **关联 FR**: FR-013

### DELETE /api/v1/backups/:id
- **描述**: 删除备份
- **关联 FR**: FR-013

---

## 告警

### GET /api/v1/alerts/rules
- **描述**: 告警规则列表
- **关联 FR**: FR-011

### POST /api/v1/alerts/rules
- **描述**: 创建告警规则
- **关联 FR**: FR-011
- **请求**:
  ```json
  {
    "name": "High CPU",
    "targetType": "node",
    "targetId": null,
    "metric": "cpu",
    "operator": ">",
    "threshold": 90,
    "durationSec": 60,
    "notifyType": "webhook",
    "notifyTarget": "https://hooks.example.com/xxx"
  }
  ```

### PUT /api/v1/alerts/rules/:id
- **描述**: 更新告警规则
- **关联 FR**: FR-011

### DELETE /api/v1/alerts/rules/:id
- **描述**: 删除告警规则
- **关联 FR**: FR-011

### GET /api/v1/alerts/events
- **描述**: 告警事件列表
- **关联 FR**: FR-011
- **Query**: `?ruleId=xxx&resolved=false`

---

## 模板

### GET /api/v1/templates
- **描述**: 服务端模板列表
- **关联 FR**: FR-014

### POST /api/v1/templates
- **描述**: 创建模板（平台管理员）
- **关联 FR**: FR-014

---

## 审计日志

### GET /api/v1/audit
- **描述**: 审计日志列表（平台管理员）
- **关联 FR**: FR-015
- **Query**: `?userId=xxx&action=instance.start&from=xxx&to=yyy`

---

## 错误码

| HTTP | 含义 |
|---|---|
| 400 | 请求参数错误 |
| 401 | 未认证或 token 无效 |
| 403 | 无权限 |
| 404 | 资源不存在 |
| 409 | 资源冲突（如用户名已存在） |
| 422 | 业务逻辑错误（如配额超限） |
| 429 | 请求过于频繁 |
| 500 | 服务器内部错误 |

错误响应格式：
```json
{
  "error": "QUOTA_EXCEEDED",
  "message": "组配额已满：最大实例数 10",
  "details": { "maxInstances": 10, "currentInstances": 10 }
}
```
