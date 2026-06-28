# 功能规格：前端 mock API 与测试基座

> 状态：开发中　·　关联 PRD：FR-196 / FR-197 / FR-198（地基）、FR-199~210（域簇，契约源在本文档 §7）、FR-211（Playwright E2E，§8）、FR-212（CI 门禁，§9）　·　分支：feature/frontend-mock-api-foundation　·　决策：[ADR-047](../../adr/047-frontend-test-mock-architecture.md)

## 1. 背景与目标

前端无任何 mock / 集成测试设施。目标交付一套**有状态内存假后端**，同时支撑两种用途：

- **可运行 mock 模式**：`VITE_MOCK=1 npm run dev` 起整站、不依赖 Go 后端，全站可点。
- **测试基座**：jsdom + testing-library 渲染页面打到假后端，**强断言**成功 / 错误码 / 空态，数据跨 endpoint **联动**、逻辑正确。

本文档同时是 **FR-199~210 域簇的范式契约源**（§7）——域簇 agent 不再自行设计，照 §7 办理。

## 2. 需求（要什么）

**范围内（地基 FR-196/197/198）**：
- MSW v2 接入（`server` for node、`worker` for 浏览器）+ `public/mockServiceWorker.js`。
- vitest 双 project：`node`（保留纯逻辑）+ `dom`（jsdom，`*.dom.test.tsx`），setup 启停 server、每例 `reset`。
- render harness `renderWithProviders`（QueryClient/Router/i18n/theme，镜像 `main.tsx`）。
- `VITE_MOCK` 浏览器 mock 模式开关（`main.tsx` 条件启 worker）。
- 内存假后端核心：`collection<T>` 抽象（Map+seed+reset）、惰性 collection 工厂、跨实体联动。
- 错误注入框架：`mockInject` / `clearInjections` + `domainRoute` 包装。
- 鉴权中间件 `requireAuth`（token→session 校验）。
- per-domain 自动聚合（`import.meta.glob`）。
- 实时流仿真：WS 终端 PTY 伪交互、SSE `/instances/events` 流。
- 一条纵切端到端验证（auth 域，作为 §7 样例）。

**范围外**：
- 不改任何业务代码 / 真实后端（仅新增 `mocks/`、`test/`、配置）。
- 不建 mock↔真后端类型生成器（YAGNI，ADR-047 已记为已知漂移成本）。
- 日志 / 指标实时流不做 WS/SSE（它们本就是 REST 轮询）。
- 域簇页面 handler / 测试由 FR-199~210 各自交付，本地基只交付 auth 纵切样例。

## 3. 设计（怎么做）

### 3.1 目录布局

```
web/src/
  mocks/
    browser.ts            # setupWorker(...handlers)（FR-196）
    server.ts             # setupServer(...handlers)（FR-196）
    db/
      collection.ts       # createCollection<T>()：list/find/get/insert/update/remove/reset（FR-197）
      index.ts            # db = collection 工厂 + resetDb() + seedAll()（FR-197）
    api.ts                # API(p)=`/api/v1${p}`；http/HttpResponse re-export（FR-196）
    inject.ts             # mockInject/clearInjections/takeOverride + domainRoute 包装（FR-197）
    auth-middleware.ts     # requireAuth(info) → 401 Response | null（FR-197）
    handlers/
      index.ts            # import.meta.glob('./domains/*.ts') 聚合 handlers + seeds（FR-197）
      domains/
        auth.ts           # FR-199（样例由地基交付，其余域簇各自加文件）
        <domain>.ts       # FR-200~210 各加一个
    realtime/
      terminal-ws.ts      # WS 终端伪交互（FR-198）
      instance-events.ts  # SSE /instances/events（FR-198）
  test/
    render.tsx            # renderWithProviders（FR-196）
    setup.ts              # server.listen/resetHandlers/close + resetDb + clearInjections（FR-196）
public/mockServiceWorker.js   # `npx msw init public/`（FR-196，dev 用，不入 go:embed）
```

