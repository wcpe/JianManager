# 功能规格：自更新对接 GitHub Releases（增强 FR-081）

> 状态：草拟　·　关联 PRD：FR-175（增强已交付 FR-081）　·　关联 ADR：ADR-036（扩写，取代 ADR-020 §4 feed 立场）　·　分支：feature/fr-175-self-update-github

## 1. 背景与目标

FR-081（✅ v0.9.0）已实现 CP/Worker 二进制自更新全链路，但更新源是「通用 `feed_url` JSON manifest」——需要有人手工产出并托管 feed.json。批 1 的 FR-173 已让 GitHub Actions 在 `github.com/wcpe/jianmanager` 产出正式 release / 滚动 `nightly` 预发布（产物 `<component>-<os>-<arch>[.exe]` + `checksums.txt`，见 ADR-036）。本 FR 让 CP 自更新**原生读 GitHub Releases API**（用户已选此方案，非 feed.json），把「发布」（FR-173）与「消费」（自更新）接上，运营者只需填仓库名即可在线升级。P1。

## 2. 需求（要什么）

- **新增配置**（CP `control-plane.yaml` 的 `update` 段，加性）：
  - `github_repo`：`owner/repo`，默认 `wcpe/jianmanager`。非空即启用 GitHub 源。
  - `channel`：`stable`（默认，取最新正式 release）/ `prerelease`（取滚动 `nightly` 预发布）。
  - `github_token`：可选，`${ENV}` 注入；填了则 GitHub API 带 `Authorization` 提升限流额度（未填走匿名 60 次/时，够手动用）。
- **解析 GitHub Releases API** 产出与现有 `Feed` 等价的归一结果：`tag_name`→version、`body`→notes、`assets[].browser_download_url`→各平台二进制 URL、sha256 取自 release 的 `checksums.txt` 资产（按 ADR-036 契约 `<sha256>␠␠<filename>` 解析）。资产名按 ADR-036 反解出 `component`/`os`/`arch`。
- **复用既有四路径**：检查更新 / CP 自升 / 单节点升 / 全网编排在 GitHub 源下全部可用，**逻辑不重写**——只把「拿到 `*Feed`」这一步从 feed_url 换成 GitHub 解析。
- **下载经 FR-174 代理**：CP 自升用已注入的 `outboundClient()`；Worker 升级仍由 CP 经 gRPC 下发 `download_url`+`sha256`，Worker 侧用自己的代理 client 下载（FR-174 已就位）。**sha256 强制校验**不变。
- **渠道语义**：`stable` 只取最新正式 release（GitHub `/releases/latest` 天然排除 prerelease/draft）；`prerelease` 取 `nightly` tag 的滚动预发布。
- **降级与错误**：GitHub API 限流（403/429 且 `X-RateLimit-Remaining: 0`）、无网、仓库/标签不存在、无匹配本平台资产、无 `checksums.txt` —— 均友好报错，沿用 FR-081 既有错误码体系（router 错误映射），不 5xx 误导。
- **向后兼容**：保留 `feed_url`/`binary_base_url` 作可选回退——`github_repo` 为空且 `feed_url` 非空时仍走原 feed 路径，不破坏 FR-081 既有行为与测试。
- 范围内：CP 配置 + GitHub 解析器 + 路由分发 + 错误映射 + ADR 扩写 + 测试。
- 不做（范围外）：
  - 自动定时检查 / 自动升级（仍仅手动触发，沿用 FR-081 边界）。
  - 任意历史版本回拉（仍 latest-only：stable=最新正式、prerelease=nightly）。
  - 二进制签名验证（sha256 完整性即范围；签名是客户端 OTA FR-087 线）。
  - 灰度/百分比放量（逐节点串行 + 失败隔离不变）。
  - 前端大改：`CheckResult` 仅**加性**新增 `source` 字段（如 `github:wcpe/jianmanager@stable` / `feed`），`SystemUpdatePage` 既有渲染不变、可选顺带展示来源；不重构页面。

## 3. 设计（怎么做）

