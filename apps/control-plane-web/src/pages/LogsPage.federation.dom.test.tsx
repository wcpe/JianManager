/**
 * LogsPage × logs-federation 接线 DOM 强断言（FR-482）。
 *
 * 覆盖：
 * - partial/离线覆盖 → 横幅可见，失败不得呈空成功；
 * - resolveExportDownloadAffordance 阻断 → 导出按钮禁用；
 * - sourceTag=legacy → Legacy 徽标；404 → 横幅说明服务不可达，且**不降级**（本视图列表为空，
 *   经典 /logs 的行不再冒充本视图的内容）；
 * - Legacy 只读视图已随 CP logs 表下线（2026-10-29）——入口已删，旧深链回退默认视图。
 * - ErrFederatedNotReady → 联邦未就绪说明，空结果不当成功。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router'
import { queryClient } from '@/lib/query-client'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { FEDERATED_NOT_READY_MESSAGE } from '@/api/logFederation'
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
      message: 'federation-seed-log-line',
      time: '2026-07-18T12:00:00Z',
    },
  ],
  total: 1,
  page: 1,
  pageSize: 100,
}

function stubBaseHandlers() {
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([])),
    http.get(API('/instances'), () => HttpResponse.json([])),
    http.get(API('/logs'), () => HttpResponse.json(seedLogsPage)),
	http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [], coverage: { complete: true, targets: [] } })),
	http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], coverage: { complete: true, targets: [] } })),
    http.get(API('/logs/export'), () =>
      new HttpResponse('{"message":"x"}\n', {
        headers: { 'Content-Type': 'application/x-ndjson' },
      }),
    ),
  )
}

describe('LogsPage × logs-federation（FR-482）', () => {
  beforeEach(() => {
    stubBaseHandlers()
  })

  it('① partial 覆盖 → 横幅可见，不得呈空成功', async () => {
    loginMockUser()
    server.use(
      // FR-480/482 wire 形态：snake_case coverage + quality（后端门面 Search 包装）
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({
          ok: true,
          sourceTag: 'federated',
          available: true,
          items: [],
          quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
          coverage: {
            complete: false,
            partial_reasons: ['OFFLINE', 'BACKLOG'],
            targets: [{ target_id: 'n-offline', state: 'offline', reasons: ['OFFLINE'] }],
            enumeration_state: 'OPEN',
          },
        }),
      ),
      // 空结果对：覆盖不完整时仍不得显示「暂无日志」。
      // F-003：联邦活动时列表用 federation.items；此处空 items 触发空态但不可呈成功。
      http.get(API('/logs'), () =>
        HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 100 }),
      ),
    )

    renderWithProviders(<LogsPage />)
    const banner = await screen.findByTestId('logs-coverage-banner')
    expect(banner).toBeInTheDocument()
    expect(banner.dataset.tone).toBe('warning')
    expect(banner.dataset.state).toBe('offline')
    expect(banner.dataset.exportAllowed).toBe('false')
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent('部分节点当前不可达')

    // 失败/partial ≠ 空成功。
    await waitFor(() =>
      expect(screen.getByTestId('logs-empty-state')).toHaveAttribute(
        'data-empty-success',
        'false',
      ),
    )
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    expect(screen.getByTestId('logs-empty-not-success')).toBeInTheDocument()
  })

  it('② 覆盖不完整 → 导出按钮禁用（resolveExportDownloadAffordance）', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({
          ok: true,
          sourceTag: 'federated',
          items: seedLogsPage.items,
          quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
          coverage: {
            complete: false,
            partial_reasons: ['TRUNCATED'],
            targets: [{ target_id: 'n1', state: 'success', included: true }],
            enumeration_state: 'OPEN',
          },
        }),
      ),
    )

    renderWithProviders(<LogsPage />)
    await screen.findByTestId('logs-coverage-banner')
    const exportBtn = await screen.findByTestId('logs-export-trigger')
    await waitFor(() => expect(exportBtn).toBeDisabled())
    expect(screen.getByTestId('logs-export-blocked')).toBeInTheDocument()
    expect(screen.getByTestId('logs-coverage-banner').dataset.exportAllowed).toBe('false')
  })

  it('③ sourceTag=legacy → Legacy 徽标 + 覆盖横幅', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({
          sourceTag: 'legacy',
          items: seedLogsPage.items,
          total: 1,
          coverage: {
            sourceTag: 'legacy',
            complete: false,
            fromTime: '2020-01-01T00:00:00Z',
            toTime: '2023-01-01T00:00:00Z',
            ndjsonInScope: false,
            notes: ['legacy rows only'],
          },
          notes: ['legacy path: CP logs rows only'],
        }),
      ),
    )

    renderWithProviders(<LogsPage />)
    expect(await screen.findByTestId('logs-source-tag')).toHaveTextContent('Legacy')
    const banner = await screen.findByTestId('logs-coverage-banner')
    expect(banner.dataset.state).toBe('legacy')
    expect(screen.getAllByTestId('logs-source-tag-banner').length).toBeGreaterThan(0)
    // 横幅标题改为面向用户的说法（不再出现 Legacy 这类内部术语）。
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent('含切换前的历史数据')
  })

  it('④ 联邦 API 404 → 横幅说明服务不可达，且不拿经典表格的行冒充本视图', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({ message: 'not found' }, { status: 404 }),
      ),
    )

    renderWithProviders(<LogsPage />)
    const banner = await screen.findByTestId('logs-coverage-banner')
    expect(banner.dataset.degraded).toBe('true')
    // 404 属「接口不可达」而非「功能未启用」，文案不得误导用户去开启。
    const title = screen.getByTestId('logs-coverage-title')
    expect(title).toHaveTextContent('实时日志查询服务暂不可用')
    expect(title).not.toHaveTextContent('未启用')
    expect(await screen.findByTestId('logs-source-tag')).toHaveTextContent('Legacy')
    // 查询服务不可用 → 本视图就是没有数据：经典 /logs 的种子行（见 stubBaseHandlers 桩）不得渲染。
    // 若它们出现，用户会把平台/历史日志读成「节点/实例日志」，即静默换源式误导。
    await waitFor(() =>
      expect(screen.queryByText(/federation-seed-log-line/)).not.toBeInTheDocument(),
    )
    // 空列表也不得呈成功空态（否则等于宣称「查过且确实没有」）。
    expect(await screen.findByTestId('logs-empty-state')).toHaveAttribute(
      'data-empty-success',
      'false',
    )
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    // 经典导出同步收回：它导出的是平台日志，与当前视图无关（同「静默换源」的误导）。
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
    expect(banner.dataset.exportAllowed).toBe('false')
  })

  it('Legacy 只读入口已随 CP logs 表下线，不得留下死入口', async () => {
	const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
	loginMockUser(`mock.${payload}.sig`)
	const legacyCalls: string[] = []
	server.use(
	  http.get(API('/logs/federation'), () => HttpResponse.json({ sourceTag: 'federated', items: seedLogsPage.items, coverage: { complete: true, targets: [] } })),
	  http.get(API('/logs/legacy'), () => {
		legacyCalls.push('called')
		return HttpResponse.json({ items: [], total: 0 })
	  }),
	)
	// 回归背景（2026-10-29）：把 legacyMode 恒置 false 却留下「更多 → Legacy 只读」菜单项，
	// 点击后 URL/视图都不变、列表仍是联邦行——一个点了没反应的死入口。现整条入口删除。
	renderWithProviders(<LogsPage />, { route: '/logs?view=legacy' })
	await screen.findByText('federation-seed-log-line')
	// ?view=legacy 是非法值（已从 LOG_VIEWS 移除）→ 回退默认视图，且不发 /logs/legacy。
	expect(screen.getByRole('tab', { name: '节点/实例日志' })).toHaveAttribute('aria-selected', 'true')
	expect(screen.queryByTestId('logs-more-trigger')).not.toBeInTheDocument()
	expect(screen.queryByTestId('logs-view-legacy')).not.toBeInTheDocument()
	expect(legacyCalls).toEqual([])
  })

  it('F-003 联邦返回唯一事件时列表必须显示联邦 items，不得混入 Legacy 行', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({
          ok: true,
          sourceTag: 'federated',
          available: true,
          items: [
            {
              event_id: 'fed-1',
              log_source_id: 'worker-1',
              level: 'ERROR',
              message: 'federation-only-event-line',
              event_time_utc: '2026-09-21T00:00:00Z',
            },
          ],
          quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
          coverage: {
            complete: true,
            partial_reasons: [],
            targets: [{ target_id: 'w1', state: 'success', included: true }],
            enumeration_state: 'EXHAUSTED',
          },
        }),
      ),
      http.get(API('/logs'), () =>
        HttpResponse.json({
          items: [
            {
              id: 99,
              source: 'instance',
              level: 'info',
              instanceId: 1,
              instanceUuid: 'x',
              nodeId: 1,
              message: 'legacy-should-not-show-when-federated',
              time: '2026-09-20T00:00:00Z',
            },
          ],
          total: 1,
          page: 1,
          pageSize: 100,
        }),
      ),
    )

    renderWithProviders(<LogsPage />)
    expect(await screen.findByText('federation-only-event-line')).toBeInTheDocument()
    expect(screen.queryByText('legacy-should-not-show-when-federated')).not.toBeInTheDocument()
  })

  it('⑤ ErrFederatedNotReady → 联邦未就绪说明，空结果不当成功', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs'), () =>
        HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 100 }),
      ),
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({
          sourceTag: 'federated',
          federatedReady: false,
          items: [],
          notes: [FEDERATED_NOT_READY_MESSAGE],
        }),
      ),
    )

    renderWithProviders(<LogsPage />)
    const banner = await screen.findByTestId('logs-coverage-banner')
    // 引擎未就绪 → 横幅保留该诊断（不得退化成「含切换前的历史数据」），
    // 且说明服务未启用、已回退平台与历史日志。
    expect(banner.dataset.state).toBe('engine_not_ready')
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent(
      /日志查询服务未启用|已自动切换到平台与历史日志/,
    )
    await waitFor(() =>
      expect(screen.getByTestId('logs-empty-state')).toHaveAttribute(
        'data-empty-success',
        'false',
      ),
    )
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
  })

  it('⑥ 覆盖完整 + exportVerified → 导出可用，横幅隐藏', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({
          sourceTag: 'federated',
          items: seedLogsPage.items,
          coverage: { complete: true, partialReasons: [], targets: [] },
          quality: { duplicateQuality: 'EXACT', statsQuality: 'EXACT' },
          exportStatus: 'READY',
          exportVerified: true,
        }),
      ),
    )

    renderWithProviders(<LogsPage />)
    expect(await screen.findByText(/federation-seed-log-line/)).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByTestId('logs-export-trigger')).not.toBeDisabled(),
    )
    // complete 且无 partial → 横幅不展示。
    await waitFor(() =>
      expect(screen.queryByTestId('logs-coverage-banner')).not.toBeInTheDocument(),
    )
  })

	it('联邦 next_cursor 驱动下一页且保持完整 coverage', async () => {
		loginMockUser()
		// 未枚举完的窗口必须沿原视图续游标，同戳尾部不能靠时间锚跳过。
		const requests: URLSearchParams[] = []
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				const query = new URL(request.url).searchParams
				requests.push(query)
				const second = query.get('cursor') === 'cursor-page-2' && query.get('viewId') === 'cv-pages'
				return HttpResponse.json({
					sourceTag: 'federated',
					items: [{
						event_id: second ? 'page-2' : 'page-1', log_source_id: 'node:1',
						message: second ? 'federation-page-two' : 'federation-page-one',
						event_time_utc: '2026-09-22T12:00:00.123456789Z',
					}],
					coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: second ? 'EXHAUSTED' : 'OPEN' },
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					view: { view_id: 'cv-pages' }, exhausted: second,
					next_cursor: second ? '' : 'cursor-page-2',
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		expect(await screen.findByText('federation-page-one')).toBeInTheDocument()
		await user.click(screen.getByRole('button', { name: '下一页' }))
		expect(await screen.findByText('federation-page-two')).toBeInTheDocument()
		await waitFor(() => expect(screen.queryByText('federation-page-one')).not.toBeInTheDocument())
		const secondRequest = requests.find((query) => query.get('cursor') === 'cursor-page-2')!
		const firstRequest = requests.filter((query) => !query.has('cursor')).at(-1)!
		expect(secondRequest.get('viewId')).toBe('cv-pages')
		expect(secondRequest.get('from')).toBe(firstRequest.get('from'))
		expect(secondRequest.get('to')).toBe(firstRequest.get('to'))
		expect(screen.queryByTestId('logs-coverage-banner')).not.toBeInTheDocument()
	})

	it('预算截断的空页仍沿原游标续页，不前移窗口', async () => {
		loginMockUser()
		const requests: URLSearchParams[] = []
		server.use(http.get(API('/logs/federation'), ({ request }) => {
			const query = new URL(request.url).searchParams
			requests.push(query)
			const next = query.get('cursor') === 'budget-cursor'
			return HttpResponse.json({ sourceTag: 'federated', items: next ? seedLogsPage.items : [],
				view: { view_id: 'budget-view' }, next_cursor: next ? '' : 'budget-cursor', exhausted: next,
				coverage: { complete: false, partial_reasons: ['TRUNCATED'], targets: [] } })
		}))
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		await screen.findByTestId('logs-empty-not-success')
		await waitFor(() => expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled())
		await user.click(screen.getByRole('button', { name: '下一页' }))
		await screen.findByText('federation-seed-log-line')
		const next = requests.find((query) => query.get('cursor') === 'budget-cursor')!
		expect(next.get('viewId')).toBe('budget-view')
		expect(next.get('to')).toBe(requests.filter((query) => !query.has('cursor')).at(-1)!.get('to'))
	})

	it('跨节点扫描预算截断且无安全游标时，空items保留不完整警告', async () => {
		loginMockUser()
		server.use(http.get(API('/logs/federation'), () => HttpResponse.json({
			sourceTag: 'federated', items: [], next_cursor: '', exhausted: false, budget_cut: true,
			view: { view_id: 'scan-cut-view' },
			coverage: { complete: false, partial_reasons: ['TRUNCATED'], targets: [], enumeration_state: 'OPEN' },
			quality: { duplicate_quality: 'exact', stats_quality: 'partial' },
		})))
		renderWithProviders(<LogsPage />)
		const banner = await screen.findByTestId('logs-coverage-banner')
		expect(banner).toHaveAttribute('data-state', 'truncated')
		expect(banner).toHaveAttribute('data-export-allowed', 'false')
		expect(await screen.findByTestId('logs-empty-not-success')).toBeInTheDocument()
		expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
		expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
	})

	it('窗口枚举结束才前移到原窗口下界，返回上一页复用缓存', async () => {
		loginMockUser()
		const requests: URLSearchParams[] = []
		let firstFrom = ''
		server.use(http.get(API('/logs/federation'), ({ request }) => {
			const query = new URL(request.url).searchParams
			requests.push(query)
			const shifted = query.get('to') === firstFrom
			if (!shifted) firstFrom = query.get('from') ?? ''
			return HttpResponse.json({ sourceTag: 'federated', items: [{ ...seedLogsPage.items[0], message: shifted ? 'earlier-window' : 'finished-window' }],
				view: { view_id: shifted ? 'earlier-view' : 'finished-view' }, next_cursor: '', exhausted: true,
				coverage: { complete: true, targets: [] } })
		}))
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		await screen.findByText('finished-window')
		await waitFor(() => expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled())
		await user.click(screen.getByRole('button', { name: '下一页' }))
		await screen.findByText('earlier-window')
		expect(requests.at(-1)!.get('to')).toBe(firstFrom)
		expect(requests.at(-1)!.has('cursor')).toBe(false)
		expect(requests.at(-1)!.has('viewId')).toBe(false)
		const callCount = requests.length
		await user.click(screen.getByRole('button', { name: '上一页' }))
		await screen.findByText('finished-window')
		expect(requests).toHaveLength(callCount)
	})

	it('切筛选丢弃旧游标但保留列表直到新响应，刷新中禁止续旧窗口', async () => {
		loginMockUser()
		let release = () => {}
		let started = false
		const responseGate = new Promise<void>((resolve) => { release = resolve })
		let filteredRequest: URLSearchParams | undefined
		server.use(http.get(API('/logs/federation'), async ({ request }) => {
			const query = new URL(request.url).searchParams
			if (query.has('keyword')) {
				filteredRequest = query
				started = true
				await responseGate
			}
			return HttpResponse.json({ sourceTag: 'federated', items: [{ ...seedLogsPage.items[0], message: query.has('keyword') ? 'filtered-page' : query.has('cursor') ? 'old-second-page' : 'old-first-page' }],
				view: { view_id: 'original-view' }, next_cursor: 'original-cursor', exhausted: false,
				coverage: { complete: true, targets: [] } })
		}))
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		await screen.findByText('old-first-page')
		await waitFor(() => expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled())
		await user.click(screen.getByRole('button', { name: '下一页' }))
		await screen.findByText('old-second-page')
		await user.type(screen.getByPlaceholderText('搜索日志内容…'), 'x')
		await waitFor(() => expect(started).toBe(true))
		expect(screen.getByText('old-second-page')).toBeInTheDocument()
		expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
		expect(filteredRequest!.has('cursor')).toBe(false)
		expect(filteredRequest!.has('viewId')).toBe(false)
		expect(filteredRequest!.get('page')).toBe('1')
		release()
		await screen.findByText('filtered-page')
	})

	it('身份及权限切换清除共享联邦缓存，拒绝结果不展示上一主体数据', async () => {
		const adminToken = `mock.${btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`
		const memberToken = `mock.${btoa(JSON.stringify({ userId: 2, username: 'member', role: 0, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`
		loginMockUser(adminToken)
		server.use(http.get(API('/logs/federation'), ({ request }) => {
			if (request.headers.get('authorization') === `Bearer ${memberToken}`) return HttpResponse.json({ message: 'forbidden' }, { status: 403 })
			return HttpResponse.json({ sourceTag: 'federated', items: seedLogsPage.items,
				coverage: { complete: true, targets: [] } })
		}))
		const mount = () => render(<BrowserRouter><QueryClientProvider client={queryClient}><LogsPage /></QueryClientProvider></BrowserRouter>)
		const first = mount()
		await screen.findByText('federation-seed-log-line')
		expect(queryClient.getQueriesData({ queryKey: ['logsFederation'] }).length).toBeGreaterThan(0)
		first.unmount()
		loginMockUser(memberToken)
		expect(queryClient.getQueriesData({ queryKey: ['logsFederation'] })).toEqual([])
		mount()
		await screen.findByTestId('logs-coverage-banner')
		expect(screen.queryByText('federation-seed-log-line')).not.toBeInTheDocument()
	})

	it('Stats 与 level Facets 复用同scope搜索视图，独立完整且质量精确时显示精确计数', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated', items: seedLogsPage.items, view: { view_id: 'cv-insights' }, exhausted: true,
				coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
			})),
      // 服务端允许同scope复用固定视图；不要再让旧的错配观察固化成错误契约。
			http.get(API('/logs/federation/stats'), ({ request }) => {
				expect(new URL(request.url).searchParams.get('viewId')).toBe('cv-insights')
				return HttpResponse.json({ points: [{ dimensions: { level: 'ERROR' }, count: 42 }], coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } })
			}),
			http.get(API('/logs/federation/facets'), ({ request }) => {
				expect(new URL(request.url).searchParams.get('viewId')).toBe('cv-insights')
				return HttpResponse.json({ dimensions: [{ dimension: 'level', values: [{ value: 'ERROR', count: 42 }] }], coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } })
			}),
		)
		renderWithProviders(<LogsPage />)
		const insights = await screen.findByTestId('logs-federation-insights')
		expect(insights).toHaveTextContent('当前视图 42 个事件')
		// facets 串行在 stats 落定之后（统计 → 维度），文本到达需等待。
		await waitFor(() => expect(insights).toHaveTextContent('ERROR 42'))
	})

	it('Stats 使用后端真实 wire 形态 agg.count 呈现事件数', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated', items: seedLogsPage.items, view: { view_id: 'cv-agg' }, exhausted: true,
				coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
			})),
			// 后端 StatsPoint 真源：points[].agg.count（非顶层 count）。
			http.get(API('/logs/federation/stats'), () =>
				HttpResponse.json({ points: [{ agg: { count: 3, sum: 0, min: 0, max: 0, has_min_max: true } }], coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } }),
			),
			http.get(API('/logs/federation/facets'), () =>
				HttpResponse.json({ dimensions: [{ dimension: 'level', values: [{ value: '', count: 3 }] }], coverage: { complete: true } }),
			),
		)
		renderWithProviders(<LogsPage />)
		const insights = await screen.findByTestId('logs-federation-insights')
		expect(insights).toHaveTextContent('当前视图 3 个事件')
	})

	it('实时跟随切换到 FOLLOW_LIVE 联邦 Tail', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated', items: seedLogsPage.items, exhausted: true,
				coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
			})),
			http.get(API('/logs/federation/tail'), ({ request }) => {
				expect(new URL(request.url).searchParams.get('mode')).toBe('FOLLOW_LIVE')
				return HttpResponse.json({
					// 默认时间范围为有界窗口（最近 24h）：夹具事件时间必须落在窗口内，
					// 否则会被（正确地）过滤掉而看不到实时事件——这正是有界窗口的预期行为。
					items: [{ event_id: 'live-1', log_source_id: 'node:1', message: 'federation-live-tail', event_time_utc: new Date().toISOString() }],
					coverage: { complete: true, targets: [], enumeration_state: 'OPEN' }, exhausted: false,
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		// 默认时间窗为 24h（2026-09-30 性能依据）；固定选择「全部时间」，
		// 让本用例只验证「实时跟随」机制本身，不随默认窗口漂移。
		// 时间范围是 Select（combobox），不是按钮。
		HTMLElement.prototype.hasPointerCapture ??= () => false
		HTMLElement.prototype.setPointerCapture ??= () => {}
		HTMLElement.prototype.releasePointerCapture ??= () => {}
		HTMLElement.prototype.scrollIntoView ??= () => {}
		await user.click(screen.getAllByRole('combobox')[0])
		await user.click(await screen.findByRole('option', { name: '全部时间' }))
		await user.click(await screen.findByRole('button', { name: '实时跟随' }))
		expect(await screen.findByText('federation-live-tail')).toBeInTheDocument()
	})

	// 回归（P2-9）：跟随态必须**不下发 `to` 上界**。
	//
	// 旧实现把 `to` 钉在浏览器 now：Worker/实例时钟略快、或落盘时区未校正（+8h）产生的
	// 「未来时间戳」事件会被后端按 [from,to] 排掉 → 实时跟随永远空（用户观感「不实时」）。
	// 本用例的事件时间戳 = now + 8h（未校正时区偏移的实测量级），并让假后端忠实复刻
	// 「to 非空即按上界过滤」的窗口语义：改回 `to=now` 即转红。
	it('实时跟随不下发 to 上界：时钟偏移的未来时间戳事件仍可见（P2-9）', async () => {
		loginMockUser()
		const skewedItem = {
			event_id: 'live-skew',
			log_source_id: 'node:1',
			message: 'federation-skewed-live',
			event_time_utc: new Date(Date.now() + 8 * 3600_000).toISOString(),
		}
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated', items: [], exhausted: true,
				coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
			})),
			http.get(API('/logs/federation/tail'), ({ request }) => {
				const params = new URL(request.url).searchParams
				expect(params.get('mode')).toBe('FOLLOW_LIVE')
				expect(params.get('from')).toBeTruthy()
				const to = params.get('to')
				// 后端窗口语义：[from, to]；上界存在时，晚于 to 的事件必须被排除。
				const items = to && new Date(skewedItem.event_time_utc) > new Date(to) ? [] : [skewedItem]
				return HttpResponse.json({
					items,
					coverage: { complete: true, targets: [], enumeration_state: 'OPEN' }, exhausted: false,
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		// 保持默认时间窗（现为「最近 15 分钟」，见 LogsPage 的默认档）：本用例只验证跟随态的
		// 上界语义，不触碰时间范围选择器。
		await user.click(await screen.findByRole('button', { name: '实时跟随' }))
		expect(await screen.findByText('federation-skewed-live')).toBeInTheDocument()
	})

	it('⑦ gap + nextCursor=null → 缺口态横幅 + 游标结束提示（不等于历史完整）', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				ok: true, sourceTag: 'federated', available: true, items: seedLogsPage.items,
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
				coverage: { complete: false, partial_reasons: ['GAP'], targets: [], enumeration_state: 'EXHAUSTED' },
				next_cursor: null,
			})),
		)
		renderWithProviders(<LogsPage />)
		const banner = await screen.findByTestId('logs-coverage-banner')
		expect(banner.dataset.state).toBe('gap')
		expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent('该时间段内有日志缺失')
		expect(await screen.findByTestId('logs-next-cursor-hint')).toBeInTheDocument()
	})

	it('⑧ backlog → 采集积压态横幅', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				ok: true, sourceTag: 'federated', available: true, items: [],
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
				coverage: { complete: false, partial_reasons: ['BACKLOG'], targets: [], enumeration_state: 'OPEN' },
			})),
		)
		renderWithProviders(<LogsPage />)
		const banner = await screen.findByTestId('logs-coverage-banner')
		expect(banner.dataset.state).toBe('backlog')
		expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent('正在写入最新日志')
	})

	it('⑨ rehydrate_failed → 「发起回灌」引导可点击并跳转节点页', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				ok: true, sourceTag: 'federated', available: true, items: [],
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
				coverage: { complete: false, partial_reasons: ['REHYDRATE_FAILED'], targets: [], enumeration_state: 'OPEN' },
			})),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		const banner = await screen.findByTestId('logs-coverage-banner')
		expect(banner.dataset.state).toBe('rehydrate_failed')
		await user.click(screen.getByRole('button', { name: '发起回灌' }))
		await waitFor(() => expect(window.location.pathname).toBe('/nodes'))
	})

	it('⑩ archive_missing → 「发起回灌」引导（归档层缺失修复入口）', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				ok: true, sourceTag: 'federated', available: true, items: [],
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
				coverage: { complete: false, partial_reasons: ['ARCHIVE_MISSING'], targets: [], enumeration_state: 'OPEN' },
			})),
		)
		renderWithProviders(<LogsPage />)
		const banner = await screen.findByTestId('logs-coverage-banner')
		expect(banner.dataset.state).toBe('archive_missing')
		expect(screen.getByRole('button', { name: '发起回灌' })).toBeInTheDocument()
	})

	it('⑪ view_stale → 「重新打开视图」引导，点击后重新查询', async () => {
		loginMockUser()
		let calls = 0
		server.use(
			http.get(API('/logs/federation'), () => {
				calls += 1
				return HttpResponse.json({
					ok: true, sourceTag: 'federated', available: true, items: seedLogsPage.items,
					errorCode: 'VIEW_STALE',
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					coverage: { complete: false, partial_reasons: [], targets: [], enumeration_state: 'STALE' },
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		const banner = await screen.findByTestId('logs-coverage-banner')
		expect(banner.dataset.state).toBe('view_stale')
		const before = calls
		await user.click(screen.getByRole('button', { name: '重新打开视图' }))
		await waitFor(() => expect(calls).toBeGreaterThan(before))
	})

	it('⑫ offline → 「仅查在线节点」引导携带 onlineOnly=true 重新请求', async () => {
		loginMockUser()
		const seen: string[] = []
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				seen.push(request.url)
				return HttpResponse.json({
					ok: true, sourceTag: 'federated', available: true, items: [],
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					coverage: {
						complete: false, partial_reasons: ['OFFLINE'],
						targets: [{ target_id: 'n-offline', state: 'offline', reasons: ['OFFLINE'] }],
						enumeration_state: 'OPEN',
					},
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		await screen.findByTestId('logs-coverage-banner')
		await user.click(screen.getByRole('button', { name: '仅查在线节点' }))
		await waitFor(() =>
			expect(seen.some((u) => new URL(u).searchParams.get('onlineOnly') === 'true')).toBe(true),
		)
	})

	it('⑭ 来源为原始 log_source_id 时归一显示为可读标签', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated',
				items: [
					// 事件结构只有 log_source_id，取值形如 inst:<实例ID>/<流>
					{ id: 1, log_source_id: 'inst:144/file', level: 'info', instanceId: 144, nodeId: 1, message: 'instance-src-line', time: '2026-07-18T12:00:00Z' },
					{ id: 2, log_source_id: 'node:1/worker', level: 'info', instanceId: 0, nodeId: 1, message: 'worker-src-line', time: '2026-07-18T12:00:01Z' },
				],
				coverage: { complete: true, partial_reasons: [], targets: [] },
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
			})),
		)
		renderWithProviders(<LogsPage />)
		// 归一后应显示 i18n 标签（实例/节点），而非原始 inst:144/file
		await screen.findByText('instance-src-line')
		expect(screen.queryByText('inst:144/file')).toBeNull()
		expect(screen.getAllByText('实例').length).toBeGreaterThan(0)
		expect(screen.getByText('worker-src-line')).toBeInTheDocument()
		expect(screen.getAllByText('节点').length).toBeGreaterThan(0)
	})

	it('⑮ 选择来源后 source 参数下发到联邦请求', async () => {
		// 来源下拉仅在「全部日志」视图渲染，该视图仅平台管理员可见（与既有用例同一 token 构造）
		const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
		loginMockUser(`mock.${payload}.sig`)
		const seen: string[] = []
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				seen.push(request.url)
				return HttpResponse.json({
					sourceTag: 'federated', items: seedLogsPage.items,
					coverage: { complete: true, partial_reasons: [], targets: [] },
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
				})
			}),
		)
		renderWithProviders(<LogsPage />)
		await screen.findByText('federation-seed-log-line')
		const user = userEvent.setup()
		// 来源下拉仅在「全部日志」视图渲染，须先切换视图
		await user.click(screen.getByRole('tab', { name: '全部日志' }))
		const trigger = await screen.findByTestId('logs-source-select')
		// jsdom 未实现 scrollIntoView，Radix Select 展开时会调用它；仅本用例需要该垫片。
		Element.prototype.scrollIntoView = vi.fn()
		// Radix Select 用键盘激活：聚焦后回车展开，再选「实例」项
		trigger.focus()
		await user.keyboard('{Enter}')
		const option = await screen.findByRole('option', { name: '实例' })
		await user.click(option)
		await waitFor(() =>
			expect(seen.some((u) => new URL(u).searchParams.get('source') === 'instance')).toBe(true),
		)
	})

	it('⑬ 覆盖完整且校验通过 → 导出按钮提示下载前复核权限', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated', items: seedLogsPage.items,
				coverage: { complete: true, partial_reasons: [], targets: [] },
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
				exportStatus: 'READY', exportVerified: true,
			})),
		)
		renderWithProviders(<LogsPage />)
		const trigger = await screen.findByTestId('logs-export-trigger')
		await waitFor(() => expect(trigger).not.toBeDisabled())
		await waitFor(() =>
			expect(trigger).toHaveAttribute('title', expect.stringContaining('下载前将再次复核权限')),
		)
	})

	/**
	 * 2026-10-29 改：平台视图的数据源已从 CP `logs` 表（`/logs` 经典接口）换成
	 * **进程日志文件尾部**（`/diagnostics/log`），且联邦请求依旧不得被它触发。
	 *
	 * 原用例断的是 `GET /logs` 被调用——那条路径已随 logs 表下线，留着它只会让测试
	 * 靠一个不存在的接口假绿。现在改盯真实数据源，并把「不跨源」这条不变量保留。
	 */
	it('平台视图只读进程日志文件尾部，不应发起联邦覆盖请求', async () => {
		const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
		loginMockUser(`mock.${payload}.sig`)
		let federationCalls = 0
		server.use(
			http.get(API('/logs/federation'), () => {
				federationCalls += 1
				return HttpResponse.json({ sourceTag: 'federated', items: seedLogsPage.items, coverage: { complete: true, targets: [] } })
			}),
			http.get(API('/diagnostics/log'), () =>
				HttpResponse.json({
					content: 'platform-file-tail-line\n',
					truncated: false, fileBytes: 26, path: '/var/log/control-plane.log',
				}),
			),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		await screen.findByText('federation-seed-log-line')
		const federationBefore = federationCalls
		await user.click(screen.getByRole('tab', { name: '平台日志' }))
		expect(await screen.findByText('platform-file-tail-line')).toBeInTheDocument()
		expect(federationCalls).toBe(federationBefore)
		expect(screen.queryByText('federation-seed-log-line')).not.toBeInTheDocument()
	})

	/**
	 * 回归（2026-10-19，补 M-1 缺口）：级别筛选必须真正下发到联邦请求。
	 *
	 * 原 LogsPage.dom.test.tsx 覆盖过「级别筛选 error → 仅错误行保留」，该文件已按用户裁定
	 * 删除；复核后这是仅剩的两处无守卫行为之一（另一处是深链 instanceId）。级别 pill 是
	 * 用户直接可感的控件，改坏后若不红，只能靠人工发现。
	 */
	it('⑯ 点击级别 pill 后 level 参数下发到联邦请求', async () => {
		loginMockUser()
		const levelCalls: string[] = []
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				const query = new URL(request.url).searchParams
				levelCalls.push(query.get('level') ?? '')
				return HttpResponse.json({
					sourceTag: 'federated',
					items: seedLogsPage.items,
					coverage: { complete: true, partial_reasons: [], targets: [] },
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					view: { view_id: 'level-view' },
				})
			}),
		)
		renderWithProviders(<LogsPage />)
		await screen.findByText('federation-seed-log-line')

		const user = userEvent.setup()
		// pill 文案由 logs.level_error 提供（「错误」）。
		await user.click(screen.getByRole('button', { name: '错误' }))
		await waitFor(() => expect(levelCalls).toContain('error'), {
			message: '点击「错误」后 level=error 必须出现在联邦请求里',
		})

	})

	/**
	 * 回归（2026-10-19，补 M-1 缺口）：FR-345 深链的 instanceId 必须一路带到联邦请求。
	 *
	 * 深链（如从实例详情跳「该实例日志」）依赖 URL 上的 instanceId；若它在中途被丢掉，
	 * 用户会看到全平台日志而不是该实例的日志——错误不报、数据看似正常，最难发现。
	 */
	it('⑰ FR-345 深链 instanceId 传递到联邦请求，且切换级别后仍保留', async () => {
		loginMockUser()
		const seen: URLSearchParams[] = []
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				const query = new URL(request.url).searchParams
				seen.push(query)
				return HttpResponse.json({
					sourceTag: 'federated',
					items: seedLogsPage.items,
					coverage: { complete: true, partial_reasons: [], targets: [] },
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					view: { view_id: 'deep-link-view' },
				})
			}),
		)
		renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
		await screen.findByText('federation-seed-log-line')

		await waitFor(() => {
			const last = seen[seen.length - 1]
			expect(last?.get('instanceId')).toBe('2')
		}, { message: '深链 instanceId 必须下发到联邦请求' })

		// 改级别不得把 instanceId 丢掉（筛选条件是叠加而非替换）。
		const user = userEvent.setup()
		await user.click(screen.getByRole('button', { name: '错误' }))
		await waitFor(() => {
			const last = seen[seen.length - 1]
			expect(last?.get('instanceId')).toBe('2')
			expect(last?.get('level')).toBe('error')
		}, { message: '切换级别后 instanceId 与 level 必须同时保留' })
	})

	/**
	 * 回归（2026-10-19，评审复核纠正；2026-10-29 改为跨视图而非 Legacy）：切视图**保留**
	 * instanceId / nodeId 筛选。
	 *
	 * 曾一度在 switchView 里清掉两者，依据是「平台与历史日志不带这两个维度」——该前提是错的，
	 * 已回退；清掉只会把用户已经选定的范围放大，属于替用户改查询。
	 *
	 * Legacy 视图已于 2026-10-29 随 CP logs 表下线，本不变量改由「全部日志」视图承载：
	 * 它仍走同一条联邦查询链路，最能体现“切视图不改筛选”。
	 */
	it('⑲ 切换视图时保留 instanceId 筛选', async () => {
		const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
		loginMockUser(`mock.${payload}.sig`)
		const fedCalls: URLSearchParams[] = []
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				fedCalls.push(new URL(request.url).searchParams)
				return HttpResponse.json({
					sourceTag: 'federated',
					items: seedLogsPage.items,
					coverage: { complete: true, partial_reasons: [], targets: [] },
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					view: { view_id: 'switch-view' },
				})
			}),
		)
		// 深链进入时带 instanceId，模拟「从实例详情跳该实例日志」的真实路径。
		renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
		await screen.findByText('federation-seed-log-line')
		expect(fedCalls.at(-1)?.get('instanceId')).toBe('2')

		const user = userEvent.setup()
		// 「全部日志」仅平台管理员渲染（token 已按 admin 构造）。
		await user.click(screen.getByRole('tab', { name: '全部日志' }))

		await waitFor(() => {
			const last = fedCalls.at(-1)
			expect(last?.get('view')).toBe('all')
			expect(last?.get('instanceId')).toBe('2')
		}, { message: '切换视图后 instanceId 必须保留' })
	})
})
