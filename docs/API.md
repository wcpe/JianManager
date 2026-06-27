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
- **描述**: 更新用户（角色/状态/重置密码）
- **请求体**: `{ role?, status?, password? }`（`password` 非空时由平台管理员重置该用户登录密码，长度下限 8，与初始化/创建一致）
- **关联 FR**: FR-002, FR-156
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

### GET /api/v1/nodes/repair/suspects
- **描述**: 列出疑似被串改/重名的节点（只读诊断）。信号：名字带迁移去重后缀 `-dup-<id>`（曾因重名被自动改名）、或仍存在同名活跃节点组。修复重名覆盖 BUG-A 的存量数据排查入口
- **关联 FR**: FR-004（见 ADR-039 §2）；UI 入口随 FR-177
- **权限**: 平台管理员
- **响应**: `[{ node: <节点对象>, reasons: [string] }]`

### GET /api/v1/nodes/:id/orphans
- **描述**: 统计指定节点上孤立的 JDK / 实例数量（只读，修复前评估影响面）
- **关联 FR**: FR-004（见 ADR-039 §2）
- **权限**: 平台管理员
- **响应**: `{ nodeId, jdkCount, instanceCount }`
- **错误码**: `404 NOT_FOUND`（节点不存在）

### POST /api/v1/nodes/:id/reenroll
- **描述**: 把被挤占的机器作为新节点重新 enroll——为该节点行轮换全新 `node_uuid`/`node_secret`（切断与被冒用旧身份的关联，旧 secret 即刻失效，节点置离线待重注册）。挂在该节点的 JDK/实例随 `node_id` 保留。**破坏性操作**
- **关联 FR**: FR-004（见 ADR-039 §2）
- **权限**: 平台管理员（危险操作，前端二次确认）
- **请求**: `{ confirm: bool }`（必须 `true`）
- **响应**: `{ nodeId, newUuid, newSecret, oldUuid }`（`newSecret` 仅此一次返回）
- **错误码**: `409 CONFIRM_REQUIRED`（未确认）、`404 NOT_FOUND`（节点不存在）
- **审计**: `node.reenroll`

### POST /api/v1/nodes/:id/purge-orphans
- **描述**: 清理指定节点上孤立的 JDK / 实例引用——硬删该节点 NodeJDK 行、软删该节点实例（清掉冒用期间错误挂上的残留资源）。**破坏性操作**
- **关联 FR**: FR-004（见 ADR-039 §2）
- **权限**: 平台管理员（危险操作，前端二次确认）
- **请求**: `{ confirm: bool }`（必须 `true`）
- **响应**: `{ nodeId, jdkDeleted, instancesPurged }`
- **错误码**: `409 CONFIRM_REQUIRED`（未确认）、`404 NOT_FOUND`（节点不存在）
- **审计**: `node.purge_orphans`

### GET /api/v1/nodes/:id/metrics
- **描述**: 节点指标（CPU/内存/磁盘时间序列）
- **关联 FR**: FR-010

### POST /api/v1/nodes/enroll-token
- **描述**: 签发一次性、限时的节点准入 enrollment token，返回明文 + Linux/Windows 一键安装命令（傻瓜部署）
- **关联 FR**: FR-080（见 ADR-020）
- **权限**: 平台管理员
- **请求**（全部可选）: `{ nodeName?: string, ttlMinutes?: int(默认30, 1~1440) }`
- **响应** `201`:
  ```json
  {
    "token": "jmet_xxx",
    "tokenId": 12,
    "tokenPrefix": "jmet_ab12",
    "expiresAt": "2026-06-23T12:30:00Z",
    "nodeName": "",
    "controlPlaneGrpc": "cp-host:9100",
    "installCommandLinux": "curl -fsSL .../install-worker.sh | sh -s -- --control-plane cp-host:9100 --token jmet_xxx",
    "installCommandWindows": "iwr .../install-worker.ps1 -UseBasicParsing | iex; Install-JianManagerWorker -ControlPlane cp-host:9100 -Token jmet_xxx"
  }
  ```
- token **落库只存 SHA-256 哈希**，明文一次性返回、不可二次读取；`controlPlaneGrpc`/脚本基址由 CP 据请求 Host 推断，可经 `enroll.advertise_grpc`/`enroll.script_base_url` 配置覆盖
- 一键命令的二进制下载基址默认 GitHub Releases latest（`enroll.binary_url`，ADR-036 契约 `worker-<os>-<arch>[.exe]`），开箱即下载；可覆盖为内网源或置空改用脚本 `--binary` 本地兜底
- **审计**: `node.enroll_token.create`（detail 仅含 tokenId/tokenPrefix/nodeName/expiresAt，绝不含明文）

### GET /api/v1/nodes/enroll-tokens
- **描述**: 列出 enrollment token（仅元数据：前缀/过期/消费状态/预设名，无明文）
- **关联 FR**: FR-080
- **权限**: 平台管理员
- **响应**: `[{ id, tokenPrefix, nodeName, expiresAt, used, usedAt, usedByNode, revoked, createdAt }]`

### DELETE /api/v1/nodes/enroll-tokens/:id
- **描述**: 吊销未消费的 enrollment token（标记失效，立即不可用）
- **关联 FR**: FR-080
- **权限**: 平台管理员
- **错误码**: `404 ENROLL_TOKEN_NOT_FOUND`
- **审计**: `node.enroll_token.revoke`

> **gRPC `Register` 身份匹配（FR-080 + ADR-039，不改 proto）**: Worker 注册经 gRPC metadata header 携带身份/准入凭据，CP 按三级优先级匹配既有节点（修复重名覆写 BUG-A）——
> 1. **UUID 证明**：重注册时携带 `node-uuid` + `node-secret`；命中库中节点且 secret 匹配 → 按 UUID 重注册（更新 host/port/os/arch，允许改名），返回既有身份；secret 不符 → `PermissionDenied`，绝不覆写。
> 2. **同机 host 兼容（过渡）**：未升级旧 Worker 只带 name，name 命中且本次连接 host 与库存一致 → 放行重注册并告警建议升级；host 不一致落到 3。
> 3. **token 新建**：否则视为新节点，必须带有效 enrollment token（`enroll-token` header，存在+未过期+未消费+未吊销），校验通过原子标记 `used` 并换发全新 `node_uuid`/`node_secret`，失败回 `PermissionDenied`；若上报名与既有节点撞名 → `AlreadyExists` 拒绝（提示改名），绝不覆写。
>
> Worker 把换发的身份持久化到 `<dataRoot>/etc/node-identity.json`（0600），重启读取并经 metadata 出示，不重复消费一次性 token。

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
- **关联 FR**: FR-005, FR-078（docker 模式）, FR-079（资源限额）
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
    "image": "itzg/minecraft-server:latest",
    "cpuLimit": 1.5,
    "memLimitMb": 2048,
    "diskLimitMb": 10240,
    "autoStart": false,
    "autoRestart": true,
    "groupId": 1
  }
  ```
- **说明**: `processType=docker` 时 `image` 必填（容器镜像引用，默认 Docker Hub，本地缺失时启动前自动拉取，FR-078/ADR-019）；其它启动方式忽略 `image`。docker 模式宿主端口（FR-032 端口池分配）映射到容器内端口（MC 约定 25565），工作目录 bind-mount 到容器 `/data`。`cpuLimit`（核数，可小数）/`memLimitMb`（MiB）/`diskLimitMb`（MiB）为 docker 模式资源限额（FR-079/ADR-019），`0` 或缺省=不限制，仅 docker 模式生效（其它启动方式忽略）；启动时 `cpuLimit`→`--cpus`、`memLimitMb`→`--memory` 注入容器 cgroup，`diskLimitMb` 当前仅持久化展示不强制。

### GET /api/v1/instances/:id
- **描述**: 实例详情
- **关联 FR**: FR-005

### PUT /api/v1/instances/:id
- **描述**: 更新实例配置
- **关联 FR**: FR-005, FR-047, FR-079（资源限额）
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
    "tags": ["env:prod", "survival"],
    "cpuLimit": 2,
    "memLimitMb": 4096,
    "diskLimitMb": 0
  }
  ```
