import type { ComponentProps } from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import ConfigVersionDrawer from './ConfigVersionDrawer'

/**
 * ConfigVersionDrawer 强断言（FR-205 配置浏览器版本/diff/回滚联动）：复用 FR-031 配置版本端点
 * （versions / diff / rollback）。聚焦此抽屉子组件——只命中 configs 域三端点，避开 ResourceExplorer
 * 的文件域跨端点噪声。
 *
 * 种子（mocks/handlers/domains/config.ts）：server.properties 预置 2 个版本（#1 初始化、#2 改 motd）。
 */

function renderDrawer(overrides: Partial<ComponentProps<typeof ConfigVersionDrawer>> = {}) {
  const onOpenChange = vi.fn()
  const onRolledBack = vi.fn()
  const utils = renderWithProviders(
    <ConfigVersionDrawer
      instanceId={1}
      filePath="server.properties"
      open
      onOpenChange={onOpenChange}
      onRolledBack={onRolledBack}
      {...overrides}
    />,
  )
  return { ...utils, onOpenChange, onRolledBack }
}

describe('ConfigVersionDrawer 版本/diff/回滚（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser() // /configs/versions|diff|rollback 经 requireAuth，需有效 session
  })

  it('渲染种子：列出 server.properties 历史版本（#1 与 #2，倒序，带提交说明）', async () => {
    renderDrawer()
    // versions 端点按 ID 倒序：#2 在前、#1 在后。
    expect(await screen.findByText('#2')).toBeInTheDocument()
    expect(screen.getByText('#1')).toBeInTheDocument()
    // 版本提交说明（种子 message）一并呈现。
    expect(screen.getByText('改 motd 与玩家上限')).toBeInTheDocument()
    expect(screen.getByText('初始化配置')).toBeInTheDocument()
  })

  it('交互：选「从 #1 → 到 #2」→ diff 端点联动，差异块可见（含版本号标记）', async () => {
    const user = userEvent.setup()
    renderDrawer()
    await screen.findByText('#2')
    // 在 #1 行点「从」，在 #2 行点「到」，触发 useConfigDiff（两端不同才启用）。
    const row1 = screen.getByText('#1').closest('li') as HTMLElement
    const row2 = screen.getByText('#2').closest('li') as HTMLElement
    await user.click(within(row1).getByRole('button', { name: '从' }))
    await user.click(within(row2).getByRole('button', { name: '到' }))
    // unifiedDiff 文本块渲染（mock 把两版内容拼成含 "--- #1" / "+++ #2" 标记的 unified diff）。
    // 整段 diff 在一个 <pre> 文本节点内，按子串匹配该节点即可。
    const pre = await screen.findByText(/--- #1/)
    expect(pre).toBeInTheDocument()
    expect(pre.textContent).toContain('+++ #2')
  })

  it('交互：回滚 #1 → 确认弹窗 → rollback 成功回调 onRolledBack（新版本联动）', async () => {
    const user = userEvent.setup()
    const { onRolledBack } = renderDrawer()
    await screen.findByText('#1')
    const row1 = screen.getByText('#1').closest('li') as HTMLElement
    // 点该版本「回滚」→ 打开 DangerConfirm（无 scope/confirmText，确认按钮即时可用）。
    await user.click(within(row1).getByRole('button', { name: '回滚' }))
    // Sheet 与 DangerConfirm 都是 Radix Dialog（同 role=dialog），按确认弹窗标题定位其面板。
    const dialogTitle = await screen.findByText('回滚配置版本')
    const dialog = dialogTitle.closest('[role="dialog"]') as HTMLElement
    // 弹窗内的确认按钮文案 = configExplorer.rollback「回滚」。
    await user.click(within(dialog).getByRole('button', { name: '回滚' }))
    // rollback 端点成功后调用 onRolledBack（mock 自增生成 #3 并回写文件）。
    await waitFor(() => expect(onRolledBack).toHaveBeenCalled())
  })

  it('注入 500（GET configs/versions）→ 显示「加载版本失败」错误态而非崩溃', async () => {
    mockInject('get', '/instances/:id/configs/versions/:file', { kind: 'status', status: 500 })
    renderDrawer()
    expect(await screen.findByText('加载版本失败')).toBeInTheDocument()
  })

  it('open=false 时不拉版本（filePath 置空，列表不渲染版本项）', async () => {
    renderDrawer({ open: false })
    // open=false → useConfigVersions 传 null → 不发请求；#2 不应出现。
    await waitFor(() => expect(screen.queryByText('#2')).not.toBeInTheDocument())
  })
})
