import { test, expect, type Locator, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-518 §5.2：上下文芯片「移除即不再外发」与「发送时冻结」两条验收。
 *
 * 【为什么这两条必须抓包，而不是看 UI】
 * 芯片被移除后从 DOM 消失，只证明**渲染层**把它去掉了；真正要守的是「它没有出现在
 * 这一轮发给模型的请求体里」。这两件事完全可以背离：
 *  - 芯片用了本地隐藏状态，而 `buildSnapshot()` 仍读着含该项的数组 → 界面上没有、请求里还有；
 *  - 反过来，芯片还在却已从快照里被摘掉 → 用户以为带上了，实际没带。
 * 两种情况在截图上都看不出来，只有请求体是事实。
 * 同理，「发送时冻结」的可观察形态就是**每一轮请求体各自带着发送那一刻的页面上下文**：
 * 换页之后发出的第二轮必须只带新页面，第一轮那份不能再被改写，两轮也不能互相累积。
 *
 * 【mock 的验证边界（诚实说明，避免以为验了更多）】
 * devmock 收到 `contextSnapshot` 只读取、不落库（见 packages/devmock 的 runs handler），
 * 因此「后端存下来的快照不被后续页面切换影响」在 mock 下**无法**验证。
 * 本文件能验的是「前端发出的请求体」——那正是芯片与序列化逻辑的全部产出。
 */

/**
 * 目标实例取 devmock 的**具名种子覆盖行**（`packages/devmock` 的 INSTANCE_SEED_OVERRIDES）。
 *
 * 不要用 `server-0001` 这类"按序号推出来"的名字：生成名是 `server-<id padStart 4>`，
 * 但 id=1/2/3/10/11/12/20… 被种子覆盖成了 `survival-1`/`lobby-proxy`/`creative-1`，
 * 于是 `server-0001` 在 mock 里**根本不存在**，服务端 q 一条不返，表现为"下拉里选不到"。
 * `survival-1` 是声明式种子且是唯一含该子串的行（survival-lobby/world/proxy 都不含）。
 */
const TARGET_INSTANCE_NAME = 'survival-1'
const TARGET_INSTANCE_ID = 1
/** 该实例芯片的可读标签（page-context.ts 的 ENTITY_LABEL + `type-id` 格式）。 */
const TARGET_CHIP_LABEL = `实例：instance-${TARGET_INSTANCE_ID}`

/** 一条已抓到的 `POST /assistant/conversations/:id/runs`。 */
interface CapturedRun {
  /** 原始请求体字符串。HTTP 一旦发出即冻结，后续页面切换不可能改写它。 */
  raw: string
  /** 请求体里 `contextSnapshot` 的原文（null 表示前端明确没带上下文，不是"抓漏了"）。 */
  snapshotRaw: string | null
  /** 解析后的快照对象（解析失败时为 null，由断言报成失败而不是静默跳过）。 */
  snapshot: Record<string, unknown> | null
}

/**
 * 收集发往 `/assistant/conversations/:id/runs` 的 POST 请求体。
 *
 * 必须在 `page.goto` 之前调用：请求监听要赶在第一次发送前挂上，否则第一轮就漏掉了。
 */
function captureRunRequests(page: Page): CapturedRun[] {
  const captured: CapturedRun[] = []
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

    const raw = req.postData() ?? ''
    let snapshotRaw: string | null = null
    try {
      const body = JSON.parse(raw) as { contextSnapshot?: unknown }
      snapshotRaw = typeof body.contextSnapshot === 'string' ? body.contextSnapshot : null
    } catch {
      // 留着 snapshotRaw=null，下面的断言会把它报成"contextSnapshot 缺失"。
    }
    let snapshot: Record<string, unknown> | null = null
    if (snapshotRaw !== null) {
      try {
        snapshot = JSON.parse(snapshotRaw) as Record<string, unknown>
      } catch {
        snapshot = null
      }
    }
    captured.push({ raw, snapshotRaw, snapshot })
  })
  return captured
}

/** 取第 n 轮的快照，并把「没抓到 / null / 非法 JSON」三种情况分别报成可读的失败。 */
function snapshotOf(runs: CapturedRun[], index: number, when: string): Record<string, unknown> {
  const run = runs[index]
  expect(run, `${when}：应当已抓到第 ${index + 1} 次 POST /runs，实际只抓到 ${runs.length} 次`).toBeTruthy()
  expect(
    run.snapshotRaw,
    `${when}：请求体里的 contextSnapshot 为 null——前端一个字段都没外发，说明页面上下文根本没上报`,
  ).not.toBeNull()
  expect(
    run.snapshot,
    `${when}：contextSnapshot 不是合法 JSON（原文前 200 字：${String(run.snapshotRaw).slice(0, 200)}）`,
  ).not.toBeNull()
  return run.snapshot as Record<string, unknown>
}

