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
- **描述**: 节点实时指标快照（CPU/内存/磁盘）。节点已连接时 CP 经 Worker `GetNodeMetrics` 主动拉取；Worker 暂未连接时回退节点最新心跳快照，避免实时面板因连接池短暂缺失直接不可用。
- **关联 FR**: FR-010
- **权限**: 平台管理员
- **响应**: `{ cpuUsage, memoryUsage, diskUsage, memoryUsedMb, memoryTotalMb, diskUsedMb, diskTotalMb }`，其中 `cpuUsage`/`memoryUsage`/`diskUsage` 为 `0~1` 比例值。
- **错误码**: `404 NOT_FOUND`（节点不存在），`503 METRICS_UNAVAILABLE`（Worker 已连接但实时指标拉取失败）

### GET /install-worker.sh ; GET /install-worker.ps1
- **描述**: CP 匿名静态托管 Worker 一键安装脚本（Linux/macOS 的 `.sh`、Windows 的 `.ps1`）。一键命令拼 `curl <cp>/install-worker.sh | sh` / `iex (iwr <cp>/install-worker.ps1 -UseBasicParsing).Content`，依赖 CP 自托管这两路径
- **关联 FR**: FR-080（见 ADR-020 §2「也可由 CP 静态托管」）
- **路径**: 根路径（**非** `/api/v1`），显式注册、先于前端 SPA `NoRoute` 回退
- **权限**: 匿名（无 JWT）。脚本不含机密；准入凭据 enrollment token 在一键命令参数里、不在脚本里，故与签发 token 的平台管理员 JWT 端点暴露面/鉴权物理隔离
- **响应**: `200` 脚本字节（`.sh` → `text/x-shellscript`、`.ps1` → `text/plain`）；脚本未内嵌时 `503 INSTALL_SCRIPT_UNAVAILABLE`（不回退到 `index.html`）
- **内容来源**: `go:embed` 内嵌进 CP 二进制（源 = canonical `scripts/install-worker.{sh,ps1}`，`make embed-install-scripts` 同步、字节一致由测试守护）

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
    "scriptBaseUrl": "https://cp-host",
    "installCommandLinux": "curl -fsSL https://cp-host/install-worker.sh | sh -s -- --control-plane cp-host:9100 --token jmet_xxx --download-url 'https://cp-host/worker-assets/v0.13.0/{os}/{arch}/worker?token=...'",
    "installCommandWindows": "iex (iwr https://cp-host/install-worker.ps1 -UseBasicParsing).Content; Install-JianManagerWorker -ControlPlane cp-host:9100 -Token jmet_xxx -DownloadUrl 'https://cp-host/worker-assets/v0.13.0/{os}/{arch}/worker?token=...'"
  }
  ```
- token **落库只存 SHA-256 哈希**，明文一次性返回、不可二次读取；`controlPlaneGrpc`/`scriptBaseUrl` 由 CP 据请求 Host 推断，可经 `enroll.advertise_grpc`/`enroll.script_base_url` 配置覆盖。`scriptBaseUrl` 为 CP 托管脚本基址，前端据此拼「手动安装步骤」分步兜底命令
- 一键命令的脚本由 CP 托管（见上 `GET /install-worker.{sh,ps1}`）；二进制下载默认签发 CP-local Worker 资产 URL 模板（FR-190，`/worker-assets/:version/{os}/{arch}/worker?token=...`，脚本按运行时平台替换），`enroll.binary_url` 非空时可显式覆盖为内网源，离线场景仍可用脚本 `--binary` 本地兜底
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
- **说明**: 返回**裸数组全量**（无分页）。面向 1000+ 规模请改用下面的 `GET /instances/search`（分页）+ `GET /instances/aggregate`（计数）。

### GET /api/v1/instances/search
- **描述**: 分页搜索实例（FR-247，面向 1000+）。名称子串 + 多维筛选 + 排序 + 分页，避免前端全量拉。
- **关联 FR**: FR-247
- **权限**: `instance.read`（非平台管理员强制按可访问组作用域，忽略 `groupId`）
- **Query**（均可选，AND 组合）:
  - `q` 名称子串（大小写不敏感）
  - `nodeId` / `status` / `role` / `groupId` / `networkId` / `env` / `tag`（语义同 `GET /instances`）
  - `sort` `name`|`status`|`createdAt`|`nodeId`（默认 `name`；非法值回退 `name`）
  - `order` `asc`|`desc`（默认 `asc`）
  - `page` 1 基（默认 `1`，<1 归 1）
  - `pageSize` 默认 `50`，上限 `200`（超过截断）
- **响应**:
  ```json
  { "items": [ /* InstanceInfo[]（字段同 GET /instances 元素） */ ], "total": 1234, "page": 1, "pageSize": 50 }
  ```
  `total` 为当前筛选下全量条数；越界 `page` → `items: []` 且 `total` 不变。
- **示例**: `?q=survival&status=RUNNING&sort=name&order=asc&page=2&pageSize=50`

### GET /api/v1/instances/aggregate
- **描述**: 实例维度计数（FR-247）。同筛选下按状态/节点/角色分组计数，供前端筛选 chip / 分组头不拉全集即得计数。
- **关联 FR**: FR-247
- **权限**: `instance.read`（作用域同 search）
- **Query**: 与 `search` 相同的筛选维度（`q` + `nodeId/status/role/groupId/networkId/env/tag`），honor 全部传入项。渲染"某维度 chip 全量计数"时调用方自行省略该维度的筛选。
- **响应**:
  ```json
  {
    "total": 1234,
    "byStatus": { "STOPPED": 800, "STARTING": 2, "RUNNING": 400, "STOPPING": 1, "CRASHED": 31 },
    "byNode":   [ { "nodeId": 1, "count": 900 }, { "nodeId": 2, "count": 334 } ],
    "byRole":   { "backend": 1000, "proxy": 30, "universal": 204 }
  }
  ```
  `byStatus`/`byRole` 含全部枚举键（零补 0）；`byNode` 仅含出现的节点（按 nodeId 升序）。

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
- **说明**: `processType=docker` 时 `image` 必填（容器镜像引用，默认 Docker Hub，本地缺失时启动前自动拉取，FR-078/ADR-019）；其它启动方式忽略 `image`。docker 模式宿主端口（FR-032 端口池分配）映射到容器内端口（MC 约定 25565），工作目录 bind-mount 到容器 `/data`。`cpuLimit`（核数，可小数）/`memLimitMb`（MiB）/`diskLimitMb`（MiB）为 docker 模式资源限额（FR-079/ADR-019），`0`、负值或缺省=不限制，仅 docker 模式生效（其它启动方式忽略）；启动时 `cpuLimit`→`HostConfig.NanoCPUs`、`memLimitMb`→`HostConfig.Memory` 注入容器 cgroup，`diskLimitMb` 当前仅持久化展示，不向 bind mount 假装注入强制限制。

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
- **说明**: `tags` 传数组（含空数组 `[]` 清空）覆盖标签；环境维度复用 `env:` 前缀（FR-047），无独立字段。`cpuLimit`/`memLimitMb`/`diskLimitMb` 为 docker 模式资源限额（FR-079），传值覆盖、缺省/`null` 不变；`0` 或负值会归一化为不限制。变更对实例下一次启动生效，仅 docker 模式生效；磁盘限额当前仅持久化展示，不强制限制 bind mount 工作目录。

### DELETE /api/v1/instances/:id
- **描述**: 删除实例（需先停止）。节点在线时经 gRPC `RemoveInstance` 让 Worker 删除工作目录与派生索引，清理成功才删记录（兑现「所有数据将被删除」），清理失败返回 500 并透传原因、记录保留可重试；节点未连接时仅删记录并告警（目录残留在节点上）；托管区（数据根 `var/servers`）外的历史手填工作目录 Worker 拒删文件但放行记录删除
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

### GET /api/v1/metrics/processes/top
- **描述**: 返回受管实例进程 TOPN 最新快照。Worker 只上报受 JianManager 管理的实例根进程及子进程，命令摘要已截断/脱敏，不返回完整命令行或环境变量
- **关联 FR**: FR-170 ｜ **关联 ADR**: ADR-060
- **权限**: 登录；指定 `instanceId` 时按 `instance.read` 收敛；未指定 `instanceId` 的全局/节点视图仅平台管理员可用
- **Query**: `instanceId?`（数值 ID）、`nodeId?`（node_uuid）、`sort=cpu|memory|io`（默认 cpu）、`limit=1..50`（默认 10）
  - `instanceId` 与 `nodeId` 均为空时返回平台范围进程 TOPN，仅平台管理员可用
- **响应**:
  ```json
  [
    {
      "instanceId": 1,
      "instanceUuid": "uuid",
      "nodeUuid": "node-uuid",
      "pid": 1234,
      "name": "java",
      "cpuPercent": 42.5,
      "rssBytes": 536870912,
      "readBytesPerSec": 0,
      "writeBytesPerSec": 0,
      "user": "minecraft",
      "commandSummary": "java -Xmx4G -jar server.jar",
      "sampledAt": "2026-07-06T12:00:00Z"
    }
  ]
  ```
- **错误**: 400 `INVALID_SORT`/`INVALID_INSTANCE`；403 `FORBIDDEN`；404 `TARGET_NOT_FOUND`

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
- **描述**: 探针在线更新状态（FR-068/FR-114）：探针连接状态 + CP 内嵌最新探针版本/指纹 + 上次推送时间；构建跑过 `make embed-probe` 时 CP 同时内嵌探针运行库缓存包，推送阶段随 gRPC 下发
- **权限**: `instance.read`
- **响应**: `{ "instanceId":3, "instanceUuid":"...", "probeConnected":true, "embeddedVersion":"0.1.0", "embeddedFingerprint":"<sha256 前缀>", "embeddedAvailable":true, "lastPushedAt":"2026-06-22T10:00:00Z" }`（`embeddedAvailable=false` 表示本次构建未 `make embed-probe`，无可推 jar）
- **关联 FR**: FR-068 ｜ **关联 ADR**: ADR-016

### POST /api/v1/instances/:id/probe/update
- **描述**: 把 CP 内嵌最新探针 jar 经 gRPC `DeployServerProbe` 推到该实例 `plugins/` 目录，并同步下发 FR-114 `libraries_zip` 到实例根 `libraries/`（**下次重启生效**）；`restart=true` 时推送后立即重启实例使其生效（FR-068/FR-114）
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
- **背包域当前写能力**: `domain=inventory, action=writeBasicAttrs` 写玩家基础属性；payload 形如 `{player, base:{dataVersion,basicAttrs}, edited:{dataVersion,basicAttrs}}`。物品发放/回收因 AllinInventorySync 2.0.0 未导出可外部消费的结构化物品写门面，当前不在 manifest 中
- **审计**: 写动作记 `business.write`（detail 含 domain/action/operationId/reason/available）；`inventory.writeBasicAttrs` 额外记录 `player` 与变更字段列表 `fields`；审计中间件兜底记 `business.dispatch`（覆盖读+写）
- **错误**: `400 INVALID_REQUEST`（缺 domain/action 或 payload 非法 JSON）、`403 FORBIDDEN`（读缺 `instance.operate` / 写缺 `instance.business.write`）、`404 NOT_FOUND`（实例不可见/不存在）
- **关联 FR**: FR-116, FR-121 ｜ **关联 ADR**: ADR-026, ADR-027, ADR-029

### GET /api/v1/instances/:id/business/manifest
- **描述**: 取某实例的业务能力清单（JBIS 元查询，FR-116）。CP 复用业务下发通道下发保留元命令（`domain=jbis` + `action=manifest`），探针侧 `BusinessHost` 返回各业务 Provider 汇总的能力清单 JSON（`{"domains":{...}}`），供前端**动态发现各域能力、动态渲染**（不硬编码具体插件）。元命令不派发到任何业务 Provider
- **权限**: `instance.read`（只读发现；且实例须可访问）
- **响应**: `200`，`{ "instanceId":3, "domain":"jbis", "action":"manifest", "available":true, "output": { "domains": { "economy": { "actions":[{"action":"balance","args":["player","currency"],"readOnly":true}] } } }, "error":"" }`
  - `available=false`：探针未连入/无业务 Provider → `output` 为 `null` + `error`（HTTP 200，降级不 5xx）
- **错误**: `403 FORBIDDEN`（无 `instance.read`）、`404 NOT_FOUND`（实例不可见/不存在）
- **关联 FR**: FR-116 ｜ **关联 ADR**: ADR-026, ADR-027

### GET /api/v1/business/economy/mirror
- **描述**: 经济余额镜像（FR-122，CP 自有汇聚镜像、非业务真源）：逐 `node→zone` 行返回最新余额（跨区同名玩家分行不混）。Query `?player=&currency=&node=&zone=&limit=`（任意组合过滤）
- **权限**: `instance.read`；非平台管理员按当前用户可访问实例 UUID 下沉过滤
- **响应**: `{ "balances":[{ "nodeUuid":"","instanceUuid":"","zoneId":"0","playerName":"Steve","currency":"coin","currencyId":1,"balance":"100.00","lastSeq":3,"lastLedgerId":7,"occurredAt":0 }] }`（余额字符串承载 BigDecimal，禁浮点）
- **关联 FR**: FR-122 ｜ **关联 ADR**: ADR-028

### GET /api/v1/business/economy/aggregate
- **描述**: 跨区经济聚合明细（FR-122）：按 `player`+`currency` 逐 `node→zone` 返回**不盲目求和**（mce 账户按 zone 隔离，是否相加由调用方按业务语义定）。Query `?player=&currency=`
- **权限**: `instance.read`；非平台管理员按当前用户可访问实例 UUID 下沉过滤
- **响应**: `{ "rows":[{ "nodeUuid":"","zoneId":"0","balance":"100.00" }] }`
- **关联 FR**: FR-122 ｜ **关联 ADR**: ADR-028

### GET /api/v1/business/economy/leaderboard
- **描述**: 某货币余额倒序 Top-N（FR-123 旁路排行：mce 无 leaderboard API，从 JM 自有镜像表派生、不穿透探针；按 DB 方言数值 CAST 排序，避免 BigDecimal 字符串字典序错排）。逐 `node→zone` 行参与排行
- **权限**: `instance.read`；非平台管理员按当前用户可访问实例 UUID 下沉过滤
- **请求**: Query `?currency=（必填）&zone=&node=&limit=`（默认 100，上限 500）
- **响应**: `{ "currency":"coin", "rows":[{ "rank":1,"playerName":"Steve","currency":"coin","nodeUuid":"","zoneId":"0","balance":"100.00" }] }`
- **关联 FR**: FR-123 ｜ **关联 ADR**: ADR-028

### GET /api/v1/business/events
- **描述**: 通用业务事件流（FR-122，按 `(domain,dedupKey)` 去重的 envelope 表，CP 自有汇聚）。经济流水由 `?domain=economy` 过滤后前端解析信封。Query `?domain=&node=&limit=`
- **权限**: `instance.read`；非平台管理员按当前用户可访问实例 UUID 下沉过滤
- **响应**: `{ "events":[{ "domain":"economy","dedupKey":"<ledgerId>","action":"","nodeUuid":"","instanceUuid":"","operator":"","payloadJson":"{...}","occurredAt":0 }] }`
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
- **Query**: `type=paper`（默认）/`velocity`/`waterfall`（PaperMC API）/`bungeecord`（md-5 Jenkins，仅 `latest`）/`spongevanilla`（Sponge 官方 Maven metadata）、`mcVersion`、`build`（可选，PaperMC 缺省最新；SpongeVanilla 按 MC 版本取最新 artifact）
- **响应（带 mcVersion）**: `{ "type":"paper","mcVersion":"1.21.1","build":196,"filename":"...","downloadUrl":"...","sha256":"..." }`
- **关联 FR**: FR-034, FR-046

### POST /api/v1/instances/provision/server
- **描述**: 一键搭建后端子服：支持 `paper`、`spongevanilla` 等非代理核心；解析核心 → 分配端口 → 系统分配目录 + 结构化启动 → 下载核心 + 写 eula/server.properties，返回实例（STOPPED，可一键启动）
- **权限**: 平台管理员
- **请求**: `{ "nodeId":1,"name":"lobby","coreType":"spongevanilla","mcVersion":"1.21.1","build":0,"jdkId":1,"memoryMb":4096,"jvmArgs":["-XX:+UseG1GC"],"groupId":0,"onlineMode":false }`（`onlineMode` 缺省 false=代理就绪/离线；独立正版服可传 true）
- **响应**: `201` 创建的 Instance；`502 PROVISION_FAILED`（含已创建实例供重试/删除）；代理核心会返回 `422 PROVISION_FAILED`
- **关联 FR**: FR-034, FR-046

### POST /api/v1/instances/provision/bukkit
- **描述**: 旧 Paper/Bukkit 兼容入口，内部复用 `POST /instances/provision/server`；新增前端能力不再通过该入口创建 SpongeVanilla
- **权限**: 平台管理员
- **请求**: `{ "nodeId":1,"name":"lobby","coreType":"paper","mcVersion":"1.21.1","build":0,"jdkId":1,"memoryMb":4096,"jvmArgs":["-XX:+UseG1GC"],"groupId":0,"onlineMode":false }`（`onlineMode` 缺省 false=代理就绪/离线；独立正版服可传 true）
- **响应**: `201` 创建的 Instance；`502 PROVISION_FAILED`（含已创建实例供重试/删除）
- **关联 FR**: FR-034, FR-046

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
- **描述**: 复制 backend 子服为独立新实例（同节点）：系统分配新目录/端口 → CloneWorkDir 复制 → 修正 server.properties 端口/rcon/motd/level-name（保留 forwarding secret）→ 可选注册进代理。`dryRun=true` 仅预检。**复制模式（FR-231）**：`mode=quick` 只复制核心 jar + plugins/ + 根配置（server.properties 及根 *.yml/*.properties，不含 world/logs/cache）；`mode=advanced`（或省略）按 `include`/`exclude` 筛选（include 空=复制全部）；两者都始终排除运行态垃圾
- **权限**: 平台管理员
- **请求**: `{ "name":"lobby-2","motd":"","levelName":"","registerToProxyIds":[30],"dryRun":false, "mode":"quick", "include":["plugins","*.jar"], "exclude":["world"] }`
  - `mode`: `quick` / `advanced`（省略=advanced）；`include`/`exclude`: 顶层项的目录前缀或 basename glob（如 `plugins`、`*.jar`），仅 advanced 用
- **响应**: `201`（dryRun `200`）`{ instance?, allocated, excluded, registrations, warnings, dryRun }`（`excluded` 反映该次实际排除集）；`422 NOT_A_BACKEND`/`SOURCE_RUNNING`；`502 CLONE_FAILED`
- **关联 FR**: FR-036, FR-231 | **Spec**: `docs/specs/clone-instance/`, `docs/specs/instance-clone-modes/`

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

### POST /api/v1/nodes/:id/docker/check
- **描述**: 探测节点本机 Docker 守护进程可用性（CP 经 gRPC 委托 Worker `CheckDocker`，FR-237）；供创建向导选 docker 模式前先测、不可用即禁用提交
- **关联 FR**: FR-237
- **权限**: 平台管理员
- **响应**: `{ "available": true, "version": "29.4.1" }` 或 `{ "available": false, "error": "连接本机 Docker 守护进程失败: …" }`（Docker 不可用不作错误，返 200）
- **错误**: `503 NODE_OFFLINE`（节点未连接）

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
- **说明**: `wsUrl` 指向 CP 代理端点，host 取浏览器请求的 Host（支持非 localhost 访问）；scheme 跟随访问协议——经 TLS 直连或反代标注 `X-Forwarded-Proto: https` 时为 `wss`，否则 `ws`，避免 HTTPS 页面连 `ws` 被混合内容策略拦截。前端连接时以 `?token=` 附加 token。token 用 CP↔Worker 专用 **WS 令牌密钥**签发（FR-275，见 ADR-061），CP 代理与 Worker 各校验一次。
- **错误诊断（FR-276）**: CP 代理连 Worker 握手被 401/403 拒绝时，经已升级的浏览器 WS 连接下发结构化错误后关闭：
  ```json
  {"type":"state","state":"error","code":"WORKER_TOKEN_REJECTED","data":"终端令牌被 Worker 拒绝（HTTP 401）：该节点的 WS 令牌密钥与平台不一致。…"}
  ```
  网络类拨号失败无 `code` 字段（`data` 为「连接 Worker 失败: …」），前端据 `code` 区分定向诊断与一般断连。

---

## 文件管理

### GET /api/v1/instances/:id/files
- **描述**: 文件列表
- **关联 FR**: FR-008
- **权限**: `instance.file`
- **Query**: `?path=/plugins`

### GET /api/v1/instances/:id/files/read
- **描述**: 读取文件内容（在线编辑器用；Worker `ReadFile` 带 10MiB 编辑器护栏，超限截断——下载文件请用 `download` 端点，不受此上限）
- **关联 FR**: FR-008
- **Query**: `?path=plugins/essentials/config.yml`

### POST /api/v1/instances/:id/files/write
- **描述**: 写入文件内容
- **关联 FR**: FR-008
- **请求**: `{ "path": "string", "content": "string" }`

### POST /api/v1/instances/:id/files/upload
- **描述**: 文件流式上传（multipart）。CP 流式读 multipart 并经 Worker `UploadFile` client-stream 分块转发（任意大小、双侧内存 O(chunk)、无固定短超时）；Worker 落临时文件收完原子改名，中途失败不留半截目标文件。老 Worker（无 `UploadFile`）自动回退 `WriteFile` unary（≤64MB），超限明确报错引导升级节点
- **关联 FR**: FR-008 / FR-304
- **权限**: `instance.file`（可管理实例）
- **Query**: `?path=plugins/a.jar`（目标路径**首选经 query 传递**；兼容先于 `file` 部分的 multipart `path` 字段——CP 流式顺序读，读到 `file` 时必须已知目标路径）
- **请求体**: multipart，`file` 部分为文件内容
- **错误**: `400 INVALID_REQUEST`（缺 path / 缺文件 / 非法 multipart）；`404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（节点离线/路径非法/老 Worker 超 64MB/完整性校验不符）

