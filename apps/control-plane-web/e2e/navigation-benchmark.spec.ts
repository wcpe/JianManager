import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

const ROUTE_SWITCH_BUDGET_MS = 2500

interface BenchRoute {
  label: string
  href: string
  readySelector: string
  virtual?: {
    surfaceSelector: string
    itemSelector: string
    minTotal: number
    maxRendered: number
  }
}

const ROUTES: BenchRoute[] = [
  {
    label: '平台首页',
    href: '/',
    readySelector: '[data-page="overview"]',
    virtual: {
      surfaceSelector: '[data-testid="overview-instances-virtual"]',
      itemSelector: '[data-testid="overview-instance-row"]',
      minTotal: 1000,
      maxRendered: 80,
    },
  },
  {
    label: '全部服务器',
    href: '/instances',
    readySelector: '[data-page="instances"]',
    virtual: {
      surfaceSelector: '[data-testid="instances-card-virtual"]',
      itemSelector: '[data-testid="instances-card-virtual-item"]',
      minTotal: 1000,
      maxRendered: 80,
    },
  },
  { label: '网络拓扑', href: '/networks/topology', readySelector: '[data-page="networks"]' },
  { label: '群组管理', href: '/networks', readySelector: '[data-page="networks"]' },
  {
    label: '日志中心',
    href: '/logs',
    readySelector: '[data-page="logs"]',
    virtual: {
      surfaceSelector: '[data-testid="logs-virtual"]',
      itemSelector: '[data-testid="log-row"]',
      minTotal: 1000,
      maxRendered: 40,
    },
  },
  { label: '客户端分发安全', href: '/client-dist-security', readySelector: '[data-page="client-dist-security"]' },
]

const OVERVIEW_RESPONSIVE_VIEWPORTS = [
  { label: '移动端', width: 390, height: 844 },
  { label: '窄桌面端', width: 1024, height: 768 },
  { label: '桌面端', width: 1366, height: 768 },
]

const RESPONSIVE_TABLE_ROUTES: BenchRoute[] = [
  { label: '用户管理', href: '/users', readySelector: '[data-page="users"]' },
  {
    label: '客户端分发密钥',
    href: '/client-channels?channel=skyblock-s1&tab=keys',
    readySelector: '[data-page="client-channel-workbench"]',
  },
]

/** 重置会影响导航可见性的本地持久状态，避免 benchmark 被侧栏折叠态干扰。 */
async function resetConsoleLayout(page: Page): Promise<void> {
  await page.addInitScript(() => {
    localStorage.setItem('sidebar.collapsed', '0')
    localStorage.removeItem('sidebar.collapsedGroups')
  })
}

/** 断言 1000+ mock 数据页仍只渲染可视窗口，避免 benchmark 在 DOM 爆量时虚假通过。 */
async function expectVirtualRendering(page: Page, route: BenchRoute): Promise<void> {
  if (!route.virtual) return

  const surface = page.locator(route.virtual.surfaceSelector)
  await expect(surface, `${route.label} 虚拟渲染容器存在`).toBeVisible()
  await expect.poll(
    async () => Number(await surface.getAttribute('data-total-count')),
    { message: `${route.label} mock 数据量达到 1000+` },
  )
    .toBeGreaterThanOrEqual(route.virtual.minTotal)

  const renderedCount = await page.locator(route.virtual.itemSelector).count()
  expect(renderedCount, `${route.label} DOM 渲染项数量`).toBeLessThan(route.virtual.maxRendered)
}

/** 检查主工作区是否出现横向溢出。 */
async function expectNoWorkspaceOverflow(page: Page, label: string, pageSelector = '[data-page="overview"]'): Promise<void> {
  const overflow = await page.evaluate(() => {
    const workspace = document.querySelector('[data-overflow-probe="true"]')?.closest('.jm-workspace-bg') as HTMLElement | null
    if (!workspace) return 0
    return workspace.scrollWidth - workspace.clientWidth
  })
  expect(overflow, `${label} 主工作区横向溢出像素`).toBeLessThanOrEqual(2)
  await page.locator(pageSelector).evaluate((el) => el.removeAttribute('data-overflow-probe'))
}

