import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { parseAnsi } from '@/lib/console-ansi'
import api from '@/api/client'
import LogsPage from './LogsPage'

const summary = { coverage: { complete: true, targets: [], partial_reasons: [] }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' }, truncated: false }
const row = (index: number) => ({ event_id: `event-${index}`, canonical_content_hash: `hash-${index}`, log_source_id: 'inst:1/stdout', source_generation: 'g-1', ingest_position: String(9007199254740993n + BigInt(index)), message: `原始日志-${index}`, level: 'INFO', event_time_utc: new Date(Date.now() - index).toISOString(), fields: { detail: `附件原始字段-${index}` } })
const envelope = (items: unknown[], viewId = 'fixed-view', next?: string) => ({ sourceTag: 'federated', items, view: { view_id: viewId }, ...summary, exhausted: !next, ...(next ? { next_cursor: next } : {}) })
let blobs: Blob[] = []
const parameters = (request: Request) => Object.fromEntries(new URL(request.url).searchParams)

async function chooseExport(label: string) {
  const user = userEvent.setup()
  await waitFor(() => expect(screen.getByTestId('logs-export-trigger')).toBeEnabled())
  await user.click(screen.getByTestId('logs-export-trigger'))
  await user.click(await screen.findByRole('menuitem', { name: label }))
}
async function blobRows(index: number): Promise<unknown[]> {
  return (await blobs[index].text()).split('\n').filter(Boolean).map((line) => JSON.parse(line) as unknown)
}

beforeEach(() => {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
  HTMLElement.prototype.hasPointerCapture ??= () => false
  HTMLElement.prototype.setPointerCapture ??= () => {}
  HTMLElement.prototype.releasePointerCapture ??= () => {}
  HTMLElement.prototype.scrollIntoView ??= () => {}
  blobs = []
  URL.createObjectURL = vi.fn((blob: Blob) => { blobs.push(blob); return 'blob:six-fix' })
  URL.revokeObjectURL = vi.fn()
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([])),
    http.get(API('/instances'), () => HttpResponse.json([])),
    http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [{ agg: { count: 7 } }], ...summary })),
    http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [{ dimension: 'level', values: [{ value: 'INFO', count: 7 }] }], ...summary })),
  )
})
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

