# 功能规格：Worker 二进制 CP 代理缓存下发

> 状态：开发中（代码闭环完成，真机内网验收待补）　·　关联 PRD：FR-190　·　关联 ADR：ADR-059

## 1. 背景与目标

节点安装和升级当前依赖目标机器能访问公网 release 源，且可能拿到与 CP 当前版本不一致的 Worker 二进制。FR-190 要让 CP 负责按自身版本取得、校验、缓存 Worker 二进制，再通过局域网下发给安装脚本和 UpgradeNode。

## 2. 需求

- CP 能按 `version.Version + os + arch` 解析 Worker release 资产。
- CP 下载资产时使用当前出站代理配置，并用 release `checksums.txt` 校验 sha256。
- 缓存命中时不重复下载；缓存损坏时拒绝下发并重新拉取。
- 安装脚本默认从 CP 下载 Worker 二进制，保留 `--binary` 本地兜底。
- `UpgradeNode` 下发 CP-local URL 和 sha256，Worker 不需要访问公网 release 源。
- 管理面能看到目标平台资产是否已缓存、大小、sha256、缓存时间和最近错误。

范围外：

- 多版本 Worker 仓库。
- 手动上传任意版本 Worker。
- P2P/CDN 分发。
- 让 Worker 自行选择 release channel。

## 3. 设计

### 3.1 缓存键

缓存键为：

```text
component=worker
version=<cp version>
os=<target os>
arch=<target arch>
```

物理位置：

```text
<dataRoot>/cache/worker-assets/<version>/<os>-<arch>/worker[.exe]
<dataRoot>/cache/worker-assets/<version>/<os>-<arch>/metadata.json
```

`metadata.json` 至少包含：`version`、`os`、`arch`、`sha256`、`size`、`sourceUrl`、`cachedAt`。

### 3.2 服务接口

已新增服务方法：

- `EnsureWorkerAsset(ctx, os, arch string) (*WorkerAssetCacheEntry, error)`
- `WorkerAssetStatus(ctx, os, arch string) (*WorkerAssetCacheEntry, error)`
- `ListWorkerAssets() ([]WorkerAssetCacheEntry, error)`
- `OpenWorkerAsset(version, os, arch string) (*os.File, *WorkerAssetCacheEntry, error)`
- `IssueWorkerAssetToken(scope WorkerAssetTokenScope) (string, error)`
- `ValidateWorkerAssetToken(token string, expected WorkerAssetTokenScope) (*WorkerAssetTokenScope, error)`

### 3.3 HTTP 契约草案

管理端点：

- `GET /api/v1/self-update/worker-assets`
  - 权限：平台管理员
  - 响应：`[{ version, os, arch, cached, sha256, size, cachedAt, lastError }]`

- `POST /api/v1/self-update/worker-assets/cache`
  - 权限：平台管理员
  - 请求：`{ "os": "linux", "arch": "amd64" }`
  - 响应：缓存元数据

下载端点：

- `GET /worker-assets/:version/:os/:arch/worker`
  - 权限：短期下载 token；安装脚本 token 与升级 token 分开签发。
  - token 承载：由于安装脚本和现有 `UpgradeWorkerRequest.download_url` 都只能稳定传 URL，本 FR 使用 `?token=<opaque>` query token；所有日志、审计和错误响应必须脱敏或不记录 token 明文。
  - token scope：升级 token 绑定 `version/os/arch/purpose/nodeUuid`；安装命令为跨平台脚本，安装 token 绑定 `version/purpose`，`os/arch` 可用通配符并由脚本运行时替换 URL 模板。
  - TTL：默认 10 分钟；过期或 scope 不匹配返回 `403 INVALID_WORKER_ASSET_TOKEN`。
  - 响应：Worker 二进制。
  - 错误：`404 WORKER_ASSET_NOT_CACHED`、`403 INVALID_WORKER_ASSET_TOKEN`。

### 3.4 安装脚本

安装脚本输入保持兼容：

- 默认：从 CP-local worker asset URL 下载；一键命令携带 `{os}/{arch}` 模板，脚本按运行时平台替换后请求。
- `--binary <path>`：继续使用本地二进制。
- `--download-url <url>`：保留显式覆盖，但不作为一键命令默认值。

### 3.5 UpgradeNode

`UpgradeNode` 流程：

1. 读取目标节点 OS/Arch。
2. 调 `EnsureWorkerAsset`。
3. 签发 `purpose=upgrade`、绑定目标节点 UUID 的短期下载 token。
4. 调 Worker `UpgradeWorker`，传 CP-local URL、sha256、targetVersion。

安装脚本生成一键命令时签发 `purpose=install` 的短期 token；安装 URL 模板里的 `{os}/{arch}` 由脚本运行时替换，脚本 token 不可用于节点升级，升级 token 不可用于安装。

## 4. 任务拆分

- [x] 新增 Worker 资产缓存服务和单测。
- [x] 新增管理状态/缓存端点、下载端点、短期 token 签发/校验单测，并跳过 `/worker-assets/` Gin 访问日志避免 query token 落日志；`purpose=upgrade` token 签发必须绑定 `nodeUuid`。
- [x] 改造安装脚本生成的一键命令。
- [x] 改造 `UpgradeNode` 使用 CP-local URL。
- [x] 前端系统更新页展示缓存状态与预缓存按钮，按目标 CP 版本 + os/arch 匹配缓存，避免同平台旧版本误显示为已缓存；MSW/DOM 覆盖。
- [x] 文档同步：ARCHITECTURE、API、ADR、本 spec。

## 5. 验收标准

- 单测覆盖缓存命中、缓存损坏重拉、token scope 不匹配。
- 集成测试覆盖 `UpgradeNode` 下发 CP-local URL。
- 单机断言：模拟 Worker 侧无法访问公网，仍能从 CP 下载二进制。
- 真实浏览器截图：系统更新页显示 Worker 资产缓存状态和节点升级路径。
- 真机：内网 Worker 使用 CP-local 资产安装并完成注册；升级同理。

## 6. 风险 / 待定

- 是否允许管理端主动预热所有平台资产需审核。
- 若构建版本为开发版且无 release 资产，需定义本地 dev 兜底策略。
- 真机内网安装与升级验收仍需在 release 资产可用环境补齐。
