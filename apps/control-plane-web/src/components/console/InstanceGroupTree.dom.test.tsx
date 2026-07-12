import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { InstanceGroupTree } from './InstanceGroupTree'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useConsoleStore } from '@/stores/console'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'

function renderTree(selectedGroupId: number | null = null, onSelect = vi.fn()) {
  loginMockUser()
  useConsoleStore.setState({ collapsedGroups: {} })
  renderWithProviders(<InstanceGroupTree selectedGroupId={selectedGroupId} onSelect={onSelect} />)
  return { onSelect }
}

describe('InstanceGroupTree', () => {
  it('按名称搜索分组，并展开匹配分支上下文', async () => {
    const user = userEvent.setup()
    renderTree()

    expect(await screen.findByRole('treeitem', { name: /生存/ })).toBeInTheDocument()
    await user.type(screen.getByRole('textbox', { name: '搜索分组' }), '创造')

    expect(screen.getByRole('treeitem', { name: /创造/ })).toBeInTheDocument()
    expect(screen.getByRole('treeitem', { name: /亚洲区/ })).toBeInTheDocument()
    expect(screen.queryByRole('treeitem', { name: /生存/ })).not.toBeInTheDocument()
  })

  it('暴露树与选中态 a11y 属性', async () => {
    renderTree(2)

    const tree = await screen.findByRole('tree', { name: '分组树' })
    expect(within(tree).getByRole('treeitem', { name: /全部实例/ })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    expect(await screen.findByRole('treeitem', { name: /生存/ })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('treeitem', { name: /亚洲区/ })).toHaveAttribute('aria-expanded', 'true')
  })

  it('支持键盘折叠和选择分组', async () => {
    const user = userEvent.setup()
    const { onSelect } = renderTree()

    const asia = await screen.findByRole('treeitem', { name: /亚洲区/ })
    asia.focus()
    await user.keyboard(' ')

    await waitFor(() => expect(asia).toHaveAttribute('aria-expanded', 'false'))
    expect(screen.queryByRole('treeitem', { name: /生存/ })).not.toBeInTheDocument()

    await user.keyboard('{Enter}')
    expect(onSelect).toHaveBeenCalledWith(1)
  })

  it('大数据分组只渲染可视窗口', async () => {
    server.use(
      http.get(API('/instance-groups'), () =>
        HttpResponse.json(
          Array.from({ length: 300 }, (_, index) => ({
            id: index + 1,
            uuid: `g-${index + 1}`,
            name: `分组-${String(index).padStart(3, '0')}`,
            parentId: null,
            sort: index,
            instanceCount: 0,
          })),
        ),
      ),
    )
    renderTree()

    const tree = await screen.findByRole('tree', { name: '分组树' })
    expect(await screen.findByRole('treeitem', { name: /分组-000/ })).toBeInTheDocument()
    expect(within(tree).getAllByRole('treeitem').length).toBeLessThan(80)
    expect(screen.queryByRole('treeitem', { name: /分组-299/ })).not.toBeInTheDocument()
  })

  it('空态提供创建第一个分组 CTA', async () => {
    const user = userEvent.setup()
    server.use(http.get(API('/instance-groups'), () => HttpResponse.json([])))
    renderTree()

    expect(await screen.findByText('暂无分组')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '创建第一个分组' }))

    expect(screen.getByRole('dialog', { name: '新建根分组' })).toBeInTheDocument()
  })
})
