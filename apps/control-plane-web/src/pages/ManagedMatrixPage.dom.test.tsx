import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ManagedMatrixPage from './ManagedMatrixPage'
import {
  formatCacheAge,
  formatDesiredVersion,
  formatObservedVersion,
  isDriftFilter,
  staleBridgeNote,
  type ManagedMatrixRow,
} from '@/api/managedMatrix'

/**
 * 探针托管对账矩阵 DOM 测试。
 *
 * 盯住四处「写错了会让运维得出相反结论」的地方：
 * 1. **`null` 与 `0` 必须分开显示**——`desiredVersion=null` 是「从未下发」，`0` 是「下发过第 0 版」；
 *    `cacheAgeSeconds=null` 是「从未生效过」，`0` 是「刚刚生效」。混成 0 会让排查反向。
 * 2. **连接维度以桥为准**：桥断开时探针自报的 `connected` 已经过期，界面必须显示断开并给出陈旧提示，
 *    否则运维会以为机器还在线。
 * 3. **交叉矩阵**按「配置来源 × 连接状态」统计，格子计数必须与行数据一致。
 * 4. **漂移筛选**只筛出不一致的机器，不能把 inSync 也算进去。
 */

/** 构造一行矩阵数据。 */
function rowOf(overrides: Partial<ManagedMatrixRow> = {}): ManagedMatrixRow {
  return {
    instanceId: 7,
    instanceUuid: 'uuid-7',
    instanceName: 'survival1',
    nodeId: 1,
    nodeName: 'node-01',
    desiredVersion: 3,
    observedVersion: 3,
    mode: 'managed',
    connection: 'connected',
    reportedConnection: 'connected',
    cacheAgeSeconds: 12,
    consistency: 'inSync',
    ...overrides,
  }
}

/** 挂载矩阵相关端点。 */
function useMatrixEndpoints(options: {
  rows?: ManagedMatrixRow[]
  summary?: Record<string, unknown>
  detail?: Record<string, unknown>
} = {}) {
  const rows = options.rows ?? [rowOf()]
  server.use(
    http.get(API('/managed-matrix'), () =>
      HttpResponse.json({ items: rows, total: rows.length, page: 1, pageSize: 20 }),
    ),
    http.get(API('/managed-matrix/summary'), () =>
      HttpResponse.json({
        total: rows.length,
        byConsistency: { inSync: 1, drift: 1, neverReported: 0, failed: 0 },
        byConnection: { connected: 1, degraded: 0, unauthorized: 0, disconnected: 1 },
        byConfiguration: { cp: 1, file: 1 },
        crossMatrix: [
          { configuration: 'cp', connection: 'connected', count: 1 },
          { configuration: 'file', connection: 'disconnected', count: 1 },
        ],
        ...options.summary,
      }),
    ),
    http.get(API('/managed-matrix/instances/:id'), () =>
      HttpResponse.json({
        instanceId: 7,
        instanceName: 'survival1',
        desiredVersion: 3,
        observedVersion: 3,
        consistency: 'inSync',
        layers: [],
        effective: {},
        traced: [],
        ...options.detail,
      }),
    ),
  )
}

describe('对账矩阵：null / 0 的语义必须分开', () => {
  it('从未下发显示「未下发」而不是 0', async () => {
    loginMockUser()
    useMatrixEndpoints({ rows: [rowOf({ desiredVersion: null })] })

    renderWithProviders(<ManagedMatrixPage />)
    const cell = await screen.findByTestId('managed-desired')
    // 这是本页最容易写错、也最会误导排查的一处。
    expect(cell.textContent).not.toContain('0')
    await waitFor(() => expect(cell.textContent?.trim().length ?? 0).toBeGreaterThan(0))
  })

  it('从未上报显示「未上报」而不是 0', async () => {
    loginMockUser()
    useMatrixEndpoints({ rows: [rowOf({ observedVersion: null })] })

    renderWithProviders(<ManagedMatrixPage />)
    const cell = await screen.findByTestId('managed-observed')
    await waitFor(() => expect(cell.textContent?.trim().length ?? 0).toBeGreaterThan(0))
    expect(cell.textContent).not.toContain('0')
  })

  it('cacheAgeSeconds 为 null 时留空占位，不是「0 秒」', async () => {
    loginMockUser()
    useMatrixEndpoints({ rows: [rowOf({ cacheAgeSeconds: null })] })

    renderWithProviders(<ManagedMatrixPage />)
    const cell = await screen.findByTestId('managed-cache-age')
    await waitFor(() => expect(cell).toBeInTheDocument())
    // null = 从未生效过；显示 0 会被读成「刚刚生效」，语义完全相反。
    expect(cell.textContent).not.toMatch(/^0/)
  })
})

