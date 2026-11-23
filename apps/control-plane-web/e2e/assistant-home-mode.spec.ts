import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/** 
 *§4.1 / §6 首页模式与千级数据摘要 · 真浏览器验收。
 *
 * 为什么这两条必须落到 e2e：
 * - **深链刷新不丢**是「路由层 + 浏览器刷新」的合成事实。jsdom 里既没有真刷新，
 *   也复现不出「先按默认渲染再切走」的首帧闪回；只有真浏览器 reload 才能判定。
 * - **千级数据摘要有界**是**网络 + DOM 双重**事实：请求参数（pageSize / range / 是否重复下发）
 *   只有真发请求才看得见；「不随 1200 实例膨胀」只有真 DOM 才数得出来。
 *   两者都不能靠「理论上更快/有界」推断——那是断言不了的东西。
 *
 * 本文件的上界与阈值全部来自实测，出处写在各自断言旁，不取整、不臆测。
 */

/** 首页模式记忆的存储键（与 src/lib/shared/home-mode.ts 的 HOME_MODE_KEY 同源）。 */
const HOME_MODE_KEY = 'console.homeMode'

/** 
 * AI 首页总 DOM 节点数上界。
 *
 * 【这个数字怎么来的】实测值 **716**（`document.querySelectorAll('*').length`，
 * devmock 默认档 large = 1200 实例，Desktop Chrome 1280×720，
 * 右侧摘要区已全部落定：对话 / 可以这样问 / 需要关注 / 近期任务 / 集群水位 / 最近会话 共 6 个 panel）。
 *
 * 上界取 **2000 ≈ 2.8 倍**：
 * - 上限侧要容纳合理增长（多一条会话、多一个告警、文案变长、a11y 属性增补）；
 * - 下限侧必须**排掉「按实例数渲染」**：1200 个实例哪怕每行只 5 个节点也 ≥ 6000，远超 2000。
 *   所以这条上界同时是「不随实例数膨胀」的判据，不是随手填的大数。
 *
 * 它锚定的是**当前 devmock 的 1200 规模**：若默认档调到 small/medium，上界仍成立（更宽松），
 * 但「排掉按实例数渲染」的力度会变弱——届时改档应重测。
 */
const DOM_NODE_UPPER_BOUND = 2000

/** 
 * 摘要请求落定的超时：devmock large 规模首屏要下懒加载 chunk，给足。
 *
 * 从 30s 提到 45s 的依据（实测，非保守猜测）：两个用例串行跑时，
 * 后一个用例的**对照页 /nodes** 在 30s 内等不到 `h1`——同一时刻 vite 还在首屏
 * 预构建 + 千级 mock 数据在浏览器内生成，CPU 被前一个用例的残留查询占着。
 * 单独跑该用例 29.5s 就通过，说明不是产品慢，是并发环境下的就绪等待不够。
 * 这是**就绪等待**、不是被测属性，放宽它不会削弱任何断言。
 */
const READY_TIMEOUT = 45_000

/** 单个对照观察窗时长。 */
const WINDOW_MS = 30_000

/** 
 * 观察窗长度定为 **30s** 的依据。
 *
 * 本仓真实存在的轮询间隔：任务进度 2s（`ACTIVE_TASKS_REFETCH_MS`）、
 * 集群水位 10s（`useMetricOverview`）、实例/通知兜底 30s。
 * 30s 窗内：2s 轮询器 ≈15 次、10s 轮询器 ≈3 次、30s 轮询器 ≈1 次。
 * 因此「≤2 次」足以排掉 2s 与 10s 两类（它们会给 15 / 3 次），
 * 这正是「摘要区不许把既有 hook 的轮询带进来」要守住的东西。
 */
const POLLING_DELTA_UPPER_BOUND = 2