/** 检查共享表格由自身容器承接横向滚动，不把滚动传导到主工作区。 */
async function expectTablesOwnHorizontalScroll(page: Page, label: string, pageSelector: string): Promise<void> {
  const result = await page.locator(pageSelector).evaluate((root) => {
    const workspace = root.closest('.jm-workspace-bg') as HTMLElement | null
    const workspaceScrollLeft = workspace?.scrollLeft ?? 0
    const violations: string[] = []
    const tables = Array.from(root.querySelectorAll<HTMLElement>('[data-slot="table"]'))

    tables.forEach((table, index) => {
      const container = table.parentElement
      if (!container || container.getAttribute('data-slot') !== 'table-container') {
        violations.push(`table[${index}] 缺少共享滚动容器`)
        return
      }

      const overflowX = getComputedStyle(container).overflowX
      if (overflowX !== 'auto' && overflowX !== 'scroll') {
        violations.push(`table[${index}] overflow-x=${overflowX}`)
        return
      }

      const maxScrollLeft = container.scrollWidth - container.clientWidth
      if (maxScrollLeft > 2) {
        container.scrollLeft = maxScrollLeft
        if (container.scrollLeft <= 0) violations.push(`table[${index}] 自身容器不可滚动`)
        if ((workspace?.scrollLeft ?? 0) !== workspaceScrollLeft) {
          violations.push(`table[${index}] 滚动传导到主工作区`)
        }
        container.scrollLeft = 0
      }
    })

    return { tableCount: tables.length, violations }
  })

  expect(result.tableCount, `${label} 关键表格存在`).toBeGreaterThan(0)
  expect(result.violations, `${label} 表格横向滚动归属`).toEqual([])
}

async function animationDurationMs(page: Page, selector: string): Promise<number> {
  return page.locator(selector).evaluate((el) => {
    const rawDuration = getComputedStyle(el).animationDuration.split(',')[0] ?? '0s'
    const durationMs = rawDuration.endsWith('ms') ? Number.parseFloat(rawDuration) : Number.parseFloat(rawDuration) * 1000
    return Number.isFinite(durationMs) ? durationMs : 0
  })
}

async function visibleAnimationDurationMs(page: Page, selector: string): Promise<number> {
  return page.locator(selector).evaluate((el) => {
    if (el.getAttribute('data-visible') !== 'true') return 0

    const style = getComputedStyle(el)
    if (style.animationName === 'none') return 0

    const rawDuration = style.animationDuration.split(',')[0] ?? '0s'
    const durationMs = rawDuration.endsWith('ms') ? Number.parseFloat(rawDuration) : Number.parseFloat(rawDuration) * 1000
    return Number.isFinite(durationMs) ? durationMs : 0
  })
}

