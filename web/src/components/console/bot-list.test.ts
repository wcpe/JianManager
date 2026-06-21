import { describe, it, expect } from 'vitest'
import {
  botStatusKind,
  summaryCounts,
  groupBots,
  parseBotConfig,
  suggestBotServer,
} from './bot-list'
import type { BotInfo, BotSummary } from '@/api/bots'
import type { NodeInfo } from '@/api/nodes'

function bot(id: number, status: string, behavior: string): BotInfo {
  return {
    id,
    uuid: `b-${id}`,
    instanceId: 1,
    name: `bot-${id}`,
    status,
    config: JSON.stringify({ server: 'mc.example.com', port: 25565, auth: 'offline' }),
    behavior,
    workerId: 'w1',
    createdAt: '',
    updatedAt: '',
  }
}

function node(host: string): NodeInfo {
  return {
    id: 1,
    uuid: 'n-1',
    name: 'node-a',
    host,
    grpcPort: 0,
    wsPort: 0,
    status: 1,
    os: 'linux',
    arch: 'amd64',
    cpuCores: 0,
    memoryMb: 0,
    diskTotalMb: 0,
    cpuUsage: 0,
    memoryUsage: 0,
    diskUsage: 0,
    networkBytesSent: 0,
    networkBytesRecv: 0,
    lastHeartbeat: null,
    createdAt: '',
  }
}

describe('botStatusKind', () => {
  it('maps backend statuses to semantic buckets', () => {
    expect(botStatusKind('connected')).toBe('online')
    expect(botStatusKind('connecting')).toBe('connecting')
    expect(botStatusKind('error')).toBe('error')
    expect(botStatusKind('disconnected')).toBe('offline')
    expect(botStatusKind('whatever')).toBe('offline')
  })
})

describe('summaryCounts', () => {
  it('derives card counts from aggregate byStatus (not page data)', () => {
    const summary: BotSummary = {
      total: 1000,
      byStatus: { connected: 800, connecting: 50, disconnected: 130, error: 20 },
    }
    expect(summaryCounts(summary)).toEqual({
      total: 1000,
      online: 800,
      connecting: 50,
      error: 20,
    })
  })

  it('returns zeros when summary is missing', () => {
    expect(summaryCounts(undefined)).toEqual({ total: 0, online: 0, connecting: 0, error: 0 })
  })

  it('treats absent byStatus keys as zero', () => {
    expect(summaryCounts({ total: 5, byStatus: {} })).toEqual({
      total: 5,
      online: 0,
      connecting: 0,
      error: 0,
    })
  })
})

describe('groupBots', () => {
  it('groups by status kind in online→connecting→error→offline order', () => {
    const bots = [
      bot(1, 'disconnected', 'idle'),
      bot(2, 'connected', 'guard'),
      bot(3, 'error', 'idle'),
      bot(4, 'connecting', 'follow'),
      bot(5, 'connected', 'idle'),
    ]
    const groups = groupBots(bots, 'status')
    expect(groups.map((g) => g.key)).toEqual(['online', 'connecting', 'error', 'offline'])
    expect(groups[0].bots.map((b) => b.id)).toEqual([2, 5])
  })

  it('groups by behavior sorted by name', () => {
    const bots = [bot(1, 'connected', 'patrol'), bot(2, 'connected', 'guard'), bot(3, 'connected', 'guard')]
    const groups = groupBots(bots, 'behavior')
    expect(groups.map((g) => g.key)).toEqual(['guard', 'patrol'])
    expect(groups[0].bots.map((b) => b.id)).toEqual([2, 3])
  })

  it('buckets blank behavior as unknown', () => {
    const groups = groupBots([bot(1, 'connected', '')], 'behavior')
    expect(groups.map((g) => g.key)).toEqual(['unknown'])
  })

  it('returns empty array for empty input', () => {
    expect(groupBots([], 'status')).toEqual([])
  })
})

describe('parseBotConfig', () => {
  it('parses valid JSON config', () => {
    expect(parseBotConfig('{"server":"h","port":1,"auth":"offline"}')).toEqual({
      server: 'h',
      port: 1,
      auth: 'offline',
    })
  })

  it('falls back to placeholder on invalid JSON', () => {
    expect(parseBotConfig('not-json')).toEqual({ server: '', port: 0, auth: '' })
  })
})

describe('suggestBotServer', () => {
  it('prefills node host and default MC port', () => {
    expect(suggestBotServer(node('10.0.0.5'))).toEqual({ server: '10.0.0.5', port: 25565 })
  })

  it('falls back to localhost when node is unknown', () => {
    expect(suggestBotServer(undefined)).toEqual({ server: '127.0.0.1', port: 25565 })
  })
})
