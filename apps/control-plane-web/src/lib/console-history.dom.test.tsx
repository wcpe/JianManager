import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { LogEntry } from '@/api/logs'
import { fetchLogCursorPage } from '@/api/logs'
import { findStartupSeq, historyRowsToLines, historySeqAt, mergeHistoryAndLive, useConsoleHistory } from './console-history'

vi.mock('@/api/logs', () => ({ fetchLogCursorPage: vi.fn() }))

const mockedFetch = vi.mocked(fetchLogCursorPage)

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
  mockedFetch.mockReset()
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
    mockedFetch.mockResolvedValueOnce({
      items: [
        row(3, '2026-08-27T00:00:03Z', 'newer'),
        row(2, '2026-08-27T00:00:02Z', 'older'),
      ],
      nextCursor: 'cursor-2',
      limit: 200,
    })
    const { result } = renderHook(() => useConsoleHistory({ instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' }))

    act(() => result.current.loadEarlier())
    await waitFor(() => expect(result.current.rowCount).toBe(2))

    expect(mockedFetch).toHaveBeenCalledWith({
      instanceId: 7,
      source: 'instance',
      to: '2026-08-27T00:00:04Z',
      cursor: undefined,
      limit: 200,
    })
    expect(result.current.lines.map((line) => line.raw)).toEqual(['older', 'newer'])
    expect(result.current.oldestTime).toBe('2026-08-27T00:00:02Z')
    expect(result.current.exhausted).toBe(false)
  })

  it('滚动抖动同时触发时只发一个请求，末页明确结束', async () => {
    let resolve!: (page: { items: LogEntry[]; nextCursor: string | null; limit: number }) => void
    mockedFetch.mockReturnValueOnce(new Promise((done) => { resolve = done }))
    const { result } = renderHook(() => useConsoleHistory({ instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' }))

    act(() => {
      result.current.loadEarlier()
      result.current.loadEarlier()
    })
    expect(mockedFetch).toHaveBeenCalledTimes(1)

    await act(async () => {
      resolve({ items: [row(1, '2026-08-27T00:00:01Z', 'oldest')], nextCursor: null, limit: 200 })
    })
    await waitFor(() => expect(result.current.exhausted).toBe(true))
    expect(result.current.exhausted).toBe(true)
    expect(result.current.lines.map((line) => line.raw)).toEqual(['oldest'])
  })

  it('跳到时间点会连续消费 nextCursor，直到目标时间被历史窗口覆盖', async () => {
    mockedFetch
      .mockResolvedValueOnce({
        items: [
          row(3, '2026-08-27T00:00:03Z', 'third'),
          row(2, '2026-08-27T00:00:02Z', 'second'),
        ],
        nextCursor: 'cursor-2',
        limit: 200,
      })
      .mockResolvedValueOnce({
        items: [row(1, '2026-08-27T00:00:01Z', 'first')],
        nextCursor: null,
        limit: 200,
      })
    const { result } = renderHook(() => useConsoleHistory({ instanceId: 7, anchorTime: '2026-08-27T00:00:04Z' }))

    let outcome: string | undefined
    await act(async () => {
      outcome = await result.current.loadUntil('2026-08-27T00:00:01Z')
    })

    expect(outcome).toBe('reached')
    expect(mockedFetch).toHaveBeenCalledTimes(2)
    expect(mockedFetch.mock.calls[1][0]).toMatchObject({ cursor: 'cursor-2' })
    expect(result.current.seqAtTime('2026-08-27T00:00:01Z')).toBe(-3)
  })
})
