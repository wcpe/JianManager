import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

/** 
 *后端快照不变 —— **真实 CP 验收**（非 devmock）。
 *
 * ## 为什么不放在 mock 用例里
 *
 * devmock 的 assistant 域会**读取** `contextSnapshot` 但**不落库**
 * （见 `packages/devmock/src/handlers/domains/assistant.ts` 的 SubmitRun）。
 * 因此「换页后已发送消息的快照不变」这条验收在 mock 下**物理上无法验证**——
 * 断言只能落到前端发出的请求体上，而 spec 要的恰恰是**后端存下来的那份**。
 *
 * 本文件直连真实 CP，用真实数据库断言 `assistant_runs.context_snapshot`。
 *
 * ## 被验证的约定
 *
 * `AssistantRun.ContextSnapshot` 的字段注释写明：**不随配置变更回写**——
 * 「事后追责要还原的是『当时用的什么』，不是今天的配置」。
 * 换成页面上下文同理：一轮消息带着「实例页 + 某个实例」发出后，
 * 用户之后切到节点页并继续对话，**先前那轮的快照必须原样保留**。
 * 若实现把快照存在会话级共享字段里、每轮覆盖，历史轮次就会显示成
 * 「它当时在看节点页」——那是**伪造的证据**，比缺字段更危险。
 *
 * ## 运行前提
 *
 *   CP：真实二进制 + 隔离库（本文件按 `FR518_CP_DB` 指向该库做断言）
 *   FE：非 mock 的 vite dev server，`VITE_API_TARGET` 指向该 CP
 *   baseURL 由 `FR518_BASE_URL` 给出（默认 127.0.0.1:5299）
 *
 * 缺任一前提即 skip，并**明写「未验证」而不是静默通过**。
 */

const BASE_URL = process.env.FR518_BASE_URL ?? 'http://127.0.0.1:5299'
// 不写死绝对路径：本文件入了仓库，写死本机路径会泄漏用户名与目录结构。
// 未设 FR518_CP_DB 时下面的 skip 会生效并明写「未验证」。
const CP_DB = process.env.FR518_CP_DB ?? ''
const ADMIN_USER = process.env.FR518_USER ?? 'admin'
const ADMIN_PASS = process.env.FR518_PASS ?? 'fr514live'

test.use({ baseURL: BASE_URL })

/** 
 * 直接查真实库里的快照（这是本文件的**主张要点**：断言落在持久化层）。
 *
 * 用 sqlite3 CLI 而不是再起一个 Go 进程：只读一行，依赖面最小。
 * `mode=ro` 保证不会因为我们查询而改动被测库。
 */
function readSnapshot(runId: number): string | null {
  const out = execFileSync(
    'sqlite3',
    [`file:${CP_DB}?mode=ro`, `SELECT COALESCE(context_snapshot,'<NULL>') FROM assistant_runs WHERE id=${runId};`],
    { encoding: 'utf8' },
  ).trim()
  return out === '' || out === '<NULL>' ? null : out
}

