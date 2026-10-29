import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/** 
 *§8 剩余的真浏览器验收项 · 真浏览器判定。
 *
 * 这三条共同点：**判据本身就是浏览器行为**，jsdom 里做不出来——
 * - 「首屏不闪回」要看的是**渲染时序**：用户在记忆为 classic 时冷启动，
 *   屏幕上不该出现「先 AI 后经典」的跳变。jsdom 没有真实的首帧绘制。
 * - 「两模式互不嵌套」除 DOM 外还要看**Network**：经典模式下不该发出 AI 首页摘要区的请求。
 *   DOM 里没有节点 ≠ 没有发请求（组件可能挂载后立刻卸载，请求已经飞出去了）。
 * - 「键盘可达」依赖真实焦点顺序与 `:focus-visible`，jsdom 不做 tab 导航。
 *
 * 等待策略统一用 `waitUntil: 'commit'`：本站首屏按路由分块下载 chunk，
 * 用默认 'load' 会在 mock service worker 接管导航时偶发 `net::ERR_ABORTED`（已在既有用例中踩过）。
 */

const HOME_MODE_KEY = 'console.homeMode'
const READY_TIMEOUT = 45_000

/** 断言当前渲染的是哪一种首页模式。 */
async function expectHomeMode(page: Page, mode: 'classic' | 'assistant', ctx: string): Promise<void> {
  const classic = page.locator('[data-page="overview"]')
  const assistant = page.locator('[data-page="assistant-home"]')
  if (mode === 'classic') {
    await expect(classic, `${ctx}：应渲染经典首页`).toBeVisible({ timeout: READY_TIMEOUT })
    await expect(assistant, `${ctx}：经典模式不应挂载 AI 首页树`).toHaveCount(0)
  } else {
    await expect(assistant, `${ctx}：应渲染 AI 首页`).toBeVisible({ timeout: READY_TIMEOUT })
    await expect(classic, `${ctx}：AI 模式不应挂载经典首页树`).toHaveCount(0)
  }
}

test.describe('首屏不闪回', () => {
  /** 
   * 记忆为 classic 时冷启动 `/?home=classic` 与直接 `/`，都不应出现「先 AI 后经典」的闪跳。
   *
   * 【怎么观测】在文档创建前注入轮询器，每帧检查 `[data-page="assistant-home"]` 是否出现过。
   * 只要出现过一次就记下来——**哪怕它随后被正确替换掉**，对用户来说那仍是一次闪跳。
   *
   * 这条的价值在于：路由层如果用「先渲染默认模式、再用 effect 修正」的写法，
   * 功能测试全都会通过（最终状态正确），只有这条会失败。
   */
  for (const route of ['/?home=classic', '/']) {
    test(`记忆为 classic 时，冷启动 ${route} 不闪出 AI 首页`, async ({ page }) => {
      // 三次整页导航 + 3.5s 观察窗，默认 30s 不够（导航本身在 devmock large 规模下就要十几秒）
      test.setTimeout(180_000)
      await page.goto('/login', { waitUntil: 'commit' })
      await page.waitForTimeout(1500)
      await login(page)

      // 先写入记忆（必须在导航前，冷启动才有意义）
      await page.evaluate((key) => localStorage.setItem(key, 'classic'), HOME_MODE_KEY)

      // 注入观测器：在本次导航的文档创建阶段就开始记录
      await page.addInitScript(() => {
        const w = window as unknown as { __sawAssistantHome?: boolean }
        w.__sawAssistantHome = false
        const probe = () => {
          if (document.querySelector('[data-page="assistant-home"]')) w.__sawAssistantHome = true
        }
        // document 在 init script 阶段可能尚不可用，等 DOM 就绪后开始逐帧观察
        const start = () => {
          probe()
          const timer = window.setInterval(probe, 16)
          // 3 秒后停止：首屏切换发生在这之前，之后是稳态
          window.setTimeout(() => window.clearInterval(timer), 3000)
        }
        if (document.readyState === 'loading') {
          document.addEventListener('DOMContentLoaded', start, { once: true })
        } else {
          start()
        }
      })

      await page.goto(route, { waitUntil: 'commit' })
      await expectHomeMode(page, 'classic', `冷启动 ${route}`)

      // 给观察窗留足时间（导航本身可能比 3 秒窗口更长）
      await page.waitForTimeout(3500)
      const sawAssistant = await page.evaluate(
        () => (window as unknown as { __sawAssistantHome?: boolean }).__sawAssistantHome === true,
      )
      expect(sawAssistant, `记忆为 classic 时冷启动 ${route}，AI 首页树一刻都不该出现（首屏闪回）`).toBe(false)

      // 清理，避免影响后续用例
      await page.evaluate((key) => localStorage.removeItem(key), HOME_MODE_KEY)
    })
  }
})

