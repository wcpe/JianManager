import { describe, it, expect, beforeEach } from 'vitest'
import {
  parseSseBuffer,
  BotLoadEventClient,
  subscribeBotLoadRunStream,
  __resetBotLoadStreamSingletonsForTest,
  __botLoadStreamSharedCountForTest,
} from './session-event-client'

describe('parseSseBuffer', () => {
  it('解析 event/id/data 多行 data', () => {
    const raw = 'id: 1\nevent: counts\ndata: {"a":1}\n\nid: 2\nevent: metric\ndata: line1\ndata: line2\n\npartial'
    const { frames, rest } = parseSseBuffer(raw)
    expect(frames).toHaveLength(2)
    expect(frames[0]).toEqual({ event: 'counts', id: '1', data: '{"a":1}', retry: undefined })
    expect(frames[1]).toEqual({ event: 'metric', id: '2', data: 'line1\nline2', retry: undefined })
    expect(rest).toBe('partial')
  })

  it('忽略注释与空块', () => {
    const { frames } = parseSseBuffer(': keep-alive\n\nevent: warning\ndata: {"code":"X"}\n\n')
    expect(frames).toHaveLength(1)
    expect(frames[0]!.event).toBe('warning')
  })
})

describe('BotLoadEventClient / 共享订阅', () => {
  beforeEach(() => {
    __resetBotLoadStreamSingletonsForTest()
  })

  it('引用计数归零时关闭共享连接', () => {
    const frames: string[] = []
    const unsub1 = subscribeBotLoadRunStream({
      runId: 1,
      url: '/api/v1/bots/stress-sessions/1/stream',
      getToken: async () => 'tok',
      onEvent: (f) => frames.push(f.event),
      fetchImpl: async () =>
        new Response('', { status: 503, headers: { 'Content-Type': 'text/event-stream' } }),
    })
    const unsub2 = subscribeBotLoadRunStream({
      runId: 1,
      url: '/api/v1/bots/stress-sessions/1/stream',
      getToken: async () => 'tok',
      onEvent: () => {},
      fetchImpl: async () =>
        new Response('', { status: 503, headers: { 'Content-Type': 'text/event-stream' } }),
    })
    expect(__botLoadStreamSharedCountForTest()).toBe(1)
    unsub1()
    expect(__botLoadStreamSharedCountForTest()).toBe(1)
    unsub2()
    expect(__botLoadStreamSharedCountForTest()).toBe(0)
  })

  it('404 永久关闭不重连', async () => {
    const statuses: string[] = []
    const client = new BotLoadEventClient({
      runId: 9,
      url: '/x',
      getToken: async () => 't',
      onEvent: () => {},
      onStatus: (s) => statuses.push(s),
      autoReconnect: true,
      fetchImpl: async () => new Response('nf', { status: 404 }),
    })
    client.start()
    await new Promise((r) => setTimeout(r, 20))
    expect(statuses).toContain('error')
    expect(client.getStatus()).toBe('error')
    client.stop()
  })
})
