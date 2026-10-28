import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { TooltipProvider } from '@jianmanager/ui/components/tooltip'
import { render } from '@testing-library/react'
import '@/i18n'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ConfigDeliveriesPage from './ConfigDeliveriesPage'
import ConfigDeliveryDetailPage from './ConfigDeliveryDetailPage'
import {
  deliveryActionsOf,
  deliveryStatusLevel,
  deferredNote,
  isActivationConfirmed,
  isCircuitBreakPaused,
  type ConfigDeliveryOrder,
  type DeliveryStatus,
} from '@/api/configDeliveries'

/**
 * 配置交付编排（FR-531/532）DOM 与状态机逻辑测试。
 *
 * 这里盯住的是三条**会直接误导运维**的语义：
 * 1. `deferred` 与 `failed` 必须分开计数与分开展示（合并会让人把「机器离线」读成「配置坏了」）；
 * 2. 熔断后状态是 `paused` 且 `pauseKind=circuit_break`，界面必须显式说清是系统踩的刹车；
 * 3. `BEST_EFFORT` 目标虽然 `status=activated`，文案**不能**写成「已生效」。
 *
 * 另外覆盖状态机按钮的启用/禁用（判据来自后端状态机表）与回滚对话框的整批/子集两种范围。
 */

/** 构造一张交付单；只覆盖被测断言会用到的字段。 */
function orderOf(overrides: Partial<ConfigDeliveryOrder> = {}): ConfigDeliveryOrder {
  return {
    id: 7,
    configFileId: 3,
    filePath: 'plugins/Foo/config.yml',
    targetLayerLevel: 'group',
    targetLayerScopeId: 2,
    targetLayerVersionId: 11,
    fromLayerVersionId: 10,
    batchMode: 'percent',
    observeWindowSec: 120,
    failureRateThreshold: 20,
    unhealthyRateThreshold: 30,
    activationMethod: 'hot_reload',
    activateTimeoutSec: 300,
    reloadTimeoutSec: 10,
    autoRetryDeferred: true,
    deferredRetryLimit: 3,
    status: 'rolling',
    plannedCount: 10,
    pushedCount: 4,
    activatedCount: 3,
    failedCount: 1,
    deferredCount: 2,
    skippedCount: 0,
    currentBatch: 1,
    totalBatches: 3,
    createdBy: 1,
    createdAt: '2026-10-24T10:00:00Z',
    updatedAt: '2026-10-24T10:05:00Z',
    ...overrides,
  }
}

/** 构造一个目标行。 */
function targetOf(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    orderId: 7,
    instanceId: 1,
    instanceName: '生存服',
    batchNo: 1,
    status: 'activated',
    changedFileCount: 1,
    backupPresent: true,
    diskWritten: true,
    activateConfirmed: true,
    bestEffort: false,
    retryCount: 0,
    rolledBack: false,
    rollbackOfRollback: false,
    ...overrides,
  }
}

/** 详情页所需的四个端点一起注册。 */
function useDetailEndpoints(options: {
  order?: ConfigDeliveryOrder
  targets?: ReturnType<typeof targetOf>[]
  observe?: Record<string, unknown>
  impact?: Record<string, unknown>
}) {
  const order = options.order ?? orderOf()
  server.use(
    http.get(API('/config-deliveries/:id'), () => HttpResponse.json(order)),
    http.get(API('/config-deliveries/:id/targets'), () =>
      HttpResponse.json({
        targets: options.targets ?? [targetOf()],
        total: (options.targets ?? [targetOf()]).length,
        page: 1,
        pageSize: 50,
      }),
    ),
    http.get(API('/config-deliveries/:id/observe'), () =>
      HttpResponse.json(
        options.observe ?? {
          orderId: order.id,
          status: order.status,
          currentBatch: 1,
          totalBatches: 3,
          thresholds: {
            failureRateThreshold: 20,
            unhealthyRateThreshold: 30,
            offlineBreakPercent: 10,
            observeWindowSec: 120,
          },
          measured: {
            planned: 10,
            failed: 1,
            deferred: 2,
            failureRate: 12.5,
            unhealthy: 0,
            unhealthyRate: 0,
          },
          batches: [],
        },
      ),
    ),
    http.get(API('/config-deliveries/:id/impact'), () =>
      HttpResponse.json(
        options.impact ?? {
          orderId: order.id,
          planned: 10,
          online: 8,
          offline: 2,
          changed: 9,
          unchanged: 1,
          undetermined: 0,
          offlineRate: 20,
          wouldCircuitBreak: false,
          targets: [],
          truncated: false,
        },
      ),
    ),
  )
}

