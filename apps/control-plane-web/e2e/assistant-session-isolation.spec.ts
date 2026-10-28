import { test, expect, type Locator, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-519「跨路由不丢会话」与「换用户不串号」两条验收（mock 模式真浏览器）。
 *
 * ── 为什么这两条必须用 e2e，而不是组件测 ──
 *
 * 两条验收守的都是**「状态不属于某个会消失的东西」**：
 *  1. 浮窗挂在 Dashboard 壳层（`DashboardPage`），路由切换只换 `<Workspace />` 里的页面。
 *     要验的是「路由变了、组件树没重建，会话与草稿还在」。jsdom 里换成内存路由会把这件事
 *     绕过去——而「浮窗是否真的放在壳层」恰恰是最容易回归的地方（有人把它挪进某个页面里，
 *     组件测仍会绿）。
 *  2. 换用户不串号涉及三处**不随组件卸载清空**的存储：
 *       - `sessionStorage`（草稿持久化，见 `lib/assistant/chat-draft.ts`）——按标签页存活，
 *         登出不会自动清，`page.goto` 也不会清；
 *       - 模块级 `draftCache`（同标签页内多视图共享的那层 Map）——挂在模块作用域上，
 *         组件卸载与路由跳转都清不掉，只有 `resetDraftScope()` 会清；
 *       - React Query 缓存（`['assistant','chat', ...]` 前缀）——跨页面共享。
 *
 * ## 所以「换用户不串号」**必须真实登出 → 真实登录**，且第二次登录刻意**不整页刷新**
 *
 * 这是本文件最关键的一条纪律。若用「新开 browser context」或登录前 `page.goto()` 重置上下文
 * 来「模拟换人」，上述三处存储会被浏览器整个丢掉，用例会**无条件通过**，却完全没验证到
 * 产品里的 `resetDraftScope()`。这里走最苛刻的路径：同一标签页、同一 JS 上下文，点
 * 「账户 → 退出登录」真实登出，再在登录页就地登录，让模块级缓存与 sessionStorage 都活着。
 * 这条路径下若有串号缺陷，用例一定红。
 *
 * ## 断言纪律
 *
 * 不为「让用例变绿」放宽断言。若「换用户不串号」失败，说明产品仍有串号缺陷，
 * 应当红着并如实报告，而不是把 `not.toContain` 之类删掉。
 */

/** devmock 的回复固定以这句话开头（`收到你的问题：「<原文前 60 字>」`）。 */
const MOCK_REPLY_MARK = '收到你的问题：「'

/**
 * 等 mock 后端（MSW Service Worker）真正接管请求。
 *
 * 【为什么需要】`main.tsx` 刻意**先渲染、后异步启 MSW**（否则整站要等 worker 起来才白屏），
 * 代价是这段窗口里发出的 API 请求会真的走 Vite 代理打到不存在的后端。登录请求若正好落在
 * 窗口里就会静默失败、页面停在登录页——表现为「点了登录没反应」，与产品无关。
 * 实测踩到过一次：同一用例单独跑必过、跟在另一用例后面跑偶发失败。
 *
 * 探测点选 `/setup/status`：它是 mock 里**公开且无副作用**的端点（固定回 `setupRequired:false`），
 * 能回来就说明 worker 已在拦截。探测失败只在超时后报错，不掩盖真正的产品问题。
 */
async function waitForMockBackend(page: Page): Promise<void> {
  const deadline = Date.now() + 30_000
  let last = '未发起'
  while (Date.now() < deadline) {
    last = await page.evaluate(async () => {
      try {
        const res = await fetch('/api/v1/setup/status', { headers: { Accept: 'application/json' } })
        return `status=${res.status}`
      } catch (err) {
        return `fetch 失败：${String(err)}`
      }
    })
    if (last === 'status=200') return
    await page.waitForTimeout(250)
  }
  throw new Error(`mock 后端在 30s 内未接管请求（最近一次：${last}），登录无从验证`)
}

/**
 * 清掉本地登录态，让 `/login` 真的渲染出表单。
 *
 * 【为什么需要】`LoginPage` 对**已登录**用户会 `<Navigate to={returnTo} replace />`
 * （见 `pages/LoginPage.tsx` 的 isAuthenticated 分支）。所以「页面上已经有 token 时
 * 再打开 /login」看到的不是登录表单，而是被弹回控制台——此时等 `用户名` 输入框会一直等到超时。
 * 这是本用例的**准备动作**（模拟一个干净的浏览器），不是被测行为：
 * 真正要守的「换用户」用的是下面的就地登录，全程不刷新、不清存储。
 */
async function clearAuth(page: Page): Promise<void> {
  await page.goto('/login', { waitUntil: 'domcontentloaded', timeout: 30_000 })
  const hadToken = await page.evaluate(() => {
    const had = localStorage.getItem('accessToken') !== null
    localStorage.removeItem('accessToken')
    localStorage.removeItem('refreshToken')
    return had
  })
  // 有 token 时首屏已被重定向走，必须再加载一次才会落到登录页。
  if (hadToken) await page.goto('/login', { waitUntil: 'domcontentloaded', timeout: 30_000 })
  await waitForMockBackend(page)
  await expect(
    page.getByLabel('用户名', { exact: true }),
    '登录页应渲染出用户名输入框（若卡在这里，通常是被已登录态重定向回了控制台）',
  ).toBeVisible({ timeout: 30_000 })
}

/**
 * 从「干净浏览器」状态登录指定账号。
 *
 * 仅用于用例的**准备阶段**（把测量起点归位）。会用整页加载与清 token，
 * 因此绝不能出现在测量区间里——那里必须走 `loginInPlace` 的同一 JS 上下文路径。
 */
async function loginAs(page: Page, username: string, password: string): Promise<void> {
  await clearAuth(page)
  // 交给公共 helper：它负责填充、点登录并等控制台外壳。
  await login(page, username, password)
}

/** 发一轮并等这一轮**真正收尾**（与 `assistant-page-context.spec.ts` 同口径）。 */
async function sendRound(panel: Locator, text: string): Promise<void> {
  const input = panel.getByLabel('输入消息')
  await input.fill(text)

  const sendButton = panel.getByRole('button', { name: '发送', exact: true })
  await expect(sendButton, '填入文本且上一轮已收尾时，发送按钮应可用').toBeEnabled()
  await sendButton.click()

  await expect(
    panel.getByRole('log'),
    `第「${text}」轮应收到 devmock 的完整回复`,
  ).toContainText(`${MOCK_REPLY_MARK}${text.slice(0, 60)}`, { timeout: 30_000 })

  // 必须等到 `running` 回 false：否则下一轮发送会被 `send` 开头的 running 守卫静默吞掉，
  // 表现为「点了没反应」——那是用例自身的问题，不该混进产品结论。
  await expect(
    panel.getByRole('button', { name: '停止生成' }),
    '本轮 run_end 后应退出「生成中」状态',
  ).toHaveCount(0, { timeout: 30_000 })
}

function dockOf(page: Page): Locator {
  return page.getByRole('complementary', { name: 'AI 助手' })
}

/** 打开壳层顶栏的助手浮窗（已打开则直接返回，避免重复点击把它关掉）。 */
async function openAssistantDock(page: Page): Promise<Locator> {
  const panel = dockOf(page)
  if (!(await panel.isVisible())) {
    await page.getByRole('button', { name: '打开 AI 助手' }).first().click({ timeout: 15_000 })
  }
  await expect(panel, '助手浮窗应可见').toBeVisible({ timeout: 30_000 })
  return panel
}

/**
 * 点浮窗头部的「新会话」，把当前会话切到 `null`。
 *
 * 这个动作是**串号缺陷的必然碰撞点**：草稿 key 的会话段此时是字面量 `new`。不带 owner 的
 * 旧实现里，任何两个用户的「新会话」草稿都落在同一个键上，必然互相回填；具体会话 id 因
 * 全局自增通常不重叠，`new` 这一条则 100% 重叠。两条用例都要先按它。
 *
 * 传入 page 而不是 locator：整页重载会把浮窗连带卸载，那时旧 locator 永远等不到元素，
 * 现场重开浮窗比让用例卡到超时更容易定位。点击给 15s 上限也是同一考虑——
 * 环境重载时应尽快失败并进入重试，而不是挂到整例超时。
 */
async function startNewConversation(page: Page): Promise<Locator> {
  const panel = await openAssistantDock(page)
  await panel.getByRole('button', { name: '新会话' }).click({ timeout: 15_000 })
  return panel
}

/**
 * 「本次测量因整页重载而作废」——**不是**产品失败。
 *
 * Vite dev 在重新预构建依赖、或 worker/HMR 重连时会整页 reload。那是一次真实的重载：
 * 组件树重建、浮窗状态丢失、模块级草稿缓存清空。它与本文件的两条验收前提直接冲突
 * （用例 1 要「不卸载」，用例 2 要「同一 JS 上下文」），但根因在 dev server 而非产品。
 *
 * 处理方式：把这次测量**作废并重做**，而不是把断言放宽或直接判绿。判据是客观事实
 * （是否真的发生了 document load），不是「断言没过就重试」——真实的产品失败照旧立刻抛出。
 */
class MeasurementInvalidated extends Error {}

/** 若测量窗口内发生过整页重载，作废本次测量。 */
function assertNoReload(loads: string[], when: string): void {
  if (loads.length > 0) {
    throw new MeasurementInvalidated(`${when}期间发生了整页重载：${JSON.stringify(loads)}`)
  }
}

/**
 * 执行一次测量；仅当「整页重载」破坏了前提时重做。
 *
 * @param attempt    测量主体；内部应在关键节点调用 `assertNoReload` 以便尽早退出
 * @param loads      整页重载采集器（`captureDocumentLoads` 的返回值）
 * @param maxAttempts 最多尝试次数（每次都会把采集器清零后重新开始）
 */
async function measureWithRetry(
  attempt: () => Promise<void>,
  loads: string[],
  maxAttempts = 3,
): Promise<void> {
  for (let i = 1; i <= maxAttempts; i++) {
    loads.length = 0
    try {
      await attempt()
      return
    } catch (err) {
      // 只有「整页重载」这一客观事实能作废测量；其余错误（含断言失败）原样抛出。
      const invalidated = err instanceof MeasurementInvalidated || loads.length > 0
      if (!invalidated) throw err
      if (i === maxAttempts) {
        throw new Error(
          `连续 ${maxAttempts} 次测量都因整页重载中断，无法得到有效结论（dev server 侧行为）：` +
            `${err instanceof Error ? err.message : String(err)}`,
          { cause: err },
        )
      }
      // 明示出来，避免「重试掩盖了环境问题」这类隐性代价：console 之外也记进报告注解，
      // 这样从 HTML 报告/CI 日志里能直接看到「这一例是靠重试才通过的」。
      const reason = err instanceof Error ? err.message : String(err)
      console.warn(`[e2e] 第 ${i}/${maxAttempts} 次测量前提被整页重载破坏，重做。原因：${reason}`)
      test.info().annotations.push({
        type: 'measurement-retry',
        description: `第 ${i} 次测量作废（整页重载）：${reason}`,
      })
    }
  }
}

/**
 * 采集发往 `/assistant/conversations/:id/runs` 的 POST。
 *
 * 必须在 `login` 之前挂上监听：要守的那次「额外重提」发生在导航之后，晚挂就会漏掉。
 */
function captureRunSubmissions(page: Page): string[] {
  const urls: string[] = []
  page.on('request', (req) => {
    if (req.method() !== 'POST') return
    let pathname: string
    try {
      pathname = new URL(req.url()).pathname
    } catch {
      return
    }
    // 只认「提交一轮」，不认 cancel / rollback 等同前缀路径。
    if (!/\/assistant\/conversations\/\d+\/runs$/.test(pathname)) return
    urls.push(pathname)
  })
  return urls
}

/**
 * 采集主文档的整页加载事件（= 与 SPA 路由切换相对立的「整页重载」）。
 *
 * 两条用例都靠它自证前提成立：
 *  - 用例 1 要的是「点链接切路由」，整页重载会把浮窗卸载、把结论换成「刷新后恢复」；
 *  - 用例 2 要的是「同一 JS 上下文里换人」，整页重载会把模块级草稿缓存一起清掉。
 * 前提被破坏时应当红，而不是安静地变成一个更弱的测试。
 */
function captureDocumentLoads(page: Page): string[] {
  const paths: string[] = []
  page.on('load', () => {
    try {
      paths.push(new URL(page.url()).pathname)
    } catch {
      paths.push('<unknown>')
    }
  })
  return paths
}

/**
 * 等外壳「稳定」：连续 700ms 没有被整页重载替换掉。
 *
 * 【为什么需要这个】Vite dev 在**首次**访问某路由的分包时，若该分包引入了尚未预构建的依赖，
 * 会重新预构建并整页 reload。那是一次真实重载：组件树重建、浮窗状态丢失、模块级缓存清空。
 * 它是 dev server 的行为，与产品无关，却正好落在断言之间制造假红（实测踩过一次：
 * 切到 /instances 时整个外壳连同浮窗消失，看起来像「路由切换卸载了浮窗」）。
 *
 * 做法：给当前外壳打个探针属性，等一会儿再看探针还在不在；不在就是被重载换掉了，重试。
 * 预热结束前的这些重载不计入测量区间（调用方在预热后清空 `loads`）。
 */
async function waitForStableShell(page: Page): Promise<void> {
  for (let attempt = 0; attempt < 8; attempt++) {
    const stable = await page.evaluate(async () => {
      const shell = document.querySelector('[data-slot="console-shell"]')
      if (!shell) return false
      shell.setAttribute('data-e2e-shell-probe', '1')
      await new Promise((resolve) => setTimeout(resolve, 700))
      return document.querySelector('[data-slot="console-shell"][data-e2e-shell-probe="1"]') !== null
    })
    if (stable) return
    await page.waitForTimeout(500)
  }
  throw new Error('控制台外壳始终在被整页重载替换，无法开始测量（dev server 状态异常？）')
}

/**
 * 走一遍 `/instances` → `/logs` → `/`，**全部用点链接/切工作区的 SPA 导航**。
 *
 * 特别不能用 `page.goto`：那是整页加载，会把浮窗卸载、把 JS 上下文重建，
 * 于是「跨路由不丢会话」就退化成「刷新后从 sessionStorage 恢复」，与该验收项无关。
 * 侧栏文案会随 i18n 与计数变化，故按 `href` 定位（语义属性，不是 class）。
 */
async function navigateInstancesLogsHome(page: Page): Promise<void> {
  const sidebar = page.locator('[data-slot="console-sidebar"]')
  const workspaces = page.locator('[data-slot="top-nav-workspaces"]')

  // 每步点击都给 15s 上限：环境整页重载时元素会反复 detach，不设上限会挂到整例超时，
  // 既看不出原因、也轮不到重试机制生效。
  await sidebar.locator('a[href="/instances"]').first().click({ timeout: 15_000 })
  await expect(page, '应已通过侧栏进入实例列表页').toHaveURL(/\/instances$/, { timeout: 30_000 })
  // 页面自身的就绪标记：只断言 URL 会把「路由变了但页面没渲染」也判成成功。
  await expect(
    page.locator('[data-page="instances"]'),
    '切到 /instances 时该页内容应真的渲染出来',
  ).toHaveCount(1, { timeout: 30_000 })

  // 日志中心属「观测与自动化」工作区：切工作区会 SPA 导航到该区落地页，仍非整页刷新。
  await workspaces.getByRole('button', { name: '观测与自动化' }).click({ timeout: 15_000 })
  await sidebar.locator('a[href="/logs"]').first().click({ timeout: 15_000 })
  await expect(page, '应已通过侧栏进入日志中心').toHaveURL(/\/logs$/, { timeout: 30_000 })
  await expect(
    page.locator('[data-page="logs"]'),
    '切到 /logs 时该页内容应真的渲染出来',
  ).toHaveCount(1, { timeout: 30_000 })

  // 切回「服务器运维」工作区 = 导航到该区落地页 `/`。
  await workspaces.getByRole('button', { name: '服务器运维' }).click({ timeout: 15_000 })
  await expect(page, '应已回到平台首页').toHaveURL(/\/$/, { timeout: 30_000 })
}

/** 真实登出（与 `users-groups.spec.ts` 同口径：账户 → 退出登录 → 回到登录页）。 */
async function logout(page: Page): Promise<void> {
  await page.getByRole('button', { name: '账户' }).click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await expect(
    page.getByRole('button', { name: '登录', exact: true }),
    '登出后应回到登录页',
  ).toBeVisible({ timeout: 30_000 })
}

/**
 * 在登录页**就地**登录，不做整页导航。
 *
 * 与 `helpers.login()` 的唯一区别正是本文件要守的那一点：`login()` 会先 `page.goto('/login')`，
 * 那是整页加载，会把模块级草稿缓存清掉。就地登录保留同一 JS 上下文，
 * 才验证得到 `resetDraftScope()` 是否真的尽到责任。
 */
async function loginInPlace(page: Page, username: string, password: string): Promise<void> {
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(
    page.locator('[data-slot="console-shell"]'),
    `以 ${username} 登录后应进入控制台外壳`,
  ).toBeVisible({ timeout: 30_000 })
}

/**
 * 当前登录用户的 id（从 access token 的 payload 读，不验签——只用于判定存储键归属）。
 *
 * 不写死「admin 是 1、operator 是 2」：那是 devmock 的种子细节，写死会让用例在种子调整后
 * 以「断言 userId 不对」的名义失败，掩盖真正的串号结论。
 */
async function currentUserId(page: Page): Promise<number> {
  const id = await page.evaluate(() => {
    const token = localStorage.getItem('accessToken')
    if (!token) return 0
    const segment = token.split('.')[1] ?? ''
    const normalized = segment.replace(/-/g, '+').replace(/_/g, '/')
    const padded = normalized.padEnd(normalized.length + ((4 - (normalized.length % 4)) % 4), '=')
    try {
      const claims = JSON.parse(atob(padded)) as { userId?: number }
      return Number(claims.userId ?? 0)
    } catch {
      return 0
    }
  })
  expect(id, '当前登录用户应能从 access token 解出 userId（否则存储键归属无从判定）').toBeGreaterThan(0)
  return id
}

/** 读出 sessionStorage 里全部助手草稿键（key 形如 `jm-assistant-draft:<owner>:<会话>`）。 */
async function draftKeys(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const keys: string[] = []
    for (let i = 0; i < sessionStorage.length; i++) {
      const key = sessionStorage.key(i)
      if (key && key.startsWith('jm-assistant-draft:')) keys.push(key)
    }
    return keys
  })
}

