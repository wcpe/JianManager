import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

const ROUTE_READY_TIMEOUT_MS = 15_000

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
  await expect(surface, `${route.label} 虚拟渲染容器存在`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
  await expect.poll(
    async () => Number(await surface.getAttribute('data-total-count')),
    { message: `${route.label} mock 数据量达到 1000+`, timeout: ROUTE_READY_TIMEOUT_MS },
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

/** 同一视口内逐页验证横向溢出；每个视口独立计时，避免慢 runner 把覆盖误判为超时。 */
async function expectRoutesWithoutWorkspaceOverflow(
  page: Page,
  viewport: (typeof OVERVIEW_RESPONSIVE_VIEWPORTS)[number],
): Promise<void> {
  await page.setViewportSize({ width: viewport.width, height: viewport.height })
  for (const route of ROUTES) {
    await page.goto(route.href)
    const ready = page.locator(route.readySelector)
    await expect(ready, `${viewport.label} ${route.label} 页面就绪`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
    await expectVirtualRendering(page, route)
    await ready.evaluate((el) => el.setAttribute('data-overflow-probe', 'true'))
    await expectNoWorkspaceOverflow(page, `${viewport.label} ${route.label}`, route.readySelector)
  }
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

async function armVisibleAnimationDurationProbe(page: Page, rootSelector: string, targetSelector: string): Promise<void> {
  await page.locator(rootSelector).evaluate((root, selector) => {
    const probeRoot = root as HTMLElement & { __jmVisibleAnimationObserver?: MutationObserver }
    probeRoot.__jmVisibleAnimationObserver?.disconnect()
    probeRoot.dataset.observedAnimationDurationMs = '0'

    const sample = () => {
      for (const target of probeRoot.querySelectorAll<HTMLElement>(selector)) {
        if (target.dataset.visible !== 'true') continue
        const rawDuration = getComputedStyle(target).animationDuration.split(',')[0] ?? '0s'
        const durationMs = rawDuration.endsWith('ms') ? Number.parseFloat(rawDuration) : Number.parseFloat(rawDuration) * 1000
        const observedDurationMs = Number.parseFloat(probeRoot.dataset.observedAnimationDurationMs ?? '0')
        if (Number.isFinite(durationMs) && durationMs > observedDurationMs) {
          probeRoot.dataset.observedAnimationDurationMs = String(durationMs)
        }
      }
    }

    const observer = new MutationObserver(sample)
    observer.observe(probeRoot, {
      attributes: true,
      attributeFilter: ['data-mode', 'data-visible'],
      childList: true,
      subtree: true,
    })
    probeRoot.__jmVisibleAnimationObserver = observer
    sample()
  }, targetSelector)
}

async function readVisibleAnimationDurationProbe(page: Page, rootSelector: string): Promise<number> {
  return page.locator(rootSelector).evaluate((root) => {
    const probeRoot = root as HTMLElement & { __jmVisibleAnimationObserver?: MutationObserver }
    probeRoot.__jmVisibleAnimationObserver?.disconnect()
    delete probeRoot.__jmVisibleAnimationObserver
    return Number.parseFloat(probeRoot.dataset.observedAnimationDurationMs ?? '0')
  })
}

interface SidebarFrameStats {
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
}

interface SidebarFrameProbe {
  read: () => Promise<SidebarFrameStats>
}

async function sidebarFrameStats(page: Page): Promise<SidebarFrameProbe> {
  const shell = page.locator('[data-slot="console-shell"]')
  await shell.evaluate((root) => {
    interface FrameSample {
      sidebarW: number
      drawerW: number
      contentX: number
      contentRight: number
      sidebarTransition: string
      sidebarTransitionDurationMs: number
      drawerTransition: string
      drawerTransitionDurationMs: number
      drawerAnimationName: string
      expandedModeTransition: string
      collapsedModeTransition: string
      expandedIconTransition: string
      contentTransition: string
      contentClipPath: string
    }

    const probeRoot = root as HTMLElement & { __jmSidebarFrameStats?: Promise<SidebarFrameStats> }
    const sidebar = document.querySelector('[data-slot="console-sidebar"]') as HTMLElement | null
    const drawer = document.querySelector('[data-slot="sidebar-drawer"]') as HTMLElement | null
    const content = document.querySelector('[data-slot="console-content"]') as HTMLElement | null
    const expandedMode = document.querySelector('.jm-sidebar-mode[data-mode="expanded"]') as HTMLElement | null
    const collapsedMode = document.querySelector('.jm-sidebar-mode[data-mode="collapsed"]') as HTMLElement | null
    const expandedIcon = document.querySelector('.jm-sidebar-mode[data-mode="expanded"] .jm-nav-link-icon') as HTMLElement | null
    if (!sidebar || !drawer || !content) throw new Error('侧栏帧采样目标不存在')

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
    const sample = (): FrameSample => {
      const sidebarStyle = getComputedStyle(sidebar)
      const drawerStyle = getComputedStyle(drawer)
      const contentStyle = getComputedStyle(content)
      const contentRect = content.getBoundingClientRect()
      return {
        sidebarW: sidebar.getBoundingClientRect().width,
        drawerW: drawer.getBoundingClientRect().width,
        contentX: contentRect.x,
        contentRight: contentRect.right,
        sidebarTransition: sidebarStyle.transitionProperty,
        sidebarTransitionDurationMs: toMs(sidebarStyle.transitionDuration),
        drawerTransition: drawerStyle.transitionProperty,
        drawerTransitionDurationMs: toMs(drawerStyle.transitionDuration),
        drawerAnimationName: drawerStyle.animationName,
        expandedModeTransition: expandedMode ? getComputedStyle(expandedMode).transitionProperty : 'none',
        collapsedModeTransition: collapsedMode ? getComputedStyle(collapsedMode).transitionProperty : 'none',
        expandedIconTransition: expandedIcon ? getComputedStyle(expandedIcon).transitionProperty : 'none',
        contentTransition: contentStyle.transitionProperty,
        contentClipPath: contentStyle.clipPath,
      }
    }

    const sidebarStartW = Math.round(sidebar.getBoundingClientRect().width)
    const drawerStartW = Math.round(drawer.getBoundingClientRect().width)
    const contentStartX = Math.round(content.getBoundingClientRect().x)
    const viewportWidth = window.innerWidth

    probeRoot.__jmSidebarFrameStats = new Promise((resolve) => {
      const frames: number[] = []
      const samples: FrameSample[] = []
      let lastFrameAt: number | null = null
      let sampling = false

      const finish = () => {
        observer.disconnect()
        const finalSample = samples.at(-1) ?? sample()
        const drawerWidths = [drawerStartW, ...samples.map((item) => item.drawerW), finalSample.drawerW]
        const drawerMinW = Math.min(...drawerWidths)
        const drawerMaxW = Math.max(...drawerWidths)
        const transitionSamples = samples.filter((item) => item.drawerW > drawerMinW + 1 && item.drawerW < drawerMaxW - 1)
        const drawerMidpoint = (drawerMinW + drawerMaxW) / 2
        const midSample = transitionSamples.reduce<FrameSample | undefined>((closest, item) => {
          if (!closest) return item
          return Math.abs(item.drawerW - drawerMidpoint) < Math.abs(closest.drawerW - drawerMidpoint) ? item : closest
        }, undefined) ?? samples[Math.floor(samples.length / 2)] ?? finalSample

        resolve({
          maxFrameMs: Math.round(Math.max(0, ...frames) * 10) / 10,
          framesOver24: frames.filter((value) => value > 24).length,
          sidebarTransition: midSample.sidebarTransition,
          sidebarTransitionDurationMs: midSample.sidebarTransitionDurationMs,
          drawerTransition: midSample.drawerTransition,
          drawerTransitionDurationMs: midSample.drawerTransitionDurationMs,
          drawerAnimationName: midSample.drawerAnimationName,
          expandedModeTransition: midSample.expandedModeTransition,
          collapsedModeTransition: midSample.collapsedModeTransition,
          expandedIconTransition: midSample.expandedIconTransition,
          sidebarStartW,
          sidebarMidW: Math.round(midSample.sidebarW),
          sidebarFinalW: Math.round(finalSample.sidebarW),
          drawerStartW,
          drawerMidW: Math.round(midSample.drawerW),
          drawerFinalW: Math.round(finalSample.drawerW),
          contentTransition: midSample.contentTransition,
          contentClipPath: midSample.contentClipPath,
          contentMidVisibleRight: Math.round(midSample.contentRight - clipRightPx(midSample.contentClipPath)),
          viewportWidth,
          contentStartX,
          contentMidX: Math.round(midSample.contentX),
          contentFinalX: Math.round(finalSample.contentX),
        })
      }

      const tick = (now: number) => {
        if (lastFrameAt !== null) frames.push(now - lastFrameAt)
        lastFrameAt = now
        samples.push(sample())
        if (probeRoot.dataset.sidebarMotion === 'idle') {
          finish()
          return
        }
        requestAnimationFrame(tick)
      }

      const startSampling = () => {
        if (sampling || probeRoot.dataset.sidebarMotion === 'idle') return
        sampling = true
        samples.push(sample())
        requestAnimationFrame(tick)
      }

      const observer = new MutationObserver(startSampling)
      observer.observe(probeRoot, { attributes: true, attributeFilter: ['data-sidebar-motion'] })
      startSampling()
    })
  })

  return {
    read: () => shell.evaluate(async (root) => {
      const probeRoot = root as HTMLElement & { __jmSidebarFrameStats?: Promise<SidebarFrameStats> }
      if (!probeRoot.__jmSidebarFrameStats) throw new Error('侧栏帧采样探针未布防')
      try {
        return await probeRoot.__jmSidebarFrameStats
      } finally {
        delete probeRoot.__jmSidebarFrameStats
      }
    }),
  }
}

/** 关键页面的 SPA 路由切换 benchmark：记录单页切换耗时供持续观察。 */
test.describe('页面切换 benchmark（mock 模式）', () => {
  test.beforeEach(async ({ page }) => {
    await resetConsoleLayout(page)
    await login(page)
  })

  test('记录关键页面切换耗时', async ({ page }) => {
    const results: Array<{ label: string; elapsedMs: number }> = []

    for (const route of ROUTES) {
      const link = page.locator(`a[href="${route.href}"]`).first()
      await expect(link, `${route.label} 导航入口存在`).toBeVisible()

      const started = await page.evaluate(() => performance.now())
      await link.click()
      await expect(page.locator(route.readySelector), `${route.label} 页面就绪`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
      await expectVirtualRendering(page, route)
      await page.evaluate(() => new Promise(requestAnimationFrame))
      const ended = await page.evaluate(() => performance.now())
      const elapsedMs = Math.round(ended - started)

      results.push({ label: route.label, elapsedMs })
    }

    const sorted = results.map((r) => r.elapsedMs).sort((a, b) => a - b)
    const p95 = sorted[Math.max(0, Math.ceil(sorted.length * 0.95) - 1)] ?? 0
    test.info().annotations.push({
      type: 'benchmark',
      description: JSON.stringify({ p95Ms: p95, results }),
    })
  })

  test('全部服务器首屏只走分页端点，返回后恢复滚动并附截图', async ({ page }) => {
    await page.goto('/instances?status=RUNNING&pageSize=50')
    await expect(page.locator('[data-page="instances"]'), '全部服务器页就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
    const surface = page.locator('[data-testid="instances-card-virtual"]')
    await expect(surface, '实例卡片虚拟列表存在').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
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
    await expect(page.locator('[data-page="instance-console"]'), '实例详情深链就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
    await page.goBack()
    await expect(page.locator('[data-page="instances"]'), '返回全部服务器页就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
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
      await expect(page.locator('[data-page="overview"]'), `${viewport.label} 首页就绪`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
      await expectVirtualRendering(page, ROUTES[0])
      await page.locator('[data-page="overview"]').evaluate((el) => el.setAttribute('data-overflow-probe', 'true'))
      await expectNoWorkspaceOverflow(page, viewport.label)
    }
  })

  for (const viewport of OVERVIEW_RESPONSIVE_VIEWPORTS) {
    test(`关键页面 ${viewport.label} 不产生横向溢出`, async ({ page }) => {
      test.setTimeout(60_000)
      await expectRoutesWithoutWorkspaceOverflow(page, viewport)
    })
  }

  test('1024x768 关键表格只在自身容器内横向滚动', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 768 })

    for (const route of RESPONSIVE_TABLE_ROUTES) {
      await page.goto(route.href)
      const ready = page.locator(route.readySelector)
      await expect(ready, `${route.label} 页面就绪`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
      await ready.evaluate((el) => el.setAttribute('data-overflow-probe', 'true'))
      await expectNoWorkspaceOverflow(page, `窄桌面端 ${route.label}`, route.readySelector)
      await expectTablesOwnHorizontalScroll(page, `窄桌面端 ${route.label}`, route.readySelector)
    }
  })

  test('移动端导航可打开主要分组', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/')
    await expect(page.locator('[data-page="overview"]'), '移动端首页就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })

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
    test.setTimeout(60_000)

    for (const route of [ROUTES[0], ROUTES[1]]) {
      await page.goto(route.href)
      await expect(page.locator(route.readySelector), `${route.label} 页面就绪`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
      await expectVirtualRendering(page, route)
    }

    await page.goto('/')
    await expect(page.locator('[data-page="overview"]'), '首页就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
    const progressTrack = page.locator('[data-slot="top-loading-track"]')
    await expect(progressTrack, '顶部进度条轨道存在').toBeAttached()
    await expect.poll(async () => progressTrack.getAttribute('data-visible'), { message: '稳定后顶部进度条隐藏' }).toBe('false')
    await expect(page.locator('[data-slot="workspace-route-transition"]'), '工作区路由动画容器存在').toBeVisible()
    await expect
      .poll(async () => page.locator('[data-slot="workspace-route-transition"]').evaluate((el) => getComputedStyle(el).animationName))
      .toContain('jm-route-enter')

    const results: Array<{ route: string; collapse: SidebarFrameStats; expand: SidebarFrameStats }> = []

    for (const route of [ROUTES[0], ROUTES[1]]) {
      await page.goto(route.href)
      await expect(page.locator(route.readySelector), `${route.label} 页面就绪`).toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
      await expectVirtualRendering(page, route)

      const sidebar = page.locator('[data-slot="console-sidebar"]')
      const drawer = page.locator('[data-slot="sidebar-drawer"]')
      await expect(sidebar, `${route.label} 桌面侧栏可见`).toBeVisible()
      await expect(drawer, `${route.label} 侧栏内部 drawer 可见`).toBeVisible()
      await expect(sidebar, `${route.label} 侧栏默认展开状态`).toHaveAttribute('data-state', 'expanded')

      const collapseButton = page.locator('[data-slot="console-header"]').getByRole('button', { name: '收起侧栏' }).filter({ hasNotText: 'JianManager' })
      await expect(collapseButton, `${route.label} 显式侧栏收起按钮唯一`).toHaveCount(1)
      const collapseProbe = await sidebarFrameStats(page)
      await collapseButton.click()
      // 控制端延迟超过侧栏 320ms 动画，验证探针不依赖断言恢复时机。
      await page.waitForTimeout(400)
      const collapseStats = await collapseProbe.read()
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

      const headerExpandButton = page.locator('[data-slot="console-header"]').getByRole('button', { name: '展开侧栏' })
      const railExpandButton = page.locator('[data-mode="collapsed"][aria-hidden="false"] button[aria-label="展开侧栏"]')
      await expect(headerExpandButton, `${route.label} 顶栏展开入口唯一`).toHaveCount(1)
      await expect(railExpandButton, `${route.label} 图标轨展开入口唯一`).toHaveCount(1)
      const expandProbe = await sidebarFrameStats(page)
      await railExpandButton.click()
      // 控制端延迟超过侧栏 320ms 动画，验证探针可在慢速 CI 中保留完整结果。
      await page.waitForTimeout(400)
      const expandStats = await expandProbe.read()
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
      results.push({ route: route.label, collapse: collapseStats, expand: expandStats })
    }

    test.info().annotations.push({
      type: 'sidebar-animation-benchmark',
      description: JSON.stringify({ results }),
    })
  })

  test('系统减少动态时关闭装饰切页动画并保留进度反馈', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto('/networks/topology')
    await expect(page.locator('[data-page="networks"]'), '网络拓扑页就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })

    expect(await animationDurationMs(page, '[data-slot="workspace-route-transition"]'), '减少动态模式下关闭装饰切页动画').toBe(0)

    const progressTrackSelector = '[data-slot="top-loading-track"]'
    await armVisibleAnimationDurationProbe(page, progressTrackSelector, '[data-testid="top-loading-bar"]')
    await page.locator('a[href="/logs"]').first().click()
    // 模拟慢速 CI：测试进程恢复断言时，路由进度反馈可能已经结束。
    await page.waitForTimeout(800)
    expect(await readVisibleAnimationDurationProbe(page, progressTrackSelector), '减少动态模式下进度条动画可见').toBeGreaterThanOrEqual(120)
    await expect(page.locator('[data-page="logs"]'), '日志中心页就绪').toBeVisible({ timeout: ROUTE_READY_TIMEOUT_MS })
    await expect.poll(
      async () => page.locator('[data-slot="top-loading-track"]').getAttribute('data-visible'),
      { message: '切页完成后顶部进度条稳定隐藏' },
    )
      .toBe('false')
  })
})
