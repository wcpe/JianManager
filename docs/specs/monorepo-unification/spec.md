# Spec: 多语言 monorepo 统一布局与命令面（FR-283~288）

- **状态**: 🔨 开发中（FR-283 spec 已落审；FR-284/285/286 免 spec，以本文档对应节为执行边界；FR-287/288 需 spec，见 §5/§6）
- **关联**: ADR-064（accepted，布局与命令面决策）、ADR-005（go:embed 单二进制，重申不变）、ADR-047（devmock 仅 dev/test 形态）、ADR-058（`@jianmanager/ui` 组件包）、ADR-065（`version.go` 版本唯一真值）、ADR-036（发布管线与产物命名）
- **计划**: `.tmp/brainstorm-monorepo-unification-2026-07-12.md`；执行序 `283 → (284, 285) → 286 → 287 → 288`，单分支串行

## 1. 目标布局（终态）

```
JianManager/
├─ go.mod  pnpm-workspace.yaml  package.json(根,private)  Taskfile.yml(FR-287)
├─ apps/
│  ├─ control-plane/        # Go main（原 cmd/control-plane，FR-285）
│  ├─ worker/               # Go main（原 cmd/worker，FR-285）
│  ├─ jmctl/                # Go main（原 cmd/jmctl，FR-285）
│  ├─ bot-worker/           # Node 子进程（原 bot-worker/，FR-286；npm 自管，排除出 pnpm workspace）
│  ├─ control-plane-web/    # React 主控台（原 web/，FR-283）
│  └─ ui-museum/            # React 组件博物馆（原 web/wiki，FR-283）
├─ packages/
│  ├─ ui/                   # @jianmanager/ui（原 web/packages/ui，FR-283）
│  ├─ devmock/              # @jianmanager/devmock（原 web/src/mocks，FR-284）
│  ├─ tsconfig/             # @jianmanager/tsconfig 共享 TS 配置（FR-283 新增）
│  └─ eslint-config/        # @jianmanager/eslint-config 共享 lint（FR-283 新增）
├─ internal/  proto/        # Go 库与契约，留根不动
├─ third_party/  client-updater/   # vendored / 异构构建输入，不动
└─ docs/  .claude/  scripts/ ...
```

## 2. FR-283 仓库根 pnpm workspace + 前端提升（ref，需 spec）

### 2.1 范围

真 workspace 化 + 前端目录消歧，**行为不变**（ref）。不含：devmock 抽包（FR-284）、Go/bot-worker 移动（FR-285/286）、Taskfile（FR-287）、版本接真源（FR-288）。

### 2.2 迁移步骤（git mv 保历史；「纯移动」与「内容修改」分 commit）

1. 根建 `pnpm-workspace.yaml`（`apps/*`、`packages/*`，显式排除 `apps/bot-worker`）+ 根 `package.json`（`private: true`、`packageManager` 钉 pnpm 版本、聚合脚本）。
2. `git mv web apps/control-plane-web` → `git mv apps/control-plane-web/packages/ui packages/ui` → `git mv apps/control-plane-web/wiki apps/ui-museum`。
3. `apps/control-plane-web`、`apps/ui-museum` 的 `package.json` 声明 `"@jianmanager/ui": "workspace:*"`；删除两处 `vite.config.ts` 的 `@jianmanager/ui` alias（保留 `@` → `./src`）。`packages/ui` 维持源码 exports（`./src/index.ts`），由消费方 Vite 直接转译，不引入独立构建步。
4. 抽 `packages/tsconfig`（`base.json` / `react-app.json` / `node.json` 预设）与 `packages/eslint-config`（flat config），两 app 与 `packages/ui` extends/import 之，消除三处重复配置。
5. npm → pnpm：删 `web/package-lock.json`，根 `pnpm install` 生成 `pnpm-lock.yaml`；pnpm 严格 node_modules 暴露的幽灵依赖逐个补显式声明。
6. 构建链路径同步（见 §2.3 触点清单），embed 拷贝目标 `internal/controlplane/embed/dist` **不变**，仅源改 `apps/control-plane-web/dist`。
7. 文档同步：`CLAUDE.md` 目录表、`docs/ARCHITECTURE.md` 前端架构节、`.claude/rules/architecture-invariants.md` 核对（go:embed 语义不变，无需改不变量本身）。

