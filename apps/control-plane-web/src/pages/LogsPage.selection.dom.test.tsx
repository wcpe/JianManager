/**
 * 日志列表的多选与复制、来源名称反查、帮助浮层。
 *
 * 这几块新交互都有“看着能用、实际不生效”的失败模式：
 *   - 选中态存在行内 state → 滚出视口再回来就丢；
 *   - 复制只拼 message → 粘出去对不上是哪一行；
 *   - 名称拉不到时退回泛称 → 用户以为是“某个未指明实例”；
 *   - 帮助只说了来源没说视图 → 看不懂三个页签的区别。
 * 所以断言以**可粘贴的文本**、**data-\* 事实值**、**浮层里真存在的小节**为准，
 * 而不依赖仅在已加载翻译资源时才有意义的文案。
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { parseLogSource, resolveSourceLabel } from '@/lib/logs/logs-source-label'
import type { LogEntry } from '@/lib/console/console-log-types'
import LogsPage from './LogsPage'

const log = (overrides: Partial<LogEntry> = {}): LogEntry => ({
  id: 1,
  source: 'instance',
  level: 'info',
  instanceId: 2,
  instanceUuid: 'i-2',
  nodeId: 1,
  message: 'hello',
  time: '2026-11-10T08:00:00Z',
  ...overrides,
})

const summary = {
  coverage: { complete: true, targets: [], partial_reasons: [] },
  quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
  truncated: false,
}

const envelope = (items: unknown[], viewId = 'view-1') => ({
  sourceTag: 'federated', items, view: { view_id: viewId }, ...summary, exhausted: true,
})
const event = (index: number, sourceId = 'inst:2/file') => ({
  id: index + 1,
  log_source_id: sourceId,
  level: index % 2 === 0 ? 'INFO' : 'WARN',
  message: `日志行-${index}`,
  event_time_utc: new Date(Date.UTC(2026, 10, 10, 8, 0, index)).toISOString(),
})

describe('logs-source-label 纯函数', () => {
  it('解析 inst:/node: 前缀与流名', () => {
    expect(parseLogSource('inst:2/file')).toEqual({ kind: 'instance', id: 2, stream: 'file' })
    expect(parseLogSource('node:1/worker')).toEqual({ kind: 'worker', id: 1, stream: 'worker' })
    expect(parseLogSource('control_plane:x')).toEqual({ kind: 'unknown', id: null })
    expect(parseLogSource(undefined)).toEqual({ kind: 'unknown', id: null })
  })

  it('名称优先，拉不到时退回带 id 的写法而不是泛称', () => {
    const lookup = { instances: [{ id: 2, name: 'logtest-mc' }], nodes: [{ id: 1, name: 'dev-node' }] }
    expect(resolveSourceLabel(log({ sourceRef: 'inst:2/file' }), lookup).name).toBe('logtest-mc')
    expect(resolveSourceLabel(log({ sourceRef: 'node:1/file', source: 'worker' }), lookup).name).toBe('dev-node')
    // 名称缺失：id 仍已知，应交回调用方拼 `实例 #2`。
    const missing = resolveSourceLabel(log({ sourceRef: 'inst:99/file' }), lookup)
    expect(missing.name).toBeNull()
    expect(missing.id).toBe(99)
  })
})

describe('LogsPage 多选复制与帮助', () => {
  beforeEach(() => {
    const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
    loginMockUser(`mock.${payload}.sig`)
    HTMLElement.prototype.scrollIntoView ??= () => {}
    server.use(
      http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'dev-node', status: 1 }])),
      http.get(API('/instances'), () => HttpResponse.json([{ id: 2, name: 'logtest-mc', nodeId: 1 }])),
      http.get(API('/logs/federation'), () => HttpResponse.json(envelope(Array.from({ length: 20 }, (_, i) => event(i))))),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [{ agg: { count: 20 } }], ...summary })),
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({
        dimensions: [{ dimension: 'level', values: [{ value: 'INFO', count: 10 }, { value: 'WARN', count: 10 }] }], ...summary,
      })),
      http.get(API('/logs/federation/streams'), () => HttpResponse.json({
        streams: [
          { log_source_id: 'inst:2/file', stream: 'file', hits: 15 },
          { log_source_id: 'node:1/worker', stream: 'worker', hits: 5 },
        ],
        complete: true, ...summary,
      })),
    )
  })

  it('洞察栏在表格之外：卡片不包含日志行，日志行也不包含卡片', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('日志行-0')
    const insights = await screen.findByTestId('logs-federation-insights')
    // 这是关键结构调整：统计不再挤在表格工具栏里。
    expect(within(insights).queryAllByTestId('log-row')).toHaveLength(0)
    expect(screen.getByTestId('logs-virtual')).not.toContainElement(insights)
    // 四块卡片都在（来源卡片依赖 streams，已桩）。
    expect(await screen.findByTestId('logs-events-total')).toBeInTheDocument()
    expect(await screen.findByTestId('logs-level-card')).toBeInTheDocument()
    expect(await screen.findByTestId('logs-source-card')).toBeInTheDocument()
    expect(screen.getByTestId('logs-time-card')).toBeInTheDocument()
  })

  it('keeps all four insight cells while source data is pending', async () => {
    let release: (() => void) | undefined
    server.use(http.get(API('/logs/federation/streams'), async ({ request }) => {
      await new Promise<void>((resolve) => {
        release = resolve
        request.signal.addEventListener('abort', () => resolve(), { once: true })
      })
      return HttpResponse.json({ streams: [{ log_source_id: 'inst:2/file', stream: 'file', hits: 20 }], complete: true, ...summary })
    }))
    renderWithProviders(<LogsPage />)
    try {
      await waitFor(() => expect(release).toBeDefined())
      const insights = screen.getByTestId('logs-federation-insights')
      const cellIds = ['total', 'level', 'sources', 'time'].map((name) => `logs-insight-cell-${name}`)
      expect(Array.from(insights.children).map((node) => node.getAttribute('data-testid'))).toEqual(cellIds)
      expect(screen.getByTestId('logs-insight-cell-sources')).toBeEmptyDOMElement()
      const timeCell = screen.getByTestId('logs-insight-cell-time')
      release?.()
      expect(await screen.findByTestId('logs-source-card')).toBeInTheDocument()
      expect(screen.getByTestId('logs-insight-cell-time')).toBe(timeCell)
      expect(Array.from(insights.children).map((node) => node.getAttribute('data-testid'))).toEqual(cellIds)
    } finally { release?.() }
  })

  it('洞察卡片不再出现 EXACT/PARTIAL 这类后端枚举当正文', async () => {
    renderWithProviders(<LogsPage />)
    const insights = await screen.findByTestId('logs-federation-insights')
    await screen.findByTestId('logs-events-total')
    expect(insights.textContent ?? '').not.toMatch(/EXACT|PARTIAL/)
    // 质量标记只在非精确时作为 tooltip 细节出现，不进正文。
  })

  it('来源列与来源卡片都显示具体实例/节点名称', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('日志行-0')
    // 日志行里显示实例名，而不是「实例」两个字。
    await waitFor(() => expect(screen.getAllByText('logtest-mc').length).toBeGreaterThan(0))
    // 来源卡片同样按名称显示。
    const bars = await screen.findAllByTestId('logs-source-bar')
    const names = bars.map((bar) => bar.getAttribute('data-source-name'))
    expect(names).toContain('logtest-mc')
    expect(names).toContain('dev-node')
  })

  it('日志正文保留浏览器原生选区，不再渲染自定义多选条或接管复制快捷键', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('日志行-0')

    const messages = screen.getAllByTestId('log-message')
    expect(messages).toHaveLength(20)
    expect(screen.queryByTestId('logs-selection-bar')).not.toBeInTheDocument()
    expect(screen.queryByTestId('logs-copy-selected')).not.toBeInTheDocument()
    expect(screen.queryByTestId('log-row-toggle')).not.toBeInTheDocument()
    expect(screen.getByTestId('logs-virtual')).toHaveAttribute('data-rendered-count', '20')

    // jsdom 无法模拟真实鼠标拖选，但 DOM 必须允许浏览器对正文节点建立原生 Range。
    const selection = window.getSelection()
    const range = document.createRange()
    range.selectNodeContents(messages[0]!)
    selection?.removeAllRanges()
    selection?.addRange(range)
    expect(selection?.toString()).toContain('日志行-0')
  })

  it('帮助浮层补齐“三种视图的区别”', async () => {
    renderWithProviders(<LogsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByTestId('logs-help-trigger'))
    const content = await screen.findByTestId('logs-help-content')
    // 第一个小节就是视图区别（用户最常问的）。
    const first = within(content).getByTestId('logs-help-section-0')
    expect(first).toBeInTheDocument()
    // 三条视图说明各自成条：节点/实例、平台、全部。
    expect(first.querySelectorAll('li')).toHaveLength(3)
    // 来源仍是小节（现在排第二）。
    expect(within(content).getByTestId('logs-help-section-1')).toBeInTheDocument()
  })
})
