# ADR-036: 发布管线与 GitHub Releases 产物/校验/渠道契约

- **日期**: 2026-06-27
- **状态**: accepted
- **上下文**: 仓库当前**无任何 CI**（无 `.github/`），发版靠手工 `make build`（且产物只到 Windows `.exe`）。面板/节点自更新（FR-081 已交付，见 `docs/specs/self-update/api.md`）的设计前提是「从某处下载带 sha256 的二进制制品并替换」，但**没有任何东西产出可供消费的 release 与制品**——自更新现阶段只能靠手工搭 feed / 内网 URL 兜底（ADR-020 §4 立的「可配 feed 源」）。要让发布与自更新形成闭环，必须先有「**自动产出 release 制品**」的发布管线，并确立一套**产物命名 / 完整性校验 / 发布渠道**契约，让产出侧（CI）与消费侧（自更新 FR-175）对齐同一心智。本 ADR 由 FR-173（CI/CD 发布管线）创建，落地 GitHub Actions 发布管线并固化该契约；契约本身由 FR-173 与 FR-175 共享。

## 决策

确立「**GitHub Actions 发布管线 + GitHub Releases 制品契约**」：普通 push 出滚动预发布、打 tag 出正式发布，交叉编译 Control Plane + Worker 并按固定命名打包上传到 GitHub Releases，附 sha256 校验清单。产物命名 / 校验 / 渠道三项契约固化如下，供 FR-175（自更新对接 GitHub Releases）直接消费。

### 1. 产物命名契约

- 二进制命名：`<component>-<os>-<arch>[.exe]`。
  - `<component>` ∈ `control-plane` | `worker`（与 FR-081 自更新组件取值、`docs/specs/self-update/api.md` 的 `component` 字段一致）。
  - `<os>` = `runtime.GOOS` 取值（`linux` | `windows`）；`<arch>` = `runtime.GOARCH` 取值（本线 `amd64`）。
  - `windows` 目标带 `.exe` 后缀，其余无后缀。
- 本线目标矩阵：`linux/amd64`、`windows/amd64`，组件 `control-plane`、`worker`——共 **4 个二进制**。
- 该命名与 `docs/specs/self-update/api.md` 的「无 feed 时按 `<base>/<component>-<os>-<arch>` 约定拼下载地址」「按目标组件 + 目标节点的 os/arch 精确匹配选制品」完全对齐，使自更新可仅凭命名规则定位制品。

### 2. 完整性校验契约

- 每个 release 附 `checksums.txt`，覆盖该 release 的**全部二进制**。
- 行格式：`<sha256(小写十六进制)>␠␠<filename>`（两个空格分隔，等价 `sha256sum` 默认输出），`<filename>` 为不含目录的纯文件名（即 §1 的命名）。
- 完整性以 **sha256** 为准（不信传输通道、只信内容指纹），与制品库 ADR-011、自更新 ADR-020 §4 的 sha256 校验心智同源。
- **不做二进制签名**：sha256 完整性校验即本线范围。Ed25519 签名是面向**玩家客户端 OTA**（FR-087 / ADR-022）的范畴，与面向**运营者宿主**的节点/面板分发是物理隔离的两套分发面，互不混用。

### 3. 发布渠道契约

