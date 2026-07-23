import { describe, it, expect } from 'vitest'
import {
  readBotFilter,
  writeBotFilter,
  readFailureFilter,
  writeFailureFilter,
  FAILURE_CATEGORIES,
} from './filters'

describe('bot-load filters', () => {
  it('读写 Bot 筛选', () => {
    const params = new URLSearchParams('q=load&status=error&node=2&page=3')
    const f = readBotFilter(params)
    expect(f).toEqual({ q: 'load', status: 'error', node: '2', step: undefined, error: undefined, page: 3 })
    const next = writeBotFilter(new URLSearchParams('tab=bots'), { ...f, page: 1 })
    expect(next.get('q')).toBe('load')
    expect(next.get('page')).toBeNull()
    expect(next.get('tab')).toBe('bots')
  })

  it('读写失败筛选', () => {
    const f = readFailureFilter(new URLSearchParams('category=scenario&errorCode=X'))
    expect(f.category).toBe('scenario')
    const written = writeFailureFilter(new URLSearchParams(), { category: 'network', page: 2 })
    expect(written.get('category')).toBe('network')
    expect(written.get('page')).toBe('2')
  })

  it('失败分类恰五类', () => {
    expect([...FAILURE_CATEGORIES]).toEqual(['target', 'executor', 'network', 'scenario', 'internal'])
  })
})