- **说明**: `tags` 传数组（含空数组 `[]` 清空）覆盖标签；环境维度复用 `env:` 前缀（FR-047），无独立字段。`cpuLimit`/`memLimitMb`/`diskLimitMb` 为 docker 模式资源限额（FR-079），传值（含 `0`=清除限制）覆盖、缺省/`null` 不变；变更对实例下一次启动生效，仅 docker 模式生效。

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
- **描述**: 向运行中的实例下发一行控制台命令（复用既有 SendCommand 委托，仅对 RUNNING 实例生效；命令不改变实例状态）。批量下发见 `POST /instances/batch`（action=command）。
- **关联 FR**: FR-005
- **权限**: `instance.operate`（资源级按可访问实例隔离）
- **请求**: `{ "command": "say hello" }`
- **响应**: `200 { "message": "已发送" }`
- **错误**: 400 `INVALID_REQUEST`（缺 command）；404 `NOT_FOUND`（实例不存在/无权访问）；422 `INSTANCE_NOT_RUNNING`（实例非 RUNNING）；503 `COMMAND_FAILED`（Worker 未连接/委托失败）

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
- **描述**: 实例指标。经 ServerProbe `/metrics` 取富指标（**RCON 已退役 FR-067/ADR-016**——探针未部署/抓取失败时富指标 N/A，不再回退 RCON）
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
- **描述**: 在线玩家列表，经 ServerProbe 探针事件实时聚合（FR-066/067），每个玩家标注所在子服（BC 跨服感知）；按可访问实例集合收敛
- **权限**: `instance.read`
- **响应**: `{ "players":[{"name":"alice","instanceId":3,"instanceName":"lobby"}], "backends":[{"instanceId":3,"instanceName":"lobby","available":true}] }`（`available=false` 的后端探针未连入，结果优雅降级）
- **关联 FR**: FR-054

### GET /api/v1/instances/:id/players/events
- **描述**: SSE 推送某实例（探针）的实时玩家事件（FR-066）。CP 订阅各 Worker 的 `StreamPluginEvents`（探针经反向 WS 上报），按实例 UUID 过滤后扇出。探针未连入时事件流为空（前端据 `connected` 降级提示）。子服端（Bukkit 探针）报本服 `player_join`/`player_quit`/`chat`，代理端（BC 探针）报 `player_join`/`player_quit`/`cross_server`（精确跨服路由）
- **权限**: `instance.read`（且实例须可访问）
- **响应**: `text/event-stream`
  - 首帧 `event: init`，`data` 为 `{ "connected": true, "players":[{"name":"alice","server":"lobby"}] }`（当前探针连接状态 + 实时在线名册快照）
  - 增量 `event: player`，`data` 为单条事件 `{ "instanceUuid":"...","instanceId":3,"instanceName":"lobby","type":"player_join","timestamp":1719000000,"playerName":"alice","playerUuid":"...","server":"lobby" }`（`type` ∈ connected/disconnected/heartbeat/player_join/player_quit/chat/cross_server；cross_server 附 `fromServer`/`toServer`；chat 附 `message`）
- **关联 FR**: FR-066

### GET /api/v1/instances/:id/probe/update
- **描述**: 探针在线更新状态（FR-068）：探针连接状态 + CP 内嵌最新探针版本/指纹 + 上次推送时间
- **权限**: `instance.read`
- **响应**: `{ "instanceId":3, "instanceUuid":"...", "probeConnected":true, "embeddedVersion":"0.1.0", "embeddedFingerprint":"<sha256 前缀>", "embeddedAvailable":true, "lastPushedAt":"2026-06-22T10:00:00Z" }`（`embeddedAvailable=false` 表示本次构建未 `make embed-probe`，无可推 jar）
- **关联 FR**: FR-068 ｜ **关联 ADR**: ADR-016

### POST /api/v1/instances/:id/probe/update
- **描述**: 把 CP 内嵌最新探针 jar 经 gRPC `DeployServerProbe` 推到该实例 plugins 目录（**下次重启生效**）；`restart=true` 时推送后立即重启实例使其生效（FR-068）
- **权限**: `instance.operate` ｜ **审计**: `instance.probe.update`
- **请求**: `{ "restart": false }`
- **响应**: `{ "instanceId":3, "deployed":true, "restarted":false, "probeConnected":true, "embeddedVersion":"0.1.0", "message":"已推送，下次重启生效" }`
- **错误**: `422 PROBE_NOT_EMBEDDED`（构建未内嵌探针）、`404 NOT_FOUND`
- **关联 FR**: FR-068

### GET /api/v1/instances/:id/server-state
- **描述**: 按需查询某实例全量 Bukkit 内部状态（server/worlds/jvm/**classloader**/scheduler/listeners），经探针反向 WS 桥的 `QueryServerState`（action=`query_state`）同步取回探针采集的状态 JSON（FR-076）。轻指标走 `/metrics`；本端点仅前端开「服务器状态」tab/手动刷新时调用。探针采集异步非侵入、有界、超时降级，**绝不拖慢服务器**。CP 不解析 `state`（原样透传探针 JSON，探针字段演进无需改 CP）
- **权限**: `instance.read`（且实例须可访问）
- **响应**: `{ "instanceId":3, "connected":true, "available":true, "state": { "collectedAt":1750000000000, "server":{...}, "worlds":{"items":[...],"total":3,"truncated":false}, "jvm":{...}, "classloader":{"counts":{...},"pluginLoaders":{...}}, "scheduler":{...}, "listeners":{...} }, "error":"" }`
  - `connected=false`：探针未连入 → `state` 为 `null`，前端提示部署/连接探针（HTTP 200，降级不 5xx）
  - `connected=true` 且 `available=false`：探针在线但本次采集超时/失败 → `state` 为 `null` + `error` 说明，前端提示重试
- **错误**: `403 FORBIDDEN`（无 `instance.read`）、`404 NOT_FOUND`（实例不可见/不存在）
- **关联 FR**: FR-076 ｜ **关联 ADR**: ADR-016

### POST /api/v1/instances/:id/business
- **描述**: JBIS 业务对接——向某实例下发一条业务命令（`domain.action` + 结构化 `payload`）并取回结果（FR-116/FR-121，见 ADR-026/027/029）。CP **插件无关**：经既有探针桥（ADR-016）把信封下发到目标实例 ServerProbe 业务对接层（BusinessHost→per-plugin Provider 执行），结果 JSON 原样透传，CP 不解析。`domain` 区分业务域（`economy`/`inventory`…），与监控/治理（`core.*`）同桥分流
- **权限**: 读动作（`write=false`/缺省）`instance.operate`；**写动作（`write=true`，对应 manifest `readOnly=false`，如改余额/改背包）`instance.business.write`**（FR-121）。两者均须实例可访问
- **请求**: `{ "domain":"economy", "action":"balance", "payload":"{\"player\":\"alice\",\"currency\":\"coin\"}", "write":false }`
  - `payload`：结构化参数 JSON 字符串，CP 不解析原样下发；`domain`/`action` 必填
  - `write`（可选，默认 `false`）：是否为高危写动作；前端据 manifest `readOnly` 取反设置
  - `operationId`（可选，写动作必带）：**幂等标识**，对同一逻辑操作的重试必须稳定。CP 用作 payload `taskId`（探针→插件 mce `BusinessOrder` 幂等键，跨节点重试天然防重）；缺省时 CP 兜底生成（但失去重试去重）
  - `reason`（可选）：操作原因，透传进插件流水 `reason` + JM 审计
  - 写动作时 CP 向 payload 注入 `taskId`/`operator`/`operatorId`/`nodeId`/`reason`（仅当业务方未显式同名入参时），使插件审计流水记录操作者（哪个管理员/哪个节点/为什么）
- **响应**: `200`，`{ "instanceId":3, "domain":"economy", "action":"balance", "available":true, "output": {...业务结果JSON...}, "error":"" }`
  - `available=false`：探针未连入/域不可用/Provider 执行失败 → `output` 为 `null` + `error` 说明（HTTP 200，降级不 5xx）
- **审计**: 写动作记 `business.write`（detail 含 domain/action/operationId/reason/available）；审计中间件兜底记 `business.dispatch`（覆盖读+写）
- **错误**: `400 INVALID_REQUEST`（缺 domain/action 或 payload 非法 JSON）、`403 FORBIDDEN`（读缺 `instance.operate` / 写缺 `instance.business.write`）、`404 NOT_FOUND`（实例不可见/不存在）
- **关联 FR**: FR-116, FR-121 ｜ **关联 ADR**: ADR-026, ADR-027, ADR-029

