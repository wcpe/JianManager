import { describe, it, expect, vi } from 'vitest'
import { useEffect } from 'react'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@jianmanager/devmock/inject'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ResourceExplorer, { type ConfigCapabilities } from './ResourceExplorer'

/**
 * 资源管理器（文件管理器主视图）强断言（FR-204 文件归档域）。
 * 验 mock 假后端 files 集合渲染 + 目录下钻导航联动 + 错误注入。
 * 文件 API 受 requireAuth 保护，故渲染前 loginMockUser() 注入会话 token。
 * 用 instanceId=1（files 种子所在实例）。
 */

describe('ResourceExplorer（mock 假后端）', () => {
  it('渲染工作目录种子：根级文件与目录', async () => {
    loginMockUser()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // 根目录列出种子：文件 server.properties（仅右列表有）+ 目录 plugins/world（左树与右列表各一份）。
    expect(await screen.findByText('server.properties')).toBeInTheDocument()
    expect(screen.getAllByText('world').length).toBeGreaterThan(0)
    expect(screen.getAllByText('plugins').length).toBeGreaterThan(0)
  })

  it('下钻目录：双击 plugins 反映其子项', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // 双击右侧列表里的 plugins 目录行 → 导航进入 → 列出 plugins 下子项。
    const pluginsRows = await screen.findAllByText('plugins')
    // 取列表里的那个（可点击的文件行 span）。最后一个通常为列表项；逐个尝试双击直到出现子项。
    for (const row of pluginsRows) {
      await user.dblClick(row)
    }
    expect(await screen.findByText('config.yml')).toBeInTheDocument()
    expect(screen.getByText('Essentials.jar')).toBeInTheDocument()
  })

  it('目录树支持 role=tree 与键盘下钻', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    const tree = await screen.findByRole('tree', { name: '文件目录树' })
    const root = within(tree).getByRole('treeitem', { name: '/' })
    root.focus()

    await user.keyboard('{ArrowDown}')
    const plugins = within(tree).getByRole('treeitem', { name: /plugins/ })
    expect(plugins).toHaveFocus()

    await user.keyboard('{Enter}')
    await waitFor(() => expect(plugins).toHaveAttribute('aria-selected', 'true'))
    expect(await screen.findByText('config.yml')).toBeInTheDocument()
  })

  it('大目录树只渲染可视窗口', async () => {
    loginMockUser()
    server.use(
      http.get(API('/instances/:id/files'), ({ request }) => {
        const path = new URL(request.url).searchParams.get('path') ?? ''
        if (path !== '') return HttpResponse.json([])
        return HttpResponse.json(
          Array.from({ length: 300 }, (_, index) => ({
            name: `dir-${String(index).padStart(3, '0')}`,
            isDir: true,
            size: 0,
            modTime: 1_700_000_000,
          })),
        )
      }),
    )
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    const tree = await screen.findByRole('tree', { name: '文件目录树' })
    expect(await within(tree).findByRole('treeitem', { name: /dir-000/ })).toBeInTheDocument()
    expect(within(tree).getAllByRole('treeitem').length).toBeLessThan(80)
    expect(within(tree).queryByRole('treeitem', { name: /dir-299/ })).not.toBeInTheDocument()
  })

  it('二进制与超大文件不触发文本读取并保留下载入口', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const readSpy = vi.fn()
    server.use(
      http.get(API('/instances/:id/files'), () =>
        HttpResponse.json([
          { name: 'large.log', isDir: false, size: 2 * 1024 * 1024, modTime: 1_700_000_000 },
          { name: 'icon.png', isDir: false, size: 128 * 1024, modTime: 1_700_000_000 },
        ]),
      ),
      http.get(API('/instances/:id/files/read'), () => {
        readSpy()
        return HttpResponse.text('不应读取')
      }),
    )
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    await user.dblClick(await screen.findByText('large.log'))
    expect(await screen.findByText(/文件过大/)).toBeInTheDocument()
    // 预览区与工具栏各有「下载」：断言至少存在可点的预览下载入口
    expect(screen.getAllByRole('button', { name: '下载' }).length).toBeGreaterThanOrEqual(1)

    await user.dblClick(await screen.findByText('icon.png'))
    expect(await screen.findByText(/二进制文件/)).toBeInTheDocument()
    expect(readSpy).not.toHaveBeenCalled()
  })

  it('粘贴移动串行执行并展示部分失败明细', async () => {
    loginMockUser()
    const user = userEvent.setup()
    server.use(
      http.post(API('/instances/:id/files/rename'), async ({ request }) => {
        const body = await request.json() as { oldPath: string; newPath: string }
        if (body.oldPath === 'world') {
          return HttpResponse.json({ error: 'MOVE_FAILED', message: 'world 被锁定' }, { status: 500 })
        }
        return HttpResponse.json({ ok: true })
      }),
    )
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    await screen.findByText('server.properties')
    await user.click(screen.getByRole('checkbox', { name: 'server.properties' }))
    await user.click(screen.getByRole('checkbox', { name: 'world' }))

    const serverRow = screen.getByText('server.properties').closest('li') as HTMLElement
    await user.pointer({ keys: '[MouseRight]', target: serverRow })
    await user.click(await screen.findByRole('menuitem', { name: /剪切/ }))

    for (const plugins of screen.getAllByText('plugins')) {
      await user.dblClick(plugins)
    }
    expect(await screen.findByText('config.yml')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /粘贴/ }))

    const status = await screen.findByRole('status')
    await waitFor(() => expect(status).toHaveTextContent('已完成 2/2'))
    expect(status).toHaveTextContent('失败 1')
    expect(status).toHaveTextContent('world 被锁定')
  })

  it('配置编辑器未保存关闭时使用共享 Dialog 确认', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const config: ConfigCapabilities = {
      renderEditor: ({ onDirtyChange, onClose }) => <DirtyConfigEditor onDirtyChange={onDirtyChange} onClose={onClose} />,
      renderVersionDrawer: () => null,
    }
    renderWithProviders(<ResourceExplorer instanceId={1} config={config} />)

    await user.dblClick(await screen.findByText('server.properties'))
    await user.click(await screen.findByRole('button', { name: '关闭配置' }))

    const dialog = await screen.findByRole('dialog', { name: '有未保存的修改' })
    expect(within(dialog).getByText('有未保存的修改，确定放弃并继续？')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    expect(screen.getByRole('button', { name: '关闭配置' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '关闭配置' }))
    await user.click(within(await screen.findByRole('dialog', { name: '有未保存的修改' })).getByRole('button', { name: '确认' }))

    await waitFor(() => expect(screen.queryByRole('button', { name: '关闭配置' })).not.toBeInTheDocument())
  })

  it('注入 500：目录加载失败显示错误态（不崩溃）', async () => {
    loginMockUser()
    mockInject('get', '/instances/:id/files', { kind: 'status', status: 500 })
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // FileList 把加载错误渲染为 destructive 文案；注入默认 body.message = "注入的模拟错误"。
    await waitFor(() => expect(screen.getByText('注入的模拟错误')).toBeInTheDocument())
    // 列表未渲染出种子文件（确认是错误态而非正常态）。
    expect(screen.queryByText('server.properties')).not.toBeInTheDocument()
  })

  it('FR-375：地址栏 / 后退前进 / 三视图 / 权限列', async () => {
    loginMockUser()
    const user = userEvent.setup()
    server.use(
      http.get(API('/instances/:id/files'), ({ request }) => {
        const path = new URL(request.url).searchParams.get('path') ?? ''
        if (path === 'plugins') {
          return HttpResponse.json([
            {
              name: 'config.yml',
              isDir: false,
              size: 128,
              modTime: 1_700_000_000,
              modeString: 'rw-r--r--',
              writable: true,
              readable: true,
            },
          ])
        }
        return HttpResponse.json([
          {
            name: 'plugins',
            isDir: true,
            size: 0,
            modTime: 1_700_000_000,
            modeString: 'rwxr-xr-x',
            writable: true,
            readable: true,
          },
          {
            name: 'locked.txt',
            isDir: false,
            size: 10,
            modTime: 1_700_000_001,
            modeString: 'r--r--r--',
            writable: false,
            readable: true,
          },
          {
            name: 'server.properties',
            isDir: false,
            size: 100,
            modTime: 1_700_000_002,
            modeString: 'rw-r--r--',
            writable: true,
            readable: true,
          },
        ])
      }),
    )
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    expect(await screen.findByTestId('resource-explorer')).toBeInTheDocument()
    const root = screen.getByTestId('resource-explorer')
    expect(root.className).toMatch(/overflow-hidden/)
    expect(root.className).toMatch(/h-full|min-h/)

    // 权限列与只读锁标
    expect(await screen.findByText('权限')).toBeInTheDocument()
    expect(await screen.findByText('locked.txt')).toBeInTheDocument()
    expect(screen.getByTitle('不可写')).toBeInTheDocument()

    // 地址栏跳转
    const addr = screen.getByLabelText('地址栏')
    await user.clear(addr)
    await user.type(addr, 'plugins{Enter}')
    expect(await screen.findByText('config.yml')).toBeInTheDocument()

    // 后退回根
    const back = screen.getByRole('button', { name: '后退' })
    expect(back).not.toBeDisabled()
    await user.click(back)
    expect(await screen.findByText('locked.txt')).toBeInTheDocument()

    // 三视图切换
    await user.click(screen.getByRole('button', { name: '大图标' }))
    expect(screen.getByRole('button', { name: '大图标' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: '列表' }))
    expect(screen.getByRole('button', { name: '列表' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: '详细信息' }))
    expect(screen.getByRole('button', { name: '详细信息' })).toHaveAttribute('aria-pressed', 'true')
  })
})

function DirtyConfigEditor({
  onDirtyChange,
  onClose,
}: {
  onDirtyChange: (dirty: boolean) => void
  onClose: () => void
}) {
  useEffect(() => {
    onDirtyChange(true)
    return () => onDirtyChange(false)
  }, [onDirtyChange])

  return <button onClick={onClose}>关闭配置</button>
}
