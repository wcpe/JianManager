# ADR-064: 多语言 monorepo 统一布局与命令面（apps/packages/internal/third_party + go-task）

- **日期**: 2026-07-12
- **状态**: proposed（草稿；FR-283 开工前定稿为 accepted）
- **取代关系**: 无取代。**重申并细化 ADR-005**（go:embed 单二进制 + 开发模式反代不变、embed 目标目录不变），仅更新「前端源码目录」这一结构前提的措辞；不改 ADR-001/002/003/006 等任何进程模型与通信边界。
- **关联**: FR-283~288（本 ADR 落地）、ADR-005（前端 go:embed 单二进制）、ADR-036（发布管线 + 产物命名契约）、ADR-047（devmock 仅 dev/test 形态、不入嵌入产物）、ADR-058（`@jianmanager/ui` 组件包与组件博物馆）、ADR-065（`version.go` 版本唯一真值——FR-288 把前端接上它）。

## 上下文

当前是**多语言仓库**（Go / TypeScript-React / Node.js / Kotlin-Gradle / Protobuf），但组织方式是「path-alias 伪 monorepo」，几处不统一造成认知与运维摩擦：

- **前端非真工作区**：`web/packages/ui`（`@jianmanager/ui`）靠 `web/vite.config.ts` 的 `resolve.alias` 解析，不是 workspace 依赖；`web/wiki` 靠 `npm --prefix wiki` 启动；`web/package.json` 无 `workspaces` 字段。
- **devmock 内联**：MSW 假后端（`web/src/mocks`，ADR-047）内联在主应用里，`import '@/...'` 耦合 app 内部，无法作为独立包被复用/约束。
- **命令面分散**：`Makefile`（Unix-shell：`cp`、`cd && ./gradlew`）+ `npm --prefix` + `gradlew` + `go build ./cmd/...` 各一套；在**Windows 开发机**上 Makefile 需 git-bash 才能跑，别扭。
- **前端版本脱离真源**：ADR-065 已确立 `internal/version/version.go` 为版本唯一真值，但 `web/package.json` 仍独立记版并注入前端 `__APP_VERSION__`——真源 bump 后前端不随动（实证：`version.go` 已 `0.15.0-dev`，`web/package.json` 仍 `0.14.0`）。
- **命名误导**：主 React 应用叫 `web/`——本系统存在多个「console」（运维控制台 / 导播台 ADR-035 / jmctl 紧急控制台 ADR-041），裸 `web` 既不说明是哪个后端的前端、也不说明是哪个界面。

诉求：把多语言仓库统一为**一致的顶层布局 + 一个命令面 + 一个版本真源**，同时**不牺牲 Go 原生工具链与 go:embed 单二进制**，不引入重型构建系统。

## 决策

### 1. 顶层三桶 + Go/契约/vendored 留根

以「可运行外壳 / 第一方 JS 库 / vendored 与异构构建输入」划分顶层，**Go 第一方库以原生形态留在根 `internal/`**（Go 本身就是「`cmd/*`=外壳、`internal/*`=库」，不强塞进 `packages/`）：

```
JianManager/
├─ go.mod  pnpm-workspace.yaml
├─ apps/                          # 所有可运行外壳（自解释命名，禁 web/app/frontend 裸词）
│  ├─ control-plane/              # Go：CP 二进制（原 cmd/control-plane）
│  ├─ worker/                     # Go：Worker 二进制（原 cmd/worker）
│  ├─ jmctl/                      # Go：紧急控制台 CLI（原 cmd/jmctl，ADR-041）
│  ├─ bot-worker/                 # Node：Mineflayer 子进程（原 bot-worker/）
│  ├─ control-plane-web/          # React：CP 浏览器控制台（原 web/，点名后端消歧）
│  └─ ui-museum/                  # React：组件博物馆（原 web/wiki，ADR-058）
├─ packages/                      # 第一方 JS 库
│  ├─ ui/                         # @jianmanager/ui 共享组件/token/charts（原 web/packages/ui）
│  ├─ devmock/                    # MSW 假后端（原 web/src/mocks，依赖反转后）
│  ├─ tsconfig/                   # 共享 TypeScript 配置
│  └─ eslint-config/              # 共享 ESLint 配置
├─ internal/                      # Go 第一方共享库（留根，Go 原生）
├─ proto/                         # gRPC 契约（留根）
├─ third_party/                   # vendored 子模块（ServerProbe，不动）
├─ client-updater/                # 异构构建输入（Gradle，不动）
└─ docs/ .claude/ scripts/ ...
```

**命名纪律**：`apps/*` 目录名必须一眼看出「哪个进程 / 哪个后端的哪个界面」。主控台取 `control-plane-web`（点名后端=control-plane，与 `apps/control-plane` 成对），博物馆取 `ui-museum`（对齐 ADR-058 术语）。

**归属判定规则**（新增目录据此归位，避免再出现 `web/` 式误导）：

| 类型 | 例 | 归宿 |
|---|---|---|
| 你运行的外壳 | control-plane / worker / jmctl / bot-worker / control-plane-web / ui-museum | `apps/` |
| 第一方 JS 库 | ui / devmock / tsconfig / eslint-config | `packages/` |
| 第一方 Go 库 | 现 `internal/` | 留根 `internal/`（Go 原生） |
| vendored 上游 / 子模块 | ServerProbe | `third_party/` |
| 异构工具链构建输入 | client-updater（Gradle） | 顶层兄弟 |
| 契约 | proto | 留根 |

### 2. 单 go.mod，Go 仅抬入口、不动 internal

