# 功能规格：FR-046 Sponge 子服支持

> 状态：开发中 · 关联 PRD：FR-046 · 分支：当前 FR-046 验收分支

## 1. 背景与目标

FR-046 属于 MC 群组服 V2 的后端子服扩展：在既有 FR-034 向导式 Bukkit/Paper 建服能力上，补齐 Sponge 后端子服的可选入口、核心解析、部署与验收证据链。

本轮目标是让平台管理员可以通过同一套一键建服流程创建 SpongeVanilla 与 SpongeForge 两类后端子服，并保持实例模型对外兼容：实例 `type` 仍为 `minecraft_java`，`role` 仍为 `backend`，具体运行时变体通过建服请求中的 `coreType` 与启动/部署结构区分。

## 2. 需求（要什么）

- 范围内：
  - 支持 `coreType=spongevanilla`：从 Sponge 官方下载源解析版本与构建，下载 SpongeVanilla universal jar 到实例工作目录，并以 `server.jar` 结构化启动。
  - 支持 `coreType=spongeforge`：从 Sponge 官方下载源解析 SpongeForge mod jar，同时通过 Forge 官方 installer 初始化对应 Forge 服务端，再把 SpongeForge jar 写入 `mods/` 目录，最终以 Forge 服务端启动入口运行。
  - 继续复用 `POST /api/v1/instances/provision/bukkit` 与 `GET /api/v1/cores`；不新增 `sponge` 实例类型字符串。
  - 一键建服仍自动分配工作目录、server/query/probe 端口、绑定 JDK、写入 `eula.txt`、`server.properties`，并部署 ServerProbe（可用时）。
  - 前端一键建服对话框提供 Paper、SpongeVanilla、SpongeForge 三个后端核心选项，按所选核心查询版本并展示即将下载/安装的产物。
  - Mock 数据、单测、集测、单机断言与真实浏览器验收必须都能覆盖 SpongeVanilla 与 SpongeForge。
- 范围外：
  - 不新增实例类型 `sponge`，不改变现有 `minecraft_java` 序列化语义。
  - 不在本 FR 中实现 Sponge 插件/Mod 管理、Forge 模组市场、代理自动注册增强或 Sponge 专属配置编辑器。
  - 不要求把 SpongeForge 的 Forge 版本选择暴露为单独高级向导；后端可按 SpongeForge 官方产物名/版本解析得到兼容 Forge 坐标，解析失败时返回明确错误。

## 3. 设计（怎么做）

### 3.1 核心类型与模型

- `CoreService` 新增 Sponge 家族识别：`spongevanilla` 与 `spongeforge`。
- `CoreInfo.Type` 返回规范化后的核心类型（`spongevanilla` / `spongeforge`），但创建出的实例仍写入：
  - `type=minecraft_java`
  - `role=backend`
  - `processType=daemon`
- 启动差异放在 `LaunchSpec.CoreJar` 与部署步骤里表达：
  - SpongeVanilla：`CoreJar=server.jar`。
  - SpongeForge：`CoreJar` 指向 Forge 服务端启动 jar 或后端现有启动结构可表达的稳定入口；SpongeForge 本体作为 `mods/SpongeForge.jar` 部署。

### 3.2 下载源

- SpongeVanilla/SpongeForge 版本列表与下载链接以 Sponge 官方下载页/仓库为准：
  - `https://dl.spongepowered.org/spongevanilla`
  - `https://dl.spongepowered.org/spongeforge`
  - 官方 Maven 产物链接：`https://repo.spongepowered.org/repository/maven-releases/org/spongepowered/...`
- Forge 服务端初始化以 Forge 官方 Maven installer 为准：
  - `https://maven.minecraftforge.net/net/minecraftforge/forge/...`
- Sponge 下载页对非浏览器 User-Agent 可能返回 403；后端请求 Sponge/Forge 元数据时必须设置稳定 User-Agent，并复用现有出站代理/超时机制。

### 3.3 Control Plane 流程

- `GET /api/v1/cores?type=spongevanilla`：返回可用 Minecraft 版本，新→旧。
- `GET /api/v1/cores?type=spongevanilla&mcVersion=...&build=...`：返回 SpongeVanilla 下载信息。
- `GET /api/v1/cores?type=spongeforge` 与 resolve 分支同理，但 `CoreInfo` 需要能表达 SpongeForge mod 与 Forge installer/入口的安装信息；如现有 `CoreInfo` 不足，新增向后兼容字段，不删除既有字段。
- `POST /api/v1/instances/provision/bukkit` 接受 `coreType=paper|spongevanilla|spongeforge`；根据核心类型选择部署策略。
- 创建实例失败不落库；实例已创建但下载/安装/配置失败时沿用现有部分失败语义，返回实例供重试/删除。

