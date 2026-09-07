import { describe, it, expect, vi, beforeEach } from 'vitest'
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
import { resetClipboardBusForTests } from './explorer-clipboard-bus'

/**
 * 资源管理器（文件管理器主视图）强断言（FR-204 文件归档域）。
 * 验 mock 假后端 files 集合渲染 + 目录下钻导航联动 + 错误注入。
 * 文件 API 受 requireAuth 保护，故渲染前 loginMockUser() 注入会话 token。
 * 用 instanceId=1（files 种子所在实例）。
 */

describe('ResourceExplorer（mock 假后端）', () => {
  // 剪贴板真源是模块级的实例总线，跨用例残留（如上一例剪切后部分失败保留的条目）会让
  // 「粘贴不可用」这类前置条件失真。每例重置，用例间互不影响。
  beforeEach(() => {
    resetClipboardBusForTests()
  })

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
    // 预览区保留「下载」入口（FR-422 后工具栏的下载仅在有选中时明文出现，此处无选中）。
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
    // FR-422：粘贴收进工具栏「更多操作」下拉（低频动作），故先开下拉再点条目。
    await user.click(screen.getByRole('button', { name: '更多操作' }))
    const pasteItem = await screen.findByRole('menuitem', { name: /粘贴/ })
    expect(pasteItem).not.toHaveAttribute('aria-disabled', 'true')
    await user.click(pasteItem)

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

    // 地址栏跳转。FR-422：默认位展示面包屑，点「编辑路径」才换出可输入的地址栏
    //（同一条横栏内互换，不再面包屑与输入框各占一行）。
    expect(screen.queryByLabelText('地址栏')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '编辑路径' }))
    const addr = screen.getByLabelText('地址栏')
    await user.clear(addr)
    await user.type(addr, 'plugins{Enter}')
    expect(await screen.findByText('config.yml')).toBeInTheDocument()
    // 提交后退回面包屑：路径段可点，且不再有第二份路径 UI。
    expect(screen.queryByLabelText('地址栏')).not.toBeInTheDocument()
    expect(
      within(screen.getByRole('navigation', { name: '路径导航' })).getByRole('button', { name: 'plugins' }),
    ).toBeInTheDocument()

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

  it('FR-422：分段/导航/面包屑/工具/视图同处一条横栏，面包屑只一份', async () => {
    loginMockUser()
    renderWithProviders(
      <ResourceExplorer instanceId={1} toolbarLeading={<button type="button">分段占位</button>} />,
    )
    await screen.findByText('server.properties')

    // 原先四条横栏（分段 / 导航+地址栏+视图 / 面包屑 / 八个平铺按钮）现在同处一个横栏容器。
    const bar = screen.getByTestId('explorer-toolbar')
    expect(within(bar).getByRole('button', { name: '分段占位' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '后退' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '前进' })).toBeInTheDocument()
    expect(within(bar).getByRole('navigation', { name: '路径导航' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '新建' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '上传' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '搜索' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '更多操作' })).toBeInTheDocument()
    expect(within(bar).getByRole('button', { name: '详细信息' })).toBeInTheDocument()

    // 病症之一是「路径输入框里一份、下面再渲染一份」——现在全局只有一份路径 UI。
    expect(screen.getAllByRole('navigation', { name: '路径导航' })).toHaveLength(1)
    expect(screen.queryByLabelText('地址栏')).not.toBeInTheDocument()

    // 目录汇总取自真实列表（种子根目录 3 项 + 唯一文件的字节数），不是写死的装饰。
    expect(within(bar).getByRole('button', { name: '编辑路径' })).toHaveTextContent(/^3 项 · \d+ B$/)
  })

  it('FR-422：收进「更多操作」的动作仍可达、可键盘打开、可触发', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)
    await screen.findByText('server.properties')

    const bar = screen.getByTestId('explorer-toolbar')
    const more = within(bar).getByRole('button', { name: '更多操作' })

    // 键盘可达：聚焦下拉触发器后回车即展开（Radix DropdownMenu 语义）。
    more.focus()
    await user.keyboard('{Enter}')
    const menu = await screen.findByRole('menu')

    // 收进下拉 ≠ 删掉：五个低频动作全在，且 disabled 语义与原平铺按钮逐一对应
    //（无选中 → 下载/删除/清空选择 不可用；剪贴板空 → 粘贴不可用）。
    expect(within(menu).getByRole('menuitem', { name: /下载/ })).toHaveAttribute('aria-disabled', 'true')
    expect(within(menu).getByRole('menuitem', { name: /删除/ })).toHaveAttribute('aria-disabled', 'true')
    expect(within(menu).getByRole('menuitem', { name: /粘贴/ })).toHaveAttribute('aria-disabled', 'true')
    expect(within(menu).getByRole('menuitem', { name: /清空选择/ })).toHaveAttribute('aria-disabled', 'true')

    // 可触发：下拉里的「全选」真的选中全部条目。
    await user.click(within(menu).getByRole('menuitem', { name: /全选/ }))
    await waitFor(() => expect(screen.getByRole('checkbox', { name: 'server.properties' })).toBeChecked())
    expect(screen.getByRole('checkbox', { name: 'plugins' })).toBeChecked()

    // 有选中后下载/删除提升为明文按钮（此时它们是用户的下一步动作，不该藏起来）。
    expect(within(bar).getByRole('button', { name: '下载' })).toBeEnabled()
    expect(within(bar).getByRole('button', { name: '删除' })).toBeEnabled()

    // 「清空选择」转可用，且从下拉里点得动。
    await user.click(within(bar).getByRole('button', { name: '更多操作' }))
    const clear = within(await screen.findByRole('menu')).getByRole('menuitem', { name: /清空选择/ })
    expect(clear).not.toHaveAttribute('aria-disabled', 'true')
    await user.click(clear)
    await waitFor(() =>
      expect(screen.getByRole('checkbox', { name: 'server.properties' })).not.toBeChecked(),
    )
    // 选中清空后下载/删除退回下拉，明文位不再有永久 disabled 的噪声按钮。
    expect(within(bar).queryByRole('button', { name: '下载' })).not.toBeInTheDocument()
    expect(within(bar).queryByRole('button', { name: '删除' })).not.toBeInTheDocument()
  })

  it('FR-422：目录汇总不造数——加载失败时不显示项数与大小', async () => {
    loginMockUser()
    mockInject('get', '/instances/:id/files', { kind: 'status', status: 500 })
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    await waitFor(() => expect(screen.getByText('注入的模拟错误')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: '编辑路径' })).toHaveTextContent('')
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