### GET /api/v1/instances/:id/business/manifest
- **描述**: 取某实例的业务能力清单（JBIS 元查询，FR-116）。CP 复用业务下发通道下发保留元命令（`domain=jbis` + `action=manifest`），探针侧 `BusinessHost` 返回各业务 Provider 汇总的能力清单 JSON（`{"domains":{...}}`），供前端**动态发现各域能力、动态渲染**（不硬编码具体插件）。元命令不派发到任何业务 Provider
- **权限**: `instance.read`（只读发现；且实例须可访问）
- **响应**: `200`，`{ "instanceId":3, "domain":"jbis", "action":"manifest", "available":true, "output": { "domains": { "economy": { "actions":[{"action":"balance","args":["player","currency"],"readOnly":true}] } } }, "error":"" }`
  - `available=false`：探针未连入/无业务 Provider → `output` 为 `null` + `error`（HTTP 200，降级不 5xx）
- **错误**: `403 FORBIDDEN`（无 `instance.read`）、`404 NOT_FOUND`（实例不可见/不存在）
- **关联 FR**: FR-116 ｜ **关联 ADR**: ADR-026, ADR-027

### GET /api/v1/instances/:id/business/economy/mirror
- **描述**: 经济余额镜像（FR-122，CP 自有汇聚镜像、非业务真源）：逐 `node→zone` 行返回最新余额（跨区同名玩家分行不混）。Query `?player=&currency=&node=&zone=&limit=`（任意组合过滤）
- **权限**: `instance.read`
- **响应**: `{ "balances":[{ "nodeUuid":"","zoneId":"0","playerName":"Steve","currency":"coin","currencyId":1,"balance":"100.00","lastSeq":3,"lastLedgerId":7,"occurredAt":0 }] }`（余额字符串承载 BigDecimal，禁浮点）
- **关联 FR**: FR-122 ｜ **关联 ADR**: ADR-028

### GET /api/v1/instances/:id/business/economy/aggregate
- **描述**: 跨区经济聚合明细（FR-122）：按 `player`+`currency` 逐 `node→zone` 返回**不盲目求和**（mce 账户按 zone 隔离，是否相加由调用方按业务语义定）。Query `?player=&currency=`
- **权限**: `instance.read`
- **响应**: `{ "rows":[{ "nodeUuid":"","zoneId":"0","balance":"100.00" }] }`
- **关联 FR**: FR-122 ｜ **关联 ADR**: ADR-028

### GET /api/v1/instances/:id/business/economy/leaderboard
- **描述**: 某货币余额倒序 Top-N（FR-123 旁路排行：mce 无 leaderboard API，从 JM 自有镜像表派生、不穿透探针；按 DB 方言数值 CAST 排序，避免 BigDecimal 字符串字典序错排）。逐 `node→zone` 行参与排行
- **权限**: `instance.read`
- **请求**: Query `?currency=（必填）&zone=&node=&limit=`（默认 100，上限 500）
- **响应**: `{ "currency":"coin", "rows":[{ "rank":1,"playerName":"Steve","currency":"coin","nodeUuid":"","zoneId":"0","balance":"100.00" }] }`
- **关联 FR**: FR-123 ｜ **关联 ADR**: ADR-028

### GET /api/v1/instances/:id/business/events
- **描述**: 通用业务事件流（FR-122，按 `(domain,dedupKey)` 去重的 envelope 表，CP 自有汇聚）。经济流水由 `?domain=economy` 过滤后前端解析信封。Query `?domain=&node=&limit=`
- **权限**: `instance.read`
- **响应**: `{ "events":[{ "domain":"economy","dedupKey":"<ledgerId>","action":"","nodeUuid":"","operator":"","payloadJson":"{...}","occurredAt":0 }] }`
- **关联 FR**: FR-122 ｜ **关联 ADR**: ADR-028

### POST /api/v1/players/:name/kick
- **描述**: 踢出玩家，经探针插件桥 `SendPluginCommand` 向目标后端集合下发 kick（FR-067）。范围互斥：`instanceId`（单服）> `networkId`（群组）> 全部可见后端
- **权限**: `instance.operate` | **审计**: `player.kick`
- **请求**: `{ "instanceId":0, "networkId":0, "reason":"" }`（均可选）
- **响应**: `{ "player":"alice","action":"kick","total":2,"succeeded":2,"failed":0,"results":[...] }`
- **错误**: `422 NO_REACHABLE_BACKEND`、`404 NOT_FOUND`（指定实例不可见）
- **关联 FR**: FR-054

### POST /api/v1/players/:name/ban
- **描述**: 封禁玩家，经探针插件桥下发 ban（FR-067）并写入封禁记录（玩家/原因/操作者/范围/是否生效）
- **权限**: `instance.operate` | **审计**: `player.ban`
- **请求**: `{ "instanceId":0, "networkId":0, "reason":"破坏" }`
- **响应**: 同 kick 的执行汇总
- **关联 FR**: FR-054

### POST /api/v1/players/:name/unban
- **描述**: 解封玩家，经探针插件桥下发 pardon（FR-067），并把该玩家仍生效的封禁记录置为失效（保留历史）
- **权限**: `instance.operate` | **审计**: `player.unban`
- **请求**: `{ "instanceId":0, "networkId":0 }`（可选）
- **关联 FR**: FR-054

### GET /api/v1/instances/:id/whitelist
- **描述**: 查询单后端子服白名单（经探针插件桥 `whitelist list`，FR-067）
- **权限**: `instance.read`
- **响应**: `{ "instanceId":3,"available":true,"players":["alice","bob"] }`
- **关联 FR**: FR-054

### POST /api/v1/instances/:id/whitelist
- **描述**: 单后端子服白名单增删（经探针插件桥 `whitelist add|remove`，FR-067）
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

## 实例组织分组（FR-165）

> 文件夹式「实例组织分组树」——多级嵌套（自引用 `parent_id` 邻接表）+ 实例-组 M:N，与用户组（RBAC/配额）、网络群组（proxy↔backend 部署）三者**正交**，仅供人为组织归类、折叠、批量运维。详见 `docs/specs/ui-redesign/fr-165-instance-grouping.md`、ADR-033。读 `instance:read`、写 `instance:write`（不引入新权限节点；树是实例的组织视图，按实例权限收敛）。

### GET /api/v1/instance-groups
- **描述**: 返回分组树（扁平节点列表，前端据 `parentId` 重建层级），每节点含「子树聚合去重」实例数
- **关联 FR**: FR-165
- **权限**: `instance:read`
- **响应**:
  ```json
  [
    { "id": 1, "uuid": "…", "name": "亚洲区", "parentId": null, "sort": 0, "instanceCount": 12 },
    { "id": 2, "uuid": "…", "name": "生存", "parentId": 1, "sort": 0, "instanceCount": 5 }
  ]
  ```
  - `instanceCount`：该节点子树（含自身及所有后代）去重后的实例数（同一实例属多组/属祖先与后代只计一次）

### POST /api/v1/instance-groups
- **描述**: 新建分组节点（`parentId` 省略=根分组，给出时父必须存在）
- **关联 FR**: FR-165
- **权限**: `instance:write`
- **请求**: `{ "name": "生存", "parentId": 1 }`
- **响应**: `201` `{ id, uuid, name, parentId, sort }`
- **错误**: 400 `INVALID_REQUEST`（名称空/超长）；400 `INSTANCE_GROUP_PARENT_NOT_FOUND`（父不存在）

### PUT /api/v1/instance-groups/:id
- **描述**: 改名 / 移动父节点（防环）。`parentId` 字段语义三态：**缺省**=不改父；**`null`**=移到根；**数字**=移到该父下
- **关联 FR**: FR-165
- **权限**: `instance:write`
- **请求**: `{ "name": "生存服" }` 或 `{ "parentId": 4 }` 或 `{ "parentId": null }`
- **响应**: `200` `{ id, uuid, name, parentId, sort }`
- **错误**: 409 `INSTANCE_GROUP_CYCLE`（移到自身或子孙下成环）；400 `INSTANCE_GROUP_PARENT_NOT_FOUND`；404 `INSTANCE_GROUP_NOT_FOUND`

### DELETE /api/v1/instance-groups/:id
- **描述**: 删除分组节点。非空（有子节点或成员实例）时**拒删**（提示先清空，不级联）；删组只解绑成员关系，不删实例
- **关联 FR**: FR-165
- **权限**: `instance:write`
- **响应**: `204`
- **错误**: 409 `INSTANCE_GROUP_NOT_EMPTY`（有子组或成员）；404 `INSTANCE_GROUP_NOT_FOUND`

