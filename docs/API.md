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

### POST /api/v1/nodes/:id/maintenance
- **描述**: 置/解节点维护模式（cordon）。维护中拒绝新实例调度到该节点，与在线/离线状态正交
- **关联 FR**: FR-048
- **权限**: 平台管理员
- **请求**: `{ enabled: bool }`
- **响应**: 更新后的节点对象（含 `maintenance`）
- **审计**: `node.maintenance`

### POST /api/v1/nodes/:id/drain
- **描述**: 排空节点——停止其上所有 RUNNING 实例（复用实例停止 gRPC，不做迁移）。STARTING 为瞬态不强停
- **关联 FR**: FR-048
- **权限**: 平台管理员（危险操作，前端二次确认）
- **响应**: `{ stoppedCount, stopped: [id], failed: [id], errors?: [string] }`
- **审计**: `node.drain`

### DELETE /api/v1/nodes/:id
- **描述**: 主动下线节点：解除注册并保留记录（软删除），复连需重新注册。节点在线时拒绝（422）
- **关联 FR**: FR-004, FR-048
- **权限**: 平台管理员（危险操作，前端二次确认）
- **审计**: `node.delete`

### GET /api/v1/nodes/:id/metrics
- **描述**: 节点指标（CPU/内存/磁盘时间序列）
- **关联 FR**: FR-010

---

## 实例

### GET /api/v1/instances
- **描述**: 实例列表（按当前用户权限过滤）
- **关联 FR**: FR-005, FR-047
- **权限**: `instance.read`
- **Query**（多维筛选，任意组合，AND）:
  - `nodeId` 节点 ID
  - `groupId` 用户组 ID（非平台管理员忽略，强制按可访问组过滤）
  - `status` 状态（`RUNNING` 等）
  - `role` 角色（`backend`/`proxy`/`universal`）
  - `networkId` 群组（Network 软标签）ID（FR-047）
  - `env` 环境维度（`dev`/`test`/`prod`，对应 `env:` 前缀标签，FR-047）
  - `tag` 单个自由标签精确匹配（FR-047）
- **示例**: `?nodeId=1&networkId=2&env=prod&tag=survival&status=RUNNING`

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
- **关联 FR**: FR-005, FR-047
- **权限**: `instance.write`
- **请求**（字段均可选，缺省/`null` 表示不变）:
  ```json
  {
    "name": "Survival",
    "startCommand": "java -jar paper.jar nogui",
    "autoStart": true,
    "autoRestart": true,
    "jdkId": 3,
    "envVars": { "TZ": "Asia/Shanghai" },
    "tags": ["env:prod", "survival"]
  }
  ```
- **说明**: `tags` 传数组（含空数组 `[]` 清空）覆盖标签；环境维度复用 `env:` 前缀（FR-047），无独立字段。

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

### POST /api/v1/instances/batch
- **描述**: 按 id 列表或筛选条件批量执行操作，CP 侧信号量分片有界并发经 gRPC 委托对应 Worker（复用既有 per-instance RPC），返回成功/失败/跳过计数（FR-058）
- **关联 FR**: FR-058
- **权限**: `instance:operate`（资源级按可访问实例隔离）
- **请求**:
  ```json
  {
    "action": "command",
    "ids": [1, 2, 3],
    "filter": { "nodeId": 2, "status": "RUNNING", "role": "backend" },
    "command": "say hello"
  }
  ```
  - `action` ∈ `command` | `start` | `stop` | `restart` | `kill`
  - 目标二选一：`ids` 或 `filter`（皆空 → 400；同时给出以 `ids` 为准）
  - `command`：`action=command` 时必填；目标上限 5000（超出 → 400）
  - 动作映射（复用既有 per-instance RPC）：`command`→SendCommand（仅对 RUNNING 实例）、`start/stop/restart/kill`→Start/Stop/Restart/KillInstance
  - 生命周期动作委托结果回写终态，失败回写 CRASHED；`command` 不改实例状态
- **响应**:
  ```json
  {
    "action": "command",
    "requested": 3,
    "succeeded": 2,
    "failed": 1,
    "skipped": 0,
    "errors": [ { "instanceId": 3, "error": "Worker node-x 未连接" } ]
  }
  ```
  - `skipped`：请求 `ids` 中越权/不存在被静默剔除的数量（存在性隐藏）
  - `failed` 仅统计 Worker 委托结果；危险操作（批量 kill/stop）前端二次确认，服务端经审计中间件留痕（`instance.batch`）
- **错误**: 400 `INVALID_REQUEST`（action 非法 / 目标皆空 / command 缺 command / 超上限）；403 `FORBIDDEN`

### GET /api/v1/instances/:id/metrics
- **描述**: 实例指标。优先经 ServerProbe `/metrics` 取富指标（探针未部署/抓取失败时回退 RCON+RSS）
- **关联 FR**: FR-010
- **响应**:
  ```json
  {
    "tps": 20.03,
    "onlinePlayers": 7,
    "memoryMb": 391,
    "msptMillis": 0.60,
    "threads": 59,
    "cpuPercent": 7.9,
    "heapMaxMb": 2048,
    "uptimeSeconds": 112.7,
    "worlds": [{"name":"world","loadedChunks":49,"entities":84,"tileEntities":2}],
    "probeAvailable": true
  }
  ```
  `probeAvailable=false` 时富指标为零值，调用方仅展示 tps/onlinePlayers/memoryMb 与提示「未安装 ServerProbe 探针」。