> **并行不撞车**：域簇只新增 `handlers/domains/<domain>.ts`（+ 可选 `*.dom.test.tsx`），不改 `handlers/index.ts`、不改 `db/index.ts`、不改配置。唯一可能并行编辑的 `package.json`（devDeps 已由地基装齐）/`vite.config.ts` 域簇都不碰。

### 3.2 db 抽象（FR-197）

```ts
// mocks/db/collection.ts
export interface Entity { id: number | string }
export function createCollection<T extends Entity>(seedFn: () => T[] = () => []) {
  let rows: T[] = []
  let auto = 1
  const api = {
    seed() { rows = seedFn(); auto = rows.length + 1 },
    reset() { api.seed() },
    list(pred?: (r: T) => boolean) { return pred ? rows.filter(pred) : [...rows] },
    find(pred: (r: T) => boolean) { return rows.find(pred) },
    get(id: T['id']) { return rows.find(r => String(r.id) === String(id)) },
    insert(row: Omit<T, 'id'> & Partial<Pick<T, 'id'>>) {
      const r = { id: row.id ?? auto++, ...row } as T; rows.push(r); return r
    },
    update(id: T['id'], patch: Partial<T>) {
      const r = api.get(id); if (r) Object.assign(r, patch); return r
    },
    remove(id: T['id']) { rows = rows.filter(r => String(r.id) !== String(id)) },
  }
  return api
}
```

```ts
// mocks/db/index.ts
import { createCollection, type Entity } from './collection'
const registry = new Map<string, ReturnType<typeof createCollection>>()
/** 惰性 collection 工厂：域簇用 db<User>('users', seedFn) 声明自己的集合，不改中心类型。 */
export function db<T extends Entity>(name: string, seedFn?: () => T[]) {
  if (!registry.has(name)) registry.set(name, createCollection<T>(seedFn) as never)
  return registry.get(name)! as ReturnType<typeof createCollection<T>>
}
export function resetDb() { registry.forEach(c => c.reset()) }
export function seedAll() { registry.forEach(c => c.seed()) }
```

跨实体联动靠 handler 读写多个 collection（如 login 写 `sessions`，`requireAuth` 读 `sessions`；createInstance 写 `instances`，list 读同一 collection）。

### 3.3 错误注入 + domainRoute（FR-197）

```ts
// mocks/inject.ts
export type Scenario =
  | { kind: 'status'; status: number; body?: unknown }
  | { kind: 'empty' }            // 列表返回空 / 对象返回 null
  | { kind: 'network' }          // 网络错误（HttpResponse.error()）
  | { kind: 'delay'; ms: number; then?: Scenario }
const overrides = new Map<string, Scenario>()   // key = `${METHOD} ${pattern}`
export function mockInject(method: string, pattern: string, s: Scenario) {
  overrides.set(`${method.toUpperCase()} ${pattern}`, s)
}
export function clearInjections() { overrides.clear() }

import { http, HttpResponse, delay } from 'msw'
import { API } from './api'
type Resolver = Parameters<typeof http.get>[1]
/** 注册一条域路由：先查注入覆盖，否则走 resolver 默认成功。pattern 同时是注入键。 */
export function domainRoute(method: 'get'|'post'|'put'|'patch'|'delete', pattern: string, resolver: Resolver) {
  return http[method](API(pattern), async (info) => {
    const ov = overrides.get(`${method.toUpperCase()} ${pattern}`)
    if (ov) return applyScenario(ov)
    return resolver(info)
  })
}
async function applyScenario(s: Scenario): Promise<Response> {
  if (s.kind === 'delay') { await delay(s.ms); return s.then ? applyScenario(s.then) : HttpResponse.json({}) }
  if (s.kind === 'network') return HttpResponse.error()
  if (s.kind === 'empty') return HttpResponse.json([])
  return HttpResponse.json(s.body ?? { error: 'INJECTED', message: '注入错误' }, { status: s.status })
}
```

测试用：`mockInject('get', '/instances', { kind:'status', status:500 })` → 该页应显示错误态。

### 3.4 鉴权中间件（FR-197）

