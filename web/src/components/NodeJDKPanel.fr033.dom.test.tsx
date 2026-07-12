import { describe, it, expect, beforeAll, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import NodeJDKPanel from './NodeJDKPanel'

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

describe('NodeJDKPanel（FR-033 JDK 与运行时管理）', () => {
  it('展示已登记 JDK，并可探测登记外部 JDK 后回到列表', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeJDKPanel nodeId={1} active />)

    expect((await screen.findAllByText('temurin')).length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('Java 21')).toBeInTheDocument()
    // FR-298 后 JDK 面板下方还有「运行时」统一列表分区，同路径出现多处。
    expect(screen.getAllByText('/opt/jdks/temurin-21').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByRole('button', { name: /托管2/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '登记已有' }))
    expect(screen.getByLabelText('标记为 Worker 托管（仅作记录）')).toBeInTheDocument()
    expect(screen.getByText(/后端自动探测厂商/)).toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('/opt/jdks/temurin-21 或 .../bin/java'), '/opt/jdks/custom-java-21')
    await user.click(screen.getByRole('button', { name: '检测' }))

    expect(await screen.findByText('21.0.4+9')).toBeInTheDocument()
    expect(screen.getByText('/opt/jdks/custom-java-21')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(screen.getByRole('button', { name: /外部1/ })).toBeInTheDocument())
    // FR-298 统一列表分区同步展示新登记 JDK，同名/同路径出现多处。
    expect(screen.getAllByText('Temurin').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('/opt/jdks/custom-java-21').length).toBeGreaterThanOrEqual(1)
  })

  it('探测失败时展示错误且保持保存禁用', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeJDKPanel nodeId={1} active />)

    await user.click(await screen.findByRole('button', { name: '登记已有' }))
    await user.type(screen.getByPlaceholderText('/opt/jdks/temurin-21 或 .../bin/java'), '/opt/invalid-runtime')
    await user.click(screen.getByRole('button', { name: '检测' }))

    expect(await screen.findByText(/所选目录不是有效的 JDK/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled()
  })
})

describe('NodeJDKPanel 编辑登记信息（FR-311）', () => {
  it('行内编辑：改厂商与路径保存后列表更新', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeJDKPanel nodeId={1} active />)

    // 种子 JDK 行出现后点铅笔开编辑模态。
    await screen.findByText('/opt/jdks/temurin-21')
    await user.click(screen.getAllByRole('button', { name: '编辑' })[0])
    expect(await screen.findByText('编辑 JDK 登记信息')).toBeInTheDocument()

    // 改具体版本 + 路径 → 保存。
    const verInput = screen.getByRole('textbox', { name: '版本号' })
    await user.clear(verInput)
    await user.type(verInput, '21.0.99')
    const pathInput = screen.getByRole('textbox', { name: '本地路径' })
    await user.clear(pathInput)
    await user.type(pathInput, '/opt/jdks/temurin-21-edited')
    await user.click(screen.getByRole('button', { name: '保存' }))

    // 模态关、列表出现新路径（mock PUT 生效 + invalidate 刷新）。
    await waitFor(() => expect(screen.queryByText('编辑 JDK 登记信息')).not.toBeInTheDocument())
    expect(await screen.findByText('/opt/jdks/temurin-21-edited')).toBeInTheDocument()
  })
})
