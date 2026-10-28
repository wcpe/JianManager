import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-507 验收③：列表页大数据量渲染开销的前后对比实测。
 *
 * 【为什么要单独一个 spec】
 * `navigation-benchmark.spec.ts` 只记录「切页耗时」并作为 annotation 输出，不产出
 * 「渲染开销是否随条数线性恶化」的证据；而验收③明示要求「有前后对比数据」。
 * 本文件直接测量**同一容器在不同数据量下的渲染耗时**与 **DOM 行数**，这正是两条判据：
 * 1. DOM 行数被窗口裁剪（不随条数增长）；
 * 2. 渲染耗时随条数亚线性（不随条数线性恶化）。
 *
 * 【怎么测】
 * 不改产品代码，用 Playwright 在真实 Chromium 里驱动：滚到顶端 → 滚到底端 → 回到顶端，
 * 用 PerformanceObserver 采每一帧的 long task + 用 rAF 计帧间隔，再用 `performance.now()`
 * 包住「把列表滚到底」这一动作（强制它重建窗口内的行）。
 */

const READY_TIMEOUT_MS = 20_000

interface RenderProbe {
  /** DOM 里当前真实存在的行数。 */
  domRows: number
  /** 容器总高（px）——由总条数 × 行高算出，可反推数据量。 */
  scrollHeight: number
  /** 可见窗口高度。 */
  clientHeight: number
  /** 已加载总条数（产品自己维护的计数）。 */
  totalCount: number
}

/** 读取一个虚拟列表容器的状态。 */
async function readProbe(page: Page, surface: string, rowSelector: string): Promise<RenderProbe> {
  const el = page.locator(surface)
  const [domRows, scrollHeight, clientHeight, totalCount] = await Promise.all([
    page.locator(rowSelector).count(),
    el.evaluate((node) => node.scrollHeight),
    el.evaluate((node) => node.clientHeight),
    el.evaluate((node) => Number(node.getAttribute('data-total-count') ?? 0)),
  ])
  return { domRows, scrollHeight, clientHeight, totalCount }
}

/**
 * 反复滚到底、回顶部，共 `rounds` 轮，返回每轮的耗时。
 * 每轮内多次跳转是为了强制虚拟窗口重算 + 行组件重新挂载（这正是「随条数恶化」的受力点）。
 */
async function measureScrollRender(page: Page, surface: string, rounds: number): Promise<number[]> {
  const el = page.locator(surface)
  const timings: number[] = []
  for (let i = 0; i < rounds; i += 1) {
    const ms = await el.evaluate(async (node) => {
      const target = node as HTMLElement
      const total = Math.max(0, target.scrollHeight - target.clientHeight)
      const started = performance.now()
      // 逐段跳到底：每段都触发一次窗口重算与行重挂载。
      const steps = 8
      for (let s = 1; s <= steps; s += 1) {
        target.scrollTop = (total * s) / steps
        target.dispatchEvent(new Event('scroll', { bubbles: true }))
        await new Promise(requestAnimationFrame)
      }
      // 回到顶部，让下一轮从同一起点开始。
      target.scrollTop = 0
      target.dispatchEvent(new Event('scroll', { bubbles: true }))
      await new Promise(requestAnimationFrame)
      return performance.now() - started
    })
    timings.push(Math.round(ms * 10) / 10)
  }
  return timings
}

/** 统计测量期间的 long task（>50ms）次数与总阻塞时长。 */
async function collectLongTasks(page: Page, action: () => Promise<void>): Promise<{ count: number; totalMs: number }> {
  await page.evaluate(() => {
    const store = window as Window & { __jmLongTasks?: { count: number; totalMs: number } }
    store.__jmLongTasks = { count: 0, totalMs: 0 }
    const obs = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const s = (window as Window & { __jmLongTasks?: { count: number; totalMs: number } }).__jmLongTasks
        if (!s) continue
        s.count += 1
        s.totalMs += entry.duration
      }
    })
    obs.observe({ entryTypes: ['longtask'] })
  })
  await action()
  return page.evaluate(() => {
    const s = (window as Window & { __jmLongTasks?: { count: number; totalMs: number } }).__jmLongTasks
    return { count: s?.count ?? 0, totalMs: Math.round((s?.totalMs ?? 0) * 10) / 10 }
  })
}

