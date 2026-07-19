import { describe, it, expect } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import { server } from '@jianmanager/devmock/server'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
import RuntimeAssetsPage from './RuntimeAssetsPage'

function trackReconcilePosts() {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    if (request.method === 'POST') paths.push(new URL(request.url).pathname)
  }
  server.events.on('request:start', listener)
  return { paths, stop: () => server.events.removeListener('request:start', listener) }
}

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

  it('client-file 表格展示存储位置与对账状态，lost 制品红标并提示自愈', async () => {
    loginMockUser()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('lost-client-pack')).toBeInTheDocument()
    expect(screen.getByText('存储位置')).toBeInTheDocument()
    expect(screen.getByText('对账状态')).toBeInTheDocument()

    const localRow = screen.getByText('lobby-client-config').closest('tr') as HTMLElement
    expect(within(localRow).getByText('本机')).toBeInTheDocument()
    expect(within(localRow).getByText('正常')).toBeInTheDocument()

    const lostRow = screen.getByText('lost-client-pack').closest('tr') as HTMLElement
    expect(within(lostRow).getByText('rustfs-主渠道')).toBeInTheDocument()
    expect(within(lostRow).getAllByText('失效')).toHaveLength(2)
    expect(within(lostRow).getByTitle('外置对象已缺失，重传同内容文件即可自愈')).toBeInTheDocument()
  })

  it('展示运行记录与报告，两个危险操作确认后才发送处置请求', async () => {
    loginMockUser()
    const tracker = trackReconcilePosts()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    expect(await screen.findByText('制品存储对账')).toBeInTheDocument()
    const viewReport = await screen.findByRole('button', { name: '查看报告' })
    expect(screen.getByText('手动')).toBeInTheDocument()
    await user.click(viewReport)

    const report = await screen.findByRole('dialog', { name: /对账报告/ })
    expect(within(report).getByText(/索引 3 · 对象 3 · 一致 1/)).toBeInTheDocument()
    expect(within(report).getByText(/var\/artifacts\/client-file\/99/)).toBeInTheDocument()
    expect(within(report).getByText('var/artifacts/client-file/ff/orphan-pack.zip')).toBeInTheDocument()

    expect(tracker.paths).not.toContain('/api/v1/artifact-reconcile/runs/1/resolve-missing')
    await user.click(within(report).getByRole('button', { name: '全部标记失效' }))
    const markDialog = await screen.findByRole('dialog', { name: '确认标记缺失制品为失效？' })
    expect(tracker.paths).not.toContain('/api/v1/artifact-reconcile/runs/1/resolve-missing')
    await user.click(within(markDialog).getByRole('button', { name: '全部标记失效' }))
    await waitFor(() => expect(tracker.paths).toContain('/api/v1/artifact-reconcile/runs/1/resolve-missing'))

    await user.click(within(report).getByRole('button', { name: '清理全部孤儿' }))
    const cleanupDialog = await screen.findByRole('dialog', { name: '确认清理全部孤儿对象？' })
    expect(tracker.paths).not.toContain('/api/v1/artifact-reconcile/runs/1/cleanup-orphans')
    await user.click(within(cleanupDialog).getByRole('button', { name: '清理全部孤儿' }))
    await waitFor(() => expect(tracker.paths).toContain('/api/v1/artifact-reconcile/runs/1/cleanup-orphans'))
    tracker.stop()
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
    expect(within(dialog).getByText(/实例 #1 \/ 资产 #\d+: 已部署/)).toBeInTheDocument()
    expect(within(dialog).getByText(/实例 #2 \/ 资产 #\d+: 已部署/)).toBeInTheDocument()
    const rows = db<{ instanceId: number; name: string; dir: string }>('plugins').list(
      (p) => p.name === 'ViaVersion-5.0.1.jar' && p.dir === 'plugins',
    )
    expect(rows.map((p) => p.instanceId).sort()).toEqual([1, 2])
  })

  it('导入制品：选类型+文件上传后新制品出现在列表，弹窗展示进度条', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<RuntimeAssetsPage />)

    // 打开「导入制品」弹窗（制品区标题栏按钮）。
    await user.click(await screen.findByRole('button', { name: '导入制品' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('导入制品到制品库')).toBeInTheDocument()

    // 选类型 plugin + 命名 + 选文件（命名保证断言不依赖 jsdom 对 File 名的处理）。
    await user.selectOptions(within(dialog).getByLabelText('类型'), 'plugin')
    await user.type(within(dialog).getByLabelText('名称（可选）'), 'MyImportedPlugin')
    const fileInput = within(dialog).getByLabelText('文件') as HTMLInputElement
    await user.upload(fileInput, new File(['jarbytes'], 'MyImportedPlugin-1.0.jar', { type: 'application/java-archive' }))

    // 上传前进度条不显示；提交后（上传期间）进度区（role=status）出现，证明导入期间有进度反馈。
    expect(screen.queryByRole('status', { name: '上传进度' })).not.toBeInTheDocument()
    // submit 同步先置 0% 进度；用 fireEvent 在请求完成前观察该瞬时状态，避免 await user.click 吞掉整个 mock 请求周期。
    fireEvent.click(within(dialog).getByRole('button', { name: '导入制品' }))
    expect(screen.getByRole('status', { name: '上传进度' })).toBeInTheDocument()

    // 成功后弹窗关闭，overview 联动刷新，新制品名出现在列表。
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText('MyImportedPlugin')).toBeInTheDocument()
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