### 2.3 迁移触点清单（实施时逐项核对，以构建绿为准）

| 类别 | 触点 |
|---|---|
| 前端配置 | `vite.config.ts`×2（alias/路径）、`tsconfig.{json,app,node}`、`eslint.config.js`、`playwright.config.ts`、`components.json`、`.env.mock`、`index.html`、`public/mockServiceWorker.js`（随 app 走，ADR-047 dev 专用） |
| Makefile | `embed-web`（`cp -r web/dist/*` → `apps/control-plane-web/dist/*`）、`gen-licenses`（web 工作目录 + `web/public/licenses.json` 路径）、`clean`（`web/dist`） |
| CI | `.github/workflows/release.yml`：前端 build/test 步骤路径 + pnpm 安装（corepack）+ FR-212 前端质量门禁 job 路径 |
| Docker | `Dockerfile.control-plane` 前端构建阶段（路径 + pnpm） |
| 忽略清单 | `.gitignore` / `.dockerignore` 中 `web/` 路径引用 |
| 文档 | `CLAUDE.md`、`docs/ARCHITECTURE.md`、`web/README.md`（随移动更名核对内容） |

### 2.4 明确不做

- 不建 `apps/mock-studio` / `apps/admin` / `packages/cli` / `packages/benchmark`（YAGNI，ADR-064 既定）。
- 不抽 `packages/api-contracts`（见 §3 FR-284 决策）。
- 不动 `internal/`、`proto/`、`third_party/`、`client-updater/`。
- CI 不改发布语义（ADR-036 契约不变），只改路径与包管理器。

## 3. FR-284 devmock 抽包（ref，免 spec——本节即执行边界）

- 移 `apps/control-plane-web/src/mocks` → `packages/devmock`（`@jianmanager/devmock`）。
- **依赖反转决策**：devmock **自持类型**——以 `docs/API.md` 为契约参照在包内声明请求/响应形状，**禁止 import 应用内部（`@/…`）**；应用（`main.tsx` 的 `VITE_MOCK` 分支）与测试（`server.ts` 引用处）改 import `@jianmanager/devmock`。不抽独立契约包：mock 数据天然形状绑定，形状漂移由既有 dom 测试（真组件打真 mock，ADR-047 机制）看守；将来出现第二个消费 app 再立 FR 抽 `api-contracts`。
- `public/mockServiceWorker.js`（msw init 产物）留 app，不入包。
- **不变量保持**：生产构建不含 devmock（`VITE_MOCK` 条件动态 import 机制不变），验收断产物 chunk 无 devmock 代码。

## 4. FR-285 / FR-286 Go 入口与 bot-worker 迁 apps（ref，免 spec——本节即执行边界）

### FR-285（Go 三入口）
- `git mv cmd/control-plane apps/control-plane`（worker / jmctl 同）；`go.mod` module 名不变，包路径变 `<module>/apps/...`——main 包无 importer，**只有构建脚本引用变**。
- 触点：`Makefile`（`build-*` / `dist*` / `embed-worker` / `dev` 的 `./cmd/*` → `./apps/*`）、`release.yml` go build 步骤、`Dockerfile.control-plane` / `Dockerfile.worker`、README 构建说明；实施时 `grep -r "cmd/(control-plane|worker|jmctl)"` 清零非历史引用（ADR/CHANGELOG 历史记录不改）。
- go:embed 目标目录全部不变（`internal/**/embed/*`）。