/** 
 * 【为什么要做「对照页」测量，而不是直接要求计数为 0】
 *
 * 实测发现 mock 运行时存在一个**与无关的全站噪声源**：SSE 流（`/instances/events`）
 * 被服务端每 ~5s 关闭一次，客户端按设计自动重连（`api/events.ts`），重连会用
 * `invalidateQueries({queryKey:['instances']})` 失效缓存并伴随一次权限重载，
 * 于是**同一毫秒内**成片端点一起重取（实测 41036ms 一次、46272ms 一次，
 * 波及 nodes / topology / instance-groups / notifications / metrics / tasks 等十余个端点），
 * 首页的私有查询（alerts / conversations / crashed / overview）也会被顺带重取（42255ms、47287ms）。
 *
 * 因此「静置 30s 内某端点计数必须为 0」在该环境下不成立——它会被噪声随机打破，
 * 假红恰恰掩盖真正的结论。可判定的形式是**对照**：
 * 同样时长、同样噪声下，比较「有首页摘要的 /」与「没有首页摘要的 /nodes」，
 * 两者都同幅承受噪声与顶栏轮询，**差值**才等于首页自己多加的轮询。
 */
const CONTROL_PATH = '/nodes'

/** 观察期收集到的请求签名（只在监听器里记录，断言都放在测试体里，避免回调里的失败被吞掉）。 */
interface WatchedRequests {
  /** `/metrics/overview` 的完整 search（含前导 `?`），按发生顺序。 */
  overviewSearches: string[]
  /** `/instances/search` 的查询参数，按发生顺序。 */
  instanceSearches: URLSearchParams[]
  /** 裸数组端点 `GET /instances`（= 一次拉回 1200 条的那个），出现即记录。 */
  bareInstanceListSearches: string[]
  /** 首页私有摘要端点的发生次数（只计数）。 */
  alertEvents: number
  assistantConversations: number
  /** 已知的「一定会轮询」对照端点：存在活跃任务时 2s 一拍。 */
  tasksWithLimit5: number
}

/** 
 * 挂 `/api/v1` 请求监听器。
 *
 * 必须在 `goto` **之前**调用：首页摘要在挂载当刻就发请求，晚挂监听会漏掉首批。
 * 只按 pathname 精确匹配——`/alerts/events/unread-count`、`/metrics/series/batch`
 * 分别是顶栏未读数与趋势图，路径不同，不会污染这里的计数。
 */
function watchRequests(page: Page, seen: WatchedRequests): void {
  page.on('request', (req) => {
    const url = new URL(req.url())
    if (!url.pathname.startsWith('/api/v1/')) return
    switch (url.pathname) {
      case '/api/v1/metrics/overview':
        seen.overviewSearches.push(url.search)
        break
      case '/api/v1/instances/search':
        seen.instanceSearches.push(url.searchParams)
        break
      case '/api/v1/instances':
        seen.bareInstanceListSearches.push(url.search)
        break
      case '/api/v1/alerts/events':
        seen.alertEvents += 1
        break
      case '/api/v1/assistant/conversations':
        seen.assistantConversations += 1
        break
      case '/api/v1/tasks':
        // 首页「近期任务」用的是 limit=5；顶栏用的是 limit=100（`useTasks()` 默认）。
        if (url.searchParams.get('limit') === '5') seen.tasksWithLimit5 += 1
        break
      default:
        break
    }
  })
}

function emptyWatched(): WatchedRequests {
  return {
    overviewSearches: [],
    instanceSearches: [],
    bareInstanceListSearches: [],
    alertEvents: 0,
    assistantConversations: 0,
    tasksWithLimit5: 0,
  }
}

/** 
 * 记录「全库实例总数」，用于证明数据集确实是千级。
 *
 * 来源是页面**自己**发出的 `GET /instances/aggregate` 响应里的 `total`：
 * 该端点由顶栏无条件调用（`ConsoleHeader` 的 `useInstanceAggregate()`），
 * 且 `total` 是全库计数、与任何分页窗口无关。
 *
 * 刻意不用额外的 `fetch` 探针：那会引入「mock 尚未接管 → Failed to fetch」
 * 这类与本用例无关的失败面（连续跑的第二次实测踩到过）。
 */
function recordDatasetTotal(page: Page, sink: { total: number }): void {
  page.on('response', (res) => {
    const url = new URL(res.url())
    if (url.pathname !== '/api/v1/instances/aggregate') return
    if (url.search) return
    void res
      .json()
      .then((body: { total?: number }) => {
        if (typeof body.total === 'number') sink.total = body.total
      })
      .catch(() => {
        // 响应体不可解析时保持 -1；断言会明确失败，而不是静默通过。
      })
  })
}

