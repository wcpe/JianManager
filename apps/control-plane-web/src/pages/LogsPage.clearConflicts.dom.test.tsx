/**
 * 身份冲突人工清空入口（日志页质量标记的运维入口）DOM 断言。
 *
 * 覆盖四条产品面红线：
 * - 只在**确有冲突**时出现（否则按钮没有任何可清的东西）；
 * - 破坏性操作走二次确认（不是一次点击就清）；
 * - 理由是必填（缺理由时确认按钮禁用，且不发请求）；
 * - 成功后调用**正确端点**并带上理由（审计凭据的必要输入）。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import LogsPage from './LogsPage'

const seedLogsPage = {
  items: [
    {
      id: 1,
      source: 'instance',
      level: 'info',
      instanceId: 1,
      instanceUuid: 'i-1',
      nodeId: 1,
      message: 'conflict-seed-log-line',
      time: '2026-07-18T12:00:00Z',
    },
  ],
  total: 1,
  page: 1,
  pageSize: 100,
}

/** 冲突态联邦响应：duplicate_quality=conflict ⇒ coverage.duplicateUnresolved=true。 */
function conflictFederationBody() {
  return {
    ok: true,
    sourceTag: 'federated',
    available: true,
    items: seedLogsPage.items,
    quality: { duplicate_quality: 'conflict', stats_quality: 'partial' },
    coverage: {
      complete: false,
      partial_reasons: ['DUPLICATE_UNRESOLVED'],
      targets: [{ target_id: 'n1', state: 'success', included: true }],
      enumeration_state: 'OPEN',
    },
  }
}

/** 无冲突态：不出现清空入口。 */
function cleanFederationBody() {
  return {
    ok: true,
    sourceTag: 'federated',
    available: true,
    items: seedLogsPage.items,
    quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
    coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: 'EXHAUSTED' },
  }
}

/**
 * 生产实测形态：`coverage.complete: true` + `duplicate_quality: conflict`。
 *
 * 这正是缺陷 3 的现场——覆盖是完整的，质量维度才降级；横幅不得说「覆盖不完整」。
 */
function completeConflictFederationBody() {
  return {
    ok: true,
    sourceTag: 'federated',
    available: true,
    items: seedLogsPage.items,
    quality: { duplicate_quality: 'conflict', stats_quality: 'partial' },
    coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: 'EXHAUSTED' },
  }
}

/** 未对账重复（UNRESOLVED）：与身份冲突是**两个状态**，没有任何可人工清空的对象。 */
function unresolvedFederationBody() {
  return {
    ok: true,
    sourceTag: 'federated',
    available: true,
    items: seedLogsPage.items,
    quality: { duplicate_quality: 'unresolved', stats_quality: 'partial' },
    coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: 'EXHAUSTED' },
  }
}

/** 让页面处于「已选中单一节点」的上下文（入口可用性的必要条件之一）。 */
function stubBaseHandlers() {
  server.use(
    http.get(API('/nodes'), () =>
      HttpResponse.json([{ id: 1, name: 'node-one', status: 'online' }]),
    ),
    http.get(API('/instances'), () => HttpResponse.json([])),
    http.get(API('/logs'), () => HttpResponse.json(seedLogsPage)),
    http.get(API('/logs/federation/stats'), () =>
      HttpResponse.json({ points: [], coverage: { complete: true, targets: [] } }),
    ),
    http.get(API('/logs/federation/facets'), () =>
      HttpResponse.json({ dimensions: [], coverage: { complete: true, targets: [] } }),
    ),
  )
}

/**
 * 通过节点选择器真的选中一个节点。
 *
 * 为什么不深链预置：`nodeId` 只由该 Select 的 onValueChange 驱动（没有 URL 初始化），
 * 从 URL 写进去页面读不到——那样写出来的用例会"绿"得毫无意义（它测的是"选了节点"这个前提
 * 从未成立时的分支）。
 *
 * jsdom 未实现 scrollIntoView，Radix Select 展开时会调用它（同 LogsPage.federation.dom.test.tsx）；
 * 且 Radix Select 需用键盘激活展开，直接 click trigger 不展开列表。
 */
async function selectNode(label = 'node-one') {
  Element.prototype.scrollIntoView = vi.fn()
  const trigger = await screen.findByTestId('logs-node-select')
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  await userEvent.click(await screen.findByRole('option', { name: label }))
}

