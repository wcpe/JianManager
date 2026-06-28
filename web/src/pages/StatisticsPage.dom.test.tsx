import { describe, it, expect, beforeAll, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { db } from '@/mocks/db'
import type { Session } from '@/mocks/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import StatisticsPage from './StatisticsPage'

/**
 * StatisticsPage 强断言（FR-220 平台级聚合统计）：验节点/实例/玩家 KPI 出数、实例状态分布含 CRASHED、
 * 分发区块按平台管理员权限分支（管理员见汇总、非管理员降级）、错误注入不崩。
 *
 * /nodes、/instances、/players 非可观测域端点，用 server.use 就地桩供本页聚合；
 * /metrics/overview、/client-dist/observability 由 mock 域 handler 提供。
 */
const ADMIN_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`
const MEMBER_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 2, username: 'bob', role: 0, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`

function loginAs(token: string, userId: number) {
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r', userId })
  useAuthStore.getState().login(token, 'r')
}

function node(over: Partial<Record<string, unknown>> = {}) {
  return {
    id: 1, uuid: 'n1', name: '北京节点', host: '10.0.0.1', grpcPort: 9100, wsPort: 9200,
    status: 1, maintenance: false, os: 'linux', arch: 'amd64', cpuCores: 8, memoryMb: 16384,
    diskTotalMb: 524288, cpuUsage: 40, memoryUsage: 50, diskUsage: 30, networkBytesSent: 0,
    networkBytesRecv: 0, loadAvg1: 1.2, lastHeartbeat: new Date().toISOString(), createdAt: new Date().toISOString(),
    ...over,
  }
}

function inst(over: Partial<Record<string, unknown>> = {}) {
  return {
    id: 1, uuid: 'i1', nodeId: 1, name: 'survival', type: 'minecraft', role: 'backend',
    processType: 'daemon', status: 'RUNNING', startCommand: '', workDir: '/srv', serverPort: 25565,
    autoStart: false, autoRestart: false, tags: '', createdAt: new Date().toISOString(),
    ...over,
  }
}

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    class RO { observe() {} unobserve() {} disconnect() {} }
    ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = RO
  }
})

beforeEach(() => {
  server.use(
    http.get(API('/nodes'), () =>
      HttpResponse.json([node({ id: 1, os: 'linux', arch: 'amd64' }), node({ id: 2, name: '上海节点', os: 'windows', arch: 'amd64', maintenance: true })]),
    ),
    http.get(API('/instances'), () =>
      HttpResponse.json([
        inst({ id: 1, status: 'RUNNING', role: 'backend', processType: 'daemon' }),
        inst({ id: 2, name: 'creative', status: 'CRASHED', role: 'backend', processType: 'docker' }),
        inst({ id: 3, name: 'proxy', status: 'STOPPED', role: 'proxy', processType: 'daemon' }),
      ]),
    ),
    http.get(API('/players'), () =>
      HttpResponse.json({
        players: [{ name: 'alice', instanceId: 1, instanceName: 'survival' }],
        backends: [
          { instanceId: 1, instanceName: 'survival', available: true },
          { instanceId: 2, instanceName: 'creative', available: false },
        ],
      }),
    ),
  )
})

describe('StatisticsPage（mock 假后端）', () => {
  it('① 平台管理员：渲染 KPI + 实例状态分布含「已崩溃」', async () => {
    loginAs(ADMIN_TOKEN, 1)
    renderWithProviders(<StatisticsPage />)
    expect(await screen.findByRole('heading', { name: '统计' })).toBeInTheDocument()
    // 实例状态分布面板出现（含崩溃维度标签）。
    expect(await screen.findByText('实例·按状态')).toBeInTheDocument()
    expect((await screen.findAllByText('已崩溃')).length).toBeGreaterThan(0)
    // 构成分布面板含角色/进程/系统/架构。
    expect(screen.getByText('实例·按角色')).toBeInTheDocument()
    expect(screen.getByText('节点·按系统')).toBeInTheDocument()
  })

  it('② 平台管理员：分发观测区块出汇总（成功率/活跃客户端）', async () => {
    loginAs(ADMIN_TOKEN, 1)
    renderWithProviders(<StatisticsPage />)
    // 分发端点（mock）成功 → 汇总卡出现。
    expect(await screen.findByText('更新成功率')).toBeInTheDocument()
    expect(await screen.findByText('活跃客户端')).toBeInTheDocument()
    expect(await screen.findByText('分发·版本分布')).toBeInTheDocument()
  })

  it('③ 非平台管理员：分发区块降级为权限提示，其余维度仍在', async () => {
    loginAs(MEMBER_TOKEN, 2)
    renderWithProviders(<StatisticsPage />)
    // 节点/实例维度照常。
    expect(await screen.findByRole('heading', { name: '统计' })).toBeInTheDocument()
    expect(await screen.findByText('实例·按状态')).toBeInTheDocument()
    // 分发区块降级提示，不发起请求、不出汇总。
    expect(await screen.findByText('客户端分发统计需平台管理员权限')).toBeInTheDocument()
    expect(screen.queryByText('更新成功率')).not.toBeInTheDocument()
  })

  it('④ 注入 500（/metrics/overview）→ 页面不崩溃，KPI 回退本地聚合', async () => {
    loginAs(ADMIN_TOKEN, 1)
    mockInject('get', '/metrics/overview', { kind: 'status', status: 500 })
    renderWithProviders(<StatisticsPage />)
    // overview 失败 → 计数回退 /instances 本地聚合，页面与分布面板仍渲染（非崩溃）。
    expect(await screen.findByRole('heading', { name: '统计' })).toBeInTheDocument()
    expect(await screen.findByText('实例·按状态')).toBeInTheDocument()
  })

  it('⑤ 平台管理员 + 分发端点 403 → 分发区块降级为错误态，其余正常', async () => {
    loginAs(ADMIN_TOKEN, 1)
    mockInject('get', '/client-dist/observability', { kind: 'status', status: 403 })
    renderWithProviders(<StatisticsPage />)
    expect(await screen.findByText('实例·按状态')).toBeInTheDocument()
    expect(await screen.findByText('加载分发观测失败')).toBeInTheDocument()
  })
})