describe('日志六模块正式行为回归', () => {
  it('101+事件跨页：当前页冻结完整raw页，全部匹配枚举全窗，UTC时间段换scope不夹旧view', async () => {
    const rows = Array.from({ length: 161 }, (_, index) => row(index))
    const exports: Record<string, string>[] = []
    server.use(
      http.get(API('/logs/federation'), ({ request }) => {
        const query = parameters(request)
        return HttpResponse.json(envelope(query.cursor ? rows.slice(100) : rows.slice(0, 100), 'fixed-view', query.cursor ? undefined : 'opaque-page-2'))
      }),
      http.post(API('/logs/federation/export'), ({ request }) => {
        const query = parameters(request)
        exports.push(query)
        const result = query.range === 'page' ? query.cursor ? rows.slice(100) : rows.slice(0, 100) : query.viewId ? rows : rows.slice(40, 42)
        return new HttpResponse(result.map((item) => JSON.stringify(item)).join('\n'), { headers: { 'Content-Type': 'application/x-ndjson' } })
      }),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('原始日志-0')
    expect(screen.getAllByTestId('log-row').length).toBeLessThanOrEqual(24)
    await chooseExport('导出当前页')
    await waitFor(() => expect(blobs).toHaveLength(1))
    expect(await blobRows(0)).toEqual(rows.slice(0, 100))
    expect(exports[0]).toMatchObject({ range: 'page', viewId: 'fixed-view', limit: '100' })
    expect(exports[0].cursor).toBeUndefined()
    await userEvent.setup().click(screen.getByRole('button', { name: '下一页' }))
    await screen.findByText('原始日志-100')
    await chooseExport('导出当前页')
    await waitFor(() => expect(blobs).toHaveLength(2))
    expect(await blobRows(1)).toEqual(rows.slice(100))
    expect(exports[1]).toMatchObject({ range: 'page', cursor: 'opaque-page-2', limit: '100' })
    await chooseExport('导出全部匹配')
    await waitFor(() => expect(blobs).toHaveLength(3))
    expect(await blobRows(2)).toEqual(rows)
    expect(exports[2]).toMatchObject({ range: 'all', limit: '1000000', viewId: 'fixed-view' })
    expect(exports[2].cursor).toBeUndefined()
    await chooseExport('导出所选时间段')
    expect(exports).toHaveLength(3)
    fireEvent.change(screen.getByLabelText('开始时间（UTC）'), { target: { value: '2026-10-21T10:00:00' } })
    fireEvent.change(screen.getByLabelText('结束时间（UTC）'), { target: { value: '2026-10-21T10:01:00' } })
    await userEvent.setup().click(screen.getByRole('button', { name: '导出该时间段' }))
    await waitFor(() => expect(blobs).toHaveLength(4))
    expect(await blobRows(3)).toEqual(rows.slice(40, 42))
    expect(exports[3]).toMatchObject({ range: 'all', from: '2026-10-21T10:00:00.000Z', to: '2026-10-21T10:01:00.000Z' })
    expect(exports[3].viewId).toBeUndefined()
    expect(exports[3].cursor).toBeUndefined()
  })

  it('onlineOnly、trim关键字、实际窗口和目标在Search/Stats/Facets/Export严格同scope', async () => {
    const searches: Record<string, string>[] = []
    const stats: Record<string, string>[] = []
    const facets: Record<string, string>[] = []
    const exports: Record<string, string>[] = []
    const raw = row(1)
    server.use(
      http.get(API('/logs/federation'), ({ request }) => {
        const query = parameters(request); searches.push(query)
        return HttpResponse.json(query.onlineOnly === 'true' ? envelope([raw], 'online-view') : { ...envelope([raw]), coverage: { complete: false, targets: [], partial_reasons: ['OFFLINE'] } })
      }),
      http.get(API('/logs/federation/stats'), ({ request }) => { stats.push(parameters(request)); return HttpResponse.json({ points: [], ...summary }) }),
      http.get(API('/logs/federation/facets'), ({ request }) => { facets.push(parameters(request)); return HttpResponse.json({ dimensions: [], ...summary }) }),
      http.post(API('/logs/federation/export'), ({ request }) => { exports.push(parameters(request)); return new HttpResponse(JSON.stringify(raw), { headers: { 'Content-Type': 'application/x-ndjson' } }) }),
    )
    renderWithProviders(<LogsPage />, { route: '/logs?instanceId=1' })
    await userEvent.setup().click(await screen.findByTestId('logs-coverage-action-logsFederation.action.requestOnline'))
    fireEvent.change(screen.getByPlaceholderText('搜索日志内容…'), { target: { value: '  原始  ' } })
    await waitFor(() => expect(searches.at(-1)?.keyword).toBe('原始'))
    await waitFor(() => expect(facets.at(-1)?.keyword).toBe('原始'))
    await chooseExport('导出当前页')
    await waitFor(() => expect(blobs).toHaveLength(1))
    const effective = searches.at(-1)!
    for (const query of [stats.at(-1)!, facets.at(-1)!, exports[0]]) {
      for (const key of ['view', 'onlineOnly', 'instanceId', 'keyword', 'from', 'to']) expect(query[key], key).toBe(effective[key])
      expect(query.viewId).toBe('online-view')
    }
  })

  it('移到更早完整窗口后聚合与导出使用新窗口，不拼首窗和新view', async () => {
    const searches: Record<string, string>[] = []
    const exports: Record<string, string>[] = []
    const raw = row(1)
    server.use(
      http.get(API('/logs/federation'), ({ request }) => { const query = parameters(request); searches.push(query); return HttpResponse.json(envelope([{ ...raw, message: `时间窗-${query.page}` }], `window-${query.page}`)) }),
      http.post(API('/logs/federation/export'), ({ request }) => { exports.push(parameters(request)); return new HttpResponse('{}\n', { headers: { 'Content-Type': 'application/x-ndjson' } }) }),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('时间窗-1')
    expect(searches).toHaveLength(1)
    await userEvent.setup().click(screen.getByRole('button', { name: '下一页' }))
    await screen.findByText('时间窗-2')
    await chooseExport('导出全部匹配')
    await waitFor(() => expect(exports).toHaveLength(1))
    expect(exports[0]).toMatchObject({ viewId: 'window-2', from: searches[1].from, to: searches[1].to })
    expect(exports[0].from).not.toBe(searches[0].from)
  })

  it('新条件加载期间可保留旧行占位，但不得导出旧view冒充新scope', async () => {
    let resolve!: () => void
    server.use(http.get(API('/logs/federation'), async ({ request }) => {
      if (parameters(request).level) await new Promise<void>((done) => { resolve = done })
      return HttpResponse.json(envelope([row(1)]))
    }))
    const view = renderWithProviders(<LogsPage />)
    await screen.findByText('原始日志-1')
    await userEvent.setup().click(screen.getByRole('button', { name: '错误', exact: true }))
    await waitFor(() => expect(resolve).toBeTypeOf('function'))
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
    view.unmount(); resolve()
    expect(blobs).toHaveLength(0)
  })

  it.each([403, 409, 500])('当前页下载即时鉴权/校验被%d拒绝时不产生本地附件', async (status) => {
    let exports = 0
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json({ ...envelope([row(1)]), coverage: { ...summary.coverage, complete: false, pending: true } })),
      http.post(API('/logs/federation/export'), () => { exports++; return HttpResponse.json({ error: status === 403 ? 'PERMISSION_REVOKED' : 'VIEW_STALE' }, { status }) }),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('原始日志-1')
    await chooseExport('导出当前页')
    await waitFor(() => expect(exports).toBe(1))
    await waitFor(() => expect(screen.getByTestId('logs-export-trigger')).toBeEnabled())
    expect(blobs).toHaveLength(0)
  })

  it('当前页附件身份或原始字段变化则拒绝下载，不用新页替换冻结页', async () => {
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([row(1)]))),
      http.post(API('/logs/federation/export'), () => new HttpResponse(JSON.stringify(row(2)), { headers: { 'Content-Type': 'application/x-ndjson' } })),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('原始日志-1')
    await chooseExport('导出当前页')
    await waitFor(() => expect(screen.getByTestId('logs-export-trigger')).toBeEnabled())
    expect(blobs).toHaveLength(0)
  })

  it.each([
    { name: '精确', stats: { ...summary }, facets: { ...summary }, exact: true, error: false },
    { name: '部分覆盖', stats: { ...summary, coverage: { complete: false, partial_reasons: ['BUDGET_EXCEEDED'] } }, facets: { ...summary, coverage: { complete: false } }, exact: false, error: false },
    { name: '未知质量', stats: { coverage: { complete: true } }, facets: { coverage: { complete: true } }, exact: false, error: false },
    { name: '未校验覆盖', stats: { ...summary, coverage: { complete: false, pending: true } }, facets: { ...summary, coverage: { complete: false, pending: true } }, exact: false, error: false },
    { name: '统计截断', stats: { ...summary, truncated: true }, facets: { ...summary }, exact: false, error: false },
    { name: '未对账质量', stats: { ...summary, quality: { duplicate_quality: 'unresolved', stats_quality: 'approximate' } }, facets: { ...summary, truncated: true }, exact: false, error: false },
    { name: '失败', stats: {}, facets: {}, exact: false, error: true },
  ])('独立聚合矩阵：$name不依赖主列表complete背书', async ({ stats, facets, exact, error }) => {
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([row(1)]))),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [{ agg: { count: 7 } }], ...stats }, { status: error ? 500 : 200 })),
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [{ dimension: 'level', truncated: true, values: [{ value: 'INFO', count: 7 }] }], ...facets }, { status: error ? 500 : 200 })),
    )
    renderWithProviders(<LogsPage />)
    const insight = await screen.findByTestId('logs-federation-insights')
    await waitFor(() => expect(screen.getByTestId('logs-facets-summary')).not.toHaveTextContent('尚未获取'))
    if (exact) expect(insight).toHaveTextContent('当前视图 7 个事件')
    else expect(insight).not.toHaveTextContent('当前视图 7 个事件')
    if (error) { expect(insight).toHaveTextContent('数量未知'); expect(insight).not.toHaveTextContent('0 个事件') }
    else expect(insight).toHaveTextContent('结果已截断')
    expect(insight).not.toHaveTextContent('至少')
  })

  it.each(['stats', 'facets'])('%s单独失败时不得抹去另一聚合自己的成功与精确性', async (failed) => {
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([row(1)]))),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [{ agg: { count: 7 } }], ...summary }, { status: failed === 'stats' ? 500 : 200 })),
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [{ dimension: 'level', values: [{ value: 'INFO', count: 7 }] }], ...summary }, { status: failed === 'facets' ? 500 : 200 })),
    )
    renderWithProviders(<LogsPage />)
    await waitFor(() => expect(screen.getByTestId(failed === 'stats' ? 'logs-stats-summary' : 'logs-facets-summary')).toHaveTextContent('查询失败，数量未知'))
    if (failed === 'stats') expect(await screen.findByText('INFO 7')).toBeInTheDocument()
    else expect(await screen.findByText('当前视图 7 个事件')).toBeInTheDocument()
  })

  it('成功空聚合与聚合失败分开；空值仅在自身coverage和quality精确时标零', async () => {
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([row(1)]))),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [], ...summary })),
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], ...summary })),
    )
    renderWithProviders(<LogsPage />)
    expect(await screen.findByText('当前视图 0 个事件')).toBeInTheDocument()
  })

  it('范围Dialog取消零POST，非法UTC范围不能确认', async () => {
    let exports = 0
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([row(1)]))),
      http.post(API('/logs/federation/export'), () => { exports++; return new HttpResponse('') }),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('原始日志-1')
    await chooseExport('导出所选时间段')
    fireEvent.change(screen.getByLabelText('开始时间（UTC）'), { target: { value: '2027-01-01T00:00' } })
    expect(screen.getByRole('button', { name: '导出该时间段' })).toBeDisabled()
    await userEvent.setup().click(screen.getByRole('button', { name: '取消' }))
    expect(exports).toBe(0)
  })

  it.each(['unchanged', 'changed', 'revoked'])('经典当前页%s：真实同页全量、即时鉴权且变化不下载旧行', async (mode) => {
    const rows = Array.from({ length: 101 }, (_, index) => ({ id: index + 1, source: 'control_plane', level: 'info', instanceId: 0, instanceUuid: '', nodeId: 0, message: `平台原始-${index}`, time: new Date(Date.now() - index).toISOString() }))
    let calls = 0
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([]))),
      http.get(API('/logs'), () => {
        calls++
        if (calls > 1 && mode === 'revoked') return HttpResponse.json({ error: 'PERMISSION_REVOKED' }, { status: 403 })
        return HttpResponse.json({ items: calls > 1 && mode === 'changed' ? rows.slice(1, 101) : rows.slice(0, 100), total: 101, page: 1, pageSize: 100 })
      }),
    )
    renderWithProviders(<LogsPage />)
    await userEvent.setup().click(screen.getByRole('tab', { name: '平台日志' }))
    await screen.findByText('平台原始-0')
    await chooseExport('导出当前页')
    await waitFor(() => expect(calls).toBe(2))
    await waitFor(() => expect(screen.getByTestId('logs-export-trigger')).toBeEnabled())
    if (mode === 'unchanged') expect(await blobRows(0)).toEqual(rows.slice(0, 100))
    else expect(blobs).toHaveLength(0)
  })

  it('导出可取消且卸载也取消：迟到附件不得触发下载', async () => {
    const raw = row(1)
    const release: (() => void)[] = []
    const post = vi.spyOn(api, 'post')
    server.use(
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope([raw]))),
      http.post(API('/logs/federation/export'), async () => {
        await new Promise<void>((resolve) => release.push(resolve))
        return new HttpResponse(JSON.stringify(raw), { headers: { 'Content-Type': 'application/x-ndjson' } })
      }),
    )
    const view = renderWithProviders(<LogsPage />)
    await screen.findByText('原始日志-1')
    await chooseExport('导出当前页')
    await waitFor(() => expect(release).toHaveLength(1))
    await userEvent.setup().click(screen.getByRole('button', { name: '取消' }))
    expect(post.mock.calls[0][2]?.signal?.aborted).toBe(true)
    release[0]()
    await chooseExport('导出当前页')
    await waitFor(() => expect(release).toHaveLength(2))
    view.unmount()
    expect(post.mock.calls[1][2]?.signal?.aborted).toBe(true)
    release[1]()
    expect(blobs).toHaveLength(0)
  })

  it('XSS/ANSI只作纯文本，虚拟列表及60实例不逐实例查询反证保留', async () => {
    let instanceCalls = 0
    const payload = '<img src=x onerror="alert(1)"><svg onload="alert(2)">'
    server.use(
      http.get(API('/instances'), () => { instanceCalls++; return HttpResponse.json(Array.from({ length: 60 }, (_, index) => ({ id: index + 1, name: `实例-${index + 1}` }))) }),
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope(Array.from({ length: 100 }, (_, index) => ({ ...row(index), ...(index === 0 ? { message: payload } : {}) }))))),
    )
    const { container } = renderWithProviders(<LogsPage />)
    await screen.findByText(payload)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('svg[onload]')).toBeNull()
    expect(parseAnsi('\u001b]8;;javascript:alert(1)\u0007链接文本\u001b]8;;\u0007')).toEqual([{ text: '链接文本' }])
    expect(screen.getAllByTestId('log-row').length).toBeLessThanOrEqual(24)
    await userEvent.setup().click(screen.getAllByRole('combobox').at(-1)!)
    expect(await screen.findByRole('option', { name: '实例-60' })).toBeInTheDocument()
    expect(screen.getAllByRole('option')).toHaveLength(61)
    expect(instanceCalls).toBe(1)
  })
})