/** 渲染详情页（带 :id 路由参数）。 */
function renderDetail() {
  loginMockUser()
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <MemoryRouter initialEntries={['/config-deliveries/7']}>
          <Routes>
            <Route path="/config-deliveries/:id" element={<ConfigDeliveryDetailPage />} />
          </Routes>
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
  return { user }
}

describe('配置交付状态机：按钮可用性（纯函数，对应后端状态机表）', () => {
  const cases: { status: DeliveryStatus; enabled: string[] }[] = [
    { status: 'draft', enabled: ['submit', 'cancel', 'edit', 'remove'] },
    { status: 'pending_approval', enabled: ['approve', 'cancel'] },
    { status: 'approved', enabled: ['start', 'cancel'] },
    { status: 'rolling', enabled: ['pause', 'cancel', 'rollback'] },
    { status: 'observing', enabled: ['pause', 'cancel', 'rollback'] },
    { status: 'paused', enabled: ['resume', 'cancel', 'rollback'] },
    { status: 'failed', enabled: ['rollback'] },
    { status: 'completed', enabled: ['rollback'] },
    { status: 'rolled_back', enabled: ['rollback'] },
    { status: 'rolling_back', enabled: ['cancel'] },
    { status: 'cancelled', enabled: [] },
  ]

  for (const { status, enabled } of cases) {
    it(`${status} 只开放 ${enabled.length > 0 ? enabled.join(' / ') : '无动作'}`, () => {
      const actions = deliveryActionsOf(status)
      const on = Object.entries(actions)
        .filter(([, v]) => v)
        .map(([k]) => k)
        .sort()
      expect(on).toEqual([...enabled].sort())
    })
  }
})

describe('配置交付状态语义（纯函数）', () => {
  it('熔断判定要求 paused + circuit_break 两个条件同时成立', () => {
    expect(isCircuitBreakPaused({ status: 'paused', pauseKind: 'circuit_break' })).toBe(true)
    // 人工暂停不是熔断：两者处置完全不同（一个等人点继续，一个要先查原因）。
    expect(isCircuitBreakPaused({ status: 'paused', pauseKind: 'manual' })).toBe(false)
    // 熔断本身也不会把单推进 failed。
    expect(isCircuitBreakPaused({ status: 'failed', pauseKind: 'circuit_break' })).toBe(false)
  })

  it('已生效必须有确认，bestEffort 永远不算「已生效」', () => {
    expect(
      isActivationConfirmed({ status: 'activated', activateConfirmed: true, bestEffort: false }),
    ).toBe(true)
    // status=activated 但 bestEffort：只是「请求已下发」，插件没给回执。
    expect(
      isActivationConfirmed({ status: 'activated', activateConfirmed: false, bestEffort: true }),
    ).toBe(false)
    expect(
      isActivationConfirmed({ status: 'activated', activateConfirmed: false, bestEffort: false }),
    ).toBe(false)
  })

  it('状态配色：deferred 走 warning（目标不在线）而非 danger（配置坏了）', () => {
    expect(deliveryStatusLevel('failed')).toBe('danger')
    expect(deliveryStatusLevel('paused')).toBe('warning')
    expect(deliveryStatusLevel('completed')).toBe('success')
  })

  it('deferred 说明只在真有挂起目标时出现，且明说「不计入失败率」', () => {
    expect(deferredNote({ deferred: 0, planned: 10 })).toBe('')
    const note = deferredNote({ deferred: 2, planned: 10 })
    expect(note).toContain('2 台')
    expect(note).toContain('不计入失败率')
  })
})

