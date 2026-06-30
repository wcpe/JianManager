# Docker 实例修通 + 一键 Minecraft 傻瓜建服 + Docker 检测（FR-078 续 / FR-236 / FR-237）

> 状态：开发中 · 完成 FR-078（容器化实例运行）· 新 FR-237（Docker 检测）· FR-236 docker 傻瓜建服
> 关联 ADR-019（Docker 策略）/ ADR-003（区别：docker 不叠 daemon wrapper）

## 1. 背景与根因（已真机证）

Docker **容器引擎链路本身是通的**——`dockerStrategy` 真机集成测试全过（拉镜像→创建→绑定 Windows 工作目录→端口映射→attach→stdin/stdout→停止→清理），CP→Worker 也完整下发 image/端口/限额（`buildCreateInstanceRequest` → `SetDockerConfig`）。

用户报「docker 起不来」的真因是**创建流程缺少 MC-in-docker 惯用法**：

1. **缺 `EULA=TRUE`**（真机证）：标准镜像 `itzg/minecraft-server` 无 `-e EULA=TRUE` 时**启动即退出**——
   `[init] [ERROR] Please accept the Minecraft EULA … -e EULA=TRUE`。创建向导从不为 docker 实例设 MC 环境变量。
2. **强制启动命令**：`CreateInstanceRequest.StartCommand` 带 `binding:"required"`，docker 镜像（itzg 等）由 entrypoint 自管启动、本应**空命令**，被迫填 java 命令既无意义、对部分镜像还覆盖 CMD。
3. **无 docker 可用性检测**：节点没装/没起 Docker 时仍可选 docker 模式，创建后才在 worker 端 `连接本机 Docker 守护进程失败`。

端口映射不在问题内：`dockerPortMappings` 已把 MC 实例宿主 `ServerPort` 映射到容器 25565（tcp+udp）。

## 2. 设计

### 2.1 FR-078 修通：docker 启动命令可空 + MC 环境

- **后端**：去掉 `CreateInstanceRequest.StartCommand` 的 `binding:"required"`，改在 `InstanceService.Create` 条件校验——**非 docker（daemon/direct）仍必填**；**docker 允许空**（空=交镜像 entrypoint 自管）。`sanitizeStartCommand` 容空。
- **EnvVars**：创建请求与向导提交携带 `envVars`（CP 已支持持久化 + 下发；向导提交此前未带，补上）。

### 2.2 FR-236 一键 Minecraft（docker 傻瓜建服）

- **向导**：`processType=docker` 且 `type=minecraft_java` 时，出「一键 Minecraft 服（itzg 镜像）」预设，点一下即：
  - `image = itzg/minecraft-server:latest`
  - `envVars: { EULA: "TRUE" }`（+ 选填简单字段 `VERSION`、`TYPE`，留空走镜像默认）
  - `startCommand` 置空（镜像自管）
  - 宿主端口由既有 MC 端口池分配 → `dockerPortMappings` 自动映射到容器 25565
- 即「选节点→点一键 Minecraft→建」三步起一个真能跑的 MC docker 服。高级用户仍可改镜像/资源/环境。
- 与 FR-189/FR-230 向导协调：docker 高级步加「镜像预设 + 简易环境（EULA 等）」，不破既有 docker 字段（image/cpu/mem）。

### 2.3 FR-237 Docker 可用性检测 + 引导

- **Worker**：新增轻量 RPC `CheckDocker`（或复用现有探测）→ 返回 `available`/`version`/`error`（探本机 Docker 守护进程，复用 `dockerClientFromEnv` + `ServerVersion`）。
- **CP**：`POST /nodes/:id/docker-check` 委托 Worker，返回 `{available, version, error}`；节点离线 503。
- **向导**：选 docker 模式时探目标节点 docker，可用则显「Docker vX 可用」、不可用则禁用 docker 提交并提示「该节点未检测到 Docker」；与 FR-229「连通性测试族」同范式（复用 `PingNodeButton` 风格组件）。

## 3. 验收

- [ ] 后端：docker 实例可空 `startCommand` 创建成功；非 docker 仍校验必填（单测）。
- [ ] 一键 Minecraft：向导 `processType=docker`+MC 点预设 → image=itzg、envVars 含 `EULA=TRUE`、空命令、端口映射就绪（前端单测/组件测）。
- [ ] FR-237：`POST /nodes/:id/docker-check` 有 docker 返 available+version、无返 available=false+error、离线 503（后端单测）；向导 docker 不可用禁用+提示（组件测）。
- [x] **真机根因证**：`docker run itzg/minecraft-server` 无 EULA → EULA 错误退出；带 `-e EULA=TRUE` → 越过 EULA、`Downloading server`（2026-06-30）。
- [x] **真机验（FR-078/236）**：本机 Docker 节点经向导「一键 Minecraft」建 docker MC 实例（**空启动命令被接受**）→ 启动 → `docker ps` 见 `jianmanager-<uuid>` 容器 RUNNING（Up，25565/tcp）、itzg **越过 EULA** 下载启动服务端（`Resolved version … Downloading … server`）、UI 实例转 RUNNING（2026-06-30）。
- [x] **真机验（FR-237）**：创建向导选 docker 模式进高级步 → 自动探节点 Docker → 横幅显示「Docker 29.4.1 可用」（2026-06-30）；不可用节点由 `stepValid` 阻止提交（gating 逻辑覆盖，本机仅有 docker 节点故未真机走不可用分支）。

## 4. 关联

worker `process/docker.go`（已通）、`process/images.go`、新 `CheckDocker`；proto 可能加 `CheckDockerRequest/Response`；CP `service/instance.go`（Create 校验 + envVars）、`service/docker_image.go`、`router/`；web `pages/InstanceWizardPage.tsx`（docker 预设 + 环境 + 检测）、`api/`。