### GET /api/v1/metrics/series
- **描述**: 节点/实例历史曲线。Worker 心跳上报节点指标 + 每实例 ServerProbe 快照，CP 分级降采样持久化（raw 48h / 5m 30d / 1h ≥1y，ADR-013），按区间自动选档返回
- **关联 FR**: FR-060 ｜ **关联 ADR**: ADR-013, ADR-014
- **权限**: 登录；`scope=node` 对认证用户开放，`scope=instance` 按 `instance.read` 收敛（越权 403）
- **Query**: `scope`(node\|instance) 必填；`targetId` 必填（node_uuid 或 instance_uuid）；`metrics` 可选（逗号分隔指标键；`scope=instance` 含 `world_*` 时按 world 维度返回多序列）；`range`(1h\|6h\|24h\|7d\|30d\|90d) 或 `from`/`to`(RFC3339)；`resolution`(auto\|raw\|5m\|1h，默认 auto)
- **响应**:
  ```json
  {
    "resolution": "5m",
    "from": "2026-06-20T00:00:00Z",
    "to": "2026-06-21T00:00:00Z",
    "series": [
      { "metricKey": "inst_tps", "unit": "tps", "world": "",
        "points": [ { "ts": "2026-06-20T00:05:00Z", "avg": 19.8, "min": 14.2, "max": 20.0 } ] },
      { "metricKey": "world_entities", "unit": "count", "world": "world_nether",
        "points": [ { "ts": "2026-06-20T00:05:00Z", "avg": 312, "min": 300, "max": 333 } ] }
    ]
  }
  ```
  raw 档 `points` 的 `avg/min/max` 同为样本值，缺测（探针不可用）`avg:null` 渲染为断点。
- **错误**: 400 `INVALID_SCOPE`/`INVALID_RANGE`/`INVALID_RESOLUTION`；403 `FORBIDDEN`；404 `TARGET_NOT_FOUND`

### GET /api/v1/metrics/overview
- **描述**: 总览页跨节点聚合：当前总量 + 聚合历史曲线（总 CPU 均值 / 总内存合计 / 总在线玩家合计）
- **关联 FR**: FR-060
- **权限**: 登录（仅聚合总量与曲线，不暴露单实例明细）
- **Query**: `range` 或 `from`/`to`（默认 24h）；`resolution`(auto\|raw\|5m\|1h)
- **响应**:
  ```json
  {
    "totals": { "nodeCount": 3, "onlineNodeCount": 2, "runningInstances": 5,
                "cpuPct": 47.5, "loadAvg": 31.2, "memUsedBytes": 3221225472, "memTotalBytes": 8589934592,
                "onlinePlayers": 12 },
    "resolution": "5m",
    "trends": [
      { "metricKey": "node_cpu_pct", "unit": "pct", "points": [ { "ts": "...", "avg": 47.5 } ] },
      { "metricKey": "node_mem_used", "unit": "bytes", "points": [ { "ts": "...", "avg": 3.2e9 } ] },
      { "metricKey": "inst_players_online", "unit": "count", "points": [ { "ts": "...", "avg": 12 } ] }
    ]
  }
  ```
  `totals` 取 Node/Instance 表当前值 + 各实例最近 2min 在线人数合计。
- **错误**: 400 `INVALID_RANGE`/`INVALID_RESOLUTION`；403 `FORBIDDEN`

### GET /api/v1/players
- **描述**: 在线玩家列表，聚合可见后端子服（role=backend 且运行中）的 RCON `list` 输出，每个玩家标注所在子服（BC 跨服感知）；按可访问实例集合收敛
- **权限**: `instance.read`
- **响应**: `{ "players":[{"name":"alice","instanceId":3,"instanceName":"lobby"}], "backends":[{"instanceId":3,"instanceName":"lobby","available":true}] }`（`available=false` 的后端 RCON 不可用，结果优雅降级）
- **关联 FR**: FR-054

### POST /api/v1/players/:name/kick
- **描述**: 踢出玩家，向目标后端集合下发 RCON `kick`。范围互斥：`instanceId`（单服）> `networkId`（群组）> 全部可见后端
- **权限**: `instance.operate` | **审计**: `player.kick`
- **请求**: `{ "instanceId":0, "networkId":0, "reason":"" }`（均可选）
- **响应**: `{ "player":"alice","action":"kick","total":2,"succeeded":2,"failed":0,"results":[...] }`
- **错误**: `422 NO_REACHABLE_BACKEND`、`404 NOT_FOUND`（指定实例不可见）
- **关联 FR**: FR-054

### POST /api/v1/players/:name/ban
- **描述**: 封禁玩家，向目标后端集合下发 RCON `ban` 并写入封禁记录（玩家/原因/操作者/范围/是否生效）
- **权限**: `instance.operate` | **审计**: `player.ban`
- **请求**: `{ "instanceId":0, "networkId":0, "reason":"破坏" }`
- **响应**: 同 kick 的执行汇总
- **关联 FR**: FR-054