### 3.1 配置（`internal/controlplane/config/config.go` + `SelfUpdateConfig`）
- `UpdateConfig` 加 `GitHubRepo string` (`github_repo`)、`Channel string` (`channel`)、`GitHubToken string` (`github_token`)；viper 默认：`update.github_repo`=`wcpe/jianmanager`、`update.channel`=`stable`、`update.github_token`=``。
- `service.SelfUpdateConfig` 同步加 `GitHubRepo`/`Channel`/`GitHubToken`；`cmd/control-plane/main.go` 装配处透传（已有 `SetHTTPClient(outboundClient)` 注入，不动）。
- `configs/control-plane.yaml` 的 `update:` 段补注释样例（默认填 `wcpe/jianmanager` + `stable`）。

### 3.2 GitHub 解析器（新文件 `internal/controlplane/service/selfupdate_github.go`）
- `type ghRelease struct { TagName string; Body string; Prerelease bool; Assets []ghAsset }`、`ghAsset{ Name, BrowserDownloadURL string }`（对齐 GitHub API JSON）。
- `func (s *SelfUpdateService) fetchGitHubRelease(ctx) (*Feed, error)`：
  1. 据 channel 选端点：`stable`→`GET https://api.github.com/repos/{repo}/releases/latest`；`prerelease`→`GET .../releases/tags/nightly`。
  2. 请求头：`Accept: application/vnd.github+json`、`X-GitHub-Api-Version: 2022-11-28`、有 token 加 `Authorization: Bearer <token>`。经 `outboundClient()`（代理）+ 15s 超时。
  3. 状态码映射：404→`ErrUpdateNoArtifact`（仓库/渠道无 release）；403/429 且限流头耗尽→`ErrUpdateRateLimited`（新增）；其它非 200→错误。
  4. 解析 release JSON。找 `checksums.txt` 资产→经 `outboundClient` 下载（小文件，限体积如 1MB）→解析成 `map[filename]sha256`。无 `checksums.txt`→错误（不允许无校验升级）。
  5. 遍历 assets，按 ADR-036 命名 `parseAssetName(name) (component, os, arch, ok)` 反解（`control-plane`/`worker` 前缀 + `-os-arch` + 可选 `.exe`）；命中则组 `FeedArtifact{Component,OS,Arch,URL:browser_download_url,SHA256:checksums[name]}`（缺 sha256 则跳过该资产并记日志，不静默放行无校验）。
  6. 组 `&Feed{Version: tagName, Notes: body, Artifacts: [...]}` 返回。
- `parseAssetName` 抽为纯函数，单测覆盖（`control-plane-linux-amd64`/`worker-windows-amd64.exe`/非法名）。
- `parseChecksums` 抽为纯函数（解析 `<sha>␠␠<file>` 多行，容忍空行/额外空格），单测。

### 3.3 路由分发（改 `selfupdate.go`）
- `Configured()` 改为：`github_repo` 非空 **或** `feed_url` 非空。
- 新增 `resolveRelease(ctx) (*Feed, error)`（或直接改造 `FetchFeed`）：
  - `feedFetcher` 测试桩优先（保留，现有测试不破）。
  - `github_repo` 非空→`fetchGitHubRelease`。
  - 否则 `feed_url` 非空→原 feed JSON 逻辑。
  - 都空→`ErrUpdateNotConfigured`。
- `CheckUpdate` / `resolveArtifact`（CP 自升 / 节点升 / rollout 共用）改调 `resolveRelease` 取 `*Feed`，**其余编排逻辑零改动**（`SelectArtifact`/版本比较/下载/替换/gRPC 下发/rollout 全不动）。
- `CheckResult` 加性新增 `Source string`（`github:owner/repo@channel` 或 `feed` 或空），`CheckUpdate` 填充。

### 3.4 错误映射（`router/selfupdate.go`）
- 新增 `ErrUpdateRateLimited`→ `429`（或 `503`）+ 错误码 `UPDATE_RATE_LIMITED`，沿用既有 error→HTTP 映射风格。其余复用 FR-081 既有码（`UPDATE_NOT_CONFIGURED`/`UPDATE_NO_ARTIFACT`/`UPDATE_CHECKSUM_MISMATCH`/`UPDATE_DOWNLOAD_FAILED`/`UPDATE_ALREADY_LATEST`）。