### GET /api/v1/instance-groups/:id/instances
- **描述**: 返回该组「子树（含自身及后代）去重」的实例 ID 集合，供「按组（含子树）筛选」与右列表共用
- **关联 FR**: FR-165
- **权限**: `instance:read`
- **响应**: `{ "instanceIds": [10, 11, 23] }`
- **错误**: 404 `INSTANCE_GROUP_NOT_FOUND`

### POST /api/v1/instance-groups/:id/members
- **描述**: 批量将实例加入分组（幂等：已存在或不存在的实例跳过）
- **关联 FR**: FR-165
- **权限**: `instance:write`
- **请求**: `{ "instanceIds": [10, 11] }`
- **响应**: `200` `{ "added": 2, "members": [ { "instanceId": 10, "name": "…", "role": "backend", "nodeId": 2, "status": "RUNNING" } ] }`

### DELETE /api/v1/instance-groups/:id/members
- **描述**: 批量从分组移除实例（不影响实例本身）
- **关联 FR**: FR-165
- **权限**: `instance:write`
- **请求**: `{ "instanceIds": [10] }`
- **响应**: `204`

---

## 群组服关系模型（FR-032）

> 全部位于平台管理员路由组。详见 `docs/specs/network-resource-model/`。

### GET /api/v1/nodes/:id/ports
- **描述**: 查看某节点端口占用与分配范围
- **响应**: `{ nodeId, ranges:{serverPortBase,rconPortBase,rangeSize}, occupied:[{instanceId,name,role,serverPort,rconPort,queryPort}] }`

### GET /api/v1/nodes/:id/docker/images
- **描述**: 列出节点本机 Docker 镜像（CP 经 gRPC 委托 Worker，FR-078/ADR-019）
- **关联 FR**: FR-078
- **权限**: 平台管理员
- **响应**: `[{ "id":"sha256:…", "tags":["itzg/minecraft-server:latest"], "sizeBytes":123456789, "created":1700000000 }]`
- **错误**: `503 NODE_OFFLINE`（节点未连接）；`422 DOCKER_UNAVAILABLE`（节点未安装/未运行 Docker）

### POST /api/v1/nodes/:id/docker/images/pull
- **描述**: 在节点拉取镜像（默认 Docker Hub）
- **关联 FR**: FR-078
- **权限**: 平台管理员
- **请求**: `{ "image": "itzg/minecraft-server:latest" }`
- **响应**: `{ "message": "已拉取" }`；**错误**: `503 NODE_OFFLINE`、`502 DOCKER_OP_FAILED`

### POST /api/v1/nodes/:id/docker/images/remove
- **描述**: 在节点删除镜像（引用含 `/` 与 `:`，故放请求体而非路径参数）
- **关联 FR**: FR-078
- **权限**: 平台管理员
- **请求**: `{ "image": "itzg/minecraft-server:latest", "force": false }`
- **响应**: `{ "message": "已删除" }`；**错误**: `503 NODE_OFFLINE`、`502 DOCKER_OP_FAILED`

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

### POST /api/v1/instances/:id/files/search
- **描述**: 跨文件全文搜索 / 文件名快速打开。CP 仅经 gRPC 把查询转发到目标节点 Worker；索引是 Worker 本地派生资产（落数据根 `var/index/`，不进 CP 数据库，见 ADR-017）。索引**首建后台异步**（FR-113，见 ADR-024）：未就绪时本次返回 `indexing=true`（空命中），调用方稍后用同一查询重试；已就绪时查询前增量更新索引（指纹比对增/改/删），再倒排取候选文件、候选内精确行扫描返回命中
- **关联 FR**: FR-074, FR-113
- **权限**: `instance.file`（可访问实例）
- **请求**: `{ "query": "string", "mode": "content", "maxResults": 200 }`。`query` 必填；`mode` 取 `content`（默认，全文）或 `filename`（文件名子串匹配，行号为 0）；`maxResults<=0` 时由 Worker 取默认上限
- **响应**: `200`，`{ "hits": [{ "path": "plugins/config.yml", "line": 12, "snippet": "命中行片段" }], "truncated": false, "indexing": false }`。`path` 相对工作目录、以 `/` 分隔；`line` 1 起（filename 模式为 0）；`snippet` 仅 content 模式有值；`truncated=true` 表示命中达上限被截断；`indexing=true` 表示索引首建未就绪（`hits` 为空，应稍后用同一查询重试，FR-113）
- **错误**: `400 INVALID_REQUEST`（缺 query）；`404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（节点离线/工作目录未设置/搜索失败）

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

### GET /api/v1/instances/:id/files/archive/entries
- **描述**: 列出某归档（jar/zip）内全部条目（扁平，前端按「/」重建子树）。Worker 用 Go `archive/zip` 列举，不起进程、零落盘；条目名经 zip-slip 校验，单归档条目数上限 50000（超出 `truncated=true`）
- **关联 FR**: FR-075
- **权限**: `instance.file`（可访问实例，只读）
- **Query**: `?path=plugins/Foo.jar`（归档文件相对工作目录路径，必填）
- **响应**: `{ "entries": [{ "name": "plugin.yml", "isDir": false, "size": 320, "compressedSize": 210, "modified": 1700000000, "crc32": 123456 }], "truncated": false }`
- **错误**: `400 INVALID_REQUEST`（缺 path）；`404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（非归档/路径越界/节点离线）

### GET /api/v1/instances/:id/files/archive/read
- **描述**: 读取归档内某条目内容（文本预览，流式截断到上限 4 MiB）。返回原始字节，截断/二进制经响应头标注
- **关联 FR**: FR-075
- **权限**: `instance.file`（可访问实例，只读）
- **Query**: `?path=plugins/Foo.jar&entry=plugin.yml`（归档文件 + 归档内条目名，均必填）
- **响应**: `200`，`Content-Type: application/octet-stream`，响应头 `X-Truncated: true`（截断时）、`X-Binary: true`（嗅探为二进制时），响应体为条目原始字节
- **错误**: `400 INVALID_REQUEST`（缺 path/entry）；`404 NOT_FOUND`；`422 BUSINESS_ERROR`（非归档/条目不存在/目录条目/越界/节点离线）

### POST /api/v1/instances/:id/files/decompile
- **描述**: 反编译工作目录内 `.class`/`.jar`（或归档内某 `.class`）为 Java 源码。Worker 经实例绑定 JDK（或系统候选 JDK / `JAVA_HOME` 兜底）受控 exec CFR，仅静态分析字节码、不运行目标代码；超时 30s + 输入上限 16 MiB + 输出截断 4 MiB。**失败/降级以 `success:false`+`error` 在 `200` 体内返回**（无 JDK / 无 CFR / 超时 / 超限 / CFR 非 0 退出）
- **关联 FR**: FR-075
- **权限**: `instance.file`（可访问实例，只读）
- **请求**: `{ "path": "plugins/Foo.jar", "entry": "com/example/Foo.class" }`（`entry` 可选；`path` 为 `.class` 时忽略 `entry`；`path` 为 `.jar` 且 `entry` 空时反编译整个 jar）
- **响应**: `{ "success": true, "source": "/*\n * Decompiled with CFR 0.152.\n */\npublic class Foo { ... }\n", "truncated": false, "decompiler": "CFR 0.152" }`
- **降级响应**: `200`，`{ "success": false, "error": "无可用 JDK，反编译降级" }`
- **错误**: `400 INVALID_REQUEST`（缺 path）；`404 NOT_FOUND`；`422 BUSINESS_ERROR`（路径越界/节点离线）

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
- **描述**: 向 Bot 下发聊天/控制命令（链路：CP → Worker SendBotCommand → bot-worker send-command IPC → Mineflayer chat）
- **关联 FR**: FR-009
- **请求**: `{ "command": "/tp 0 64 0" }`
- **响应**: `200 { "message": "已发送" }`
- **错误**: 400 `INVALID_REQUEST`（缺 command）；404 `NOT_FOUND`（Bot 不存在/无权访问）；503 `COMMAND_FAILED`（Worker 未连接/委托失败）

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
- **描述**: 更新定时任务（`cronExpr`/`action`/`enabled` 可选；`action=command` 时可携 `payload` 改命令，FR-153）
- **关联 FR**: FR-012, FR-153

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