### FR-286（bot-worker）
- `git mv bot-worker apps/bot-worker`；**保持 npm 自管**（生产随 Worker 部署自带 node_modules 的机制不变），已在 §2.2-1 排除出 pnpm workspace。
- Worker 侧路径解析是**配置驱动**（`internal/worker/bot/manager.go` 的 `BotWorkerPath`，env `JIANMANAGER_BOT_WORKER_PATH`）：更新**默认相对路径**与所有默认值出处（worker 配置默认、e2e `internal/e2e/e2e_fullchain_test.go`、部署打包引用）；显式配置的存量部署不受影响。
- ⚠️ 运行时路径耦合：测试绿 ≠ 真能用，**必真机验 bot 真入服**。

## 5. FR-287 统一命令面 go-task（feat，需 spec）

- 根 `Taskfile.yml` 为**唯一命令入口**；动词矩阵：

| 动词 | 委托 |
|---|---|
| `task build` | `go build ./apps/...` + `pnpm -r build`（含 embed-web 前置） |
| `task test` | `go test ./...` + `pnpm -r test` |
| `task lint` | `go vet` / `golangci-lint` + `pnpm -r lint` |
| `task dev` | 起 CP（`--dev`）+ 前端 dev server |
| 域命名空间 | `go:*` / `web:*` / `probe:*`（gradlew）/ `dist` 等细分目标 |

- **决策：task 只编排、不搬逻辑**——`Makefile` 保留 embed/dist 配方作被委托层（避免双真源）；CI 仍直调底层命令（不强制管线走 task，避免大改 release.yml）。
- 验收硬指标：**Windows（开发机 PowerShell）+ Linux 双平台跑通**；README 记录动词表与 go-task 安装。

## 6. FR-288 前端版本接真源（feat，需 spec）

- 真源 = `internal/version/version.go`（ADR-065 既定）。机制：构建期从该文件提取版本注入 Vite `define`（`__APP_VERSION__`）——dev 模式 `vite.config.ts` 直接读文件解析；生产构建同路径（monorepo 下相对路径稳定）。
- `apps/control-plane-web/package.json` 的 `version` 字段冻结 `0.0.0`（不再承载版本语义）；`ui-museum` 同。
- 约束：不引入对版本号的严格 `^\d+\.\d+\.\d+$` 校验（versioning.md 既定，须容 `-dev` / `-rc.N`）。
- 验证：bump `version.go` → 前端侧栏版本随动，与 Go `/version`、产物路径（ADR-036）三者一致。

## 7. 验收标准（逐 FR）

- **FR-283**：根 `pnpm install` 成功；`control-plane-web` + `ui-museum` build 绿；vitest（node+dom 双 project）全绿；Playwright e2e 绿；`make embed-web && go build` 绿（embed 新源路径）；CI 路径同步（真 CI 绿于 push 后验）；**真机 CP 起、前端正常加载**；`git log --follow` 可追历史。
- **FR-284**：`VITE_MOCK` 整站 mock 可跑；vitest 全绿；**生产构建产物无 devmock 代码**；e2e mock 模式绿。
- **FR-285**：`go build ./apps/...` + `go test ./...` + `go vet ./...` 绿；`make dist` 四产物出齐；**真机 CP/Worker/jmctl 起**。
- **FR-286**：单测绿；**真机 bot 真入服 spawn 成功**（必验项）。
- **FR-287**：`task build/test/lint` Windows + Linux 双平台通，覆盖 Go+JS+probe 三链；README 落。
- **FR-288**：bump 真源一处 → 前端展示 + `/version` + 产物路径一致（含 `-dev` 后缀场景）。

## 8. 风险与回退

- **移动/修改分 commit**：每个 `git mv` 批次单独提交，保 rename 检测与可整体 revert。
- **pnpm 幽灵依赖显形**：预期若干未声明依赖报错，逐个显式补——属修正而非回退项。
- **CI 双真源窗口**：路径改动与 `release.yml` 更新必须同 commit，避免中间态红。
- **Windows 兼容**：pnpm 默认 junction link，无管理员权限要求；已在本仓开发机形态内。
- **回退**：各步独立可编译提交，`git revert` 粒度回退；embed 目标不变故 Go 侧回退面极小。
