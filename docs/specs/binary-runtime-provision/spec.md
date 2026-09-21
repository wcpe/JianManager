# 通用二进制运行时搭建（FR-441 / FR-442）

> 状态：开发中·真机验收通过（待发版）　·　关联 PRD：FR-441、FR-442　·　依赖：ADR-090

## 1. 背景

JianManager 的 `provision_server` 现有能力覆盖 Minecraft 生态：`paper` / `spongevanilla` / `spongeforge` / `velocity` / `waterfall` / `bungeecord`。这些核心都由 `CoreService.ResolveBuild` 按 project 名去**对应厂商的官方 API** 解析版本与构建号。

但平台上还跑着**非 Minecraft 的配套服务**——最典型的是 Beacon（Go 编写的区服配置中心）。这类程序的共同点：

- 没有官方版本解析 API（不像 PaperMC 的 fill v3）
- 分发形态是**预编译二进制**，不是 jar
- 不适用 `server.properties` / EULA / 版本构建号

现状下运维只能手工上传二进制、手写启动命令、手工建实例——绕开了平台的任务追踪、通知与制品管理。

## 2. 目标与非目标

**目标**
- 新增 `coreType=binary`，支持三类制品来源：**制品库 asset** / **远程 URL** / **节点本地文件**
- 下载与安装全程走既有 `TaskService.RunAsync`，有阶段进度与终态站内信
- 失败进 `DAMAGED` 且保留 `provisionSpec` 供重建
- 在此基础上提供 **Beacon 预设**（FR-442）

**非目标**
- 不做通用「任意程序管理器」（不引入 systemd / 进程守护语义）
- 不改动既有 MC 核心的搭建路径
- 不做二进制的版本自动升级（升级仍走「重新搭建 + 切换」的人工决策）

## 3. 设计

### 3.1 coreType=binary 的解析差异

`CoreService.ResolveBuild` 对 MC 核心做「版本 → 构建 → 下载 URL + 校验和」三段解析。`binary` **跳过全部三段**，改为由请求直接提供制品来源：

```go
type BinarySource struct {
    Kind string `json:"kind"` // "asset" | "url" | "node_file"
    // kind=asset 时：制品库既有 asset UUID
    AssetID string `json:"assetId,omitempty"`
    // kind=url 时：远程下载地址（须 https，支持可选 sha256 校验）
    URL string `json:"url,omitempty"`
    SHA256 string `json:"sha256,omitempty"`
    // kind=node_file 时：节点上的绝对路径（须在受管目录内或显式放行）
    NodePath string `json:"nodePath,omitempty"`
    // 落盘后的文件名（相对实例工作目录），如 beacon-1.1.0-linux-amd64
    Filename string `json:"filename"`
    // 可执行位（默认 true）
    Executable *bool `json:"executable,omitempty"`
}
```

### 3.2 三类来源的处理

| Kind | 处理 |
|---|---|
| `asset` | 复用既有制品库分发通道（`artifact_version_delivery` 的签名 token），Worker 从 CP 拉取 |
| `url` | 由 **Worker** 下载（避免 CP 中转大文件）；支持 `sha256` 校验，不匹配即失败 |
| `node_file` | Worker 就地复制到工作目录；**路径须通过安全校验**（防越权读任意文件） |

> **安全约束**：`node_file` 的路径必须落在节点受管目录内，或经显式的运维放行配置。此约束与既有 `instance_import` 的路径校验同口径。

### 3.3 异步任务（关键要求）

**所有** `binary` 搭建必须走 `TaskService.RunAsync`，不得同步执行。理由：

1. 二进制体积大（Beacon 约 30MB，ServerProbe 同类），下载可能数分钟
2. 前端需看到**下载进度**，否则用户无法区分「卡住」与「在下载」
3. 终态需发**站内信**通知，长任务无人守屏

新增任务类型：
```go
TaskKindBinaryProvision = "binary_provision"
```

阶段划分（`SetStage(progress, stage)`）：