> FR-011 阈值告警在 FR-085 扩展为多通道 + 多触发类型 + 分级聚合静默 + 确认历史。
> 所有端点限平台/组管理员（沿用 protected 分组鉴权）。

### 告警规则

#### GET /api/v1/alerts/rules
- **描述**: 告警规则列表
- **关联 FR**: FR-011, FR-085

#### POST /api/v1/alerts/rules
- **描述**: 创建告警规则
- **关联 FR**: FR-011, FR-085
- **请求**（按 `triggerType` 取用相应字段）:
  ```json
  {
    "name": "High CPU",
    "triggerType": "metric",
    "level": "warn",
    "targetType": "node",
    "targetId": null,
    "metric": "cpu", "operator": ">", "threshold": 90, "durationSec": 60,
    "keyword": "",
    "eventMatch": "",
    "channelIds": [1, 2],
    "dedupWindowSec": 300,
    "silenceStart": "23:00", "silenceEnd": "07:00",
    "notifyRecover": true
  }
  ```
- **字段**:
  - `triggerType`: `metric` | `instance_crash` | `node_offline` | `log_keyword` | `player_event` | `backup_failed`（缺省 `metric`）
  - `level`: `info` | `warn` | `critical`（缺省 `warn`）
  - `keyword`: 仅 `log_keyword` 用；`eventMatch`: 仅 `player_event` 用（`join`/`quit`/`chat`/`cross_server`，空=任意）
  - `channelIds`: 路由的通知通道 ID 列表（空=不外发，仍入事件库 + 站内）
  - `dedupWindowSec`: 去抖聚合窗口；`silenceStart`/`silenceEnd`: 静默窗口（`HH:MM`，支持跨午夜）
  - `notifyType`/`notifyTarget`: 兼容 FR-011 单 webhook 直发（未配 `channelIds` 时回退）
- **错误**: `400 INVALID_REQUEST`（非法触发类型/级别）

#### PUT /api/v1/alerts/rules/:id
- **描述**: 更新告警规则可变字段（`triggerType`/`targetType` 不可改）
- **关联 FR**: FR-011, FR-085
- **请求**（均可选）: `enabled` `threshold` `level` `channelIds` `dedupWindowSec` `silenceStart` `silenceEnd` `notifyRecover` `keyword` `eventMatch`

#### DELETE /api/v1/alerts/rules/:id
- **描述**: 删除告警规则
- **关联 FR**: FR-011

### 告警事件

#### GET /api/v1/alerts/events
- **描述**: 告警事件分页列表（含规则名预加载，按触发时间倒序，FR-149）
- **关联 FR**: FR-011, FR-085, FR-149
- **Query**: `ruleId` `resolved`(true/false) `acknowledged`(true/false) `level` `triggerType` `keyword`(模糊匹配 message) `from`/`to`(RFC3339 时间范围) `page`(从 1 起) `pageSize`(默认 50)
- **响应**: `{ "items": [...], "total": <命中总数> }`；事件含 `level` `triggerType` `count`(聚合计数) `resolved` `acknowledged` `acknowledgedBy` `acknowledgedAt` `read`

#### GET /api/v1/alerts/events/unread-count
- **描述**: 未读告警数（站内角标）
- **关联 FR**: FR-085
- **响应**: `{ "unread": 3 }`

#### POST /api/v1/alerts/events/:id/ack
- **描述**: 确认/认领一条告警事件（记录确认人与时间，置已读）
- **关联 FR**: FR-085
- **错误**: `404 NOT_FOUND`

#### POST /api/v1/alerts/events/:id/read
- **描述**: 标记单条事件为已读
- **关联 FR**: FR-085

#### POST /api/v1/alerts/events/read-all
- **描述**: 标记全部未读事件为已读
- **关联 FR**: FR-085

### 通知通道（FR-085）

> 通道是可复用的通知出口，多条规则可路由到同一通道。凭证子字段（URL/token/password）
> 强制以 `${ENV_VAR}` 引用环境变量，落库不含明文（见 config-files 规范）。

#### GET /api/v1/alerts/channels
- **描述**: 通知通道列表
- **关联 FR**: FR-085

#### POST /api/v1/alerts/channels
- **描述**: 创建通知通道
- **关联 FR**: FR-085
- **请求**:
  ```json
  {
    "name": "运维钉钉",
    "type": "dingtalk",
    "enabled": true,
    "config": { "url": "${JM_DINGTALK_WEBHOOK}" }
  }
  ```
- **`type`**: `webhook` | `email` | `dingtalk` | `wecom` | `feishu` | `discord` | `telegram` | `inapp`
- **`config`（按类型）**:
  - webhook/dingtalk/wecom/feishu/discord: `{ "url": "${ENV}" }`
  - telegram: `{ "token": "${ENV}", "chatId": "..." }`
  - email: `{ "host", "port", "username", "password": "${ENV}", "from", "to" }`
  - inapp: `{}`
- **错误**: `400 INVALID_REQUEST`（凭证非 `${ENV}` 引用 / 必填缺失 / 非法类型）

#### PUT /api/v1/alerts/channels/:id
- **描述**: 更新通知通道
- **关联 FR**: FR-085

#### DELETE /api/v1/alerts/channels/:id
- **描述**: 删除通知通道
- **关联 FR**: FR-085
- **错误**: `409 CHANNEL_IN_USE`（被规则引用）、`404 NOT_FOUND`

#### POST /api/v1/alerts/channels/:id/test
- **描述**: 向通道发送一条测试通知（验证配置与连通性）
- **关联 FR**: FR-085
- **错误**: `502 TEST_SEND_FAILED`（投递失败，message 含原因）

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

## 运行时与制品全局页（FR-082）

> 只读聚合端点，给「运行时与制品」全局页一次性提供 JDK 矩阵 + 引用关系 + 制品占用/去重/冷热统计。
> 不引入新表/新 proto，跨现有表（`nodes`/`node_jdks`/`instances`/`assets`）聚合。
> 权限：平台管理员。删除受引用项仍走各自端点（JDK：`DELETE /nodes/:id/jdks/:jid`；制品：`DELETE /assets/:id`），本端点只展示引用。

### GET /api/v1/runtime-assets/overview
- **描述**: 跨节点 JDK 矩阵（每项含引用实例清单）+ 制品按类型分组（每组含占用/去重/冷热统计）+ 两区汇总
- **关联 FR**: FR-082（聚合 FR-033 JDK 绑定语义 + FR-045 制品库元数据）
- **引用解析**:
  - JDK 引用由实例绑定真实推导：`instances.jdk_id`（直接绑定，`binding=direct`）或 `instances.java_major_version`（按 Java 大版本绑定，解析到同节点同大版本中 id 最大者，`binding=major`）；跨节点不串台
  - 制品当前不持久化「实例↔制品」连接（FR-045 消费侧 `ref_count` 为占位，见 ADR-011），故制品区给「按类型」占用/去重/冷热 + 既有 `refCount`，不臆造实例连接
- **响应 200**:
```json
{
  "jdks": [
    {
      "id": 10, "nodeId": 1, "nodeName": "node-a", "nodeOnline": true,
      "vendor": "Temurin", "majorVersion": 21, "version": "21.0.4", "arch": "x64",
      "path": "/opt/jdks/temurin-21", "managed": true,
      "instances": [
        { "id": 100, "uuid": "<uuid>", "name": "paper-1", "status": "RUNNING", "binding": "direct" }
      ],
      "refCount": 1
    }
  ],
  "jdkSummary": { "nodeCount": 1, "jdkCount": 1, "referencedJdk": 1, "instanceRefs": 1 },
  "assets": [
    {
      "type": "core",
      "items": [ { "id": 1, "type": "core", "name": "paper-1.20.4", "sha256": "<64hex>", "size": 48234123, "refCount": 0, "storageState": "hot", "...": "（字段同 GET /assets 单条）" } ],
      "count": 1, "totalSize": 48234123, "referencedCount": 0,
      "hotCount": 1, "archivedCount": 0, "externalCount": 0
    }
  ],
  "assetSummary": { "assetCount": 1, "totalSize": 48234123, "referencedCount": 0, "hotCount": 1, "archivedCount": 0, "externalCount": 0 }
}
```
- **错误**: 401/403（非平台管理员）；500 `INTERNAL_ERROR`

---

## 平台存储（FR-083）

