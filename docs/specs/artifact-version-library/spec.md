# 功能规格：制品版本库与 ServerProbe 在线分发

> 状态：已交付（待真机验收）　·　关联 PRD：FR-409　·　关联 ADR：ADR-083（取代 ADR-014 / ADR-016 / ADR-036 中的 ServerProbe 内嵌决策）

## 1. 背景与目标

现有制品库以 `assets` 表和内容寻址存储（CAS）管理不可变二进制，已有来源、校验、存储渠道和生命周期能力；但它没有「逻辑制品、多来源、多版本、默认版本和版本绑定」的管理模型。ServerProbe 目前又以 git 子模块、构建期 jar 与依赖缓存的方式嵌入 Control Plane，导致版本固定在 JianManager 的发布版本上。

FR-409 在既有 CAS 之上增加通用制品版本层，先接入 ServerProbe。Control Plane 负责从可信来源下载、校验并缓存 jar；Worker 仅从 Control Plane 拉取目标 jar。管理员可以保留多个版本、手动设全局默认、给 Worker 设新实例默认版本，或在单实例上手动升级 / 回滚。

优先级为 P1。该功能不新增进程，也不修改 ServerProbe 仓库。

## 2. 需求（要什么）

### 2.1 通用制品版本库

- 保留现有 `assets` 作为物理 CAS 层，所有既有制品消费者保持行为与数据兼容。
- 新增通用逻辑层：
  - **制品包**：可版本化的逻辑产品，例如首个包 `serverprobe`。
  - **制品来源**：一个包可配置多个来源，来源类型由 provider 扩展点支持。
  - **制品版本**：语义版本、来源发布标识、来源资产名、预期 SHA-256、关联 CAS asset 与缓存状态。
- 首个 provider 为 GitHub Releases，默认来源为 `wcpe/ServerProbe`；后续来源类型可增，不在本 FR 实现。
- 管理员手动同步来源。同步只把新版本加入版本库，不自动修改全局默认版本。
- 版本首次被设为默认或部署使用前，CP 必须下载 jar、用来源提供的 SHA-256 校验并写入 CAS；未成功缓存的版本不可作为部署目标。
- 被全局默认、Worker 或实例绑定的版本不可删除；来源、包同样受下游引用保护。

### 2.2 ServerProbe 版本选择

- 有效版本按「全局默认 → Worker 默认 → 实例显式版本」解析；后两项为空时继承上一级。
- 管理员手动设全局默认版本；新 Worker 默认继承它。
- Worker 默认版本变化**只影响之后新建的实例**，不得向既有实例下发 jar。
- 单实例可以显式选择任意已缓存的 ServerProbe 版本；这项手动操作用于升级与回滚，并立即触发该实例部署。
- 实例可切回继承版本；切回也是一次显式部署操作。运行中的 JVM 不做热替换、不自动重启，提示「已就位，下次重启生效」。

### 2.3 CP 缓存与 Worker 拉取

- CP 从 GitHub Releases 读取正式 Release 与匹配的 `ServerProbe-*.jar` 资产。没有 SHA-256 摘要、资产不唯一、版本或摘要非法时拒绝入库。
- CP 下载必须复用既有出站 HTTP 客户端与代理配置；成功后经现有 `AssetService` 以新 `server-probe` 类型存入 CAS。
- CP 只缓存和下发 jar，不再生成或传输 `probe-libraries.zip`；游戏服首次启动按 ServerProbe / TabooLib 原有行为自行联网解析依赖。
- CP 通过已有 CP-local 制品下发模式签发短期下载 URL；令牌 scope 记录目标 Node UUID 与版本，令牌不记录在日志、审计或错误响应中。该直连 HTTP URL 是短期 bearer capability，传输须走 TLS。
- Worker 收到版本切换指令后，主动从 CP 下载 jar，校验 SHA-256、写入临时文件并原子替换实例 `plugins/ServerProbe.jar`，再写入探针配置。下载、校验或替换失败必须返回明确错误，原 jar 不可损坏。

### 2.4 管理面