### POST /api/v1/players/:name/unban
- **描述**: 解封玩家，向目标后端集合下发 RCON `pardon`，并把该玩家仍生效的封禁记录置为失效（保留历史）
- **权限**: `instance.operate` | **审计**: `player.unban`
- **请求**: `{ "instanceId":0, "networkId":0 }`（可选）
- **关联 FR**: FR-054

### GET /api/v1/instances/:id/whitelist
- **描述**: 查询单后端子服白名单（RCON `whitelist list`）
- **权限**: `instance.read`
- **响应**: `{ "instanceId":3,"available":true,"players":["alice","bob"] }`
- **关联 FR**: FR-054

### POST /api/v1/instances/:id/whitelist
- **描述**: 单后端子服白名单增删（RCON `whitelist add|remove`）
- **权限**: `instance.write` | **审计**: `player.whitelist.add` / `player.whitelist.remove`
- **请求**: `{ "action":"add", "player":"alice" }`（`action`：`add`/`remove`）
- **关联 FR**: FR-054

### GET /api/v1/bans
- **描述**: 封禁记录查询（平台侧台账）
- **权限**: `instance.read`
- **Query**: `player`（模糊匹配）、`active=true`（仅生效中）、`limit`（默认 100）
- **响应**: `[{ "id":1,"playerName":"alice","reason":"破坏","scope":"global","scopeId":0,"operatorId":1,"active":true,"createdAt":"...","unbannedAt":null,"operator":{"username":"admin"} }]`
- **关联 FR**: FR-054

### GET /api/v1/cores
- **描述**: 查询服务端核心可用版本/构建。无 `mcVersion` 返回版本列表；带 `mcVersion` 返回下载信息
- **权限**: 平台管理员
- **Query**: `type=paper`（默认）/`velocity`/`waterfall`（PaperMC API）/`bungeecord`（md-5 Jenkins，仅 `latest`）、`mcVersion`、`build`（可选，缺省最新）
- **响应（带 mcVersion）**: `{ "type":"paper","mcVersion":"1.21.1","build":196,"filename":"...","downloadUrl":"...","sha256":"..." }`
- **关联 FR**: FR-034

### POST /api/v1/instances/provision/bukkit
- **描述**: 一键搭建 Paper 后端子服：解析核心 → 分配端口 → 系统分配目录 + 结构化启动 → 下载核心 + 写 eula/server.properties，返回实例（STOPPED，可一键启动）
- **权限**: 平台管理员
- **请求**: `{ "nodeId":1,"name":"lobby","coreType":"paper","mcVersion":"1.21.1","build":0,"jdkId":1,"memoryMb":4096,"jvmArgs":["-XX:+UseG1GC"],"groupId":0,"onlineMode":false }`（`onlineMode` 缺省 false=代理就绪/离线；独立正版服可传 true）
- **响应**: `201` 创建的 Instance；`502 PROVISION_FAILED`（含已创建实例供重试/删除）
- **关联 FR**: FR-034

### POST /api/v1/instances/provision/proxy
- **描述**: 一键搭建代理（role=proxy）：velocity/waterfall（PaperMC）/bungeecord（md-5 Jenkins），分配监听端口/目录，下载核心，生成转发配置；Velocity 生成 forwarding secret 并返回一次
- **权限**: 平台管理员
- **请求**: `{ "nodeId":1,"name":"velocity-main","proxyType":"velocity","version":"3.3.0-SNAPSHOT","jdkId":1,"memoryMb":1024,"jvmArgs":[],"groupId":0,"onlineMode":true,"backendRegistrations":[] }`（`onlineMode` 缺省 true=正版网络；离线模式群组服传 false，持久化后 resync 不会重置）
- **响应**: `201 { instance, forwardingSecret?, registrations, warnings }`；`502 PROVISION_FAILED`
- **关联 FR**: FR-035 | **Spec**: `docs/specs/provision-proxy/`

### POST /api/v1/proxies/:id/resync
- **描述**: 重新把注册关系与 secret 推到代理配置与各后端（代理/后端离线恢复后）
- **权限**: 平台管理员
- **响应**: `200 { synced, secretConsistent, warnings }`
- **关联 FR**: FR-035

### GET / POST /api/v1/proxies/:id/registrations，PATCH / DELETE …/:rid
- **描述**: 管理 proxy↔backend 注册（M:N）；POST/PATCH/DELETE 落库后同步写代理 servers/priorities/forced-host 并下发 Velocity secret
- **权限**: 平台管理员
- **请求(POST)**: `{ "backendId":21,"alias":"lobby","priority":0,"forcedHost":"","restricted":false }`
- **错误**: `404 INSTANCE_NOT_FOUND`、`422 NOT_A_PROXY`/`NOT_A_BACKEND`、`409 ALIAS_CONFLICT`/`ALREADY_REGISTERED`
- **关联 FR**: FR-032（关系）/ FR-035（同步）

### POST /api/v1/instances/:id/clone
- **描述**: 复制 backend 子服为独立新实例（同节点）：系统分配新目录/端口 → CloneWorkDir 复制（排除运行态）→ 修正 server.properties 端口/rcon/motd/level-name（保留 forwarding secret）→ 可选注册进代理。`dryRun=true` 仅预检
- **权限**: 平台管理员
- **请求**: `{ "name":"lobby-2","motd":"","levelName":"","registerToProxyIds":[30],"dryRun":false }`
- **响应**: `201`（dryRun `200`）`{ instance?, allocated, excluded, registrations, warnings, dryRun }`；`422 NOT_A_BACKEND`/`SOURCE_RUNNING`；`502 CLONE_FAILED`
- **关联 FR**: FR-036 | **Spec**: `docs/specs/clone-instance/`