/** 快照里的实体数组（形状不对就当空数组，避免断言抛在类型上而不是业务上）。 */
function entitiesOf(snapshot: Record<string, unknown>): Array<Record<string, unknown>> {
  const raw = snapshot.entities
  return Array.isArray(raw) ? (raw as Array<Record<string, unknown>>) : []
}

/** 快照里是否含某个类型化实体引用（id 序列化时统一转成了字符串）。 */
function hasEntity(
  entities: Array<Record<string, unknown>>,
  type: string,
  id: string | number,
): boolean {
  return entities.some((e) => e.type === type && String(e.id) === String(id))
}

/** 深遍历找出所有名为 `label` 的键路径（用路径而不是字符串包含：值里出现 label 不算违规）。 */
function labelPaths(value: unknown, path = '$'): string[] {
  if (Array.isArray(value)) {
    return value.flatMap((item, i) => labelPaths(item, `${path}[${i}]`))
  }
  if (value && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).flatMap(([k, v]) => {
      const here = `${path}.${k}`
      return k === 'label' ? [here, ...labelPaths(v, here)] : labelPaths(v, here)
    })
  }
  return []
}

/**
 * 发一轮消息并等这一轮真正收尾。
 *
 * 收尾判据取两件事的组合，都不能只看「文本出现」：
 *  - mock 的完整回复文本到达（三段 text_delta 全推完）；
 *  - 「停止生成」按钮消失（`running` 已回 false，即 run_end 已被处理、可以发下一轮）。
 * 否则第二轮会被 `send` 开头的 `if (running) return` 静默吞掉，表现为"点了没反应"。
 */
async function sendRound(panel: Locator, text: string): Promise<void> {
  const input = panel.getByLabel('输入消息')
  await input.fill(text)

  const sendButton = panel.getByRole('button', { name: '发送', exact: true })
  await expect(sendButton, '填入文本且上一轮已收尾时，发送按钮应可用').toBeEnabled()
  await sendButton.click()

  // devmock 的回复固定包含「收到你的问题：「<原文前 60 字>」」，用它确认本轮流已到齐。
  await expect(
    panel.getByRole('log'),
    `第「${text}」轮应收到 devmock 的完整回复（含被截到 60 字的原文）`,
  ).toContainText(`收到你的问题：「${text.slice(0, 60)}`, { timeout: 20_000 })
  await expect(
    panel.getByRole('button', { name: '停止生成' }),
    '本轮 run_end 后应退出生成中状态（否则下一轮发送会被 running 守卫吞掉）',
  ).toHaveCount(0, { timeout: 20_000 })
}

/** 打开壳层顶栏的助手浮窗。 */
async function openAssistantDock(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  const panel = page.getByRole('complementary', { name: 'AI 助手' })
  await expect(panel, '助手浮窗应可见').toBeVisible()
  return panel
}

/**
 * 通过「添加实体」把目标实例挂成本轮上下文。
 *
 * 走服务端搜索（千级实例不全量列举），因此必须真的键入关键字并选中返回项——
 * 直接点开下拉就选，等于在测「前 50 条候选里恰好有它」，那是靠种子排序蒙对的假通过。
 */
async function addInstanceEntity(panel: Locator, page: Page): Promise<void> {
  await panel.getByRole('button', { name: '添加实体' }).click()

  const trigger = panel.locator('[data-slot="combobox-trigger"]')
  await expect(trigger, '点「添加实体」后应出现实例选择器').toBeVisible()
  await trigger.click()

  const content = page.locator('[data-slot="combobox-content"]')
  await expect(content, '实例选择器的候选弹层应展开').toBeVisible()
  await content.locator('input').fill(TARGET_INSTANCE_NAME)

  // 先用断言等候选出现，而不是直接 click()：click 失败只报"等不到这个元素"，
  // 报不出下拉里到底是什么。这一步最容易因为种子里没有该实例而失败。
  const option = content.getByRole('button', { name: TARGET_INSTANCE_NAME, exact: true })
  await expect(
    option,
    `服务端搜索「${TARGET_INSTANCE_NAME}」后，下拉里应出现同名候选——` +
      '首版取 server-0001 时这里是空的（该 id 被种子覆盖成 survival-1），' +
      `下拉当前内容：${JSON.stringify(await content.innerText().catch(() => '<读取失败>'))}`,
  ).toBeVisible()
  await option.click()

  // 选中后选择器收起、芯片出现：这一步成功才说明实体真的进了上下文。
  await expect(
    panel.getByRole('button', { name: `移除「${TARGET_CHIP_LABEL}」` }),
    `添加成功后应出现「${TARGET_CHIP_LABEL}」芯片`,
  ).toBeVisible()
}

