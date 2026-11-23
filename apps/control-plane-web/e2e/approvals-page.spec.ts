import { test, expect } from '@playwright/test'

/** 
 *平台审批中心 · 三档视口验收。
 *
 * 为什么要有这条 e2e：审批台是表格页，列多（操作/目标/申请人/理由/状态/时间/决策按钮）。
 * 窄屏下若只是「DOM 里还在、实际被裁掉」，`toBeVisible` 仍会通过 —— 那验不到真问题。
 * 所以这里断言的是**几何**：表格容器不得横向溢出，决策按钮必须落在视口内且可点。
 *
 * 登录：经真实 `POST /auth/login` 拿 token 写进 localStorage（应用就是把 token 存在那里，
 * 见 stores/auth.ts），而不是走登录页 UI —— 本用例关心的是表格布局，不是登录表单。
 * 凭据由环境变量提供，缺省用本仓 devmock 的种子管理员。
 */

const USERNAME = process.env.E2E_USERNAME ?? 'admin'
const PASSWORD = process.env.E2E_PASSWORD ?? 'admin123'

/** 
 * 登录并落到应用里。返回后端是否可达 —— 不可达时用例明确跳过而不是假装通过。
 */
async function signIn(page: import('@playwright/test').Page): Promise<boolean> {
  const resp = await page.request.post('/api/v1/auth/login', {
    data: { username: USERNAME, password: PASSWORD },
  })
  if (!resp.ok()) return false
  const body = (await resp.json()) as { accessToken?: string; refreshToken?: string }
  if (!body.accessToken) return false
  await page.addInitScript(
    ([access, refresh]) => {
      localStorage.setItem('accessToken', access as string)
      localStorage.setItem('refreshToken', refresh as string)
    },
    [body.accessToken, body.refreshToken ?? ''],
  )
  return true
}

const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 834, height: 900 },
  { name: 'phone', width: 390, height: 844 },
] as const

for (const vp of VIEWPORTS) {
  test(`审批中心在 ${vp.name} 视口（${vp.width}px）下不横向溢出且决策可点`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    const signedIn = await signIn(page)
    test.skip(!signedIn, '后端不可达或无种子管理员：本用例需真实后端（不是 mock）')
    await page.goto('/approvals')

    // 页头必须在
    await expect(page.getByRole('heading', { name: '审批中心' })).toBeVisible()

    // 无横向溢出：文档宽度不得超过视口宽度（允许 1px 亚像素误差）
    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }))
    expect(
      overflow.scrollWidth,
      `视口 ${vp.width}px 下出现横向溢出：scrollWidth=${overflow.scrollWidth} > clientWidth=${overflow.clientWidth}`,
    ).toBeLessThanOrEqual(overflow.clientWidth + 1)

    // 待审批表格若有行，决策按钮必须完整落在视口内（不能只「存在于 DOM」）
    const rows = page.locator('table tbody tr')
    const rowCount = await rows.count()
    if (rowCount === 0) {
      // 空态是合法状态：断言空态文案在，避免用例在无数据时假通过
      await expect(page.getByText('暂无审批申请')).toBeVisible()
      return
    }

    // 宽表在窄屏**按设计**横向滚动（Table 自带 overflow-x-auto），所以「按钮初始在视口外」
    // 不是缺陷 —— 缺陷是「滚不到」或「滚动没被收在容器内、把整页撑宽」。
    // 因此这里断言的是可操作性：滚进视野后能点到。
    const approveBtn = page.getByRole('button', { name: '批准', exact: true }).first()
    await approveBtn.scrollIntoViewIfNeeded()
    await expect(approveBtn).toBeInViewport()
    const box = await approveBtn.boundingBox()
    expect(box, '批准按钮应可测量').not.toBeNull()
    expect(
      box!.x + box!.width,
      `滚动后批准按钮仍越界（right=${box!.x + box!.width} > 视口 ${vp.width}）`,
    ).toBeLessThanOrEqual(vp.width + 1)
  })
}
