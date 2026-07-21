import { describe, expect, it } from 'vitest'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from './client-dist-query'

describe('client-dist-query', () => {
  it('兼容历史 channel 并优先读取 channelId', () => {
    expect(readClientDistQuery(new URLSearchParams('channel=legacy'))).toMatchObject({ channelId: 'legacy' })
    expect(readClientDistQuery(new URLSearchParams('channel=legacy&channelId=current'))).toMatchObject({ channelId: 'current' })
  })

  it('写入时统一 channelId 并仅透传冻结键', () => {
    const next = updateClientDistQuery(new URLSearchParams('channel=legacy&extra=drop'), {
      channelId: 'skyblock-s1',
      ip: '192.0.2.9',
      tab: 'events',
    })

    expect(next.toString()).toBe('channelId=skyblock-s1&ip=192.0.2.9&tab=events')
  })

  it('构造跨页链接时保留现有筛选并允许覆盖 tab', () => {
    const href = buildClientDistHref(
      '/client-dist-security',
      new URLSearchParams('channelId=skyblock-s1&machineId=m-1&version=2&tab=logs'),
      { ip: '192.0.2.9', tab: 'events' },
    )

    expect(href).toBe('/client-dist-security?channelId=skyblock-s1&ip=192.0.2.9&machineId=m-1&version=2&tab=events')
  })
})