- 制品库提供制品包、来源和版本管理入口：查看来源、手动同步、查看版本与缓存 / 校验状态、缓存指定版本、设全局默认、删除未引用项。
- Worker 详情提供默认版本下拉，显示「继承全局」或具体版本；保存不触发既有实例更新。
- 实例详情提供版本下拉，显示解析后的继承来源；实例手动保存时触发 Worker 拉取。新建实例按 Worker 默认版本解析。
- 仅平台管理员可管理来源、版本和全局默认；Worker / 实例选择沿用现有节点与实例授权边界。

### 2.5 范围外

- 不改动或发布 ServerProbe 仓库，不新增其 Release 工作流。
- 不实现非 GitHub 的来源 provider、私有来源凭证管理、自动定时同步、自动切换全局默认、灰度推送或批量升级。
- 不迁移既有 `core`、`plugin`、`client-file` 等资产消费者到版本库。
- 不下发 TabooLib 依赖缓存，不承诺游戏服断网首启探针可用。

## 3. 设计（怎么做）

### 3.1 数据模型与 CAS 关系

`assets` 继续是内容寻址的物理文件索引，只加 `server-probe` 资产类型。新增三个通用模型：

| 模型 | 核心字段 | 说明 |
|---|---|---|
| `ArtifactPackage` | `key`、`name`、`asset_type` | 逻辑制品；`serverprobe` 是第一个种子包。 |
| `ArtifactSource` | `package_id`、`provider`、`name`、`config`、`enabled` | 来源配置。当前 provider 仅 `github-release`，配置包含 `owner/repo`。 |
| `ArtifactVersion` | `package_id`、`source_id`、`version`、`release_ref`、`asset_name`、`expected_sha256`、`asset_id`、`cached_at` | 一条外部发布版本；未缓存时 `asset_id` 为空。 |

`ArtifactVersion.asset_id` 指向不可变 CAS `assets` 记录。相同字节仍由 CAS 去重；版本记录保留各自来源和语义版本，不能用文件名或 hash 反推版本。

全局默认保存 `serverprobe` 包的 `ArtifactVersion` 引用；Node 和 Instance 各增加可空 `ProbeVersionID`。解析函数必须返回「实际版本 + 继承层级」，供创建、部署和 UI 共用。

### 3.2 来源同步与入库

定义最小 provider 接口：列出可用版本及其 jar 资产元数据，并按版本取得字节流。`github-release` 实现调用 GitHub Releases API，只接受非 draft、非 prerelease 的 Release，选择唯一 `ServerProbe-*.jar` 资产并读取其 `sha256:` 摘要。

同步仅 upsert `ArtifactVersion` 元数据，不下载全部历史 jar。管理员显式缓存版本，或将版本设为全局默认 / 实例部署时，CP 通过 `AssetService` 单飞下载、完整读取并校验摘要后入库。下载失败不创建可用 asset 引用，并保留可诊断错误。

### 3.3 部署协议与安全

保留现有 `DeployServerProbe` 的字节载荷字段以兼容已注册 Worker；为 Worker 主动拉取新增可选的下载 URL、SHA-256 和版本字段。新版 CP 一律走 URL 分支，且不传 jar / `libraries_zip`；新版 Worker 优先处理 URL 分支。

下载端点复用 FR-190 的 CP-local 分发原则，但令牌 scope 明确记录 `component=server-probe`、目标 Node UUID、ArtifactVersion ID 与短 TTL。直连 HTTP 端点只能校验短期 bearer token 的签名、版本与失效时间，因此必须走 TLS；令牌只作为 URL 查询参数传输，路由访问日志、审计和错误响应必须脱敏。Worker 下载到实例目录的临时文件，校验摘要后再原子替换；任何失败都保留旧 jar。

创建实例时先解析有效版本、确保 CP 缓存，再下发拉取指令。实例手动改版本同样走这条路径。Worker 默认改动仅更新绑定记录，不枚举或更新旧实例。

### 3.4 前端与 API

