/**
 * LogsPage × logs-federation 接线 DOM 强断言（FR-482）。
 *
 * 覆盖：
 * - partial/离线覆盖 → 横幅可见，失败不得呈空成功；
 * - resolveExportDownloadAffordance 阻断 → 导出按钮禁用；
 * - sourceTag=legacy → Legacy 徽标；404 → 降级 legacy 视图 + 横幅；
 * - ErrFederatedNotReady → 联邦未就绪说明，空结果不当成功。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
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

  it('④ 联邦 API 404 → 降级 legacy 视图 + 横幅，经典表格仍渲染', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({ message: 'not found' }, { status: 404 }),
      ),
    )

    renderWithProviders(<LogsPage />)
    // 表格仍走经典 /logs。
    expect(await screen.findByText(/federation-seed-log-line/)).toBeInTheDocument()
    const banner = await screen.findByTestId('logs-coverage-banner')
    expect(banner.dataset.degraded).toBe('true')
    // 404 属「接口不可达」而非「功能未启用」，文案不得误导用户去开启。
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent(
      /实时日志查询服务暂不可用|回退平台与历史日志/,
    )
    expect(await screen.findByTestId('logs-source-tag')).toHaveTextContent('Legacy')
    // 降级路径保留经典导出（非联邦下载门禁）。
    await waitFor(() =>
      expect(screen.getByTestId('logs-export-trigger')).not.toBeDisabled(),
    )
  })

  it('Legacy 只读页签不与联邦集合拼接，并关闭导出和实时跟随', async () => {
	const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
	loginMockUser(`mock.${payload}.sig`)
	server.use(
	  http.get(API('/logs/federation'), () => HttpResponse.json({ sourceTag: 'federated', items: [], coverage: { complete: true, targets: [] } })),
	  http.get(API('/logs/legacy'), () => HttpResponse.json({
		sourceTag: 'legacy', items: [{ ...seedLogsPage.items[0], message: 'legacy-explicit-row' }],
		total: 1, page: 1, pageSize: 100,
		coverage: { sourceTag: 'legacy', complete: false, notes: ['legacy rows only'] },
	  })),
	)
	const user = userEvent.setup()
	renderWithProviders(<LogsPage />)
	// Legacy 只读入口已收纳到右侧「更多」菜单（不再与主视图并列的页签）。
	await user.click(await screen.findByTestId('logs-more-trigger'))
	await user.click(await screen.findByTestId('logs-view-legacy'))
	expect(await screen.findByText('legacy-explicit-row')).toBeInTheDocument()
	expect(await screen.findByTestId('logs-source-tag')).toHaveTextContent(/Legacy/i)
	expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
	expect(screen.getByRole('button', { name: '实时跟随' })).toBeDisabled()
	expect(screen.queryByText('federation-seed-log-line')).not.toBeInTheDocument()
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
		server.use(
			http.get(API('/logs/federation'), ({ request }) => {
				const cursor = new URL(request.url).searchParams.get('cursor')
				return HttpResponse.json({
					sourceTag: 'federated',
					items: [{
						event_id: cursor ? 'page-2' : 'page-1', log_source_id: 'node:1',
						message: cursor ? 'federation-page-two' : 'federation-page-one',
						event_time_utc: cursor ? '2026-09-22T11:00:00Z' : '2026-09-22T12:00:00Z',
					}],
					coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: cursor ? 'EXHAUSTED' : 'OPEN' },
					quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
					view: { view_id: 'cv-pages' }, exhausted: !!cursor,
					next_cursor: cursor ? '' : 'cursor-page-2',
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		expect(await screen.findByText('federation-page-one')).toBeInTheDocument()
		await user.click(screen.getByRole('button', { name: '下一页' }))
		expect(await screen.findByText('federation-page-two')).toBeInTheDocument()
		await waitFor(() => expect(screen.queryByText('federation-page-one')).not.toBeInTheDocument())
		expect(screen.queryByTestId('logs-coverage-banner')).not.toBeInTheDocument()
	})

	it('同一联邦 View 展示 Stats 与 level Facets', async () => {
		loginMockUser()
		server.use(
			http.get(API('/logs/federation'), () => HttpResponse.json({
				sourceTag: 'federated', items: seedLogsPage.items, view: { view_id: 'cv-insights' }, exhausted: true,
				coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
				quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
			})),
			http.get(API('/logs/federation/stats'), ({ request }) => {
				expect(new URL(request.url).searchParams.get('viewId')).toBe('cv-insights')
				return HttpResponse.json({ points: [{ dimensions: { level: 'ERROR' }, count: 42 }], coverage: { complete: true } })
			}),
			http.get(API('/logs/federation/facets'), ({ request }) => {
				expect(new URL(request.url).searchParams.get('viewId')).toBe('cv-insights')
				return HttpResponse.json({ dimensions: [{ dimension: 'level', values: [{ value: 'ERROR', count: 42 }] }], coverage: { complete: true } })
			}),
		)
		renderWithProviders(<LogsPage />)
		const insights = await screen.findByTestId('logs-federation-insights')
		expect(insights).toHaveTextContent('当前视图 42 个事件')
		expect(insights).toHaveTextContent('ERROR 42')
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
				HttpResponse.json({ points: [{ agg: { count: 3, sum: 0, min: 0, max: 0, has_min_max: true } }], coverage: { complete: true } }),
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
					items: [{ event_id: 'live-1', log_source_id: 'node:1', message: 'federation-live-tail', event_time_utc: '2026-09-23T00:00:00Z' }],
					coverage: { complete: true, targets: [], enumeration_state: 'OPEN' }, exhausted: false,
				})
			}),
		)
		const user = userEvent.setup()
		renderWithProviders(<LogsPage />)
		await user.click(await screen.findByRole('button', { name: '实时跟随' }))
		expect(await screen.findByText('federation-live-tail')).toBeInTheDocument()
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
})