> 对 Control Plane 侧数据根（ADR-010 FHS 布局）只读浏览 + 占用统计 + 制品归档冷热可见 + `cache/` 受控清理。
> 数据根是平台级资源（仅 CP 读写，见架构不变量），故全部端点限平台管理员。Worker 侧数据根（`var/servers`、`opt/jdks` 落各节点本机）按节点经实例文件管理浏览，不在此组范围。

### GET /api/v1/storage/overview
- **描述**: 按固定 FHS 布局统计各子目录占用（大小/文件数）+ 用途标注 + 跨 `assets` 表聚合制品库冷热分布（FR-045 `storage_state`）
- **关联 FR**: FR-083（聚合 FR-044 数据根布局 + FR-045 归档状态）
- **权限**: 平台管理员
- **统计目录**（固定顺序，缺失目录仍列出且 `exists=false`）: `bin`、`etc`、`opt/jdks`、`var/servers`、`var/log`、`var/artifacts`、`cache`（仅 `cache` 的 `clearable=true`）
- **响应 200**:
```json
{
  "base": "/abs/path/to/data",
  "dirs": [
    { "path": "var/artifacts", "label": "artifacts", "size": 48234123, "fileCount": 12, "exists": true, "clearable": false },
    { "path": "cache", "label": "cache", "size": 1024, "fileCount": 2, "exists": true, "clearable": true }
  ],
  "totalSize": 48235147,
  "totalFiles": 14,
  "archive": { "hotCount": 3, "archivedCount": 1, "externalCount": 0, "hotSize": 48234123, "archivedSize": 2048, "externalSize": 0 }
}
```
- **错误**: 401/403（非平台管理员）；500 `INTERNAL_ERROR`

### GET /api/v1/storage/files
- **描述**: 列举数据根内某目录的直接子项（只读，目录在前再按名排序）。不读取文件内容
- **关联 FR**: FR-083
- **权限**: 平台管理员
- **Query**: `?path=var/artifacts`（相对数据根，以「/」分隔；空/省略表示数据根本身）
- **路径守卫**: `..` 折叠后经 `filepath.Rel` 二次校验，绝不逃出数据根；布局声明但未创建的目录返回空列表
- **响应 200**:
```json
[
  { "name": "core", "isDir": true, "size": 0, "modTime": 1719100000 },
  { "name": "index.json", "isDir": false, "size": 256, "modTime": 1719100001 }
]
```
- **错误**: 400 `INVALID_PATH`（路径越出数据根）；400 `NOT_A_DIR`（目标不是目录）；401/403；500 `INTERNAL_ERROR`

### POST /api/v1/storage/cache/clear
- **描述**: 清空 `cache/` 目录内容（受控清理，二次确认由前端强制）。仅删除 `cache/` 下直接子项、保留 `cache/` 本身，绝不触及其他目录
- **关联 FR**: FR-083（受控清理，FR-059 二次确认）
- **权限**: 平台管理员
- **响应 200**: `{ "removed": 2 }`（删除的条目数）
- **错误**: 401/403；500 `INTERNAL_ERROR`

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

## 面板自更新（FR-081 / FR-175）

> Control Plane 与各节点 Worker 的二进制在线升级（ADR-020 / ADR-036 §7）。均挂运营者浏览器 JWT 入口、**仅平台管理员**。
> **更新源**（FR-175，见 ADR-036 §7）：默认**原生读 GitHub Releases API**（`control-plane.yaml` 的 `update.github_repo`，默认 `wcpe/jianmanager`），`update.channel` 选 `stable`（取 `/releases/latest` 最新正式）或 `prerelease`（取滚动 `nightly` 预发布）；sha256 取自 release 的 `checksums.txt` 资产（ADR-036 §2 契约），资产名按 ADR-036 §1 命名 `<component>-<os>-<arch>[.exe]` 反解。`update.github_token` 可选，提升 GitHub API 限流额度（匿名 60 次/时）。`github_repo` 为空且 `feed_url` 非空时**回退**原 feed JSON 路径（FR-081）；二者均空即未配置。下载经 FR-174 出站代理。
> 升级类操作写审计（detail 仅含版本/节点元数据，绝不含下载 url 或凭据）。升级流程：下载目标版本制品 → **sha256 校验** → 替换二进制 → 平滑重启；Worker 升级经 CP gRPC 编排（`GetVersion`/`UpgradeWorker`），daemon 模式下不杀运行中的游戏服。

### GET /api/v1/self-update/check
- **描述**: 检查更新——按配置的更新源（GitHub Releases 或 feed）解析最新版本，对比 CP 自身与各节点当前版本，标注是否有更新及是否有匹配平台（component+os+arch）的制品
- **权限**: 平台管理员
- **关联 FR**: FR-081、FR-175
- **响应** (200): `{ "configured", "latestVersion", "notes", "source", "controlPlane": ComponentStatus, "nodes": [ComponentStatus] }`，其中 `source` 标更新源（`github:owner/repo@channel` | `feed` | 空），`ComponentStatus = { "nodeId?", "nodeUuid?", "name?", "online", "currentVersion", "os", "arch", "updateAvailable", "artifactAvailable" }`；节点当前版本经 gRPC `GetVersion` 实时拉取，离线节点 `online=false`
- **错误**: 409 `UPDATE_NOT_CONFIGURED`（未配置 github_repo / feed_url）| 429 `UPDATE_RATE_LIMITED`（GitHub API 限流，可配 github_token 提额）| 502 `UPDATE_CHECK_FAILED`（拉取/解析更新源失败）

### POST /api/v1/self-update/control-plane/upgrade
- **描述**: 升级 CP 自身（下载 → sha256 校验 → 替换 → 平滑重启）。替换成功后异步延迟重启，先返回 202
- **权限**: 平台管理员
- **关联 FR**: FR-081、FR-175
- **请求**: `{ "version": "可选，留空取更新源最新" }`
- **响应** (202): `{ "status": "restarting", "fromVersion", "toVersion" }`
- **错误**: 409 `UPDATE_NOT_CONFIGURED` / `UPDATE_ALREADY_LATEST` | 422 `UPDATE_NO_ARTIFACT`（更新源无匹配本平台制品）| 429 `UPDATE_RATE_LIMITED` | 502 `UPDATE_FAILED`
- **审计**: `self_update.control_plane`

### POST /api/v1/self-update/nodes/:id/upgrade
- **描述**: 经 gRPC 令目标节点下载校验替换并重启 Worker（daemon 模式下游戏服不掉）
- **权限**: 平台管理员
- **关联 FR**: FR-081、FR-175
- **请求**: `{ "version": "可选，留空取更新源最新" }`
- **响应** (202): `{ "status": "upgrading", "nodeId", "fromVersion", "toVersion" }`
- **错误**: 409 `UPDATE_NOT_CONFIGURED` | 422 `UPDATE_NO_ARTIFACT` | 429 `UPDATE_RATE_LIMITED` | 503 `NODE_OFFLINE`（节点未连接）| 502 `UPDATE_FAILED`
- **审计**: `self_update.node`

### POST /api/v1/self-update/nodes/upgrade-all
- **描述**: 全网逐节点升级编排（串行、异步）。同一时刻仅允许一个 rollout 运行中。`nodeIds` 省略=全部在线节点
- **权限**: 平台管理员
- **关联 FR**: FR-081
- **请求**: `{ "version": "可选", "nodeIds": [1, 2] }`
- **响应** (202): Rollout 快照（见下）
- **错误**: 409 `UPDATE_NOT_CONFIGURED` | 409 `ROLLOUT_BUSY`（已有全网升级进行中）
- **审计**: `self_update.rollout`

### GET /api/v1/self-update/rollout
- **描述**: 查询当前/最近一次全网升级编排进度（逐节点状态）。从未发起过返回 `state=idle` 空快照
- **权限**: 平台管理员
- **关联 FR**: FR-081
- **响应** (200): `{ "rolloutId", "targetVersion", "state"(idle|running|completed), "startedAt", "finishedAt", "total", "succeeded", "failed", "pending", "nodes": [ { "nodeId", "name", "state"(pending|upgrading|succeeded|failed), "fromVersion", "toVersion", "error", "attempts" } ] }`

---

## 客户端分发（频道 + 拉取密钥）

> 运营管理端点（运营者浏览器 JWT 入口，仅平台管理员）。面向玩家公网的 manifest/制品端点见 FR-087。
> 密钥落库只存 SHA-256 哈希，明文仅创建/轮换响应一次性返回、不可二次读取（FR-086、ADR-022）。
> 路径中 `:id` = 频道 slug（channelId）。

