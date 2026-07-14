import { beforeAll, describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { useAuthStore } from '@/stores/auth'
import InstanceBackupSegment from './InstanceBackupSegment'

/**
 * InstanceBackupSegment 强断言（FR-339 备份·定时分区接真）。
 * devmock 种子：实例1 定时任务 每晚重启(启用) / 每日备份(停用)；实例1 备份 full-2026-06-01(已完成) / inc-2026-06-02(增量·挂 #1)；
 * 实例1 状态 RUNNING（domains/instance.ts）→ 恢复入口按运行态守卫禁用。定时/备份均为实例原生作用域（天然按 instanceId=1）。
 */

/** 按 Panel 标题定位其面板容器（定时/备份两面板并列，操作按钮需分区隔离断言）。 */
function panelByTitle(title: string): HTMLElement {
  return screen.getByText(title).closest('[data-slot="panel"]') as HTMLElement
}

/** 授予组管理员角色：删除定时/删除备份走 DangerConfirm scope=group，需 role≥1 放行确认。 */
function grantGroupAdmin(): void {
  // loginMockUser 的 token 解不出 role；沿用 InstanceConsolePage 范式直接置 role。
  useAuthStore.setState({ role: 1, isAuthenticated: true })
}

describe('InstanceBackupSegment（mock 假后端）', () => {
  beforeAll(() => {
    // Radix Dialog 在 jsdom 下依赖的指针/滚动 API 缺失补桩。
    Object.defineProperty(HTMLElement.prototype, 'hasPointerCapture', { configurable: true, value: () => false })
    Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', { configurable: true, value: () => undefined })
    Object.defineProperty(HTMLElement.prototype, 'releasePointerCapture', { configurable: true, value: () => undefined })
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: () => undefined })
  })

  it('渲染本实例定时任务列表（含启停开关、删除按钮、去独立页创建入口）', async () => {
    loginMockUser()
    renderWithProviders(<InstanceBackupSegment instanceId={1} />)

    const schedules = panelByTitle('定时任务')
    // 实例 1 两条种子任务。
    expect(await within(schedules).findByText('每晚重启')).toBeInTheDocument()
    expect(within(schedules).getByText('每日备份')).toBeInTheDocument()
    // 创建/编辑较重，页签留「去定时任务页创建」链接引导。
    expect(within(schedules).getByRole('link', { name: '去定时任务页创建' })).toBeInTheDocument()

    const row = within(schedules).getByRole('row', { name: /每晚重启/ })
    // 启停开关（seed 启用）+ 删除按钮均在行内。
    expect(within(row).getByRole('switch')).toHaveAttribute('aria-checked', 'true')
    expect(within(row).getByRole('button', { name: '删除' })).toBeInTheDocument()
  })

  it('切换启用状态 → PUT {enabled} → 开关联动翻转', async () => {
    const user = userEvent.setup()
    loginMockUser()

    // 捕获 PUT body 断言启停只发 { enabled }（不夹带其它字段）；仍放行真 devmock handler 落库，
    // 使后续 GET /schedules 反映翻转（若用 server.use 覆盖 PUT 则不改集合，开关无法联动）。
    let putBody: Record<string, unknown> | null = null
    const onReq = async ({ request }: { request: Request }) => {
      const url = new URL(request.url)
      if (request.method === 'PUT' && url.pathname.endsWith('/schedules/1')) {
        putBody = (await request.clone().json().catch(() => ({}))) as Record<string, unknown>
      }
    }
    server.events.on('request:start', onReq)

    try {
      renderWithProviders(<InstanceBackupSegment instanceId={1} />)
      const schedules = panelByTitle('定时任务')
      await within(schedules).findByText('每晚重启')

      const toggle = within(within(schedules).getByRole('row', { name: /每晚重启/ })).getByRole('switch')
      expect(toggle).toHaveAttribute('aria-checked', 'true')
      await user.click(toggle)

      await waitFor(() => expect(putBody).toEqual({ enabled: false }))
      await waitFor(() => {
        const after = within(panelByTitle('定时任务')).getByRole('row', { name: /每晚重启/ })
        expect(within(after).getByRole('switch')).toHaveAttribute('aria-checked', 'false')
      })
    } finally {
      server.events.removeListener('request:start', onReq)
    }
  })

  it('删除定时任务弹 DangerConfirm，确认后列表移除', async () => {
    const user = userEvent.setup()
    loginMockUser()
    grantGroupAdmin()
    renderWithProviders(<InstanceBackupSegment instanceId={1} />)

    const schedules = panelByTitle('定时任务')
    const row = await within(schedules).findByText('每日备份').then((el) => el.closest('tr') as HTMLElement)
    await user.click(within(row).getByRole('button', { name: '删除' }))

    const dialog = await screen.findByRole('dialog', { name: /删除定时任务「每日备份」/ })
    await user.click(within(dialog).getByRole('button', { name: '删除' }))

    await waitFor(() => expect(within(panelByTitle('定时任务')).queryByText('每日备份')).not.toBeInTheDocument())
  })

  it('渲染本实例备份列表；实例运行中 → 恢复入口禁用并提示先停止（FR-013 守卫）', async () => {
    loginMockUser()
    renderWithProviders(<InstanceBackupSegment instanceId={1} />)

    const backups = panelByTitle('备份管理')
    // 实例 1 两条种子备份。
    expect(await within(backups).findByText('full-2026-06-01T02:00:00')).toBeInTheDocument()
    expect(within(backups).getByText('inc-2026-06-02T02:00:00')).toBeInTheDocument()

    // 实例 1 seed 为 RUNNING：恢复会被下次自动存档覆盖（静默失效），按钮须禁用 + 定向提示。
    const restoreButtons = within(backups).getAllByRole('button', { name: '恢复' })
    expect(restoreButtons.length).toBeGreaterThan(0)
    for (const btn of restoreButtons) {
      expect(btn).toBeDisabled()
      expect(btn).toHaveAttribute('title', '实例运行中，请先停止实例再恢复')
    }
  })

  it('点「全量备份」触发创建 → 列表联动新增（POST 写）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<InstanceBackupSegment instanceId={1} />)

    const backups = panelByTitle('备份管理')
    await within(backups).findByText('full-2026-06-01T02:00:00')
    // 初始仅 seed 的 1 个全量备份。
    expect(within(backups).getAllByText(/^full-/)).toHaveLength(1)

    await user.click(within(backups).getByRole('button', { name: '全量备份' }))

    // POST /instances/1/backups → 新增全量备份 → 列表失效重取 → 出现第二个 full- 行。
    await waitFor(() => {
      expect(within(panelByTitle('备份管理')).getAllByText(/^full-/).length).toBeGreaterThanOrEqual(2)
    })
  })

  it('点备份「删除」弹 DangerConfirm 确认框（危险确认拦截）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    grantGroupAdmin()
    renderWithProviders(<InstanceBackupSegment instanceId={1} />)

    const backups = panelByTitle('备份管理')
    // inc-2026-06-02 无依赖增量，可安全删除（full-1 被 inc-2 依赖，删它会 422）。
    const row = await within(backups).findByText('inc-2026-06-02T02:00:00').then((el) => el.closest('tr') as HTMLElement)
    await user.click(within(row).getByRole('button', { name: '删除' }))

    // 删除须经 DangerConfirm 二次确认，确认后列表移除。
    const dialog = await screen.findByRole('dialog', { name: '确定删除此备份？' })
    await user.click(within(dialog).getByRole('button', { name: '删除' }))

    await waitFor(() => expect(within(panelByTitle('备份管理')).queryByText('inc-2026-06-02T02:00:00')).not.toBeInTheDocument())
  })
})