```ts
// mocks/auth-middleware.ts
import { HttpResponse } from 'msw'
import { db } from './db'
export function requireAuth(info: { request: Request }): Response | null {
  const token = info.request.headers.get('Authorization')?.replace(/^Bearer /, '')
  if (!token || !db('sessions').find((s: any) => s.accessToken === token)) {
    return HttpResponse.json({ error: 'UNAUTHORIZED', message: '未授权' }, { status: 401 })
  }
  return null
}
```

受保护 handler 首行：`const denied = requireAuth(info); if (denied) return denied`。公共端点（`/auth/login`、`/auth/refresh`、`/setup/*`）不调。

### 3.5 render harness + setup（FR-196）

```tsx
// test/render.tsx
import { render } from '@testing-library/react'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/i18n'
export function renderWithProviders(ui: React.ReactElement, { route = '/' } = {}) {
  window.history.pushState({}, '', route)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<BrowserRouter><QueryClientProvider client={qc}>{ui}</QueryClientProvider></BrowserRouter>)
}
```

```ts
// test/setup.ts
import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from '@/mocks/server'
import { resetDb } from '@/mocks/db'
import { clearInjections } from '@/mocks/inject'
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => { server.resetHandlers(); resetDb(); clearInjections() })
afterAll(() => server.close())
```

### 3.6 vitest 双环境（FR-196）

`vite.config.ts` 的 `test` 改为 projects：
```ts
test: {
  projects: [
    { extends: true, test: { name: 'node', environment: 'node',
        include: ['src/**/*.test.{ts,tsx}'], exclude: ['src/**/*.dom.test.{ts,tsx}'] } },
    { extends: true, test: { name: 'dom', environment: 'jsdom',
        include: ['src/**/*.dom.test.tsx'], setupFiles: ['src/test/setup.ts'] } },
  ],
}
```
`package.json`：`"test": "vitest run"`（跑两 project），可加 `"test:dom": "vitest run --project dom"`。

### 3.7 mock 模式开关（FR-196）

```ts
// main.tsx（挂载前）
async function enableMocking() {
  if (!import.meta.env.VITE_MOCK) return
  const { worker } = await import('@/mocks/browser')
  await worker.start({ onUnhandledRequest: 'bypass' })
}
enableMocking().then(() => { /* createRoot(...).render(...) 原逻辑 */ })
```
mock 模式下 seed 一次（`browser.ts` 启动时 `seedAll()`），并可挂一个极简 dev 控制台 API（`window.__mock.inject(...)`）切错误，非必须。

### 3.8 实时流仿真（FR-198）

- **WS 终端**：mock 的 `GET /instances/:id/terminal-token` 返回 `{ token, wsUrl: 'ws://localhost/_mock/terminal', expiresIn: 30 }`；`realtime/terminal-ws.ts` 用 `ws.link('ws://localhost/_mock/terminal')`：连接发 banner（`{type:'stdout',data:'[mock 终端已连接]\n'}`），收 `{type:'stdin',data}` 回显 `{type:'stdout',data}` 假输出（`list`→`players online: ...`、`stop`→`[状态] stopping`+`{type:'state',state:'STOPPED'}`、其余回 `[mock] 已执行: <cmd>`）。
- **SSE `/instances/events`**：handler 返回 `new HttpResponse(stream, { headers: {'Content-Type':'text/event-stream'} })`，`stream` 由 `ReadableStream` 周期 enqueue `event: instance\ndata: {"type":"state_change","instanceUuid":"..."}\n\n`；导出 `emitInstanceEvent(payload)` 供测试 / mock 模式触发。测试中可只 enqueue 一条后保持/关闭。

## 4. 任务拆分

**FR-196（运行基座）**
- [ ] 装 devDeps：msw / jsdom / @testing-library/react / @testing-library/user-event / @testing-library/jest-dom；`npx msw init public/`。
- [ ] `mocks/api.ts`、`mocks/server.ts`、`mocks/browser.ts`、`handlers/index.ts`（glob 聚合，初期可空）。
- [ ] `test/render.tsx`、`test/setup.ts`；`vite.config.ts` 双 project；`package.json` 脚本。
- [ ] `main.tsx` `VITE_MOCK` 条件启 worker。
- [ ] 冒烟：`VITE_MOCK=1` 起站到登录页；一个 demo `*.dom.test.tsx` 用 testing-library 跑通；`npm test` 双 project 绿。

