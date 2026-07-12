import { describe, it, expect } from 'vitest'
import { screen, waitFor, within, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import ResourceExplorer from './ResourceExplorer'

/**
 * 文件操作 → 文件列表/树联动强断言（FR-204 文件归档域 / FR-070）。
 *
 * 覆盖 ResourceExplorer.dom.test.tsx 浏览测试之外的写操作：新建、删除、重命名各自经
 * 对应 mock 端点（POST /files/write、DELETE /files、POST /files/rename）改写假后端文件树，
 * refreshAll 重拉后列表随之增删改。删除经 DangerConfirm 二次确认，故先把 auth store 角色提到
 * 组管理员（loginMockUser 只写 session/token，不解出 role；删除确认按钮要求 role>=1）。
 *
 * 降级说明：上传（POST /files/upload，multipart）本应也覆盖，但 Node 内置 undici 的
 * multipartFormDataParser 无法解析 axios 在 jsdom 下产生的 multipart body（request.formData() 抛
 * ERR_ASSERTION），属环境限制、非组件缺陷，且修复需改 mock/源码（超范围）。故改以「新建文件」
 * 覆盖等价的「写操作 → 列表联动」语义（新建即写空内容，POST /files/write 走 JSON 可正常解析）。
 *
 * 用 instanceId=1（files 种子所在实例）。toast 走 sonner 但 harness 未挂 <Toaster>，故只断言列表 DOM 变化。
 */

/** 让删除确认可点：把当前用户角色设为组管理员（DangerConfirm scope=group 要求 role>=1）。 */
function asGroupAdmin() {
  loginMockUser()
  useAuthStore.setState({ role: 1, isAuthenticated: true })
}

/** 取右侧文件列表（ul）。用根目录种子里的 server.properties 行定位其所属 ul。 */
async function fileListEl(): Promise<HTMLElement> {
  const seed = await screen.findByText('server.properties')
  const ul = seed.closest('ul')
  if (!ul) throw new Error('文件列表 ul 未找到')
  return ul as HTMLElement
}

describe('文件操作 → 列表联动（mock 假后端，FR-204）', () => {
  it('新建文件 → 列表出现该文件', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // 等列表就绪（根目录种子已出现），确认目标文件尚不存在。
    const list = await fileListEl()
    expect(within(list).queryByText('created.txt')).not.toBeInTheDocument()

    // Toolbar「新建」下拉 → 「新建文件」→ PromptDialog 输入名字 → 确认（写空内容到假后端）。
    await user.click(screen.getByRole('button', { name: '新建' }))
    await user.click(await screen.findByText('新建文件'))
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox') as HTMLInputElement
    await user.type(input, 'created.txt')
    const confirm = within(dialog).getByRole('button', { name: '确认' })
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    // 写入后 refreshAll 重拉 → 新文件出现在根目录列表。
    await waitFor(() => expect(screen.getByText('created.txt')).toBeInTheDocument())
  })

  it('选中文件 → 删除二次确认 → 列表移除该文件', async () => {
    asGroupAdmin()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    const list = await fileListEl()
    // 勾选 server.properties 行的复选框（aria-label=文件名）。
    const checkbox = within(list).getByLabelText('server.properties')
    await user.click(checkbox)

    // Toolbar「删除」按钮转可用（选中数>0）。点击打开 DangerConfirm。
    const delButtons = screen.getAllByRole('button', { name: '删除' })
    // 取可用的那个（Toolbar 删除按钮；选中后 enabled）。
    const toolbarDelete = delButtons.find((b) => !(b as HTMLButtonElement).disabled)
    expect(toolbarDelete).toBeDefined()
    await user.click(toolbarDelete as HTMLElement)

    // DangerConfirm 弹窗出现，确认按钮（destructive，文案 files.delete=删除）可点（role=1 已满足）。
    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: '删除' })
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    // 删除落到假后端 + refreshAll 重拉 → server.properties 从列表消失。
    await waitFor(() => expect(screen.queryByText('server.properties')).not.toBeInTheDocument())
    // 其它种子仍在（确认是定向删除而非整列清空）。
    expect(screen.getAllByText('plugins').length).toBeGreaterThan(0)
  })

  it('右键重命名文件 → 列表显示新名、旧名消失', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    const list = await fileListEl()
    const row = within(list).getByText('server.properties')

    // Radix ContextMenu：在文件行触发 contextmenu 打开菜单。
    fireEvent.contextMenu(row)
    // 菜单项「重命名」（files.rename）出现在 portal 中。
    const renameItem = await screen.findByText('重命名')
    await user.click(renameItem)

    // PromptDialog 打开，输入框预填旧名；清空后输入新名并确认。
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox') as HTMLInputElement
    await user.clear(input)
    await user.type(input, 'renamed.properties')
    const confirm = within(dialog).getByRole('button', { name: '确认' })
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    // rename 落到假后端 + refreshAll → 新名出现、旧名消失。
    await waitFor(() => expect(screen.getByText('renamed.properties')).toBeInTheDocument())
    expect(screen.queryByText('server.properties')).not.toBeInTheDocument()
  })

  it('删除端点注入 500：删除失败列表保持不变（不崩溃）', async () => {
    asGroupAdmin()
    mockInject('delete', '/instances/:id/files', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    const list = await fileListEl()
    await user.click(within(list).getByLabelText('server.properties'))
    const toolbarDelete = screen
      .getAllByRole('button', { name: '删除' })
      .find((b) => !(b as HTMLButtonElement).disabled)
    await user.click(toolbarDelete as HTMLElement)
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '删除' }))

    // 删除失败（500）：catch 后只 toast，refreshAll 未调用，文件仍在列表（不崩溃）。
    // 等弹窗关闭后断言文件仍在。
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByText('server.properties')).toBeInTheDocument()
  })
})
