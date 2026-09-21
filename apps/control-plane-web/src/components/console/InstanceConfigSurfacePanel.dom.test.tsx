import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import InstanceConfigSurfacePanel from './InstanceConfigSurfacePanel'

/** 渲染实例 1 的关键配置面板（假后端种子：query.port 内联、motd 文件引用）。 */
function renderPanel() {
  loginMockUser()
  const user = userEvent.setup()
  renderWithProviders(<InstanceConfigSurfacePanel instanceId={1} />)
  return { user }
}

describe('InstanceConfigSurfacePanel（FR-451 配置源明面化）', () => {
  it('明面展示受管项：内联项可编辑、文件引用项只读展示生效值', async () => {
    renderPanel()

    const inlineRow = await screen.findByTestId('config-surface-row-props.query.port')
    expect(within(inlineRow).getByLabelText('Query 监听端口')).toHaveValue('25566')

    const fileRow = screen.getByTestId('config-surface-row-props.motd')
    // file 项只读展示解析生效值，且不提供内联输入。
    expect(within(fileRow).getByText('A Mock Minecraft Server')).toBeInTheDocument()
    expect(within(fileRow).queryByLabelText('服务器描述')).toBeNull()
  })

  it('内联改值并保存：提交后刷新呈现新值', async () => {
    const { user } = renderPanel()

    const input = within(await screen.findByTestId('config-surface-row-props.query.port')).getByLabelText('Query 监听端口')
    await user.clear(input)
    await user.type(input, '25577')
    await user.click(screen.getByTestId('config-surface-save'))

    await waitFor(() =>
      expect(within(screen.getByTestId('config-surface-row-props.query.port')).getByLabelText('Query 监听端口')).toHaveValue('25577'),
    )
  })

  it('把文件引用项切为内联并提交 source=inline（以文件现值为初值，不清空）', async () => {
    const { user } = renderPanel()
    let captured: unknown = null
    server.use(
      http.put(API('/instances/1/configs/surface'), async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ items: [] })
      }),
    )

    const row = await screen.findByTestId('config-surface-row-props.motd')
    // 文件中立开关：点「内联」后该行出现可编辑输入。
    await user.click(within(row).getByRole('button', { name: '内联' }))
    const input = within(row).getByLabelText('服务器描述')
    expect(input).toBeInTheDocument()
    // file→inline 以文件当前生效值预填，避免破坏性写入空值。
    expect(input).toHaveValue('A Mock Minecraft Server')

    await user.click(screen.getByTestId('config-surface-save'))
    await waitFor(() =>
      expect(captured).toEqual({
        items: [{ itemKey: 'props.motd', source: 'inline', inlineValue: 'A Mock Minecraft Server' }],
      }),
    )
  })

  it('无改动时保存按钮禁用', async () => {
    renderPanel()
    await screen.findByTestId('config-surface-panel')
    expect(screen.getByTestId('config-surface-save')).toBeDisabled()
  })

  it('文件现值不可用（读取失败）时切内联并保存不发送 inlineValue（交后端迁移兜底，避免破坏性写入）', async () => {
    server.use(
      http.get(API('/instances/1/configs/surface'), () =>
        HttpResponse.json({
          items: [
            {
              itemKey: 'props.motd', source: 'file', registered: true, filePath: 'server.properties',
              fileKey: 'motd', effectiveValue: '', effectiveSource: 'file', editable: false,
              group: '展示与容量', description: '服务器描述', type: 'string', previewError: '节点离线，无法读取文件',
            },
          ],
        }),
      ),
    )
    let captured: unknown = null
    server.use(
      http.put(API('/instances/1/configs/surface'), async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ items: [] })
      }),
    )
    const { user } = renderPanel()

    const row = await screen.findByTestId('config-surface-row-props.motd')
    await user.click(within(row).getByRole('button', { name: '内联' }))
    await user.click(screen.getByTestId('config-surface-save'))

    // 不发送 inlineValue（置 nil），后端 file→inline 迁移读取文件现值兜底。
    await waitFor(() =>
      expect(captured).toEqual({ items: [{ itemKey: 'props.motd', source: 'inline' }] }),
    )
  })
})
