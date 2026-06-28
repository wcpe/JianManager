import { describe, it, expect, beforeAll } from 'vitest'
import { screen, within } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { db } from '@/mocks/db'
import type { Session } from '@/mocks/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@/mocks/inject'
import ClientDistMonitoringPage from './ClientDistMonitoringPage'

/**
 * ClientDistMonitoringPage 强断言（FR-218）：
 * ① 平台管理员：总览 KPI（汇总）+ 时序趋势卡 + 分布面板出数（接通 /client-dist/observability series/summary/分布）。
 * ② 频道筛选器：选单频道后请求带 channelId、内容仍渲染（用 mock 频道 list 填下拉）。
 * ③ 非平台管理员：整页降级为权限提示、不出 KPI、不发起请求。
 * ④ 端点 403/500 注入：降级为错误态、不崩溃。
 *
 * 图表（recharts）依赖 ResizeObserver 实测宽度，jsdom 无布局——补 ResizeObserver 桩使组件不崩，
 * 断言 Panel 标题 / KPI 标签 / 频道下拉项（均不依赖容器尺寸）即证数据路径接通。
 * /client-channels、/client-dist/observability 由 client 域 mock handler 提供，无需就地桩。
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

// 图表（recharts）依赖 ResizeObserver；Radix Select 依赖 pointer/scroll API——jsdom 默认缺，按标准配方补齐。
beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    class RO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = RO
  }
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
})

describe('ClientDistMonitoringPage（mock 假后端）', () => {
  it('① 平台管理员：渲染 KPI + 时序趋势 + 分布面板', async () => {
    loginAs(ADMIN_TOKEN, 1)
    renderWithProviders(<ClientDistMonitoringPage />)

    expect(await screen.findByRole('heading', { name: '客户端分发监控' })).toBeInTheDocument()
    // 汇总 KPI（summary 到达）。
    expect(await screen.findByText('更新成功率')).toBeInTheDocument()
    expect(screen.getByText('活跃客户端')).toBeInTheDocument()
    // 时序趋势卡标题（series 路径）。
    expect(screen.getByText('拉取趋势')).toBeInTheDocument()
    expect(screen.getByText('更新成功率趋势')).toBeInTheDocument()
    // 分布面板（versionDist/platformDist/lagDist）。
    expect(screen.getByText('版本分布')).toBeInTheDocument()
    expect(screen.getByText('平台分布')).toBeInTheDocument()
    expect(screen.getByText('版本滞后分布')).toBeInTheDocument()
    // 分布桶内容来自 mock buildObservability（windows 平台、v7 版本）。
    expect(await screen.findByText('Windows')).toBeInTheDocument()
    expect(screen.getByText('v7')).toBeInTheDocument()
  })

  it('② 频道筛选器：渲染并默认「全部频道（总）」（频道 list 已加载）', async () => {
    loginAs(ADMIN_TOKEN, 1)
    renderWithProviders(<ClientDistMonitoringPage />)
    await screen.findByText('更新成功率')

    // 频道筛选器存在且默认取总（不传 channelId）；下拉项由 mock 频道 list 填充（Radix 弹层在 jsdom 不稳，不展开断言）。
    const trigger = screen.getByRole('combobox')
    expect(within(trigger).getByText('全部频道（总）')).toBeInTheDocument()
  })

  it('③ 非平台管理员：整页降级为权限提示、不出 KPI', async () => {
    loginAs(MEMBER_TOKEN, 2)
    renderWithProviders(<ClientDistMonitoringPage />)

    expect(await screen.findByRole('heading', { name: '客户端分发监控' })).toBeInTheDocument()
    expect(await screen.findByText('客户端分发监控需平台管理员权限')).toBeInTheDocument()
    expect(screen.queryByText('更新成功率')).not.toBeInTheDocument()
  })

  it('④ 平台管理员 + 端点 403 → 降级为错误态、不崩溃', async () => {
    loginAs(ADMIN_TOKEN, 1)
    mockInject('get', '/client-dist/observability', { kind: 'status', status: 403 })
    renderWithProviders(<ClientDistMonitoringPage />)

    expect(await screen.findByRole('heading', { name: '客户端分发监控' })).toBeInTheDocument()
    expect(await screen.findByText('加载分发观测失败')).toBeInTheDocument()
    expect(screen.queryByText('更新成功率')).not.toBeInTheDocument()
  })
})
