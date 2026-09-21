import { beforeEach, describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-448/FR-449 回归：审计发现的两处「能力画像门控」缺陷。
 *
 * 1. 概览的 ServerProbe 未连入横幅必须与 MC 世界语义联动（`mcSemantics`）：proxy/generic/beacon
 *    本不该有 ServerProbe，横幅只会误导（同文件顶栏 showProbeChip 早已按 mcSemantics 门控）。
 * 2. proxy 的「玩家」页签必须呈现跨服分布（FR-445 §2.3 按角色语义），
 *    而不是复用 backend 的单实例实名名单视图。
 *
 * devmock 种子：1=backend(RUNNING)、2=proxy(STOPPED，探针未连)、3=backend(CRASHED，探针未连)。
 */
const probeText = '探针未连入，部分在线玩家与世界数据为预览值'

function overviewOf(instanceId: number) {
  return renderWithProviders(<InstanceConsolePage instanceId={instanceId} />, {
    route: `/instances/${instanceId}?tab=overview`,
  })
}

describe('概览探针横幅按 mcSemantics 门控（FR-448）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('backend（MC 世界语义）+ 探针未连 → 显示探针横幅', async () => {
    overviewOf(3)
    expect(await screen.findByText(probeText)).toBeInTheDocument()
  })

  it('proxy（无世界语义）→ 不显示 MC 专属探针横幅', async () => {
    overviewOf(2)
    // 概览面板已渲染（动态与告警标题出现）后再断言横幅缺席。
    expect(await screen.findByText('动态与告警')).toBeInTheDocument()
    expect(screen.queryByText(probeText)).toBeNull()
  })
})

describe('proxy 玩家页签走跨服分布（FR-445 §2.3 / FR-449）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('proxy 玩家页签渲染跨服分布面板，而非 backend 的单实例名单', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={2} />, { route: '/instances/2?tab=players' })
    expect(await screen.findByTestId('bc-players')).toBeInTheDocument()
    expect(screen.queryByText('在线玩家')).toBeNull()
    expect(screen.queryByText('封禁记录')).toBeNull()
  })

  it('backend 玩家页签仍渲染单实例名单，不渲染跨服面板', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1?tab=players' })
    expect(await screen.findByText('在线玩家')).toBeInTheDocument()
    expect(screen.queryByTestId('bc-players')).toBeNull()
  })
})