test.describe('后端快照（真实 CP）', () => {
  test.skip(
    !process.env.FR518_CP_DB && process.env.FR518_REQUIRE_REAL !== '1',
    '需要真实 CP：设置 FR518_CP_DB 指向隔离库、FR518_BASE_URL 指向非 mock 前端；' +
      '本用例验的是后端落库的快照，devmock 无法替代',
  )

  test('换页后，已发送轮次的 context_snapshot 原样不变', async ({ page, request }) => {
    test.setTimeout(180_000)

    // ── 准备：经真实 CP 登录，拿 token（前端本身也走这条，这里为了直接断言接口返回）──
    const loginResp = await request.post(`${BASE_URL}/api/v1/auth/login`, {
      data: { username: ADMIN_USER, password: ADMIN_PASS },
    })
    expect(loginResp.ok(), `真实 CP 登录失败（${loginResp.status()}）：请确认 CP 在跑且账号可用`).toBeTruthy()
    const token = (await loginResp.json()).accessToken as string

    // ── 浏览器侧：注入登录态后进入实例页 ──
    await page.goto('/login')
    await page.evaluate((t) => {
      localStorage.setItem('accessToken', t)
    }, token)
    await page.goto('/instances')
    await expect(
      page.getByRole('heading', { level: 1 }),
      '实例页应正常渲染（真实 CP 下需要库里有实例）',
    ).toBeVisible({ timeout: 45_000 })

    // 打开助手浮窗
    await page.getByRole('button', { name: '打开 AI 助手' }).click()
    // 浮窗是 `<aside aria-label="AI 助手">`（complementary landmark），不是 dialog——
    // 用 dialog 会等不到。定位口径与 mock 用例保持一致，避免两处漂移。
    const panel = page.getByRole('complementary', { name: 'AI 助手' })
    await expect(panel, '助手浮窗应可见').toBeVisible({ timeout: 20_000 })

    /** 
     * 发一轮消息并返回 runId。
     *
     * 两个必要的准备动作，都是**真实后端相比 mock 才暴露的约束**：
     *  - 先点「新会话」：后端对同一会话有「单活跃 run」的部分唯一索引，
     *    上一轮若仍停在 waiting_confirmation / running，再提交会得到
     *    409 `CONVERSATION_BUSY`（mock 不实现该状态机，所以 mock 用例遇不到）。
     *  - 用**新的 panel 引用**：切页后浮窗虽不卸载，但重新取一次更稳。
     */
    const sendInNewConversation = async (p: ReturnType<typeof page.getByRole>, text: string): Promise<number> => {
      await p.getByRole('button', { name: '新会话' }).click()
      await page.waitForTimeout(300)

      const runsResp = page.waitForResponse(
        (r) => /\/api\/v1\/assistant\/conversations\/\d+\/runs$/.test(r.url()) && r.request().method() === 'POST',
        { timeout: 60_000 },
      )
      const input = p.getByRole('textbox').first()
      await input.fill(text)
      await input.press('Enter')
      const resp = await runsResp
      expect(resp.ok(), `提交 run 失败：${resp.status()} ${await resp.text()}`).toBeTruthy()
      const body = await resp.json()
      return Number(body.runId)
    }

    const run1 = await sendInNewConversation(panel, '第一轮：看实例页的上下文')
    const snap1 = readSnapshot(run1)
    expect(snap1, `第一轮 run ${run1} 应在库里落下 context_snapshot`).not.toBeNull()

    const parsed1 = JSON.parse(snap1 as string)
    expect(parsed1.pageKey, '第一轮快照应记录实例页').toBe('instances')
    // `entities` 允许为 null：实例页未选中具体实体时后端就存 null
    // （实测真实落库形状 `{v:1,pageKey:"instances",entities:null,filters:{groupBy:"region"}}`）。
    // 断言「要么 null、要么数组」而不是硬要求数组——把契约写成实现碰巧的样子是另一种脆弱。
    expect(
      parsed1.entities === null || Array.isArray(parsed1.entities),
      `快照的 entities 应为 null 或数组，实际 ${JSON.stringify(parsed1.entities)}`,
    ).toBeTruthy()
    // filters 是页面上下文的实质内容：换页后它必须仍是实例页那一份。
    expect(
      typeof parsed1.filters === 'object' && parsed1.filters !== null,
      '实例页快照应带 filters（分组/筛选是页面上下文的实质内容）',
    ).toBeTruthy()

    // ── 关键动作：切到**另一个**页面（节点页），让页面上下文变化 ──
    await page.goto('/nodes')
    await expect(
      page.getByRole('heading', { level: 1 }),
      '节点页应正常渲染',
    ).toBeVisible({ timeout: 45_000 })
    await page.waitForTimeout(1500) // 让 usePageContext 上报新页上下文

    // ── 第二轮：在节点页再发一条，制造「后续轮次」──
    // 若实现把快照存会话级共享字段，这一轮会把第一轮的值覆盖掉。
    await page.getByRole('button', { name: '打开 AI 助手' }).click()
    const panel2 = page.getByRole('complementary', { name: 'AI 助手' })
    await expect(panel2, '助手浮窗应再次可见').toBeVisible({ timeout: 20_000 })
    const run2 = await sendInNewConversation(panel2, '第二轮：已经切到节点页了')
    const snap2 = readSnapshot(run2)
    expect(snap2, `第二轮 run ${run2} 应落下 context_snapshot`).not.toBeNull()
    expect(
      JSON.parse(snap2 as string).pageKey,
      '两轮快照的 pageKey 必须不同——否则「换页」这个前提没成立，本用例测不到东西',
    ).toBe('nodes')

    // ── 核心断言：第一轮的快照**一个字都没变** ──
    const snap1After = readSnapshot(run1)
    expect(
      snap1After,
      `第一轮 run ${run1} 的快照被改写了！` +
        '这违反「不随配置变更回写」的约定：历史轮次显示的是它当时看到的上下文，' +
        '不是今天的页面。被改成：' +
        String(snap1After).slice(0, 200),
    ).toBe(snap1)

    // ── 补充断言：两轮各自的快照互相独立，不是同一份 ──
    expect(snap2, '两轮快照不应完全相同（否则可能是同一条记录被读了两遍）').not.toBe(snap1)

    // 观测值留痕：失败时直接看到两轮快照长什么样。
    console.log(
      `run1=${run1} pageKey=${JSON.parse(snap1 as string).pageKey} | ` +
        `run2=${run2} pageKey=${JSON.parse(snap2 as string).pageKey} | ` +
        `run1 事后仍为：${JSON.parse(snap1After as string).pageKey}`,
    )
  })
})
