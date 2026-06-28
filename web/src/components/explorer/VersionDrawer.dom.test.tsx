import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import VersionDrawer from './VersionDrawer'

/**
 * 文件历史版本抽屉强断言（FR-204 文件归档域 / FR-051 文件版本 / FR-070）。
 *
 * 直接渲染 VersionDrawer（open=true），驱动 @/api/fileVersions 的读/写端点：
 * - 读：GET /files/versions 列出种子版本（plugins/config.yml 有一条 #1）；
 * - 写联动：点版本行「回滚」→ DangerConfirm 确认 → POST /files/rollback 写回旧内容并新增一条回滚版本，
 *   列表失效重拉后出现「回滚自 #1」的新版本（证明读 + 写 + 列表联动）；
 * - 读端点 500：注入后抽屉显示「加载版本失败」错误态（不崩溃）。
 *
 * 文件版本端点受 requireAuth 保护，故渲染前 loginMockUser()。
 * VersionDrawer 的回滚 DangerConfirm 未传 scope，故无需提升角色即可确认。
 */

const FILE = 'plugins/config.yml'

/** 渲染打开态的版本抽屉。onOpenChange/onRolledBack 给空实现即可，断言只看抽屉内 DOM。 */
function renderDrawer() {
  return renderWithProviders(
    <VersionDrawer
      instanceId={1}
      filePath={FILE}
      open
      onOpenChange={() => {}}
      onRolledBack={() => {}}
    />,
  )
}

describe('文件历史版本抽屉（mock 假后端，FR-204）', () => {
  it('读出种子版本列表（#1）', async () => {
    loginMockUser()
    renderDrawer()

    // GET /files/versions 返回种子版本 #1，抽屉列表渲染出它。
    expect(await screen.findByText('#1')).toBeInTheDocument()
    // 非空态：不展示「暂无版本」。
    expect(screen.queryByText('暂无版本')).not.toBeInTheDocument()
  })

  it('回滚 #1 → 确认 → 新增「回滚自 #1」版本（写 + 列表联动）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderDrawer()

    // 列表就绪。回滚前没有「回滚自」标记。
    await screen.findByText('#1')
    expect(screen.queryByText(/回滚自/)).not.toBeInTheDocument()

    // 点版本 #1 行内的「回滚」按钮（列表里的那个）。
    const rollbackButtons = screen.getAllByRole('button', { name: '回滚' })
    await user.click(rollbackButtons[0])

    // DangerConfirm 与 Sheet 都是 role=dialog，故用确认弹窗专属标题「回滚文件」定位其容器，
    // 再点该容器内的「回滚」确认按钮（destructive，无 scope 故默认可点）。
    const confirmDialog = (await screen.findByText('回滚文件')).closest('[role="dialog"]') as HTMLElement
    expect(confirmDialog).not.toBeNull()
    const confirm = within(confirmDialog).getByRole('button', { name: '回滚' })
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    // POST /files/rollback 写回旧内容 + 插入一条 rollbackOfVersionId=1 的新版本（#2）；
    // onSuccess 失效版本缓存 → 重拉 → 列表出现「回滚自 #1」与 #2。
    await waitFor(() => expect(screen.getByText(/回滚自/)).toBeInTheDocument())
    expect(screen.getByText('#2')).toBeInTheDocument()
    // 确认弹窗已关闭（「回滚文件」标题消失），但抽屉 Sheet 仍在。
    await waitFor(() => expect(screen.queryByText('回滚文件')).not.toBeInTheDocument())
  })

  it('版本端点注入 500：显示「加载版本失败」错误态（不崩溃）', async () => {
    loginMockUser()
    mockInject('get', '/instances/:id/files/versions', { kind: 'status', status: 500 })
    renderDrawer()

    // versionsQ.error → 抽屉渲染 destructive 错误文案，且不展示种子版本。
    await waitFor(() => expect(screen.getByText('加载版本失败')).toBeInTheDocument())
    expect(screen.queryByText('#1')).not.toBeInTheDocument()
  })
})
