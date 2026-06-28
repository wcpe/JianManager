import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { mockInject } from '@/mocks/inject'
import BackupsPage from './BackupsPage'

/**
 * 备份管理页（FR-207 域簇）。三条强断言：
 * ① 选实例后渲染出 seed 备份；② 创建全量备份（POST 写）→ 列表联动新增；③ 注入 500 → 显空态（不崩溃）。
 * BackupsPage 需选实例才拉备份；/instances 属实例域（本域不重定义），测试内用 server.use 临时桩。
 */
describe('BackupsPage（mock）', () => {
  beforeEach(() => {
    loginMockUser() // 受 requireAuth 保护，渲染前置已登录态
    // /instances 由实例域负责；本域测试只需一个最小桩供实例下拉选择。
    server.use(
      http.get(API('/instances'), () =>
        HttpResponse.json([{ id: 1, name: 'survival', uuid: 'i-1' }]),
      ),
    )
  })

  /** 选中 id=1 的实例，触发 useBackups(1) 拉取该实例备份。 */
  async function selectInstance(user: ReturnType<typeof userEvent.setup>) {
    // 选实例前页面仅有「实例」「存储」两个原生 select；实例为第一个。
    // 等实例查询返回、option「survival」渲染后再选，避免选不到值。
    await screen.findByRole('option', { name: 'survival' })
    const instanceSelect = screen.getAllByRole('combobox')[0]
    await user.selectOptions(instanceSelect, '1')
  }

  it('选实例后渲染 seed 备份', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupsPage />)
    // 未选实例时显示提示，无备份行。
    expect(await screen.findByText('请先选择一个实例查看备份列表')).toBeInTheDocument()

    await selectInstance(user)

    expect(await screen.findByText('full-2026-06-01T02:00:00')).toBeInTheDocument()
    expect(screen.getByText('inc-2026-06-02T02:00:00')).toBeInTheDocument()
  })

  it('创建全量备份 → 列表联动新增（POST 写联动）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)
    await screen.findByText('full-2026-06-01T02:00:00')

    // 初始仅 seed 的 1 个全量备份（#1）。
    expect(screen.getAllByText(/^full-/)).toHaveLength(1)

    await user.click(screen.getByRole('button', { name: '全量备份' }))

    // POST /instances/1/backups → 新增一条全量备份 → 列表失效重取 → 出现第二个 full- 行。
    await waitFor(() => {
      expect(screen.getAllByText(/^full-/).length).toBeGreaterThanOrEqual(2)
    })
  })

  it('注入 500 → 显示空态而非崩溃', async () => {
    const user = userEvent.setup()
    mockInject('get', '/instances/:id/backups', { kind: 'status', status: 500 })
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)

    // 备份查询失败 → list 为空 → 渲染「暂无备份」空态，页面不崩。
    expect(await screen.findByText('暂无备份')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('full-2026-06-01T02:00:00')).not.toBeInTheDocument()
    })
  })
})