**FR-197（假后端核心 + 注入 + 鉴权 + 聚合）**
- [ ] `db/collection.ts`、`db/index.ts`、`inject.ts`、`auth-middleware.ts`。
- [ ] `handlers/index.ts` 用 `import.meta.glob('./domains/*.ts', { eager:true })` 聚合每模块 `handlers` + 调 `seed`。
- [ ] 单测：collection CRUD/reset、inject 覆盖命中、requireAuth 放行/拒绝（node project，纯逻辑可不入 jsdom）。

**FR-198（实时流）**
- [ ] `realtime/terminal-ws.ts`（ws.link 伪交互）、`realtime/instance-events.ts`（SSE 流 + emitter），并入 `browser.ts`/`server.ts` 的 handler 列表。
- [ ] 测试：terminal-token handler 返回 wsUrl；（真机）mock 模式终端回显；SSE emitter 推一条被 `useInstanceEvents` 消费。

**ADR / 文档**
- [ ] ADR-047（已写）；`docs/ARCHITECTURE.md` 前端架构补「测试与 mock」段；CHANGELOG 未发布段。

## 5. 验收标准

- **FR-196**：① `VITE_MOCK=1 npm run dev` 起站点、能进登录页（真机/浏览器）；② demo `*.dom.test.tsx`（testing-library 渲染一个真实组件并断言文案）绿；③ `npm test` 两 project 全绿、现有 node 纯逻辑单测不回归。
- **FR-197**：① collection insert→list 见到、remove→消失、reset→归种子（单测）；② `mockInject` 命中改变响应（单测）；③ `requireAuth` 无/错 token 401、有效 token 放行（单测）；④ 新增一个 `handlers/domains/*.ts` 即被自动聚合（不改 index）。
- **FR-198**：① mock `GET /instances/:id/terminal-token` 返回含 `wsUrl`；② **真机**：mock 模式打开实例终端能输入命令并回显假输出（用户浏览器确认）；③ `emitInstanceEvent` 推送一条，`useInstanceEvents` 收到并失效 `['instances']`（jsdom 测试或真机）。
- **纵切（auth 样例，§7）**：LoginPage 在 mock 下：正确凭据→登录跳转；**错误凭据→显示错误且页面不刷新/不跳转**（即登录 bug 的回归靶子）；`mockInject` 注入 500→显示错误态。
- **真机维度**（须用户在浏览器确认，测试绿不替代）：mock 模式整站可点 + 终端回显 + SSE 推送到达。

## 6. 风险 / 待定

- **WS 终端全仿真**最脆：MSW `ws` API 较新，jsdom 下 WebSocket 行为需验证；若 jsdom 测试不稳，终端交互**降为真机验收**、jsdom 只测 token handler。退路：终端 banner 占位 + SSE 全仿真（ADR-047 未采但保留）。
- `import.meta.glob` 在 vitest 下需 Vite 解析（vitest 基于 Vite，预期可用）；若失效，退回 `handlers/index.ts` 显式 import（届时成为唯一并行接触点，需串行合并该文件）。
- `onUnhandledRequest:'error'` 会让未 mock 的请求测试即失败——这是**有意**的覆盖闸，域簇必须补齐自己的 handler。

## 7. 域簇范式契约（FR-199~210 照此办理，**不要另行设计**）

每个域簇 = **一个 `handlers/domains/<domain>.ts`** + **该域页面的 `*.dom.test.tsx`**。`<domain>.ts` 必须导出：
- `export const handlers: HttpHandler[]` —— 用 `domainRoute(method, pattern, resolver)` 注册该域**全部** endpoint，读写 `db('<collection>')`，受保护端点首行 `requireAuth`。
- `export function seed(): void` —— 用 `db('<collection>', () => [...假数据])` 声明并播种本域 collection（**惰性声明在 resolver 里也可**，但 seed 必须保证列表非空、字段贴合 `web/src/api/<module>.ts` 的 TS 类型 / `docs/API.md`）。

