import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import api from '@/api/client'
import type { LogEntry } from '@/api/logs'
import { findStartupSeq, historyRowsToLines, historySeqAt, mergeHistoryAndLive, useConsoleHistory } from './console-history'

/**
 * 2026-10-29：上溯数据源已换。CP `logs` 表的游标接口（以及封装它的 `fetchLogCursorPage`）
 * 已随表删除；`useConsoleHistory` 现在直接经 `api.get('/logs/federation')` 取页
 * （包装函数 `fetchHistoryCursorPage` 不导出）。
 *
 * 原来在本文件里 mock 的是已被删除的 `fetchLogCursorPage`——模块 mock 生效后该导出不存在，
 * 调用即抛错，用例全红。现在改用 `vi.spyOn(api, 'get')` 接管真实客户端（比模块 mock 更贴近）：
 * **断言的不变量一条不少**——串行翻页、轮询去重、接缝冻结、实例切换重置与入参形状。
 */
let mockedGet: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  mockedGet = vi.spyOn(api, 'get')
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** 联邦端点返回的扁平事件形状（测试只在乎 message/time/cursor）。 */
function fedPage(rows: LogEntry[], nextCursor: string | null) {
  return {
    data: {
      items: rows.map((r) => ({
        message: r.message,
        event_time_utc: r.time,
        level: r.level,
        stream: r.stream,
      })),
      // 生产响应是 snake_case（与后端信封体一致）。
      next_cursor: nextCursor,
    },
  }
}

/** 取第 n 次请求的 query 参数（与旧断言 `mock.calls[n][0]` 同形）。 */
function pageParams(call = 0): Record<string, unknown> {
  return (mockedGet.mock.calls[call]?.[1] as { params?: Record<string, unknown> } | undefined)?.params ?? {}
}