async function sidebarFrameStats(page: Page): Promise<{
  maxFrameMs: number
  framesOver24: number
  sidebarTransition: string
  sidebarTransitionDurationMs: number
  drawerTransition: string
  drawerTransitionDurationMs: number
  drawerAnimationName: string
  expandedModeTransition: string
  collapsedModeTransition: string
  expandedIconTransition: string
  sidebarStartW: number
  sidebarMidW: number
  sidebarFinalW: number
  drawerStartW: number
  drawerMidW: number
  drawerFinalW: number
  contentTransition: string
  contentClipPath: string
  contentMidVisibleRight: number
  viewportWidth: number
  contentStartX: number
  contentMidX: number
  contentFinalX: number
}> {
  return page.evaluate(async () => {
    const sidebar = document.querySelector('[data-slot="console-sidebar"]') as HTMLElement | null
    const drawer = document.querySelector('[data-slot="sidebar-drawer"]') as HTMLElement | null
    const content = document.querySelector('[data-slot="console-content"]') as HTMLElement | null
    const expandedMode = document.querySelector('.jm-sidebar-mode[data-mode="expanded"]') as HTMLElement | null
    const collapsedMode = document.querySelector('.jm-sidebar-mode[data-mode="collapsed"]') as HTMLElement | null
    const expandedIcon = document.querySelector('.jm-sidebar-mode[data-mode="expanded"] .jm-nav-link-icon') as HTMLElement | null
    if (!sidebar || !drawer || !content) {
      return {
        maxFrameMs: 0,
        framesOver24: 0,
        sidebarTransition: 'none',
        sidebarTransitionDurationMs: 0,
        drawerTransition: 'none',
        drawerTransitionDurationMs: 0,
        drawerAnimationName: 'none',
        expandedModeTransition: 'none',
        collapsedModeTransition: 'none',
        expandedIconTransition: 'none',
        sidebarStartW: 0,
        sidebarMidW: 0,
        sidebarFinalW: 0,
        drawerStartW: 0,
        drawerMidW: 0,
        drawerFinalW: 0,
        contentTransition: 'none',
        contentClipPath: 'none',
        contentMidVisibleRight: 0,
        viewportWidth: 0,
        contentStartX: 0,
        contentMidX: 0,
        contentFinalX: 0,
      }
    }

    const toMs = (raw: string) => {
      const first = raw.split(',')[0]?.trim() ?? '0s'
      const value = Number.parseFloat(first)
      if (!Number.isFinite(value)) return 0
      return first.endsWith('ms') ? value : value * 1000
    }
    const clipRightPx = (raw: string) => {
      const match = raw.match(/^inset\((.+)\)$/)
      if (!match) return 0
      const parts = match[1].split(/\s+/)
      const right = parts[1] ?? parts[0] ?? '0px'
      const value = Number.parseFloat(right)
      return Number.isFinite(value) ? value : 0
    }

    const sidebarStartW = Math.round(sidebar.getBoundingClientRect().width)
    const drawerStartW = Math.round(drawer.getBoundingClientRect().width)
    const contentStartX = Math.round(content.getBoundingClientRect().x)
    const frames: number[] = []
    const drawerWidths: number[] = []
    const sidebarWidths: number[] = []
    const contentTransitions: string[] = []
    const contentClipPaths: string[] = []
    const contentXs: number[] = []
    const contentRights: number[] = []
    let last: number | null = null
    let done = false
    const tick = (now: number) => {
      if (last !== null) frames.push(now - last)
      last = now
      // 逐帧记录动画数据，避免慢速 CI 将固定时刻快照落到动画末态。
      drawerWidths.push(drawer.getBoundingClientRect().width)
      sidebarWidths.push(sidebar.getBoundingClientRect().width)
      const contentStyle = getComputedStyle(content)
      const contentRect = content.getBoundingClientRect()
      contentTransitions.push(contentStyle.transitionProperty)
      contentClipPaths.push(contentStyle.clipPath)
      contentXs.push(contentRect.x)
      contentRights.push(contentRect.right)
      if (!done) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
    await new Promise((resolve) => window.setTimeout(resolve, 180))
    const sidebarStyle = getComputedStyle(sidebar)
    const drawerStyle = getComputedStyle(drawer)
    const sidebarTransition = sidebarStyle.transitionProperty
    const sidebarTransitionDurationMs = toMs(sidebarStyle.transitionDuration)
    const drawerTransition = drawerStyle.transitionProperty
    const drawerTransitionDurationMs = toMs(drawerStyle.transitionDuration)
    const drawerAnimationName = drawerStyle.animationName
    const expandedModeTransition = expandedMode ? getComputedStyle(expandedMode).transitionProperty : 'none'
    const collapsedModeTransition = collapsedMode ? getComputedStyle(collapsedMode).transitionProperty : 'none'
    const expandedIconTransition = expandedIcon ? getComputedStyle(expandedIcon).transitionProperty : 'none'
    await new Promise((resolve) => window.setTimeout(resolve, 360))
    done = true

    // 从整段逐帧采样派生中段宽度（比固定时刻快照稳）：
    // drawer 取「过渡中」帧（严格介于观测最小/最大之间）的中位数——只要 rAF 采到过渡就稳落 (start,end) 开区间；
    // sidebar（aside 不做 width 过渡、宽度恒定）取采样众数，抹平点击瞬间可能的单帧亚像素抖动。
    const median = (xs: number[]) => xs.slice().sort((a, b) => a - b)[Math.floor(xs.length / 2)] ?? 0
    const mode = (xs: number[]) => {
      const counts = new Map<number, number>()
      let best = xs[0] ?? 0
      let bestN = 0
      for (const x of xs) {
        const n = (counts.get(x) ?? 0) + 1
        counts.set(x, n)
        if (n > bestN) {
          bestN = n
          best = x
        }
      }
      return best
    }
    const dMin = Math.min(drawerStartW, ...drawerWidths)
    const dMax = Math.max(drawerStartW, ...drawerWidths)
    // 「drawer 过渡中」的帧下标（drawer 宽度严格介于观测最小/最大之间）。
    const transitioningIdx: number[] = []
    drawerWidths.forEach((w, i) => {
      if (w > dMin + 1 && w < dMax - 1) transitioningIdx.push(i)
    })
    const drawerTransitioning = transitioningIdx.map((i) => drawerWidths[i])
    // sidebar 中段宽度取「drawer 过渡中」同帧的 aside 宽度：证「drawer 视觉过渡期间 aside 布局不随之重排」，
    // 避开整段众数会把动画结束后已落定的 aside 终宽计入（那不是「中段」）。
    const sidebarDuringTransition = transitioningIdx.map((i) => Math.round(sidebarWidths[i]))
    const sidebarMidW = sidebarDuringTransition.length ? mode(sidebarDuringTransition) : Math.round(sidebarStartW)
    const drawerMidW = Math.round(drawerTransitioning.length ? median(drawerTransitioning) : (dMin + dMax) / 2)
    // content 必须与 drawer 的真实过渡帧对齐；固定等待 180ms 在慢速 CI 上可能落到 idle，误读为 transition:none。
    const contentMidIdx = transitioningIdx[Math.floor(transitioningIdx.length / 2)] ?? 0
    const contentTransition = contentTransitions[contentMidIdx] ?? 'none'
    const contentClipPath = contentClipPaths[contentMidIdx] ?? 'none'
    const contentMidX = Math.round(contentXs[contentMidIdx] ?? contentStartX)
    const contentMidVisibleRight = Math.round((contentRights[contentMidIdx] ?? 0) - clipRightPx(contentClipPath))

    return {
      maxFrameMs: Math.round(Math.max(0, ...frames) * 10) / 10,
      framesOver24: frames.filter((value) => value > 24).length,
      sidebarTransition,
      sidebarTransitionDurationMs,
      drawerTransition,
      drawerTransitionDurationMs,
      drawerAnimationName,
      expandedModeTransition,
      collapsedModeTransition,
      expandedIconTransition,
      sidebarStartW,
      sidebarMidW,
      sidebarFinalW: Math.round(sidebar.getBoundingClientRect().width),
      drawerStartW,
      drawerMidW,
      drawerFinalW: Math.round(drawer.getBoundingClientRect().width),
      contentTransition,
      contentClipPath,
      contentMidVisibleRight,
      viewportWidth: window.innerWidth,
      contentStartX,
      contentMidX,
      contentFinalX: Math.round(content.getBoundingClientRect().x),
    }
  })
}

/** 关键页面的 SPA 路由切换 benchmark：记录单页切换耗时并设宽松预算，防止明显卡顿回归。 */
test.describe('页面切换 benchmark（mock 模式）', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ page }) => {
    await resetConsoleLayout(page)
    await login(page)
  })

  test('关键页面切换耗时不超过预算', async ({ page }) => {
    const results: Array<{ label: string; elapsedMs: number }> = []

    for (const route of ROUTES) {
      const link = page.locator(`a[href="${route.href}"]`).first()
      await expect(link, `${route.label} 导航入口存在`).toBeVisible()

      const started = await page.evaluate(() => performance.now())
      await link.click()
      await expect(page.locator(route.readySelector), `${route.label} 页面就绪`).toBeVisible()
      await expectVirtualRendering(page, route)
      await page.evaluate(() => new Promise(requestAnimationFrame))
      const ended = await page.evaluate(() => performance.now())
      const elapsedMs = Math.round(ended - started)

      results.push({ label: route.label, elapsedMs })
      expect(elapsedMs, `${route.label} 页面切换耗时`).toBeLessThan(ROUTE_SWITCH_BUDGET_MS)
    }

    const sorted = results.map((r) => r.elapsedMs).sort((a, b) => a - b)
    const p95 = sorted[Math.max(0, Math.ceil(sorted.length * 0.95) - 1)] ?? 0
    test.info().annotations.push({
      type: 'benchmark',
      description: JSON.stringify({ budgetMs: ROUTE_SWITCH_BUDGET_MS, p95Ms: p95, results }),
    })
  })

  test('全部服务器首屏只走分页端点，返回后恢复滚动并附截图', async ({ page }) => {
    await page.goto('/instances?status=RUNNING&pageSize=50')
    await expect(page.locator('[data-page="instances"]'), '全部服务器页就绪').toBeVisible()
    const surface = page.locator('[data-testid="instances-card-virtual"]')
    await expect(surface, '实例卡片虚拟列表存在').toBeVisible()
    const collectInstancePaths = () =>
      page.evaluate(() =>
        ((window as Window & { __jmApiRequestPaths?: string[] }).__jmApiRequestPaths ?? []).filter((path) => path.startsWith('/api/v1/instances')),
      )
    await expect.poll(
      async () => {
        const paths = await collectInstancePaths()
        return paths.includes('/api/v1/instances/search') && paths.includes('/api/v1/instances/aggregate')
      },
      { message: '首屏请求分页搜索与聚合端点' },
    ).toBe(true)
    const instancePaths = await collectInstancePaths()
    expect(instancePaths.filter((path) => path === '/api/v1/instances'), '首屏不请求裸实例全集').toHaveLength(0)

    await surface.evaluate((el) => {
      el.scrollTop = 352
      el.dispatchEvent(new Event('scroll', { bubbles: true }))
    })
    await expect.poll(async () => surface.evaluate((el) => Math.round(el.scrollTop)), { message: '滚动位置写入前置条件' }).toBe(352)
    await test.info().attach('instances-card-before-detail', {
      body: await page.screenshot(),
      contentType: 'image/png',
    })

    await page.goto('/instances/1')
    await expect(page.locator('[data-page="instance-console"]'), '实例详情深链就绪').toBeVisible()
    await page.goBack()
    await expect(page.locator('[data-page="instances"]'), '返回全部服务器页就绪').toBeVisible()
    const restored = page.locator('[data-testid="instances-card-virtual"]')
    await expect.poll(async () => restored.evaluate((el) => Math.round(el.scrollTop)), { message: '返回后恢复列表滚动位置' }).toBeGreaterThanOrEqual(300)
    await test.info().attach('instances-card-after-back', {
      body: await page.screenshot(),
      contentType: 'image/png',
    })
  })

  test('平台首页响应式不产生横向溢出', async ({ page }) => {
    for (const viewport of OVERVIEW_RESPONSIVE_VIEWPORTS) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height })
      await page.goto('/')
      await expect(page.locator('[data-page="overview"]'), `${viewport.label} 首页就绪`).toBeVisible()
      await expectVirtualRendering(page, ROUTES[0])
      await page.locator('[data-page="overview"]').evaluate((el) => el.setAttribute('data-overflow-probe', 'true'))
      await expectNoWorkspaceOverflow(page, viewport.label)
    }
  })

  test('关键页面桌面和移动端均不产生横向溢出', async ({ page }) => {
    test.setTimeout(60_000)

    for (const viewport of OVERVIEW_RESPONSIVE_VIEWPORTS) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height })
      for (const route of ROUTES) {
        await page.goto(route.href)
        const ready = page.locator(route.readySelector)
        await expect(ready, `${viewport.label} ${route.label} 页面就绪`).toBeVisible()
        await expectVirtualRendering(page, route)
        await ready.evaluate((el) => el.setAttribute('data-overflow-probe', 'true'))
        await expectNoWorkspaceOverflow(page, `${viewport.label} ${route.label}`, route.readySelector)
      }
    }
  })

  test('1024x768 关键表格只在自身容器内横向滚动', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 768 })

    for (const route of RESPONSIVE_TABLE_ROUTES) {
      await page.goto(route.href)
      const ready = page.locator(route.readySelector)
      await expect(ready, `${route.label} 页面就绪`).toBeVisible()
      await ready.evaluate((el) => el.setAttribute('data-overflow-probe', 'true'))
      await expectNoWorkspaceOverflow(page, `窄桌面端 ${route.label}`, route.readySelector)
      await expectTablesOwnHorizontalScroll(page, `窄桌面端 ${route.label}`, route.readySelector)
    }
  })

  test('移动端导航可打开主要分组', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/')
    await expect(page.locator('[data-page="overview"]'), '移动端首页就绪').toBeVisible()

    const mobileNav = page.locator('[data-slot="mobile-console-nav"]')
    await expect(mobileNav, '移动端导航可见').toBeVisible()
    await mobileNav.getByRole('button', { name: '服务器' }).click()
    const mobilePanel = page.locator('[data-slot="mobile-nav-panel"]')
    await expect(mobilePanel, '移动端导航抽屉展开').toHaveAttribute('data-state', 'open')
    await expect.poll(async () => mobilePanel.evaluate((el) => getComputedStyle(el).animationName), { message: '移动端导航抽屉展开动画' }).toContain('jm-panel-up')
    await expect(mobileNav.getByRole('link', { name: '全部服务器' }), '服务器分组链接可见').toBeVisible()
    await expect(mobileNav.getByRole('link', { name: '节点' }), '节点链接可见').toBeVisible()

    await mobileNav.getByRole('button', { name: '收起移动导航' }).click()
    await expect(mobilePanel, '移动端导航抽屉收起').toHaveAttribute('data-state', 'closed')
    expect(await mobilePanel.evaluate((el) => getComputedStyle(el).animationName), '移动端导航抽屉收起动画').toContain('jm-panel-down')
    await page.waitForTimeout(20)
    await expect(mobilePanel, '移动端导航抽屉收起动画期间仍挂载').toHaveAttribute('data-state', 'closed')
  })

  test('顶部进度条存在，数据页侧栏折叠/展开不逐帧重排主工作区', async ({ page }) => {
    for (const route of [ROUTES[0], ROUTES[1]]) {
      await page.goto(route.href)
      await expect(page.locator(route.readySelector), `${route.label} 页面就绪`).toBeVisible()
      await expectVirtualRendering(page, route)
    }

    await page.goto('/')
    await expect(page.locator('[data-page="overview"]'), '首页就绪').toBeVisible()
    const progressTrack = page.locator('[data-slot="top-loading-track"]')
    await expect(progressTrack, '顶部进度条轨道存在').toBeAttached()
    await expect.poll(async () => progressTrack.getAttribute('data-visible'), { message: '稳定后顶部进度条隐藏' }).toBe('false')
    await expect(page.locator('[data-slot="workspace-route-transition"]'), '工作区路由动画容器存在').toBeVisible()
    await expect
      .poll(async () => page.locator('[data-slot="workspace-route-transition"]').evaluate((el) => getComputedStyle(el).animationName))
      .toContain('jm-route-enter')

    const results: Array<{ route: string; collapse: Awaited<ReturnType<typeof sidebarFrameStats>>; expand: Awaited<ReturnType<typeof sidebarFrameStats>> }> = []

    for (const route of [ROUTES[0], ROUTES[1]]) {
      await page.goto(route.href)
      await expect(page.locator(route.readySelector), `${route.label} 页面就绪`).toBeVisible()
      await expectVirtualRendering(page, route)

      const sidebar = page.locator('[data-slot="console-sidebar"]')
      const drawer = page.locator('[data-slot="sidebar-drawer"]')
      await expect(sidebar, `${route.label} 桌面侧栏可见`).toBeVisible()
      await expect(drawer, `${route.label} 侧栏内部 drawer 可见`).toBeVisible()
      await expect(sidebar, `${route.label} 侧栏默认展开状态`).toHaveAttribute('data-state', 'expanded')

      const collapseButton = page.locator('[data-slot="console-header"]').getByRole('button', { name: '收起侧栏' }).filter({ hasNotText: 'JianManager' })
      await expect(collapseButton, `${route.label} 显式侧栏收起按钮唯一`).toHaveCount(1)
      await collapseButton.click()
      const collapseStats = await sidebarFrameStats(page)
      await expect(sidebar, `${route.label} 侧栏折叠动画状态`).toHaveAttribute('data-state', 'collapsed')
      await expect
        .poll(async () => sidebar.evaluate((el) => Math.round(el.getBoundingClientRect().width)), { message: `${route.label} 侧栏折叠后仍保留 56px 导航轨` })
        .toBe(56)
      expect(collapseStats.sidebarTransition, `${route.label} 收起时 aside 不做 width transition，避免主工作区逐帧重排`).not.toContain('width')
      expect(collapseStats.sidebarMidW, `${route.label} 收起中段 aside 布局宽度保持展开宽度`).toBe(collapseStats.sidebarStartW)
      expect(collapseStats.drawerTransition, `${route.label} 收起由 drawer 视觉宽度过渡驱动`).toContain('width')
      expect(collapseStats.drawerTransitionDurationMs, `${route.label} 收起 drawer 过渡时长`).toBeGreaterThanOrEqual(250)
      expect(collapseStats.drawerAnimationName, `${route.label} 收起不再使用 clip-path keyframes`).toBe('none')
      expect(collapseStats.drawerMidW, `${route.label} 收起中段 drawer 宽度处于连续过渡中`).toBeGreaterThan(56)
      expect(collapseStats.drawerMidW, `${route.label} 收起中段 drawer 宽度处于连续过渡中`).toBeLessThan(240)
      expect(collapseStats.contentTransition, `${route.label} 收起内容区只做 compositor transform`).toContain('transform')
      expect(collapseStats.contentMidX, `${route.label} 收起中段内容区通过 transform 左移`).toBeLessThan(collapseStats.contentStartX)
      expect(collapseStats.expandedModeTransition, `${route.label} 收起展开层做淡出和缩进动画`).toContain('opacity')
      expect(collapseStats.expandedModeTransition, `${route.label} 收起展开层做淡出和缩进动画`).toContain('transform')
      expect(collapseStats.collapsedModeTransition, `${route.label} 收起折叠图标层做淡入和缩进动画`).toContain('opacity')
      expect(collapseStats.collapsedModeTransition, `${route.label} 收起折叠图标层做淡入和缩进动画`).toContain('transform')
      expect(collapseStats.expandedIconTransition, `${route.label} 图标自身保留缩进过渡`).toContain('transform')
      expect(collapseStats.contentFinalX, `${route.label} 收起结束内容区落到图标轨后`).toBeLessThanOrEqual(80)
      expect(collapseStats.framesOver24, `${route.label} 收起掉帧数量`).toBeLessThanOrEqual(10)

      const headerExpandButton = page.locator('[data-slot="console-header"]').getByRole('button', { name: '展开侧栏' })
      const railExpandButton = page.locator('[data-mode="collapsed"][aria-hidden="false"] button[aria-label="展开侧栏"]')
      await expect(headerExpandButton, `${route.label} 顶栏展开入口唯一`).toHaveCount(1)
      await expect(railExpandButton, `${route.label} 图标轨展开入口唯一`).toHaveCount(1)
      await railExpandButton.click()
      const expandStats = await sidebarFrameStats(page)
      await expect(sidebar, `${route.label} 侧栏展开动画状态`).toHaveAttribute('data-state', 'expanded')
      expect(expandStats.sidebarTransition, `${route.label} 展开时 aside 不做 width transition，避免主工作区逐帧重排`).not.toContain('width')
      expect(expandStats.sidebarMidW, `${route.label} 展开中段 aside 布局宽度保持折叠宽度`).toBe(expandStats.sidebarStartW)
      expect(expandStats.drawerTransition, `${route.label} 展开由 drawer 视觉宽度过渡驱动`).toContain('width')
      expect(expandStats.drawerTransitionDurationMs, `${route.label} 展开 drawer 过渡时长`).toBeGreaterThanOrEqual(250)
      expect(expandStats.drawerAnimationName, `${route.label} 展开不再使用 clip-path keyframes`).toBe('none')
      expect(expandStats.drawerMidW, `${route.label} 展开中段 drawer 宽度处于连续过渡中`).toBeGreaterThan(56)
      expect(expandStats.drawerMidW, `${route.label} 展开中段 drawer 宽度处于连续过渡中`).toBeLessThan(240)
      expect(expandStats.contentTransition, `${route.label} 展开内容区只做 compositor transform`).toContain('transform')
      expect(expandStats.contentTransition, `${route.label} 展开内容区右边缘用裁切停在视口边界`).toContain('clip-path')
      expect(expandStats.contentMidX, `${route.label} 展开中段内容区通过 transform 右移`).toBeGreaterThan(expandStats.contentStartX)
      expect(Math.abs(expandStats.contentMidVisibleRight - expandStats.viewportWidth), `${route.label} 展开中段内容区右边缘停在视口边界`).toBeLessThanOrEqual(3)
      expect(expandStats.expandedModeTransition, `${route.label} 展开展开层做淡入和归位动画`).toContain('opacity')
      expect(expandStats.expandedModeTransition, `${route.label} 展开展开层做淡入和归位动画`).toContain('transform')
      expect(expandStats.collapsedModeTransition, `${route.label} 展开折叠图标层做淡出和归位动画`).toContain('opacity')
      expect(expandStats.collapsedModeTransition, `${route.label} 展开折叠图标层做淡出和归位动画`).toContain('transform')
      expect(expandStats.expandedIconTransition, `${route.label} 图标自身保留缩进过渡`).toContain('transform')
      expect(expandStats.contentFinalX, `${route.label} 展开结束内容区落到展开侧栏后`).toBeGreaterThanOrEqual(220)
      expect(expandStats.framesOver24, `${route.label} 展开掉帧数量`).toBeLessThanOrEqual(10)
      results.push({ route: route.label, collapse: collapseStats, expand: expandStats })
    }

    test.info().annotations.push({
      type: 'sidebar-animation-benchmark',
      description: JSON.stringify({ results, frameBudgetMs: 24 }),
    })
  })

  test('系统减少动态时关闭装饰切页动画并保留进度反馈', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto('/networks/topology')
    await expect(page.locator('[data-page="networks"]'), '网络拓扑页就绪').toBeVisible()

    expect(await animationDurationMs(page, '[data-slot="workspace-route-transition"]'), '减少动态模式下关闭装饰切页动画').toBe(0)

    await page.locator('a[href="/logs"]').first().click()
    await expect.poll(
      async () => visibleAnimationDurationMs(page, '[data-testid="top-loading-bar"]'),
      { message: '减少动态模式下进度条动画可见', timeout: 1000 },
    )
      .toBeGreaterThanOrEqual(120)
    await expect(page.locator('[data-page="logs"]'), '日志中心页就绪').toBeVisible()
    await expect.poll(
      async () => page.locator('[data-slot="top-loading-track"]').getAttribute('data-visible'),
      { message: '切页完成后顶部进度条稳定隐藏' },
    )
      .toBe('false')
  })
})
