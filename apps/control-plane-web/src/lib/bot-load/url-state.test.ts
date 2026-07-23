import { describe, expect, it } from 'vitest'
import {
  mergeSearchParams,
  parseBotsTab,
  readSessionsFilter,
  readTemplatesFilter,
} from './url-state'

describe('bot-load url-state', () => {
  it('tab 非法回退 fleet', () => {
    expect(parseBotsTab(null)).toBe('fleet')
    expect(parseBotsTab('templates')).toBe('templates')
    expect(parseBotsTab('sessions')).toBe('sessions')
    expect(parseBotsTab('nope')).toBe('fleet')
  })

  it('读会话/模板筛选', () => {
    const sp = new URLSearchParams('q=hello&instanceId=2&page=3&runState=running')
    expect(readSessionsFilter(sp)).toEqual({
      q: 'hello',
      instanceId: 2,
      runState: 'running',
      page: 3,
    })
    const tp = new URLSearchParams('q=cmd&tag=demo&page=2')
    expect(readTemplatesFilter(tp)).toEqual({ q: 'cmd', tag: 'demo', page: 2 })
  })

  it('mergeSearchParams 删除空值', () => {
    const cur = new URLSearchParams('tab=templates&q=a')
    const next = mergeSearchParams(cur, { q: '', page: 2, tag: 'x' })
    expect(next.get('tab')).toBe('templates')
    expect(next.get('q')).toBeNull()
    expect(next.get('page')).toBe('2')
    expect(next.get('tag')).toBe('x')
  })
})