test.describe.configure({ timeout: 120_000 })

test.describe('FR-518 页面上下文芯片的外发行为', () => {
  test('移除芯片后，该实体不再出现在发出的 contextSnapshot 里', async ({ page }) => {
    const runs = captureRunRequests(page)

    await login(page)
    await page.goto('/instances')
    await expect(
      page.locator('[data-page="instances"]'),
      '/instances 页面容器应就绪（实例列表是千级数据，dev 首次进入该路由还要下载 chunk）',
    ).toBeVisible({ timeout: 45_000 })

    const panel = await openAssistantDock(page)

    // 白名单页（/instances）应已上报自身页上下文，芯片是它的可见产物。
    // 这条同时是「注册端已接通」的证据——没有它，后面的实体断言会在空上下文上做，结论不成立。
    await expect(
      panel.getByRole('button', { name: '移除「当前页：实例」' }),
      '/instances 应上报「当前页：实例」上下文（注册端接通）',
    ).toBeVisible()

    await addInstanceEntity(panel, page)

    // ── 第一轮：先证明"确实加上了" ──
    await sendRound(panel, '第一轮：这条带着实例上下文')

    await expect
      .poll(() => runs.length, { message: '第一轮应抓到一次 POST /runs', timeout: 20_000 })
      .toBe(1)

    const first = snapshotOf(runs, 0, '移除前那一轮')
    expect(first.pageKey, '移除前那一轮的 pageKey 应是 instances').toBe('instances')
    expect(
      hasEntity(entitiesOf(first), 'instance', TARGET_INSTANCE_ID),
      `移除前那一轮的 contextSnapshot.entities 应包含 {type:'instance', id:'${TARGET_INSTANCE_ID}'}——` +
        '这是基准：没有它，后面"移除了"的结论分不清是移除生效还是压根没加上',
    ).toBe(true)
    expect(
      labelPaths(first),
      'contextSnapshot 不应外发 label 字段：label 是本地化的展示文案，只用于 UI，' +
        '外发会让后端误把它当实体名称（名称属于按权限回填的内容，不是前端凭据）',
    ).toEqual([])

    // ── 移除芯片 ──
    await panel.getByRole('button', { name: `移除「${TARGET_CHIP_LABEL}」` }).click()
    await expect(
      panel.getByRole('button', { name: `移除「${TARGET_CHIP_LABEL}」` }),
      '点移除后该芯片应从 DOM 消失',
    ).toHaveCount(0)
    // 移除实体不该连带移除「当前页」：那是另一项，用户没动它。
    await expect(
      panel.getByRole('button', { name: '移除「当前页：实例」' }),
      '移除实体芯片不应连带移除「当前页」芯片',
    ).toBeVisible()

    // ── 第二轮：抓包证明它真的没外发 ──
    await sendRound(panel, '第二轮：移除后这条不该再带实例')

    await expect
      .poll(() => runs.length, { message: '第二轮应抓到第二次 POST /runs', timeout: 20_000 })
      .toBe(2)

    const second = snapshotOf(runs, 1, '移除后那一轮')
    expect(
      hasEntity(entitiesOf(second), 'instance', TARGET_INSTANCE_ID),
      `移除后那一轮的 contextSnapshot.entities 不应再包含 instance ${TARGET_INSTANCE_ID}——` +
        '芯片从界面消失不等于它没被发出去，只有请求体为空才能证明"移除即不再外发"',
    ).toBe(false)
    expect(
      entitiesOf(second),
      '移除唯一一个实体后，entities 应为空数组（当前页上下文仍在，因此快照本身不该为 null）',
    ).toEqual([])
    expect(second.pageKey, '移除实体不应影响页面级上下文，pageKey 仍是 instances').toBe('instances')
    expect(labelPaths(second), 'contextSnapshot 不应外发 label 字段').toEqual([])
  })

  test('跨页切换时两轮快照各自独立，且浮窗与会话不随路由丢失', async ({ page }) => {
    const runs = captureRunRequests(page)

    await login(page)
    await page.goto('/instances')
    await expect(
      page.locator('[data-page="instances"]'),
      '/instances 页面容器应就绪（实例列表是千级数据，dev 首次进入该路由还要下载 chunk）',
    ).toBeVisible({ timeout: 45_000 })

    const panel = await openAssistantDock(page)
    await expect(
      panel.getByRole('button', { name: '移除「当前页：实例」' }),
      '/instances 应上报自身页上下文',
    ).toBeVisible()

    // ── 第一轮（在 /instances） ──
    await sendRound(panel, '第一轮：在实例页发的')
    await expect.poll(() => runs.length, { message: '第一轮应抓到一次 POST /runs', timeout: 20_000 }).toBe(1)
    const first = snapshotOf(runs, 0, '实例页那一轮')
    const firstRaw = runs[0].raw

    expect(first.pageKey, '实例页那一轮的 pageKey 应是 instances').toBe('instances')
    // instances 页上报的枚举筛选（默认分组维度 region）——用来区分"两轮互不累积"。
    expect(
      first.filters,
      '实例页那一轮应带上该页的枚举筛选（groupBy）',
    ).toMatchObject({ groupBy: expect.any(String) })

    // ── 客户端导航到 /nodes（不整页重载，浮窗需跨路由常驻） ──
    const sidebar = page.locator('[data-slot="console-sidebar"]')
    await sidebar.getByRole('link', { name: /^节点/ }).click()
    // 先断言路由真的切了（URL 是即时信号），再等页面容器——
    // 两者分开报错才能区分「没导航」与「导航了但页面还没渲染出来」。
    await expect(page, '点侧栏「节点」后应导航到 /nodes（客户端路由，不整页重载）').toHaveURL(/\/nodes$/)
    await expect(
      page.locator('[data-page="nodes"]'),
      '/nodes 页面容器应就绪（该路由是独立 chunk，dev 下首次进入需现下载）',
    ).toBeVisible({ timeout: 45_000 })

    // 顺带守住"上下文跟着页面走"：芯片文案应换成节点页的。
    // 这条是第二轮快照的**前提**，没有它，pageKey 变了也可能只是碰巧。
    await expect(
      panel.getByRole('button', { name: '移除「当前页：节点」' }),
      '切到 /nodes 后上下文芯片应变为「当前页：节点」',
    ).toBeVisible()

    // 浮窗跨路由常驻、上一轮的消息仍可见（会话不随路由丢）。
    await expect(panel, '切换路由后助手浮窗应仍然挂载').toBeVisible()
    await expect(
      panel.getByRole('log'),
      '切换路由后上一轮问答仍应可见（会话状态挂在壳层而不是页面里）',
    ).toContainText('第一轮：在实例页发的')

    // ── 第二轮（在 /nodes） ──
    await sendRound(panel, '第二轮：切到节点页发的')
    await expect.poll(() => runs.length, { message: '第二轮应抓到第二次 POST /runs', timeout: 20_000 }).toBe(2)

    const second = snapshotOf(runs, 1, '节点页那一轮')

    expect(
      second.pageKey,
      `节点页那一轮的 pageKey 应是 nodes（实际 ${String(second.pageKey)}）——` +
        '两轮 pageKey 不同才说明每轮取的是"发送那一刻"所在页面的上下文',
    ).toBe('nodes')
    expect(
      second.pageKey,
      '两轮 pageKey 必须不同：相同意味着快照没在发送时取值（例如被缓存或复用上一轮）',
    ).not.toBe(first.pageKey)
    expect(
      second.filters,
      '节点页那一轮不应带上实例页的 filters——带上就是两轮上下文互相累积，而不是各自独立',
    ).toBeUndefined()

    // 第一轮的请求体在第二轮之后仍是原样：HTTP 发出即冻结，这里守住的是
    // "实现没有把快照对象做成就地可变、跨轮复用"——真可变时序列化会带出后来的值。
    expect(runs[0].raw, '第一轮请求体不应被第二轮改写（快照在发送那一刻冻结）').toBe(firstRaw)
    expect(
      (JSON.parse(firstRaw) as { contextSnapshot?: string }).contextSnapshot,
      '第一轮 contextSnapshot 里的 pageKey 应仍为 instances',
    ).toContain('"pageKey":"instances"')
  })
})
