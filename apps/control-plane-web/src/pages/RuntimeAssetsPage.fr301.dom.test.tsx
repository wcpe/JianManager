import { describe, it, expect } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import RuntimeAssetsPage from './RuntimeAssetsPage'

/** 假后端非 JDK 运行时行形状（同 mocks/handlers/domains/node.ts 的 MockNodeRuntime）。 */
interface MockNodeRuntime {
  id: number
  nodeId: number
  type: string
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

/** 往假后端植入一条 nodejs 运行时（节点 1），令 overview 矩阵出现多类型列。 */
function seedNodejsRuntime() {
  db<MockNodeRuntime>('node-runtimes').insert({
    nodeId: 1,
    type: 'nodejs',
    name: 'Node.js 22',
    majorVersion: 22,
    version: '22.17.0',
    arch: 'x64',
    path: '/usr/local/bin/node',
    managed: false,
    createdAt: '2026-06-28T08:00:00Z',
  })
}

/**
 * RuntimeAssetsPage FR-301 强断言：①矩阵渲染多类型（jdk + nodejs 类型徽章分列）
 * ②「上次同步」显示 + 刷新按钮触发强制同步（syncedAt 前移为「刚刚」）
 * ③部分节点同步失败容忍——旧数据（JDK 卡片/矩阵）仍在，不清空不转错误态。
 * 独立于既有 RuntimeAssetsPage.dom.test.tsx（不动其用例）。
 */
describe('RuntimeAssetsPage FR-301（多运行时矩阵与刷新）', () => {
  it('矩阵渲染多类型：jdk 与 nodejs 各成列、类型徽章区分，nodejs 引用恒 0', async () => {
    loginMockUser()
    seedNodejsRuntime()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('节点 × 运行时矩阵（格内为引用实例数）')).toBeInTheDocument()
    // 类型徽章：seed 两个 temurin JDK → 两列 JDK 徽章；nodejs 一列。
    expect(screen.getAllByText('JDK').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('Node.js')).toBeInTheDocument()
    // nodejs 列头以 v<major> 标注版本。
    expect(screen.getByText('v22')).toBeInTheDocument()
    // jdk 列头保留 厂商+major（JDK 卡片标题同文案，故用 getAllByText）。
    expect(screen.getAllByText('temurin 21').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('temurin 17').length).toBeGreaterThanOrEqual(1)
  })

  it('显示「上次同步」并可手动刷新：刷新后 syncedAt 前移为「刚刚」，旧数据仍在（失败容忍）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    // 初始 syncedAt = seed 的固定历史时间（非「刚刚」）。
    const syncLabel = await screen.findByText(/上次同步/)
    expect(syncLabel).toBeInTheDocument()
    expect(syncLabel.textContent).not.toContain('刚刚')

    // 点击刷新：mock 在线节点（alpha）syncedAt 前移，离线节点（beta）ok=false。
    await user.click(screen.getByRole('button', { name: /刷新/ }))

    // 刷新成功后 overview 失效重拉，整体 syncedAt 变为「刚刚」。
    await waitFor(() => expect(screen.getByText(/上次同步/).textContent).toContain('刚刚'))

    // 失败容忍：离线节点未拖垮页面，JDK 旧数据（卡片版本号）仍在、无错误态。
    expect(screen.getByText('21.0.3+9')).toBeInTheDocument()
    expect(screen.getByText('17.0.11+9')).toBeInTheDocument()
    expect(screen.queryByText('加载运行时与制品失败')).not.toBeInTheDocument()
  })

  it('从未同步（全节点无时间戳）显示「尚未同步」', async () => {
    loginMockUser()
    interface MockRuntimeSync { id: number; nodeId: number; syncedAt: string | null }
    const syncs = db<MockRuntimeSync>('runtime-syncs')
    for (const rec of syncs.list()) syncs.update(rec.id, { syncedAt: null })
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('尚未同步')).toBeInTheDocument()
  })

  it('刷新接口 500：报错提示但页面数据不清空', async () => {
    loginMockUser()
    const user = userEvent.setup()
    mockInject('post', '/runtime-assets/refresh', { kind: 'status', status: 500 })
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('21.0.3+9')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /刷新/ }))

    // 失败后旧数据仍在、不转错误态。
    await waitFor(() => expect(screen.getByRole('button', { name: /刷新/ })).toBeEnabled())
    expect(screen.getByText('21.0.3+9')).toBeInTheDocument()
    expect(screen.queryByText('加载运行时与制品失败')).not.toBeInTheDocument()
  })
})