describe('对账矩阵：连接维度以桥为准', () => {
  it('桥断开但探针自报 connected 时显式提示陈旧', async () => {
    loginMockUser()
    useMatrixEndpoints({
      rows: [
        rowOf({
          connection: 'disconnected',
          reportedConnection: 'connected',
          reportedAt: new Date(Date.now() - 30 * 60 * 1000).toISOString(),
        }),
      ],
    })

    renderWithProviders(<ManagedMatrixPage />)
    const note = await screen.findByTestId('managed-stale-note')
    // 只显示「断开」而不解释为什么与自报不一致，运维无法判断是刚掉线还是掉了半小时。
    expect(note.textContent?.trim().length ?? 0).toBeGreaterThan(0)
  })

  it('桥在线时不应出现陈旧提示', async () => {
    loginMockUser()
    useMatrixEndpoints({ rows: [rowOf({ connection: 'connected', reportedConnection: 'connected' })] })

    renderWithProviders(<ManagedMatrixPage />)
    await screen.findByTestId('managed-matrix-row')
    expect(screen.queryByTestId('managed-stale-note')).not.toBeInTheDocument()
  })
})

describe('对账矩阵：筛选与交叉矩阵', () => {
  it('勾选「只看漂移」后按 consistency 重新查询', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const seen: string[] = []
    server.use(
      http.get(API('/managed-matrix'), ({ request }) => {
        seen.push(new URL(request.url).search)
        return HttpResponse.json({ items: [rowOf()], total: 1, page: 1, pageSize: 20 })
      }),
      http.get(API('/managed-matrix/summary'), () =>
        HttpResponse.json({
          total: 1,
          byConsistency: { drift: 1 },
          byConnection: {},
          byConfiguration: {},
          crossMatrix: [],
        }),
      ),
    )

    renderWithProviders(<ManagedMatrixPage />)
    await screen.findByTestId('managed-matrix-row')
    await user.click(screen.getByTestId('managed-matrix-only-drift'))

    // 筛选必须下推到服务端：列表要分页，前端过滤只会筛当前页。
    await waitFor(() => expect(seen.some((q) => q.includes('drift'))).toBe(true))
  })

  it('交叉矩阵格子计数取自 summary.crossMatrix', async () => {
    loginMockUser()
    useMatrixEndpoints()

    renderWithProviders(<ManagedMatrixPage />)
    const cell = await screen.findByTestId('managed-matrix-cell-cp-connected')
    // 计数为 0 表示 summary 没取到或字段名不匹配 —— 等它变成真实计数再断言。
    await waitFor(() => expect(cell.textContent).toContain('1'))
  })
})

describe('对账矩阵：纯函数映射', () => {
  it('formatDesiredVersion：null 与 0 必须给出不同结果', () => {
    // 这两者语义不同，映射成同一个字符串会让排查反向。
    expect(formatDesiredVersion(null, '未下发')).toBe('未下发')
    expect(formatDesiredVersion(0, '未下发')).not.toBe('未下发')
  })

  it('formatObservedVersion：null 与 0 必须区分', () => {
    expect(formatObservedVersion(null, '未上报')).toBe('未上报')
    expect(formatObservedVersion(0, '未上报')).not.toBe('未上报')
  })

  it('formatCacheAge：null 与 0 必须区分', () => {
    // null=从未生效过；0=刚刚生效。混同会把「没生效过」读成「刚成功」。
    expect(formatCacheAge(null, '—')).toBe('—')
    expect(formatCacheAge(0, '—')).not.toBe('—')
  })

  it('isDriftFilter 只认漂移档', () => {
    expect(isDriftFilter('drift')).toBe(true)
    expect(isDriftFilter('inSync')).toBe(false)
    expect(isDriftFilter('')).toBe(false)
  })

  it('staleBridgeNote：桥断开且有过上报时间时必须给出提示', () => {
    // 签名是 (row, relativeText)：只认 row 上的 connection/reportedAt，相对时间由调用方格式化。
    expect(staleBridgeNote({ connection: 'disconnected', reportedAt: '2026-11-16T10:00:00Z' }, '3 分钟前')).toBe(
      '3 分钟前',
    )
    // 桥仍在线时不该冒出「陈旧」提示。
    expect(staleBridgeNote({ connection: 'connected', reportedAt: '2026-11-16T10:00:00Z' }, '3 分钟前')).toBeNull()
    // 从未上报过就没有「最后上报于」可讲，也不该编一个。
    expect(staleBridgeNote({ connection: 'disconnected', reportedAt: null }, '3 分钟前')).toBeNull()
  })
})