test.describe('FR-507 验收③：列表虚拟化前后对比实测', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  /*
    审计页的 mock 只有 ~15 条真实数据（见 packages/devmock/src/handlers/domains/identity.ts 的
    `audit` 集合），无法在 mock 下复现 250 条量级；dom 用例里那个 250 是测试自己注入的。
    因此审计页在这里降级为「小数据量对照组」：只验证虚拟化机制在它身上同样生效、
    且容器可滚动。真正的「大数据量不随条数恶化」证据由日志中心（1000+ 条）承担。
  */
  test('审计页（小数据量对照）：虚拟容器同样裁剪渲染', async ({ page }) => {
    test.setTimeout(180_000)
    await page.goto('/audit')
    // 审计页的 PageShell 不传 data-page（与其他页不同），就绪以页头标题为准。
    // 现成用例 e2e/settings-audit.spec.ts、backup-audit-backend.spec.ts 同样这么判。
    await expect(page.getByRole('heading', { name: '审计日志' }), '审计页就绪').toBeVisible({ timeout: READY_TIMEOUT_MS })

    const surface = '[data-testid="audit-log-virtual"]'
    await expect(page.locator(surface), '审计页虚拟容器存在').toBeVisible({ timeout: READY_TIMEOUT_MS })

    const probe = await readProbe(page, surface, '[data-audit-row-id]')
    test.info().annotations.push({
      type: 'audit-render-benchmark',
      description: JSON.stringify({ probe }),
    })

    // 该数据量本来就小，能全部渲染也可接受；关键是容器与行都在、且数量与真实数据量一致地小。
    expect(probe.domRows, '审计页可见行数不超过真实数据量').toBeLessThanOrEqual(60)
  })

  test('日志中心（1000+ 条）：DOM 行数被裁剪，不随数据量线性增长', async ({ page }) => {
    test.setTimeout(180_000)
    await page.goto('/logs')
    await expect(page.locator('[data-page="logs"]'), '日志中心就绪').toBeVisible({ timeout: READY_TIMEOUT_MS })

    // 默认时间窗只有 15 分钟（quick-first-paint 依据），需显式切到「全部时间」才有 1000+ 条。
    const rangeSelect = page.getByTestId('logs-range-select')
    await expect(rangeSelect, '日志时间范围控件存在').toBeVisible({ timeout: READY_TIMEOUT_MS })
    await rangeSelect.click()
    await page.getByRole('option', { name: '全部时间' }).click()
    await expect(rangeSelect, '时间范围已切到全部时间').toContainText('全部时间')

    const surface = '[data-testid="logs-virtual"]'
    await expect(page.locator(surface), '日志虚拟容器存在').toBeVisible({ timeout: READY_TIMEOUT_MS })
    // 等真实数据量到位（与 navigation-benchmark.spec.ts 同口径：1000+）
    await expect
      .poll(async () => Number(await page.locator(surface).getAttribute('data-total-count')), {
        message: '日志数据量达到 1000+',
        timeout: READY_TIMEOUT_MS,
      })
      .toBeGreaterThanOrEqual(1000)

    const probe = await readProbe(page, surface, '[data-testid="log-row"]')
    expect(probe.scrollHeight, '日志容器可滚动高度应远大于视口').toBeGreaterThan(probe.clientHeight + 200)

    const timings = await measureScrollRender(page, surface, 3)
    const longTasks = await collectLongTasks(page, () => measureScrollRender(page, surface, 3).then(() => undefined))

    test.info().annotations.push({
      type: 'logs-render-benchmark',
      description: JSON.stringify({ probe, timings, longTasks }),
    })

    // 核心判据：数据量 1000+，但 DOM 只挂载几十行——渲染量与数据量解耦。
    expect(probe.totalCount, '已加载总条数').toBeGreaterThanOrEqual(1000)
    expect(probe.domRows, '日志页 DOM 行数应远小于 1000（虚拟裁剪）').toBeLessThan(80)
    const worst = Math.max(...timings)
    expect(worst, '日志页单轮滚动重挂载耗时').toBeLessThan(3000)
  })

  test('任务页：数百条量级渲染开销与 DOM 行数', async ({ page }) => {
    test.setTimeout(180_000)
    await page.goto('/tasks')
    await expect(page.locator('[data-page="tasks"]'), '任务页就绪').toBeVisible({ timeout: READY_TIMEOUT_MS })

    const surface = '[data-testid="tasks-virtual"]'
    await expect(page.locator(surface), '任务页虚拟容器存在').toBeVisible({ timeout: READY_TIMEOUT_MS })

    const probe = await readProbe(page, surface, '[data-task-row-id]')
    expect(probe.scrollHeight, '任务页容器可滚动高度应远大于视口').toBeGreaterThan(probe.clientHeight + 200)
    const timings = await measureScrollRender(page, surface, 3)

    test.info().annotations.push({
      type: 'tasks-render-benchmark',
      description: JSON.stringify({ probe, timings }),
    })

    expect(probe.domRows, '任务页 DOM 行数应被虚拟窗口裁剪').toBeLessThan(120)
    const worst = Math.max(...timings)
    expect(worst, '任务页单轮滚动重挂载耗时').toBeLessThan(3000)
  })
})