### GET /api/v1/client-channels
- **描述**: 列出全部分发频道（含密钥数量）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200):
  ```json
  [ { "id": 1, "channelId": "skyblock-s1", "name": "空岛一服", "description": "",
      "currentVersion": 0, "keyCount": 2, "createdAt": "datetime", "updatedAt": "datetime" } ]
  ```

### POST /api/v1/client-channels
- **描述**: 创建分发频道（每服一个）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **请求**: `{ "channelId": "skyblock-s1", "name": "空岛一服", "description": "可选" }`
- **响应** (201): 频道对象
- **错误**: 400 `INVALID_CHANNEL_ID`（slug 非法，须 `^[a-z0-9][a-z0-9-]{1,63}$`）| 409 `CHANNEL_EXISTS`

### GET /api/v1/client-channels/:id
- **描述**: 频道详情（含密钥元数据列表，无明文）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200):
  ```json
  { "id": 1, "channelId": "skyblock-s1", "name": "空岛一服", "description": "", "currentVersion": 0,
    "createdAt": "datetime", "updatedAt": "datetime",
    "keys": [ { "id": 10, "name": "正式包", "keyPrefix": "jmck_ab12", "revoked": false,
                "expiresAt": null, "lastUsedAt": null, "createdAt": "datetime" } ] }
  ```
- **错误**: 404 `CHANNEL_NOT_FOUND`

### PUT /api/v1/client-channels/:id
- **描述**: 更新频道名称/描述（channelId 不可改）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **请求**: `{ "name": "新名", "description": "新描述" }`
- **响应** (200): 频道对象
- **错误**: 404 `CHANNEL_NOT_FOUND`

### DELETE /api/v1/client-channels/:id
- **描述**: 删除频道及其全部拉取密钥
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): `{ "message": "已删除" }`
- **错误**: 404 `CHANNEL_NOT_FOUND`
- **审计**: `client_channel.delete`

### GET /api/v1/client-channels/:id/keys
- **描述**: 列出频道下拉取密钥（仅元数据，无明文）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): 密钥元数据数组（同详情 `keys`）
- **错误**: 404 `CHANNEL_NOT_FOUND`

### POST /api/v1/client-channels/:id/keys
- **描述**: 创建拉取密钥；**明文仅此响应返回一次，不可二次读取**
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **请求**: `{ "name": "正式包", "expiresAt": "2027-01-01T00:00:00Z" }`（`expiresAt` 可选）
- **响应** (201):
  ```json
  { "id": 10, "name": "正式包", "keyPrefix": "jmck_ab12", "revoked": false, "expiresAt": null,
    "createdAt": "datetime", "key": "jmck_<一次性明文>" }
  ```
- **错误**: 404 `CHANNEL_NOT_FOUND` | 400 `INVALID_REQUEST`
- **审计**: `client_key.create`（detail 不含明文）

### POST /api/v1/client-channels/:id/keys/:kid/rotate
- **描述**: 轮换密钥（同一记录换新明文，旧明文立即失效）；**新明文一次性返回**
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): 同创建响应（含一次性 `key`）
- **错误**: 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND`
- **审计**: `client_key.rotate`（detail 不含明文）

### DELETE /api/v1/client-channels/:id/keys/:kid
- **描述**: 吊销密钥（保留记录、标记 revoked，立即鉴权失效）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): `{ "message": "已吊销" }`
- **错误**: 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND`
- **审计**: `client_key.revoke`

---

## 客户端分发 manifest 与制品（FR-087/088）

> **鉴权分两组、物理隔离（ADR-022/023、contract §4）**：
> - **发布/版本管理端点**（运营操作）：`/api/v1` JWT，**仅平台管理员**（同频道管理 FR-086）。`POST .../files`、`POST .../versions`、`GET .../versions`、`GET .../versions/:version`、`POST .../rollback`。
> - **消费端点**（玩家）：**拉取密钥**鉴权（请求头 `X-Client-Key`，无 JWT），与运营浏览器入口隔离。`GET .../manifest`、`GET /client-artifacts/:sha256`。
>
> 理由：拉取密钥半公开（随整包分发必然泄露），用它鉴权「发布」=严重漏洞；内容可信靠 manifest 的 Ed25519 签名而非密钥。**版本历史仅管理面可见，玩家侧只认 latest**（FR-088）。
>
> **签名密钥 fail-closed（ADR-022 实施补充，粒度细化见 ADR-038）**：生产态（`dev_mode=false`）**未注入** `JIANMANAGER_CLIENT_SIGN_PRIVKEY` → Control Plane **降级启动**（视为未启用客户端 OTA：签名器不可用，发布 / 拉取签名 manifest 调用时返回「签名私钥未配置」，其余功能照常）；**误把源码公开的内置开发密钥贴进 env** → **拒绝启动**（配置错误快失败）。两种情况都绝不用开发密钥对外签 manifest；仅 `dev_mode=true` 零配置回退开发密钥。部署见 `docs/DEPLOY.md`。

### POST /api/v1/client-channels/:id/files
- **描述**: 上传客户端文件制品（入 FR-045 制品库 `type=client-file`，按制品自身 sha256 内容寻址去重）。返回的 `sha256` 即 manifest `files[].artifact.sha256`
- **关联 FR**: FR-087
- **鉴权**: **JWT，平台管理员**（运营操作）
- **请求**: `multipart/form-data` — `file`（必）、`codec`（可，`zstd`|`none`）、`expectedSha256`（可，制品自身 sha256 校验）
- **响应** (201): `{ "sha256": "ef56…", "md5": "cd34…", "size": 45678, "codec": "zstd" }`（`md5` 供发布向导填 `file.md5`；codec=none 时制品即原始内容，`sha256`/`md5`/`size` 可直接作 `files[]` 的解压后字段）
- **错误**: 400 `INVALID_REQUEST`（缺 file）| 404 `CHANNEL_NOT_FOUND` | 422 `CHECKSUM_MISMATCH`
- **审计**: `client_file.publish`

### POST /api/v1/client-channels/:id/versions
- **描述**: 发布版本并切 latest 指针。`version` 由服务端**单调递增分配**（防降级基准，contract §3），不接受客户端指定
- **关联 FR**: FR-087
- **鉴权**: **JWT，平台管理员**（运营操作）
- **请求**:
  ```json
  { "files": [ { "path": "mods/foo.jar", "sha256": "ab12…", "md5": "cd34…", "size": 123456,
                 "sync": "strict", "platform": null,
                 "artifact": { "sha256": "ef56…", "size": 45678, "codec": "zstd" } } ],
    "managedDirs": ["mods", "config"],
    "agent": { "wedge": { "version": 3 }, "core": { "version": 5, "platforms": { "windows": { "artifact": { "sha256": "…", "size": 0, "codec": "zstd" } } } } },
    "note": "首发" }
  ```
  - `files` 必填且非空；`path` 须 POSIX 相对路径不逃逸；`sync∈{strict,once,ignore}`；`platform∈{null,windows,macos,linux}`；非 `ignore` 文件须带 `artifact.sha256`
- **响应** (201): `{ "id": 1, "channelId": "skyblock-s1", "version": 1, "note": "首发", "createdAt": "datetime" }`
- **错误**: 400 `INVALID_REQUEST` / `INVALID_VERSION_FILES`（清单非法，含具体原因）| 404 `CHANNEL_NOT_FOUND`
- **审计**: `client_version.publish`

### GET /api/v1/client-channels/:id/versions
- **描述**: 版本历史列表（版本号 DESC）。**仅管理面**——玩家侧只认 latest，不经此端点拉取任意版本（FR-088）
- **关联 FR**: FR-088
- **鉴权**: **JWT，平台管理员**（运营操作）
- **响应** (200): `[ { "version": 2, "note": "…", "fileCount": 3, "createdBy": 1, "createdAt": "datetime", "isLatest": true }, { "version": 1, …, "isLatest": false } ]`
- **错误**: 404 `CHANNEL_NOT_FOUND`

### GET /api/v1/client-channels/:id/versions/:version
- **描述**: 版本详情（含完整文件清单 + 托管目录 + 自更新段），供管理台查看与回滚前确认（FR-088）
- **关联 FR**: FR-088
- **鉴权**: **JWT，平台管理员**（运营操作）
- **响应** (200): `{ "version": 1, "note": "…", "createdBy": 1, "createdAt": "datetime", "isLatest": false, "managedDirs": ["mods"], "files": [ { "path": "mods/foo.jar", "sha256": "…", "md5": "…", "size": 0, "sync": "strict", "platform": null, "artifact": { … } } ], "agent": { … } }`
- **错误**: 400 `INVALID_REQUEST`（版本号非法）| 404 `CHANNEL_NOT_FOUND` / `VERSION_NOT_FOUND`

