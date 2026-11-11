import { type Locator, type Page, expect } from '@playwright/test'

/**
 * 经 mock 登录页用种子管理员（admin/admin123）登录，等待**控制台外壳**就绪。
 *
 * 【为什么不等 `[data-page="overview"]`】那是首页的**内容**节点，其渲染依赖
 * `/metrics/overview` 等一组查询；导航重构引入路由分块后首屏要多下载几个 chunk，
 * 实测从登录到内容就绪可超过 5 秒——而 Playwright 默认断言超时正是 5 秒，
 * 于是每一个调用 login() 的用例都会卡在这一行上失败（曾一次波及 39 个用例）。
 *
 * 外壳 `[data-slot="console-shell"]` 在鉴权通过后立即挂载，语义上它才是「已进入控制台」
 * 的标志；各用例真正关心的页面元素由它们自己断言，不该由公共 helper 代劳。
 *
 * 注：mock 调试面板曾钉在右下角挡住表格行按钮，现由 mount-mock-control-panel 在
 * `navigator.webdriver` 下直接不挂载（见该文件注释），这里无需再做处理。
 */
export async function login(page: Page, username = 'admin', password = 'admin123'): Promise<void> {
  await page.goto('/login')
  await waitForMockReady(page)
  await fillStable(page.getByLabel('用户名', { exact: true }), username)
  await fillStable(page.getByLabel('密码', { exact: true }), password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  // 【为何是 45s】登录后要拉 /auth/me + 权限节点 + 首屏路由 chunk；shard 的**第一个**用例
  // 还要等 vite 依赖预构建（首例几十秒很正常）。实测 ops-console-layout / bots-load-wizard
  // 首轮卡在登录就绪、**retry 就过**（同一 runner、同一份代码）—— 是冷启动预算不够，
  // 不是功能坏，也不是断言错。45s 只影响首次等待，不影响后续用例（模块已缓存）。
  // 注意：这不是放宽断言，真实登不进去仍会失败（只是晚 15s）。
  await expect(page.locator('[data-slot="console-shell"]')).toBeVisible({ timeout: 45_000 })
}

/**
 * 等 mock 模式的 Service Worker 真的接管完再继续。
 *
 * 【为何必须等】页面是「先 render，mock 就绪后 invalidateQueries() 重取」。
 * 这能救 React Query 的查询，但**登录是命令式流程**，不走缓存。
 * 慢环境（CI 首个用例还要等 vite 依赖预构建）下，首批请求会抢在 SW 接管前
 * 发出——它们穿透到 vite proxy 指向的空端口（mock 模式的 `http://127.0.0.1:9`），
 * 确定性 ECONNREFUSED，鉴权失败后页面就停在登录页，等 45s 也没用。
 * 这正是四个 shard 里**每个 shard 的第 1 个用例**稳定挂掉的原因。
 *
 * 标记由 main.tsx 在 `await startMockWorker()` 之后写入。非 mock 模式（真后端）
 * 不会有这个标记，但那条路径也不跑 e2e。
 */
async function waitForMockReady(page: Page): Promise<void> {
  await page.waitForFunction(
    () => (window as unknown as { __JM_MOCK_READY__?: boolean }).__JM_MOCK_READY__ === true,
    undefined,
    { timeout: 90_000 },
  )
}

/**
 * 往登录表单的**受控**输入框里填值，并确认值真的留住了才继续。
 *
 * 【为何不能只 `fill()` 一次】登录页是 React 受控表单（`value` + `onChange` →
 * `LoginPageView` 的 `useState`）。`fill()` 是「一次性写入整串 + 派发一个 input 事件」，
 * 与人类逐字输入不同：它只有**一次** state 更新机会。
 *
 * mock 就绪后 `main.tsx` 会 `invalidateQueries()`，让 `/setup/status` 等查询重来一次，
 * 于是 `LoginPage` 重渲染；CI runner 上主线程常被 vite 编译大 chunk 占住，这条 state 更新
 * 会排在重渲染之后。React 重渲染时按**旧 state（空串）**写回受控 value，DOM 里刚填进去的
 * 密码就被抹掉了。
 *
 * 后果不是「登录失败」而是**根本没登录**：点「登录」时浏览器原生 `required` 校验拦下表单
 * （trace 截图里那句 “Please fill out this field”），请求一个都没发，页面停在登录页，
 * 45 秒后报的却是 `[data-slot="console-shell"]` 不可见 —— 与真因毫无关系，极难定位。
 * 实测 trace：用户名（先填，多出 280ms 给 React 处理）保住了，密码（后填，紧跟着点击）被清空。
 *
 * 所以：填 → 等一拍 → 回读，被冲掉就重填，连续两次读数都正确才算稳。
 */
async function fillStable(locator: Locator, value: string): Promise<void> {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    await locator.fill(value)
    // 给「可能排队的重渲染」一个把值冲掉的机会；冲掉了下一轮就重填。
    await locator.page().waitForTimeout(50)
    if ((await locator.inputValue()) !== value) continue
    // 连续两次读都正确，才认为这次填进去的值已经落到 React state 上。
    await locator.page().waitForTimeout(50)
    if ((await locator.inputValue()) === value) return
  }
  // 十轮都没稳住：交给断言给出可读的失败（而不是带着空字段去点登录，报成别的错）。
  await expect(locator).toHaveValue(value)
}

/**
 * 把首页模式固定为「经典」，供需要经典首页（`[data-page="overview"]`）的用例使用。
 *
 * 【为什么需要】`/` 的默认模式已改为 AI 首页（`DEFAULT_HOME_MODE = 'assistant'`），
 * 而经典首页的 `data-page="overview"` 只在记忆为 classic 或深链带 `?home=classic` 时才会出现。
 * 靠 localStorage 记忆而不是每次都改写 URL，是因为多数用例进首页不是靠 `goto('/')`，
 * 而是靠登录后的默认落地 —— 那时已经没有“改 URL”的机会了。
 *
 * 必须在**任何导航之前**调用（`addInitScript` 在每次文档创建时都会跑，比 `evaluate` 可靠）。
 *
 * 【为何不叫 useXxx】它不是 React Hook，只是个 async 工具函数；但以 `use` 开头会被
 * `react-hooks/rules-of-hooks` 当成 Hook（已在 navigation-benchmark 里报 error）。
 */
export async function pinClassicHome(page: Page): Promise<void> {
  await page.addInitScript(() => {
    try {
      localStorage.setItem('console.homeMode', 'classic')
    } catch {
      // 隐私模式下列 localStorage 可能抛错；失败即退化为默认模式，由调用方的断言报错。
    }
  })
}

/** 在服务端搜索型 Combobox 中输入并选择精确选项。 */
export async function selectComboboxOption(page: Page, triggerText: string, optionText: string): Promise<void> {
  const trigger = page.locator('[data-slot="combobox-trigger"]').filter({ hasText: triggerText }).first()
  await expect(trigger).toBeVisible()
  await trigger.click()

  const content = page.locator('[data-slot="combobox-content"]')
  await expect(content).toBeVisible()
  await content.locator('input').fill(optionText)
  const option = content.getByRole('button', { name: optionText, exact: true })
  await expect(option).toBeVisible()
  await option.click()
  // 提交后触发器显示所选项，确认选中生效且弹层已关闭。
  await expect(page.getByRole('button', { name: optionText, exact: true })).toBeVisible()
}
