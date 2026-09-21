import { beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-445 / FR-448：详情页 Tab 显隐只由能力画像 `capabilities` 决定（画像权威）。
 * devmock 详情响应下发 capabilities；实例 1=backend、2=proxy、30=generic/beacon（见 instance.ts 种子）。
 */
async function tabBar() {
  const bar = await screen.findByRole('tablist')
  return bar
}

describe('InstanceConsolePage 能力画像门控（FR-448）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('backend（id=1）：保留原 9 Tab，含监控/业务/Bot', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={1} />)
    const bar = await tabBar()
    for (const label of ['概览', '控制台', '文件配置', '监控', '玩家', '插件', '备份定时', '业务', 'Bot']) {
      expect(within(bar).getByRole('tab', { name: label })).toBeInTheDocument()
    }
    // 非 MC 专有 Tab 不出现在 backend 上。
    expect(within(bar).queryByRole('tab', { name: '子服拓扑' })).toBeNull()
    expect(within(bar).queryByRole('tab', { name: '进程' })).toBeNull()
  })

  it('proxy（id=2）：有子服拓扑/进程/健康/配置，无监控(TPS)/业务/Bot', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={2} />)
    const bar = await tabBar()
    for (const label of ['子服拓扑', '玩家', '插件', '进程', '健康检查', '配置']) {
      expect(within(bar).getByRole('tab', { name: label })).toBeInTheDocument()
    }
    for (const label of ['监控', '业务', 'Bot']) {
      expect(within(bar).queryByRole('tab', { name: label })).toBeNull()
    }
  })

  it('generic/beacon（id=30）：仅有通用 Tab，无任何 MC 专有 Tab', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={30} />)
    const bar = await tabBar()
    for (const label of ['概览', '控制台', '文件配置', '进程', '健康检查', '配置', '备份定时']) {
      expect(within(bar).getByRole('tab', { name: label })).toBeInTheDocument()
    }
    for (const label of ['监控', '玩家', '插件', '业务', 'Bot']) {
      expect(within(bar).queryByRole('tab', { name: label })).toBeNull()
    }
  })

  it('深链落到隐藏 Tab 时回退概览，不白屏（FR-448）', async () => {
    // beacon 画像不含 plugins；?tab=plugins 应回退 overview。
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30?tab=plugins' })
    const bar = await tabBar()
    expect(within(bar).getByRole('tab', { name: '概览' })).toHaveAttribute('aria-selected', 'true')
    expect(within(bar).queryByRole('tab', { name: '插件' })).toBeNull()
    // 概览面板仍渲染（非空白）。
    expect(await screen.findByText('动态与告警')).toBeInTheDocument()
  })
})
