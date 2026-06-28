# ADR-047: 前端测试与 mock 架构（MSW 内存假后端 + 双运行形态 + per-domain 自动聚合）

- **日期**: 2026-06-28
- **状态**: accepted
- **关联**: FR-196/197/198（地基）、FR-199~210（域簇）；[ADR-005](005-frontend-embed.md)（go:embed 前端，mock 仅 dev/test 形态、不入嵌入产物）

## 上下文

前端（`web/`）此前无任何 mock / 集成测试设施：测试是 vitest `node` 环境的纯逻辑单测（无 jsdom、无 testing-library、无网络 mock）。新需求要一套能同时满足两种用途的"前端假后端"：

1. **可运行 mock 模式**：`VITE_MOCK=1` 起整个前端、不依赖 Go 后端，全站可点（开发 / 演示 / UI 走查）。
2. **自动化测试基座**：渲染页面打到假后端、**强断言**各状态（成功 / 各错误码 / 空态），逻辑正确、数据跨 endpoint **联动**。

约束与现状：① axios 客户端 baseURL `/api/v1`，401 响应拦截器自动刷新 / 跳登录；② 实时面窄——**WS 仅终端**（`Terminal.tsx` 连 `${wsUrl}?token=`，收发 `{type:stdin|resize}` / `{type:stdout|stderr|state}`），**SSE 仅 `/instances/events`**（`fetch`+`ReadableStream`，非 EventSource），日志 / 指标只是 REST 轮询；③ ~48 API 模块 / ~30 页要一次铺满、且**最大并行**开发（12 路 worktree），中心文件改动会撞车。

## 决策

1. **MSW v2 作唯一传输拦截层**：node 测试用 `setupServer`、浏览器 mock 模式用 `setupWorker`；WS 用 `ws.link` 拦截（同 realm patch WebSocket），SSE 用 `http` handler 返回流式 `Response`。一套 handler 两形态复用，不引第二套 mock 机制。
2. **有状态内存假后端（手写领域层）**：通用 `collection<T>(name)` 抽象（`Map` + `seed` + `reset`），跨实体联动（登录→session→鉴权；建实例→入列表→可删）。**不**用 `@mswjs/data` 自动 CRUD——本项目 API 动作型重（start/stop/deploy/clone…）、字段形态特殊（`envVars`/`launchSpec`/`tags` 为 JSON 字符串），自动 handler 适配差；其"关系型内存库"思路可借鉴但不直接采用。
3. **双运行形态共享 handler/seed/db**：浏览器 mock 模式由 `VITE_MOCK` 开关（`main.tsx` 挂载前条件 `await worker.start()`）；测试由 jsdom project 的 setup 启 `server`。二者 import 同一 `handlers` / `db` / `seed`。
4. **vitest 双 project**：`node`（纯逻辑单测，**保留现状**，`*.test.ts`）+ `dom`（jsdom + testing-library，组件 / 页面强断言，`*.dom.test.tsx`）。互不污染、互不重配。
5. **per-domain 自动聚合（最大并行的前提）**：域簇各自只新增 `web/src/mocks/handlers/domains/<domain>.ts`（导出 `handlers` 数组 + 可选 `seed(db)`），由 `import.meta.glob` 聚合；**无中心 index 需要编辑** → 12 路并行零冲突。`db` 为惰性 collection 工厂，域簇声明自己的 collection 不改中心类型。
6. **错误注入注册表**：`mockInject(method, pathPattern, scenario)` 运行时覆盖（status 401/403/404/409/500、空态、网络错误、延迟）；handler 经 `domainRoute()` 包装，先查覆盖再走默认成功——"成功默认铺满 + 按需注入错误"，避免 200×N 组合爆炸。
7. **实时流仿真**：WS 终端 PTY 伪交互（连接发 banner，收 `stdin` 回 `stdout` 假输出，`list`→在线玩家等少量拟真）；SSE `/instances/events` 返回可控 emitter 驱动的流式 Response。日志 / 指标仍走 REST 轮询、**不**入流设施。
8. **E2E 层 + CI 门禁（FR-211/212）**：在单测（node）/ 组件测（jsdom）之上加 **Playwright E2E**，跑 `VITE_MOCK` mock 模式整站（无需真后端）验关键跨页流；E2E 是基座的**消费方**而非替代——MSW 假后端同时喂组件测与 E2E。CI 分两闸：新增 `.github/workflows/ci.yml`（on `pull_request`/`push`）拦 web lint + vitest(node+dom) + Playwright E2E，**合并前**阻断；既有 `release.yml` 的 test 闸（已跑 lint+vitest）补加 E2E，**发版前**阻断。前端 lint 复用既有 eslint（`web/eslint.config.js`），不新引 lint 工具。

## 理由

- **单一拦截层、两形态复用**：MSW 是唯一同时覆盖 node/浏览器 + fetch/XHR/WS/SSE 的方案，避免维护两套 mock。
- **手写领域层对动作型 API 保真**：联动与动作语义靠手写最直接，强断言才有"逻辑正确"的靶子。
- **per-domain glob 让最大并行不撞车**：把唯一中心接触点（handler/seed 聚合）消解为"加文件即生效"，12 路 worktree 各写各的。
- **注入注册表**：以"默认成功 + 局部注入"覆盖"所有情况"，体量可控、可扩展。

## 后果

- 新增 devDeps：`msw`、`jsdom`、`@testing-library/react`、`@testing-library/user-event`、`@testing-library/jest-dom`、`@playwright/test`（FR-211）。
- 新增 `.github/workflows/ci.yml`（FR-212，PR/push 门禁）；`release.yml` test 闸补加 E2E job。Playwright 浏览器在 CI 用 `npx playwright install --with-deps chromium`。
- 新增目录：`web/src/mocks/`（browser/server/db/inject/auth-middleware/realtime/handlers/domains）、`web/src/test/`（render harness + setup）。
- 改 `web/vite.config.ts`（vitest projects）、`web/src/main.tsx`（`VITE_MOCK` 条件启 worker）、`web/package.json`（脚本 `test`/`test:dom`、devDeps）、`public/mockServiceWorker.js`（`msw init` 生成，仅 dev 用、不入 go:embed 产物）。
- 新增配置项 `VITE_MOCK`（构建期 env，仅前端 dev/test；不进 `control-plane.yaml`）。
- **已知成本**：mock 后端镜像 200+ endpoint 会与真后端**漂移**；本批不建代码生成器（YAGNI），靠契约对齐 `docs/API.md` + `web/src/api/*.ts` 类型。
- 架构不变量不破：mock 仅 dev/test 形态，不改三进程模型、不入生产嵌入产物（ADR-005）。

## 替代方案

- **axios-mock-adapter** — 只拦 axios，漏 `/instances/events` 的原生 fetch SSE 与终端 WS，无法支撑"整站 + 实时"，否决。
- **@mswjs/data 自动 CRUD** — 关系型内存库省 CRUD 手写，但动作型端点 / 特殊字段适配差、仍需大量自定义 handler，徒增 DSL 学习成本，否决为"自动 handler"，仅借鉴关系思路。
- **Storybook / 纯 fixture 夹具** — 不满足"联动 + 强断言 + 整站可运行"，否决。
- **Playwright 真实 E2E（连真后端）** — 重、慢、需真后端。**改为 Playwright 跑 mock 模式整站**（决策 8）：保留真浏览器端到端价值、又不依赖真后端，作单测/组件测之上的顶层闸，采纳。
- **仅 Playwright、不要 MSW 组件测** — E2E 慢、定位差、每页覆盖成本高；分层（组件测细、E2E 抓关键流）更划算，否决。
