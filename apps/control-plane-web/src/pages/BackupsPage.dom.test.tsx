import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { mockInject } from '@jianmanager/devmock/inject'
import BackupsPage from './BackupsPage'

/** 
 * 给 `countDependents` 套一层计数壳（其余备份纯逻辑保持真实实现）：
 * 用于断言渲染期不再逐行调用它（那是 O(n²)，配合 3 秒轮询会放大成百万级比较）。
 */
const { countDependentsSpy } = vi.hoisted(() => ({ countDependentsSpy: vi.fn() }))

vi.mock('@/lib/backups/backup', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/backups/backup')>()
  return {
    ...actual,
    countDependents: (backups: Parameters<typeof actual.countDependents>[0], parentId: number) => {
      countDependentsSpy(backups, parentId)
      return actual.countDependents(backups, parentId)
    },
  }
})

/** 
 * 备份管理页（域簇）。强断言覆盖：
 * ① 选实例后渲染出 seed 备份；② 创建全量备份（POST 写）→ 列表联动新增；③ 注入 500 → 错误态；
 * ④ 恢复守卫；⑤ 列表视图按滚动窗口裁剪；⑥ 依赖计数不再逐行全表扫描。
 * BackupsPage 需选实例才拉备份；/instances 属实例域（本域不重定义），测试内用 server.use 临时桩。
 */
