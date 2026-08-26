# ADR-083: 基于 CAS 的制品版本库与 ServerProbe CP 分发

- **日期**: 2026-08-24
- **状态**: accepted
- **关联**: FR-409、ADR-011、ADR-014、ADR-016、ADR-036、ADR-037、ADR-059、ADR-074
- **部分取代**: ADR-014 的 ServerProbe 子模块 / 构建期内嵌 / CP 直传 jar 决策；ADR-016 决策 6 的制品来源与部署载荷；ADR-036 §5、ADR-074 决策 3 中的 ServerProbe 内嵌资产描述

## 上下文

现有制品库（ADR-011）已经以类型分区 CAS 管理不可变文件、来源、校验和与生命周期，但缺少「同一逻辑制品的多个来源、多个语义版本和默认版本」模型。ServerProbe 因而长期以 git 子模块、发布构建时 Gradle 构建、CP `go:embed` jar 和离线依赖缓存的特殊路径存在。这使探针版本被 JianManager 版本锁定，也让发布 CI 依赖另一仓库的构建环境。

用户要求把 ServerProbe 纳入制品管理：CP 集中访问外网来源并缓存可信版本；Worker 只向 CP 拉取；管理员可保留历史版本、手动设默认并在单实例级别升级或回滚。用户同时明确不下发运行库缓存，游戏服自行联网解析 ServerProbe 运行库依赖。

## 决策

### 1. 现有 CAS 是物理层，新增通用版本层

保留 ADR-011 的 `assets` 表与 `var/artifacts/<type>/<sha256[:2]>/<sha256><ext>` CAS 作为唯一物理字节存储。新增三类通用逻辑模型：

- `ArtifactPackage`：逻辑制品身份，例如首个 `serverprobe` 包；
- `ArtifactSource`：一个包的来源配置，带 provider 类型和启用状态；
- `ArtifactVersion`：来源发布的语义版本、Release 标识、资产名、预期 SHA-256 与已缓存 CAS `asset_id`。

一个 `ArtifactVersion` 可以尚未缓存，只有在 CP 已下载、校验并通过 `AssetService` 入 CAS 后才可作为部署目标。`assets` 的既有消费者不强制迁移；新版本层以可选方式逐步接入。ServerProbe 使用独立的 `server-probe` 资产类型，以免和用户上传的一般插件混淆。

### 2. 来源 provider 可扩展，首个可信来源固定为 GitHub Releases

版本库以最小 provider 接口列出版本和取得资产。当前只实现 `github-release` provider，并预置 `wcpe/ServerProbe` 作为默认来源；后续 provider 另行开发，不在本决策实现范围。

同步由管理员手动触发，只登记正式、非草稿、非预发布 Release 的版本元数据，不自动下载全部历史版本，也不自动切换全局默认。每个可部署版本必须唯一匹配 `ServerProbe-*.jar` Release asset，并具备 GitHub API 返回的 SHA-256 摘要；缺摘要、资产歧义或非法版本一律不可部署。

CP 下载时必须使用 ADR-037 的出站 HTTP 客户端与代理规则，逐字节计算 SHA-256 并与来源摘要比较，成功后才写入 CAS。来源 URL 与 Release 证据记录在版本和资产元数据中。

### 3. 版本选择采用可解释的三级继承

有效 ServerProbe 版本按以下顺序解析：

```text
全局默认版本 → Worker 默认版本 → 实例显式版本
```

Node 和 Instance 的版本引用允许为空，空值表示继承。管理员手动设置全局默认。修改 Worker 默认版本只改变后续创建实例的解析结果，绝不枚举、修改或下发该 Worker 上已有实例。已有实例要升级或回滚，必须由管理员在实例级别显式切换版本；切回继承也是一次显式操作。

被全局默认、Worker 或 Instance 引用的版本不可删除；来源和包也遵循相同的引用保护。

### 4. CP 是可信缓存和局域网分发端，Worker 主动拉取

CP 在部署前确保目标 `ArtifactVersion` 已映射到经校验的 CAS asset。CP 不经 gRPC 传输 jar 字节，而是经已有 gRPC 指令面提供版本、SHA-256 和 CP-local 下载 URL；Worker 主动请求 CP 的只读制品端点，下载到临时文件、校验 SHA-256 后原子替换实例的 `plugins/ServerProbe.jar`。

