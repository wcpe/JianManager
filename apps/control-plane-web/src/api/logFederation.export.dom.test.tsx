/**
 * 联邦导出请求形态与阻断报告解析（FR-480 导出闸）。
 *
 * 缺陷背景：`exportFederatedLogs` 原实现删掉 `limit` 再发请求，而后端把缺失的
 * limit 当 0、0 又回退到服务端默认上限（10000 行）——于是「数据超 10000 行」
 * 必然被判超限，前端却没有任何途径调大，导出永久失败。
 *
 * 本组用例锁定两点：① 导出请求必须携带显式 limit；② 阻断报告的 reasons 与
 * limits（行/字节上限、实取行数）都要被解析出来，供 UI 给出可操作文案。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const postMock = vi.fn()
vi.mock('@/api/client', () => ({
  default: { post: (...args: unknown[]) => postMock(...args) },
}))

import {
  FEDERATED_EXPORT_REQUEST_LIMIT,
  FederatedExportIncompleteError,
  exportFederatedLogs,
  parseExportBlockedReport,
  parseExportBlockedReasons,
} from './logFederation'

/** 造一个「非 NDJSON 的阻断报告」响应，模拟导出闸返回 200 + JSON。 */
function blockedResponse(report: unknown) {
  return {
    headers: { 'content-type': 'application/json; charset=utf-8' },
    data: new Blob([JSON.stringify(report)]),
  }
}

describe('exportFederatedLogs 请求形态', () => {
  beforeEach(() => {
    postMock.mockReset()
    // jsdom 无 URL.createObjectURL；下载路径仅需不抛错。
    URL.createObjectURL = vi.fn(() => 'blob:mock')
    URL.revokeObjectURL = vi.fn()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('未指定 limit 时补上导出专用上限（不再删除该参数）', async () => {
    postMock.mockResolvedValue({
      headers: { 'content-type': 'application/x-ndjson' },
      data: new Blob(['{"a":1}']),
    })

    await exportFederatedLogs({ from: '2026-01-01T00:00:00Z', to: '2026-01-02T00:00:00Z' })

    expect(postMock).toHaveBeenCalledTimes(1)
    const [, , config] = postMock.mock.calls[0] as [string, unknown, { params: Record<string, unknown> }]
    expect(config.params.limit).toBe(FEDERATED_EXPORT_REQUEST_LIMIT)
    // 分页参数仍然剔除：导出是「不按页取」，page/pageSize/cursor 无意义。
    expect(config.params).not.toHaveProperty('page')
    expect(config.params).not.toHaveProperty('pageSize')
    expect(config.params).not.toHaveProperty('cursor')
  })

  it('调用方显式指定的 limit 被保留（不覆盖）', async () => {
    postMock.mockResolvedValue({
      headers: { 'content-type': 'application/x-ndjson' },
      data: new Blob(['{"a":1}']),
    })

    await exportFederatedLogs({ limit: 500 })

    const [, , config] = postMock.mock.calls[0] as [string, unknown, { params: Record<string, unknown> }]
    expect(config.params.limit).toBe(500)
  })

  it('非法 limit（0 / 负数 / 非数字）回退到导出专用上限', async () => {
    postMock.mockResolvedValue({
      headers: { 'content-type': 'application/x-ndjson' },
      data: new Blob(['{"a":1}']),
    })

    for (const bad of [0, -1, 'abc', null]) {
      postMock.mockClear()
      await exportFederatedLogs({ limit: bad })
      const [, , config] = postMock.mock.calls[0] as [string, unknown, { params: Record<string, unknown> }]
      expect(config.params.limit).toBe(FEDERATED_EXPORT_REQUEST_LIMIT)
    }
  })

  it('阻断报告 → 抛出结构化错误并携带 reasons 与 limits', async () => {
    postMock.mockResolvedValue(
      blockedResponse({
        export_incomplete: true,
        incomplete_reasons: ['budget_exceeded', 'row_limit_exceeded'],
        limits: { max_rows: 10000, max_bytes: 67108864, rows: 10200 },
        artifact: null,
      }),
    )

    const err = await exportFederatedLogs({}).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(FederatedExportIncompleteError)
    const typed = err as FederatedExportIncompleteError
    expect(typed.reasons).toEqual(['budget_exceeded', 'row_limit_exceeded'])
    expect(typed.limits).toEqual({ maxRows: 10000, maxBytes: 67108864, rows: 10200 })
  })
})

describe('parseExportBlockedReport', () => {
  it('解析 reasons 与 limits（snake_case wire 形态）', () => {
    const report = parseExportBlockedReport(
      JSON.stringify({
        incomplete_reasons: ['byte_budget_exceeded', '', 'quality_unresolved'],
        limits: { max_rows: 20000, max_bytes: 1073741824, rows: 19999 },
      }),
    )
    expect(report.reasons).toEqual(['byte_budget_exceeded', 'quality_unresolved'])
    expect(report.limits).toEqual({ maxRows: 20000, maxBytes: 1073741824, rows: 19999 })
  })

  it('缺失 / 非法 limits 不影响 reasons 解析', () => {
    expect(parseExportBlockedReport('{"incomplete_reasons":["row_limit_exceeded"]}')).toEqual({
      reasons: ['row_limit_exceeded'],
    })
    const bad = parseExportBlockedReport(
      JSON.stringify({ incomplete_reasons: ['row_limit_exceeded'], limits: 'nope' }),
    )
    expect(bad.reasons).toEqual(['row_limit_exceeded'])
    expect(bad.limits).toBeUndefined()
  })

  it('非数字 / 非正的 limits 字段被丢弃（不让 UI 显示 0 行或 NaN）', () => {
    const report = parseExportBlockedReport(
      JSON.stringify({ incomplete_reasons: ['row_limit_exceeded'], limits: { max_rows: 0, max_bytes: -1, rows: 'x' } }),
    )
    expect(report.limits).toEqual({ maxRows: undefined, maxBytes: undefined, rows: undefined })
  })

  it('非法输入不抛错：返回空 reasons（调用方走泛化失败文案）', () => {
    expect(parseExportBlockedReport('not-json')).toEqual({ reasons: [] })
    expect(parseExportBlockedReport('{}')).toEqual({ reasons: [] })
    expect(parseExportBlockedReasons('{"incomplete_reasons":"x"}')).toEqual([])
  })
})
