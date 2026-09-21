import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import BcSegment from './BcSegment'
import BinarySegment from './BinarySegment'

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

/** FR-449：BC 代理专属分段（子服列表 / 跨服玩家 / 自身指标 / 配置）。 */
describe('BcSegment（FR-449，devmock）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('子服列表以 /topology 注册为唯一真源，跨服玩家与自身指标/配置分区呈现', async () => {
    // 实例 10=survival-proxy，devmock 注册了 backend 11(survival-lobby)/12(survival-world)。
    renderWithProviders(<BcSegment instanceId={10} />)

    const sub = await screen.findByTestId('bc-subservers')
    expect(await within(sub).findByText('survival-lobby')).toBeInTheDocument()
    expect(within(sub).getByText('survival-world')).toBeInTheDocument()

    expect(screen.getByTestId('bc-players')).toBeInTheDocument()
    // 自身运行指标（复用 ProcessPanel）与配置编辑分区。
    expect(await screen.findByTestId('process-panel')).toBeInTheDocument()
    expect(screen.getByTestId('config-segment')).toBeInTheDocument()
  })

  it('未注册子服时给出空态而非空白', async () => {
    // 实例 2=lobby-proxy，devmock 未注册任何子服。
    renderWithProviders(<BcSegment instanceId={2} />)
    const sub = await screen.findByTestId('bc-subservers')
    expect(await within(sub).findByText('该代理未注册任何子服')).toBeInTheDocument()
  })
})

/** FR-450：二进制 / beacon 专属分段（进程指标 / 端口健康 / 启动参数），不写死产品名。 */
describe('BinarySegment（FR-450，devmock）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('呈现进程指标/端口健康/启动参数，启动命令取自实例 startCommand', async () => {
    renderWithProviders(<BinarySegment instanceId={30} />)

    expect(await screen.findByTestId('process-panel')).toBeInTheDocument()
    expect(screen.getByTestId('health-panel')).toBeInTheDocument()
    expect(screen.getByTestId('launch-params-panel')).toBeInTheDocument()
    // 启动参数明面可编辑，初始值 = devmock beacon 的 startCommand。
    expect(
      await screen.findByDisplayValue('./beacon-1.1.0-linux-amd64 --config config.yaml'),
    ).toBeInTheDocument()
  })
})