/** 断言首页渲染的是哪一种模式（用 `[data-page]` 钩子，不依赖 class 或文案）。 */
async function expectHomeMode(page: Page, mode: 'classic' | 'assistant', ctx: string): Promise<void> {
  const classic = page.locator('[data-page="overview"]')
  const assistant = page.locator('[data-page="assistant-home"]')
  if (mode === 'classic') {
    await expect(classic, `${ctx}：应渲染经典首页（[data-page="overview"]）`).toBeVisible({ timeout: READY_TIMEOUT })
    // 两棵树由路由层二选一、互不嵌套：经典模式下 AI 首页树必须一点都没挂载。
    await expect(assistant, `${ctx}：经典模式不应同时挂载 AI 首页树`).toHaveCount(0)
    await expect(
      page.getByRole('heading', { level: 1, name: '首页' }),
      `${ctx}：经典首页页头标题应为「首页」`,
    ).toBeVisible({ timeout: READY_TIMEOUT })
  } else {
    await expect(assistant, `${ctx}：应渲染 AI 首页（[data-page="assistant-home"]）`).toBeVisible({
      timeout: READY_TIMEOUT,
    })
    await expect(classic, `${ctx}：AI 模式不应同时挂载经典首页树`).toHaveCount(0)
    await expect(
      page.getByRole('heading', { level: 1, name: 'AI 助手' }),
      `${ctx}：AI 首页页头标题应为「AI 助手」`,
    ).toBeVisible({ timeout: READY_TIMEOUT })
  }
}

