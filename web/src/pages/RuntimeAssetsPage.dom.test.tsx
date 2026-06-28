import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import RuntimeAssetsPage from './RuntimeAssetsPage'

/**
 * RuntimeAssetsPage 强断言（FR-200）：①渲染 seed JDK/制品 ②删 JDK→overview 联动减少 ③注入 500→错误态。
 * 该页仅调本域 GET /runtime-assets/overview + DELETE /nodes/:id/jdks/:jid（无跨域请求）。
 */
describe('RuntimeAssetsPage（mock 假后端）', () => {
  it('渲染 seed 的 JDK 矩阵与制品', async () => {
    loginMockUser()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('运行时与制品')).toBeInTheDocument()
    // 两个区标题。
    expect(screen.getByText('JDK 运行时')).toBeInTheDocument()
    expect(screen.getByText('制品库')).toBeInTheDocument()
    // seed 的 JDK 卡片（temurin 21 / 17）版本号与制品名（paper core）。
    expect(screen.getByText('21.0.3+9')).toBeInTheDocument()
    expect(screen.getByText('17.0.11+9')).toBeInTheDocument()
    expect(screen.getByText('paper-1.20.4')).toBeInTheDocument()
  })

  it('删除一个 JDK 后，overview 联动移除该 JDK', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('17.0.11+9')).toBeInTheDocument()

    // 定位「temurin 17」卡片（JDKCard 用 Panel，data-slot=panel）内的删除按钮，打开危险确认弹窗。
    const card17 = screen.getByText('17.0.11+9').closest('[data-slot="panel"]') as HTMLElement
    await user.click(within(card17).getByRole('button', { name: '删除' }))

    // 危险确认弹窗：点确认（JDK 删除无需逐字输入）。
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '删除' }))

    // useDeleteRuntimeJDK 失效 ['runtime-assets-overview'] → 重新拉取，handler 现已无 17，DOM 中消失。
    await waitFor(() => expect(screen.queryByText('17.0.11+9')).not.toBeInTheDocument())
    // 另一个 JDK 仍在。
    expect(screen.getByText('21.0.3+9')).toBeInTheDocument()
  })

  it('注入 500：显示加载失败错误态', async () => {
    loginMockUser()
    mockInject('get', '/runtime-assets/overview', { kind: 'status', status: 500 })
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('加载运行时与制品失败')).toBeInTheDocument()
  })
})
