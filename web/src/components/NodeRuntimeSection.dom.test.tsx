import { describe, it, expect, beforeAll, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import NodeRuntimeSection from './NodeRuntimeSection'

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

beforeEach(() => {
  loginMockUser()
})

describe('NodeRuntimeSection（FR-298 节点运行时库）', () => {
  it('统一列表展示 JDK（类型徽章），扫描发现后勾选候选入库', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeSection nodeId={1} active />)

    // 统一视图：node-jdks 种子（2 条 temurin）以 type=jdk 呈现，带 JDK 类型徽章。
    expect((await screen.findAllByText('temurin')).length).toBeGreaterThanOrEqual(2)
    expect(screen.getAllByText('JDK').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('/opt/jdks/temurin-21')).toBeInTheDocument()

    // 扫描发现：开模态列候选。
    await user.click(screen.getByRole('button', { name: /扫描发现/ }))
    expect(await screen.findByText('扫描发现运行时')).toBeInTheDocument()

    // jdk 候选与已登记路径相同 → 标「已在库」且禁勾；nodejs 候选可勾。
    expect(await screen.findByText('已在库')).toBeInTheDocument()
    const nodeCheckbox = await screen.findByRole('checkbox', { name: '/usr/local/bin/node' })
    expect(nodeCheckbox).toBeEnabled()
    expect(screen.getByRole('checkbox', { name: '/opt/jdks/temurin-21' })).toBeDisabled()

    // 未勾选时入库按钮禁用；勾选 nodejs 候选后入库。
    expect(screen.getByRole('button', { name: /入库所选/ })).toBeDisabled()
    await user.click(nodeCheckbox)
    await user.click(screen.getByRole('button', { name: /入库所选/ }))

    // 入库成功：模态关、列表出现 Node.js 行。
    await waitFor(() => expect(screen.queryByText('扫描发现运行时')).not.toBeInTheDocument())
    expect(await screen.findByText('Node.js 22')).toBeInTheDocument()
    expect(screen.getByText('/usr/local/bin/node')).toBeInTheDocument()
    expect(screen.getByText('Node.js')).toBeInTheDocument() // 类型徽章
  })

  it('入库后重复扫描该候选标已在库', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeSection nodeId={1} active />)

    // 先入库 nodejs 候选。
    await user.click(await screen.findByRole('button', { name: /扫描发现/ }))
    await user.click(await screen.findByRole('checkbox', { name: '/usr/local/bin/node' }))
    await user.click(screen.getByRole('button', { name: /入库所选/ }))
    await waitFor(() => expect(screen.queryByText('扫描发现运行时')).not.toBeInTheDocument())

    // 重扫：两条候选均已在库、皆禁勾，入库按钮保持禁用。
    await user.click(screen.getByRole('button', { name: /扫描发现/ }))
    await screen.findByText('扫描发现运行时')
    await waitFor(() => expect(screen.getAllByText('已在库')).toHaveLength(2))
    expect(screen.getByRole('checkbox', { name: '/usr/local/bin/node' })).toBeDisabled()
    expect(screen.getByRole('button', { name: /入库所选/ })).toBeDisabled()
  })
})