### POST /api/v1/client-channels/:id/rollback
- **描述**: 运营回滚——取历史版本 `sourceVersion` 的内容，**以更高版本号重发为新 latest**（保持 `version` 单调，客户端按防降级正常前进、不被拒，ADR-022 §3 / contract §3）。不下发更低版本号
- **关联 FR**: FR-088
- **鉴权**: **JWT，平台管理员**（运营操作）
- **请求**: `{ "sourceVersion": 1, "note": "可选，空则生成「回滚至 vN」" }`
- **响应** (201): `{ "id": 7, "channelId": "skyblock-s1", "version": 3, "sourceVersion": 1, "note": "回滚至 v1", "createdAt": "datetime" }`
- **错误**: 400 `INVALID_REQUEST`（缺/非法 sourceVersion）| 404 `CHANNEL_NOT_FOUND` / `VERSION_NOT_FOUND`
- **审计**: `client_version.rollback`

### GET /api/v1/client-dist/events
- **描述**: 拉取/下载明细检索（FR-093 全链路追踪）。明细短保留（默认 14 天滚动清理）；发布事件审计见 `/audit`（`client_*.publish`/`rollback`）
- **关联 FR**: FR-093
- **鉴权**: **JWT，平台管理员**
- **查询参数**: `channelId` / `machineId` / `ip` / `kind`(manifest|artifact) / `version` / `since`(RFC3339) / `until`(RFC3339) / `limit`(默认 200，上限 1000)
- **响应** (200): `[ { "id", "channelId", "machineId", "ip", "kind", "version", "artifactSha", "bytes", "status", "durationMs", "createdAt" } ]`（created_at DESC）

### GET /api/v1/client-dist/ip-rules
- **描述**: 列出分发端点 IP 防护规则（FR-096 L7 防护）
- **关联 FR**: FR-096 | **鉴权**: **JWT，平台管理员**
- **响应** (200): `[ { "id", "cidr", "mode"(deny|allow), "note", "createdBy", "createdAt" } ]`

### POST /api/v1/client-dist/ip-rules
- **描述**: 新增 IP 防护规则（运行时生效、入审计）。`mode=deny` 黑名单（deny 优先）；`mode=allow` 存在即白名单模式
- **关联 FR**: FR-096 | **鉴权**: **JWT，平台管理员**
- **请求**: `{ "cidr": "1.2.3.4 或 10.0.0.0/8", "mode": "deny|allow", "note": "可选" }`
- **响应** (201): 规则对象 | **错误**: 400 `INVALID_IP_RULE`（CIDR/mode 非法）
- **审计**: `client_ip_rule.add`

### DELETE /api/v1/client-dist/ip-rules/:id
- **描述**: 删除 IP 防护规则（运行时生效、入审计）
- **关联 FR**: FR-096 | **鉴权**: **JWT，平台管理员**
- **响应** (200): `{ "message": "已删除" }` | **错误**: 404 `IP_RULE_NOT_FOUND`
- **审计**: `client_ip_rule.remove`

### GET /api/v1/client-dist/protection-stats
- **描述**: 防护拦截计数（FR-096 可观测；内存计数、不写库）
- **关联 FR**: FR-096 | **鉴权**: **JWT，平台管理员**
- **响应** (200): `{ "denyBlocked", "rateLimited", "concurrencyLimited" }`

### GET /api/v1/client-dist/stats
- **描述**: 分发统计后台（FR-095）：只读聚合 FR-093/094/092 数据，按频道 + 时间窗
- **关联 FR**: FR-095 | **鉴权**: **JWT，平台管理员**
- **查询参数**: `channelId`（频道）、`days`（窗口天数，默认 30，上限 365）
- **响应** (200): `{ "channelId", "days", "downloads":[{day,requests,bytes}], "versions":[{version,requests}], "results":[{result,count}], "successRate", "rollbackRate", "activeMachines", "topIps":[{ip,count}] }`

### GET /api/v1/client-dist/updater-jars
- **描述**: 内嵌客户端更新器 jar 的版本与可用性（FR-107 接入引导，供前端展示 + 禁用缺失下载）
- **关联 FR**: FR-107 | **鉴权**: **JWT，平台管理员**
- **响应** (200): `{ "version", "wedge": {"available", "size"}, "core": {"available", "size"} }`

### GET /api/v1/client-dist/updater-jars/:component
- **描述**: 下载内嵌更新器 jar（`component` ∈ `wedge` | `core`），供运营方接入（FR-107）。属管理面，走 JWT、不用拉取密钥
- **关联 FR**: FR-107 | **鉴权**: **JWT，平台管理员**
- **响应** (200): jar 二进制（`Content-Type: application/java-archive`、`Content-Disposition: attachment; filename=...`）
- **错误**: 400 `INVALID_COMPONENT`（非 wedge/core）| 404 `JAR_NOT_EMBEDDED`（构建未 `make embed-client-updater`）

### GET /api/v1/client-channels/:id/manifest
- **描述**: 返回频道 **latest** 的**签名 manifest**（contract §2）。只提供当前版本，不暴露历史
- **关联 FR**: FR-087、FR-092（机器码登记）
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必）；`X-Machine-Id`（可，机器码统计/辅助限流）。**无 JWT**
- **机器码登记（FR-092）**: 鉴权通过后若 `X-Machine-Id` 非空，则 best-effort 登记入 `client_machines`（弱一致、失败不阻断）。机器码**客户端生成、不可信**，仅统计 + 辅助限流（限流主键 IP，FR-096），**不作授权依据**
- **响应** (200): contract §2 的签名 manifest（含 `sig.alg=Ed25519`、`sig.keyId`、`sig.value`）
  - Headers：`ETag: "<version>:<keyId>"`、`Cache-Control: no-cache`（弱缓存，靠 ETag 命中省传输）
- **响应** (304): `If-None-Match` 命中 ETag（Not Modified）
- **错误**: 401 `INVALID_CLIENT_KEY`（无/无效/吊销/过期 key）| 404 `CHANNEL_NOT_FOUND` / `NO_LATEST_VERSION`（频道尚未发布版本）

### GET /api/v1/client-artifacts/:sha256
- **描述**: 按内容寻址下载客户端制品（zstd 压缩流或原文，按 codec）。制品跨频道共享，路径无频道段
- **关联 FR**: FR-087
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必，任一有效密钥即授权）；`X-Machine-Id`（可）。**无 JWT**
- **响应** (200/206): 二进制制品；支持 `Range`（断点续传，206 部分内容）；强缓存（内容寻址不可变，`Cache-Control: public, max-age=31536000, immutable` + `ETag` 为内容 sha256）
- **错误**: 401 `INVALID_CLIENT_KEY` | 404 `ARTIFACT_NOT_FOUND` | 416（Range 越界，由 `http.ServeContent` 处理）

### POST /api/v1/client-channels/:id/pack
- **描述**: 把频道 latest 版本打成 `.jmpack`（复用已存制品 + Ed25519 签名）入库 `type=client-pack`（FR-097）
- **关联 FR**: FR-097 | **鉴权**: **JWT，平台管理员**
- **响应** (201): `{ "sha256", "md5", "size", "codec" }`（.jmpack 制品元数据）
- **错误**: 404 `CHANNEL_NOT_FOUND` / `NO_LATEST_VERSION` / `ARTIFACT_NOT_FOUND` | 400 `INVALID_VERSION_FILES`
- **审计**: `client_pack.create`

### POST /api/v1/client-telemetry
- **描述**: 客户端遥测上报（FR-094，contract §4.3）。**best-effort、202 不阻塞**；隐私可关在客户端
- **关联 FR**: FR-094
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必，任一有效密钥）；`X-Machine-Id`（可）。**无 JWT**
- **请求**: `{ "channel", "result"(success|fail-static|rolled-back|error), "fromVersion", "toVersion", "os", "javaVersion", "launcher", "durationMs", "bootSuccess", "error"? }`
- **响应** (202): 无体（落库失败不影响响应）
- **错误**: 401 `INVALID_CLIENT_KEY`

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