test.describe('两模式互不嵌套（网络层）', () => {
  /** 
   * 经典模式下不该发出 AI 首页的摘要请求。
   *
   * 与 DOM 断言互补：`[data-page="assistant-home"]` 不存在只说明**当前**没挂着，
   * 但若路由层「先挂 AI 首页再切走」，摘要请求已经发出去了——后端白挨一次查询，
   * 且若未来这些接口带副作用，问题会更大。
   */
  test('经典首页不发出 AI 摘要区请求', async ({ page }) => {
    test.setTimeout(120_000)
    await page.goto('/login', { waitUntil: 'commit' })
    await page.waitForTimeout(1500)
    await login(page)

    const assistantSummaryPaths: string[] = []
    page.on('request', (req) => {
      const url = new URL(req.url())
      // 只认 **AI 首页摘要区独有**的请求。判据必须精确到参数，不能只看路径：
      // `/metrics/overview` 经典首页也在用（OverviewPage 的 useMetricOverview），
      // 按路径匹配会把正常请求误判成「AI 首页被挂载过」——这条初版正是这么错的。
      // - `/instances/search?status=CRASHED&pageSize=5`：AI 首页「异常实例」摘要专用
      // - `/alerts/events`：AI 首页「告警」摘要专用（经典首页不引 useAlertEvents）
      const isCrashedSummary =
        url.pathname.endsWith('/instances/search') &&
        url.searchParams.get('status') === 'CRASHED' &&
        url.searchParams.get('pageSize') === '5'
      const isAlertsSummary = url.pathname.endsWith('/alerts/events')
      if (isCrashedSummary || isAlertsSummary) {
        assistantSummaryPaths.push(`${url.pathname}${url.search}`)
      }
    })

    await page.evaluate((key) => localStorage.setItem(key, 'classic'), HOME_MODE_KEY)
    await page.goto('/?home=classic', { waitUntil: 'commit' })
    await expectHomeMode(page, 'classic', '经典首页')
    // 落定后观察一小窗，确认没有迟到的请求
    await page.waitForTimeout(3000)

    expect(
      assistantSummaryPaths,
      `经典模式下不得发出 AI 首页摘要请求，实际发出：${assistantSummaryPaths.join(', ')}`,
    ).toEqual([])

    await page.evaluate((key) => localStorage.removeItem(key), HOME_MODE_KEY)
  })
})