### GET /api/v1/instances/:id/files/download
- **描述**: 单文件流式下载。经 Worker `DownloadFile` 服务端流原样分块返回（不打包、任意大小不截断），CP 逐帧转写响应体并 `Flush`；响应携带 `Content-Length`（源文件总大小），流中途失败即字节数不符，客户端按下载失败处理，不会拿到「看似成功」的半截文件
- **关联 FR**: FR-008
- **权限**: `instance.file`（可访问实例）
- **Query**: `?path=plugins/essentials/config.yml`
- **响应**: `200`，`Content-Type: application/octet-stream`，`Content-Disposition: attachment`
- **错误**: `400 INVALID_REQUEST`（缺 path）；`404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（节点离线/文件不存在/目标是目录/路径非法；老 Worker 不支持流式下载时明确报错引导升级，**不回退**会截断的 `ReadFile`）

### POST /api/v1/instances/:id/files/archive
- **描述**: 批量打包下载。把选中的若干文件/目录（目录递归）即时打包为 **zip** 流式返回。Worker 边遍历边打包边流式发送（不全量缓冲整包），CP 把 gRPC 流转为 HTTP `application/zip` 响应体；打包开始前失败仍返回 JSON 错误
- **关联 FR**: FR-070
- **权限**: `instance.file`（可访问实例）
- **请求**: `{ "paths": ["plugins", "server.properties", "world/level.dat"] }`（相对工作目录，非空；每条经路径校验：禁 `..`/前导 `/`；目录递归、文件直纳入）
- **响应**: `200`，`Content-Type: application/zip`，`Content-Disposition: attachment; filename="<instanceName>-files.zip"`，响应体为分块 zip 字节流
- **错误**: `400 INVALID_REQUEST`（paths 为空或含非法路径）；`404 NOT_FOUND`（实例不存在/无权限）；`422 BUSINESS_ERROR`（节点离线/工作目录未设置/打包失败，流已开始则截断连接）

### POST /api/v1/instances/:id/files/search
- **描述**: 跨文件全文搜索 / 文件名快速打开。CP 仅经 gRPC 把查询转发到目标节点 Worker；索引是 Worker 本地派生资产（落数据根 `var/index/`，不进 CP 数据库，见 ADR-017）。索引**首建后台异步**（FR-113，见 ADR-024）：未就绪时本次返回 `indexing=true`（空命中），调用方稍后用同一查询重试；已就绪时查询前增量更新索引（指纹比对增/改/删），再倒排取候选文件、候选内精确行扫描返回命中
- **关联 FR**: FR-074, FR-113, FR-141
- **权限**: `instance.file`（可访问实例）
- **请求**: `{ "query": "string", "mode": "content", "maxResults": 200, "rootPath": "plugins", "extensions": [".yml", ".json"] }`。`query` 必填；`mode` 取 `content`（默认，全文）或 `filename`（文件名子串匹配，行号为 0）；`maxResults<=0` 时由 Worker 取默认上限；`rootPath` 可选，限定相对目录范围（禁止 `..`/前导 `/`）；`extensions` 可选，限定扩展名范围（服务端规范化为 `.ext`）
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

## 插件 / 模组

> 复用文件 gRPC（ListFiles/ReadFile/WriteFile/RenameFile/DeleteFile）在实例 `plugins/`、`mods/`、`resourcepacks/`、`datapacks/` 目录上操作；`plugins/mods` 接受 `.jar`，`resourcepacks/datapacks` 接受 `.zip`，禁用态统一追加 `.disabled`。列表会有界读取 jar/zip 内 `plugin.yml`、`bungee.yml`、`fabric.mod.json`、`mods.toml`、`pack.mcmeta` 提取版本、作者与依赖摘要，解析失败仅降级为空。实例级隔离（AuthzService），写操作经审计中间件记录（`plugin.deploy`/`plugin.delete`/`plugin.toggle`/`plugin.batch_deploy`）。

### GET /api/v1/instances/:id/plugins
- **描述**: 列出实例四个受控目录的 jar/zip 制品，识别启用/禁用状态（目录不存在视为空），并尽量返回制品内元信息
- **关联 FR**: FR-052, FR-143
- **权限**: 实例可访问（成员仅限有权实例）
- **响应**: `[{ "name": "EssentialsX.jar", "dir": "plugins", "enabled": true, "size": 1024, "modTime": 1710000000, "version": "2.20.1", "author": "EssentialsX", "dependencies": ["Vault"] }]`

### POST /api/v1/instances/:id/plugins
- **描述**: 上传单服制品（multipart）。jar 制品先入制品库（FR-045，`type=plugin`，sha256 去重）再部署到目标目录；zip 资源包/数据包直接写入受控目录
- **关联 FR**: FR-052, FR-045, FR-143
- **权限**: 实例可管理
- **表单**: `file`（必填；`plugins/mods` 为 `.jar`，`resourcepacks/datapacks` 为 `.zip`）、`dir`（可选，`plugins`|`mods`|`resourcepacks`|`datapacks`，默认 `plugins`）、`overwrite`（可选，`true`/`1` 时覆盖同名文件）
- **响应**: `201 { "message": "已上传", "asset": { ...Asset } }`
- **错误**: `409 FILE_EXISTS`（同名文件已存在且未允许覆盖）

### DELETE /api/v1/instances/:id/plugins/:name
- **描述**: 删除指定制品（同时匹配启用/禁用文件名）。二次确认在前端完成
- **关联 FR**: FR-052, FR-143
- **权限**: 实例可管理
- **Query**: `?dir=plugins|mods|resourcepacks|datapacks`（可选，默认 `plugins`）
- **路径参数**: `name` 为展示名（不含 `.disabled`）

### POST /api/v1/instances/:id/plugins/:name/toggle
- **描述**: 启用/禁用制品（在原文件名与 `.disabled` 后缀之间重命名，不删除文件）
- **关联 FR**: FR-052, FR-143
- **权限**: 实例可管理
- **Query**: `?dir=plugins|mods|resourcepacks|datapacks`（可选，默认 `plugins`）
- **响应**: `{ "message": "已切换", "enabled": false }`

### POST /api/v1/plugins/batch-deploy
- **描述**: 从制品库选择 `type=plugin` 的一个或多个 jar，批量部署到多个实例的 `plugins/` 或 `mods/` 目录；同步返回逐实例/逐插件结果
- **关联 FR**: FR-053, FR-045
- **权限**: `instance:write`，目标实例按 AuthzService 可访问集合收敛；显式 ids 中不存在或越权目标计入 `skipped`
- **请求**: `{ "assetIds":[1,2], "target": { "ids":[10,11], "filter": { "status":"STOPPED", "role":"backend", "nodeId":1 } }, "destination":"plugins", "overwrite":false }`
  - `target.ids` 与 `target.filter` 二选一；单次可见目标上限 500
  - `destination`: `plugins` / `mods`，非法或空值回落到 `plugins`
  - `overwrite=false` 时同名文件已存在会返回该条失败；`overwrite=true` 允许覆盖
- **响应**: `{ "requestedInstances":2, "requestedAssets":2, "succeeded":3, "failed":1, "skipped":0, "results":[{ "instanceId":10, "assetId":1, "ok":true }, { "instanceId":11, "assetId":2, "ok":false, "error":"文件已存在: plugins/Foo.jar" }] }`

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
- **描述**: Bot 基础详情（DB 基础字段 + 读取时经 Worker `ListBots` 懒回填 `status`）；位置/血量/背包/事件流等富遥测归 FR-041，当前 HTTP 详情不承诺返回
- **关联 FR**: FR-009（基础详情）, FR-041（富遥测延后）

### GET /api/v1/bots/:id/events
- **描述**: 单 Bot 实时事件 SSE（状态、血量、饥饿、位置、聊天/行为事件）
- **关联 FR**: FR-041
- **权限**: `bot:read`（按 Bot 所属实例隔离）
- **事件**:
  - `event: init`：`{ "botId": 1, "botUuid": "..." }`
  - `event: bot`：
    ```json
    {
      "botId": 1,
      "botUuid": "uuid",
      "type": "state",
      "data": { "status": "connected", "health": 20, "food": 20, "behavior": "guard", "position": { "x": 0, "y": 64, "z": 0 } },
      "timestamp": 1780000000000
    }
    ```
- **错误**: 403 `FORBIDDEN`；404 `NOT_FOUND`；503 `STREAM_UNAVAILABLE`

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

### POST /api/v1/bots/stress-sessions
- **描述**: 创建持久化 Bot 压测会话，支持 YAML 动作编排。
- **关联 FR**: FR-042 / FR-274
- **权限**: `bot:manage`（资源级按目标实例隔离）
- **兼容别名**: `POST /api/v1/bots/stress-test`
- **请求**:
  ```json
  {
    "instanceId": 1,
    "count": 50,
    "behavior": "idle",
    "namePrefix": "load",
    "config": { "server": "127.0.0.1", "port": 25565, "auth": "offline" },
    "orchestrationYaml": "loop: true\nstaggerMs: 500\nphases:\n  - durationSec: 60\n    behavior: idle\n"
  }
  ```
  - `instanceId`: 必填。
  - `count`: 范围保持 `1..5000`，FR-274 真实验收固定使用 50。
  - `namePrefix`: 必填，启动时生成 Bot 名称前缀，形如 `load-001`。
  - `config`: 保持现有 Bot 连接配置 JSON。
  - `behavior`: 在 `orchestrationYaml` 为空时必填；在 `orchestrationYaml` 非空时可省略，响应中的 `behavior` 取首个阶段行为。
  - `orchestrationYaml`: 可选；非空时必须通过 YAML 编排校验。
- **响应**: `201`
  ```json
  {
    "id": 1,
    "uuid": "uuid",
    "instanceId": 1,
    "count": 50,
    "behavior": "idle",
    "namePrefix": "load",
    "config": { "server": "127.0.0.1", "port": 25565, "auth": "offline" },
    "orchestrationYaml": "loop: true\nstaggerMs: 500\nphases:\n  - durationSec: 60\n    behavior: idle\n",
    "orchestrationSummary": {
      "enabled": true,
      "loop": true,
      "staggerMs": 500,
      "phaseCount": 1,
      "durationSec": 60,
      "behaviors": ["idle"]
    },
    "status": "pending",
    "startedAt": null,
    "stoppedAt": null,
    "createdAt": "datetime",
    "updatedAt": "datetime",
    "counts": { "total": 0, "byStatus": {} }
  }
  ```
- **错误**:
  - 400 `INVALID_REQUEST`：参数缺失、数量越界、旧模式缺 `behavior`、YAML 语法错误或编排语义非法。
  - 403 `FORBIDDEN`：无权管理目标实例。

### GET /api/v1/bots/stress-sessions
- **描述**: 分页列出压测会话，返回会话状态、关联 Bot 聚合计数和编排摘要。
- **关联 FR**: FR-042 / FR-274
- **权限**: `bot:read`（按可访问实例集合收敛）
- **Query**: `?page=1&pageSize=20`
- **响应**:
  ```json
  {
    "items": [
      {
        "id": 1,
        "instanceId": 1,
        "count": 50,
        "behavior": "idle",
        "namePrefix": "load",
        "status": "running",
        "orchestrationSummary": {
          "enabled": true,
          "loop": true,
          "staggerMs": 500,
          "phaseCount": 4,
          "durationSec": 330,
          "behaviors": ["idle", "patrol", "guard", "custom"]
        },
        "counts": { "total": 50, "byStatus": { "connected": 50 } }
      }
    ],
    "total": 1,
    "page": 1,
    "pageSize": 20
  }
  ```

### GET /api/v1/bots/stress-sessions/:id
- **描述**: 查询单个压测会话详情，返回持久化 YAML。
- **关联 FR**: FR-042 / FR-274
- **权限**: `bot:read`（按会话目标实例隔离）
- **响应**: `200` 同创建响应。
- **错误**:
  - 403 `FORBIDDEN`：无读取权限。
  - 404 `NOT_FOUND`：会话不存在或无权访问。

### POST /api/v1/bots/stress-sessions/:id/start
- **描述**: 启动压测会话，按会话配置批量创建并上线 Bot；含 YAML 编排时下发 `orchestrated` 行为和 `behavior_config`。
- **关联 FR**: FR-042 / FR-274
- **权限**: `bot:manage`（按会话目标实例隔离）
- **响应**: `200` 会话视图，含 `counts` 和 `orchestrationSummary`。
- **错误**:
  - 400 `INVALID_REQUEST`：会话状态不允许启动或持久化编排无法解析。
  - 404 `NOT_FOUND`：会话不存在或无权访问。

### POST /api/v1/bots/stress-sessions/:id/stop
- **描述**: 停止压测会话，将会话关联 Bot 批量置为 `stopped`
- **关联 FR**: FR-042 / FR-274
- **权限**: `bot:manage`（按会话目标实例隔离）
- **响应**: `200` 会话视图，含 `counts` 和 `orchestrationSummary`。
- **错误**:
  - 404 `NOT_FOUND`：会话不存在或无权访问。

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
- **描述**: 实例备份列表（含 `mode` 全量/增量、`parentId` 备份链、`storageId` 存储位置、`checksum`/`checksumAlgo` 完整性校验字段；历史备份可能为空）
- **关联 FR**: FR-013, FR-056, FR-057, FR-171

### POST /api/v1/instances/:id/backups
- **描述**: 创建备份。`incremental=true` 时挂到该实例最近一次已完成备份后形成备份链（仅打包变化文件）；`storageId` 指定远程存储后端，缺省存于节点本地；Worker 完成后返回并持久化归档 SHA-256
- **关联 FR**: FR-013, FR-056, FR-057, FR-171
- **请求**: `{ "name": "string", "incremental": false, "storageId": 0 }`
- **错误**: 422 `BUSINESS_ERROR`（增量但无可作基准的已完成全量备份）

### POST /api/v1/backups/:id/restore
- **描述**: 恢复备份。增量备份沿父链回溯解析整链（全量基 + 各增量），委托 Worker 按序回放；远程备份先拉回本地再回放；有 `checksum` 的归档在解压前校验，不一致则拒绝恢复
- **关联 FR**: FR-013, FR-056, FR-057, FR-171
- **错误**: 409 `INSTANCE_NOT_STOPPED`（实例进程可能存活——STARTING/RUNNING/STOPPING 时拒绝恢复，须先停止实例；STOPPED/CRASHED 放行）；422 `BUSINESS_ERROR`（备份未完成/链断裂等）

### DELETE /api/v1/backups/:id
- **描述**: 删除备份。被增量子备份依赖时拒绝（422），避免割裂备份链
- **关联 FR**: FR-013, FR-056

### GET /api/v1/backup-storages
- **描述**: 备份远程存储后端列表（凭证以 `${ENV_VAR}` 引用，不返回明文），并返回容量聚合与最近一次人工测试结果
- **权限**: 平台管理员
- **关联 FR**: FR-057, FR-152
- **响应加性字段**: `backupCount`、`usedBytes`、`lastTestAt`、`lastTestOk`、`lastTestMessage`

### POST /api/v1/backup-storages
- **描述**: 创建远程存储后端（`type` ∈ s3/sftp/webdav）。凭证字段须为 `${ENV_VAR}` 引用，明文/非法类型回 422
- **权限**: 平台管理员
- **关联 FR**: FR-057, FR-152
- **请求**: `{ "name": "string", "type": "s3", "endpoint": "", "bucket": "", "region": "", "prefix": "", "accessKeyEnv": "${VAR}", "secretKeyEnv": "${VAR}", "useSsl": true }`

### POST /api/v1/backup-storages/test
- **描述**: 测试未保存的存储后端配置；不创建记录，不写 `lastTest*`。S3 使用短超时 SigV4 `HEAD bucket`，WebDAV 使用 `OPTIONS`，SFTP 建立 SSH 握手
- **权限**: 平台管理员
- **关联 FR**: FR-152
- **请求**: 同 `POST /api/v1/backup-storages`
- **响应**: `{ "ok": true, "message": "连接正常", "latencyMs": 0 }`

### POST /api/v1/backup-storages/:id/test
- **描述**: 测试已保存的存储后端，并更新 `lastTestAt`、`lastTestOk`、`lastTestMessage`；探测方式同未保存配置测试
- **权限**: 平台管理员
- **关联 FR**: FR-152
- **响应**: `{ "ok": true, "message": "连接正常", "latencyMs": 0 }`

### DELETE /api/v1/backup-storages/:id
- **描述**: 删除远程存储后端。被备份引用时拒绝（422）
- **权限**: 平台管理员
- **关联 FR**: FR-057, FR-152

---

## 告警

> FR-011 阈值告警在 FR-085 扩展为多通道 + 多触发类型 + 分级聚合静默 + 确认历史。
> 所有端点沿用 `protected` 分组：未登录返回 401，认证用户可访问全局告警事件与 `/alerts`；本 FR 不额外按平台/组管理员收紧。

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
  - `targetType`/`targetId`: `metric`、`node_offline` 使用 `node`；`instance_crash`、`log_keyword`、`player_event`、`backup_failed` 使用 `instance`；`targetId=null` 表示该目标类型全局匹配
  - `keyword`: 仅 `log_keyword` 用且必填；`eventMatch`: 仅 `player_event` 用（`join`/`quit`/`chat`/`cross_server`，空=任意）
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
- **描述**: 删除资产；被引用（`refCount>0`）时拒绝。`client-updater-core` 内置归档或被频道选定时同样拒绝删除，避免楔子首次/下次启动拉不到 updater-core
- **关联 FR**: FR-045 / FR-259
- **错误**: 404 `NOT_FOUND`；409 `ASSET_IN_USE`（附当前引用数；`client-updater-core` 内置或被频道选定时也返回该错误）

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
  - `client-file` 制品沿用 `metadata` JSON 暴露客户端相对路径/codec（如 `path`、`targetPath`、`codec`），前端据此展示 OTA 文件来源
- **权限**: 平台管理员；未登录返回 401，普通成员返回 403，均不返回聚合数据
- **响应 200**:
```json
{
  "jdks": [
    {
      "id": 10, "nodeId": 1, "nodeName": "node-a", "nodeOnline": true,
      "vendor": "Temurin", "majorVersion": 21, "version": "21.0.4", "arch": "x64",
      "path": "/opt/jdks/temurin-21", "managed": true,
      "instances": [
        { "id": 100, "uuid": "<uuid>", "name": "paper-1", "status": "RUNNING", "binding": "direct" },
        { "id": 101, "uuid": "<uuid>", "name": "lobby-proxy", "status": "STOPPED", "binding": "major" }
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
- **错误**: 401（未登录）；403（已登录但非平台管理员）；500 `INTERNAL_ERROR`

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
    { "path": "bin", "label": "bin", "size": 0, "fileCount": 0, "exists": true, "clearable": false },
    { "path": "etc", "label": "etc", "size": 0, "fileCount": 0, "exists": true, "clearable": false },
    { "path": "opt/jdks", "label": "jdks", "size": 320000000, "fileCount": 240, "exists": true, "clearable": false },
    { "path": "var/servers", "label": "servers", "size": 1048576, "fileCount": 30, "exists": true, "clearable": false },
    { "path": "var/log", "label": "log", "size": 8192, "fileCount": 3, "exists": true, "clearable": false },
    { "path": "var/artifacts", "label": "artifacts", "size": 48234123, "fileCount": 12, "exists": true, "clearable": false },
    { "path": "cache", "label": "cache", "size": 4096, "fileCount": 2, "exists": true, "clearable": true }
  ],
  "totalSize": 369294987,
  "totalFiles": 287,
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

## 数据库资源管理器（FR-084）

> 只读浏览 Control Plane 自身数据库（表清单 + 分页行数据）。严守「数据库仅 Control Plane 可读写」不变量：只执行元数据读取与 `SELECT`，无写入、导出、执行 SQL、删除或迁移端点；全部端点限平台管理员。

### GET /api/v1/db/tables
- **描述**: 列出 Control Plane 当前数据库全部表及行数；表名按稳定顺序返回
- **关联 FR**: FR-084
- **权限**: 平台管理员
- **响应 200**:
```json
{
  "tables": [
    { "name": "users", "rowCount": 3 },
    { "name": "instances", "rowCount": 12 }
  ]
}
```
- **错误**: 401（未登录）；403（已登录但非平台管理员）；500 `INTERNAL_ERROR`

### GET /api/v1/db/tables/:name/rows
- **描述**: 分页只读查询某表行数据，并返回列定义；敏感列在服务端脱敏后返回
- **关联 FR**: FR-084
- **权限**: 平台管理员
- **Query**:
  - `page`: 页码，从 1 起，默认 1
  - `pageSize`: 每页行数，默认 50，最大 200（越界钳制）
  - `sort`: 排序列名，必须命中该表列白名单；非法列忽略
  - `order`: `asc` / `desc`，默认 `asc`
  - `filterColumn` + `filterValue`: 简单过滤；列名必须命中列白名单，过滤值参数化绑定
- **脱敏规则**: 列名（不区分大小写）命中 `password`、`passwd`、`secret`、`token`、`node_secret`、`private_key`、`priv_key`、`sign_priv`、`salt`、`api_key`、`access_key`、`credential`、`pull_key`、`key_hash` 任一片段时，非空值替换为 `******`
- **响应 200**:
```json
{
  "table": "users",
  "columns": [
    { "name": "id", "type": "integer", "sensitive": false },
    { "name": "username", "type": "text", "sensitive": false },
    { "name": "password_hash", "type": "text", "sensitive": true }
  ],
  "rows": [
    { "id": 1, "username": "admin", "password_hash": "******" }
  ],
  "page": 1,
  "pageSize": 50,
  "total": 3
}
```
- **错误**: 401；403；404 `TABLE_NOT_FOUND`（表名不在白名单）；500 `INTERNAL_ERROR`

---

## 审计日志

### GET /api/v1/audit
- **描述**: 审计日志列表（平台管理员）
- **关联 FR**: FR-015, FR-172
- **Query**: `?userId=xxx&action=instance.start&targetType=instance&from=2024-01-01T00:00:00Z&to=2024-12-31T23:59:59Z&page=1&pageSize=50`
- **参数说明**:
  - `from`/`to`: RFC3339 格式时间，按 created_at 筛选范围
  - `page`/`pageSize`: 传入任一分页参数时返回分页 envelope；`pageSize` 上限 200
  - `limit`: 仅旧数组模式兼容使用；新前端应使用 `page/pageSize`
- **响应**:
  - 分页模式：`{ "items": [AuditLogInfo], "total": 123, "page": 1, "pageSize": 50 }`
  - 旧数组模式：`AuditLogInfo[]`

### GET /api/v1/audit/export
- **描述**: 按过滤条件导出审计日志 NDJSON（平台管理员）
- **关联 FR**: FR-172
- **Query**: `?format=ndjson&userId=xxx&action=instance.start&targetType=instance&from=2024-01-01T00:00:00Z&to=2024-12-31T23:59:59Z`
- **参数说明**:
  - `format`: 当前仅支持 `ndjson`；传其它值返回 `400 UNSUPPORTED_FORMAT`
  - `page`、`pageSize`、`limit` 会被忽略，导出始终按过滤条件导出全量匹配记录
- **响应**: `application/x-ndjson`，服务端按批次流式写出，每行一条白名单 JSON
- **字段白名单**: `id`、`createdAt`、`userId`、`username`、`action`、`targetType`、`targetId`、`ip`
- **审计**: 导出行为写入 `audit.export`，detail 记录 `format`、过滤条件摘要与 `success/failure` 状态

---

## 任务中心与站内信（FR-183，见 ADR-040）

> 长耗时跨进程动作（首批：JDK 一键下载安装）改为异步任务：发起即返回 `taskId`，进度/日志/历史经心跳汇聚到 CP，完成发站内信。
> 任务/站内信按归属隔离：非平台管理员只见自己发起的任务、只读/操作自己的站内信；平台管理员可见全部任务。

### POST /api/v1/nodes/:id/jdks/install（行为变更）
- **描述**: 一键下载安装 JDK（**异步化**）。建任务 + 令 Worker 启动即返回，立即回执 `taskId`；进度/完成在任务中心与站内信查看。取代原「同步阻塞最长 20min 返回 JDK 记录」。
- **关联 FR**: FR-072, FR-183, FR-178
- **权限**: 平台管理员
- **请求体**: `{ "vendor": "Temurin", "majorVersion": 21, "arch": "x64", "version": "21.0.4" }`
  - `version` 可选（FR-178）：非空时 Worker 经 foojay 按具体版本解析下载源；为空取该大版本最新 GA。
- **响应**: `202 Accepted` `{ "taskId": "<uuid>", "task": { ...Task } }`
- **错误码**: `503 NODE_OFFLINE`（节点未连接，不建悬挂任务）；`404 NOT_FOUND`（节点不存在）；`502 INSTALL_FAILED`（下发 Worker 失败，任务已置 failed）

### POST /api/v1/nodes/:id/jdks/probe（FR-228）
- **描述**: 探测节点上某路径（JDK home 目录或 java 可执行文件）的 JDK 信息，供「登记已有」自动填厂商/版本/架构（免手填）。Worker 归一路径后跑 `java -XshowSettings:properties -version` 解析。
- **关联 FR**: FR-228（增强 FR-178/033）
- **权限**: 平台管理员
- **请求体**: `{ "path": "/opt/jdks/temurin-21" }`（或指向 `.../bin/java`）
- **响应**: `{ "valid": true, "vendor": "Temurin", "majorVersion": 21, "version": "21.0.4+9", "arch": "x64", "javaHome": "/opt/jdks/temurin-21" }`；非 JDK → `{ "valid": false, "error": "未找到 bin/java 或无法读取版本…" }`（HTTP 200，valid=false 不作 5xx）
- **错误码**: `503 NODE_OFFLINE`；`404 NOT_FOUND`（节点不存在）；`400 INVALID_REQUEST`（缺 path）

### DELETE /api/v1/nodes/:id/jdks/:jid（FR-228 细化）
- **描述**: 删除已登记 JDK。**按来源区分文件处置**：`managed=true`（平台下载托管）删记录 + 移除 Worker 上的文件（前端要求二次输入「厂商 主版本」确认）；`managed=false`（外部登记）**仅删记录、不动磁盘文件**（外部 JDK 由用户自管）。被实例引用时拒绝。
- **关联 FR**: FR-036 / FR-072 / FR-228
- **权限**: 平台管理员
- **响应**: `{ "message": "已删除" }`
- **错误码**: `409 JDK_IN_USE`（被实例占用，附 `instances`）；`404 NOT_FOUND`

### 节点运行时管理（FR-178）

> 节点级运行时面板后端：制品缓存（性能优化，真·节点级）、JDK 版本目录（foojay）、目录浏览。
> 全部经 gRPC 委托 Worker（CP 不直接读节点 FS），仅平台管理员；缓存破坏性操作写审计。

#### GET /api/v1/nodes/:id/artifact-cache
- **描述**: 列出节点本地制品缓存项 + 总占用 + 当前容量上限。`name`/`version` 缺失时 CP 用全局制品库（asset 表）按 sha256 补全。
- **关联 FR**: FR-178
- **权限**: 平台管理员
- **响应**: `{ "items": [{ "sha256", "name", "type", "version", "size", "cachedAt", "lastUsedAt" }], "totalBytes": 0, "capBytes": 0 }`（`capBytes=0` 表示不限；时间为 Unix 秒）
- **错误码**: `503 NODE_OFFLINE`；`404 NOT_FOUND`；`502 WORKER_ERROR`

#### DELETE /api/v1/nodes/:id/artifact-cache/:sha256
- **描述**: 逐项清除指定 sha256 的缓存（幂等）。写审计 `node.artifact_cache.evict`。
- **关联 FR**: FR-178
- **权限**: 平台管理员
- **响应**: `{ "message": "已清除" }`

#### POST /api/v1/nodes/:id/artifact-cache/clear
- **描述**: 清空节点全部制品缓存。写审计 `node.artifact_cache.clear`。
- **关联 FR**: FR-178
- **权限**: 平台管理员
- **响应**: `{ "removed": 3 }`

#### PUT /api/v1/nodes/:id/artifact-cache/cap
- **描述**: 设置缓存容量上限（字节，0=不限）。设定后即按新上限触发一次 LRU（`lastUsedAt` 升序）淘汰。写审计 `node.artifact_cache.set_cap`。
- **关联 FR**: FR-178
- **权限**: 平台管理员
- **请求体**: `{ "capBytes": 1073741824 }`
- **响应**: `{ "capBytes": 1073741824, "totalBytes": 0 }`
- **错误码**: `400 INVALID_REQUEST`（上限为负）；`503 NODE_OFFLINE`

#### GET /api/v1/nodes/:id/jdk/catalog?vendor=&major=&arch=
- **描述**: 经 CP 代理 foojay disco 查询某发行版可选的具体 JDK 版本（喂前端版本选择器）。统一出站代理、避前端跨域。
- **关联 FR**: FR-178
- **权限**: 平台管理员
- **Query**: `vendor`（必填，如 Temurin/Liberica/Microsoft/Semeru/GraalVM…）；`major`（可选大版本）；`arch`（可选，x64/aarch64）
- **响应**: `[{ "distribution", "majorVersion", "javaVersion", "archiveType", "latest" }]`
- **错误码**: `400 INVALID_REQUEST`（缺 vendor）；`502 WORKER_ERROR`（foojay 不可达，前端降级为手填版本）

#### GET /api/v1/nodes/:id/browse?path=
- **描述**: 只读列出节点上某绝对路径下的子目录（JDK 路径登记目录选择器）。`path` 为空时返回起点（Windows 盘符 / Unix 根）；只列目录、防穿越。
- **关联 FR**: FR-178
- **权限**: 平台管理员
- **响应**: `{ "path": "/opt", "parent": "/", "dirs": [{ "name": "jdks", "path": "/opt/jdks" }] }`
- **错误码**: `503 NODE_OFFLINE`；`502 WORKER_ERROR`（路径不可访问/非目录/相对路径）

### 节点运行时库（FR-298）

> 运行时泛化管理：JDK 沿用 `node_jdks` 与既有 `/nodes/:id/jdks` 全链路**不动**；本组端点提供
> **统一 Runtime 视图**（node_jdks(type=jdk) + node_runtimes 读侧拼装）、**扫描发现**（Worker
> `ScanRuntimes` 扫常见安装路径）与**泛化登记/删除**。仅平台管理员；扫描/登记/删除写审计
> （`node.runtime.scan` / `node.runtime.register` / `node.runtime.delete`，中英翻译在 `audit.actions.*`）。

#### GET /api/v1/nodes/:id/runtimes
- **描述**: 统一 Runtime 视图。JDK 部分复用 JDK List 的 `syncFromWorker` 容忍语义（同步失败仍回 DB 数据）；排序 type 升序（jdk 在前）→ major 降序。
- **关联 FR**: FR-298
- **权限**: 平台管理员
- **响应**: `[{ "id", "nodeId", "type": "jdk|nodejs|python", "name": "Temurin|Node.js 22", "majorVersion", "version", "arch", "path", "managed", "createdAt" }]`（`id` 按 `type` 归属各承载表，增删须带 type）
- **错误码**: `404 NOT_FOUND`（节点不存在）

#### POST /api/v1/nodes/:id/runtimes/scan
- **描述**: 代理 Worker `ScanRuntimes` 扫描常见安装路径回候选列表。Worker 侧托管根下候选标 `alreadyRegistered`，CP 再按 DB 已登记 (type,path) 补标（重复扫描已入库项即标出）。路径不存在/探测失败静默跳过。
- **关联 FR**: FR-298
- **权限**: 平台管理员
- **请求体**: `{ "types": ["jdk", "nodejs"] }`（可空/省略 = 全部可扫描类型）
- **响应**: `{ "candidates": [{ "type", "vendor", "version", "majorVersion", "arch", "path", "alreadyRegistered" }] }`
- **审计**: `node.runtime.scan`（detail 含 types 与候选数）
- **错误码**: `422 BUSINESS_ERROR`（未知扫描类型）；`503 NODE_OFFLINE`；`404 NOT_FOUND`；`502 WORKER_ERROR`

#### POST /api/v1/nodes/:id/runtimes
- **描述**: 泛化登记运行时。`type=jdk` **转发现有 JDK 登记链路**（落 `node_jdks`，需 `vendor`+`majorVersion`）；其它已知类型（nodejs / python 预留）落 `node_runtimes`（`name` 缺省自动生成如 "Node.js 22"）。未知类型拒绝。
- **关联 FR**: FR-298
- **权限**: 平台管理员
- **请求体**: `{ "type": "nodejs", "name": "Node.js 22", "vendor": "", "majorVersion": 22, "version": "22.17.0", "arch": "x64", "path": "/usr/local/bin/node", "managed": false }`（`type`/`version`/`path` 必填）
- **响应**: `201 Created` 统一视图行（同 GET 元素结构）
- **审计**: `node.runtime.register`
- **错误码**: `422 BUSINESS_ERROR`（未知类型 / 同 node+type+path 重复登记 / JDK 缺 vendor 或 majorVersion）；`404 NOT_FOUND`；`400 INVALID_REQUEST`

#### DELETE /api/v1/nodes/:id/runtimes/:rid?type=
- **描述**: 删除运行时。`type` **必带**（定位承载表）：`type=jdk` 走现有 JDK 删除链路（占用守卫 + 托管连文件语义不变）；其它类型仅删 `node_runtimes` 记录（波1 均为外部登记，不动磁盘文件；托管 Node 文件清理随 FR-299）。
- **关联 FR**: FR-298
- **权限**: 平台管理员
- **响应**: `{ "message": "已删除" }`
- **审计**: `node.runtime.delete`
- **错误码**: `400 INVALID_REQUEST`（缺 type）；`409 JDK_IN_USE`（jdk 被实例占用，附 `instances`）；`404 NOT_FOUND`；`422 BUSINESS_ERROR`（未知类型）

### 节点出站代理（FR-185，见 ADR-043）

> 节点级出站代理：继承平台全局默认（设置面板配，见「平台设置」network 键）或为本节点自定义。
> 真相源 = CP DB；设置经心跳下发到 Worker，节点运行时重建出站 client（免改 worker.yml/重启）。
> 含凭据的代理 URL 在响应中一律脱敏（仅 `scheme://host:port`）。仅平台管理员；设置写审计。

#### GET /api/v1/nodes/:id/proxy
- **描述**: 查看节点出站代理配置（脱敏），含当前生效值与全局默认值。
- **关联 FR**: FR-185
- **权限**: 平台管理员
- **响应**: `{ "mode": "inherit"|"custom", "url": "<脱敏，仅 custom>", "noProxy": "<仅 custom>", "effectiveUrl": "<脱敏；custom→节点值，inherit→全局默认>", "effectiveNoProxy": "", "globalDefaultUrl": "<脱敏>", "online": true }`
  - `online=false` 时前端标注「待下发」（下次心跳上线后生效）。
- **错误码**: `404 NOT_FOUND`（节点不存在）

#### PATCH /api/v1/nodes/:id/proxy
- **描述**: 设置节点继承全局/自定义代理。`mode=custom` 时 `url` 必填且须为合法代理地址（http/https/socks5，复用 httpclient 校验）；`mode=inherit` 时清空自定义字段、改用全局默认。生效经心跳下发到 Worker。写审计 `node.proxy.set`（仅记录 mode，不记录敏感 URL）。
- **关联 FR**: FR-185
- **权限**: 平台管理员
- **请求体**: `{ "mode": "inherit"|"custom", "url": "http://127.0.0.1:7890", "noProxy": "localhost,10.0.0.0/8" }`（inherit 时 `url`/`noProxy` 忽略）
- **响应**: 同 `GET`（脱敏视图）
- **错误码**: `404 NOT_FOUND`（节点不存在）；`422 BUSINESS_ERROR`（mode 非法 / custom 缺地址 / 代理地址非法）

### GET /api/v1/tasks
- **描述**: 任务列表（倒序）。非平台管理员只见自己发起的，平台管理员见全部。
- **关联 FR**: FR-183 / FR-227（筛选）
- **权限**: 所有认证用户（归属隔离）
- **Query**: `?limit=100`；筛选（FR-227）`?kind=&state=&nodeId=&keyword=&since=&until=`（`since`/`until` 为 RFC3339；`keyword` 模糊匹配标题/详情）
- **响应**: `[{ id, taskId, nodeId, kind, state, progress, title, detail, error, result, cancelRequested, createdBy, createdAt, updatedAt }]`
  - `state`: `pending` / `running` / `succeeded` / `failed` / `canceled`；`progress`: 0~100
  - `cancelRequested`: 已请求强制停止但 Worker 尚未确认中断（在线 running 取消时为 true，前端显「取消中」，FR-227）

### GET /api/v1/tasks/:taskId
- **描述**: 单个任务详情（含滚动日志）。越权或不存在返回 404（不泄露存在性）。
- **关联 FR**: FR-183
- **权限**: 所有认证用户（仅自己发起的；平台管理员不限）
- **响应**: `{ "task": { ...Task }, "logs": [{ id, taskId, seq, line, ts }] }`

### POST /api/v1/tasks/:taskId/cancel
- **描述**: 强制停止任务（FR-227）。**真中断**：pending（Worker 未起）或节点离线 → 直接置 `canceled`；running 在线 → 置 `cancelRequested=true`，经心跳 `HeartbeatResponse.cancel_task_ids` 下发，Worker 取消执行 context（中断下载 + 清理临时文件）后回报 `canceled` 终态。
- **关联 FR**: FR-227
- **权限**: 所有认证用户（仅自己发起的；平台管理员不限）；越权/不存在 404
- **错误**: 已终态 → 409 `ALREADY_TERMINAL`
- **响应**: `{ "message": "已请求停止" }`

### GET /api/v1/notifications
- **描述**: 当前用户的站内信列表（倒序）。
- **关联 FR**: FR-183
- **权限**: 所有认证用户（仅自己的）
- **Query**: `?unread=true&limit=50`（`unread=true` 仅未读）
- **响应**: `[{ id, userId, level, title, body, taskId, readAt, createdAt }]`
  - `level`: `info` / `success` / `warning` / `error`；`readAt` 缺省=未读

### GET /api/v1/notifications/unread-count
- **描述**: 当前用户未读站内信数量（用于角标）。
- **关联 FR**: FR-183
- **权限**: 所有认证用户
- **响应**: `{ "unread": 3 }`

### POST /api/v1/notifications/:id/read
- **描述**: 标记一条站内信为已读（已读幂等返回成功）。
- **关联 FR**: FR-183
- **权限**: 所有认证用户（仅自己的）
- **错误码**: `404 NOT_FOUND`（不存在或非本人）

### POST /api/v1/notifications/read-all
- **描述**: 标记当前用户全部未读站内信为已读。
- **关联 FR**: FR-183
- **权限**: 所有认证用户
- **响应**: `{ "updated": 5 }`

## 统一通知中心（FR-216，见 ADR-048）

> 站内信（定向消息）+ 告警事件（系统警报）**合并为一条只读通知流**：页眉单铃铛 + 通知中心页消费。
> **视图聚合不新建表**——查询时把 `notifications`（按当前用户）+ `alert_events`（全局，与既有 `/alerts` 可见性一致）合并；`source` 判别（`message`/`alert`）、级别统一到四档（告警 `warn→warning`、`critical→error`）。
> 标记已读下推到各源既有语义（站内信按本人、告警全局）。既有 `/notifications/*`、`/alerts/*` 端点与写入源保留不动。

### GET /api/v1/notifications/feed
- **描述**: 统一通知流分页列表（站内信 + 告警合并，按发生时间倒序）。
- **关联 FR**: FR-216
- **权限**: 所有认证用户（消息按本人、告警面向全体）
- **Query**: `?source=message|alert`（空=全部）、`unread=true`（仅未读）、`keyword=`（标题/正文模糊）、`page=1`、`pageSize=50`
- **响应**: `{ "items": [{ source, id, level, title, body, read, createdAt, taskId?, triggerType?, acknowledged?, resolved? }], "total": 12 }`
  - `source`: `message`（站内信）/ `alert`（告警事件）；`level`: `info`/`success`/`warning`/`error`（统一枚举）
  - `taskId` 仅 message；`triggerType`/`acknowledged`/`resolved` 仅 alert
  - `total` = 两源命中数之和（两源不重叠，不去重）

### GET /api/v1/notifications/feed/unread-count
- **描述**: 统一未读数（当前用户未读站内信 + 全局未读告警），用于页眉角标。
- **关联 FR**: FR-216
- **权限**: 所有认证用户
- **响应**: `{ "unread": 5 }`

### POST /api/v1/notifications/feed/read-all
- **描述**: 全部标记已读（当前用户站内信 + 全局告警）。
- **关联 FR**: FR-216
- **权限**: 所有认证用户
- **响应**: `{ "updated": 7 }`

### POST /api/v1/notifications/feed/:source/:id/read
- **描述**: 标记单条通知为已读。`:source` = `message`（下推站内信，按本人归属）/ `alert`（下推告警，全局）。
- **关联 FR**: FR-216
- **权限**: 所有认证用户（message 仅本人）
- **响应**: `{ "message": "已标记已读" }`
- **错误码**: `400 INVALID_REQUEST`（source 非法）；`404 NOT_FOUND`（message 不存在或非本人）

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

## 连通性自检（FR-229）

> 「先测后用」连通性探测，仅平台管理员。出站 HTTP 测试经 CP 当前出站客户端（含已配置代理，FR-185）发起，反映「CP 能否到达该源」；节点存活经 gRPC 调用 Worker 轻量 `GetVersion` 主动探活（不读心跳缓存）。出站 HTTP 测试可让 CP 请求任意 URL（SSRF 面），故限平台管理员。

### POST /api/v1/diagnostics/http-test
- **描述**: 经 CP 出站客户端 GET 目标 URL 测可达性（代理设置 / JDK 下载源连通性复用）
- **关联 FR**: FR-229（增强 FR-185/178）
- **权限**: 平台管理员
- **请求体**: `{ "url": "https://api.github.com" }`（仅带 host 的 http/https 绝对 URL，否则 400 `INVALID_URL`）
- **响应**: `{ "ok": true, "status": 200, "latencyMs": 371 }`；连接失败返 `{ "ok": false, "latencyMs": <ms>, "error": "<原因>" }`（连接失败不作 5xx，置 `ok=false`）
- **超时**: 10s

### POST /api/v1/nodes/:id/ping
- **描述**: 主动探测节点 Worker 是否存活（JDK 一键下载前先测，避免对离线/卡顿节点发起会卡死的下载）
- **关联 FR**: FR-229
- **权限**: 平台管理员
- **响应**: `{ "alive": true, "latencyMs": 0, "version": "0.12.0", "os": "windows", "arch": "amd64" }`；未连接/调用失败返 `{ "alive": false, "latencyMs": <ms>, "error": "<原因>" }`（非 5xx）；节点不存在 404 `NOT_FOUND`
- **超时**: 5s

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
      { "key": "backup.retention_days", "value": "14", "editable": true, "sensitive": false, "overridden": false, "effectiveImmediately": false },
      { "key": "proxy.url", "value": "http://127.0.0.1:7890", "editable": true, "sensitive": true, "overridden": true, "effectiveImmediately": true },
      { "key": "proxy.no_proxy", "value": "localhost,10.0.0.0/8", "editable": true, "sensitive": false, "overridden": true, "effectiveImmediately": true }
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
- **可写白名单键**: `log.level`（debug|info|warn|error）、`jdk.mirror.temurin` / `jdk.mirror.corretto` / `jdk.mirror.zulu`、`graceful_stop.timeout`（Go duration 文本）、`backup.retention_days`（非负整数）、`proxy.url`（network 类，敏感，FR-185）、`proxy.no_proxy`（network 类，FR-185）
- **各项生效方式**（FR-063 / FR-185）：
  - `log.level`：`effectiveImmediately=true`，落库即在 CP 内切换（slog LevelVar）
  - `jdk.mirror.*`：安装 JDK 时 CP 取生效值经 `InstallJDK.mirror_base` 下发 Worker，影响下载源
  - `graceful_stop.timeout`：启动实例时 CP 取生效值经 `CreateInstance.graceful_stop_timeout_seconds` 下发 Worker→wrapper；对设置变更后**新启动**的实例生效，已运行实例保留启动时的值
  - `backup.retention_days`：CP 后台巡检（约每小时一轮）裁剪 `createdAt` 早于 N 天的备份；`≤0` 不裁剪；被未超期增量子链引用的全量基跳过以保链可恢复
  - `proxy.url` / `proxy.no_proxy`（FR-185/ADR-043）：`effectiveImmediately=true`，落库即重建 CP 出站持有者（CP 自身下载立即走新代理）；`proxy.url` 敏感，回显脱敏（含凭据时仅 `scheme://host:port`），非法地址（非 http/https/socks5 / 不可解析）整体拒绝（422）。此全局值同时作为各节点默认代理（节点页可覆盖），优先级 settings DB > control-plane.yml > env
- **请求**: `{ "values": { "log.level": "debug", "backup.retention_days": "30" } }`

---

## 面板自更新（FR-081 / FR-175 / FR-182 / FR-186 / FR-190）

> Control Plane 与各节点 Worker 的二进制在线升级与回滚（ADR-020 / ADR-036 §7 / ADR-042）。均挂运营者浏览器 JWT 入口、**仅平台管理员**。
> **更新源**（FR-175，见 ADR-036 §7）：默认**原生读 GitHub Releases API**（`control-plane.yml` 的 `update.github_repo`，默认 `wcpe/JianManager`），`update.channel` 选 `stable`（取 `/releases/latest` 最新正式）或 `prerelease`（取滚动 `latest` 预发布，FR-182 由 `nightly` 改名）；sha256 取自 release 的 `checksums.txt` 资产（ADR-036 §2 契约），资产名按 ADR-036 §1 命名 `<component>-<os>-<arch>[.exe]` 反解。`update.github_token` 可选，提升 GitHub API 限流额度（匿名 60 次/时）。`github_repo` 为空且 `feed_url` 非空时**回退**原 feed JSON 路径（FR-081）；二者均空即未配置。下载经 FR-174 出站代理。
> 升级类操作写审计（detail 仅含版本/节点元数据，绝不含下载 url 或凭据）。升级流程：下载目标版本制品 → **sha256 校验** → 替换二进制 → 平滑重启；Worker 升级经 CP gRPC 编排（`GetVersion`/`UpgradeWorker`），daemon 模式下不杀运行中的游戏服。
> **升级前自动备份 + 一键回滚**（FR-182，见 ADR-042）：升级（CP 自升 / 节点升）在替换前把当前二进制 + 版本/sha256 备份到数据根 `cache/selfupdate-backup/<component>`（每组件单份，覆盖上一份）。`check` 透出各组件 `backupVersion`，可一键回滚到上一版（校验备份 sha256 → 换回 → 平滑重启）；节点回滚经 gRPC `UpgradeWorker(action=rollback)`，Worker 走本地备份不下载。无备份返回 `UPDATE_NO_BACKUP`。
> **Worker 二进制 CP 代理缓存下发**（FR-190，见 ADR-059）：节点升级前，CP 按当前 `version.Version + os + arch` 从 self-update feed/GitHub release artifact 下载并 sha256 校验 Worker 二进制，缓存到数据根 `cache/worker-assets/<version>/<os>-<arch>/`，再向 Worker 下发 CP-local 下载 URL + sha256；Worker 升级无需访问公网 release 源。CP-local 下载 URL 使用短期 query token，审计不记录 token 明文；`purpose=upgrade` token 必须绑定 `nodeUuid`，安装 token 与升级 token 分开签发。

### GET /api/v1/self-update/check
- **描述**: 返回**服务端缓存的上次成功检查结果**（FR-186），**不触发 live 网络调用**（毫秒级返回，进系统更新页即时回显）。缓存空时返回 `cached:false` 的最小结果（仅当前 `configured`/`source` + CP 本机当前版本），由前端据此触发一次 `refresh`。live 检查走 `POST /self-update/check/refresh`
- **权限**: 平台管理员
- **关联 FR**: FR-186、FR-081、FR-175、FR-182
- **响应** (200): `{ "configured", "latestVersion", "notes", "source", "controlPlane": ComponentStatus, "nodes": [ComponentStatus], "cached", "checkedAt?" }`，其中 `cached`（FR-186）报告本结果是否命中缓存、`checkedAt`（FR-186，ISO 时间，缓存空时省略）为上次成功检查时刻；`source` 标更新源（`github:owner/repo@channel` | `feed` | 空），`ComponentStatus = { "nodeId?", "nodeUuid?", "name?", "online", "currentVersion", "os", "arch", "updateAvailable", "artifactAvailable", "backupVersion?" }`；`backupVersion`（FR-182）为升级前备份的版本，非空时该组件可一键回滚
- **错误**: 502 `UPDATE_CHECK_FAILED`（读取缓存失败，罕见）

### POST /api/v1/self-update/check/refresh
- **描述**: 显式触发一次 **live 检查更新**并更新缓存（FR-186）——按配置的更新源（GitHub Releases 或 feed）解析最新版本，经 gRPC `GetVersion` 实时拉取各节点当前版本与 `backupVersion`，对比标注是否有更新及是否有匹配平台（component+os+arch）的制品；**成功后 upsert 覆盖服务端缓存**并返回最新结果（`cached:true` + `checkedAt`）。**失败时不清缓存**（断网/限流后 `GET /check` 仍可回显上次结果）。「检查更新」按钮与进页后台静默刷新均调此。未配源不报错，返回 `configured:false` 的可渲染结果（同样写缓存）
- **权限**: 平台管理员
- **关联 FR**: FR-186、FR-081、FR-175、FR-182
- **请求**: 无
- **响应** (200): 同 `GET /check` 的结构，`cached:true` + `checkedAt` 为本次检查时刻
- **错误**: 429 `UPDATE_RATE_LIMITED`（GitHub API 限流，可配 github_token 提额）| 502 `UPDATE_CHECK_FAILED`（拉取/解析更新源失败）

### POST /api/v1/self-update/control-plane/upgrade
- **描述**: 升级 CP 自身（下载 → sha256 校验 → 替换 → 平滑重启）。替换成功后异步延迟重启，先返回 202
- **权限**: 平台管理员
- **关联 FR**: FR-081、FR-175
- **请求**: `{ "version": "可选，留空取更新源最新" }`
- **响应** (202): `{ "status": "restarting", "fromVersion", "toVersion" }`
- **错误**: 409 `UPDATE_NOT_CONFIGURED` / `UPDATE_ALREADY_LATEST` | 422 `UPDATE_NO_ARTIFACT`（更新源无匹配本平台制品）| 429 `UPDATE_RATE_LIMITED` | 502 `UPDATE_FAILED`
- **审计**: `self_update.control_plane`

### POST /api/v1/self-update/control-plane/rollback
- **描述**: 回滚 CP 自身到升级前备份（FR-182）。校验备份 sha256 → 换回备份二进制 → 平滑重启。替换成功后异步延迟重启，先返回 202。不依赖更新源/网络
- **权限**: 平台管理员
- **关联 FR**: FR-182
- **请求**: 无
- **响应** (202): `{ "status": "restarting", "fromVersion", "toVersion"(回滚到的备份版本) }`
- **错误**: 409 `UPDATE_NO_BACKUP`（无可回滚的备份）| 502 `UPDATE_FAILED`（回滚失败，如备份 sha256 不符）
- **审计**: `self_update.control_plane_rollback`

### POST /api/v1/self-update/nodes/:id/upgrade
- **描述**: 经 gRPC 令目标节点下载校验替换并重启 Worker（daemon 模式下游戏服不掉）。FR-190 起，CP 会先预缓存与当前 CP 版本一致的目标平台 Worker 资产，签发短期下载 token，并把 CP-local URL + sha256 下发给 Worker；若更新源版本与 CP 当前版本不一致则拒绝，避免节点拿到与控制面协议错位的 Worker
- **权限**: 平台管理员
- **关联 FR**: FR-081、FR-175、FR-190
- **请求**: `{ "version": "可选，留空取更新源最新" }`
- **响应** (202): `{ "status": "upgrading", "nodeId", "fromVersion", "toVersion" }`
- **错误**: 409 `UPDATE_NOT_CONFIGURED` | 422 `UPDATE_NO_ARTIFACT` | 429 `UPDATE_RATE_LIMITED` | 503 `NODE_OFFLINE`（节点未连接）| 502 `UPDATE_FAILED`
- **审计**: `self_update.node`

### POST /api/v1/self-update/nodes/:id/rollback
- **描述**: 经 gRPC 令目标节点回滚到其升级前备份（FR-182）。Worker 走本地备份（`UpgradeWorker(action=rollback)`，不下载），校验备份 sha256 → 换回 → 重启 Worker（daemon 模式下游戏服不掉）
- **权限**: 平台管理员
- **关联 FR**: FR-182
- **请求**: 无
- **响应** (202): `{ "status": "rolling-back", "nodeId", "fromVersion", "toVersion" }`
- **错误**: 409 `UPDATE_NO_BACKUP`（节点无可回滚的备份）| 503 `NODE_OFFLINE`（节点未连接）| 502 `UPDATE_FAILED`
- **审计**: `self_update.node_rollback`

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

### GET /api/v1/self-update/worker-assets
- **描述**: 查看 CP 本地已缓存的 Worker 二进制资产元信息（FR-190）。仅列缓存目录中已有 metadata 的条目；若文件损坏或 sha256 不符，`cached=false` 并返回 `lastError`
- **权限**: 平台管理员
- **关联 FR**: FR-190
- **响应** (200): `[ { "version", "os", "arch", "cached", "sha256", "size", "sourceUrl", "cachedAt", "lastError" } ]`
- **错误**: 500 `WORKER_ASSET_LIST_FAILED`

### POST /api/v1/self-update/worker-assets/cache
- **描述**: 手动预缓存目标平台 Worker 二进制资产（FR-190）。CP 按当前 `version.Version` 在更新源中匹配 `component=worker, os, arch`，下载经当前 CP 出站代理，sha256 校验通过后落 `cache/worker-assets`
- **权限**: 平台管理员
- **关联 FR**: FR-190
- **请求**: `{ "os": "linux", "arch": "amd64" }`
- **响应** (200): `{ "version", "os", "arch", "cached": true, "sha256", "size", "sourceUrl", "cachedAt" }`
- **错误**: 409 `UPDATE_NOT_CONFIGURED` | 422 `UPDATE_NO_ARTIFACT` | 502 `WORKER_ASSET_FAILED`
- **审计**: `self_update.worker_asset_cache`（记录 version/os/arch/size，不记录下载 token）

### GET /worker-assets/:version/:os/:arch/worker
- **描述**: Worker 二进制 CP-local 下载端点（FR-190）。该端点不走 JWT，必须携带 CP 签发的短期 query token：`?token=<opaque>`。升级 token 绑定 `version/os/arch/purpose/nodeUuid`；安装 token 绑定 `version/purpose` 并允许 `os/arch` 通配，供安装脚本运行时替换平台模板；默认 TTL 10 分钟。`purpose=install` 且当前版本缓存未命中时，CP 会按更新源即时拉取、校验并缓存后下发。响应为二进制文件流
- **权限**: 短期 Worker 资产下载 token
- **关联 FR**: FR-190
- **响应** (200): `application/octet-stream`
- **错误**: 403 `INVALID_WORKER_ASSET_TOKEN` | 404 `WORKER_ASSET_NOT_CACHED` | 409 `UPDATE_NOT_CONFIGURED` | 422 `UPDATE_NO_ARTIFACT` | 502 `WORKER_ASSET_FAILED`

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
- **描述**: 创建拉取密钥；明文随此响应一次性返回，**创建后亦可经 reveal 端点查看**（FR-192：密钥发出后永久使用）。`value` 可选——留空自动生成，填入则用作自定义密钥明文值（管理员自控这把永久 key）
- **关联 FR**: FR-086 / FR-192
- **权限**: 平台管理员
- **请求**: `{ "name": "正式包", "expiresAt": "2027-01-01T00:00:00Z", "value": "自定义值" }`（`expiresAt`/`value` 均可选）
- **响应** (201):
  ```json
  { "id": 10, "name": "正式包", "keyPrefix": "jmck_ab12", "revoked": false, "expiresAt": null,
    "createdAt": "datetime", "revealable": true, "key": "jmck_<明文>" }
  ```
  （`revealable`=后端是否存有可逆加密副本，配了 `JIANMANAGER_CLIENT_KEY_ENC_SECRET` 或 dev 态为 `true`，FR-192）
- **错误**: 404 `CHANNEL_NOT_FOUND` | 400 `INVALID_REQUEST`
- **审计**: `client_key.create`（detail 不含明文，含 `custom` 标记是否自定义值）

### PUT /api/v1/client-channels/:id/keys/:kid
- **描述**: 编辑拉取密钥（FR-192，见 ADR-044）。改名必填；`value` 可选——留空仅改名，填入则改值（重算 `KeyHash` 使鉴权切到新值 + 重写 `KeyEnc` 供查看）。**改值会使持旧值的已分发客户端失效**。取代原「轮换」（已删除）
- **关联 FR**: FR-192
- **权限**: 平台管理员
- **请求**: `{ "name": "灰度", "value": "新密钥明文", "expiresAt": "2027-01-01T00:00:00Z" }`（`value` 可空=只改名；`expiresAt` 不传=保持原过期时间，传 ISO 时间=设置过期时间，传 `null`/空字符串=清空为永不过期）
- **响应** (200): 同创建响应；改值时 `key`=新明文（回显供复制），仅改名/改过期时间时 `key` 为空串
- **错误**: 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND` | 400 `INVALID_REQUEST`
- **审计**: `client_key.update`（detail 不含明文，含 `valueChanged` 标记是否改值）

### GET /api/v1/client-channels/:id/keys/:kid/reveal
- **描述**: 查看拉取密钥明文（FR-192，见 ADR-044）。密钥改可逆加密存储后管理员可随时查看明文 + 复制；鉴权仍只用哈希比对，行为不变。仅当后端存有可逆加密副本（`KeyEnc`）时可查看
- **关联 FR**: FR-192
- **权限**: 平台管理员
- **响应** (200): `{ "key": "jmck_<明文>" }`
- **错误**: 404 `KEY_NOT_REVEALABLE`（存量老哈希密钥 / 创建时未配 `JIANMANAGER_CLIENT_KEY_ENC_SECRET`，明文不可找回；可经 PUT 改值为已知值后再查看）| 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND`
- **审计**: `client_key.reveal`（detail 仅元数据 channelId/keyId，**绝不含明文**）
- **备注**: 频道详情/密钥列表的密钥元数据新增派生布尔 `revealable`（= 后端存有 `KeyEnc`），供前端对不可查看的老密钥禁用「查看」并提示；`KeyEnc`/`KeyHash` 本身不序列化

### DELETE /api/v1/client-channels/:id/keys/:kid
- **描述**: 吊销密钥（保留记录、标记 revoked，立即鉴权失效）
- **关联 FR**: FR-086
- **权限**: 平台管理员
- **响应** (200): `{ "message": "已吊销" }`
- **错误**: 404 `CHANNEL_NOT_FOUND` / `KEY_NOT_FOUND`
- **审计**: `client_key.revoke`

### GET /api/v1/client-channels/:id/updater-config
- **描述**: 按频道生成 `jm-updater.json`（FR-259，见 ADR-054）。返回完整配置字段，运营者直接下载放入整合包。FR-256 起不再含签名公钥（验签已去，信任靠 HTTPS + 拉取密钥鉴权，推翻 ADR-022/053）；`coreEndpoint` 配置字段已移除，楔子用 `endpoint + channel` 自动拼接 updater-core 端点。`endpoint` 按 CP 请求推断 API 根路径预填（可改，但必须保持 `/api/v1` 根路径，禁止填写 `/client-channels/...` 后缀）；`key` 留空占位由运营粘贴拉取密钥
- **关联 FR**: FR-259、FR-258
- **鉴权**: **JWT，平台管理员**
- **响应** (200):
  ```json
  { "channel": "skyblock-s1", "key": "", "endpoint": "https://cdn.example.com/api/v1",
    "timeoutSec": 120, "telemetry": true, "bootConfirmSec": 30 }
  ```
- **错误**: 403 `FORBIDDEN`（非平台管理员）| 404 `CHANNEL_NOT_FOUND`

---

## 客户端分发 manifest 与制品（FR-087/088）

> **鉴权分两组、物理隔离（ADR-022/023、contract §4；信任模型见 [ADR-054](../adr/054-updater-arch-simplification.md)）**：
> - **发布/版本管理端点**（运营操作）：`/api/v1` JWT，**仅平台管理员**（同频道管理 FR-086）。`POST .../files`、`POST .../versions`、`GET .../versions`、`GET .../versions/:version`、`POST .../rollback`、`GET/POST .../updater-core/versions`、`PUT .../updater-core/selected`。
> - **消费端点**（玩家）：**拉取密钥**鉴权（请求头 `X-Client-Key`，无 JWT），与运营浏览器入口隔离。`GET .../manifest`、`GET /client-artifacts/:sha256`、`GET .../updater-core`。
>
> 理由：拉取密钥半公开（随整包分发必然泄露），用它鉴权「发布」=严重漏洞。FR-256 起去掉 manifest Ed25519 验签（推翻 ADR-022/053）——私钥在服务器上验签形同虚设（服务器被攻破即私钥泄露），信任靠 **HTTPS + 拉取密钥鉴权 + sha256 完整性校验**。**版本历史仅管理面可见，玩家侧只认 latest**（FR-088）。

### POST /api/v1/client-channels/:id/files
- **描述**: 上传客户端文件制品（入 FR-045 制品库 `type=client-file`，按制品自身 sha256 内容寻址去重）。返回的 `sha256` 即 manifest `files[].artifact.sha256`
- **关联 FR**: FR-087
- **鉴权**: **JWT，平台管理员**（运营操作）
- **请求**: `multipart/form-data` — `file`（必）、`codec`（可，`zstd`|`none`）、`expectedSha256`（可，制品自身 sha256 校验）
- **响应** (201): `{ "sha256": "ef56…", "md5": "cd34…", "size": 45678, "codec": "zstd" }`（`md5` 供发布向导填 `file.md5`；codec=none 时制品即原始内容，`sha256`/`md5`/`size` 可直接作 `files[]` 的解压后字段）
- **错误**: 400 `INVALID_REQUEST`（缺 file）| 404 `CHANNEL_NOT_FOUND` | 422 `CHECKSUM_MISMATCH`
- **审计**: `client_file.publish`

### POST /api/v1/client-channels/:id/uploads
- **描述**: 大文件**分块上传**初始化（FR-251，增强 FR-088 单次上传）。声明文件总大小 → 建上传会话 → 返回 `uploadId` 与服务端敲定的 `chunkSize`/`chunkCount`（前端据此切片）。用于 4G+ 整合包，避免单请求超时、无进度、失败整传重来
- **关联 FR**: FR-251
- **鉴权**: **JWT，平台管理员**（运营操作，与 `POST .../files` 同组）
- **请求**: `{ "filename": "pack.zip", "totalSize": 5368709120, "chunkSize": 8388608 }`（`totalSize` 必 >0；`chunkSize` 可空，<=0 用默认 8 MiB，越界夹取到 [1 MiB, 64 MiB]）
- **响应** (201): `{ "uploadId": "a1b2…", "chunkSize": 8388608, "chunkCount": 640 }`
- **错误**: 400 `INVALID_REQUEST` / `INVALID_UPLOAD_INIT`（totalSize<=0）| 404 `CHANNEL_NOT_FOUND`

### PUT /api/v1/client-channels/:id/uploads/:uploadId/chunks/:index
- **描述**: 上传第 `index`（0 基）个分片。**幂等**——重传同 index 覆盖，支持失败重试；分片先落临时文件再原子 rename，避免半写脏片被 complete 采纳
- **关联 FR**: FR-251
- **鉴权**: **JWT，平台管理员**
- **请求**: body 为该分片**原始字节**（`application/octet-stream`，流式落盘、不缓冲整片进内存）；非末片须恰为 `chunkSize`，末片为末段余量
- **响应** (200): `{ "received": 12, "total": 640 }`（已收片数 / 总片数）
- **错误**: 400 `INVALID_REQUEST`（序号非法）/ `INVALID_CHUNK_INDEX`（越界）| 403 `UPLOAD_CHANNEL_MISMATCH` | 404 `UPLOAD_NOT_FOUND`（会话不存在 / 已过期）| 422 `INVALID_CHUNK_SIZE`（字节数不符）

### POST /api/v1/client-channels/:id/uploads/:uploadId/complete
- **描述**: 完成分块上传。校验分片齐全 + 总字节匹配 → 顺序拼装（`io.MultiReader` 流式，不额外落整文件）喂入 FR-045 制品库（同 `POST .../files` 的内容寻址 CAS）→ 返回与单次上传**逐字段一致**的结果 → 清理临时分片
- **关联 FR**: FR-251
- **鉴权**: **JWT，平台管理员**
- **请求**: `{ "codec": "none", "expectedSha256": "ef56…" }`（**均可选**，请求体可空；codec 空补 `none`）
- **响应** (201): `{ "sha256": "ef56…", "md5": "cd34…", "size": 5368709120, "codec": "none" }`（同 `POST .../files`，即 manifest `files[].artifact.sha256`）
- **错误**: 403 `UPLOAD_CHANNEL_MISMATCH` | 404 `UPLOAD_NOT_FOUND` | 422 `UPLOAD_INCOMPLETE`（缺片）/ `INVALID_CHUNK_SIZE`（拼装总字节不符）/ `CHECKSUM_MISMATCH`（expectedSha256 不符）
- **审计**: `client_file.publish`（detail 含 `via: "chunked"`）

### DELETE /api/v1/client-channels/:id/uploads/:uploadId
- **描述**: 弃单——移除上传会话 + 清临时分片。幂等（会话不存在亦返回 204）。前端取消上传时调用；空闲超 1h 的会话由后台 TTL 自动回收，CP 重启清残留分片
- **关联 FR**: FR-251
- **鉴权**: **JWT，平台管理员**
- **响应**: `204 No Content`
- **错误**: 403 `UPLOAD_CHANNEL_MISMATCH`（跨频道弃他人会话）

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
    "cleanExclude": ["玩家mod", "custom-mods"],
    "agent": { "wedge": { "version": 3 }, "core": { "version": 5, "platforms": { "windows": { "artifact": { "sha256": "…", "size": 0, "codec": "zstd" } } } } },
    "note": "首发" }
  ```
  - `files` 必填且非空；`path` 须 POSIX 相对路径不逃逸；`sync∈{strict,once,ignore}`；`platform∈{null,windows,macos,linux}`；非 `ignore` 文件须带 `artifact.sha256`
  - `managedDirs`（FR-255 扩语义）：托管/自动清理目录，可含嵌套路径串（如 `config/foo`，客户端前缀匹配）；含哨兵 `"*"` 时语义 = 清空整个 gameDir（删清单未列的一切，玩家区 + `cleanExclude` 除外）；留空则不自动清理
  - `cleanExclude`（FR-255，可选）：运营自定义追加排除，命中前缀的路径永不删（叠加在玩家区 `PLAYER_ZONE` 之上）；不得为 `"*"`（与哨兵冲突）、不得路径逃逸；空则省略（`omitempty`，老 manifest JSON 不变）
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
- **响应** (200): `{ "version": 1, "note": "…", "createdBy": 1, "createdAt": "datetime", "isLatest": false, "managedDirs": ["mods"], "cleanExclude": ["custom-mods"], "files": [ { "path": "mods/foo.jar", "sha256": "…", "md5": "…", "size": 0, "sync": "strict", "platform": null, "artifact": { … } } ], "agent": { … } }`
- **错误**: 400 `INVALID_REQUEST`（版本号非法）| 404 `CHANNEL_NOT_FOUND` / `VERSION_NOT_FOUND`

### GET /api/v1/client-channels/:id/files/content
- **描述**: 按制品 `sha256` 读取 client-file 制品内容用于**管理台文本预览**（FR-214，复用 FR-213 共享文件浏览器预览发布草稿/版本文件）。**只读**。玩家消费端点 `GET /client-artifacts/:sha256` 走拉取密钥、与浏览器 JWT 入口物理隔离（ADR-022/023），管理台无拉取密钥不能复用之取内容预览，故补此 JWT 路径。降级由 `kind` 显式表达，前端据此渲染或降级（不可预览必可下载）
- **关联 FR**: FR-214 | **鉴权**: **JWT，平台管理员**（运营操作）
- **查询参数**: `sha256`（必，制品自身 sha256 = `files[].artifact.sha256`）
- **响应** (200): `{ "kind": "text"|"binary"|"too-large", "content": "…", "size": 45678, "codec": "none" }`
  - `kind=text`：`content` 为 UTF-8 文本；`kind=binary`（含 NUL 或已压缩制品，本期发布恒 `codec=none`，`zstd` 等不在管理面解压）/`kind=too-large`（超 1 MiB 不读全量）：仅 `size`/`codec`，无 `content`，前端降级为「仅下载」
- **错误**: 400 `INVALID_REQUEST`（缺 sha256）| 403 `FORBIDDEN`（非平台管理员）| 404 `ARTIFACT_NOT_FOUND`

### GET /api/v1/client-channels/:id/files/download
- **描述**: 按制品 `sha256` 下载 client-file 制品（**管理台**，FR-214）。与上同理：浏览器需一个 JWT 下载入口（含预览降级态的下载兜底），与玩家拉取密钥制品端点隔离
- **关联 FR**: FR-214 | **鉴权**: **JWT，平台管理员**（运营操作）
- **查询参数**: `sha256`（必）
- **响应** (200): 制品二进制（`Content-Disposition: attachment`）。支持 `Range`（`http.ServeContent`）
- **错误**: 400 `INVALID_REQUEST`（缺 sha256）| 403 `FORBIDDEN` | 404 `ARTIFACT_NOT_FOUND`

### POST /api/v1/client-channels/:id/rollback
- **描述**: 运营回滚——取历史版本 `sourceVersion` 的内容，**以更高版本号重发为新 latest**（保持 `version` 单调，客户端按防降级正常前进、不被拒，ADR-022 §3 / contract §3）。不下发更低版本号
- **关联 FR**: FR-088
- **鉴权**: **JWT，平台管理员**（运营操作）
- **请求**: `{ "sourceVersion": 1, "note": "可选，空则生成「回滚至 vN」" }`
- **响应** (201): `{ "id": 7, "channelId": "skyblock-s1", "version": 3, "sourceVersion": 1, "note": "回滚至 v1", "createdAt": "datetime" }`
- **错误**: 400 `INVALID_REQUEST`（缺/非法 sourceVersion）| 404 `CHANNEL_NOT_FOUND` / `VERSION_NOT_FOUND`
- **审计**: `client_version.rollback`

### GET /api/v1/client-dist/events
- **描述**: 拉取/下载明细检索（FR-093 全链路追踪；FR-249 错误追踪；FR-265 日志 Tab 兼容入口）。拉取失败（含 401 鉴权失败）也记录事件并带语义错误码 `errCode`。明细短保留（默认 14 天滚动清理）；发布事件审计见 `/audit`（`client_*.publish`/`rollback`）
- **关联 FR**: FR-093、FR-249、FR-265
- **鉴权**: **JWT，平台管理员**
- **查询参数**: `channelId` / `machineId` / `ip` / `kind`(manifest|artifact) / `outcome`(success|failure，空=全部；failure⟺status≥400，success⟺0<status<400 含 200/206/304) / `errCode`(精确筛，如 `INVALID_CLIENT_KEY`) / `version` / `since`(RFC3339) / `until`(RFC3339) / `limit`(默认 200，上限 1000)
- **响应** (200): `[ { "id", "channelId", "machineId", "ip", "kind", "version", "artifactSha", "bytes", "status", "errCode", "errReason", "method", "path", "etag", "durationMs", "createdAt" } ]`（created_at DESC）。`errCode` 成功事件为空、失败事件填语义码（`INVALID_CLIENT_KEY`/`NO_LATEST_VERSION`/`ARTIFACT_NOT_FOUND`/`SIGN_KEY_NOT_CONFIGURED`/`CHANNEL_NOT_FOUND`/`INTERNAL_ERROR`）

### GET /api/v1/client-dist/events/search
- **描述**: 分发请求日志分页检索（FR-265）。在兼容 `/client-dist/events` 数组响应的基础上，提供日志 Tab 使用的分页对象，并支持运行态维度筛选。
- **关联 FR**: FR-265 | **鉴权**: **JWT，平台管理员**
- **查询参数**: 兼容 `/client-dist/events` 的 `channelId` / `machineId` / `ip` / `kind` / `outcome` / `errCode` / `version`，并新增 `artifactSha` / `runtimeVersion` / `coreVersion` / `platform` / `lag` / `page`(默认 1) / `pageSize`(默认 100，上限 500)
- **响应** (200): `{ "items":[{ "id", "channelId", "machineId", "ip", "kind", "version", "artifactSha", "bytes", "status", "errCode", "errReason", "method", "path", "etag", "durationMs", "createdAt" }], "page":1, "pageSize":100, "total":1234 }`

### GET /api/v1/client-dist/events/:id
- **描述**: 单条分发请求脱敏详情（FR-265）。仅返回白名单请求/响应头；`X-Client-Key` 只保存 `present`/脱敏标记，绝不保存明文。
- **关联 FR**: FR-265 | **鉴权**: **JWT，平台管理员** | **审计**: `client_dist_event.detail`
- **响应** (200): `{ "id", "channelId", "machineId", "ip", "kind", "version", "artifactSha", "bytes", "status", "errCode", "errReason", "method", "path", "etag", "durationMs", "createdAt", "requestHeaders":{}, "responseHeaders":{} }`
- **错误**: 400 `INVALID_REQUEST`（事件 ID 非法）| 404 `EVENT_NOT_FOUND`

### GET /api/v1/client-dist/realtime
- **描述**: 分发请求近实时聚合（FR-265 监控 Tab）。只统计 manifest/artifact HTTP 请求健康度，不混入客户端更新成功率或运行版本。
- **关联 FR**: FR-265 | **鉴权**: **JWT，平台管理员**
- **查询参数**: `channelId`（可选）
- **响应** (200): `{ "summary1h":{ "manifestPulls", "artifactPulls", "errorRequests", "activeMachines" }, "requestRate24h":[{ "ts", "manifest", "artifact", "error" }], "recentErrors":[{ "id", "time", "channelId", "kind", "target", "ip", "status", "errCode" }], "topIps1h":[{ "ip", "count" }] }`

### GET /api/v1/client-dist/clients
- **描述**: 客户端运行态聚合（FR-265 客户端 Tab）。读取启动心跳最新态与 `client_telemetry` 更新结果；不承诺“在线客户端”。
- **关联 FR**: FR-265 | **鉴权**: **JWT，平台管理员** | **审计**: `client_dist_clients.query`
- **查询参数**: `channelId`（可选）、`range`(`24h`|`7d`|`30d`|`90d`，默认 `7d`)
- **响应** (200): `{ "channelId", "from", "to", "summary":{ "recentStarted", "todayStarted", "recentStarts", "todayStarts", "updateSuccessRate", "updateFailureRate" }, "items":[{ "channelId", "machineId", "playerName", "ip", "platform", "javaVersion", "launcher", "coreVersion", "localVersion", "firstSeenAt", "lastHeartbeatAt" }], "runtimeVersionDist":[{ "version", "count" }], "coreVersionDist":[{ "value", "count" }], "platformDist":[{ "value", "count" }], "launcherDist":[{ "value", "count" }], "lagDist":[{ "lag", "count" }], "updateResultSeries":[{ "ts", "success", "failStatic", "rolledBack", "error" }] }`

### POST /api/v1/client-channels/:id/telemetry/heartbeat
- **描述**: updater-core 启动心跳（FR-265）。按 `channel_id + machine_id` upsert `client_runtime_states`，只更新运行态，不写 `client_telemetry`，因此不会污染更新成功率。`playerName` 优先兼容 `X-Player-Name`，为空时按 `channel + X-Machine-Id + X-Install-Id` 从 `client_security_profiles` 反查最近安全画像补全。
- **关联 FR**: FR-265 | **鉴权**: 玩家侧拉取密钥（`X-Client-Key`，必须属于该频道）
- **请求头**: `X-Machine-Id`（必需，否则 best-effort 忽略心跳）、`X-Install-Id`（可选但推荐，用于关联安全画像）、`X-Player-Name`（兼容旧客户端，可选且不可信，仅用于观测与排障）
- **请求**: `{ "platform":"windows", "javaVersion":"17.0.10", "launcher":"HMCL", "coreVersion":"3", "localVersion":15 }`
- **响应**: `202 Accepted`
- **错误**: 401 `INVALID_CLIENT_KEY`

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

---

## 客户端分发防护中心（FR-264）

### POST /api/v1/client-security/hello
- **描述**: updater-core 启动早期上报安全画像。`machineId`、`installId`、`channel` 必填；`playerName` 来自 body 或 `X-Player-Name`，可为空且可伪造，仅作粗略排查线索。IP 临时封禁先于密钥校验生效。
- **关联 FR**: FR-264
- **鉴权**: 玩家侧拉取密钥（`X-Client-Key`）
- **请求**:
  ```json
  { "channel":"skyblock-s1", "playerName":"Steve", "machineId":"m-1", "installId":"i-1",
    "coreVersion":"5", "wedgeVersion":"3", "manifestVersion":"12",
    "os":"windows", "osVersion":"10", "arch":"amd64", "javaVendor":"Temurin",
    "javaVersion":"17", "javaArch":"amd64", "launcher":"official", "locale":"zh-CN",
    "timezone":"Asia/Shanghai", "memoryTier":"4-8g" }
  ```
- **响应**: `202 Accepted`
- **错误**: 400 `INVALID_REQUEST` | 401 `INVALID_CLIENT_KEY` | 403 `IP_TEMP_BLOCKED` / `CLIENT_KEY_SUSPENDED`

### GET /api/v1/client-dist/security/overview
- **描述**: 防护中心安全总览（画像、风险事件、动作、活跃封禁等聚合）。
- **鉴权**: JWT，平台管理员
- **响应** (200): `{ "activeDownloads", "downloadBytesPerSecond", "abnormalRequests", "unauthorizedRequests", "forbiddenRequests", "rateLimitedRequests", "blockedIpCount", "throttledKeyCount", "protectedChannelCount", "topIps", "topKeys", "topChannels", "topPlayers" }`

### GET /api/v1/client-dist/security/events
- **描述**: 风险事件列表，按创建时间倒序。
- **鉴权**: JWT，平台管理员
- **响应** (200): `[ { "id", "subjectType", "subjectValue", "channelId", "machineId", "installId", "playerName", "ip", "keyId", "keyPrefix", "ruleCode", "severity", "scoreDelta", "action", "reason", "createdAt" } ]`

### GET /api/v1/client-dist/security/logs
- **描述**: 客户端分发安全全量日志详情，合并 security hello、风险事件、保护动作、分发请求日志、运行态心跳与更新遥测。管理台「客户端分发安全 / 日志详情」消费此接口。
- **鉴权**: JWT，平台管理员
- **查询参数**: `type`（可选，`hello|risk|action|request|runtime|telemetry`）、`channelId`、`machineId`、`playerName`、`ip`、`page`、`pageSize`（默认 50，上限 200）
- **响应** (200): `{ "items":[{ "id", "type", "title", "channelId", "machineId", "playerName", "ip", "status", "errCode", "createdAt", "detail" }], "total", "page", "pageSize" }`

### GET /api/v1/client-dist/security/profiles
- **描述**: 客户端安全画像列表，按最近出现倒序。
- **鉴权**: JWT，平台管理员
- **响应** (200): `[ { "id", "channelId", "machineId", "installId", "playerName", "keyId", "keyPrefix", "lastIp", "coreVersion", "os", "javaVersion", "riskLevel", "protectionState", "lastSeen" } ]`

### GET /api/v1/client-dist/security/ip-analysis
- **描述**: IP 剖析聚合，用于查异常来源 IP。
- **鉴权**: JWT，平台管理员
- **查询参数**: `limit`（默认 200）
- **响应** (200): `[ { "ip", "requestCount", "rejectCount", "invalidKeyCount", "notFoundCount", "rangeCount", "downloadBytes", "keyCount", "channelCount", "riskScore", "blocked", "lastSeen" } ]`

### GET /api/v1/client-dist/security/player-analysis
- **描述**: 玩家名剖析聚合。玩家名可伪造，不作为可信身份。
- **鉴权**: JWT，平台管理员
- **查询参数**: `limit`（默认 200）
- **响应** (200): `[ { "playerName", "installCount", "machineCount", "ipCount", "keyCount", "channelCount", "downloadBytes", "abnormalRequests", "riskScore", "lastSeen" } ]`

### GET /api/v1/client-dist/security/actions
- **描述**: 保护动作列表（IP 封禁、key 状态、频道保护等）。
- **鉴权**: JWT，平台管理员
- **响应** (200): `[ { "id", "targetType", "targetValue", "channelId", "action", "status", "reason", "auto", "expiresAt", "createdBy", "createdAt", "canceledAt" } ]`

### POST /api/v1/client-dist/security/ip-blocks
- **描述**: 手动临时封禁 IP。命中消费端点返回 `IP_TEMP_BLOCKED` + `Retry-After`。
- **鉴权**: JWT，平台管理员
- **请求**: `{ "ip":"192.0.2.1", "reason":"异常拉取", "ttlSeconds":3600 }`（也兼容 `durationMinutes`）
- **响应** (201): `ClientProtectionAction`
- **错误**: 400 `INVALID_REQUEST`

### POST /api/v1/client-dist/security/ip-blocks/:id/cancel
- **描述**: 取消 IP 临时封禁 / 处置动作，状态置 `canceled`。
- **鉴权**: JWT，平台管理员
- **响应** (200): `{ "ok": true }`

### POST /api/v1/client-dist/security/keys/:id/state
- **描述**: 切换拉取密钥安全状态：`normal` / `observe` / `throttled` / `suspended` / `revoked`。`suspended` 拉取返回 `CLIENT_KEY_SUSPENDED`。
- **鉴权**: JWT，平台管理员
- **请求**: `{ "state":"suspended", "reason":"异常拉取" }`
- **响应** (200): `{ "ok": true }`

### PUT /api/v1/client-dist/security/channels/:id/protection
- **描述**: 设置频道保护模式。频道只允许降速 / 降级保护，不做自动封禁。
- **鉴权**: JWT，平台管理员
- **请求**: `{ "mode":"protected", "reason":"异常流量" }`
- **响应** (200): `{ "ok": true }`
- **错误**: 404 `CHANNEL_NOT_FOUND`

### DELETE /api/v1/client-dist/security/channels/:id/protection
- **描述**: 清除频道保护模式，恢复 `normal`。
- **鉴权**: JWT，平台管理员
- **响应** (200): `{ "ok": true }`

### GET /api/v1/client-dist/security/groups
- **描述**: 安全分组列表。
- **鉴权**: JWT，平台管理员
- **响应** (200): `[ { "id", "name", "kind", "targetType", "enabled", "createdBy", "createdAt", "updatedAt" } ]`

### POST /api/v1/client-dist/security/groups
- **描述**: 创建安全分组。
- **鉴权**: JWT，平台管理员
- **请求**: `{ "name":"高风险 IP", "kind":"manual", "targetType":"ip", "rule":{}, "actionPolicy":{}, "enabled":true }`
- **响应** (201): `ClientSecurityGroup`

### PUT /api/v1/client-dist/security/groups/:id
- **描述**: 更新安全分组。
- **鉴权**: JWT，平台管理员
- **请求**: 同创建分组
- **响应** (200): `ClientSecurityGroup`

### DELETE /api/v1/client-dist/security/groups/:id
- **描述**: 删除安全分组。
- **鉴权**: JWT，平台管理员
- **响应** (200): `{ "ok": true }`

### GET /api/v1/client-dist/security/privacy-notice
- **描述**: 防护中心遥测告知文案。
- **鉴权**: JWT，平台管理员
- **响应** (200): `{ "requiredFields", "diagnosticFields", "notice", "retentionDays" }`

### FR-264 消费端错误码

| 错误码 | HTTP | 说明 |
|---|---:|---|
| `IP_TEMP_BLOCKED` | 403 | IP 被临时封禁，带 `Retry-After` |
| `CLIENT_KEY_SUSPENDED` | 403 | 拉取密钥暂停，带 `Retry-After` |
| `RATE_LIMITED` | 429 | per-key / per-channel 限速，带 `Retry-After` |
| `CHANNEL_PROTECTED` | 429 | 频道保护模式下制品下载降速 / 暂缓，带 `Retry-After` |
| `DOWNLOAD_CONCURRENCY_LIMITED` | 429 | 下载并发过高，带 `Retry-After` |
| `BANDWIDTH_LIMITED` | 429 | 字节配额受限，带 `Retry-After` |
| `ARTIFACT_NOT_ALLOWED` | 403 | 制品不在该 key 所属频道允许范围内 |

---

### GET /api/v1/client-dist/stats
- **描述**: 分发统计后台（FR-095）：只读聚合 FR-093/094/092 数据，按频道 + 时间窗
- **关联 FR**: FR-095 | **鉴权**: **JWT，平台管理员**
- **查询参数**: `channelId`（频道）、`days`（窗口天数，默认 30，上限 365）
- **响应** (200): `{ "channelId", "days", "downloads":[{day,requests,bytes}], "versions":[{version,requests}], "results":[{result,count}], "successRate", "rollbackRate", "activeMachines", "topIps":[{ip,count}] }`

### GET /api/v1/client-dist/observability
- **描述**: 客户端分发**观测数据底座**（FR-217，见 ADR-049；FR-265 修订指标边界）：消费后台离线卷积的小时级时序快照 `client_dist_snapshots`（源 FR-093 events + FR-094 telemetry），返**跨频道/单频道**的时序 + 区间分布聚合 + 汇总标量。与 FR-095 `/client-dist/stats`（单频道按日看板）并存不替代——本端点服务观测·分发监控页的跨频道/平台时序
- **关联 FR**: FR-217（消费方 FR-218/219）、FR-265 | **鉴权**: **JWT，平台管理员** | **审计**: `client_dist_observability.query`
- **查询参数**: `channelId`（可，省略=**总**，跨频道合并含空频道桶）、`from`/`to`（可，RFC3339，同时给且 `to>from`）、`range`（可，无 from/to 时回退枚举 `24h`/`7d`/`30d`/`90d`/`180d`，默认 `7d`）
- **响应** (200):
  ```
  {
    "channelId", "from", "to",
    "series": [{ ts, manifestPulls, artifactPulls, downloadBytes,
                 activeMachines, updateTotal, updateSuccess, updateFailStatic,
                 updateRolledBack, updateError }],   // 按 ts 升序的小时桶；跨频道时同小时合并；缺数小时无点
    "summary": { manifestPulls, artifactPulls, downloadBytes,
                 updateTotal, updateSuccess, updateFailStatic, updateRolledBack, updateError,
                 successRate, failStaticRate, rollbackRate,
                 activeMachines, activeMachinesExact },   // activeMachinesExact: 区间在明细保留窗(14d)内=精确去重独立数 true；窗外=各桶人次求和近似 false（ADR-049 §4）
    "versionDist": [{ version, count }],     // 区间内跨桶合并、按 count 降序
    "platformDist": [{ os, count }],
    "lagDist": [{ lag, count }]              // current_version - toVersion，按 lag 升序
  }
  ```
- **错误**: 400 `INVALID_RANGE`（from/to 非法或 `to<=from`，或 range 非枚举）| 403 `FORBIDDEN`（非平台管理员）| 500 `INTERNAL_ERROR`
- **说明**: 未知 `channelId` 返 200 空时序 + 零汇总（不 404，避免泄露频道存在性、便于前端统一空态）。machineId 客户端可伪造、不可信，仅统计近似（ADR-023）

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
- **描述**: 返回频道 **latest** 的 manifest（contract §2；FR-256 起去 `sig` 段不再验签，见 [ADR-054](../adr/054-updater-arch-simplification.md)）。只提供当前版本，不暴露历史。`agent.core` 由频道选定 updater-core 版本驱动（FR-259，见 ADR-054 修订 ADR-045）；无选定版本时回退手填透传（兼容）。`agent.wedge` 仍来自发布快照（楔子冻结、信息性）
- **关联 FR**: FR-087、FR-092（机器码登记）、FR-259（`agent.core` 由选定 core 版本驱动）
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必）；`X-Machine-Id`（可，机器码统计/辅助限流）。**无 JWT**
- **机器码登记（FR-092）**: 鉴权通过后若 `X-Machine-Id` 非空，则 best-effort 登记入 `client_machines`（弱一致、失败不阻断）。机器码**客户端生成、不可信**，仅统计 + 辅助限流（限流主键 IP，FR-096），**不作授权依据**
- **响应** (200): contract §2 的 manifest（去 `sig` 段）
  - Headers：`ETag: "<version>"`、`Cache-Control: no-cache`（弱缓存，靠 ETag 命中省传输）
- **响应** (304): `If-None-Match` 命中 ETag（Not Modified）
- **错误**: 401 `INVALID_CLIENT_KEY`（无/无效/吊销/过期 key）| 404 `CHANNEL_NOT_FOUND` / `NO_LATEST_VERSION`（频道尚未发布版本）

### GET /api/v1/client-artifacts/:sha256
- **描述**: 按内容寻址下载客户端制品（zstd 压缩流或原文，按 codec）。制品跨频道共享，路径无频道段
- **关联 FR**: FR-087
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必，任一有效密钥即授权）；`X-Machine-Id`（可）。**无 JWT**
- **响应** (200/206): 二进制制品；支持 `Range`（断点续传，206 部分内容）；强缓存（内容寻址不可变，`Cache-Control: public, max-age=31536000, immutable` + `ETag` 为内容 sha256）
- **错误**: 401 `INVALID_CLIENT_KEY` | 404 `ARTIFACT_NOT_FOUND` | 416（Range 越界，由 `http.ServeContent` 处理）

### GET /api/v1/client-channels/:id/updater-core
- **描述**: 返回频道当前选定 updater-core 分发信息（FR-259，见 [ADR-054](../adr/054-updater-arch-simplification.md)）。楔子首次启动 / 后续启动只按 `version` 是否大于本地 `selectedVersion` 决定是否下载；这里的 `version` 是频道级递增分发版本，不等同于归档列表里的 jar 版本。切回旧 `sha256` 回滚时，后端仍会抬高分发版本，确保冻结 wedge 会下载目标旧 jar。返回格式冻结（spec §2.5.3），后续 CP 升级只能加字段不能删/改已有字段
- **关联 FR**: FR-259、FR-258
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必）。**无 JWT**
- **响应** (200): `{ "version": 3, "sha256": "ab12…", "downloadUrl": "/api/v1/client-artifacts/<sha256>", "size": 2097152 }`（`version` 为频道级分发版本；`sha256` 才是实际 core jar 制品；`downloadUrl` 指向制品分发端点，可 Range 续传）
- **错误**: 401 `INVALID_CLIENT_KEY` | 404 `CHANNEL_NOT_FOUND` / `NO_SELECTED_CORE`（频道未选定 core 版本）

### GET /api/v1/client-channels/:id/updater-core/versions
- **描述**: 列出全部归档 updater-core 版本（含选定标记），供运营面板切换回滚（FR-259）
- **关联 FR**: FR-259 | **鉴权**: **JWT，平台管理员**
- **响应** (200): `[ { "version": 2, "coreVersion":"0.1.0-SNAPSHOT", "displayVersion":"0.1.0-SNAPSHOT+abc123def456.dirty", "gitCommit":"abc123def456", "dirty":true, "buildTime":"datetime", "sha256": "ab12…", "size": 2097152, "createdAt": "datetime", "selected": true }, { "version": 1, "sha256": "cd34…", "size": 2048000, "createdAt": "datetime", "selected": false } ]`（`version` 是数字归档版本；`coreVersion/displayVersion/gitCommit/dirty/buildTime` 来自 jar 内元信息，旧 jar 可为空）
- **错误**: 403 `FORBIDDEN`（非平台管理员）| 404 `CHANNEL_NOT_FOUND`

### POST /api/v1/client-channels/:id/updater-core/versions
- **描述**: 手动上传 updater-core.jar hotfix，归档为 `client-updater-core` 制品；可选择上传后立即作为当前频道选定版本（FR-259 增强）。客户端下次启动按 updater-core 端点拿到该版本。
- **关联 FR**: FR-259 | **鉴权**: **JWT，平台管理员**
- **请求**: `multipart/form-data`，字段：`file`（必填，jar 文件）、`version`（可选兜底；后端优先读取 jar 内 `META-INF/jm-updater-core.properties` / Manifest 元信息）、`select`（可选，`true`/`1` 表示上传后立即选用）。缺少元信息的紧急 hotfix jar 也允许上传。
- **响应** (200): `{ "version": 9, "coreVersion":"0.1.1-hotfix", "displayVersion":"0.1.1-hotfix+def456abc789", "gitCommit":"def456abc789", "dirty":false, "buildTime":"datetime", "sha256": "ab12…", "size": 2097152, "createdAt": "datetime", "selected": true }`
- **错误**: 400 `INVALID_REQUEST`（缺文件/文件不可读）| 403 `FORBIDDEN`（非平台管理员）| 404 `CHANNEL_NOT_FOUND`
- **审计**: `client_core.upload`

### PUT /api/v1/client-channels/:id/updater-core/selected
- **描述**: 切换频道选定的 updater-core 版本（一键回滚，FR-259）。后端写入目标 `sha256` 并维护频道级递增分发版本；客户端下次启动按 API 根 `endpoint` 自动查询 updater-core 端点，看到更大的 `version` 后下载目标 `sha256`，因此即使回滚到旧归档 jar 也会生效
- **关联 FR**: FR-259 | **鉴权**: **JWT，平台管理员**
- **请求**: `{ "sha256": "ab12…" }`
- **响应** (200): `{ "ok": true }`
- **错误**: 403 `FORBIDDEN`（非平台管理员）| 404 `CHANNEL_NOT_FOUND` / `CORE_VERSION_NOT_FOUND`
- **审计**: `client_updater_core.select`

### POST /api/v1/client-telemetry
- **描述**: 客户端遥测上报（FR-094，contract §4.3）。**best-effort、202 不阻塞**；隐私可关在客户端
- **关联 FR**: FR-094
- **鉴权**: **拉取密钥**（请求头 `X-Client-Key`，必，任一有效密钥）；`X-Machine-Id`（可但推荐）、`X-Install-Id`（可但推荐，用于关联安全画像）、`X-Player-Name`（兼容旧客户端，可选且不可信；为空时按 `channel + machineId + installId` 从安全画像补全）。**无 JWT**
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