**字段保真**：返回结构必须匹配既有 `web/src/api/*.ts` 的 interface（含 `tags`/`envVars`/`launchSpec` 等**字符串化** JSON 字段——见全局记忆「JSON 字符串字段前端解析」：后端返字符串，前端解析；mock 也必须返字符串）。

**完整样例（auth 域，地基随 FR-199 交付，其余域照抄结构）**：

```ts
// mocks/handlers/domains/auth.ts
import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'

interface User { id: number; uuid: string; username: string; password: string; role: number; disabled?: boolean }
interface Session { id: number; accessToken: string; refreshToken: string; userId: number }

export function seed() {
  db<User>('users', () => [
    { id: 1, uuid: 'u-admin', username: 'admin', password: 'admin123', role: 10 },
    { id: 2, uuid: 'u-op', username: 'operator', password: 'op123456', role: 1 },
  ])
  db<Session>('sessions', () => [])
}

// 简化 JWT：mock 不验签，payload 内嵌 role/username 供前端 decodeJwt 读取
function fakeJwt(u: User) {
  const payload = btoa(JSON.stringify({ userId: u.id, username: u.username, role: u.role, exp: Math.floor(Date.now()/1000)+900 }))
  return `mock.${payload}.sig`
}

export const handlers = [
  domainRoute('post', '/auth/login', async ({ request }) => {
    const { username, password } = await request.json() as { username: string; password: string }
    const u = db<User>('users').find(x => x.username === username && x.password === password && !x.disabled)
    if (!u) return HttpResponse.json({ error: 'UNAUTHORIZED', message: '用户名或密码错误' }, { status: 401 })
    const s = db<Session>('sessions').insert({ accessToken: fakeJwt(u), refreshToken: `r-${u.id}-${Date.now()}`, userId: u.id })
    return HttpResponse.json({ accessToken: s.accessToken, refreshToken: s.refreshToken, expiresIn: 900 })
  }),
  domainRoute('post', '/auth/refresh', async ({ request }) => {
    const { refreshToken } = await request.json() as { refreshToken: string }
    const s = db<Session>('sessions').find(x => x.refreshToken === refreshToken)
    if (!s) return HttpResponse.json({ error: 'UNAUTHORIZED', message: 'refreshToken 无效或已过期' }, { status: 401 })
    const u = db<User>('users').get(s.userId)!
    db<Session>('sessions').update(s.id, { accessToken: fakeJwt(u) })
    return HttpResponse.json({ accessToken: s.accessToken, refreshToken: s.refreshToken, expiresIn: 900 })
  }),
  // …users/groups/audit/setup 其余 endpoint，受保护者首行 requireAuth …
]
```

```tsx
// pages/LoginPage.dom.test.tsx（页面强断言样例）
import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import LoginPage from './LoginPage'

describe('LoginPage（mock）', () => {
  it('错误凭据：显示错误且不跳转（登录 bug 回归）', async () => {
    renderWithProviders(<LoginPage />, { route: '/login' })
    await userEvent.type(screen.getByLabelText(/用户名|username/i), 'admin')
    await userEvent.type(screen.getByLabelText(/密码|password/i), 'wrong')
    await userEvent.click(screen.getByRole('button', { name: /登录|submit/i }))
    expect(await screen.findByText(/用户名或密码错误|失败/)).toBeInTheDocument()
    expect(window.location.pathname).toBe('/login')   // 未被整页跳转
  })
  it('注入 500：显示错误态', async () => {
    mockInject('post', '/auth/login', { kind: 'status', status: 500 })
    renderWithProviders(<LoginPage />, { route: '/login' })
    // …填表提交，断言错误提示…
  })
})
```

> 域簇验收统一三条：① 页面渲染出 seed 数据（强断言具体文案/行数）；② 典型写操作后列表 / 详情**联动**变化；③ `mockInject` 注入错误后页面显示错误态（非崩溃、非整页刷新）。

