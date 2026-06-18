# 实施计划 — FR-034 搭建 Bukkit 子服

> 关联 FR: FR-034 | 优先级: P1 | 状态: 🔨 in-progress（后端到 provision 端点已完成，前端向导待做）

## 任务拆解

### Phase 0: 运行时底座（前置，已完成）
- [x] 结构化启动派生 + 绑定 JDK 下发（ADR-008 / FR-033 Phase 3）：`service/launch.go`，`registerOnWorker` 解析 `NodeJDK.Path` 下发，Worker 注入 `JAVA_HOME`/`PATH`
- [x] 同节点唯一端口分配（FR-032 slice）：`service/ports.go`，Instance 增 `server_port`/`query_port`

### Phase 1: 核心下载（已完成）
- [x] `service/core.go`（`CoreService`）走 PaperMC API 列版本、解析最新/指定构建的下载 URL + sha256（httptest 覆盖）
- [x] `GET /api/v1/cores`（无 mcVersion 列版本；带 mcVersion 返回下载信息）
- [ ] Purpur / Spigot 下载源（后续；Spigot 需 BuildTools）

### Phase 2: 子服搭建（已完成）
- [x] `DownloadCore` Worker RPC（`proto/worker.proto` + `internal/worker/grpc/provision_ops.go`）：下载核心 jar 到工作目录 + sha256 校验
- [x] 工作目录系统分配（复用 FR-044）
- [x] 写入 `eula.txt` / `server.properties`（server-port / online-mode=false / rcon / query）经已有 `WriteConfig` RPC
- [x] 生成结构化 `LaunchSpec`（核心固定落地 `server.jar`）

### Phase 3: Control Plane 创建流程（已完成）
- [x] `POST /api/v1/instances/provision/bukkit`（`router/provision.go` + `service/provision.go`，平台管理员）
- [x] 串起：解析核心 → 分配端口 → 创建实例（结构化启动 + 绑定 JDK + 端口）→ 注册 Worker → 下载核心 → 写基础配置
- [ ] 可选：创建时即注册进所选代理（待 FR-035/032 注册关系）

### Phase 4: 前端向导（待做）
- [ ] 核心类型/版本选择（消费 `GET /cores`）
- [ ] JDK 与内存参数选择
- [ ] 代理注册可选项
- [ ] 创建后跳转实例详情并可一键启动

## 后续优化 / 待办
- **核心走制品库**：当前 Worker 直接 HTTP 下载核心；应改为 CP `IngestFromURL` 入制品库（FR-045，去重/校验/模板源）后再交付 Worker（单机可让 Worker 从数据根复制，远程走上传），避免重复下载。
- **真机验收**：端到端「下核心 → 起服 → RUNNING → Bot 进服」由 FR-043 在真实环境验收。

## 风险
- 下载源网络失败需明确错误（已：HTTP 状态码 + sha256 校验入错误信息）。
- Paper 配置文件路径随版本变化，需通过 FR-031 schema 渐进适配。
