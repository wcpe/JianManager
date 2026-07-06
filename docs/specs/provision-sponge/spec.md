# 功能规格：Sponge 子服支持

> 状态：实现中　·　关联 PRD：FR-046　·　关联 ADR：ADR-007、ADR-008

## 1. 背景与目标

现有建服向导和核心解析主要覆盖 Paper/Velocity/Waterfall/BungeeCord。FR-046 先支持 SpongeVanilla 后端子服，并为 SpongeForge 预留 core family 与 UI 文案；SpongeForge 只有在能确认稳定直链发行物且不需要通用 Forge 安装器时才进入本轮实现，否则延期为后续补充 FR。全部实现仍沿用群组服 M:N 建模、系统分配工作目录/端口和结构化启动。

## 2. 需求

- 建服向导核心类型新增 `spongevanilla`；`spongeforge` 作为待确认候选展示或灰置，不承诺本轮可创建。
- 按 MC 版本解析可用 Sponge 核心。
- 下载核心 jar 优先命中制品库，未命中时入库。
- 创建后仍使用系统分配工作目录、端口、JDK 和结构化启动。
- 创建 backend 时可注册到代理。

范围外：

- 通用 Forge 安装器管理；因此 SpongeForge 不能依赖 Forge 安装器流程作为本轮交付前提。
- modpack 安装。
- Sponge 插件市场。
- 对旧 Sponge 版本做无限兼容。

## 3. 设计

### 3.1 核心类型

新增 core family：

- `spongevanilla`
- `spongeforge`（预留 / 待确认；若发行物需要 Forge 安装器则不进入本轮创建能力）

实例仍是 `role=backend`，不新增实例角色。

### 3.2 API 草案

`GET /api/v1/cores` 返回新增类型：

```json
{ "type": "spongevanilla", "mcVersion": "1.20.1", "version": "x.y.z", "downloadUrl": "...", "sha256": "..." }
```

新增通用 provision 入口 `POST /api/v1/instances/provision/server` 承载 SpongeVanilla 等非 Bukkit 命名的后端创建；既有 `POST /api/v1/instances/provision/bukkit` 保留为 Paper/Bukkit 兼容入口。首版实现可让旧入口内部复用同一服务方法，但 API 文档和前端新增能力不得继续把 Sponge 称为 Bukkit。

### 3.3 启动

Worker 仍按 `jdk + jvm_args + core_jar + args` 派生启动命令，不允许用户手填自由命令覆盖结构化启动。

## 4. 任务拆分

- [x] 补 Sponge core resolver 和单测。
- [x] 新增通用 provision service/router 入口，保留旧 `/instances/provision/bukkit` 兼容。
- [x] 扩展前端向导选项与 i18n。
- [x] 补假后端 mock 和 DOM 测试。
- [ ] 真机创建 SpongeVanilla backend 并启动；若 SpongeForge 发行链已确认无需 Forge 安装器，可追加 SpongeForge 真机验收。
- [x] 文档同步：API、ARCHITECTURE。

## 5. 验收标准

- `/cores` 能列出 SpongeVanilla 候选；SpongeForge 未确认时必须以禁用/待确认状态展示，不提供可提交创建。
- 创建 Sponge backend 时系统分配端口/目录/JDK。
- 创建后可一键启动到 RUNNING。
- 代理注册关系不破坏 ADR-007。
- 亮/暗色和中/英文向导正常。

## 6. 风险 / 待定

- SpongeForge 发行物与 Forge 安装器关系需在实现前确认最小支持版本；未确认前不得把 SpongeForge 标为本轮已交付。
- 若官方源没有稳定 sha256，需要通过制品库入库时计算 sha256。