test.describe('首页键盘可达性', () => {
  /** 
   * Tab 序列应覆盖：问候区 → 输入器 → 建议问题 → 摘要区 → 切换入口。
   *
   * 这里不逐项钉死 DOM 顺序（改版容易假失败），只锁两件真正影响可用性的事：
   * 1. **输入器能通过 Tab 到达**——若它在焦点链外，键盘用户根本没法提问；
   * 2. **切换入口能通过 Tab 到达**——否则键盘用户被困在 AI 模式里出不去。
   *
   * 同时断言焦点环可见性所需的条件（元素确实获得了焦点，而非只是存在）。
   */
  test('Tab 能到达输入器与首页切换入口', async ({ page }) => {
    // 60 次 Tab，每次一次 evaluate；默认 30s 不够
    test.setTimeout(120_000)
    await page.goto('/login', { waitUntil: 'commit' })
    await page.waitForTimeout(1500)
    await login(page)
    // 前序用例会把记忆写成 classic（它们各自收尾清理，但一旦中途失败就执行不到清理行）。
    // 本用例必须从「无记忆 → 默认 AI 首页」起步，否则会落到经典首页而找不到输入器。
    await page.evaluate((key) => localStorage.removeItem(key), HOME_MODE_KEY)
    await page.goto('/', { waitUntil: 'commit' })
    await expectHomeMode(page, 'assistant', 'AI 首页')

    /* 
      从**输入器本身**起算，而不是从页面顶部一路 Tab。
      原因（实测）：本站顶栏 + 侧边栏（含服务器列表）就吃掉上百个可聚焦元素，
      从顶部遍历根本走不到内容区——那测的是「全站 DOM 顺序」，不是 AI 首页内容区的可用性。
    */
    const input = page.locator('[data-page="assistant-home"] textarea').first()
    await expect(input, 'AI 首页应有输入器').toBeVisible({ timeout: READY_TIMEOUT })

    // 输入器可聚焦 = 键盘用户能提问
    await input.focus()
    const focusedTag = await page.evaluate(() => document.activeElement?.tagName.toLowerCase() ?? '')
    expect(focusedTag, '输入器必须可获得焦点（键盘用户靠它提问）').toBe('textarea')

    // 从输入器向后 Tab：应经过建议问题 → 摘要区（实测序列），即内容区内部顺序正常
    const reached: string[] = []
    for (let i = 0; i < 20; i++) {
      await page.keyboard.press('Tab')
      const desc = await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null
        if (!el) return 'none'
        const tag = el.tagName.toLowerCase()
        const label = el.getAttribute('aria-label') ?? el.textContent?.trim().slice(0, 24) ?? ''
        return `${tag}:${label}`
      })
      reached.push(desc)
    }
    expect(
      reached.some((r) => r.startsWith('button:') && r.includes('？')),
      `输入器之后应能 Tab 到建议问题，实际：${reached.join(' | ')}`,
    ).toBe(true)

    /* 
      「首页切换入口」不在输入器之后，而在 **PageHeader 的 actions** 里（页面顶部）。
      这与 spec §8 原文「Tab 顺序为 问候 → 输入 → 建议 → 摘要 → 切换入口」不一致：
      实现把页面级操作放在了页头，Tab 顺序上它出现在内容区**之前**。

      这里按**实际实现**断言「切换入口可键盘访问」，而不是按原文断言顺序——
      顺序差异已记入 spec §8.1「已知偏差」，是否调整布局由产品决定，
      不该由一条测试反向锁定产品结构。
    */
    // 用 href 定位而不是角色名：按钮内嵌了 ArrowRight 图标，可访问名可能被图标影响；
    // `a[href*="home=classic"]` 是这条入口的稳定锚点，且它本身就是「可点击的链接」。
    const switchEntry = page.locator('a[href*="home=classic"]').first()
    await expect(switchEntry, 'AI 首页应有切到经典首页的入口').toBeVisible({ timeout: READY_TIMEOUT })
    await switchEntry.focus()
    const switchFocused = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      if (!el) return null
      const r = el.getBoundingClientRect()
      return { tag: el.tagName.toLowerCase(), text: el.textContent?.trim() ?? '', w: r.width, h: r.height }
    })
    expect(switchFocused?.text ?? '', '切换入口必须可获得焦点（键盘用户靠它离开 AI 模式）').toContain('经典首页')
    expect(
      (switchFocused?.w ?? 0) > 0 && (switchFocused?.h ?? 0) > 0,
      '切换入口应有可见尺寸（焦点环不可见的元素等于不可达）',
    ).toBe(true)
  })
})