describe('LogsPage × 身份冲突人工清空', () => {
  beforeEach(() => {
    stubBaseHandlers()
  })

  it('① 有冲突且已选节点 → 出现清空入口；点击打开二次确认弹窗', async () => {
    loginMockUser()
    server.use(http.get(API('/logs/federation'), () => HttpResponse.json(conflictFederationBody())))

    renderWithProviders(<LogsPage />)
    await selectNode()

    const openBtn = await screen.findByTestId('logs-clear-conflicts-open')
    expect(openBtn).toBeInTheDocument()
    // 已有可用出路时不再叠加引导（避免噪音）。
    expect(screen.queryByTestId('logs-quality-guidance')).not.toBeInTheDocument()

    await userEvent.click(openBtn)
    expect(await screen.findByTestId('logs-clear-conflicts-dialog')).toBeInTheDocument()
    // 理由必填：未填时确认按钮禁用（避免一次必然失败的往返）。
    expect(screen.getByTestId('logs-clear-conflicts-confirm')).toBeDisabled()
  })

  it('② 无冲突时不出现清空入口（没有可清的东西就不该给按钮）', async () => {
    loginMockUser()
    server.use(http.get(API('/logs/federation'), () => HttpResponse.json(cleanFederationBody())))

    renderWithProviders(<LogsPage />)
    // 等首屏查询落地（避免"还没取到数据"被误判成"无冲突"）。
    await waitFor(() => expect(screen.getByText('conflict-seed-log-line')).toBeInTheDocument())
    expect(screen.queryByTestId('logs-clear-conflicts-open')).not.toBeInTheDocument()
  })

  it('③ 填写理由后可确认，且请求打到正确端点并携带理由', async () => {
    loginMockUser()
    server.use(http.get(API('/logs/federation'), () => HttpResponse.json(conflictFederationBody())))

    let posted: { url: string; body: unknown } | null = null
    server.use(
      http.post(API('/nodes/1/log-runtime/ingest/clear-identity-conflicts'), async ({ request }) => {
        posted = { url: request.url, body: await request.json() }
        return HttpResponse.json({
          state: 'cleared',
          clearedCount: 9,
          clearedSources: ['inst:145/file/g1'],
          skippedSources: [],
        })
      }),
    )

    renderWithProviders(<LogsPage />)
    await selectNode()
    await userEvent.click(await screen.findByTestId('logs-clear-conflicts-open'))
    await screen.findByTestId('logs-clear-conflicts-dialog')

    const confirm = screen.getByTestId('logs-clear-conflicts-confirm')
    const reason = screen.getByTestId('logs-clear-conflicts-reason')

    // 纯空白不算理由（后端同样会拒）。
    await userEvent.type(reason, '   ')
    expect(confirm).toBeDisabled()

    await userEvent.clear(reason)
    await userEvent.type(reason, '已核对 VL 中保留版本正确')
    expect(confirm).toBeEnabled()

    await userEvent.click(confirm)
    await waitFor(() => expect(posted).not.toBeNull())
    expect(posted!.url).toContain('/nodes/1/log-runtime/ingest/clear-identity-conflicts')
    // 理由进请求体（CP 再经可信 metadata 下传给 Worker 作为审计凭据）。
    expect(posted!.body).toMatchObject({ reason: '已核对 VL 中保留版本正确' })
    // 未指定单源 ⇒ 全节点范围（后端仅允许平台管理员）。
    expect(posted!.body).toMatchObject({ storageNamespace: '' })

    // 成功后弹窗关闭。
    await waitFor(() =>
      expect(screen.queryByTestId('logs-clear-conflicts-dialog')).not.toBeInTheDocument(),
    )
  })

  it('④ 未选中单一节点 → 不给清空入口（无法确定要清哪个节点）', async () => {
    loginMockUser()
    server.use(http.get(API('/logs/federation'), () => HttpResponse.json(conflictFederationBody())))

    renderWithProviders(<LogsPage />)
    // 等首屏查询落地后再断言缺席（避免测成"还没渲染出来"）。
    await waitFor(() => expect(screen.getByText('conflict-seed-log-line')).toBeInTheDocument())
    expect(screen.queryByTestId('logs-clear-conflicts-open')).not.toBeInTheDocument()
  })

  /**
   * 缺陷 2 回归：未选节点时用户不能被卡在「导出灰着、没有任何出路」的死角。
   * 清空是按节点下发的，所以此处不给按钮是对的；但必须明确告诉他先去选节点。
   */
  it('⑤ 有身份冲突但未选节点 → 不给入口，但给出「先选节点」的引导', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(completeConflictFederationBody())),
    )

    renderWithProviders(<LogsPage />)
    await waitFor(() => expect(screen.getByText('conflict-seed-log-line')).toBeInTheDocument())

    expect(screen.queryByTestId('logs-clear-conflicts-open')).not.toBeInTheDocument()
    // 引导必须指向可执行动作（节点选择器就在工具栏，真实存在）。
    const guidance = await screen.findByTestId('logs-quality-guidance')
    expect(guidance).toHaveTextContent('节点')
    expect(screen.getByTestId('logs-node-select')).toBeInTheDocument()
    // 导出仍被阻断，但原因说明在，不是无解释的灰按钮。
    expect(screen.getByTestId('logs-export-blocked')).toHaveTextContent('身份冲突')
  })

  /**
   * 缺陷 1 + 缺陷 3 回归：UNRESOLVED（重复事件无法对账）既没有可清的冲突，
   * 覆盖又是完整的（complete=true）。点「清空冲突」只会打到没有冲突的后端，
   * 而横幅若说「覆盖不完整」则是另一处谎报。
   */
  it('⑥ UNRESOLVED → 不给清空入口、不说覆盖不完整，并给出「无法人工清空」的引导', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(unresolvedFederationBody())),
    )

    renderWithProviders(<LogsPage />)
    // **刻意选中节点**：旧实现把 conflict/unresolved 混成一个布尔，选中节点后会给
    // unresolved 用户一个「点了也没东西可清」的按钮。这个前提让变异能被用例抓住。
    await selectNode()
    await waitFor(() => expect(screen.getByText('conflict-seed-log-line')).toBeInTheDocument())

    // 1) 不给「清空身份冲突」入口：unresolved 没有可清对象。
    expect(screen.queryByTestId('logs-clear-conflicts-open')).not.toBeInTheDocument()
    // 2) 必须给出真实可行的出路说明。
    const guidance = await screen.findByTestId('logs-quality-guidance')
    expect(guidance).toHaveTextContent('缩小时间范围')

    // 3) 质量标记在 DOM 上可区分：unresolved=true 且 conflict=false。
    const banner = screen.getByTestId('logs-coverage-banner')
    expect(banner).toHaveAttribute('data-quality-unresolved', 'true')
    expect(banner).toHaveAttribute('data-quality-conflict', 'false')
    expect(banner).toHaveAttribute('data-state', 'ready_complete')

    // 4) 覆盖完整性文案由覆盖原因决定：complete=true 时不得说「覆盖不完整」。
    const title = screen.getByTestId('logs-coverage-title')
    expect(title).toHaveTextContent('覆盖完整')
    expect(title).not.toHaveTextContent('部分日志暂时无法查询')
    expect(screen.queryByText('导出内容不完整')).not.toBeInTheDocument()
  })

  it('⑦ complete=true + conflict（已选节点）→ 清空入口可用，覆盖文案不谎报不完整', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(completeConflictFederationBody())),
    )

    renderWithProviders(<LogsPage />)
    await selectNode()

    // 有可清对象：入口出现，且不再需要额外引导。
    expect(await screen.findByTestId('logs-clear-conflicts-open')).toBeInTheDocument()
    expect(screen.queryByTestId('logs-quality-guidance')).not.toBeInTheDocument()

    const banner = screen.getByTestId('logs-coverage-banner')
    expect(banner).toHaveAttribute('data-state', 'ready_complete')
    expect(banner).toHaveAttribute('data-quality-conflict', 'true')
    expect(banner).toHaveAttribute('data-quality-unresolved', 'false')
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent(
      '覆盖完整，但存在待确认的身份冲突',
    )
    // 不得凭空补一条「导出内容不完整」的覆盖原因（旧实现会这么做）。
    expect(screen.queryByText('导出内容不完整')).not.toBeInTheDocument()
    // 导出被质量维度阻断的文案仍然保留（阻断是合理的，只是不能说成覆盖不完整）。
    expect(screen.getByTestId('logs-export-blocked')).toHaveTextContent('身份冲突')
  })
})
