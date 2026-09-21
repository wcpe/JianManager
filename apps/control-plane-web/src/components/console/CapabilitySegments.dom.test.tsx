import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import BcSegment from './BcSegment'
import BcPlayersPanel from './BcPlayersPanel'
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

/**
 * FR-449：BC 代理「子服拓扑」分段。minor 5 单一归属后本分段只承载子服列表；
 * 跨服玩家归 BcPlayersPanel（players 页签），自身指标/配置归各自页签。
 */
describe('BcSegment（FR-449，devmock）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('子服列表以 /topology 注册为唯一真源', async () => {
    // 实例 10=survival-proxy，devmock 注册了 backend 11(survival-lobby)/12(survival-world)。
    renderWithProviders(<BcSegment instanceId={10} />)

    const sub = await screen.findByTestId('bc-subservers')
    expect(await within(sub).findByText('survival-lobby')).toBeInTheDocument()
    expect(within(sub).getByText('survival-world')).toBeInTheDocument()
  })

  it('单一归属：不再内联跨服玩家/进程指标/配置面板（避免与 players/process/config 页签重复渲染）', async () => {
    renderWithProviders(<BcSegment instanceId={10} />)
    await screen.findByTestId('bc-subservers')
    expect(screen.queryByTestId('bc-players')).toBeNull()
    expect(screen.queryByTestId('process-panel')).toBeNull()
    expect(screen.queryByTestId('config-segment')).toBeNull()
  })

  it('未注册子服时给出空态而非空白', async () => {
    // 实例 2=lobby-proxy，devmock 未注册任何子服。
    renderWithProviders(<BcSegment instanceId={2} />)
    const sub = await screen.findByTestId('bc-subservers')
    expect(await within(sub).findByText('该代理未注册任何子服')).toBeInTheDocument()
  })
})

/** FR-449 §2.2.2（proxy 语义的 players 能力）：跨服玩家分布 + 本代理已注册子服在线总数。 */
describe('BcPlayersPanel（跨服玩家，devmock）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('按子服分组呈现跨服玩家分布', async () => {
    renderWithProviders(<BcPlayersPanel instanceId={10} />)
    expect(await screen.findByTestId('bc-players')).toBeInTheDocument()
  })

  it('探针不可达子服不计入总数，并显式标注口径（不含 N 个不可达子服）', async () => {
    server.use(
      http.get(API('/players'), () =>
        HttpResponse.json({
          players: [{ name: 'Alice', instanceId: 11, instanceName: 'survival-lobby' }],
          backends: [
            { instanceId: 11, instanceName: 'survival-lobby', available: true },
            { instanceId: 12, instanceName: 'survival-world', available: false },
          ],
        }),
      ),
    )
    renderWithProviders(<BcPlayersPanel instanceId={10} />)
    expect(await screen.findByTestId('bc-players-caveat')).toHaveTextContent('不含 1 个探针不可达子服')
    // 总数只计本代理已注册且可达子服的玩家（Alice=1），措辞不夸大至「全网」。
    const title = screen.getByTestId('bc-players')
    expect(title).toHaveTextContent('本代理已注册子服共 1 人在线')
    expect(title).not.toHaveTextContent('全网')
  })
})

/** FR-450：进程能力分段（进程指标 + 启动参数）；端口健康归 health 页签。 */
describe('BinarySegment（FR-450，devmock）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('呈现进程指标/启动参数，且不再内联健康面板（单一归属）', async () => {
    renderWithProviders(<BinarySegment instanceId={30} />)

    expect(await screen.findByTestId('process-panel')).toBeInTheDocument()
    expect(screen.getByTestId('launch-params-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('health-panel')).toBeNull()
    // 启动参数明面可编辑，初始值 = devmock beacon 的 startCommand。
    expect(
      await screen.findByDisplayValue('./beacon-1.1.0-linux-amd64 --config config.yaml'),
    ).toBeInTheDocument()
  })
})
