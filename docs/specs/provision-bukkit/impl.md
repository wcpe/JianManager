# 实施计划 — FR-034 搭建 Bukkit 子服

> 关联 FR: FR-034 | 优先级: P1 | 状态: 🔨 in-progress（后端 + 前端向导已完成，待真机验收）

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

### Phase 4: 前端向导（已完成）
- [x] 核心类型/版本选择（消费 `GET /cores`）：`api/provision.ts`（`useCoreVersions`/`useResolvedCore`/`useProvisionBukkit`）+ `components/ProvisionServerDialog.tsx`
- [x] 选版本后实时预览「将下载哪个构建 + 文件名」（`useResolvedCore`，build 留空取最新）
- [x] JDK（按节点）/ 内存 / JVM 参数 / 可选用户组 / 实例名选择；端口与工作目录由系统分配（向导内明示，无输入项）
- [x] 入口按钮挂载实例页（`pages/InstancesPage.tsx`「⚡ 一键搭建」）
- [x] 部分失败（实例已建但下核心/写配置未完成，端点返回 502+instance）前端降级为 warning 提示并关闭
- [x] i18n `provision.*`（zh/en）
- [ ] 代理注册可选项（待 FR-035/032 注册关系，超出本 FR 范围）
- 创建成功后失效 `instances` 列表（实例即刻出现，STOPPED 可一键启动）；未做自动跳转详情（保持与既有创建实例一致）

## 后续优化 / 待办
- **核心走制品库**：当前 Worker 直接 HTTP 下载核心；应改为 CP `IngestFromURL` 入制品库（FR-045，去重/校验/模板源）后再交付 Worker（单机可让 Worker 从数据根复制，远程走上传），避免重复下载。
- **前端按角色显隐入口**：当前前端无客户端角色信息（auth store 仅存 token），入口按钮与既有「创建实例」一致全量显示，由后端平台管理员中间件兜底（403）。如需可视化按钮级 gating，应另开横切 FR（`/me` 下推角色 + auth store 存角色 + 统一 gating 所有管理员入口）。
- **shadcn 回调类型在 TS 6.0.3 下推断为 any**：`radix-ui` 伞包类型经 `React.ComponentProps<typeof X.Root>` 未能把 `onValueChange/onOpenChange` 带出，已在调用点显式标注 `string`/`boolean` 解锁编译；根因（依赖/工具链类型解析）应另行排查，避免新增调用点反复踩坑。
- **真机验收**：端到端「下核心 → 起服 → RUNNING → Bot 进服」由 FR-043 在真实环境验收。`service/core_live_test.go`（`-tags e2e`）已联网冒烟验证 PaperMC 列版本/解析构建/下载地址可达。

## 风险
- 下载源网络失败需明确错误（已：HTTP 状态码 + sha256 校验入错误信息）。
- Paper 配置文件路径随版本变化，需通过 FR-031 schema 渐进适配。