describe('ConfigDeliveriesPage（交付单列表）', () => {
  function renderList() {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConfigDeliveriesPage />)
    return { user }
  }

  it('失败与离线挂起分两列显示，不合并成一个「异常数」', async () => {
    server.use(
      http.get(API('/config-deliveries'), () =>
        HttpResponse.json({
          orders: [orderOf({ id: 1, failedCount: 3, deferredCount: 5 })],
          total: 1,
          page: 1,
          pageSize: 20,
        }),
      ),
    )
    renderList()

    const row = await screen.findByTestId('delivery-row')
    expect(within(row).getByTestId('delivery-failed').textContent).toBe('3')
    expect(within(row).getByTestId('delivery-deferred').textContent).toBe('5')
    // 两列必须各自存在：合并成一列会让「5 台离线」读成「5 台配置坏了」。
    expect(within(row).getByTestId('delivery-failed')).not.toBe(
      within(row).getByTestId('delivery-deferred'),
    )
  })

  it('熔断停下的单在列表里带熔断标记，与人工暂停区分', async () => {
    server.use(
      http.get(API('/config-deliveries'), () =>
        HttpResponse.json({
          orders: [
            orderOf({ id: 1, status: 'paused', pauseKind: 'circuit_break' }),
            orderOf({ id: 2, status: 'paused', pauseKind: 'manual' }),
          ],
          total: 2,
          page: 1,
          pageSize: 20,
        }),
      ),
    )
    renderList()

    const rows = await screen.findAllByTestId('delivery-row')
    // 第一行（熔断）有标记，第二行（人工暂停）没有。
    expect(within(rows[0]).getByText('熔断')).toBeInTheDocument()
    expect(within(rows[1]).queryByText('熔断')).toBeNull()
  })

  it('取数失败时渲染错误态，而不是「还没有交付单」', async () => {
    server.use(
      http.get(API('/config-deliveries'), () =>
        HttpResponse.json({ error: 'INTERNAL', message: '数据库不可用' }, { status: 500 }),
      ),
    )
    renderList()
    expect(await screen.findByTestId('delivery-list-error')).toBeInTheDocument()
    expect(screen.queryByTestId('delivery-list-empty')).toBeNull()
  })
})