---

## 群组服关系模型（FR-032）

> 全部位于平台管理员路由组。详见 `docs/specs/network-resource-model/`。

### GET /api/v1/nodes/:id/ports
- **描述**: 查看某节点端口占用与分配范围
- **响应**: `{ nodeId, ranges:{serverPortBase,rconPortBase,rangeSize}, occupied:[{instanceId,name,role,serverPort,rconPort,queryPort}] }`

### GET / POST /api/v1/networks，GET / PATCH / DELETE …/:id
- **描述**: 群组（Network 非独占软标签）CRUD；删除群组不影响成员实例与代理注册
- **请求(POST)**: `{ "name":"survival","description":"" }`；**错误**: `409 NETWORK_NAME_CONFLICT`、`404 NETWORK_NOT_FOUND`

### POST /api/v1/networks/:id/members，DELETE …/members/:instanceId
- **描述**: 群组成员增删（幂等）；**请求(POST)**: `{ "instanceIds":[12,13] }`

### POST /api/v1/networks/:id/actions
- **描述**: 群组成员批量生命周期操作；**请求**: `{ "action":"start"|"stop"|"restart" }`
- **响应**: `{ action,total,succeeded,failed,results }`

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
    "wsUrl": "ws://<访问 Host>/ws/terminal",
    "expiresIn": 30
  }
  ```
- **说明**: `wsUrl` 指向 CP 代理端点，host 取浏览器请求的 Host（支持非 localhost 访问）；scheme 跟随访问协议——经 TLS 直连或反代标注 `X-Forwarded-Proto: https` 时为 `wss`，否则 `ws`，避免 HTTPS 页面连 `ws` 被混合内容策略拦截。前端连接时以 `?token=` 附加 token。

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

### POST /api/v1/instances/:id/files/archive
- **描述**: 批量打包下载。把选中的若干文件/目录（目录递归）即时打包为 **zip** 流式返回。Worker 边遍历边打包边流式发送（不全量缓冲整包），CP 把 gRPC 流转为 HTTP `application/zip` 响应体；打包开始前失败仍返回 JSON 错误
- **关联 FR**: FR-070
- **权限**: `instance.file`（可访问实例）
- **请求**: `{ "paths": ["plugins", "server.properties", "world/level.dat"] }`（相对工作目录，非空；每条经路径校验：禁 `..`/前导 `/`；目录递归、文件直纳入）
- **响应**: `200`，`Content-Type: application/zip`，`Content-Disposition: attachment; filename="<instanceName>-files.zip"`，响应体为分块 zip 字节流
- **错误**: `400 INVALID_REQUEST`（paths 为空或含非法路径）；`404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（节点离线/工作目录未设置/打包失败，流已开始则截断连接）

### DELETE /api/v1/instances/:id/files
- **描述**: 删除文件
- **关联 FR**: FR-008
- **请求**: `{ "path": "string" }`

### POST /api/v1/instances/:id/files/rename
- **描述**: 重命名/移动文件或目录。`newPath` 跨目录时即「移动」（Worker `os.Rename`），故资源管理器树内拖拽移动复用本端点，无需独立 move 端点
- **关联 FR**: FR-008, FR-020, FR-070
- **请求**: `{ "oldPath": "string", "newPath": "string" }`

### GET /api/v1/instances/:id/files/versions
- **描述**: 列出某文件的历史版本（按 ID 倒序，最新在前）。编辑保存或上传覆盖已存在文件前自动生成快照
- **关联 FR**: FR-051
- **权限**: `instance.file`（可访问实例）
- **Query**: `?path=plugins/essentials/config.yml`
- **响应**: `[{ "id": 12, "filePath": "string", "size": 0, "authorId": 0, "rollbackOfVersionId": 0, "createdAt": "RFC3339" }]`

### GET /api/v1/instances/:id/files/diff
- **描述**: 某文件 from→to 版本差异（unified diff）。`to` 省略表示与当前文件内容比较；二进制内容返回 `binary=true` 且 `unifiedDiff` 为空
- **关联 FR**: FR-051
- **权限**: `instance.file`（可访问实例）
- **Query**: `?path=...&from=11&to=12`
- **响应**: `{ "fromVersionId": 11, "toVersionId": 12, "unifiedDiff": "string", "binary": false }`

### POST /api/v1/instances/:id/files/rollback
- **描述**: 把文件回滚到指定版本，回滚前自动快照当前内容（回滚本身可被再次回滚）
- **关联 FR**: FR-051
- **权限**: `instance.file`（可管理实例）
- **请求**: `{ "path": "string", "versionId": 12 }`
- **响应**: `{ "versionId": 15 }`（回滚写回后新生成的版本 ID）

---

## 配置管理

