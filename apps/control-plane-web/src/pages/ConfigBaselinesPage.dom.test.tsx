import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ConfigBaselinesPage from './ConfigBaselinesPage'

/** 渲染配置基线页（假后端种子：group:2 的 server.properties 基线，成员实例 1 与其不一致→漂移）。 */
function renderPage() {
  loginMockUser()
  const user = userEvent.setup()
  renderWithProviders(<ConfigBaselinesPage />)
  return { user }
}

describe('ConfigBaselinesPage（FR-458 配置基线与漂移收敛）', () => {
  it('列出基线', async () => {
    renderPage()
    expect(await screen.findByText('group:2')).toBeInTheDocument()
    expect(screen.getByText('server.properties')).toBeInTheDocument()
  })

  it('漂移检测：scope 内实例与基线不一致即标记漂移', async () => {
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-drift-1'))
    expect(await screen.findByText('1/1 台漂移')).toBeInTheDocument()
    // 逐台明细：实例 1 标记为漂移。
    expect(screen.getByText('漂移')).toBeInTheDocument()
  })

  it('一键收敛：推送基线后复核残余漂移为 0', async () => {
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-drift-1'))
    await screen.findByText('1/1 台漂移')

    await user.click(screen.getByTestId('baseline-converge'))
    await waitFor(() => expect(screen.getByTestId('baseline-converge-result')).toBeInTheDocument())
    // 收敛后复核：残余漂移归零，出现「已全部一致」。
    expect(await screen.findByText('已全部一致')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('0/1 台漂移')).toBeInTheDocument())
  })

  it('新建基线：保存后出现在列表', async () => {
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-create'))
    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText('文件路径'))
    await user.type(within(dialog).getByLabelText('文件路径'), 'custom.properties')
    await user.type(within(dialog).getByLabelText('基线内容'), 'a=1')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('custom.properties')).toBeInTheDocument()
  })
})
