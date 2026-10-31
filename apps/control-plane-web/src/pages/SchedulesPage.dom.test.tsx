import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { mockInject } from '@jianmanager/devmock/inject'
import SchedulesPage from './SchedulesPage'

/** 
 * 给 `nextRuns` 套一层计数壳（其余 cron 工具保持真实实现）：用于断言「永不命中」表达式
 * 不会在每次渲染里重算——那是纯逻辑用例抓不到的渲染期开销。
 */
const { nextRunsSpy } = vi.hoisted(() => ({ nextRunsSpy: vi.fn() }))

vi.mock('@/lib/shared/cron', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/shared/cron')>()
  return {
    ...actual,
    nextRuns: (expr: string, count?: number, from?: Date) => {
      nextRunsSpy(expr)
      return actual.nextRuns(expr, count, from)
    },
  }
})

/** 
 * 定时任务页（域簇）。强断言覆盖：
 * ① 渲染出 seed 任务；② 切换启用（PUT 写）→ 开关状态联动翻转；③ 注入 500 → 错误态（不崩溃）；
 * ④ 「永不命中」cron 只算一次；⑤ 弹窗字段可访问名；⑥ 列表视图按滚动窗口裁剪。
 * SchedulesPage 同时拉 /instances（属实例域，本域不重定义），测试内用 server.use 临时桩。
 */
describe('SchedulesPage（mock）', () => {
  beforeEach(() => {
    loginMockUser() // 受 requireAuth 保护，渲染前置已登录态
    nextRunsSpy.mockClear()
    // /instances 由实例域负责；本域测试只需一个最小桩供 instanceName 展示。
    server.use(
      http.get(API('/instances'), () =>
        HttpResponse.json([{ id: 1, name: 'survival', uuid: 'i-1' }]),
      ),
    )
  })

  it('渲染 seed 定时任务', async () => {
    renderWithProviders(<SchedulesPage />)
    expect(await screen.findByText('每晚重启')).toBeInTheDocument()
    expect(screen.getByText('每日备份')).toBeInTheDocument()
    // cron 表达式原样展示（list 视图）。
    expect(screen.getByText('0 4 * * *')).toBeInTheDocument()
  })

  it('切换启用状态 → 开关联动翻转（PUT 写联动）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<SchedulesPage />)
    await screen.findByText('每晚重启')

    const row = screen.getByRole('row', { name: /每晚重启/ })
    const toggle = within(row).getByRole('switch')
    expect(toggle).toHaveAttribute('aria-checked', 'true') // seed 为启用

    await user.click(toggle)

    // PUT /schedules/1 写入 enabled=false → 列表失效重取 → 开关翻转为关闭。
    await waitFor(() => {
      const after = within(screen.getByRole('row', { name: /每晚重启/ })).getByRole('switch')
      expect(after).toHaveAttribute('aria-checked', 'false')
    })
  })

  it('注入 500 → 显示错误态而非「暂无定时任务」（不崩溃）', async () => {
    mockInject('get', '/schedules', { kind: 'status', status: 500 })
    renderWithProviders(<SchedulesPage />)
    // 列表查询失败 → 渲染错误文案：报「暂无定时任务」会让运维以为任务被删光了。
    expect(await screen.findByText('错误')).toBeInTheDocument()
    expect(screen.queryByText('暂无定时任务')).not.toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('每晚重启')).not.toBeInTheDocument()
    })
  })

  it('「永不命中」的 cron 表达式只算一次，不随每次渲染重算', async () => {
    const user = userEvent.setup()
    // 2 月 30 日永不命中：nextRuns 要逐分扫描满 366 天（约 52.7 万次迭代）。
    const neverExpr = '0 0 30 2 *'
    server.use(
      http.get(API('/schedules'), () =>
        HttpResponse.json([
          { id: 1, instanceId: 1, instanceName: 'survival', name: '永不命中', cronExpr: neverExpr, action: 'restart', enabled: true, lastRun: null },
        ]),
      ),
      http.get(API('/schedules/1/logs'), () => HttpResponse.json({ items: [], total: 0 })),
    )
    renderWithProviders(<SchedulesPage />)
    await screen.findByText('永不命中')

    // 默认列表视图不展示「下次执行」，切到卡片视图才会走预计算。
    await user.click(screen.getByRole('button', { name: '卡片' }))
    expect(await screen.findByText('未计划')).toBeInTheDocument()

    const calls = () => nextRunsSpy.mock.calls.filter(([expr]) => expr === neverExpr).length
    expect(calls()).toBe(1)

    // 与 cron 无关的交互（展开执行日志）会重渲染整页；此前每次渲染都会重算一遍。
    await user.click(screen.getByRole('button', { name: '查看日志' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '收起日志' })).toBeInTheDocument())
    expect(calls()).toBe(1)
  })

  it('创建任务弹窗的实例/名称/Cron/命令字段都有可访问名', async () => {
    const user = userEvent.setup()
    renderWithProviders(<SchedulesPage />)
    await screen.findByText('每晚重启')

    await user.click(screen.getByRole('button', { name: /创建任务/ }))
    const dialog = await screen.findByRole('dialog')

    // 三处 FieldLabel 此前都没关联到输入元素，读屏只能念出「编辑框」而拿不到字段名。
    expect(within(dialog).getByLabelText(/^实例/)).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/^名称/)).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/^Cron/)).toBeInTheDocument()

    // 命令框只在 action=command 时渲染：先切动作，再校验它的可访问名。
    // 动作选择器是 Popover + button 触发器（不是 role=combobox），下拉挂在 body 上故用全局查询。
    await user.click(within(dialog).getByRole('button', { name: '重启' }))
    await user.click(screen.getByRole('button', { name: '执行命令' }))
    expect(within(dialog).getByLabelText(/^命令/)).toBeInTheDocument()
  })

  it('列表视图按滚动窗口裁剪（任务列表是全库的，规模随集群增长）', async () => {
    const schedules = Array.from({ length: 80 }, (_, i) => ({
      id: i + 1,
      instanceId: 1,
      instanceName: 'survival',
      name: `任务-${i + 1}`,
      cronExpr: '0 4 * * *',
      action: 'restart',
      enabled: true,
      lastRun: null,
    }))
    server.use(http.get(API('/schedules'), () => HttpResponse.json(schedules)))
    renderWithProviders(<SchedulesPage />)
    await screen.findByText('任务-1')

    const rows = screen.getAllByTestId('schedule-row')
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.length).toBeLessThan(schedules.length)
    // 窗口首行在、远处的行不在：确实按滚动窗口裁剪，而非整表进 DOM。
    expect(screen.queryByText('任务-80')).toBeNull()
    // 窗口外的高度由占位撑开，滚动条长度仍按全部行计算。
    expect(screen.getByTestId('schedules-virtual-spacer-after')).toBeInTheDocument()
  })
})