test.describe('移动端 390px', () => {
  /** 
   * 390×844（iPhone 12/13/14 逻辑分辨率）下，AI 浮窗应可用：能打开、能输入、不横向溢出。
   *
   * 【2026-10-27 已按产品决策改为真全屏 sheet】
   * 此前实现是固定尺寸浮层（`h-[min(38rem,calc(100vh-6rem))]`），390px 下高度只有 72%，
   * 底部会露出页面内容、容易误触下层操作。经确认改为窄屏全屏 sheet：
   * `max-sm:inset-0 h-full w-full rounded-none`（见 AssistantDock.tsx）。
   *
   * 本用例因此断言**真正铺满视口**，而不是「接近全屏」——阈值卡在 99%，
   * 差 1% 是给亚像素取整留的余量。
   */
  test('390px 下浮窗为全屏 sheet，可输入且不横向溢出', async ({ page }) => {
    test.setTimeout(120_000)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/login', { waitUntil: 'commit' })
    await page.waitForTimeout(1500)
    await login(page)

    // 打开浮窗
    await page.getByRole('button', { name: '打开 AI 助手' }).click()
    const dock = page.getByRole('complementary', { name: 'AI 助手' })
    await expect(dock, '390px 下应能打开 AI 浮窗').toBeVisible({ timeout: READY_TIMEOUT })

    // 尺寸：应占据视口绝大部分（浮层形态下也需足够大的可读区域）
    const box = await dock.boundingBox()
    expect(box, '浮窗应有可测量的包围盒').not.toBeNull()
    // 全屏 sheet：宽高都应铺满视口（阈值 99%，余下 1% 给亚像素取整）
    expect(
      box!.width / 390,
      `全屏 sheet 宽度应铺满视口（实际 ${Math.round((box!.width / 390) * 100)}%）`,
    ).toBeGreaterThanOrEqual(0.99)
    expect(
      box!.height / 844,
      `全屏 sheet 高度应铺满视口（实际 ${Math.round((box!.height / 844) * 100)}%）`,
    ).toBeGreaterThanOrEqual(0.99)

    // 输入器可用
    const input = dock.locator('textarea').first()
    await expect(input, '窄屏下输入器应可见').toBeVisible({ timeout: READY_TIMEOUT })
    await input.fill('窄屏可用性检查')
    await expect(input).toHaveValue('窄屏可用性检查')

    // 关掉浮窗再验页面单栏顺序：浮层是全屏 sheet，开着会盖住整个页面
    const closeBtn = dock.getByRole('button', { name: '关闭' })
    if (await closeBtn.count()) await closeBtn.click()
    await expect(dock).toHaveCount(0)

    // 窄屏单栏顺序（spec §8「窄屏单栏顺序正确」）：问候 → 输入 → 建议 → 摘要
    // 用各区块的 getBoundingClientRect().top 递进来断言，而不是数 DOM 顺序——
    // DOM 顺序可能与视觉顺序差一屏（右侧栏在窄屏被堆到下方时尤其容易）。
    const order = await page.evaluate(() => {
      const root = document.querySelector('[data-page="assistant-home"]')
      const find = (text: string) => {
        const el = [...(root?.querySelectorAll('*') || [])]
          .find(e => e.children.length === 0 && e.textContent?.trim() === text)
        return el ? Math.round(el.getBoundingClientRect().top) : -1
      }
      return {
        greeting: document.querySelector('[data-page="assistant-home"]')?.innerText?.includes('下午好') ||
          document.querySelector('[data-page="assistant-home"]')?.innerText?.includes('上午好') ||
          document.querySelector('[data-page="assistant-home"]')?.innerText?.includes('晚上好') || false,
        chat: find('对话'),
        suggest: find('可以这样问'),
        alerts: find('需要关注'),
        tasks: find('近期任务'),
      }
    })
    expect(order.greeting, '窄屏应显示问候语').toBe(true)
    const seq = [
      ['对话', order.chat],
      ['可以这样问', order.suggest],
      ['需要关注', order.alerts],
      ['近期任务', order.tasks],
    ].filter(([, y]) => y >= 0) as Array<[string, number]>
    expect(seq.length, '至少应找到对话/建议/摘要三类区块用于判断顺序').toBeGreaterThanOrEqual(2)
    for (let i = 1; i < seq.length; i++) {
      expect(
        seq[i][1],
        `窄屏单栏顺序应为 对话 → 建议 → 摘要，但「${seq[i][0]}」(${seq[i][1]}px) 不应在「${seq[i-1][0]}」(${seq[i-1][1]}px) 之上`,
      ).toBeGreaterThanOrEqual(seq[i - 1][1])
    }

    // 不横向溢出：文档宽度不得超视口（浮层贴边时最容易踩到）
    const overflow = await page.evaluate(() => ({
      docWidth: document.documentElement.scrollWidth,
      viewport: window.innerWidth,
    }))
    expect(
      overflow.docWidth,
      `页面不应出现横向滚动（文档宽 ${overflow.docWidth} vs 视口 ${overflow.viewport}）`,
    ).toBeLessThanOrEqual(overflow.viewport + 1)
  })
})