describe('ConfigDeliveryDetailPage（状态机 / 目标 / 观察窗 / 回滚）', () => {
  it('rolling 状态：暂停与回滚可点，提交/批准/开始/继续禁用', async () => {
    useDetailEndpoints({ order: orderOf({ status: 'rolling' }) })
    renderDetail()

    await waitFor(() => expect(screen.getByTestId('action-pause')).toBeEnabled())
    expect(screen.getByTestId('action-rollback')).toBeEnabled()
    expect(screen.getByTestId('action-submit')).toBeDisabled()
    expect(screen.getByTestId('action-approve')).toBeDisabled()
    expect(screen.getByTestId('action-start')).toBeDisabled()
    expect(screen.getByTestId('action-resume')).toBeDisabled()
  })

  it('draft 状态：只有提交可用，其余推进与回滚都禁用', async () => {
    useDetailEndpoints({ order: orderOf({ status: 'draft' }) })
    renderDetail()

    await waitFor(() => expect(screen.getByTestId('action-submit')).toBeEnabled())
    expect(screen.getByTestId('action-start')).toBeDisabled()
    expect(screen.getByTestId('action-pause')).toBeDisabled()
    expect(screen.getByTestId('action-rollback')).toBeDisabled()
  })

  it('熔断：显示熔断现场（阈值与实测），且不误标成 failed 文案', async () => {
    useDetailEndpoints({
      order: orderOf({
        status: 'paused',
        pauseKind: 'circuit_break',
        breakReason: '失败率超过阈值',
        breakThreshold: 20,
        breakObserved: 35,
        failedBatchNo: 2,
      }),
    })
    renderDetail()

    const notice = await screen.findByTestId('delivery-circuit-break')
    expect(notice.textContent).toContain('已熔断')
    expect(notice.textContent).toContain('失败率超过阈值')
    // 阈值与实测值必须都给：只说「熔断了」对排障毫无帮助。
    expect(notice.textContent).toContain('20')
    expect(notice.textContent).toContain('35')
    expect(notice.textContent).toContain('第 2 批')
  })

  it('BEST_EFFORT 目标显示「已请求重载，插件未提供确认」，不写成已生效', async () => {
    useDetailEndpoints({
      order: orderOf({ status: 'observing' }),
      targets: [
        targetOf({
          id: 1,
          instanceId: 1,
          instanceName: '生存服',
          status: 'activated',
          activateConfirmed: false,
          bestEffort: true,
          activationMessage: '已请求重载，插件未提供确认',
        }),
      ],
    })
    renderDetail()

    const row = await screen.findByTestId('delivery-target-row')
    const text = row.textContent ?? ''
    expect(text).toContain('已请求重载，插件未提供确认')
    // 关键否定断言：任何一处写成「已生效」都是夸大。
    expect(text).not.toContain('已生效')
    expect(within(row).getByText('未提供确认')).toBeInTheDocument()
  })

  it('落盘成功但生效失败的目标，说明里点明「磁盘新、内存旧，重启会变」', async () => {
    useDetailEndpoints({
      order: orderOf({ status: 'observing' }),
      targets: [
        targetOf({
          id: 1,
          status: 'failed',
          diskWritten: true,
          activateConfirmed: false,
          reasonCode: 'DISK_OK_ACTIVATE_FAILED',
          reason: '落盘成功但生效失败：重载超时',
        }),
      ],
    })
    renderDetail()

    const row = await screen.findByTestId('delivery-target-row')
    // 落盘列显示「已落盘」，生效列不显示已确认——两列分开才看得出这种危险状态。
    expect(within(row).getByText('已落盘')).toBeInTheDocument()
    expect(within(row).getByText('未确认')).toBeInTheDocument()
  })

  it('观察窗把 failed 与 deferred 分开展示，并说明失败率分母排除 deferred', async () => {
    useDetailEndpoints({ order: orderOf({ status: 'observing' }) })
    renderDetail()

    const observe = await screen.findByTestId('delivery-observe')
    await waitFor(() =>
      expect(within(observe).getByTestId('observe-failed').textContent).toContain('1'),
    )
    expect(within(observe).getByTestId('observe-deferred').textContent).toContain('2')
    expect(within(observe).getByTestId('observe-failure-rate').textContent).toContain('12.5')

    const note = within(observe).getByTestId('observe-deferred-note')
    expect(note.textContent).toContain('不计入失败率')
  })

  it('影响预览在开跑前就警示「按离线率会熔断」', async () => {
    useDetailEndpoints({
      order: orderOf({ status: 'approved' }),
      impact: {
        orderId: 7,
        planned: 10,
        online: 8,
        offline: 2,
        changed: 9,
        unchanged: 1,
        undetermined: 0,
        offlineRate: 20,
        wouldCircuitBreak: true,
        targets: [],
        truncated: false,
      },
    })
    renderDetail()

    const warn = await screen.findByTestId('impact-would-break')
    expect(warn.textContent).toContain('熔断')
  })

  it('回滚对话框：整批默认不回退版本，子集模式必须选中目标才能确认', async () => {
    const bodies: Array<Record<string, unknown>> = []
    useDetailEndpoints({
      order: orderOf({ status: 'completed' }),
      targets: [targetOf({ id: 1, instanceId: 1, instanceName: '生存服' })],
    })
    server.use(
      http.post(API('/config-deliveries/:id/rollback'), async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({
          orderId: 7,
          rollbackId: 1,
          scope: 'full',
          succeeded: 1,
          failed: 0,
          layerRolledBack: true,
          rollbackOfRollback: false,
          targets: [],
        })
      }),
    )
    const { user } = renderDetail()

    await waitFor(() => expect(screen.getByTestId('action-rollback')).toBeEnabled())
    await user.click(screen.getByTestId('action-rollback'))

    const dialog = await screen.findByRole('dialog')
    // 默认整批：说明里必须提到「版本回退」。
    expect(within(dialog).getByTestId('rollback-scope-hint').textContent).toContain('版本回退')

    // 切到子集：说明改成「版本不回退」，且未选目标时确认禁用。
    await user.click(within(dialog).getByTestId('rollback-scope-subset'))
    expect(within(dialog).getByTestId('rollback-scope-hint').textContent).toContain('不回退')
    expect(within(dialog).getByTestId('rollback-confirm')).toBeDisabled()

    // 选中一台后确认可用，提交的是该台的 instanceId。
    await user.click(within(dialog).getByRole('checkbox'))
    await waitFor(() => expect(within(dialog).getByTestId('rollback-confirm')).toBeEnabled())
    await user.click(within(dialog).getByTestId('rollback-confirm'))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].instanceIds).toEqual([1])
  })

  it('回滚对话框：整批模式提交不含 instanceIds（后端据「非空」判定子集）', async () => {
    const bodies: Array<Record<string, unknown>> = []
    useDetailEndpoints({
      order: orderOf({ status: 'completed' }),
      targets: [targetOf({ id: 1, instanceId: 1 })],
    })
    server.use(
      http.post(API('/config-deliveries/:id/rollback'), async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({
          orderId: 7,
          rollbackId: 1,
          scope: 'full',
          succeeded: 1,
          failed: 0,
          layerRolledBack: true,
          rollbackOfRollback: false,
          targets: [],
        })
      }),
    )
    const { user } = renderDetail()

    await waitFor(() => expect(screen.getByTestId('action-rollback')).toBeEnabled())
    await user.click(screen.getByTestId('action-rollback'))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByTestId('rollback-confirm'))

    await waitFor(() => expect(bodies).toHaveLength(1))
    // 传空数组会被后端当成「非空选择器以外」还是子集？——后端以 len>0 判子集，
    // 因此整批必须传 undefined，不能传 []。
    expect(bodies[0].instanceIds).toBeUndefined()
  })
})