## 8. Playwright E2E（FR-211，跑 mock 模式整站）

E2E 是基座的**消费方**：跑 `VITE_MOCK` 整站、真浏览器、无需真后端，验关键**跨页流**（不逐页覆盖——逐页归 jsdom 组件测，YAGNI）。

### 8.1 设计
- devDep `@playwright/test`；`web/playwright.config.ts`：
  - `webServer: { command: 'npm run dev', env: { VITE_MOCK: '1' }, url: 'http://localhost:5173', reuseExistingServer: !process.env.CI }`
  - `use.baseURL: 'http://localhost:5173'`；`projects: [{ name:'chromium', use: devices['Desktop Chrome'] }]`。
- E2E 用例放 `web/e2e/*.spec.ts`（**独立于 vitest** 的 `src/**` include，互不串台）；eslint 对 `e2e/**` 用 Playwright 环境或单独 tsconfig。
- 脚本：`package.json` 加 `"e2e": "playwright test"`、`"e2e:headed": "playwright test --headed"`。

### 8.2 关键流（首批，可扩）
1. **登录**：错误凭据→显错且停在 `/login`（登录 bug 端到端回归）；正确凭据→进控制台。
2. **导航**：登录后侧栏跳转 Instances / Nodes / Monitoring 等，页面有 seed 内容。
3. **实例生命周期**：创建实例→列表出现→启动→状态变 RUNNING→停止/删除→消失（依赖 FR-201 域簇 + mock 联动）。
4.（可选）**终端**：打开运行中实例终端，输入命令→回显假输出（依赖 FR-198/209）。

### 8.3 任务 / 验收
- [ ] 装 `@playwright/test` + `playwright.config.ts` + `e2e/` + 脚本 + eslint 适配。
- [ ] 写 8.2 的 1~3（4 视终端稳定性）。
- **验收**：`npm --prefix web run e2e` 在 CI headless 全绿；本地 `--headed` 可见整站走通；E2E 不依赖真后端（仅 `VITE_MOCK`）。

## 9. CI 前端质量门禁（FR-212，PR 拦截）

### 9.1 现状与缺口
`release.yml` 的 `test` job 已跑前端 `lint + vitest + build` 并阻断 build/release——但**仅 push master/tag 触发**，PR/分支合并前不挡，且无 E2E。

### 9.2 设计
- **新增 `.github/workflows/ci.yml`**：`on: { pull_request:, push: { branches-ignore: [master] } }`（master push 由 release.yml 管，避免重复）。job `web-quality`（ubuntu, node 20）：
  ```
  npm --prefix web ci
  npm --prefix web run lint
  npm --prefix web run test          # vitest run → node + dom 两 project
  npm --prefix web run build
  npx --prefix web playwright install --with-deps chromium
  npm --prefix web run e2e
  ```
  （可选并行 job `go-quality`：go build/vet/test，PR 即挡后端回归。）
- **扩 `release.yml` 的 test job**：现有「前端 依赖 / lint / 单测 / 构建」步骤后补 `playwright install --with-deps chromium` + `playwright test`——发版前也过 E2E。
- **阻断语义**：job 失败→check 失败；仓库**分支保护**勾选这些 check 为必需即合并前硬挡（分支保护是仓库设置，CI 提供 checks；本 FR 交付 workflow，保护规则在 README/OPERATIONS 注明需手动开）。

### 9.3 任务 / 验收
- [ ] `ci.yml`（lint + vitest + build + e2e）；`release.yml` test 闸补 e2e。
- [ ] `docs/OPERATIONS`（或 README）注明：开启分支保护、勾选 `web-quality` 为必需 check。
- **验收**：① PR 上 CI 跑出 lint/vitest/e2e 并在失败时 check 变红（**真机由用户在一条 PR 上确认**——故意引入 lint 错/失败测试/失败 E2E 各验一次）；② 三者全过 PR 才显绿。

> 真机维度（须用户确认）：FR-211 整站 E2E 在真浏览器走通；FR-212 在真实 PR 上看到 CI 拦截生效。CI 行为无法纯本地断言，列为显式手动验收。