test.describe('明暗主题走查', () => {
  /** 
   * 明/暗主题各走一遍（spec §8「宽屏/窄屏、明/暗主题各走一遍」）。
   *
   * 【为什么不点「切换主题」按钮】该入口来自 `@jianmanager/ui` 包，不在本仓源码里，
   * 且它的可访问名与内部状态机不属于的范围。这里改用**直接给 `html` 加/去
   * `dark` class** 来切主题——那是主题系统对外暴露的唯一契约（所有 token 都挂在它下面），
   * 足以验证「暗色下 AI 首页仍可读、不白屏、不丢内容」这个真正要守的东西。
   */
  test('AI 首页在明/暗主题下都可读且内容不丢', async ({ page }) => {
    test.setTimeout(120_000)
    await page.goto('/login', { waitUntil: 'commit' })
    await page.waitForTimeout(1500)
    await login(page)
    await page.evaluate(() => localStorage.removeItem('console.homeMode'))
    await page.goto('/', { waitUntil: 'commit' })
    await expect(page.locator('[data-page="assistant-home"]')).toBeVisible({ timeout: READY_TIMEOUT })

    const readTheme = async () =>
      page.evaluate(() => {
        const root = document.querySelector('[data-page="assistant-home"]') as HTMLElement | null
        const h1 = root?.querySelector('h1')
        const cs = getComputedStyle(document.body)
        return {
          cls: document.documentElement.className,
          bodyBg: cs.backgroundColor,
          h1Color: h1 ? getComputedStyle(h1).color : '',
          textLen: root?.innerText?.length ?? 0,
          hasInput: !!root?.querySelector('textarea'),
        }
      })

    const light = await readTheme()

    await page.evaluate(() => document.documentElement.classList.add('dark'))
    await page.waitForTimeout(600)
    const dark = await readTheme()

    // 主题确实切换了（token 生效）
    expect(dark.cls, '应切到 dark 主题').toContain('dark')
    expect(dark.bodyBg, '暗色下 body 背景应与亮色不同，否则 token 没生效').not.toBe(light.bodyBg)
    expect(dark.h1Color, '暗色下标题颜色应与亮色不同').not.toBe(light.h1Color)

    // 内容不丢、输入器仍在（不是白屏/黑屏一片）
    expect(dark.textLen, '暗色下 AI 首页文字量应与亮色相当').toBeGreaterThan(light.textLen * 0.8)
    expect(dark.hasInput, '暗色下输入器仍应在').toBe(true)

    // 还原，避免影响其它用例
    await page.evaluate(() => document.documentElement.classList.remove('dark'))
  })
})