describe('BackupsPage（mock）', () => {
  beforeEach(() => {
    loginMockUser() // 受 requireAuth 保护，渲染前置已登录态
    countDependentsSpy.mockClear()
    // 实例选择器改 Combobox（服务端搜索）+ useInstance 拉选中详情；本域只需最小桩。
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({
          items: [{ id: 1, name: 'survival', uuid: 'i-1', status: 'STOPPED' }],
          total: 1,
          page: 1,
          pageSize: 50,
        }),
      ),
      http.get(API('/instances/:id'), () =>
        HttpResponse.json({ id: 1, name: 'survival', uuid: 'i-1', status: 'STOPPED' }),
      ),
    )
  })

  /** 
   * 选中名为 `name` 的实例（默认 survival），触发 useBackups 拉取该实例备份。
   * 实例选择器已改 Combobox（服务端搜索）：点触发器展开 → 点选项按钮提交。
   */
  async function selectInstance(
    user: ReturnType<typeof userEvent.setup>,
    name = 'survival',
  ) {
    // Combobox 触发器初始显示 placeholder「选择实例」。
    await user.click(await screen.findByRole('button', { name: '选择实例' }))
    // 展开后异步渲染服务端搜索结果 option；点其按钮提交。
    await user.click(await screen.findByRole('button', { name }))
  }

  it('选实例后渲染 seed 备份', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupsPage />)
    // 未选实例时显示提示，无备份行。
    expect(await screen.findByText('请先选择一个实例查看备份列表')).toBeInTheDocument()

    await selectInstance(user)

    expect(await screen.findByText('full-2026-06-01T02:00:00')).toBeInTheDocument()
    expect(screen.getByText('inc-2026-06-02T02:00:00')).toBeInTheDocument()
    expect(screen.getByText('aaaaaaaaaaaa...')).toBeInTheDocument()
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

  it('远程存储备份显示存储位置，创建时带上 storageId', async () => {
    const user = userEvent.setup()
    let createBody: Record<string, unknown> | null = null
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({
          items: [{ id: 2, name: 'remote-survival', uuid: 'i-2', status: 'STOPPED' }],
          total: 1,
          page: 1,
          pageSize: 50,
        }),
      ),
      http.get(API('/instances/:id'), () =>
        HttpResponse.json({ id: 2, name: 'remote-survival', uuid: 'i-2', status: 'STOPPED' }),
      ),
      http.post(API('/instances/:id/backups'), async ({ request }) => {
        createBody = await request.json() as Record<string, unknown>
        return HttpResponse.json({ id: 99, uuid: 'bk-99', instanceId: 2, name: 'full-remote-test', filePath: '', fileSizeMb: 0, type: 0, mode: 0, status: 0, storageId: createBody.storageId, createdAt: new Date().toISOString() }, { status: 201 })
      }),
    )
    renderWithProviders(<BackupsPage />)

    // 实例选 Combobox；存储仍为原生 select（下拉基数小），按 title 定位。
    await selectInstance(user, 'remote-survival')
    const storageSelect = screen.getByRole('combobox', { name: '存储位置' })

    expect(await screen.findByText('full-2026-06-03T02:00:00')).toBeInTheDocument()
    expect(screen.getAllByText('s3-primary').length).toBeGreaterThanOrEqual(2)

    await screen.findByRole('option', { name: 's3-primary' })
    await user.selectOptions(storageSelect, '1')
    await user.click(screen.getByRole('button', { name: '全量备份' }))

    await waitFor(() => expect(createBody?.storageId).toBe(1))
  })

  it('实例运行中 → 恢复按钮禁用并提示先停止（恢复守卫回归）', async () => {
    const user = userEvent.setup()
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({
          items: [{ id: 1, name: 'survival', uuid: 'i-1', status: 'RUNNING' }],
          total: 1,
          page: 1,
          pageSize: 50,
        }),
      ),
      http.get(API('/instances/:id'), () =>
        HttpResponse.json({ id: 1, name: 'survival', uuid: 'i-1', status: 'RUNNING' }),
      ),
    )
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)
    await screen.findByText('full-2026-06-01T02:00:00')

    // 运行中恢复会被下次自动存档覆盖（静默失效），按钮须禁用并给出定向提示。
    const restoreButtons = screen.getAllByRole('button', { name: '恢复' })
    expect(restoreButtons.length).toBeGreaterThan(0)
    for (const btn of restoreButtons) {
      expect(btn).toBeDisabled()
      expect(btn).toHaveAttribute('title', '实例运行中，请先停止实例再恢复')
      // 禁用按钮不可聚焦：原因还必须能被辅助技术读到（可见提示 + aria-describedby）。
      expect(btn).toHaveAttribute('aria-describedby', 'backups-restore-blocked-hint')
    }
    expect(screen.getByTestId('backups-restore-blocked-hint')).toHaveTextContent('实例运行中，请先停止实例再恢复')
  })

  it('实例已停止 → 已完成备份的恢复按钮可用', async () => {
    const user = userEvent.setup()
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({
          items: [{ id: 1, name: 'survival', uuid: 'i-1', status: 'STOPPED' }],
          total: 1,
          page: 1,
          pageSize: 50,
        }),
      ),
      http.get(API('/instances/:id'), () =>
        HttpResponse.json({ id: 1, name: 'survival', uuid: 'i-1', status: 'STOPPED' }),
      ),
    )
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)
    await screen.findByText('full-2026-06-01T02:00:00')

    const restoreButtons = screen.getAllByRole('button', { name: '恢复' })
    expect(restoreButtons.some((btn) => !(btn as HTMLButtonElement).disabled)).toBe(true)
  })

  it('注入 500 → 显示错误态而非「暂无备份」（不崩溃）', async () => {
    const user = userEvent.setup()
    mockInject('get', '/instances/:id/backups', { kind: 'status', status: 500 })
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)

    // 备份查询失败 → 渲染错误文案：报「暂无备份」会让运维以为这个实例没有可恢复的备份。
    expect(await screen.findByText('错误')).toBeInTheDocument()
    expect(screen.queryByText('暂无备份')).not.toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('full-2026-06-01T02:00:00')).not.toBeInTheDocument()
    })
  })

  it('列表视图按滚动窗口裁剪：千份备份不再整表进 DOM', async () => {
    const user = userEvent.setup()
    const backups = Array.from({ length: 120 }, (_, i) => ({
      id: i + 1,
      uuid: `bk-${i + 1}`,
      instanceId: 1,
      name: `full-${String(i + 1).padStart(3, '0')}`,
      filePath: '',
      fileSizeMb: 1,
      type: 0,
      // 偶数行做增量子备份（两档行高模型都覆盖到）。
      mode: i % 2 === 0 ? 1 : 0,
      parentId: i % 2 === 0 ? 1 : undefined,
      status: 2,
      createdAt: new Date().toISOString(),
    }))
    server.use(http.get(API('/instances/:id/backups'), () => HttpResponse.json(backups)))
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)
    await screen.findByText('full-001')

    const rows = screen.getAllByTestId('backup-row')
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.length).toBeLessThan(backups.length)
    // 窗口首行在、远处的行不在：确实按滚动窗口裁剪。
    expect(screen.queryByText('full-120')).toBeNull()
    // 窗口外的高度由占位撑开，滚动条长度仍按全部行算。
    expect(screen.getByTestId('backups-virtual-spacer-after')).toBeInTheDocument()
  })

  it('依赖计数走一次建表：渲染不再逐行全表扫描 countDependents', async () => {
    const user = userEvent.setup()
    const backups = Array.from({ length: 60 }, (_, i) => ({
      id: i + 1,
      uuid: `bk-${i + 1}`,
      instanceId: 1,
      name: `full-${String(i + 1).padStart(3, '0')}`,
      filePath: '',
      fileSizeMb: 1,
      type: 0,
      mode: 0,
      status: 2,
      createdAt: new Date().toISOString(),
    }))
    server.use(http.get(API('/instances/:id/backups'), () => HttpResponse.json(backups)))
    renderWithProviders(<BackupsPage />)
    await selectInstance(user)
    await screen.findByText('full-001')

    // 此前列表视图每行调用两次 countDependents（各扫全表一遍）：60 行即 120 次全表扫描。
    expect(countDependentsSpy.mock.calls.length).toBeLessThan(backups.length)
  })
})
