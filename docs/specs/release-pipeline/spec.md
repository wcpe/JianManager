# 功能规格：CI/CD 发布管线（GitHub Actions）

> 状态：草拟　·　关联 PRD：FR-173　·　关联 ADR：ADR-036（本 FR 创建）　·　分支：feature/fr-173-release-pipeline

## 1. 背景与目标

仓库当前**无任何 CI**（无 `.github/`），发版靠手工 `make build`（且只产 Windows `.exe`）。面板/节点自更新（FR-081 已交付）消费 release 产物，但**没有任何东西产出 release 与产物**。本 FR 补齐 GitHub Actions 发布管线：普通 push 出滚动预发布、打 tag 出正式发布，交叉编译并打包上传产物到 GitHub Releases，为 FR-175（自更新对接 GitHub）提供可消费的制品与命名契约。P1。

## 2. 需求（要什么）

- **触发**：
  - push 到 `master` → **滚动预发布**：取 `CHANGELOG.md` 的 `[Unreleased]` 段为发布说明，发布/**覆盖**固定 tag `nightly` 的预发布（`prerelease=true`，旧资产替换为本次构建）。
  - push tag `v*`（如 `v0.11.0`）→ **正式发布**：取 `CHANGELOG.md` 中该版本段（`## 0.11.0（…）`）为发布说明，建正式 release（`prerelease=false`）。
- **交叉编译**：Control Plane + Worker，目标 `linux/amd64` 与 `windows/amd64`（共 4 个二进制），含前端嵌入（`gen-licenses` → `build-web` → `embed-web` → `go:embed`）。
- **产物**：4 个二进制 + `checksums.txt`（每件 sha256）上传到对应 release。命名遵循 ADR-036 契约。
- **版本注入**：`go build -ldflags "-X github.com/wcpe/JianManager/internal/version.Version=<v>"`；正式=tag 版本（去 `refs/tags/`），预发布=`0.0.0-dev+<shortsha>`。下载的二进制 `GetVersion`/启动日志报告正确版本。
- **内嵌全部可选资产（用户已定）**：发布的二进制内嵌探针 jar（`embed-probe`）+ CFR 反编译器（`embed-cfr`）+ 客户端更新器两件套（`embed-client-updater`），使下载即用——发布版 CP 自带探针（建服即部署 / 可推探针更新 FR-010/068）与客户端更新器（FR-107），发布版 Worker 自带 CFR（离线反编译 FR-075）。
- 范围内：workflow（含上述内嵌）+ CHANGELOG 段落提取脚本（含单测）+ 产物命名契约（ADR-036）+ version.go 默认值对齐（消除现 `0.9.1` vs CHANGELOG `0.10.0` 漂移，真值仍由 ldflags 注入）。
- 不做（范围外）：
  - 自更新对接 GitHub 的**代码**（FR-175，批 2）——本 FR 只产出 release 与命名契约。
  - `linux/arm64`、`darwin/*`（用户只要 linux/amd64 + windows/amd64）。
  - 二进制签名（sha256 完整性校验即本线范围；Ed25519 签名是客户端 OTA FR-087 范畴，两线隔离）。
  - bot-worker（Node）打包分发——自更新只覆盖 CP/Worker 二进制（FR-081 组件即 control-plane/worker），bot-worker 分发另议。
  - Docker 镜像发布（已有 `make docker`，不在本 FR）。

## 3. 设计（怎么做）

### 3.1 ADR-036（本 FR 创建，FR-175 共享）

确立「**发布管线 + 自更新对接 GitHub Releases**」的产物命名 / 校验 / 渠道契约（决策正文写 ADR，勿在 spec 重复）。要点：
- 产物命名：`control-plane-<os>-<arch>[.exe]`、`worker-<os>-<arch>[.exe]`（`<os>`=`runtime.GOOS`、`<arch>`=`runtime.GOARCH`；windows 带 `.exe`）。
- 校验：`checksums.txt`，每行 `<sha256(小写)>␠␠<filename>`，覆盖全部二进制。
- 渠道：正式 release tag `vX.Y.Z`（`prerelease=false`）；滚动预发布固定 tag `nightly`（`prerelease=true`，每次 push 替换资产）。
- 本 ADR 取代 ADR-020 §4「可配 feed 源」的**来源立场**——但 FR-173 仅**确立产物契约**；将 ADR-020 §4 标 `superseded-by ADR-036` 的动作由 FR-175（真正改自更新来源）落地，避免本 FR 未改自更新代码就翻旧决策。本 ADR 在 FR-173 内先立「产物/渠道契约」，FR-175 落地时补「自更新读 GitHub API」的来源决策。

### 3.2 workflow `.github/workflows/release.yml`

全程在 `ubuntu-latest` 交叉编译；`on: push: { branches: [master], tags: ['v*'] }`；`permissions: { contents: write }`；对 `nightly` 加 `concurrency` 组防并发覆盖打架。**内嵌资产平台无关**（探针/客户端更新器 jar 内嵌 CP、CFR 内嵌 Worker），故只构建一次、跨 matrix 复用——拆 `prepare-embeds` → `build`(matrix) → `release` 三 job：

- job `prepare-embeds`（构建/获取全部 go:embed 资产，一次性）：
  1. `actions/checkout`（`submodules: recursive`——`embed-probe` 需 `third_party/ServerProbe` 子模块）。
  2. 工具链：`actions/setup-go`（1.22+）、`actions/setup-node`（20）、`actions/setup-java`（**JDK21**，`embed-probe` 用）；`embed-client-updater` 需 **Java8**——优先靠 Gradle toolchain 自动解析 Java8（`client-updater` 已声明 toolchain），必要时 setup-java 多版本。
  3. 前端：`npm ci` 于 `web/`+`bot-worker/` → `node scripts/gen-licenses.mjs` → `cd web && npm run build` → 复制 `web/dist/*` 到 `internal/controlplane/embed/dist/`（等价 `make embed-web`，用 sh）。
  4. 内嵌资产（等价 Makefile 三 target，用 sh 直跑命令）：
     - `make embed-probe`：`third_party/ServerProbe` gradlew 构探针 jar → `internal/controlplane/embed/probe/ServerProbe.jar`。
     - `make embed-cfr`：curl CFR jar + `sha256sum -c` pin 校验 → `internal/worker/embed/cfr/cfr.jar`。
     - `make embed-client-updater`：`client-updater` gradlew 构 wedge+updater-core → `internal/controlplane/embed/client-updater/{wedge,updater-core}.jar`。
  5. 上传 `internal/controlplane/embed/`（dist+probe+client-updater）与 `internal/worker/embed/cfr/` 为 job artifact，供 build matrix 下载。
- job `build`（needs prepare-embeds；matrix `{os: linux, arch: amd64}` / `{os: windows, arch: amd64}`）：
  1. `actions/checkout`（`submodules: false`，本 job 不需子模块源，只需下载的 embed 产物）+ `setup-go`。
  2. 下载 prepare-embeds 的 embed artifact 还原到对应 `internal/**/embed/` 目录（go:embed 在 `go build` 时拉入）。
  3. `GOOS=<os> GOARCH=<arch> go build -ldflags "-X github.com/wcpe/JianManager/internal/version.Version=<v>" -o dist/<component>-<os>-<arch>[.exe] ./cmd/<component>`（CP 与 Worker 各一）。
  4. 上传 4 二进制为 job artifact 供 release job 汇总。
  - 注：`go:embed` 指令对**缺失目录**会编译失败；prepare-embeds 必须确保所有 embed 目录非空（探针/CFR/client-updater 内嵌均已选定，不存在「优雅缺省」分支）。
- job `release`（needs build）：
  1. 下载所有 job artifact，汇总到一个目录，生成 `checksums.txt`（`sha256sum * > checksums.txt`）。
  2. 解析触发类型 + 版本 + 说明：调 `node scripts/changelog-extract.mjs`（见 3.3）。
     - tag：`v=${GITHUB_REF#refs/tags/}`；notes=该版本段。
     - master：`v=0.0.0-dev+${GITHUB_SHA::7}`；notes=`[Unreleased]` 段；目标 tag=`nightly`。
  3. 发布：用 `softprops/action-gh-release`（或 `gh release`）：
     - tag 路线：`tag_name=${v}`，`prerelease: false`，`body_path` 指向提取的说明，`files: dist/*`。
     - master/nightly 路线：`tag_name: nightly`，`prerelease: true`，`body_path` 指向 `[Unreleased]` 说明，`files: dist/*`；**替换**既有 nightly 资产（action-gh-release 同 tag 会更新 release 并覆盖同名资产；若需先清空，用 `gh release delete-asset` 或重建 release——机制由实现选，**行为以验收为准：nightly 只保留本次构建产物**）。
- **版本注入一致性**：版本字符串在 build 与 release 两 job 间需一致计算；建议在 build job 即按触发类型算好 `v` 注入 ldflags，release job 复用同一算法（或经 job output 传递），避免二进制内版本与 release tag 不符。

### 3.3 CHANGELOG 段落提取 `scripts/changelog-extract.mjs`

- Node 脚本（与 `gen-licenses.mjs` 同栈）。参数：`--unreleased`（输出 `## [Unreleased]` 到下一个 `## ` 之间的正文）或 `--version 0.11.0`（输出 `## 0.11.0（…）` 段正文）。
- 输出纯 markdown 到 stdout（供 `body_path`）。空段或找不到版本 → 非零退出 + 明确报错（让 CI 失败而非发空说明）。
- 纯解析逻辑可单测：给定样例 CHANGELOG 文本，提取 `[Unreleased]` 与指定版本段正确、缺失版本报错。

### 3.4 version.go

- 默认值由 `0.9.1` 对齐到 `0.10.0`（当前最新 tag），消除漂移；注释保留「真值由 ldflags 注入」。

## 4. 任务拆分

- [ ] 写 `docs/adr/036-release-pipeline-github.md`（ADR-036：产物命名/checksums/渠道契约；注明 FR-175 共享、ADR-020 §4 取代留 FR-175 落地）
- [ ] `scripts/changelog-extract.mjs` + 单测（vitest 或 node:test；放可被 CI/本地跑的位置）
- [ ] `.github/workflows/release.yml`：`prepare-embeds`（前端 + 探针 + CFR + 客户端更新器内嵌，submodules recursive + JDK21/Java8 toolchain）→ `build` matrix（linux/amd64 + windows/amd64，下载 embed 产物后 go build）→ `release`（checksums + 预发布/正式发布 + 说明取自 CHANGELOG）
- [ ] ldflags 版本注入（build/release 版本一致）；`internal/version/version.go` 默认值对齐 0.10.0
- [ ] doc-sync：PRD §4 FR-173 状态「计划」→「开发中」（只改本行）；ARCHITECTURE「构建/发布」如有章节补一段；CHANGELOG `[Unreleased]` 末尾追加一行（只加不改）；ADR-036
- [ ] 中文 commit（`feat(ci): …` / `build(ci): …` 按 git-commit 规范）

## 5. 验收标准

- `.github/workflows/release.yml` 存在且语法有效（`actionlint` 通过，或 YAML 解析无误 + 人工核对 steps）。
- `changelog-extract` 单测绿：样例 CHANGELOG 提取 `[Unreleased]` 与某版本段正确、缺失版本非零退出。
- 本地交叉编译可行：`GOOS=linux GOARCH=amd64 go build ./cmd/control-plane` 与 windows/amd64、worker 同样可编译（agent 本地验证编译路径）。
- 下载/构建出的二进制启动日志或 `GetVersion` 报告注入的版本号。
- **【需真 CI，用户确认】** push `master` → 出/覆盖 `nightly` 预发布，含 4 二进制 + `checksums.txt`，说明=CHANGELOG `[Unreleased]`；`nightly` 仅保留本次产物。
- **【需真 CI，用户确认】** push tag `vX.Y.Z` → 出正式 release（非 prerelease），含 4 二进制 + `checksums.txt`，说明=该版本段。
- 注：GitHub Actions 真跑须推远程，agent 本地只能验 workflow 语法 + 脚本单测 + 交叉编译；真 CI 两条标「待真 CI 验」，由用户推后确认。

## 6. 风险 / 待定

- **真 CI 不可本地完全验证**：标「待真 CI」，落地前由用户在远程跑一次 push + 一次 tag 确认。
- **nightly 资产替换机制**：action-gh-release 同 tag 更新行为需实测；必要时 release job 先 `gh release delete nightly --yes`（容忍不存在）再重建。行为以验收「仅保留本次产物」为准。
- **build/release 版本一致性**：务必两 job 同算法，否则二进制内版本与 release tag 不符。
- **内嵌使 CI 变重变慢**（用户已接受）：需 `submodules: recursive` + JDK21（探针）+ Java8（客户端更新器，靠 gradle toolchain）+ Node + Go 多工具链；gradle 首次构建慢——必须开 gradle/go/npm 缓存，否则每次几分钟。`embed-cfr` 的 sha256 pin 必须与 `decompiler/cfr.go` 常量一致（pin 不符 CI 失败）。
- **go:embed 缺目录即编译失败**：prepare-embeds 任一内嵌步骤失败会导致 build job go build 失败（没有「优雅缺省」）；内嵌步骤须 fail-fast 且有清晰报错。
- **GITHUB_TOKEN 权限**：需 `contents: write` 发 release / 管 tag。