test.describe('首页模式', () => {
  /** 
   * 用例 A：经典深链刷新不丢，且深链不污染记忆。
   *
   * 守的是 spec §4.1 冻结的优先级（显式 `?home=` > 记忆 > 默认 AI），
   * 以及「深链只对本次渲染生效，不改变下次打开的模式」。
   */
  test('经典深链 ?home=classic 刷新后仍是经典首页，且不污染首页记忆', async ({ page }) => {
    // 本用例要连做 6 次整页导航（深链 / 刷新 / 回首页 / 记忆反证 / 脏值 / 非法参数），
    // 每次都要等懒加载 chunk 落定，实测总时长已超过默认 30s 的用例预算——
    // 那是「步骤多」而不是「某一步卡住」，故放宽用例超时，而不是砍掉反证步骤。
    test.setTimeout(180_000)

    /* 
      ── 为什么先预热一次整页导航 ──

      `helpers.login()` 里等 console-shell 的 20s 是**全局共享**的，不宜为了一个文件的
      并跑场景改它（那会削弱其它用例对「登录慢」的敏感度）。

      但本文件跑在 devmock large（1200 实例）下，若与另两个助手 e2e 文件串行，
      后跑的用例要先等 vite 首屏预构建 + 千级种子在浏览器里生成，登录页自己的
      chunk 也要现下——实测这条路径会顶到 20s，表现为 `login()` 超时，
      而**不是**任何断言失败（单独跑本用例 29.5s 就整轮通过）。

      预热只是一次 `goto`，把首屏与依赖预构建提前做掉；它不改变任何被测属性，
      也不放宽断言——之后 `login()` 仍用它自己的 20s 上限。
    */
    await page.goto('/login', { waitUntil: 'commit' })
    await page.waitForTimeout(1500)

    await login(page)

    // ── 1. 深链进入经典首页 ──
    await page.goto('/?home=classic')
    await expectHomeMode(page, 'classic', '深链 /?home=classic')

    // ── 2. 刷新仍是经典（不闪回 AI）──
    // 用 waitUntil:'commit' 而非默认 'load'：本站首屏要按路由分块下载 chunk，
    // 若在上一步尚未落定时 reload，Playwright 会等到 'load'，期间 mock 的 service worker
    // 接管导航会 abort 那次文档请求，报成 `page.reload: net::ERR_ABORTED`（实测踩到过）——
    // 那是等待策略的问题，不是「刷新丢模式」。commit 之后由 data-page 断言自己等到真正就绪。
    await page.reload({ waitUntil: 'commit' })
    await expectHomeMode(page, 'classic', '刷新 /?home=classic 后')

    // ── 3. 深链不写记忆 ──
    expect(
      await page.evaluate((key) => localStorage.getItem(key), HOME_MODE_KEY),
      '深链 ?home=classic 只对本次渲染生效，不得写入首页记忆（console.homeMode）',
    ).toBeNull()

    // ── 4. 记忆未被污染：回 / 应仍是默认 AI 首页 ──
    await page.goto('/')
    await expectHomeMode(page, 'assistant', '深链之后访问无参数的 /')

    /* 
      反证：避免上面几条 null 断言空转成「记忆机制根本没接线」。
      真的往记忆里写 classic，再访问 / 就必须落经典首页——
      否则「深链不写记忆」与「记忆从不生效」在上面是同一个观测结果。
    */
    await page.evaluate((key) => localStorage.setItem(key, 'classic'), HOME_MODE_KEY)
    await page.goto('/')
    await expectHomeMode(page, 'classic', '记忆为 classic 后访问 /')

    // ── 5. 非法值回落：记忆里放脏值 → 按未设置处理 → AI 首页，且不白屏 ──
    await page.evaluate((key) => localStorage.setItem(key, 'foo'), HOME_MODE_KEY)
    await page.goto('/')
    await expectHomeMode(page, 'assistant', '记忆为非法值 foo 后访问 /')
    expect(
      await page.evaluate(() => document.body.innerText.trim().length),
      '记忆里存在非法值时，首页应回落默认 AI 模式并正常渲染，而不是白屏或报错页',
    ).toBeGreaterThan(50)

    // ── 6. 非法参数值与「未指定」等价（不报错页、不写记忆）──
    await page.evaluate((key) => localStorage.removeItem(key), HOME_MODE_KEY)
    await page.goto('/?home=unknown')
    await expectHomeMode(page, 'assistant', '非法参数 ?home=unknown')
    expect(
      await page.evaluate((key) => localStorage.getItem(key), HOME_MODE_KEY),
      '非法 ?home= 取值应按「未指定」处理，同样不得写入记忆',
    ).toBeNull()

    await page.screenshot({ path: '../.tmp/acceptancehome-mode-deeplink.png', fullPage: false })
  })

  /** 
   * 用例 B：1200 实例规模下 AI 首页摘要区**有界且不轮询**（Network + DOM 双重实测）。
   *
   * 守的是 spec §6（摘要取数一律有界、不轮询）与 §5.1（右栏各区块最多 5 条）。
   */
  test('千级实例下 AI 首页摘要区有界：DOM 不随实例数膨胀、摘要请求有界且不轮询', async ({ page }) => {
    // 落定 + 两个 30s 对照窗，远超默认 30s 的用例预算。
    test.setTimeout(240_000)

    const seen = emptyWatched()
    // 监听必须在 goto 之前挂上，否则漏掉挂载当刻的首批请求。
    watchRequests(page, seen)
    // 数据集规模从页面自己的聚合响应里读（见 recordDatasetTotal 注释）。
    const dataset = { total: -1 }
    recordDatasetTotal(page, dataset)

    await login(page)
    await page.goto('/')
    const home = page.locator('[data-page="assistant-home"]')
    await expect(home, '应进入默认的 AI 首页').toBeVisible({ timeout: READY_TIMEOUT })
    // 「集群水位」只在 /metrics/overview 返回 totals 后才渲染：等它出现，
    // 说明网络侧首批已发完，下面的参数断言与对照测量才有意义。
    await expect(home.getByText('集群水位'), '集群水位区块应在概览返回后渲染').toBeVisible({ timeout: READY_TIMEOUT })

    // ── 前置条件：数据集确实是千级 ──
    // 否则「DOM 不膨胀」会在一份几十条的小数据集上轻松通过，什么也证明不了。
    await expect
      .poll(() => dataset.total, {
        message: '应能观察到 /instances/aggregate 返回的全库实例总数（前置条件取数）',
        timeout: READY_TIMEOUT,
      })
      .toBeGreaterThanOrEqual(1000)
    const datasetTotal = dataset.total

    // ── Network ①：异常实例摘要走服务端搜索，窗口只有 5 条 ──
    // 首页私有签名是 status=CRASHED & page=1 & pageSize=5；顶栏同端点用的是
    // status=CRASHED & pageSize=8（无 page 参数），签名不同，计数不会被顶栏混淆。
    const homeCrashSearch = (p: URLSearchParams) =>
      p.get('status') === 'CRASHED' && p.get('page') === '1' && p.get('pageSize') === '5'
    await expect
      .poll(() => seen.instanceSearches.filter(homeCrashSearch).length, {
        message: '首页「需要关注」应经 /instances/search 取 status=CRASHED&page=1&pageSize=5（只取 5 条）',
        timeout: READY_TIMEOUT,
      })
      .toBeGreaterThan(0)

    // ── Network ②：绝不请求裸数组 GET /instances（那是 1200 条全量）──
    expect(
      seen.bareInstanceListSearches,
      'AI 首页不应请求裸数组 GET /instances：一次拉回全部 1200 条正是本页要避免的全量渲染来源',
    ).toEqual([])

    // ── Network ③：集群水位只带 range=24h（只取 totals，不画趋势图）──
    await expect
      .poll(() => seen.overviewSearches.length, {
        message: '首页「集群水位」应请求 /metrics/overview',
        timeout: READY_TIMEOUT,
      })
      .toBeGreaterThan(0)
    expect(
      [...new Set(seen.overviewSearches)],
      '/metrics/overview 只应带 range=24h（不带 resolution 等额外参数）',
    ).toEqual(['?range=24h'])

    // ── DOM：量纲上的有界 ──
    const dom = await page.evaluate(() => ({
      /** 整页节点总数（对实例数最敏感的那个指标）。 */
      totalNodes: document.querySelectorAll('*').length,
      /** AI 首页树内的节点数，把「外壳」与「页面」分开看。 */
      homeNodes: document.querySelectorAll('[data-page="assistant-home"] *').length,
      /** 「需要关注」里的异常实例链接（每条一个 /instances/:id）。 */
      instanceLinks: document.querySelectorAll('[data-page="assistant-home"] a[href^="/instances/"]').length,
      /** 实例行/卡片候选选择器的合计数：超链接、表格行、带实例标记的元素。 */
      instanceRows: document.querySelectorAll(
        '[data-page="assistant-home"] a[href^="/instances/"], [data-page="assistant-home"] tr, [data-page="assistant-home"] [data-instance-id], [data-page="assistant-home"] [data-slot="instance-card"]',
      ).length,
    }))
    expect(
      dom.totalNodes,
      `AI 首页 DOM 节点数应远低于「按 1200 实例渲染」的量级（实测 716，上界 ${DOM_NODE_UPPER_BOUND}）`,
    ).toBeLessThan(DOM_NODE_UPPER_BOUND)
    expect(dom.instanceLinks, '「需要关注」里异常实例条目最多 5 条（请求窗口 pageSize=5）').toBeLessThanOrEqual(5)
    expect(dom.instanceRows, '页面上不应出现 100+ 个实例行/卡片（那就是把千级实例全渲染了）').toBeLessThan(100)

    // ── 观察窗 1（首页）：统计各端点增量 ──
    const homeMark = { ...seen, overviewSearches: [...seen.overviewSearches] }
    await page.waitForTimeout(WINDOW_MS)
    const homeDelta = {
      overview: seen.overviewSearches.length - homeMark.overviewSearches.length,
      alerts: seen.alertEvents - homeMark.alertEvents,
      conversations: seen.assistantConversations - homeMark.assistantConversations,
      tasksLimit5: seen.tasksWithLimit5 - homeMark.tasksWithLimit5,
    }

    /* 
      ── 关于「近期任务」为什么不再轮询 ──

      本用例最初把 `/tasks?limit=5` 当**灵敏度对照**：它当时复用 `useTasks`，
      只要存在非终态任务就 2s 重取，30s 窗口内能数到 13～14 次，
      正好证明「这个观察窗确实能看见轮询」，从而让「其余端点安静」有意义。

      但那个轮询本身就是缺陷：首页是常开页，长期任务期间等于每 2s 一次全平台任务查询，
      与本文件另两处摘要（异常实例、集群水位）的取舍理由相悖，也违背 spec §6「摘要不轮询」。
      修复后「近期任务」改为私有 key 直调、不设 interval，这条对照随之失去参照物——
      它变红是**预期内的**，不是回归。

      灵敏度改由下面的「对照窗」承担：`/nodes` 上没有首页摘要，但同样挂着顶栏的
      10s `/metrics/overview` 轮询，断言 `controlDelta >= 1` 即证明观察窗有效。
      也就是说，这里不需要再有一条「必须有轮询」的断言，只需要确保
      **首页自己比对照页多出来的部分 ≈0**。
    */
    expect(
      homeDelta.tasksLimit5,
      `「近期任务」(/tasks?limit=5) 也不得周期重取：它此前复用 useTasks（有在途任务时 2s 一拍），` +
        `修复后应为私有 key 直调。窗口内 ${WINDOW_MS / 1000}s 出现 ~15 次即说明轮询被接回来了`,
    ).toBeLessThanOrEqual(POLLING_DELTA_UPPER_BOUND)

    expect(
      homeDelta.alerts,
      `「需要关注」的告警摘要（/alerts/events?pageSize=5）在 ${WINDOW_MS / 1000}s 内不应周期性重取` +
        `（2s 轮询器会给 ~15 次、10s 给 ~3 次，噪声最多 2 次）`,
    ).toBeLessThanOrEqual(POLLING_DELTA_UPPER_BOUND)
    expect(
      homeDelta.conversations,
      `「最近会话」（/assistant/conversations）在 ${WINDOW_MS / 1000}s 内不应周期性重取` +
        `（2s 轮询器会给 ~15 次、10s 给 ~3 次，噪声最多 2 次）`,
    ).toBeLessThanOrEqual(POLLING_DELTA_UPPER_BOUND)

    /* 
      ── 观察窗 2（对照页 /nodes，同样 30s）──
      /nodes 没有任何首页摘要查询，但同样挂着顶栏（10s 轮询 /metrics/overview）并承受同一套噪声。
      于是「首页窗 − 对照窗」只剩下首页自己多加的那部分轮询：没有轮询时差值 ≈0，
      若首页给 /metrics/overview 也接了 10s 轮询，差值会到 ~3（=30s/10s）。
    */
    const controlStart = seen.overviewSearches.length
    await page.goto(CONTROL_PATH)
    await expect(
      page.getByRole('heading', { level: 1 }),
      `对照页 ${CONTROL_PATH} 应正常渲染（否则对照窗测的不是同一套外壳）`,
    ).toBeVisible({ timeout: READY_TIMEOUT })
    // 先让对照页自身落定，再从干净起点计时。
    await page.waitForTimeout(3000)
    const controlMark = seen.overviewSearches.length
    await page.waitForTimeout(WINDOW_MS)
    const controlDelta = seen.overviewSearches.length - controlMark

    expect(
      controlDelta,
      `对照页 ${CONTROL_PATH} 在 ${WINDOW_MS / 1000}s 内应观察到顶栏的 /metrics/overview 轮询（≥1 次）；` +
        '若为 0，说明对照窗没测到任何东西，下面的差值比较无效',
    ).toBeGreaterThanOrEqual(1)
    expect(
      homeDelta.overview - controlDelta,
      `首页比对照页多出的 /metrics/overview 请求数应 ≈0（首页自己不得再接一个 10s 轮询；` +
        `差值达到 ~3 就说明首页把轮询接回来了）。实测首页 ${homeDelta.overview} 次 / 对照 ${controlDelta} 次`,
    ).toBeLessThanOrEqual(POLLING_DELTA_UPPER_BOUND)

    // 观测值留痕：失败时这几行直接说明「当时的规模与计数」，省掉复现成本。
    console.log(
      `DOM total=${dom.totalNodes} home=${dom.homeNodes} instanceLinks=${dom.instanceLinks} ` +
        `rows=${dom.instanceRows} datasetTotal=${datasetTotal} | ` +
        `home${WINDOW_MS / 1000}s=${JSON.stringify(homeDelta)} control${WINDOW_MS / 1000}s(overview)=${controlDelta} ` +
        `(controlStart=${controlStart})`,
    )

    // 回到首页截图存档（对照页已无意义，截的是被测页面）。
    await page.goto('/')
    await expect(home, 'AI 首页应可再次进入（截图存档用）').toBeVisible({ timeout: READY_TIMEOUT })
    await page.screenshot({ path: '../.tmp/acceptanceassistant-home-large-1200.png', fullPage: false })
  })
})