/** 草稿键的归属段（第二段）。用于在不写死 userId 的前提下判定「这条键属于谁」。 */
function draftOwnerOf(key: string): string {
  return key.split(':')[1] ?? ''
}

test.describe.configure({ timeout: 240_000 })

test.describe('FR-519 助手会话隔离', () => {
  test('跨路由不丢会话：浮窗、消息、草稿都在，且不重新提交 run', async ({ page }) => {
    const runSubmissions = captureRunSubmissions(page)
    const documentLoads = captureDocumentLoads(page)

    await loginAs(page, 'admin', 'admin123')
    await waitForStableShell(page)
    // 预热：先把三个路由各走一遍，把「dev server 首次访问某分包触发的整页重载」做掉，
    // 免得它落在测量区间里。（预热期间浮窗还没开，状态损失无妨。）
    await navigateInstancesLogsHome(page)

    const question = '切路由前发出的问题-9d21'
    const draftText = '未发送的草稿-切路由后应该还在-3c7e'

    await measureWithRetry(async () => {
      // 每次测量都从同一个确定起点开始：登录态下的 `/`、默认工作区、浮窗未开。
      // 上一次测量若被环境重载打断，可能停在 /logs 或别的中间态；不归位会让重试本身
      // 变成另一个场景（例如在「观测与自动化」侧栏里找不到 /instances 入口）。
      await page.goto('/')
      await waitForStableShell(page)
      // 这次归位用的整页加载不属于测量，从计数里剔除。
      documentLoads.length = 0

      // ── 建立会话：发一轮、再打一句不发的草稿 ──
      assertNoReload(documentLoads, '建立会话阶段')

      await openAssistantDock(page)
      await startNewConversation(page)
      assertNoReload(documentLoads, '开浮窗并切新会话时')

      const panel = dockOf(page)
      await sendRound(panel, question)
      assertNoReload(documentLoads, '发送并等待回复时')

      await expect(panel.getByRole('log'), '本轮问答应已就位（测量前的基准）').toContainText(
        `${MOCK_REPLY_MARK}${question}`,
      )

      const input = panel.getByLabel('输入消息')
      await input.fill(draftText)
      await expect(input, '草稿应先写进输入框（否则后面的「还在」无从谈起）').toHaveValue(draftText)

      // ── 测量：真的点链接切路由 ──
      await navigateInstancesLogsHome(page)
      // 三条「没丢」的断言之前先确认前提：这一段必须是 SPA 路由切换，不能是整页重载。
      assertNoReload(documentLoads, '切 /instances → /logs → / 时')

      // ── 三条「没丢」的断言 ──
      const afterNav = await openAssistantDock(page)

      await expect(
        afterNav.getByRole('log'),
        '切了 /instances → /logs → / 之后，上一轮问答仍应可见（会话未被清空）',
      ).toContainText(`${MOCK_REPLY_MARK}${question}`, { timeout: 30_000 })

      await expect(
        afterNav.getByLabel('输入消息'),
        '切路由前打的未发送草稿应仍在输入框里（草稿按会话保存，不随路由丢失）',
      ).toHaveValue(draftText)

      // ── 网络断言：整个过程只应发生 1 次「提交一轮」──
      //
      // 守的是「切路由不会把上一轮重新提交」。若实现改成「重新挂载就重放当前 run」，
      // 界面上看不出异常（内容一样），但每次切页都白跑一轮、多一条回复、多一次计费。
      expect(
        runSubmissions.length,
        `切路由过程中不应出现额外的「提交一轮」请求——本次共抓到 ${runSubmissions.length} 次：` +
          `${JSON.stringify(runSubmissions)}，期望恰好 1 次（就是上面那一轮）`,
      ).toBe(1)
    }, documentLoads)
  })

  test('换用户不串号：登出再登录后看不到上一个用户的未发送草稿', async ({ page }) => {
    const adminDraft = 'A的私密草稿-不要外泄-7f3a'
    const operatorDraft = 'B的私密草稿-不要外泄-91c2'
    const documentLoads = captureDocumentLoads(page)

    // 预热：先登一次，等 mock 后端接管、首页分包就绪。真正的测量在下面从干净起点重来。
    await loginAs(page, 'admin', 'admin123')
    await waitForStableShell(page)

    await measureWithRetry(async () => {
      // 每次测量都从同一个确定起点开始：admin 刚登录、浮窗未开、无残留草稿。
      // 上一次测量若被环境重载打断，可能停在 operator 登录态；不归位的话，
      // 「admin 写草稿 → 登出 → operator 登录」这条链就不再是从 admin 起步。
      await loginAs(page, 'admin', 'admin123')
      // 这次归位用的整页加载不属于测量，从计数里剔除。
      documentLoads.length = 0

      // ── 第一程：admin 写草稿 → 登出 → operator 登录 ──
      assertNoReload(documentLoads, '整轮换人测量开始前')

      const adminId = await currentUserId(page)
      await startNewConversation(page)
      const adminPanel = await openAssistantDock(page)
      await adminPanel.getByLabel('输入消息').fill(adminDraft)
      await expect(adminPanel.getByLabel('输入消息')).toHaveValue(adminDraft)

      await logout(page)
      // 登出必须是 SPA 内的状态变化：整页重载会把模块级草稿缓存一起清掉，
      // 那样后面「没串号」的结论就没验证到那条最容易漏的路径。
      assertNoReload(documentLoads, 'admin 登出时')

      // 登出即释放：sessionStorage 里不该再留着上一个用户作用域的草稿键。
      // 这条独立于「输入框看不到」——即使 key 已带 owner（读不到了），把别人的未发送内容
      // 长期留在浏览器里仍是不该发生的泄漏。
      const adminOwnedKeys = (await draftKeys(page)).filter(
        (k) => draftOwnerOf(k) === String(adminId),
      )
      expect(
        adminOwnedKeys,
        '登出后应删掉上一个用户作用域的草稿键（sessionStorage 不随登出自动消失，' +
          `必须由 logout 走的 resetDraftScope 清掉）；残留：${JSON.stringify(adminOwnedKeys)}`,
      ).toEqual([])

      await loginInPlace(page, 'operator', 'op123456')
      assertNoReload(documentLoads, 'operator 就地登录时')

      const operatorId = await currentUserId(page)
      expect(
        operatorId,
        '前提校验：换的必须是另一个账号，否则后面的「不串号」断言恒真、等于没测',
      ).not.toBe(adminId)

      const operatorPanel = await openAssistantDock(page)
      // 默认视图也要看：这条挡的是「会话 id 之外还有别的泄漏路径」（如共享的模块级缓存）。
      await expect(
        operatorPanel.getByLabel('输入消息'),
        '换用户后不能看到上一个用户的未发送草稿（默认会话视图）',
      ).not.toHaveValue(adminDraft)

      // 「新会话」是必然碰撞点：两个用户这一档草稿 key 的会话段都是字面量 `new`。
      await startNewConversation(page)
      await expect(
        operatorPanel.getByLabel('输入消息'),
        '换用户后不能看到上一个用户的未发送草稿：owner 段缺失时，' +
          'admin 在「新会话」里打的字会原样回填到 operator 的输入框',
      ).toHaveValue('')

      // ── 第二程（反向）：operator 写草稿 → 登出 → admin 登录 ──
      //
      // 反向不是凑数：正向只证明「admin → operator 这一对」不漏，但缺陷若出在 owner 的
      // 取值/切换时机上（例如只在登录时设置、登出没复位），往往只在一个方向上暴露。
      await operatorPanel.getByLabel('输入消息').fill(operatorDraft)
      await expect(operatorPanel.getByLabel('输入消息')).toHaveValue(operatorDraft)

      await logout(page)
      assertNoReload(documentLoads, 'operator 登出时')

      const operatorOwnedKeys = (await draftKeys(page)).filter(
        (k) => draftOwnerOf(k) === String(operatorId),
      )
      expect(
        operatorOwnedKeys,
        `反向：operator 登出后也不该留下其作用域的草稿键；残留：${JSON.stringify(operatorOwnedKeys)}`,
      ).toEqual([])

      await loginInPlace(page, 'admin', 'admin123')
      assertNoReload(documentLoads, 'admin 就地重新登录时')

      const adminPanelAgain = await openAssistantDock(page)
      await expect(
        adminPanelAgain.getByLabel('输入消息'),
        '反向：admin 重新登录后不能看到 operator 的未发送草稿（默认会话视图）',
      ).not.toHaveValue(operatorDraft)

      await startNewConversation(page)
      await expect(
        adminPanelAgain.getByLabel('输入消息'),
        '反向：admin 重新登录后不能看到 operator 的未发送草稿（「新会话」视图）',
      ).toHaveValue('')

      // 收尾：storage 里也不该出现 operator 作用域的残留键。
      const leftovers = (await draftKeys(page)).filter(
        (k) => draftOwnerOf(k) === String(operatorId),
      )
      expect(
        leftovers,
        `两程跑完后，sessionStorage 里不该残留 operator 作用域的草稿键；残留：${JSON.stringify(leftovers)}`,
      ).toEqual([])
    }, documentLoads)
  })
})