**保留单个根 `go.mod`，不拆多模块、不引入 `go.work`。** Go 侧只把三个 `main` 包 `cmd/* → apps/*`——`main` 包无 importer，移动涟漪极小。`internal/` **原地不动**：它被全项目引用，一旦搬进 `packages/` 会引发几百处 `import` 重写、丢失 Go 的 internal 封装保护，且无任何功能收益。

**go:embed 目标目录不变**：`internal/controlplane/embed/*`（`dist` / probe / cfr / client-updater / worker 二进制 / install-scripts）全部保持原位。唯一变化是前端构建**产物源路径** `web/dist → apps/control-plane-web/dist`（`Makefile` 的 `embed-web` 拷贝源改一行）。

### 3. JS 侧真 pnpm workspace

引入根 `pnpm-workspace.yaml`（`apps/*` + `packages/*`）：`packages/ui` 由 vite alias 改为**真实 workspace 依赖**；抽出 `packages/devmock`（**依赖反转**——只依赖 `docs/API.md` / `packages` 层的 API 契约类型，不再 `import '@/...'` app 内部）；`packages/tsconfig` + `packages/eslint-config` 承载共享配置。**pnpm 只认带 `package.json` 的目录**，故 `internal/`、`third_party/`、`client-updater/` 等非 JS 目录自动被 workspace 忽略，Go 与 JS 混排无冲突。

### 4. 统一命令面 go-task

引入 `Taskfile.yml` 作**唯一命令入口**，各语言暴露同一套动词（`build` / `test` / `lint` / `dev`），底层委托原生工具（`go build`/`go test`、`pnpm -r`、`gradlew`）。`Makefile` **收敛为被委托层**（保留既有 embed/dist 配方，由 Taskfile 调用）。选 go-task 因其**单二进制、跨平台、Windows 友好、Go 生态原生**，贴合本仓技术栈与开发机 OS。

### 5. 前端版本接上单一真源

版本唯一真值已由 ADR-065 确立为 `internal/version/version.go`（`-dev` 生命周期与 CHANGELOG/PRD 联动见 `.claude/rules/versioning.md`）。本决策把**前端侧接上该真源**：`__APP_VERSION__`（现从 `web/package.json` 读）改为构建期从真源派生注入，`package.json` 不再独立记版——改 `version.go` 一处，前端展示、Go `/version`（ldflags）、产物路径（ADR-036 契约）三者一致，消除前端漂移。

## 理由

- **统一「接口层」而非「构建系统」**：一个命令面 + 一套布局 + 一个版本真源，即可获得 90% 的统一感；各语言保留原生构建，零锁定。多语言最难统一的「治理层」（`.claude/rules` + SDD）本就语言无关、已具备。
- **保 Go 原生 + go:embed**：不拆模块、不动 `internal/`、embed 目标不变，把爆炸半径压到最小；单二进制部署（ADR-005）不变。
- **Windows 友好**：go-task 取代 Unix-shell Makefile 作前台入口，开发机直接可跑。
- **命名可自解释**：`apps/*` 点名进程/后端，杜绝 `web/` 式歧义。
- **YAGNI**：`mock-studio` / `admin` / `cli` / `benchmark` 等无当前消费方的可选目录本批不建，有真实需求再各自立 FR。

## 后果

- **大量路径迁移**：`Makefile` / `release.yml` / `Dockerfile.*` 的 build 与 embed 路径、`tsconfig`/`vite`/`playwright` 路径、`docs/ARCHITECTURE.md`、`CLAUDE.md` 目录表、`.claude/rules/architecture-invariants.md`「前端嵌入」措辞均需同步（doc-sync 随对应 FR 落）。
- **pnpm 接管 JS 依赖**：由 `web/package-lock.json`（npm）迁至 pnpm workspace；CI 前端步骤（ADR-036 test 闸、FR-212）改用 pnpm。
- **新增 `Taskfile.yml`**；`Makefile` 降为被委托层。
- **前端版本接真源**改动前端构建的版本注入方式（`__APP_VERSION__` 来源），发布管线读版本处不变（真源本就是 `version.go`，ADR-065）。
- 迁移的**结构移动部分行为不变**（ref）；`control-plane-web` / `ui-museum` 更名需同步 CI/文档中的旧路径引用。

## 替代方案

- **Bazel / Pants（hermetic 多语言构建）**：与 `go:embed`、Go 原生工具链、以及几十份钉死路径的 SDD 文档硬刚，采用成本极高、强锁定。放弃。
- **moon（moonrepo，多语言任务图 + 缓存 + affected）**：是「更进一步统一」的正解，但属 Level 2——待 CI 变慢/跨语言依赖图咬人时再上；本批只做 go-task 命令面。列为后续升级位，不在本批。
- **`go.work` 多模块**（每个 app/包各一 `go.mod`）：单团队单仓下是过度工程，复杂化发布（交叉编译从单模块更简单）。放弃。
- **Go 迁入 `services/api/`**（把 `internal/` 也搬进目录桶）：几百处 import 重写 + 全部 embed 目录迁移 + 丢 internal 封装 + 改遍 SDD 文档，功能收益为零。放弃——`internal/` 留根即 Go 版「packages」。
- **workspace 根设在 `web/`**（apps/packages 嵌套在 web 下）：迁移更小，但不满足「apps 在最外面」的统一诉求，且 `web/` 名本身即误导来源。放弃，取仓库根为 workspace 根。
- **共享配置合一个 `packages/config`**：少一个目录，但 tsconfig 与 eslint 语义混。取拆分（`packages/tsconfig` + `packages/eslint-config`）以求严谨。
