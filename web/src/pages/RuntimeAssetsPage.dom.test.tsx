import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { db } from '@/mocks/db'
import type { Session } from '@/mocks/handlers/domains/auth'
import RuntimeAssetsPage from './RuntimeAssetsPage'

/**
 * RuntimeAssetsPage 强断言（FR-200/FR-053）：①渲染 seed JDK/制品 ②删 JDK→overview 联动减少
 * ③插件制品批量部署到多实例 plugins/ 目录 ④注入 500→错误态。
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
    // mock 必须镜像后端 direct / major 两类绑定语义，并渲染短 chip。
    expect(screen.getByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-proxy')).toBeInTheDocument()
    expect(screen.getByText('直接')).toBeInTheDocument()
    expect(screen.getByText('大版本')).toBeInTheDocument()
    const paperRow = screen.getByText('paper-1.20.4').closest('tr') as HTMLElement
    expect(within(paperRow).getByRole('button', { name: '删除' })).toBeDisabled()
  })

  it('支持制品类型、仅被引用与关键字筛选，并展示 client-file 客户端路径', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('paper-1.20.4')).toBeInTheDocument()
    expect(screen.getByText('ViaVersion')).toBeInTheDocument()
    expect(screen.getByText('lobby-client-config')).toBeInTheDocument()
    expect(screen.getByText(/config\/servers\.json/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'client-file' }))
    expect(screen.queryByText('paper-1.20.4')).not.toBeInTheDocument()
    expect(screen.getByText('lobby-client-config')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '全部' }))
    await user.click(screen.getByLabelText('仅被引用'))
    expect(screen.getByText('paper-1.20.4')).toBeInTheDocument()
    expect(screen.getByText('lobby-client-config')).toBeInTheDocument()
    expect(screen.queryByText('ViaVersion')).not.toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('搜索名称/版本/sha256'), 'paper')
    expect(screen.getByText('paper-1.20.4')).toBeInTheDocument()
    expect(screen.queryByText('lobby-client-config')).not.toBeInTheDocument()
  })

  it('删除一个 JDK 后，overview 联动移除该 JDK', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('17.0.11+9')).toBeInTheDocument()

    // 定位「temurin 17」卡片（JDKCard 用 Panel，data-slot=panel）内的删除按钮，打开危险确认弹窗。
    const card17 = screen.getByText('17.0.11+9').closest('[data-slot="panel"]') as HTMLElement
    await user.click(within(card17).getByRole('button', { name: '删除' }))

    // 危险确认弹窗：seed JDK 17 为 managed（FR-228 按来源分级确认），删除需逐字输入「厂商 主版本」= temurin 17 才启用确认。
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('temurin 17'), 'temurin 17')
    await user.click(within(dialog).getByRole('button', { name: '删除' }))

    // useDeleteRuntimeJDK 失效 ['runtime-assets-overview'] → 重新拉取，handler 现已无 17，DOM 中消失。
    await waitFor(() => expect(screen.queryByText('17.0.11+9')).not.toBeInTheDocument())
    // 另一个 JDK 仍在。
    expect(screen.getByText('21.0.3+9')).toBeInTheDocument()
  })

  it('从插件制品行批量部署到多个实例的 plugins 目录', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('ViaVersion')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '批量部署' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('批量部署插件到实例')).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/ViaVersion/)).toBeChecked()

    const targetSearch = within(dialog).getByPlaceholderText('搜索实例名称')
    await user.type(targetSearch, 'survival-1')
    await user.click(await within(dialog).findByLabelText(/survival-1/))
    await user.clear(targetSearch)
    await user.type(targetSearch, 'lobby-proxy')
    await user.click(await within(dialog).findByLabelText(/lobby-proxy/))
    await user.click(within(dialog).getByRole('button', { name: '部署到 2 个实例' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('1 个插件制品写入 2 个实例')
    await user.click(within(dialog).getByRole('button', { name: '确认部署' }))

    expect(await within(dialog).findByText('结果：成功 2 / 跳过 0 / 失败 0')).toBeInTheDocument()
    const rows = db<{ instanceId: number; name: string; dir: string }>('plugins').list(
      (p) => p.name === 'ViaVersion-5.0.1.jar' && p.dir === 'plugins',
    )
    expect(rows.map((p) => p.instanceId).sort()).toEqual([1, 2])
  })

  it('普通成员访问 overview 被拒绝，不泄露聚合数据', async () => {
    db<Session>('sessions').insert({ accessToken: 'member-token', refreshToken: 'member-refresh', userId: 2 })
    localStorage.setItem('accessToken', 'member-token')
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('加载运行时与制品失败')).toBeInTheDocument()
    expect(screen.queryByText('paper-1.20.4')).not.toBeInTheDocument()
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
  })

  it('注入 500：显示加载失败错误态', async () => {
    loginMockUser()
    mockInject('get', '/runtime-assets/overview', { kind: 'status', status: 500 })
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('加载运行时与制品失败')).toBeInTheDocument()
  })
})