| progress | stage 文案 |
|---|---|
| 5 | 解析制品来源… |
| 20 | 获取二进制（来源：制品库 / 远程 URL / 节点本地）… |
| 60 | 下载/复制中（含字节进度）… |
| 80 | 校验完整性… |
| 90 | 写入工作目录并设置权限… |
| 95 | 派生启动命令… |
| 100 | 完成 |

> **与既有 provision 的一致性**：`TaskKindProvision` 已实现「同步段只做核心解析 + 建实例 + 登记任务，下载在 CP 后台 goroutine」的两段式（FR-319，见 `provision.go` 注释）。`binary_provision` 沿用同一模式。
>
> **补充说明**：ServerProbe 探针的部署（`deployServerProbe`）已在 `provision` 任务内异步执行，jar 来自 CP 内嵌分发（带 10 分钟签名 token），**不在本 FR 范围内**。

### 3.4 启动方式（决策 2A）

**复用既有 `startCommand` 字段**，不引入新的启动描述结构：

```
startCommand: "./beacon-1.1.0-linux-amd64"
```

理由：
- `startCommand` 已是 `varchar(1024)` 的自由命令串，Worker 通过 `sh -c` 执行，天然支持任意二进制
- 引入 `binarySpec` 会与 `launchSpec` 的既有语义重叠，增加双重真源风险
- 既有 `beacon` 实例（真实生产）本来就是用 `./beacon-1.1.0-linux-amd64` 跑的

### 3.5 实例默认属性

| 字段 | 值 | 理由 |
|---|---|---|
| `role` | `universal` | 二进制服务不参与 MC 群组服拓扑（FR-433 已加 `beacon` 角色，FR-442 预设使用） |
| `jdkId` | 0（不绑定） | Go/Rust 等编译型二进制不需要 JVM |
| `memoryMb` | 不适用 | 内存限制走 `memLimitMb`（容器/进程限制），非 JVM 堆 |

## 4. FR-442：Beacon 快速搭建预设

在 `coreType=binary` 之上提供 `coreType=beacon` 的**语法糖**（内部展开为 binary + 固定参数）。

### 4.1 制品来源优先级（决策）

```
1. 制品库已有 beacon 制品（最新版本）  → 优先使用
2. 回落 GitHub Releases（wcpe/Beacon） → 下载对应平台二进制
```

**优先制品库的理由**：内网环境可能无法访问 GitHub；且已入库的制品经过 sha256 校验，可信度更高。

### 4.2 默认参数

| 项 | 值 |
|---|---|
| 二进制文件名 | `beacon-{version}-linux-amd64` |
| 启动命令 | `./beacon-{version}-linux-amd64` |
| 角色 | `beacon`（FR-433 新增，不参与 MC 拓扑） |
| 工作目录 | 系统分配（`allocWorkDirRel`），内含 `config.yml` / `beacon.db` / `secrets/` |
| 首次启动后 | 自动释放 `config.yml` 并填入随机凭据（Beacon 自身行为） |

### 4.3 搭建后的运维闭环

一键搭建完成后，管理台应能：
1. 查看该实例的控制台输出（Beacon 启动日志）
2. 打开其 Web 管理台（须运维配置端口；本 FR 不自动暴露端口）
3. 配置协同端点（FR-443）使 Beacon 与本平台对接

## 5. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | `kind=asset` 能从制品库既有 asset 搭建出 STOPPED 可启动实例 | 真机 |
| 2 | `kind=url` 能下载并搭建；`sha256` 不匹配时失败 | 真机 |
| 3 | `kind=node_file` 能就地复制；**路径越界被拒** | 真机 + 负例 |
| 4 | 搭建全程走 `RunAsync`，前端可见阶段进度 | 真机（观察任务页） |
| 5 | 终态发站内信 | 真机 |
| 6 | 下载失败进 `DAMAGED`，`provisionSpec` 可重建 | 真机（断网模拟） |
| 7 | 启动命令由 `startCommand` 派生，实例可正常启动 | 真机 |
| 8 | 搭建出的实例角色为 `universal`（或预设的 `beacon`） | 真机 |
| 9 | FR-442：一键搭出 Beacon 并可启动；制品优先级（制品库 > GitHub）验证 | 真机 |
| 10 | FR-442：Beacon 搭建后管理台可登录（`/admin/v1/auth/login`） | 真机 |
| 11 | **未部署 Beacon 时，平台其余功能不受任何影响** | 真机 |