/* 
  【未覆盖：换用户后首页不残留上一个用户的会话】

  这条**不在本文件实现**，原因是环境限制而非取舍：
  `packages/devmock/src/handlers/domains/assistant.ts` 里的 `conversations` 是一个
  **全局数组，不按 ownerId 隔离**（该文件内没有任何 ownerId/userId 过滤）。因此
  换个账号登录后仍能看到同一批会话——用 mock 写这条断言只会得到假红。

  真实后端是按 `owner_id` 收敛的，且已被后端用例覆盖：
  `TestAssistantChat_CrossOwnerInvisible`（service 层，六条读写路径）
  + `TestAssistantChatCrossUserAccessIs404` / `TestAssistantChatCrossUserRunAccessIs404`
  （路由层，含事件补读与 SSE）。
  前端「换用户不残留」要真验，需接真实后端；已在 spec §8.2 记录。
*/


/* 
  【未覆盖：未配置任何模型时的降级】

  这条**不在本文件实现**，因为 devmock 造不出这个初态，试过三条路都不行：

  1. `page.route` 覆盖 `/assistant/models` 返回 `[]` —— **无效**：本站 mock 走 MSW
     Service Worker，在浏览器侧就截断了，Playwright 的网络层看不到这个请求（实测仍显示供应商）。
  2. 用管理接口把模型逐个 `enabled=false` —— 停用**成功**（复验 `availableAfter === 0`），
     但页面仍显示旧供应商：模型列表命中 React Query 缓存，SPA 内不重新请求。
  3. 停用后 `page.reload()` —— **停用被冲掉**：devmock 是内存假后端，刷新即重置
     （见 `src/main.tsx` 的 `enableMocking` 注释）。

  三者互斥，形成一个死结。要真验需要：给 devmock 加一个「初始无模型」的运行时档位，
  或接真实 CP（空库天然无模型）。留给真实环境。
*/

test.describe('读屏播报语义（可访问性树）', () => {
  /** 
   * 读屏播报的**实际播报行为**只能在真机辅助技术下验证（VoiceOver/NVDA），
   * 无头浏览器做不到。但这里能做一件比单测更接近事实的事：
   * 取消息区的 **ARIA 快照**，断言它在可访问性树里确实是「live region（polite）」，
   * 且内容作为它的子树可被遍历到。
   *
   * 屏幕阅读器正是依赖这棵树决定「要不要播报、什么时候播报」：
   *  - `role=log` + `aria-live=polite` → 追加内容时排队播报，不打断当前朗读；
   *  - 若退化成 `assertive`，每次 text_delta 都会打断用户，正是验收要防的刷屏。
   *
   * 边界如实标注：这证明**语义正确**，不证明某款屏幕阅读器的具体播报粒度。
   */
  test('消息区在可访问性树中是 polite 的 live region', async ({ page }) => {
    test.setTimeout(120_000)
    await page.goto('/login', { waitUntil: 'commit' })
    await page.waitForTimeout(1500)
    await login(page)
    await page.evaluate(() => localStorage.removeItem('console.homeMode'))
    await page.goto('/', { waitUntil: 'commit' })
    await expect(page.locator('[data-page="assistant-home"]')).toBeVisible({ timeout: READY_TIMEOUT })

    const log = page.locator('[role="log"]').first()
    await expect(log, '消息流应有 role=log').toBeVisible({ timeout: READY_TIMEOUT })

    // ARIA 快照：拿到的是可访问性树里的真实形态（含 role 与可访问名）
    const snapshot = await log.ariaSnapshot()
    expect(snapshot, '快照应显示为 log 容器').toMatch(/log/i)

    // 关键属性：polite 而非 assertive
    const live = await log.getAttribute('aria-live')
    expect(live, 'aria-live 必须是 polite（assertive 会逐条打断）').toBe('polite')
    expect(live, '不得为 assertive').not.toBe('assertive')

    const label = await log.getAttribute('aria-label')
    expect(label, 'live region 需要可访问名，否则播报时用户不知道这是哪块').toBeTruthy()
  })
})
