import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { InstanceGroupTree, INSTANCE_DND_MIME } from './InstanceGroupTree'
import type { InstanceGroupNode } from '@jianmanager/ui/lib/instance-group'

/**
 * FR-165 分组树 · 受控视图测（ADR-097 b 范式）。
 *
 * 应用侧已有 dom 测试（覆盖接线层 + msw）与纯逻辑测试；这里补的是**受控语义**：
 * 四个写动作的上报、失败提示的语义分流（建组/改名/非空拒删）、拖放解析与新增数提示。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', confirm: '确认', delete: '删除', loading: '加载中...' },
        instanceGroups: {
          treeTitle: '分组树',
          allInstances: '全部实例',
          newRoot: '新建分组',
          newChild: '新建子分组',
          rename: '重命名',
          toggle: '展开/折叠',
          searchLabel: '搜索分组',
          searchPlaceholder: '搜索分组名称',
          empty: '暂无分组',
          emptyHint: '创建第一个分组后，可以从右侧拖入实例或批量标记入组。',
          emptyCta: '创建第一个分组',
          noSearchResults: '没有匹配的分组',
          namePlaceholder: '输入分组名称',
          createRootTitle: '新建根分组',
          createChildTitle: '在「{{parent}}」下新建子分组',
          renameTitle: '重命名分组',
          createFailed: '创建分组失败',
          renameFailed: '重命名失败',
          deleted: '分组已删除',
          deleteNotEmpty: '分组非空，请先清空其子分组与成员实例',
          markedCount: '已标记 {{count}} 个实例入组',
          markFailed: '标记入组失败',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof InstanceGroupTree>[0]

function node(over: Partial<InstanceGroupNode> = {}): InstanceGroupNode {
  return {
    id: 1,
    uuid: 'g-1',
    name: '生产大区',
    parentId: null,
    sort: 0,
    instanceCount: 3,
    ...over,
  }
}

const groups = [
  node(),
  node({ id: 2, uuid: 'g-2', name: '小区 A', parentId: 1, instanceCount: 2 }),
]

function renderTree(props: Partial<Props> = {}) {
  const merged = {
    selectedGroupId: null,
    onSelect: vi.fn(),
    groups,
    collapsedGroups: {},
    onToggleCollapsed: vi.fn(),
    onCreate: vi.fn<Props['onCreate']>().mockResolvedValue(undefined),
    onRename: vi.fn<Props['onRename']>().mockResolvedValue(undefined),
    onDelete: vi.fn<Props['onDelete']>().mockResolvedValue(undefined),
    onDropInstances: vi.fn<Props['onDropInstances']>().mockResolvedValue({ added: 2 }),
    notify: vi.fn<Props['notify']>(),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<InstanceGroupTree {...merged} />, { wrapper: Wrapper })
  return merged
}

describe('InstanceGroupTree（FR-165 · ADR-097 b 范式）', () => {
  it('渲染「全部实例」根行与各分组行（含子树计数）', () => {
    renderTree()

    expect(screen.getByRole('treeitem', { name: /全部实例/ })).toBeInTheDocument()
    expect(screen.getByText('生产大区')).toBeInTheDocument()
    expect(screen.getByText('小区 A')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('点根行把选中清为 null', async () => {
    const user = userEvent.setup()
    const { onSelect } = renderTree({ selectedGroupId: 1 })

    await user.click(screen.getByRole('treeitem', { name: /全部实例/ }))
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('新建根分组：对话框 → 提交上报', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderTree()

    await user.click(screen.getByRole('button', { name: /新建分组/ }))
    expect(screen.getByText('新建根分组')).toBeInTheDocument()

    // 名称为空时不可提交。
    const confirm = screen.getByRole('button', { name: '确认' })
    expect(confirm).toBeDisabled()

    await user.type(screen.getByPlaceholderText('输入分组名称'), '  新区  ')
    await user.click(confirm)

    await waitFor(() => expect(onCreate).toHaveBeenCalledWith({ name: '新区' }))
  })

  it('建子分组：标题带出父组名，载荷带 parentId', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderTree()

    await user.click(screen.getAllByRole('button', { name: '新建子分组' })[0])
    expect(screen.getByText('在「生产大区」下新建子分组')).toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('输入分组名称'), '小区 B')
    await user.click(screen.getByRole('button', { name: '确认' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledWith({ name: '小区 B', parentId: 1 }))
  })

  it('改名：初值回填，提交走改名动作', async () => {
    const user = userEvent.setup()
    const { onRename, onCreate } = renderTree()

    await user.click(screen.getAllByRole('button', { name: '重命名' })[1])
    expect(screen.getByText('重命名分组')).toBeInTheDocument()
    expect(screen.getByDisplayValue('小区 A')).toBeInTheDocument()

    await user.clear(screen.getByDisplayValue('小区 A'))
    await user.type(screen.getByPlaceholderText('输入分组名称'), '小区 A2')
    await user.click(screen.getByRole('button', { name: '确认' }))

    await waitFor(() => expect(onRename).toHaveBeenCalledWith({ id: 2, name: '小区 A2' }))
    expect(onCreate).not.toHaveBeenCalled()
  })

  it('建组失败：提示创建失败且不关窗', async () => {
    const user = userEvent.setup()
    const { notify } = renderTree({ onCreate: vi.fn<Props['onCreate']>().mockRejectedValue(new Error('x')) })

    await user.click(screen.getByRole('button', { name: /新建分组/ }))
    await user.type(screen.getByPlaceholderText('输入分组名称'), '新区')
    await user.click(screen.getByRole('button', { name: '确认' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '创建分组失败'))
    expect(screen.getByText('新建根分组')).toBeInTheDocument()
  })

  it('删除失败（非空组）提示先清空，而不是笼统失败', async () => {
    const user = userEvent.setup()
    const { notify } = renderTree({ onDelete: vi.fn<Props['onDelete']>().mockRejectedValue(new Error('409')) })

    await user.click(screen.getAllByRole('button', { name: '删除' })[0])

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '分组非空，请先清空其子分组与成员实例'))
  })

  it('删除成功：提示已删除；删的是当前选中组时清空选中', async () => {
    const user = userEvent.setup()
    const { notify, onSelect } = renderTree({ selectedGroupId: 1 })

    await user.click(screen.getAllByRole('button', { name: '删除' })[0])

    await waitFor(() => expect(notify).toHaveBeenCalledWith('success', '分组已删除'))
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('拖实例入组：解析载荷、上报、提示新增数', async () => {
    const { onDropInstances, notify } = renderTree()

    const target = screen.getByRole('treeitem', { name: /生产大区/ })
    const dataTransfer = {
      types: [INSTANCE_DND_MIME],
      getData: () => JSON.stringify([101, 102]),
      dropEffect: '',
    }
    const { fireEvent } = await import('@testing-library/react')
    fireEvent.drop(target, { dataTransfer })

    await waitFor(() => expect(onDropInstances).toHaveBeenCalledWith({ groupId: 1, instanceIds: [101, 102] }))
    expect(notify).toHaveBeenCalledWith('success', '已标记 2 个实例入组')
  })

  it('搜索无结果时给出「没有匹配的分组」', async () => {
    const user = userEvent.setup()
    renderTree()

    await user.type(screen.getByPlaceholderText('搜索分组名称'), 'zzz')
    expect(screen.getByText('没有匹配的分组')).toBeInTheDocument()
  })
})