### 3.4 Worker 部署

- 复用 `DownloadCore` 下载单 jar 产物；如 SpongeForge 需要多文件/子目录写入，优先新增小而明确的 Worker RPC 或在现有 Worker 服务内抽取可测试的部署方法，不把路径穿越能力暴露给通用下载接口。
- 所有写入路径必须限制在实例工作目录内，禁止通过请求参数写任意路径。
- Forge installer 执行必须使用实例绑定的 JDK/Java，限定工作目录、超时和日志摘要；失败时返回中文错误并保留已创建实例。

### 3.5 前端

- `ProvisionServerDialog` 的核心类型从固定 Paper 改成可选：Paper、SpongeVanilla、SpongeForge。
- 选择核心类型后重置版本/build 预览，重新查询 `/cores`。
- 文案从“Paper 子服”调整为“后端子服”，但不改变现有 Paper 行为。
- Mock handler 增加 SpongeVanilla/SpongeForge 版本和创建返回，便于 DOM 测试与真实浏览器 mock 验收。

## 4. 任务拆分

- [ ] 后端核心解析：补 SpongeVanilla/SpongeForge 类型识别、版本列表、构建解析、User-Agent、单测 fixture。
- [ ] 后端建服策略：抽象 Paper/SpongeVanilla/SpongeForge 部署路径，保持实例 `minecraft_java/backend` 不变。
- [ ] Worker 安装能力：补 SpongeForge 所需 Forge installer 与 `mods/` 写入能力，并覆盖路径安全测试。
- [ ] 前端向导：核心类型选择、版本联动、下载预览、i18n/mock/DOM 测试。
- [ ] 集成与 E2E：新增 FR-046 后端集测、真实进程 E2E 或可控假源验证，覆盖两个 Sponge 变体。
- [ ] 验收证据：生成 `.tmp/fr046-sponge-acceptance/` 下的单测、集测、单机断言截图、真实浏览器断言截图和汇总报告。
- [ ] 文档同步：PRD 状态、API、CHANGELOG；如实现新增 Worker RPC 或 CoreInfo 字段，同步 `docs/API.md`。

## 5. 验收标准

- 单测：`CoreService` 对 `spongevanilla` 与 `spongeforge` 能从 fixture 列版本、选最新构建、选指定构建、报告缺失版本/构建；Paper/Velocity/Waterfall/Bungee 行为不回退。
- 集测：通过本地 httptest/假 Worker 完成 SpongeVanilla 与 SpongeForge 建服请求，断言实例为 `type=minecraft_java`、`role=backend`、端口与 JDK 写入正确、部署步骤调用正确。
- 单机断言：在本机可控环境中跑 FR-046 验收脚本，检查两个变体的工作目录、`eula.txt`、`server.properties`、核心 jar 或 `mods/SpongeForge.jar`、启动结构；输出日志与截图到 `.tmp/fr046-sponge-acceptance/`。
- 真实浏览器断言：通过真实浏览器打开前端，验证一键建服对话框能选择 SpongeVanilla 与 SpongeForge，版本列表/下载预览/提交 payload 正确；分别保存截图。
- 联网冒烟（可选 e2e tag）：在网络可用时访问官方 Sponge/Forge 源，验证至少一个 SpongeVanilla 与一个 SpongeForge 下载链接可解析且 HEAD/GET 可达；网络失败不得阻塞普通单测。
- 文档：`docs/specs/provision-sponge/spec.md`、`api.md` 存在且与实现一致；PRD FR-046 只有在全部证据通过后才可标记交付。

## 6. 风险 / 待定

- SpongeForge 是 Forge mod，不是独立服务端 jar；“完整可启动”依赖 Forge installer 初始化，耗时与网络失败率高于 SpongeVanilla。
- Sponge 官方下载页可能调整 HTML 结构；实现应把解析逻辑封装并用 fixture 锁定，联网测试只做冒烟。
- Forge installer 可能下载额外依赖；真实启动验收需要可用网络、足够磁盘和兼容 JDK。
- 如果现有启动模型不能稳定表达新版 Forge 的启动参数文件，需要在不破坏旧实例的前提下扩展 `LaunchSpec`。
