import { describe, it, expect } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { useAuthStore } from '@/stores/auth'
import PluginManager from './PluginManager'

/**
 * PluginManager 强断言（FR-206 插件域）：渲染 seed 插件 / 启用禁用切换联动 / 注入 500 显错误态。
 * seed 插件挂在 instanceId=1（见 mocks/handlers/domains/plugin.ts）。
 */
describe('PluginManager（mock 假后端）', () => {
  it('渲染 seed 插件列表', async () => {
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)
    expect(await screen.findByText('EssentialsX.jar')).toBeInTheDocument()
    expect(screen.getByText('WorldEdit.jar')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '资源包' })).toBeInTheDocument()
    expect(screen.getByText('HighResPack.zip')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '数据包' })).toBeInTheDocument()
    expect(screen.getByText('SpawnTweaks.zip')).toBeInTheDocument()
    expect(screen.getByText('启禁、删除和覆盖类变更通常需要重启实例后生效。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '插件市场' })).toBeDisabled()
  })

  it('禁用写操作 → 该行状态联动（已启用 → 已禁用）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)

    const row = (await screen.findByText('EssentialsX.jar')).closest('tr') as HTMLElement
    expect(within(row).getByText('已启用')).toBeInTheDocument()

    // 点该行「禁用」→ 切换 enabled，列表刷新后状态变「已禁用」。
    await user.click(within(row).getByRole('button', { name: '禁用' }))
    await waitFor(() => {
      const after = screen.getByText('EssentialsX.jar').closest('tr') as HTMLElement
      expect(within(after).getByText('已禁用')).toBeInTheDocument()
    })
  })

  it('从制品库批量部署插件到当前实例并显示汇总', async () => {
    const user = userEvent.setup()
    loginMockUser()
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    renderWithProviders(<PluginManager instanceId={1} />)

    await user.click(await screen.findByRole('button', { name: '批量部署' }))
    const dialog = await screen.findByRole('dialog', { name: '插件批量部署' })
    await user.click(await within(dialog).findByRole('checkbox', { name: '选择插件制品 ViaVersion-5.0.1.jar' }))
    await user.type(within(dialog).getByPlaceholderText('搜索实例'), 'survival-1')
    expect(await within(dialog).findByRole('checkbox', { name: '选择目标实例 survival-1' })).toBeChecked()
    await user.click(within(dialog).getByRole('button', { name: '部署到实例' }))
    const confirm = (await screen.findByText('确认批量部署插件？')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(confirm).getByRole('button', { name: '确认部署' }))

    expect(await within(dialog).findByText('成功 1 · 失败 0 · 跳过 0')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: '插件批量部署' })).not.toBeInTheDocument()
    })
    expect(await screen.findByText('ViaVersion-5.0.1.jar')).toBeInTheDocument()
  })

  it('支持拖拽上传并展示上传进度', async () => {
    let uploaded = false
    server.use(
      http.get(API('/instances/:id/plugins'), () =>
        HttpResponse.json([
          { name: 'EssentialsX.jar', dir: 'plugins', enabled: true, size: 1_048_576, modTime: 1_710_000_000 },
          ...(uploaded
            ? [{ name: 'DropTest.jar', dir: 'plugins', enabled: true, size: 3, modTime: 1_710_400_000 }]
            : []),
        ]),
      ),
      http.post(API('/instances/:id/plugins'), () => {
        uploaded = true
        return HttpResponse.json({ name: 'DropTest.jar', dir: 'plugins', deployed: true }, { status: 201 })
      }),
    )
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)
    await screen.findByText('EssentialsX.jar')
    const dropzone = await screen.findByTestId('plugin-dropzone')

    fireEvent.drop(dropzone, {
      dataTransfer: {
        files: [new File(['jar'], 'DropTest.jar', { type: 'application/java-archive' })],
        types: ['Files'],
      },
    })

    expect(await screen.findByText('DropTest.jar')).toBeInTheDocument()
    expect(screen.queryByText(/上传中/)).not.toBeInTheDocument()
  })

  it('同名文件拖拽上传前要求覆盖确认', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)
    await screen.findByText('EssentialsX.jar')
    const dropzone = await screen.findByTestId('plugin-dropzone')

    fireEvent.drop(dropzone, {
      dataTransfer: {
        files: [new File(['jar'], 'EssentialsX.jar', { type: 'application/java-archive' })],
        types: ['Files'],
      },
    })

    const confirm = (await screen.findByText('同名文件已存在')).closest('[role="dialog"]') as HTMLElement
    expect(confirm).toHaveTextContent('EssentialsX.jar')
    await user.click(within(confirm).getByRole('button', { name: '覆盖上传' }))

    await waitFor(() => {
      expect(screen.queryByText('同名文件已存在')).not.toBeInTheDocument()
    })
  })

  it('mock 上传同名文件且未允许覆盖时返回 409', async () => {
    loginMockUser()
    const form = new FormData()
    form.append('dir', 'plugins')
    form.append('file', 'EssentialsX.jar')

    const resp = await fetch('/api/v1/instances/1/plugins', {
      method: 'POST',
      headers: { Authorization: 'Bearer test-access-token' },
      body: form,
    })
    expect(resp.status).toBe(409)
    await expect(resp.json()).resolves.toMatchObject({ error: 'FILE_EXISTS' })
  })

  it('注入 500 → 显示加载失败错误态', async () => {
    mockInject('get', '/instances/:id/plugins', { kind: 'status', status: 500, body: { message: '插件列表加载失败' } })
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)
    expect(await screen.findByText(/插件列表加载失败|加载失败/)).toBeInTheDocument()
  })
})