下载 URL 使用短期令牌，令牌 scope 记录 `component=server-probe`、Node UUID 和 ArtifactVersion，且不得出现在日志、审计 detail、错误响应或指标标签。直连 HTTP 下载以该短期令牌为 bearer capability，端点可校验签名、版本与失效时间；部署基址必须使用 TLS。该模式复用 ADR-059 的「CP 出站、Worker 从 CP-local 下载」原则，但不复用 Worker 二进制的版本锚定规则。

`DeployServerProbe` 在 proto 中加性扩展 URL、SHA-256 和版本字段，保留现有字节字段用于旧 Worker 兼容。新版 CP 不再发送 jar 字节或 `libraries_zip`；新版 Worker 优先处理下载字段。Worker 未能下载、校验或原子替换时必须保留旧 jar 并返回明确错误。

### 5. 不再分发探针依赖缓存，也不自动重启实例

CP 只缓存和下发 ServerProbe jar，废弃 `probe-libraries.zip`、相关构建脚本与 Worker 预置逻辑。游戏服首启自行解析 TabooLib 等运行库；在游戏服无网络或依赖来源不可达时，探针可能不能启用，这是用户接受的边界。

JVM 不能热替换已加载插件 jar。实例版本切换的成功语义是「jar 已就位，下次重启生效」，本决策不触发自动重启。

### 6. 发布版不再携带 ServerProbe 源码或二进制

移除 `third_party/ServerProbe` git 子模块、构建目标、Release CI 的 ServerProbe Gradle 步骤、CP `go:embed` 探针及依赖缓存。仅此项内嵌策略改变；前端、Bot Worker、客户端更新器、Worker 二进制与 CFR 的既有内嵌策略不受影响。

## 理由

- CAS 已有完整性、去重、来源和存储后端能力，新增逻辑版本层比平行缓存更小、更一致，也为后续受管制品复用提供路径。
- 外网依赖收敛到 CP，适配已有代理配置和内网 Worker；短期下载令牌配合 TLS 与不记日志策略限制 CP 分发面暴露。
- 版本引用继承让新实例默认跟随运营策略，同时禁止 Worker 默认变更意外批量改写正在运行的实例。
- 将 ServerProbe 从发布构建移出消除 CI 对外部 Gradle / 子模块可用性的耦合，并把探针发布节奏与 JianManager 版本解耦。

## 后果

- CP 数据模型、管理 API、制品库 UI、Node / Instance 选择界面和 CP → Worker 部署协议都需要加性扩展，并由 FR-409 的规格验收。
- 既有 `DeployServerProbe` 字节载荷与依赖缓存相关代码进入兼容过渡；发布路径和所有新版调用必须只走 Worker 拉取分支。
- 过去依赖离线缓存的 FR-114 断网首启保证不再适用于 ServerProbe；UI / 诊断必须如实说明运行库网络依赖。
- 真实验收必须证明 Worker 无公网但能访问 CP、CP 能访问 GitHub、游戏服能访问其依赖源时可完成部署和实例级回滚。

## 替代方案

- **继续内嵌子模块产物**：发布版自包含但探针版本与 JianManager 发布强耦合、CI 重且跨仓库构建脆弱，否决。
- **Worker 直连 GitHub Releases**：实现较少，但每台 Worker 都需要外网和代理配置，不能满足内网节点需求，否决。
- **为 ServerProbe 新建独立文件缓存**：重复 CAS 的校验、引用与存储能力，未来来源与版本管理会分叉，否决。
- **同步即下载全部版本、自动设最新为默认**：占用不必要存储且会产生静默版本变化，用户明确要求管理员手动选择，否决。
- **继续预置 TabooLib 依赖缓存**：可保离线首启，但仍引入构建 / 分发额外资产；用户明确选择只下发 jar，否决。

## 关系

- **ADR-011**：继续作为物理 CAS、校验和引用保护基础；本 ADR 新增其上的逻辑版本层。
- **ADR-014 / ADR-016**：保留 ServerProbe 作为监控与治理桥载体、本机回环和配置语义；仅替换制品来源与部署载荷。
- **ADR-036 / ADR-074**：保留发布版本、渠道和其他内嵌资产契约；移除 ServerProbe 内嵌资产。
- **ADR-037**：CP 从 GitHub 获取 jar 必须经统一出站代理客户端。
- **ADR-059**：复用 CP-local、短期 scope token、Worker 主动下载的安全模式。