> 配置引擎（FR-031）在工作目录内读写配置文件并维护**配置版本**（`instance_config_versions`，与文件版本分离）：保留注释的多格式读写、schema 表单/文本双模式、跨文件一致性校验、版本 diff/回滚。配置读写经 gRPC 委托 Worker。前端「配置」段复用 FR-070 资源管理器组件（左树右内容/编辑器 + 交互全集），叠加 schema 双模式编辑、收藏与配置版本（FR-071）。

### GET /api/v1/instances/:id/configs/discover
- **描述**: 递归发现实例工作目录下**全部**配置文件（按扩展名识别 properties/yml/yaml/toml/json/txt/conf，不限内置 schema），返回相对路径扁平列表。供「已发现配置」快速面板与收藏解析。CP 经既有 `Worker.ListFiles` gRPC 逐目录遍历（不新增 gRPC），深度上限 8、目录上限 2000，超限截断标 `truncated`
- **关联 FR**: FR-071
- **权限**: `instance.file`（可访问实例）
- **响应**: `{ "files": [{ "path": "server.properties", "format": "properties", "supported": true }], "truncated": false }`（`supported=true` 表示命中内置 schema，可走表单模式）
- **错误**: `404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（节点离线/工作目录未设置）

### GET /api/v1/instances/:id/configs
- **描述**: 列出某目录内可管理配置文件（内置可识别格式）。`?path=` 可选子目录
- **关联 FR**: FR-031
- **权限**: `instance.file`

### GET /api/v1/instances/:id/configs/read
- **描述**: 读取单配置文件：原文 + 字段 + schema JSON + 校验结果。`?path=server.properties`
- **关联 FR**: FR-031

### POST /api/v1/instances/:id/configs/write
- **描述**: 文本模式写入配置，保存成功生成配置版本
- **关联 FR**: FR-031
- **请求**: `{ "path": "string", "content": "string", "message": "string?" }`
- **响应**: `{ "versionId": 12, "validation": { "valid": true, "issues": [] } }`

### POST /api/v1/instances/:id/configs/write-fields
- **描述**: 表单模式写入：字段级补丁回原文（保留注释/键顺序），生成配置版本
- **关联 FR**: FR-031
- **请求**: `{ "path": "string", "fields": { "server-port": "25566" }, "message": "string?" }`

### POST /api/v1/instances/:id/configs/cross-check
- **描述**: 跨文件/跨实例一致性校验（端口唯一 / online-mode 与转发配套 / forwarding secret 跨代理一致）。返回 warning 列表，不影响写入
- **关联 FR**: FR-031
- **请求**: `{ "path": "string", "content": "string" }`
- **响应**: `{ "issues": [{ "level": "warning", "message": "string", "key": "string?" }] }`

### GET /api/v1/instances/:id/configs/versions/*file
- **描述**: 列出某配置文件历史版本（按 ID 倒序）
- **关联 FR**: FR-031

### GET /api/v1/instances/:id/configs/diff/*file
- **描述**: 配置版本差异。`?from=11&to=12`，`to` 省略表示与当前文件对比
- **关联 FR**: FR-031

### POST /api/v1/instances/:id/configs/rollback/*file
- **描述**: 回滚配置到指定版本并生成新版本记录
- **关联 FR**: FR-031
- **请求**: `{ "versionId": 12, "message": "string?" }`

---

## 插件 / 模组（单服）

> 复用文件 gRPC（ListFiles/WriteFile/RenameFile/DeleteFile）在实例 `plugins/` 与 `mods/` 目录上操作；启用态文件名 `*.jar`，禁用态 `*.jar.disabled`。实例级隔离（AuthzService），写操作经审计中间件记录（`plugin.deploy`/`plugin.delete`/`plugin.toggle`）。

### GET /api/v1/instances/:id/plugins
- **描述**: 列出实例 `plugins/` 与 `mods/` 目录的插件 jar，识别启用/禁用状态（目录不存在视为空）
- **关联 FR**: FR-052
- **权限**: 实例可访问（成员仅限有权实例）
- **响应**: `[{ "name": "EssentialsX.jar", "dir": "plugins", "enabled": true, "size": 1024, "modTime": 1710000000 }]`

### POST /api/v1/instances/:id/plugins
- **描述**: 上传插件（multipart）。先入制品库（FR-045，`type=plugin`，sha256 去重）再部署到目标目录
- **关联 FR**: FR-052, FR-045
- **权限**: 实例可管理
- **表单**: `file`（必填，.jar）、`dir`（可选，`plugins`|`mods`，默认 `plugins`）
- **响应**: `201 { "message": "已上传", "asset": { ...Asset } }`

### DELETE /api/v1/instances/:id/plugins/:name
- **描述**: 删除指定插件（同时匹配启用/禁用文件名）。二次确认在前端完成
- **关联 FR**: FR-052
- **权限**: 实例可管理
- **Query**: `?dir=plugins|mods`（可选，默认 `plugins`）
- **路径参数**: `name` 为展示名（不含 `.disabled`）

### POST /api/v1/instances/:id/plugins/:name/toggle
- **描述**: 启用/禁用插件（在 `.jar` 与 `.jar.disabled` 间重命名，不删除文件）
- **关联 FR**: FR-052
- **权限**: 实例可管理
- **Query**: `?dir=plugins|mods`（可选，默认 `plugins`）
- **响应**: `{ "message": "已切换", "enabled": false }`

---

## Bot

### GET /api/v1/bots
- **描述**: Bot 列表，分页 + 多维筛选（替换原扁平数组返回，FR-038）
- **关联 FR**: FR-009, FR-038
- **权限**: `bot:read`（资源级按可访问实例隔离）
- **Query**: `?page=1&pageSize=20&instanceId=xxx&nodeId=xxx&status=connected&behavior=guard&q=keyword`
  - `page` 默认 1（< 1 归一为 1）；`pageSize` 默认 20，范围 [1,100]，越界裁剪
  - `nodeId` 经实例联表过滤；`q` 匹配 `name` 或 `uuid`
- **响应**:
  ```json
  {
    "items": [
      { "id": 1, "uuid": "...", "instanceId": 1, "name": "GuardBot",
        "status": "connected", "behavior": "guard", "config": "{...}",
        "workerId": "node-uuid", "createdAt": "...", "updatedAt": "..." }
    ],
    "total": 1,
    "page": 1,
    "pageSize": 20
  }
  ```
  - 非平台管理员：`items`/`total` 仅含其可访问实例下的 Bot

### GET /api/v1/bots/summary
- **描述**: Bot 计数聚合（全局或按 `groupBy` 分组），不返回逐条 Bot（FR-038）
- **关联 FR**: FR-038
- **权限**: `bot:read`（资源级按可访问实例隔离）
- **Query**: `?groupBy=instance|node|status|behavior` + 同 `GET /bots` 的筛选维度（先过滤再聚合）
- **响应（无 groupBy）**:
  ```json
  { "total": 12800, "byStatus": { "connected": 12000, "connecting": 800 } }
  ```
- **响应（groupBy=instance|node|status|behavior）**:
  ```json
  {
    "total": 12800,
    "byStatus": { "connected": 12000, "connecting": 800 },
    "groupBy": "instance",
    "groups": [ { "key": "1", "label": "生存服", "total": 50, "online": 48 } ]
  }
  ```
  - `groups[].key`：分组键（instance/node 为 ID 字符串，status/behavior 为该值）
  - `groups[].label`：可读名（instance→实例名，node→节点名）；`online`：该组 `connected` 数
  - 仅做 DB 聚合（COUNT + GROUP BY），不序列化任何 Bot 行
- **错误**: `groupBy` 非法值 → 400 `INVALID_REQUEST`

### POST /api/v1/bots/batch
- **描述**: 按 id 列表或筛选条件批量执行操作，经 gRPC 委托对应 Worker，返回成功/失败计数（FR-038）
- **关联 FR**: FR-038
- **权限**: `bot:manage`（资源级按可管理实例隔离）
- **请求**:
  ```json
  {
    "action": "set-behavior",
    "ids": [1, 2, 3],
    "filter": { "instanceId": 1, "nodeId": 2, "status": "connected", "behavior": "idle", "q": "guard" },
    "behavior": "follow",
    "target": "PlayerName"
  }
  ```
  - `action` ∈ `set-behavior` | `start` | `stop` | `delete`
  - 目标二选一：`ids` 或 `filter`（皆空 → 400；同时给出以 `ids` 为准）
  - `behavior`：`action=set-behavior` 时必填；目标上限 5000（超出 → 400）
  - 动作映射（复用既有 per-bot RPC）：`set-behavior`→SetBotBehavior、`start`→CreateBot、`stop`→DeleteBot(保留行,置 stopped)、`delete`→DeleteBot+软删
- **响应**:
  ```json
  {
    "action": "set-behavior",
    "requested": 3,
    "succeeded": 2,
    "failed": 1,
    "skipped": 0,
    "errors": [ { "botId": 3, "error": "Worker node-x 未连接" } ]
  }
  ```
  - `skipped`：请求 `ids` 中越权/不存在被静默剔除的数量（存在性隐藏）
  - `failed` 仅统计 Worker 委托结果；DB 侧变更按既有「失败记 warning 不阻塞」语义
- **错误**: 400 `INVALID_REQUEST`（action 非法 / 目标皆空 / set-behavior 缺 behavior / 超上限）；403 `FORBIDDEN`

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

### GET /api/v1/schedules/:id/logs
- **描述**: 定时任务执行日志列表
- **关联 FR**: FR-012
- **Query**: `?page=1&pageSize=20`
- **响应** (200):
  ```json
  {
    "items": [{ "id": 1, "scheduleId": 1, "action": "restart", "status": "success", "error": "", "startedAt": "datetime", "finishedAt": "datetime" }],
    "total": 50,
    "page": 1,
    "pageSize": 20
  }
  ```

---

## 备份

### GET /api/v1/instances/:id/backups
- **描述**: 实例备份列表（含 `mode` 全量/增量、`parentId` 备份链、`storageId` 存储位置）
- **关联 FR**: FR-013, FR-056, FR-057

### POST /api/v1/instances/:id/backups
- **描述**: 创建备份。`incremental=true` 时挂到该实例最近一次已完成备份后形成备份链（仅打包变化文件）；`storageId` 指定远程存储后端，缺省存于节点本地
- **关联 FR**: FR-013, FR-056, FR-057
- **请求**: `{ "name": "string", "incremental": false, "storageId": 0 }`
- **错误**: 422 `BUSINESS_ERROR`（增量但无可作基准的已完成全量备份）

### POST /api/v1/backups/:id/restore
- **描述**: 恢复备份。增量备份沿父链回溯解析整链（全量基 + 各增量），委托 Worker 按序回放；远程备份先拉回本地再回放
- **关联 FR**: FR-013, FR-056, FR-057

### DELETE /api/v1/backups/:id
- **描述**: 删除备份。被增量子备份依赖时拒绝（422），避免割裂备份链
- **关联 FR**: FR-013, FR-056

### GET /api/v1/backup-storages
- **描述**: 备份远程存储后端列表（凭证以 `${ENV_VAR}` 引用，不返回明文）
- **权限**: 平台管理员
- **关联 FR**: FR-057

### POST /api/v1/backup-storages
- **描述**: 创建远程存储后端（`type` ∈ s3/sftp/webdav）。凭证字段须为 `${ENV_VAR}` 引用，明文/非法类型回 422
- **权限**: 平台管理员
- **关联 FR**: FR-057
- **请求**: `{ "name": "string", "type": "s3", "endpoint": "", "bucket": "", "region": "", "prefix": "", "accessKeyEnv": "${VAR}", "secretKeyEnv": "${VAR}", "useSsl": true }`

### DELETE /api/v1/backup-storages/:id
- **描述**: 删除远程存储后端。被备份引用时拒绝（422）
- **权限**: 平台管理员
- **关联 FR**: FR-057

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

### DELETE /api/v1/templates/:id
- **描述**: 删除模板。模板与实例为松关联（建实例时拷贝 startCommand），删除模板不影响已创建的实例。
- **关联 FR**: FR-064
- **响应 200**: `{ "message": "已删除" }`
- **错误**: `400 INVALID_REQUEST`（ID 非法）、`500 INTERNAL_ERROR`

---

## 制品库

> 平台级共享资产，内容寻址（sha256）+ 类型分区存储，统一由平台管理员管理。物理文件位于数据根 `var/artifacts/<type>/<sha256[:2]>/<sha256><ext>`。参见 ADR-011。
> 权限：以下接口均要求平台管理员。

### GET /api/v1/assets
- **描述**: 列出资产，可按类型筛选、分页
- **关联 FR**: FR-045
- **Query**: `?type=core&page=1&pageSize=20`
  - `type`: 可选，`core|plugin|image|video|archive|blob`，非法值返回 400 `INVALID_TYPE`
- **响应 200**:
```json
{
  "items": [
    {
      "id": 1, "type": "core", "name": "paper-1.20.4", "version": "435",
      "filename": "paper.jar",
      "sha256": "<64hex>", "md5": "<32hex>", "size": 48234123,
      "contentType": "application/java-archive", "sourceUrl": "",
      "metadata": "", "storageState": "hot", "storageBackend": "local",
      "refCount": 0, "relPath": "var/artifacts/core/ab/<sha256>.jar",
      "createdAt": "2026-06-19T00:00:00Z", "lastUsedAt": "2026-06-19T00:00:00Z"
    }
  ],
  "total": 1, "page": 1, "pageSize": 20
}
```

### GET /api/v1/assets/:id
- **描述**: 资产详情
- **关联 FR**: FR-045
- **响应**: 单个资产对象（字段同上）；不存在返回 404 `NOT_FOUND`

### POST /api/v1/assets
- **描述**: 入库一个资产——multipart 上传 **或** 从本地路径登记。入库即算 sha256+md5；同 `(type, sha256)` 去重复用并刷新 `last_used_at`；提供期望校验和则比对，不符拒收。
- **关联 FR**: FR-045
- **方式 A（multipart 上传）** `Content-Type: multipart/form-data`：
  - `file`（必填，文件）、`type`（必填）
  - 可选：`name`、`version`、`contentType`、`sourceUrl`、`metadata`(JSON 字符串)、`expectedSha256`、`expectedMd5`
- **方式 B（从本地路径登记）** `Content-Type: application/json`：
```json
{ "type": "core", "path": "/abs/or/rel/path/to/paper.jar",
  "name": "paper-1.20.4", "version": "435", "filename": "paper.jar",
  "expectedSha256": "<64hex>" }