- **正式发布**：push tag `vX.Y.Z`（如 `v0.11.0`）触发，建正式 release（`prerelease=false`），`tag_name=vX.Y.Z`，发布说明取 `CHANGELOG.md` 中该版本段（`## X.Y.Z（…）` 到下一个 `## ` 之间的正文）。
- **滚动预发布**：push 到 `master` 触发，发布/**覆盖**固定 tag `nightly` 的预发布（`prerelease=true`），发布说明取 `CHANGELOG.md` 的 `[Unreleased]` 段。`nightly` **只保留本次构建产物**（同 tag 重发时替换既有同名资产，旧资产不残留）。
- 渠道语义：正式 = 稳定（stable）；`nightly` = 预发布（prerelease）。FR-175 自更新「stable / prerelease 渠道」即据 GitHub Releases 的 `prerelease` 标志与 tag 区分二者。

### 4. 版本注入契约

- 二进制内版本由 `go build -ldflags "-X github.com/wcpe/JianManager/internal/version.Version=<v>"` 注入（`internal/version/version.go` 的默认值仅作未注入兜底，已对齐到最新已发版本消除漂移）。
- `<v>` 取值：
  - 正式发布（tag）：`<v>` = 去前缀的 tag（`${GITHUB_REF#refs/tags/}`，即 `vX.Y.Z`）。
  - 滚动预发布（master）：`<v>` = `0.0.0-dev+<shortsha>`（7 位短 SHA），标识非正式开发构建。
- **build 与 release 两 job 必须按同一算法计算 `<v>`**，确保「二进制内 `GetVersion` 报告的版本」与「release tag」一致，不出现两者错配。

### 5. 内嵌资产立场（FR-173 落地细则）

- 发布的二进制**内嵌全部可选资产**（用户已定）：CP 内嵌探针 jar（`embed-probe`）+ 客户端更新器两件套（`embed-client-updater`）+ 前端（`embed-web`）；Worker 内嵌 CFR 反编译器（`embed-cfr`）。使「下载即用」——发布版 CP 自带探针（建服即部署 / 推探针更新 FR-010/068）与客户端更新器（FR-107），发布版 Worker 自带 CFR（离线反编译 FR-075）。
- `go:embed` 对**缺失/空目录**会编译失败（无「优雅缺省」分支）。故发布管线的 `prepare-embeds` job 必须产出所有 embed 目录的真实内容；任一内嵌步骤失败即 fail-fast，不产出「声称内嵌实则没有」的二进制。
- `embed-cfr` 的 sha256 pin 必须与 `internal/worker/decompiler/cfr.go` 的 `cfrSHA256` 常量一致（pin 不符 CI 失败）。

### 6. 与 ADR-020 §4 的关系（取代立场，FR-175 已落地）

- 本 ADR 取代 ADR-020 §4「可配 feed 源（release feed / 私有 URL）」的**分发来源立场**：发布制品的权威来源收敛为 **GitHub Releases**，自更新改为读 GitHub Releases API 解析制品（按 §1 命名 + §2 checksums 校验 + §3 渠道区分）。
- **FR-173 仅确立产物 / 渠道契约，不改自更新代码**。将 ADR-020 §4 标 `superseded-by ADR-036` 的动作、以及「自更新读 GitHub Releases API + stable/prerelease 渠道 + 经 FR-174 出站代理下载」的来源决策，由 **FR-175** 落地（见下 §7）。本 ADR 先立「产物 / 渠道契约」、再由 FR-175 补「消费侧来源决策」，避免 FR-173 未动自更新代码就翻旧决策、造成文档与实现脱节。

### 7. 自更新对接 GitHub Releases（FR-175 落地的消费侧决策）

本 §由 **FR-175** 追加，确立自更新（FR-081 已交付链路）**原生消费 GitHub Releases API** 的来源决策，落地 §6 承诺的「来源切换 + ADR-020 §4 取代」。

- **原生读 GitHub Releases API，不再要求手工 feed.json**：CP 自更新「拿到 `*Feed`」这一步从「读 `feed_url` 指向的 JSON manifest」改为「调 GitHub Releases API + 解析 release JSON + 下载 `checksums.txt` 资产组装等价的归一 `Feed`」。FR-081 既有编排（`SelectArtifact` / 版本比对 / 下载校验替换 / gRPC 下发 / rollout）**零改动**——只换「取 `Feed`」的数据源。
- **渠道 ↔ GitHub 端点映射**（落地 §3 渠道契约的消费侧）：
  - `stable`（默认）→ `GET https://api.github.com/repos/{repo}/releases/latest`（GitHub 该端点**天然排除 prerelease/draft**，即只返回最新正式 release）。
  - `prerelease` → `GET https://api.github.com/repos/{repo}/releases/tags/nightly`（取 §3 的滚动 `nightly` 预发布，tag 名与 FR-173 workflow 强耦合，同属本 ADR 契约）。
- **归一映射**：`tag_name`→`Feed.Version`、`body`→`Feed.Notes`、`assets[].browser_download_url`→各制品 URL；sha256 **取自 release 的 `checksums.txt` 资产**（按 §2 行格式 `<sha256(小写)>␠␠<filename>` 解析为 `map[filename]sha256`）。资产名按 §1 命名 `<component>-<os>-<arch>[.exe]` 反解出 `component`/`os`/`arch`。
- **无 `checksums.txt` 即视为「无可信源」报错，绝不裸下载**：sha256 是唯一完整性根（§2），release 缺 `checksums.txt`（或某资产无对应 sha256 条目）则该资产不可信、不放行（缺 sha256 的资产跳过并记日志，整 release 无 `checksums.txt` 直接报错）。这是安全底线，FR-173 workflow 保证产出 `checksums.txt`。
- **token 可选**：GitHub API 匿名限流 60 次/时，手动触发自更新（每次检查打 1~2 次 API：release + checksums）足够；配置 `github_token` 后请求带 `Authorization: Bearer`，提升限流额度。请求统一带 `Accept: application/vnd.github+json` + `X-GitHub-Api-Version: 2022-11-28`。
- **经 FR-174 出站代理下载**：API 调用与 `checksums.txt` / 二进制下载均走 FR-174 注入的进程级出站 client（`SelfUpdateService.outboundClient()`）；`browser_download_url` 的 302 重定向到 `objects.githubusercontent.com` 由标准库 `http.Client` 默认跟随、同样经代理。Worker 升级仍由 CP 经 gRPC 下发 `download_url`+`sha256`，Worker 侧用自己的代理 client 下载（FR-174 已就位）。
- **限流降级**：GitHub API 返回 `403`/`429` 视为限流（新增错误 `ErrUpdateRateLimited` → HTTP `429` + 错误码 `UPDATE_RATE_LIMITED`）；`404` 映射既有 `ErrUpdateNoArtifact`（仓库/渠道无 release）。沿用 FR-081 既有错误码体系（router 错误映射），不 5xx 误导。
- **向后兼容（feed 降为可选回退）**：`feed_url`/`binary_base_url` 保留为可选回退——`github_repo` 为空且 `feed_url` 非空时仍走原 feed JSON 路径（FR-081 既有行为与测试不破）。`Configured()` 判据放宽为「`github_repo` 非空 **或** `feed_url` 非空」。来源经新增 `CheckResult.Source`（`github:owner/repo@channel` 或 `feed`）透出，前端可选展示、不强制改造。
- **配置（加性）**：`control-plane.yaml` 的 `update` 段加 `github_repo`（默认 `wcpe/jianmanager`，非空即启用 GitHub 源）、`channel`（`stable` 默认 / `prerelease`）、`github_token`（默认空，`${ENV}` 注入）。
- **不做**：自动定时检查 / 自动升级（仍仅手动，沿用 FR-081 边界）、任意历史版本回拉（仍 latest-only：stable=最新正式、prerelease=nightly）、二进制签名（sha256 完整性即范围；签名属客户端 OTA FR-087 线）。

至此 **ADR-020 §4 的 feed 源立场正式被本 ADR 取代**（ADR-020 §4 已标 `superseded-by ADR-036`），分发与自更新来源统一收敛到 GitHub Releases，feed 仅作可选回退。

## 理由

- **契约先行、产出与消费解耦**：把命名 / 校验 / 渠道固化为契约，发布侧（CI）与消费侧（自更新）各自实现而对齐同一规则，避免两线各搞一套割裂的分发心智。
- **GitHub Releases 作权威来源**：仓库已在 GitHub，Releases 原生承载 tag、预发布标志、资产与说明，无需自建分发端点即可闭环；自更新读其公开 API 即可发现制品。
- **sha256 而非签名**：节点/面板分发面向运营者宿主，sha256 完整性足以防传输损坏 / 中间篡改的「内容不一致」；强加密签名属面向不可信玩家客户端的 OTA 范畴，两线隔离不混。
- **滚动 `nightly` 固定 tag**：让「main 上的最新可用构建」始终有一个稳定可寻址的预发布入口（`nightly`），便于试用与自更新 prerelease 渠道，且只保留最新产物不堆积。
- **版本注入两 job 同算法**：杜绝「二进制自报版本」与「release tag」不符——否则自更新比对版本会误判、运维定位混乱。
- **取代立场与落地分离**：契约（本 ADR）与来源切换（FR-175）解耦，使 FR-173 是纯增量（加 CI + 契约），不触碰已交付的自更新行为，降低回归面。

## 后果

- 新增 `.github/workflows/release.yml`（三 job：`prepare-embeds` → `build` matrix → `release`）。CI 因内嵌全部资产而变重（需 `submodules: recursive` + JDK21[探针] + Java8[客户端更新器，靠 Gradle toolchain] + Node + Go 多工具链 + Gradle/Go/npm 缓存），用户已接受。
- 新增 `scripts/changelog-extract.mjs`（提取 `[Unreleased]` / 指定版本段为发布说明，缺失非零退出让 CI 失败而非发空说明）。
- `internal/version/version.go` 默认值对齐最新已发版本（消除 `0.9.1` vs CHANGELOG `0.10.0` 漂移），真值仍由 ldflags 注入。
- FR-175 落地时须：将 ADR-020 §4 标 `superseded-by ADR-036`、改自更新读 GitHub Releases API、按本 ADR §1/§2/§3 消费制品。
- 真 CI 行为（push master 出/覆盖 `nightly`、push tag 出正式 release）须推远程后由用户实跑确认；本地只能验 workflow 语法 + 脚本单测 + 交叉编译。

## 替代方案

- **自建公网 release feed 端点 + 私有 URL（ADR-020 §4 原立场）**：要自架并运维分发端点、自管制品存储与 manifest，重复造 GitHub Releases 已提供的能力；放弃（收敛到 GitHub Releases，私有/内网兜底仍可由 FR-174 代理 + 后续 FR 另议）。
- **Ed25519 / GPG 签名所有发布二进制**：安全性更高，但要求管理签名密钥生命周期、消费侧内置公钥校验，对面向运营者宿主的分发过重，且与客户端 OTA 的签名体系混淆两条隔离分发线；放弃（sha256 完整性即够，签名留客户端 OTA FR-087）。
- **每次 push 都打唯一 tag（如按 commit）出预发布**：tag/release 会无限堆积、难清理，且无稳定「最新预发布」入口；放弃（用固定 `nightly` tag 滚动覆盖，只留最新）。
- **不内嵌可选资产、发瘦二进制 + 运行时按需下载**：CI 更轻更快，但「下载即用」体验受损（建服无探针、离线无 CFR、接入页无更新器 jar），与用户「发布版下载即用」诉求相悖；放弃（用户已定内嵌全部，接受 CI 变重）。
- **用 GoReleaser 等成品发布工具**：能省样板，但本项目发布需深度耦合多工具链内嵌（探针 Gradle / CFR pin 下载 / 客户端更新器 Gradle / 前端构建），且要自定义 nightly 覆盖与 CHANGELOG 段落说明，定制成本未必低于直接写 workflow；放弃（先用原生 Actions + Makefile target，保留后续引入成品工具的空间）。

## 关系

- **ADR-011（制品库）**：发布制品的 sha256 完整性校验思路与之同源（内容寻址 + 校验和）。
- **ADR-020（节点 enrollment 与部署）**：本 ADR 取代其 §4「可配 feed 源」的分发来源立场（来源收敛为 GitHub Releases）；取代标记由 FR-175 落地。其安装脚本 `--download-url` / `enroll.binary_url` 后续可指向 GitHub Releases 制品（按 §1 命名）。
- **ADR-022（客户端 manifest 信任与公网端点）**：面向玩家客户端 OTA 的 Ed25519 签名 + 拉取密钥分发面，与本 ADR 的节点/面板 sha256 分发面**物理隔离**，互不复用。
- **FR-081（面板自更新）**：其消费的制品命名 / sha256 校验由本 ADR §1/§2 固化；当前 feed/URL 来源（ADR-020 §4）由 FR-175 切换为 GitHub Releases。
- **FR-173（CI/CD 发布管线）**：本 ADR 的落地 FR——产出 release 与制品、固化契约。
- **FR-174（出站网络代理）**：FR-175 经 GitHub Releases API 下载制品时走 FR-174 的出站代理。
- **FR-175（自更新对接 GitHub Releases）**：本 ADR 契约的消费方，**已落地**——见 §7「自更新对接 GitHub Releases」决策段；FR-175 执行了 ADR-020 §4 的 `superseded-by ADR-036` 标记，并把自更新来源切换为 GitHub Releases API（stable/prerelease 渠道、checksums 取 sha256、token 可选、经 FR-174 代理）。