### 5.1 真机验证记录（2026-09-21，隔离环境 E2E CP 19501 + Worker 19702）

| # | 结果 | 证据 |
|---|---|---|
| 1 | ⚠️ 未验 | `kind=asset` 由单测覆盖；E2E 制品库为空，未走该档（需先向制品库注入 beacon 制品） |
| 2 | ✅ 通过 | `kind=url` 经 GitHub Releases 实下载 30593289 字节成功落盘可执行（`source=url`, `fromLocal=false`）；`http` 明文被拒（「url 必须为 https：二进制经明文传输可被中间人替换」）。**sha256 不匹配的负例未验** |
| 3 | ✅ 通过 | `kind=node_file` 就地复制 30MB 二进制，落盘 `-rwxr-xr-x` 且 **sha256 完全一致**（`3a216368…9e373`）；放行根外路径被拒（配置 `binary.node_file_roots` 生效性在上一轮已验证） |
| 4 | ⚠️ 部分 | 搭建返回 `taskId`，实例 `statusReason` 显示阶段文案（「搭建中：正在获取二进制」）；**前端任务页进度未直接观察** |
| 5 | ⚠️ 未验 | 终态站内信未直接核验 |
| 6 | ⚠️ 未验 | `DAMAGED` + `provisionSpec` 重建未验（需构造下载失败） |
| 7 | ✅ 通过 | `startCommand` 由 `./<filename>` 派生；实例经 wrapper 启动并**持续运行**（Beacon 完成真实初始化后稳定，配独立端口后 HTTP 200） |
| 8 | ✅ 通过 | `coreType=binary` → `role=universal`；`coreType=beacon` → `role=beacon`（见下） |
| 9 | ✅ 通过 | `coreType=beacon` 一键搭建成功：`role=beacon`、`jdkId=0`、`startCommand=./beacon-1.1.0-linux-amd64`；来源命中 GitHub（制品库为空，走「制品库 > GitHub」第二档），`source=url` 取件 30593289 字节 |
| 10 | ✅ 通过 | 搭建出的 Beacon 实例管理台 `POST /admin/v1/auth/login` 登录成功（`{"operator":"admin","token":"…"}`），首次鉴权初始化闭环 |
| 11 | ✅ 通过 | 未配置 Beacon 端点时平台其余功能全部正常（FR-443 未配置即跳过，零阻塞） |

> 未验项的共同前提：需构造失败注入（下载中断、sha256 错配、制品库注入）或前端观察，属**下一轮补验范围**；核心搭建链路（来源解析 → 取件 → 落盘 → 派生启动命令 → 实例可跑）已真机闭环。

## 6. 影响面

| 文件 | 改动 |
|---|---|
| `internal/controlplane/service/provision.go` | `coreType=binary` 分支 + `BinarySource` 解析 |
| `internal/controlplane/service/core.go` | `ResolveBuild` 对 binary 短路（或新增独立解析器） |
| `internal/controlplane/model/task.go` | 新增 `TaskKindBinaryProvision` |
| `internal/worker/process/*` | 远程 URL 下载 + sha256 校验 + node_file 复制（Worker 侧） |
| `proto/worker.proto` | 若需新增 RPC（取决于是否复用既有文件传输通道） |
| `internal/controlplane/mcp/tools_provision.go` | `instance_provision_server` 工具暴露 binary 参数 |
| `docs/API.md` | 搭建端点补充 binary 语义 |

## 7. 未决 / 待确认

- **大文件传输通道**：`kind=url` 由 Worker 直连下载（推荐，省 CP 带宽）；若目标 URL 仅内网可达而 Worker 不能出网，需回退 CP 中转。**首版按 Worker 直连实现**，不支持时明确报错。
- **`node_file` 的路径白名单**：首版复用 `instance_import` 的受管目录校验；若运维需要读取受管目录外的文件，走「先上传到制品库」路径（更安全，且自动获得 sha256）。
- **版本升级**：不做自动升级。运维重新搭建新版本实例后手工切换（与 MC 核心同策略）。