```
- **响应 201**: 新建或复用的资产对象
- **错误**:
  - 400 `INVALID_REQUEST`（缺 type 或既无 file 也无 path）
  - 400 `INVALID_TYPE`（类型非法）
  - 422 `CHECKSUM_MISMATCH`（期望校验和与实际不符）
  - 500 `INGEST_FAILED`

### DELETE /api/v1/assets/:id
- **描述**: 删除资产；被引用（`refCount>0`）时拒绝
- **关联 FR**: FR-045
- **错误**: 404 `NOT_FOUND`；409 `ASSET_IN_USE`（附当前引用数）

> 备注：内部「下载入库」（download → store）能力已实现于服务层（`AssetService.IngestFromURL`），供 FR-034 建服取核心时复用，暂未单独暴露为公开 endpoint。

---

## 审计日志

### GET /api/v1/audit
- **描述**: 审计日志列表（平台管理员）
- **关联 FR**: FR-015
- **Query**: `?userId=xxx&action=instance.start&targetType=instance&from=2024-01-01T00:00:00Z&to=2024-12-31T23:59:59Z&limit=100`
- **参数说明**:
  - `from`/`to`: RFC3339 格式时间，按 created_at 筛选范围

---

## 日志中心（FR-049）

> 实例运行日志（stdout/stderr）与平台结构化日志统一持久化、检索与导出。过滤与分页在 DB 完成，不全量序列化。

### GET /api/v1/logs
- **描述**: 分页查询日志
- **关联 FR**: FR-049, FR-050
- **权限**: 所有认证用户。平台管理员可见全部（实例 + 平台日志）；组成员/组管理员仅见有权实例日志，平台日志对其隐藏（强制 `source=instance` 并按可访问实例集合收敛）
- **Query**: `?source=instance&level=error&instanceId=12&nodeId=3&keyword=NPE&from=2026-01-01T00:00:00Z&to=2026-12-31T23:59:59Z&page=1&pageSize=50`
- **参数说明**:
  - `source`: `instance` / `control_plane` / `worker`
  - `level`: `debug` / `info` / `warn` / `error`
  - `keyword`: 在 message 上做 DB 侧 LIKE 检索
  - `from`/`to`: RFC3339 时间，按日志产生时间筛选
  - `page`（默认 1）/`pageSize`（默认 50，上限 500）
- **响应**:
```json
{
  "items": [
    { "id": 1, "source": "instance", "level": "info", "instanceId": 12, "instanceUuid": "...", "nodeId": 3, "stream": "stdout", "message": "Done (3.2s)! For help, type \"help\"", "time": "2026-06-20T12:00:00Z" }
  ],
  "total": 1,
  "page": 1,
  "pageSize": 50
}
```

### GET /api/v1/logs/export
- **描述**: 按当前筛选导出日志为 NDJSON 附件（每行一条 JSON，按时间正序）
- **关联 FR**: FR-049, FR-050
- **权限**: 同 `GET /logs`（同样的可见性收敛）
- **Query**: 同 `GET /logs` 的筛选参数；额外 `limit`（最大导出行数，默认/上限 50000）。分页参数忽略
- **响应**: `Content-Type: application/x-ndjson`，`Content-Disposition: attachment`

---

## 平台设置（FR-063）

> 平台配置在 YAML+env 基线上叠加一层 DB 覆盖层（`platform_settings`），生效优先级 **DB 覆盖 > 环境变量 > YAML 默认**。仅白名单项可运行时修改；启动固定/敏感项只读展示，敏感值脱敏不下发明文。参见 ADR-015。

### GET /api/v1/settings
- **描述**: 返回当前有效配置视图，分为可编辑项（`editable`）与只读项（`readOnly`）。每项含当前生效值（敏感项已脱敏）、是否可编辑、是否敏感、是否被 DB 覆盖、运行时修改是否在 CP 内即时生效
- **权限**: 平台管理员
- **关联 FR**: FR-063
- **响应**:
  ```json
  {
    "editable": [
      { "key": "log.level", "value": "info", "editable": true, "sensitive": false, "overridden": false, "effectiveImmediately": true },
      { "key": "jdk.mirror.temurin", "value": "https://api.adoptium.net", "editable": true, "sensitive": false, "overridden": false, "effectiveImmediately": false },
      { "key": "graceful_stop.timeout", "value": "30s", "editable": true, "sensitive": false, "overridden": false, "effectiveImmediately": false },
      { "key": "backup.retention_days", "value": "14", "editable": true, "sensitive": false, "overridden": false, "effectiveImmediately": false }
    ],
    "readOnly": [
      { "key": "server.port", "value": "8080", "editable": false, "sensitive": false, "overridden": false, "effectiveImmediately": false },
      { "key": "jwt.secret", "value": "dev***-me", "editable": false, "sensitive": true, "overridden": false, "effectiveImmediately": false }
    ]
  }
  ```

### PUT /api/v1/settings
- **描述**: 写入一批白名单配置覆盖。非白名单键或值不合法时整体拒绝（422）且不落库；成功后返回更新后的最新视图。可即时生效项（`log.level`）落库后立即应用
- **权限**: 平台管理员
- **关联 FR**: FR-063
- **可写白名单键**: `log.level`（debug|info|warn|error）、`jdk.mirror.temurin` / `jdk.mirror.corretto` / `jdk.mirror.zulu`、`graceful_stop.timeout`（Go duration 文本）、`backup.retention_days`（非负整数）
- **各项生效方式**（FR-063）：
  - `log.level`：`effectiveImmediately=true`，落库即在 CP 内切换（slog LevelVar）
  - `jdk.mirror.*`：安装 JDK 时 CP 取生效值经 `InstallJDK.mirror_base` 下发 Worker，影响下载源
  - `graceful_stop.timeout`：启动实例时 CP 取生效值经 `CreateInstance.graceful_stop_timeout_seconds` 下发 Worker→wrapper；对设置变更后**新启动**的实例生效，已运行实例保留启动时的值
  - `backup.retention_days`：CP 后台巡检（约每小时一轮）裁剪 `createdAt` 早于 N 天的备份；`≤0` 不裁剪；被未超期增量子链引用的全量基跳过以保链可恢复
- **请求**: `{ "values": { "log.level": "debug", "backup.retention_days": "30" } }`

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