function row(id: number, time: string, message: string, stream = 'stdout'): LogEntry {
  return {
    id,
    source: 'instance',
    level: stream === 'stderr' ? 'error' : 'info',
    instanceId: 7,
    instanceUuid: 'instance-7',
    nodeId: 2,
    stream,
    message,
    time,
  }
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('FR-419 控制台游标回溯', () => {
  it('历史行按正序映射为负 seq，并和实时缓冲的正 seq 不冲突', () => {
    const lines = historyRowsToLines([
      row(1, '2026-08-27T00:00:01Z', '[00:00:01] [Server thread/INFO]: older'),
      row(2, '2026-08-27T00:00:02Z', '[00:00:02] [Server thread/INFO]: newer'),
    ])

    expect(lines.map((line) => line.seq)).toEqual([-2, -1])
    expect(lines.map((line) => line.body)).toEqual(['older', 'newer'])
    expect(historySeqAt(0, 2)).toBe(-2)
    expect(historySeqAt(1, 2)).toBe(-1)
  })

  it('识别最近一次 MC 启动完成标记，而不是猜测普通日志行', () => {
    const lines = historyRowsToLines([
      row(1, '2026-08-27T00:00:01Z', 'Done reading config'),
      row(2, '2026-08-27T00:00:02Z', '[00:00:02] [Server thread/INFO]: Done (12.4s)! For help'),
    ])

    expect(findStartupSeq(lines)).toBe(-1)
  })

  it('以连续尾部重叠去重数据库回溯与内存缓冲，避免溢出接缝重复日志', () => {
    const history = historyRowsToLines([
      row(1, '2026-08-27T00:00:01Z', 'before-gap'),
      row(2, '2026-08-27T00:00:02Z', 'boundary-a'),
      row(3, '2026-08-27T00:00:03Z', 'boundary-b'),
    ])
    const live = [
      { seq: 0, body: '[连接已建立]', raw: '[连接已建立]', kind: 'system' as const },
      { seq: 1, body: 'boundary-a', raw: 'boundary-a', kind: 'log' as const },
      { seq: 2, body: 'boundary-b', raw: 'boundary-b', kind: 'log' as const },
      { seq: 3, body: 'after-gap', raw: 'after-gap', kind: 'log' as const },
    ]

    expect(mergeHistoryAndLive(history, live).map((line) => line.raw)).toEqual([
      'before-gap',
      '[连接已建立]',
      'boundary-a',
      'boundary-b',
      'after-gap',
    ])
  })

  it('串行请求游标页、把倒序 DB 结果 prepend 为正序，且固定会话接缝', async () => {
    mockedGet.mockResolvedValueOnce(fedPage([
      row(3, '2026-08-27T00:00:03Z', 'newer'),
      row(2, '2026-08-27T00:00:02Z', 'older'),
    ], 'cursor-2'))
    const { result } = renderHook(() => useConsoleHistory({ instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' }))

    act(() => result.current.loadEarlier())
    await waitFor(() => expect(result.current.rowCount).toBe(2))

    expect(pageParams()).toMatchObject({
      instanceId: 7,
      to: '2026-08-27T00:00:04Z',
      limit: 200,
    })
    // 首屏没有游标：不得下发 cursor（否则会从中间开始）。
    expect(pageParams().cursor).toBeUndefined()
    expect(result.current.lines.map((line) => line.raw)).toEqual(['older', 'newer'])
    expect(result.current.oldestTime).toBe('2026-08-27T00:00:02Z')
    expect(result.current.exhausted).toBe(false)
  })

  it('滚动抖动同时触发时只发一个请求，末页明确结束', async () => {
    let resolve!: (page: { data: { items: unknown[]; next_cursor: string | null } }) => void
    mockedGet.mockReturnValueOnce(new Promise((done) => { resolve = done }) as never)
    const { result } = renderHook(() => useConsoleHistory({ instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' }))

    act(() => {
      result.current.loadEarlier()
      result.current.loadEarlier()
    })
    expect(mockedGet).toHaveBeenCalledTimes(1)

    await act(async () => {
      resolve(fedPage([row(1, '2026-08-27T00:00:01Z', 'oldest')], null))
    })
    await waitFor(() => expect(result.current.exhausted).toBe(true))
    expect(result.current.exhausted).toBe(true)
    expect(result.current.lines.map((line) => line.raw)).toEqual(['oldest'])
  })

  it('跳到时间点会连续消费 nextCursor，直到目标时间被历史窗口覆盖', async () => {
    mockedGet
      .mockResolvedValueOnce(fedPage([
        row(3, '2026-08-27T00:00:03Z', 'third'),
        row(2, '2026-08-27T00:00:02Z', 'second'),
      ], 'cursor-2'))
      .mockResolvedValueOnce(fedPage([row(1, '2026-08-27T00:00:01Z', 'first')], null))
    const { result } = renderHook(() => useConsoleHistory({ instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' }))

    let outcome: string | undefined
    await act(async () => {
      outcome = await result.current.loadUntil('2026-08-27T00:00:01Z')
    })

    expect(outcome).toBe('reached')
    expect(mockedGet).toHaveBeenCalledTimes(2)
    expect(pageParams(1)).toMatchObject({ cursor: 'cursor-2' })
    expect(result.current.seqAtTime('2026-08-27T00:00:01Z')).toBe(-3)
  })

  it('接缝冻结：同实例内 anchorTime 前移（模拟缓冲溢出）不作废已加载历史、不发新请求', async () => {
    mockedGet
      .mockResolvedValueOnce(fedPage([
        row(3, '2026-08-27T00:00:03Z', 'newer'),
        row(2, '2026-08-27T00:00:02Z', 'older'),
      ], 'cursor-2'))
      .mockResolvedValueOnce(fedPage([row(1, '2026-08-27T00:00:01Z', 'oldest')], null))
    const { result, rerender } = renderHook(
      (props: { instanceId: number; anchorTime: string }) => useConsoleHistory(props),
      { initialProps: { instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' } },
    )

    act(() => result.current.loadEarlier())
    await waitFor(() => expect(result.current.rowCount).toBe(2))

    // 模拟环形缓冲溢出：接缝前移。已加载的行必须原样保留，且此变化本身不触发任何请求。
    rerender({ instanceId: 7, anchorTime: '2026-08-27T00:00:05Z' })
    expect(result.current.rowCount).toBe(2)
    expect(mockedGet).toHaveBeenCalledTimes(1)

    // 继续翻更早一页：仍用**冻结的接缝**（首次挂载值）作上界，游标继续，行 prepend 不作废。
    act(() => result.current.loadEarlier())
    await waitFor(() => expect(result.current.rowCount).toBe(3))
    expect(mockedGet).toHaveBeenCalledTimes(2)
    expect(pageParams(1)).toMatchObject({
      instanceId: 7,
      to: '2026-08-27T00:00:04Z',
      cursor: 'cursor-2',
    })
    expect(result.current.lines.map((line) => line.raw)).toEqual(['oldest', 'older', 'newer'])
  })

  it('实例切换仍重置：已加载行清空，下一次取页用新实例与全新游标', async () => {
    mockedGet
      .mockResolvedValueOnce(fedPage([row(1, '2026-08-27T00:00:01Z', 'old-instance')], 'cursor-old'))
      .mockResolvedValueOnce(fedPage([row(9, '2026-08-27T00:01:01Z', 'new-instance')], null))
    const { result, rerender } = renderHook(
      (props: { instanceId: number; anchorTime: string }) => useConsoleHistory(props),
      { initialProps: { instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' } },
    )

    act(() => result.current.loadEarlier())
    await waitFor(() => expect(result.current.rowCount).toBe(1))

    rerender({ instanceId: 8, anchorTime: '2026-08-27T00:00:06Z' })
    expect(result.current.rowCount).toBe(0)

    act(() => result.current.loadEarlier())
    await waitFor(() => expect(result.current.rowCount).toBe(1))
    expect(mockedGet).toHaveBeenCalledTimes(2)
    expect(pageParams(1)).toMatchObject({
      instanceId: 8,
      to: '2026-08-27T00:00:06Z',
    })
    // 新实例必须从头开始：不得沿用上一条实例的游标。
    expect(pageParams(1).cursor).toBeUndefined()
    expect(result.current.lines.map((line) => line.raw)).toEqual(['new-instance'])
  })
})
