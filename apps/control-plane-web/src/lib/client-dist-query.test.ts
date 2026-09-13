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
      '/client-dist-ops',
      new URLSearchParams('channelId=skyblock-s1&machineId=m-1&version=2&tab=logs'),
      { ip: '192.0.2.9', tab: 'events' },
    )

    expect(href).toBe('/client-dist-ops?channelId=skyblock-s1&ip=192.0.2.9&machineId=m-1&version=2&tab=events')
  })

  it('FR-430：新增冻结键 type/seg 随链接透传（位置在 tab 之后）', () => {
    const href = buildClientDistHref(
      '/client-dist-ops',
      new URLSearchParams('channelId=skyblock-s1&tab=logs&type=request&seg=events&unknown=drop'),
      { seg: 'ip' },
    )

    expect(href).toBe('/client-dist-ops?channelId=skyblock-s1&tab=logs&type=request&seg=ip')
  })

  it('FR-430：type/seg 可被 clear（null 删除键）', () => {
    const next = updateClientDistQuery(
      new URLSearchParams('channelId=x&tab=logs&type=request&seg=events'),
      { seg: null, type: null },
    )
    expect(next.toString()).toBe('channelId=x&tab=logs')
  })

  it('读取时透传 type/seg（仅冻结键，忽略未知键）', () => {
    const query = readClientDistQuery(new URLSearchParams('tab=profiles&seg=ip&type=request&drop=1'))
    expect(query).toMatchObject({ tab: 'profiles', seg: 'ip', type: 'request' })
    expect(query).not.toHaveProperty('drop')
  })
})