管理 API 增加制品包 / 来源 / 版本查询、GitHub 来源同步、指定版本缓存、全局默认切换和受引用保护的删除语义。节点与实例 API 加性返回并修改其版本绑定和解析结果；实例切换成功响应包含目标版本及「下次重启生效」。

前端在既有制品库入口展示版本管理，在节点详情显示 Worker 默认下拉，在实例详情显示实例版本下拉。下拉必须明确「继承全局」「继承 Worker」和解析后的具体版本，避免把默认切换误解为对旧实例的批量更新；新建实例不提供覆盖项，直接使用 Worker 默认解析结果。

### 3.5 内嵌与子模块清理

移除 `third_party/ServerProbe` git 子模块及 `.gitmodules` 条目，删除探针 Gradle 构建、`make embed-probe` / Taskfile 调用、CP `go:embed` jar / 依赖缓存和发布流水线的探针构建步骤。Provision 与在线更新改用版本库解析和 Worker 拉取，不再依赖内嵌探针是否存在。

本项只清理 ServerProbe 相关内嵌资产；前端、Bot Worker、客户端更新器和 Worker 二进制的既有内嵌策略不改变。

## 4. 任务拆分

- [x] 新增制品包 / 来源 / 版本模型、迁移、CAS `server-probe` 类型和 GitHub Releases provider。
- [x] 实现来源同步、按需缓存、摘要校验、引用保护和全局 / Worker / 实例有效版本解析。
- [x] 新增管理 API、CP-local 下载端点和节点绑定短期令牌。
- [x] 扩展 CP → Worker 探针部署协议；Worker 实现下载、校验、原子替换与明确错误。
- [x] 改造实例创建和实例手动探针更新；取消依赖缓存下发。
- [x] 实现制品库、Worker、实例版本选择界面与 i18n；新建实例按 Worker 默认版本解析。
- [x] 清理 ServerProbe 子模块、内嵌资产、构建目标与 Release CI 步骤。
- [x] 补 ADR、ARCHITECTURE、API、CHANGELOG 与 PRD 状态。

## 5. 验收标准

- 默认 GitHub 来源可手动同步 `wcpe/ServerProbe` 的正式 Release；同一版本重复同步不重复创建版本记录。
- CP 缓存 jar 时必须比对 GitHub 摘要；摘要不符、资产不唯一、缓存损坏均拒绝部署或重新下载，旧 CAS 文件不被误用。
- 未缓存版本不能被设为全局默认或作为实例部署目标；缓存成功后可被选择。
- 全局 → Worker → 实例的继承解析正确；Worker 默认切换不改变任何既有实例绑定或 jar；新实例取得切换后的 Worker 默认。
- 实例手动选择旧版本后，Worker 只从 CP 拉取该版本，完成 SHA-256 校验和原子替换；下载失败时旧 jar 保持完整。
- 下载端点拒绝过期、跨版本或非 ServerProbe 的令牌；令牌 scope 记录目标 Node UUID，且日志、审计和错误响应不含令牌明文。
- 新版部署请求不携带探针 jar 或依赖缓存；Worker 无法访问 GitHub 但可访问 CP 时仍能取得 CP 已缓存的 jar。
- 原有非版本化资产与相关 API / 测试保持兼容；最终发布构建不再要求 ServerProbe 子模块、Gradle 或内嵌探针目录。
- **需真机验收**：CP 可访问 GitHub、Worker 仅可访问 CP、游戏服可访问依赖源的网络拓扑下，创建实例成功拉取 `ServerProbe v0.2.0` 并在下次重启连接探针；单实例选择旧版本后同样成功回滚。该验收不能由单元或浏览器测试替代。

## 6. 风险 / 待定

- 用户已选择不下发依赖缓存，游戏服离线或无法访问 TabooLib 依赖源时，探针首启会失败；平台需显示明确诊断，不得伪称离线可用。
- GitHub Release API 未提供摘要的历史版本不进入可部署状态，避免无校验二进制。
- Worker 必须能访问 CP 的下载基址；CP-local URL 的解析与代理部署沿用 FR-190 已有基址规则。
- 其他来源 provider、自动同步和批量升级明确留给后续 FR，当前模型只保留扩展边界。