### 3.5 ADR
- **ADR-036**（FR-173 已建）：扩写「自更新对接 GitHub Releases」决策段——原生读 Releases API、stable/prerelease 渠道、checksums.txt 取 sha256、token 可选。
- **ADR-020 §4**：标 `superseded-by ADR-036`（feed 源立场被 GitHub 源取代；feed_url 降为可选回退）。旧 ADR 不删，加「已被取代」指引。

## 4. 任务拆分
- [ ] PRD §4 FR-175 状态「📋 计划」→「🔨 开发中」（只改本行）。
- [ ] ADR-036 扩写 + ADR-020 §4 标 superseded-by ADR-036。
- [ ] 配置：`UpdateConfig` 加 `github_repo`/`channel`/`github_token` + 默认 + `SelfUpdateConfig` 透传 + `cmd/control-plane/main.go` 装配 + `configs/control-plane.yaml` 样例。
- [ ] 测试先行：`parseAssetName`/`parseChecksums` 纯函数单测；`fetchGitHubRelease` 经 httptest 模拟 GitHub API（latest/tags 两端点 + checksums 资产）→ Feed 正确；渠道选择；限流/404/无资产/无 checksums 错误；`Configured()` 与回退 feed_url 逻辑。
- [ ] 实现：`selfupdate_github.go` + `selfupdate.go` 分发改造 + `CheckResult.Source` + 路由错误码。
- [ ] 验证：`go build`/`vet`/`test ./...` 全绿；既有 FR-081 selfupdate 测试不破。
- [ ] doc-sync：`docs/API.md`（self-update 端点的源/渠道说明 + 新错误码）、`docs/ARCHITECTURE.md`（自更新来源描述）、`CHANGELOG.md` `[Unreleased]` 追加、ADR。
- [ ] 中文 commit（`feat(control-plane)`/`docs`，按 git-commit 规范，按层拆 commit）。

## 5. 验收标准
- `github_repo` 配置后，`GET /self-update/check` 经 GitHub API 返回 `latestVersion`=最新 release tag、`notes`=release body、CP/各节点 `updateAvailable`/`artifactAvailable` 正确，`source` 标 `github:...@channel`。
- `channel=stable` 取最新正式 release（排除 prerelease）；`channel=prerelease` 取 `nightly`。
- CP 自升 / 单节点升 / 全网编排在 GitHub 源下解析出正确 asset URL + sha256（来自 checksums.txt），sha256 不符拒绝替换（复用 FR-081 校验）。
- 限流/无网/404/无匹配资产/无 checksums → 对应错误码与友好提示，不 5xx。
- `github_repo` 空 + `feed_url` 非空 → 仍走原 feed 路径（FR-081 行为不破，既有测试绿）。
- 单测：`parseAssetName`/`parseChecksums`/`fetchGitHubRelease`(httptest)/渠道/错误/回退 全覆盖；`go build`/`vet`/`test ./...` 全绿。
- **【需真机，用户确认，依赖 FR-173 真 CI 先出 release】**：CP 与一个 Worker 从 `wcpe/jianmanager` 真实 release 在线升级到新版本并重启，daemon 下游戏服不掉，Windows 替换运行中 exe 成功（裸跑二进制 re-exec）。本地以 httptest 覆盖解析与编排，真机标「待真机验」。

## 6. 风险 / 待定
- **GitHub API 限流**（匿名 60/时）：手动触发足够；token 可选提升。检查更新会打 1~2 次 API（release + checksums），不密集。
- **asset 重定向下载**：`browser_download_url` 302 跳 `objects.githubusercontent.com`，`http.Client` 默认跟随重定向且经代理——确认 FR-174 client 跟随重定向（标准库默认跟随）。
- **真机依赖 FR-173**：真实升级验证要等 FR-173 真 CI 产出 release；未出前用 httptest + 本地起 mock 验证解析/编排。
- **checksums.txt 必须存在**：FR-173 workflow 保证产出；若某 release 缺它，本 FR 视为「无可信源」报错而非裸下载（安全底线）。
- **channel=prerelease 依赖 `nightly` tag**：与 FR-173 的滚动预发布 tag 名（`nightly`）强耦合——两者同属 ADR-036 契约，保持一致。
