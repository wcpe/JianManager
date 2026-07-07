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
    expect(screen.getByText('/opt/jdks/temurin-21')).toBeInTheDocument()
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
    expect(screen.getByText('Temurin')).toBeInTheDocument()
    expect(screen.getByText('/opt/jdks/custom-java-21')).toBeInTheDocument()
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
